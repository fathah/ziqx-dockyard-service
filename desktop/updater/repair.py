"""Allowlisted, reviewed server repairs. No commands or file paths from the UI."""
import fcntl
from contextlib import closing
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time

CONFIG = Path('/etc/dockyard/config.json')
LIMIT = 2 * 1024 * 1024
ACTIONS = {'caddy_admin', 'caddy_import', 'caddy_sites', 'caddy_coherence',
           'caddy_write', 'caddy_capability', 'caddy_service', 'docker', 'docker_compose'}


def trusted(path, directory=False, allow_mode=False, allow_parent=None):
    path = Path(path)
    # Never follow a symlink in any parent, including custom configured paths.
    for parent in [*reversed(path.parents), path]:
        st = parent.lstat()
        if stat.S_ISLNK(st.st_mode) or st.st_uid != 0 or (st.st_mode & 0o022 and not ((allow_mode and parent == path) or parent == allow_parent)):
            raise ValueError('A configured repair path has unsafe ownership or permissions.')
    st = path.lstat()
    if not (stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode)):
        raise ValueError('A configured repair path has an unexpected file type.')


def read(path):
    trusted(path)
    data = Path(path).read_bytes()
    if len(data) > LIMIT:
        raise ValueError('A configured file exceeds the repair size limit.')
    return data


def run(args, cwd=None, timeout=30):
    try:
        # Match the daemon's Caddy adaptation environment, not root's login env.
        env = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'HOME': '/nonexistent'} if Path(args[0]).name == 'caddy' else {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'HOME': '/root', 'DEBIAN_FRONTEND': 'noninteractive'}
        result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.DEVNULL, timeout=timeout, cwd=cwd, env=env)
        return result.returncode == 0, result.stdout[:LIMIT]
    except (OSError, subprocess.TimeoutExpired):
        return False, b''


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=5)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def live(admin):
    connection = UnixHTTP(admin)
    try:
        connection.request('GET', '/config/')
        response = connection.getresponse()
        data = response.read(LIMIT + 1)
        if response.status != 200 or len(data) > LIMIT:
            raise ValueError('Caddy did not return a valid live configuration.')
        value = json.loads(data)
        if not isinstance(value, dict):
            raise ValueError('Caddy did not return a valid live configuration.')
        return value
    finally:
        connection.close()


def legacy_live():
    # Inspect only the loopback default endpoint, and prove its listener belongs
    # to the Caddy service before sending even a read request.
    ok, pid = run(['systemctl', 'show', 'caddy', '--property=MainPID', '--value'])
    pid = pid.decode().strip()
    listening, output = run(['ss', '-ltnpH', 'sport = :2019'])
    if not ok or not pid.isdecimal() or int(pid) < 1 or not listening or not any(
            '127.0.0.1:2019' in line and 'pid=' + pid + ',' in line for line in output.decode().splitlines()):
        return None
    connection = http.client.HTTPConnection('127.0.0.1', 2019, timeout=5)
    try:
        connection.request('GET', '/config/', headers={'Host': 'localhost:2019'})
        response = connection.getresponse()
        data = response.read(LIMIT + 1)
        if response.status != 200 or len(data) > LIMIT:
            return None
        value = json.loads(data)
        return value if isinstance(value, dict) else None
    finally:
        connection.close()


def hosts(value):
    result = set()
    if isinstance(value, dict):
        for key, child in value.items():
            if key == 'host' and isinstance(child, list):
                result.update(s for s in child if isinstance(s, str) and re.fullmatch(r'[a-zA-Z0-9.*-]{1,253}', s))
            result.update(hosts(child))
    elif isinstance(value, list):
        for child in value:
            result.update(hosts(child))
    return result


def idle(config):
    database = Path(config.get('state_dir', '/var/lib/dockyard')) / 'state.db'
    trusted(database)
    with closing(sqlite3.connect(database.as_uri() + '?mode=ro', uri=True, timeout=5)) as db:
        if db.execute("SELECT count(*) FROM jobs WHERE status IN ('queued','running','recovery_required')").fetchone()[0]:
            raise ValueError('Finish or reconcile active operations in Deployments before repairing the server.')
        projects = [json.loads(row[0]) for row in db.execute('SELECT data FROM projects ORDER BY id')]
    return projects


def admin_option(source, admin):
    """Change only a simple global admin option; preserve every site body."""
    if not re.fullmatch(r'/[a-zA-Z0-9_./-]+', admin):
        raise ValueError('The configured private socket path cannot be repaired automatically.')
    option = 'admin unix/' + admin + '|0600'
    lines = source.splitlines(keepends=True)
    first = next((i for i, line in enumerate(lines) if line.strip() and not line.lstrip().startswith('#')), len(lines))
    if first == len(lines) or lines[first].strip() != '{':
        # A global block on one line is deliberately rejected, not duplicated.
        if first < len(lines) and lines[first].lstrip().startswith('{'):
            raise ValueError('Expand the global Caddy options block before repairing its private connection.')
        return '{\n\t' + option + '\n}\n' + source
    depth = 1
    end = None
    found = False
    for i in range(first + 1, len(lines)):
        text = lines[i].split('#', 1)[0].strip()
        if depth == 1 and re.match(r'^admin(?:\s|$)', text):
            if '{' in text or found:
                raise ValueError('The custom Caddy admin policy needs review before replacement.')
            lines[i] = '\t' + option + '\n'
            found = True
        depth += text.count('{') - text.count('}')
        if depth == 0:
            end = i
            break
    if end is None:
        raise ValueError('The saved global Caddy options block is invalid.')
    if not found:
        lines.insert(first + 1, '\t' + option + '\n')
    return ''.join(lines)


