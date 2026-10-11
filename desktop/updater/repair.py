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
    routed = {name: route['host_port'] for route in routes for name in route['domains']}
    groups = [([name], routed.get(name, port)) for name in domains]
    for names, upstream in groups:
        for name in names:
            if not re.fullmatch(r'[a-z0-9.-]{1,253}', name):
                raise ValueError('A recorded route has an invalid domain.')
            result += name + ' {\n'
            if project.get('state') != 'running':
                result += '\theader Retry-After 30\n\trespond "Application unavailable" 503\n'
            elif not isinstance(upstream, int) or not 1 <= upstream <= 65535:
                raise ValueError('A recorded route has no verified upstream port.')
            else:
                result += '\treverse_proxy 127.0.0.1:' + str(upstream) + '\n'
            result += '}\n'
    return result.encode()


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
    preview = {'action': action, 'review_sha256': digest, 'changes': changes, 'changed_sections': changed,
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