def snippet(project):
    domains = project.get('domains') or []
    routes = project.get('service_routes') or []
    result = ''
    port = project.get('green_port') if project.get('active_slot') == 'green' else project.get('blue_port')
    instance = (project.get('service_instances') or {}).get(project.get('route_service'), {})
    port = instance.get('port') or port
    instances = project.get('service_instances') or {}
    routed = {name: (instances.get(route['service'], {}).get('port') or route['host_port']) for route in routes for name in route['domains']}
    groups = [([name], routed.get(name, port)) for name in domains]
    templates = {route['domain']: route for route in project.get('caddy_routes') or []}
    for names, upstream in groups:
        for name in names:
            if not re.fullmatch(r'[a-z0-9.-]{1,253}', name):
                raise ValueError('A recorded route has an invalid domain.')
            imported = templates.get(name)
            result += (imported['address'] if imported else name) + ' {\n'
            if project.get('state') != 'running':
                result += '\theader Retry-After 30\n\trespond "Application unavailable" 503\n'
            elif not isinstance(upstream, int) or not 1 <= upstream <= 65535:
                raise ValueError('A recorded route has no verified upstream port.')
            else:
                if imported:
                    body = imported['body'].replace('{dockyard.upstream}', '127.0.0.1:' + str(upstream))
                    result += body + ('' if body.endswith('\n') else '\n')
                else:
                    result += '\treverse_proxy 127.0.0.1:' + str(upstream) + '\n'
            result += '}\n'
    return result.encode()


def caddy_value(value):
    if not isinstance(value, str) or any(ord(c) < 32 for c in value):
        raise ValueError('a value that cannot be represented losslessly')
    return json.dumps(value, ensure_ascii=False)


def header_rules(operations, directive):
    if not isinstance(operations, dict) or set(operations) - {'set', 'add', 'delete', 'deferred'}:
        raise ValueError('custom header operations')
    lines = []
    for operation, prefix in [('set', ''), ('add', '+')]:
        for name, values in operations.get(operation, {}).items():
            if not re.fullmatch(r'[A-Za-z0-9!#$%&\'*+.^_`|~-]+', name) or not isinstance(values, list) or not values or (operation == 'set' and len(values) != 1):
                raise ValueError('custom header values')
            lines.extend(directive + ' ' + caddy_value(prefix + name) + ' ' + caddy_value(value) for value in values)
    for name in operations.get('delete', []):
        if not isinstance(name, str) or not re.fullmatch(r'[A-Za-z0-9!#$%&\'*+.^_`|~-]+', name):
            raise ValueError('custom header names')
        lines.append(directive + ' ' + caddy_value('-' + name))
    if operations.get('deferred'):
        if directive != '':
            raise ValueError('deferred proxy headers')
        lines.append('defer')
    return [line.lstrip() for line in lines]


def recovery_fields(value, allowed, label):
    if not isinstance(value, dict):
        raise ValueError(label + ' has invalid settings')
    unknown = set(value) - set(allowed)
    if unknown:
        # Report schema names, never private values or arbitrary object keys.
        names = sorted(key if isinstance(key, str) and re.fullmatch(r'[a-z_@][a-z0-9_@]{0,63}', key) else 'unknown field' for key in unknown)
        raise ValueError(label + ' unsupported fields: ' + ', '.join(names))


def recovery_number(value, label, duration=False):
    if type(value) is not int:
        raise ValueError(label + ' has an invalid numeric setting')
    return str(value) + ('ns' if duration else '')


def recovery_list(value, label):
    if not isinstance(value, list) or not value:
        raise ValueError(label + ' has an invalid list setting')
    return ' '.join(caddy_value(item) for item in value)


def recovery_policy(policy, depth=0):
    if depth > 8:
        raise ValueError('reverse_proxy load balancing nesting')
    recovery_fields(policy, {'policy', 'choose', 'weights', 'field', 'key', 'name', 'secret', 'max_age', 'fallback'}, 'reverse_proxy selection_policy')
    kind = policy.get('policy')
    fields = {
        'random': (), 'least_conn': (), 'round_robin': (), 'first': (),
        'ip_hash': (), 'client_ip_hash': (), 'uri_hash': (),
        'random_choose': ('choose',), 'weighted_round_robin': ('weights',),
        'header': ('field', 'fallback'), 'query': ('key', 'fallback'),
        'cookie': ('name', 'secret', 'max_age', 'fallback'),
    }
    if kind not in fields:
        raise ValueError('reverse_proxy unsupported selection_policy module')
    recovery_fields(policy, {'policy', *fields[kind]}, 'reverse_proxy selection_policy')
    result = kind
    if kind == 'random_choose':
        result += ' ' + recovery_number(policy.get('choose'), 'reverse_proxy choose')
    elif kind == 'weighted_round_robin':
        weights = policy.get('weights')
        if not isinstance(weights, list) or not weights:
            raise ValueError('reverse_proxy weights')
        result += ' ' + ' '.join(recovery_number(weight, 'reverse_proxy weight') for weight in weights)
    elif kind in ('header', 'query'):
        result += ' ' + caddy_value(policy.get('field' if kind == 'header' else 'key'))
    elif kind == 'cookie':
        if 'name' in policy or 'secret' in policy:
            result += ' ' + caddy_value(policy.get('name', ''))
        if 'secret' in policy:
            result += ' ' + caddy_value(policy['secret'])
    options = []
    if 'max_age' in policy:
        options.append('max_age ' + recovery_number(policy['max_age'], 'reverse_proxy cookie max_age', True))
    if 'fallback' in policy:
        options.append('fallback ' + recovery_policy(policy['fallback'], depth + 1))
    return result + (' {\n' + '\n'.join(options) + '\n}' if options else '')


def recovery_matchers(match):
    recovery_fields(match, {'method', 'path', 'host', 'header', 'expression'}, 'reverse_proxy retry_match')
    lines = []
    for field in ('method', 'path', 'host'):
        if field in match:
            lines.append(field + ' ' + recovery_list(match[field], 'reverse_proxy retry_match ' + field))
    if 'header' in match:
        if not isinstance(match['header'], dict):
            raise ValueError('reverse_proxy retry_match header')
        for name, values in match['header'].items():
            lines.append('header ' + caddy_value(name) + ' ' + recovery_list(values, 'reverse_proxy retry_match header'))
    if 'expression' in match:
        expression = match['expression']
        if isinstance(expression, dict):
            recovery_fields(expression, {'expr'}, 'reverse_proxy retry_match expression')
            expression = expression.get('expr')
        lines.append('expression ' + caddy_value(expression))
    return lines


def recovery_proxy_options(handler):
    durations = ('flush_interval', 'stream_timeout', 'stream_close_delay')
    sizes = ('request_buffers', 'response_buffers', 'stream_buffer_size')
    recovery_fields(handler, {'handler', 'upstreams', 'headers', 'transport', 'load_balancing', 'health_checks', 'trusted_proxies', 'rewrite', 'verbose_logs', 'handle_response', *durations, *sizes}, 'reverse_proxy')
    rules = []
    for field in durations:
        if field in handler:
            rules.append(field + ' ' + recovery_number(handler[field], 'reverse_proxy ' + field, True))
    for field in sizes:
        if field in handler:
            value = handler[field]
            rules.append(field + ' ' + ('unlimited' if type(value) is int and value == -1 else recovery_number(value, 'reverse_proxy ' + field)))
    if 'verbose_logs' in handler:
        if handler['verbose_logs'] is not True:
            raise ValueError('reverse_proxy verbose_logs')
        rules.append('verbose_logs')
    if 'trusted_proxies' in handler:
        rules.append('trusted_proxies ' + recovery_list(handler['trusted_proxies'], 'reverse_proxy trusted_proxies'))
    if 'rewrite' in handler:
        rewrite = handler['rewrite']
        recovery_fields(rewrite, {'method', 'uri'}, 'reverse_proxy rewrite')
        for field, directive in [('method', 'method'), ('uri', 'rewrite')]:
            if field in rewrite:
                rules.append(directive + ' ' + caddy_value(rewrite[field]))
    if 'load_balancing' in handler:
        balancing = handler['load_balancing']
        recovery_fields(balancing, {'selection_policy', 'retries', 'try_duration', 'try_interval', 'retry_match'}, 'reverse_proxy load_balancing')
        if 'selection_policy' in balancing:
            rules.append('lb_policy ' + recovery_policy(balancing['selection_policy']))
        for field in ('retries', 'try_duration', 'try_interval'):
            if field in balancing:
                rules.append('lb_' + field + ' ' + recovery_number(balancing[field], 'reverse_proxy ' + field, field != 'retries'))
        if 'retry_match' in balancing:
            if not isinstance(balancing['retry_match'], list) or not balancing['retry_match']:
                raise ValueError('reverse_proxy retry_match')
            for match in balancing['retry_match']:
                rules.append('lb_retry_match {\n' + '\n'.join(recovery_matchers(match)) + '\n}')
    if 'health_checks' in handler:
        checks = handler['health_checks']
        recovery_fields(checks, {'active', 'passive'}, 'reverse_proxy health_checks')
        mappings = {
            'active': {
                'path': ('health_path', 'text'), 'uri': ('health_uri', 'text'),
                'upstream': ('health_upstream', 'text'), 'port': ('health_port', 'number'),
                'method': ('health_method', 'text'), 'body': ('health_request_body', 'text'),
                'interval': ('health_interval', 'duration'), 'timeout': ('health_timeout', 'duration'),
                'passes': ('health_passes', 'number'), 'fails': ('health_fails', 'number'),
                'expect_status': ('health_status', 'number'), 'expect_body': ('health_body', 'text'),
            },
            'passive': {
                'fail_duration': ('fail_duration', 'duration'), 'max_fails': ('max_fails', 'number'),
                'unhealthy_request_count': ('unhealthy_request_count', 'number'),
                'unhealthy_latency': ('unhealthy_latency', 'duration'),
            },
        }
        for kind, mapping in mappings.items():
            if kind not in checks:
                continue
            settings = checks[kind]
            extra = {'headers', 'follow_redirects'} if kind == 'active' else {'unhealthy_status'}
            recovery_fields(settings, {*mapping, *extra}, 'reverse_proxy health_checks.' + kind)
            for field, (directive, value_kind) in mapping.items():
                if field in settings:
                    value = settings[field]
                    rules.append(directive + ' ' + (caddy_value(value) if value_kind == 'text' else recovery_number(value, 'reverse_proxy health_checks.' + field, value_kind == 'duration')))
            if 'follow_redirects' in settings:
                if settings['follow_redirects'] is not True:
                    raise ValueError('reverse_proxy health follow_redirects')
                rules.append('health_follow_redirects')
            if 'headers' in settings:
                headers = settings['headers']
                if not isinstance(headers, dict):
                    raise ValueError('reverse_proxy health headers')
                rules.append('health_headers {\n' + '\n'.join(caddy_value(name) + ' ' + recovery_list(values, 'reverse_proxy health headers') for name, values in headers.items()) + '\n}')
            if 'unhealthy_status' in settings:
                statuses = settings['unhealthy_status']
                if not isinstance(statuses, list) or not statuses:
                    raise ValueError('reverse_proxy unhealthy_status')
                rules.append('unhealthy_status ' + ' '.join(recovery_number(status, 'reverse_proxy unhealthy_status') for status in statuses))
    if 'handle_response' in handler:
        responses = handler['handle_response']
        if not isinstance(responses, list) or not responses:
            raise ValueError('reverse_proxy handle_response')
        for index, response in enumerate(responses):
            recovery_fields(response, {'match', 'status_code', 'routes'}, 'reverse_proxy handle_response')
            matcher = ''
            if 'match' in response:
                match = response['match']
                recovery_fields(match, {'status_code', 'headers'}, 'reverse_proxy response matcher')
                matcher = '@dockyard_response_' + str(index)
                matches = []
                if 'status_code' in match:
                    matches.append('status ' + ' '.join(recovery_number(status, 'reverse_proxy response status') for status in match['status_code']))
                for name, values in match.get('headers', {}).items():
                    matches.append('header ' + caddy_value(name) + ' ' + recovery_list(values, 'reverse_proxy response header'))
                rules.append(matcher + ' {\n' + '\n'.join(matches) + '\n}')
            if 'routes' in response:
                if 'status_code' in response:
                    raise ValueError('reverse_proxy combined response routes and status')
                body = recover_handlers([{'handler': 'subroute', 'routes': response['routes']}])
                rules.append('handle_response ' + matcher + ' {\nroute {\n' + '\n'.join(body) + '\n}\n}')
            elif 'status_code' in response:
                status = response['status_code']
                if not str(status).isdigit() or not 100 <= int(status) <= 599:
                    raise ValueError('reverse_proxy replacement status')
                rules.append('replace_status ' + matcher + ' ' + str(status))
            else:
                raise ValueError('reverse_proxy empty handle_response')
    return rules


def recover_handlers(handlers):
    """Render known handlers in their existing order; never guess unknown fields."""
    if not isinstance(handlers, list) or not handlers:
        raise ValueError('an empty or invalid handler chain')
    lines = []
    for handler in handlers:
        if not isinstance(handler, dict):
            raise ValueError('an invalid handler')
        kind = handler.get('handler', 'unknown')
        if kind == 'subroute' and set(handler) == {'handler', 'routes'}:
            for route in handler['routes']:
                if not isinstance(route, dict) or set(route) != {'handle'}:
                    raise ValueError('matched or grouped routes')
                lines.extend(recover_handlers(route['handle']))
        elif kind == 'reverse_proxy':
            rules = recovery_proxy_options(handler)
            upstreams = handler.get('upstreams')
            if not isinstance(upstreams, list) or not upstreams or any(not isinstance(upstream, dict) or set(upstream) != {'dial'} for upstream in upstreams):
                raise ValueError('custom proxy upstreams')
            headers = handler.get('headers', {})
            if not isinstance(headers, dict) or set(headers) - {'request', 'response'}:
                raise ValueError('custom proxy headers')
            for direction, directive in [('request', 'header_up'), ('response', 'header_down')]:
                rules.extend(header_rules(headers.get(direction, {}), directive))
            if 'transport' in handler:
                transport = handler['transport']
                timeouts = ('dial_timeout', 'dial_fallback_delay', 'response_header_timeout', 'expect_continue_timeout', 'read_timeout', 'write_timeout')
                recovery_fields(transport, {'protocol', 'tls', 'versions', 'keep_alive', 'compression', 'max_conns_per_host', *timeouts}, 'reverse_proxy transport')
                if transport.get('protocol') != 'http':
                    raise ValueError('reverse_proxy unsupported transport module')
                settings = []
                if 'tls' in transport:
                    tls = transport['tls']
                    recovery_fields(tls, {'server_name', 'insecure_skip_verify'}, 'reverse_proxy upstream TLS')
                    settings.append('tls')
                    if 'server_name' in tls:
                        settings.append('tls_server_name ' + caddy_value(tls['server_name']))
                    if tls.get('insecure_skip_verify'):
                        settings.append('tls_insecure_skip_verify')
                if 'versions' in transport:
                    settings.append('versions ' + ' '.join(caddy_value(version) for version in transport['versions']))
                for field in timeouts:
                    if field in transport:
                        settings.append(field + ' ' + recovery_number(transport[field], 'reverse_proxy ' + field, True))
                if 'keep_alive' in transport:
                    keepalive = transport['keep_alive']
                    recovery_fields(keepalive, {'enabled', 'probe_interval', 'idle_timeout', 'max_idle_conns', 'max_idle_conns_per_host'}, 'reverse_proxy keep_alive')
                    if 'enabled' in keepalive:
                        if keepalive['enabled'] is not False:
                            raise ValueError('reverse_proxy keep_alive enabled')
                        settings.append('keepalive off')
                    for field, directive in [('probe_interval', 'keepalive_interval'), ('idle_timeout', 'keepalive'), ('max_idle_conns', 'keepalive_idle_conns'), ('max_idle_conns_per_host', 'keepalive_idle_conns_per_host')]:
                        if field in keepalive:
                            settings.append(directive + ' ' + recovery_number(keepalive[field], 'reverse_proxy keep_alive.' + field, field in ('probe_interval', 'idle_timeout')))
                if 'compression' in transport:
                    if transport['compression'] is not False:
                        raise ValueError('reverse_proxy transport compression')
                    settings.append('compression off')
                if 'max_conns_per_host' in transport:
                    settings.append('max_conns_per_host ' + recovery_number(transport['max_conns_per_host'], 'reverse_proxy max_conns_per_host'))
                rules.append('transport http {\n' + '\n'.join(settings) + '\n}')
            line = 'reverse_proxy ' + ' '.join(caddy_value(upstream['dial']) for upstream in upstreams)
            lines.append(line + (' {\n' + '\n'.join(rules) + '\n}' if rules else ''))
        elif kind == 'headers' and set(handler) == {'handler', 'response'}:
            lines.append('header {\n' + '\n'.join(header_rules(handler['response'], '')) + '\n}')
        elif kind == 'encode' and set(handler) <= {'handler', 'encodings', 'prefer', 'minimum_length'}:
            encodings = handler.get('encodings', {})
            if not encodings or any(name not in ('gzip', 'zstd') or options != {} for name, options in encodings.items()):
                raise ValueError('custom compression settings')
            order = handler.get('prefer', list(encodings))
            if not isinstance(order, list) or set(order) != set(encodings) or len(order) != len(encodings):
                raise ValueError('custom compression order')
            line = 'encode ' + ' '.join(order)
            if 'minimum_length' in handler:
                length = handler['minimum_length']
                if not isinstance(length, int) or length < 0:
                    raise ValueError('custom compression length')
                line += ' {\nminimum_length ' + str(length) + '\n}'
            lines.append(line)
        elif kind == 'authentication' and set(handler) == {'handler', 'providers'} and set(handler['providers']) == {'http_basic'}:
            basic = handler['providers']['http_basic']
            if not isinstance(basic, dict) or set(basic) - {'accounts', 'hash', 'realm', 'hash_cache'} or basic.get('hash') != {'algorithm': 'bcrypt'} or basic.get('hash_cache', {}) != {}:
                raise ValueError('custom authentication settings')
            accounts = basic.get('accounts', [])
            if not accounts or any(set(account) != {'username', 'password'} for account in accounts):
                raise ValueError('custom authentication accounts')
            line = 'basic_auth bcrypt' + (' ' + caddy_value(basic['realm']) if 'realm' in basic else '')
            lines.append(line + ' {\n' + '\n'.join(caddy_value(account['username']) + ' ' + caddy_value(account['password']) for account in accounts) + '\n}')
        elif kind == 'static_response' and set(handler) <= {'handler', 'body', 'status_code'}:
            status = handler.get('status_code', 200)
            if not str(status).isdigit() or not 100 <= int(status) <= 599:
                raise ValueError('custom response status')
            lines.append('respond ' + caddy_value(handler.get('body', '')) + ' ' + str(status))
        elif kind == 'static_response' and set(handler) == {'handler', 'headers', 'status_code'}:
            headers, status = handler['headers'], handler['status_code']
            values = headers.get('Location') if isinstance(headers, dict) else None
            if set(headers or {}) != {'Location'} or str(status) not in ('301', '302', '303', '307', '308') or not isinstance(values, list) or len(values) != 1 or not values[0].startswith(('http://', 'https://', '/')):
                raise ValueError('custom response headers')
            lines.append('redir ' + caddy_value(values[0]) + ' ' + str(status))
        else:
            raise ValueError(kind + ' rules')
    return lines


def live_only_sites(saved, current):
    """Recover known HTTPS site rules, with equivalence verified before reload."""
    missing = set(hosts(current)) - set(hosts(saved))
    if not missing:
        return '', []
    found = {}
    reasons = {}
    servers = ((current.get('apps') or {}).get('http') or {}).get('servers') or {}
    for server in servers.values():
        if server.get('listen') != [':443']:
            continue
        for route in server.get('routes') or []:
            match = route.get('match') or []
            if len(match) != 1 or set(match[0]) != {'host'} or set(route) - {'match', 'handle', 'terminal'}:
                continue
            try:
                rules = recover_handlers(route.get('handle'))
            except (ValueError, TypeError, KeyError, AttributeError) as error:
                for name in set(match[0]['host']) & missing:
                    reasons[name] = str(error) if isinstance(error, ValueError) else 'invalid site rules'
                continue
            for name in match[0]['host']:
                if name in missing:
                    if name in found or not re.fullmatch(r'[a-z0-9.-]{1,253}', name):
                        raise ValueError('Running sites have ambiguous routes. No saved configuration was activated.')
                    # An explicit route preserves handler order (auth, headers,
                    # compression, proxy) instead of the adapter re-sorting it.
                    found[name] = '\troute {\n' + '\n'.join('\t\t' + line.replace('\n', '\n\t\t') for line in rules) + '\n\t}\n'
    if set(found) != missing:
        details = '; '.join(name + ': ' + reasons.get(name, 'custom listener, matcher or route structure') for name in sorted(missing - set(found)))
        raise ValueError('These running-only sites need additional recovery: ' + details + '. Dockyard will not remove them; no configuration was activated.')
    return ''.join('\n' + name + ' {\n' + found[name] + '}\n' for name in sorted(found)), sorted(found)


def normalized_handlers(handlers):
    result = []
    for handler in handlers:
        # Flatten only unconditional wrappers. Matchers, groups and terminal
        # boundaries keep their original meaning and are never discarded.
        if isinstance(handler, dict) and set(handler) == {'handler', 'routes'} and handler['handler'] == 'subroute' and all(set(route) == {'handle'} for route in handler['routes']):
            for route in handler['routes']:
                result.extend(normalized_handlers(route['handle']))
        else:
            if isinstance(handler, dict) and handler.get('handler') == 'reverse_proxy' and isinstance(handler.get('handle_response'), list):
                handler = dict(handler)
                responses = []
                for response in handler['handle_response']:
                    response = dict(response)
                    routes = response.get('routes')
                    # Canonicalize only unconditional response routes. Their
                    # exact handler order survives; matcher/group/terminal
                    # boundaries are retained and compared unchanged.
                    if isinstance(routes, list) and routes and all(isinstance(route, dict) and set(route) == {'handle'} for route in routes):
                        response['routes'] = [{'handle': normalized_handlers([item for route in routes for item in route['handle']])}]
                    responses.append(response)
                handler['handle_response'] = responses
            result.append(handler)
    return result


def recovered_site(config, name):
    matches = []
    servers = ((config.get('apps') or {}).get('http') or {}).get('servers') or {}
    for server in servers.values():
        for route in server.get('routes', []):
            if name not in hosts(route.get('match', [])):
                continue
            match = route.get('match')
            if not isinstance(match, list) or len(match) != 1 or set(match[0]) != {'host'}:
                raise ValueError('A recovered site has custom matchers: ' + name + '.')
            # Compare the complete chain, terminal flag, listeners and server
            # options, without sibling hostnames or adapter wrapper nesting.
            entry = {key: value for key, value in server.items() if key != 'routes'}
            entry['route'] = {key: value for key, value in route.items() if key not in ('match', 'handle')}
            entry['handlers'] = normalized_handlers(route.get('handle', []))
            matches.append(entry)
    if len(matches) != 1:
        raise ValueError('A recovered site has ambiguous routes: ' + name + '.')
    policies = (((config.get('apps') or {}).get('tls') or {}).get('automation') or {}).get('policies', [])
    matches[0]['certificate_policies'] = []
    for policy in policies:
        subjects = policy.get('subjects')
        if subjects is None or name in subjects or any(subject.startswith('*.') and name.endswith(subject[1:]) for subject in subjects):
            matches[0]['certificate_policies'].append({key: value for key, value in policy.items() if key != 'subjects'})
    return matches[0]


def verify_recovered_sites(current, desired, names):
    for name in names:
        if recovered_site(current, name) != recovered_site(desired, name):
            raise ValueError('Dockyard could not preserve all active rules for ' + name + '. No configuration was activated.')


def preview_recovered_sites(binary, caddyfile, source, current, names):
    # Adapt a temporary sibling so relative imports retain the original base.
    # The real Caddyfile and the running server are unchanged during review.
    fd, path = tempfile.mkstemp(prefix='.dockyard-review-', dir=caddyfile.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(source)
        ok, adapted = run([binary, 'adapt', '--config', path, '--adapter', 'caddyfile'], cwd=str(caddyfile.parent))
        if not ok:
            raise ValueError('Caddy could not validate the recovered site rules. No configuration was activated.')
        verify_recovered_sites(current, json.loads(adapted), names)
    finally:
        Path(path).unlink(missing_ok=True)


def plan(action):
    if action not in ACTIONS:
        raise ValueError('This check does not have an automatic repair.')
    raw = read(CONFIG)
    config = json.loads(raw)
    projects = idle(config)
    caddyfile = Path(config.get('caddyfile', '/etc/caddy/Caddyfile'))
    sites = Path(config.get('caddy_sites', '/etc/caddy/dockyard'))
    admin = config.get('caddy_admin_socket', '/run/caddy/admin.sock')
    binary = config.get('caddy_binary', '/usr/bin/caddy')
    changes, files = [], {}
    snapshot = {'config': raw.hex(), 'projects': projects}
    if action.startswith('caddy_'):
        trusted(binary)
        source = read(caddyfile)
        snapshot['caddyfile'] = source.hex()
        if sites.exists():
            trusted(sites, True, allow_mode=action == 'caddy_sites')
            snapshot['sites_mode'] = stat.S_IMODE(sites.lstat().st_mode)
            for path in sorted(sites.glob('*.caddy')):
                trusted(path, allow_mode=action == 'caddy_sites', allow_parent=sites if action == 'caddy_sites' else None)
                data = path.read_bytes()
                if len(data) > LIMIT:
                    raise ValueError('A generated route exceeds the size limit.')
                snapshot[str(path)] = data.hex()
                snapshot[str(path) + ':mode'] = stat.S_IMODE(path.lstat().st_mode)
        if action == 'caddy_admin':
            updated = admin_option(source.decode(), admin).encode()
            if updated != source:
                files[caddyfile] = updated
                changes.append('Configure Caddy’s private admin socket for Dockyard.')
            if str(Path(admin).parent) == '/run/caddy':
                unit = Path('/etc/systemd/system/caddy.service.d/30-dockyard-admin.conf')
                if unit.exists():
                    snapshot['caddy_unit'] = read(unit).hex()
                files[unit] = b'[Service]\nRuntimeDirectory=caddy\nRuntimeDirectoryMode=0700\n'
                changes.append('Let Caddy create its private socket directory at startup.')
        if action == 'caddy_import':
            files[caddyfile] = source.rstrip() + ('\n\nimport ' + json.dumps(str(sites / '*.caddy')) + '\n').encode()
            changes.append('Load the Dockyard route directory from the saved Caddyfile.')
        if action in ('caddy_sites', 'caddy_import'):
            changes.append('Create the root-owned generated route directory if missing.')
        if action == 'caddy_sites':
            changes.append('Remove group and public write permissions from the route directory and its regular root-owned files.')
        if action == 'caddy_coherence':
            for project in projects:
                if not re.fullmatch(r'[a-z][a-z0-9-]{0,47}', project.get('id', '')):
                    raise ValueError('A recorded project identity is invalid.')
                expected = snippet(project)
                path = sites / (project['id'] + '.caddy')
                if expected and (not path.exists() or read(path) != expected):
                    files[path] = expected
                    changes.append('Restore the recorded generated routes for ' + project['id'] + '.')
        if action in ('caddy_write', 'caddy_capability'):
            unit = Path('/etc/systemd/system/dockyard.service.d/30-dockyard-routing.conf')
            if unit.exists():
                snapshot['unit'] = read(unit).hex()
            for path in (caddyfile.parent, sites):
                if not re.fullmatch(r'/[a-zA-Z0-9_./-]+', str(path)):
                    raise ValueError('The custom Caddy folders cannot be repaired automatically.')
            files[unit] = ('[Service]\nReadWritePaths=' + str(caddyfile.parent) + ' ' + str(sites) + '\nCapabilityBoundingSet=CAP_DAC_OVERRIDE\n').encode()
            changes.append('Grant Dockyard write access to its Caddy folders and access to the private socket. Dockyard restarts briefly.')
        try:
            current = live(admin)
            snapshot['live'] = current
        except (OSError, ValueError, http.client.HTTPException):
            current = None
            snapshot['live'] = None
        if action == 'caddy_admin' and current is None:
            try:
                snapshot['legacy_live'] = legacy_live()
            except (OSError, ValueError, http.client.HTTPException):
                snapshot['legacy_live'] = None
            current = snapshot['legacy_live']
            changes.append('Restart Caddy briefly to activate its private socket. All Caddy sites may pause during this restart.')
        if action == 'caddy_coherence' and current is None:
            raise ValueError('Repair the Private Caddy connection first, then review saved and live routes.')
        if action in ('caddy_import', 'caddy_sites') and current is None:
            raise ValueError('Repair the Private Caddy connection first, then prepare the route directory.')
        ok, adapted = run([binary, 'adapt', '--config', str(caddyfile), '--adapter', 'caddyfile'], cwd=str(caddyfile.parent))
        try:
            saved = json.loads(adapted) if ok else {}
        except ValueError:
            saved = {}
            ok = False
        if not isinstance(saved, dict):
            saved, ok = {}, False
        saved_available = ok
        if action not in ('caddy_write', 'caddy_capability'):
            changes.append('Validate and activate the saved Caddy configuration; verify the private connection and live routes.')
    else:
        current, saved = {}, {}
        saved_available = True
        changes.append('Start Docker if it is stopped.' if action == 'docker' else
                       'Install the Docker Compose plugin from the server’s configured Ubuntu package repositories.')
    snapshot['active'] = {name: run(['systemctl', 'is-active', '--quiet', name])[0] for name in ('dockyard', 'caddy', 'docker')}
    snapshot['files'] = {str(path): data.hex() for path, data in files.items()}
    digest = hashlib.sha256(json.dumps([action, snapshot], sort_keys=True).encode()).hexdigest()
    sections = {'admin': 'Admin connection', 'apps': 'Routing and application settings', 'logging': 'Logging', 'storage': 'Certificate storage'}
    changed = [label for key, label in sections.items() if (saved or {}).get(key) != (current or {}).get(key)]
    if {k: v for k, v in (saved or {}).items() if k not in sections} != {k: v for k, v in (current or {}).items() if k not in sections}:
        changed.append('Other Caddy settings')
    preserved = []
    if action == 'caddy_coherence' and saved_available and current is not None:
        addition, preserved = live_only_sites(saved, current)
        if addition:
            files[caddyfile] = files.get(caddyfile, source).rstrip() + addition.encode()
            preview_recovered_sites(binary, caddyfile, files[caddyfile], current, preserved)
            changes.insert(0, 'Restore these still-running sites to the saved Caddyfile before reload: ' + ', '.join(preserved) + '.')
            snapshot['files'] = {str(path): data.hex() for path, data in files.items()}
            digest = hashlib.sha256(json.dumps([action, snapshot], sort_keys=True).encode()).hexdigest()
    preview = {'action': action, 'review_sha256': digest, 'changes': changes, 'changed_sections': changed,
               'preserved_domains': preserved,
               'reloads_caddy': action.startswith('caddy_') and action not in ('caddy_write', 'caddy_capability'),
               'saved_available': saved_available, 'live_available': current is not None,
               'saved_domains': sorted(hosts(saved)), 'live_domains': sorted(hosts(current))}
    return preview, config, files, snapshot


def atomic(path, data, mode=0o644):
    path = Path(path)
    trusted(path.parent, True)
    fd, name = tempfile.mkstemp(prefix='.dockyard-repair-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
    finally:
        Path(name).unlink(missing_ok=True)


def apply(action, expected):
    trusted(CONFIG.parent, True)
    fd = os.open(CONFIG.parent / 'repair.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as lock:
        st = os.fstat(lock.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o077:
            raise ValueError('The repair lock has unsafe permissions.')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        preview, config, files, snapshot = plan(action)
        if preview['review_sha256'] != expected:
            raise ValueError('Server settings or live routes changed. Review the repair again.')
        stopped = False
        # Stop admissions and the worker before changing shared route state.
        if snapshot['active']['dockyard']:
            if not run(['systemctl', 'stop', 'dockyard'])[0]:
                raise ValueError('Could not pause Dockyard for the repair.')
            stopped = True
        originals = {}
        backup = None
        try:
            idle(config)
            # Recheck the reviewed state after stopping admissions.
            reviewed, _, _, after = plan(action)
            after['active']['dockyard'] = snapshot['active']['dockyard']
            if after != snapshot:
                raise ValueError('Server settings changed while pausing. Review the repair again.')
            if action == 'docker':
                if not run(['systemctl', 'start', 'docker'])[0]:
                    raise ValueError('Docker could not be started. Check its service in Dockyard.')
            elif action == 'docker_compose':
                if not run(['apt-get', 'install', '-y', '--no-upgrade', '--no-install-recommends', 'docker-compose-plugin'], timeout=180)[0]:
                    raise ValueError('The configured repositories could not install docker-compose-plugin.')
            else:
                sites = Path(config.get('caddy_sites', '/etc/caddy/dockyard'))
                if not sites.exists():
                    trusted(sites.parent, True)
                    sites.mkdir(mode=0o755)
                trusted(sites, True, allow_mode=action == 'caddy_sites')
                folder = CONFIG.parent / 'repair-backups'
                if not folder.exists():
                    folder.mkdir(mode=0o700)
                trusted(folder, True)
                backup = Path(tempfile.mkdtemp(prefix=time.strftime('%Y%m%d-%H%M%S-'), dir=folder))
                atomic(backup / 'evidence.json', json.dumps(snapshot).encode(), 0o600)
                if action == 'caddy_sites':
                    os.chmod(sites, 0o755)
                    for path in sites.glob('*.caddy'):
                        trusted(path, allow_mode=True)
                        os.chmod(path, 0o644)
                for path, data in files.items():
                    if not path.parent.exists() and str(path.parent) in ('/etc/systemd/system/dockyard.service.d', '/etc/systemd/system/caddy.service.d'):
                        trusted(path.parent.parent, True)
                        path.parent.mkdir(mode=0o755)
                    originals[path] = read(path) if path.exists() else None
                    atomic(path, data)
                if any(str(path).startswith('/etc/systemd/system/') for path in files):
                    if not run(['systemctl', 'daemon-reload'])[0]:
                        raise ValueError('Could not load the reviewed service settings.')
                if action in ('caddy_write', 'caddy_capability'):
                    return {'repaired': True, 'backup': str(backup)}
                binary = config.get('caddy_binary', '/usr/bin/caddy')
                caddyfile = config.get('caddyfile', '/etc/caddy/Caddyfile')
                args = ['--config', caddyfile, '--adapter', 'caddyfile']
                if not run([binary, 'validate', *args], cwd=str(Path(caddyfile).parent))[0]:
                    raise ValueError('Caddy validation failed. The previous saved files were restored.')
                ok, data = run([binary, 'adapt', *args], cwd=str(Path(caddyfile).parent))
                desired = json.loads(data) if ok else None
                if desired is None:
                    raise ValueError('Caddy could not adapt the reviewed configuration.')
                running = snapshot.get('live') or snapshot.get('legacy_live')
                if running is not None and set(hosts(running)) - set(hosts(desired)):
                    raise ValueError('This repair would remove existing running sites. The previous saved files were restored; Caddy was not reloaded.')
                if preview.get('preserved_domains'):
                    verify_recovered_sites(running, desired, preview['preserved_domains'])
                admin = config.get('caddy_admin_socket', '/run/caddy/admin.sock')
                if not snapshot['active']['caddy']:
                    ok = run(['systemctl', 'start', 'caddy'])[0]
                    if action == 'caddy_service' and ok and run(['systemctl', 'is-active', '--quiet', 'caddy'])[0]:
                        return {'repaired': True, 'backup': str(backup)}
                elif action == 'caddy_admin' and snapshot['live'] is None:
                    # A reload cannot reach a socket that does not exist yet.
                    ok = run(['systemctl', 'restart', 'caddy'])[0]
                else:
                    ok = run([binary, 'reload', *args, '--address', 'unix/' + admin], cwd=str(Path(caddyfile).parent))[0]
                for _ in range(5):
                    try:
                        observed = live(admin)
                        if observed == desired:
                            return {'repaired': True, 'backup': str(backup)}
                        if observed != snapshot['live']:
                            originals = {}  # Ambiguous outcome: preserve evidence and saved intent.
                            raise ValueError('Caddy loaded a different configuration. The saved intent and backup were preserved; run checks before retrying.')
                    except (OSError, http.client.HTTPException):
                        pass
                    time.sleep(1)
                originals = {}  # A timeout is not proof that the reload failed.
                raise ValueError('The reload outcome could not be verified. Saved intent and backup were preserved. Run checks before retrying.')
            return {'repaired': True}
        except Exception:
            for path, original in originals.items():
                if original is None:
                    path.unlink(missing_ok=True)
                else:
                    atomic(path, original)
            if any(str(path).startswith('/etc/systemd/system/') for path in files):
                run(['systemctl', 'daemon-reload'])
            raise
        finally:
            if stopped and not run(['systemctl', 'start', 'dockyard'])[0]:
                raise ValueError('The repair finished but Dockyard could not restart. Retry the server check.')


def main(action, expected=''):
    if os.geteuid() != 0:
        raise ValueError('Repairs require the enrolled server’s root SSH connection.')
    return apply(action, expected) if expected else plan(action)[0]


def result_path(expected):
    if not re.fullmatch(r'[a-f0-9]{64}', expected):
        raise ValueError('Invalid repair review.')
    folder = CONFIG.parent / 'repair-results'
    if not folder.exists():
        trusted(CONFIG.parent, True)
        folder.mkdir(mode=0o700)
    trusted(folder, True)
    return folder / (expected + '.json')


def launch(action, expected):
    preview, _, _, snapshot = plan(action)
    if preview['review_sha256'] != expected:
        raise ValueError('Server settings changed. Review the repair again.')
    path = result_path(expected)
    unit = 'dockyard-repair-' + expected[:24]
    if run(['systemctl', 'is-active', '--quiet', unit])[0]:
        return {'running': True}
    path.unlink(missing_ok=True)
    # Independent of the SSH channel: disconnecting cannot abandon a paused daemon.
    cleanup = ['--property=ExecStopPost=/bin/systemctl start dockyard'] if snapshot['active']['dockyard'] else []
    ok, _ = run(['systemd-run', '--quiet', '--collect', '--unit=' + unit,
                 '--property=RuntimeMaxSec=300', *cleanup, '/usr/bin/python3', '-c', SOURCE,
                 'worker', action, expected])
    if not ok:
        raise ValueError('Could not start the reviewed repair on the server.')
    return {'running': True}


def status(expected):
    path = result_path(expected)
    return json.loads(read(path)) if path.exists() else {'running': True}


def entry(args):
    if args[0] == 'launch':
        return launch(*args[1:])
    if args[0] == 'status':
        return status(args[1])
    if args[0] == 'worker':
        path = result_path(args[2])
        try:
            result = main(*args[1:])
        except ValueError as error:
            result = {'error': str(error)}
        except Exception:
            result = {'error': 'The repair could not finish. Run server checks again to inspect its outcome.'}
        atomic(path, json.dumps(result).encode(), 0o600)
        return result
    return main(*args)


if __name__ == '__main__':
    try:
        print(json.dumps(entry(sys.argv[1:])))
    except ValueError as error:
        print(json.dumps({'error': str(error)}))
    except Exception:
        print(json.dumps({'error': 'The repair could not finish. Run server checks again to inspect its outcome.'}))
