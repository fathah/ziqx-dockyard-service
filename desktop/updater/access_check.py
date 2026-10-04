"""Read-only server diagnostics. Return summaries, never config or raw logs."""
from contextlib import closing
import glob
import http.client
import json
import os
from pathlib import Path
import re
import shlex
import socket
import sqlite3
import stat
import subprocess
import sys

CONFIG = Path('/etc/dockyard/config.json')
LIMIT = 2 * 1024 * 1024


def info(path):
    try:
        st = os.lstat(path)
        kind = 'directory' if stat.S_ISDIR(st.st_mode) else 'regular' if stat.S_ISREG(st.st_mode) else 'other'
        return {'exists': True, 'kind': kind, 'uid': st.st_uid, 'mode': stat.S_IMODE(st.st_mode)}
    except OSError:
        return {'exists': False, 'kind': 'missing', 'uid': -1, 'mode': 0}


def run(args, timeout=5, cwd=None):
    try:
        result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.DEVNULL, timeout=timeout, cwd=cwd)
        return result.returncode == 0 and len(result.stdout) <= LIMIT, result.stdout[:LIMIT]
    except (OSError, subprocess.TimeoutExpired):
        return False, b''


def inspect_adopted(config, project):
    """Read only Docker identity/state metadata; never fetch environment values."""
    adoption = project.get('adoption') or {}
    if not isinstance(adoption, dict) or not isinstance(adoption.get('compose_project'), str) or not isinstance(adoption.get('source_name'), str):
        return None
    if not all(isinstance(adoption.get(field), list) and all(isinstance(value, str) for value in adoption[field]) for field in ('config_files', 'container_ids')):
        return None
    docker = config.get('docker_binary', 'docker')
    name = adoption.get('compose_project', '')
    ok, output = run([docker, 'ps', '-aq', '--no-trunc', '--filter', 'label=com.docker.compose.project=' + name])
    ids = output.decode('utf-8', errors='replace').split()
    if not ok or not ids or len(ids) > 1000 or any(not re.fullmatch('[a-f0-9]{64}', id) for id in ids):
        return None
    fields = {'id': '.Id', 'running': '.State.Running', 'compose': '(index .Config.Labels "com.docker.compose.project")',
              'server': '(index .Config.Labels "io.ziqx.dockyard.server")', 'project': '(index .Config.Labels "io.ziqx.dockyard.project")',
              'folder': '(index .Config.Labels "com.docker.compose.project.working_dir")',
              'files': '(index .Config.Labels "com.docker.compose.project.config_files")'}
    template = '{' + ','.join(json.dumps(key) + ':{{json ' + value + '}}' for key, value in fields.items()) + '}'
    ok, output = run([docker, 'inspect', '--type', 'container', '--format', template, *ids])
    if not ok:
        return None
    try:
        containers = [json.loads(line) for line in output.decode('utf-8').splitlines()]
        if len(containers) != len(ids) or {item['id'] for item in containers} != set(ids):
            return None
        known = set(adoption.get('container_ids', []))
        folder = os.path.join(config.get('projects_root', '/docker'), adoption.get('source_name', ''))
        files = ','.join(adoption.get('config_files', []))
        def owned(item):
            if item.get('compose') != name:
                return False
            if config.get('server_id') and item.get('server') == config['server_id'] and item.get('project') == project['id']:
                return True
            return not item.get('server') and not item.get('project') and item['id'] in known and item.get('folder') == folder and item.get('files') == files
        return all(owned(item) for item in containers), any(item.get('running') is True for item in containers)
    except (ValueError, KeyError, TypeError, UnicodeError):
        return None


def read_json(path):
    with open(path, 'rb') as source:
        data = source.read(LIMIT + 1)
    if len(data) > LIMIT:
        raise ValueError('size limit')
    return json.loads(data)


def managed_import(path, sites, seen=None):
    """Recognize a top-level directory glob, including imports of other files.

    A comment, snippet definition, or site-local import does not enable routes.
    Specific filenames do not cover future project route files.
    """
    seen = set() if seen is None else seen
    path = Path(path).resolve()
    if path in seen or len(seen) >= 64:
        return False
    seen.add(path)
    with path.open() as source:
        text = source.read(LIMIT + 1)
    if len(text) > LIMIT:
        raise ValueError('size limit')
    depth = 0
    for line in text.splitlines():
        lexer = shlex.shlex(line, posix=True, punctuation_chars='{}')
        lexer.whitespace_split = True
        tokens = list(lexer)
        if depth == 0 and len(tokens) == 2 and tokens[0] == 'import':
            target = tokens[1]
            if not os.path.isabs(target):
                target = str(path.parent / target)
            parent, pattern = os.path.split(os.path.normpath(target))
            if Path(parent).resolve() == Path(sites).resolve() and pattern in ('*.caddy', '*'):
                return True
            for included in sorted(glob.glob(target))[:64]:
                if Path(included).is_file() and managed_import(included, sites, seen):
                    return True
        for token in tokens:
            if set(token) <= {'{', '}'}:
                depth += token.count('{') - token.count('}')
    return False


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


def live_config(path):
    connection = UnixHTTP(path, timeout=4)
    try:
        connection.request('GET', '/config/')
        response = connection.getresponse()
        data = response.read(LIMIT + 1)
        if response.status != 200 or len(data) > LIMIT:
            raise ValueError('admin response')
        return json.loads(data)
    finally:
        connection.close()


def token(value, fallback='unknown'):
    # SQLite/journal contents are untrusted; only expose known identifier shapes.
    return value if isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9_.:-]{1,96}', value) else fallback


def job_summary(row):
    data = json.loads(row[0])
    if not isinstance(data, dict):
        raise ValueError('invalid job')
    return {key: token(data.get(key)) for key in
            ('job_id', 'project_id', 'action', 'status', 'phase', 'error_code', 'finished_at')}


def diagnostics(config, key_id):
    checks = []
    jobs = []
    def add(id, label, ready, detail, repair=None, warning=False):
        check = {'id': id, 'label': label, 'status': 'ready' if ready else 'warning' if warning else 'failed',
                 'detail': detail}
        if not ready and repair:
            check['repair'] = repair
        checks.append(check)

    caddyfile = config.get('caddyfile', '/etc/caddy/Caddyfile')
    sites = config.get('caddy_sites', '/etc/caddy/dockyard')
    caddy = config.get('caddy_binary', '/usr/bin/caddy')
    admin = config.get('caddy_admin_socket', '/run/caddy/admin.sock')
    docker = config.get('docker_binary', '/usr/bin/docker')
    database = Path(config.get('state_dir', '/var/lib/dockyard')) / 'state.db'
    quote = shlex.quote
    keys = [key for key in config.get('keys', []) if key.get('id') == key_id]
    compose = len(keys) == 1 and 'compose.admin' in keys[0].get('scopes', []) and '*' in keys[0].get('projects', [])
    add('compose_access', 'Compose management', compose,
        'This Mac can manage Compose projects across the server.' if compose else
        'Enable Compose management with Touch ID to grant this Mac server-wide access.')
    docker_ok, _ = run([docker, '--config', config.get('docker_config', '/etc/dockyard/docker'), 'info', '--format', '{{.ServerVersion}}'])
    add('docker', 'Docker daemon', docker_ok, 'Docker responds to the configured client.' if docker_ok else
        'Docker is unavailable through the configured client.', 'sudo systemctl status docker --no-pager')
    compose_ok, _ = run([docker, 'compose', 'version'])
    add('docker_compose', 'Docker Compose', compose_ok, 'The Compose plugin is available.' if compose_ok else
        'Install or repair the Docker Compose plugin on the VPS.')
    caddy_active, _ = run(['systemctl', 'is-active', '--quiet', 'caddy'])
    add('caddy_service', 'Caddy service', caddy_active, 'Caddy is running.' if caddy_active else
        'Caddy is stopped or unavailable.', 'sudo systemctl status caddy --no-pager')
    try:
        imported = managed_import(caddyfile, sites)
    except (OSError, ValueError):
        imported = False
    add('caddy_import', 'Dockyard route import', imported,
        'The Caddyfile loads the generated route directory.' if imported else
        'Seamless traffic switching needs a top-level import for the configured generated route directory. Review existing files before adding it.',
        'sudo ls -la ' + quote(sites) + '\nsudo nano ' + quote(caddyfile) + '\n# Add this line at the top level of the Caddyfile:\n# import ' + json.dumps(str(Path(sites) / '*.caddy')))
    try:
        if not Path(sites).is_dir():
            raise OSError('route directory missing')
        files = list(Path(sites).glob('*.caddy'))
        folder = info(sites)
        unsafe = folder['uid'] != 0 or bool(folder['mode'] & 0o022)
        for path in files:
            file = info(path)
            unsafe = unsafe or file['kind'] != 'regular' or file['uid'] != 0 or bool(file['mode'] & 0o022)
        add('caddy_sites', 'Generated route files', not unsafe,
            'Review root ownership, writable permissions, links and file types in the generated route directory.' if unsafe else
            'The route directory is empty; Dockyard creates files during deployment.' if not files else
            f'{len(files)} generated route files found. Caddy validation checks for route conflicts.',
            'sudo ls -la ' + quote(sites))
    except OSError:
        add('caddy_sites', 'Generated route files', False, 'The configured route directory cannot be inspected.')
    adapted_ok, adapted_bytes = run([caddy, 'adapt', '--config', caddyfile, '--adapter', 'caddyfile'], timeout=8, cwd=str(Path(caddyfile).parent))
    try:
        adapted = json.loads(adapted_bytes) if adapted_ok else None
        adapted_ok = adapted_ok and isinstance(adapted, dict)
    except ValueError:
        adapted_ok, adapted = False, None
    valid, _ = run([caddy, 'validate', '--config', caddyfile, '--adapter', 'caddyfile'], timeout=8, cwd=str(Path(caddyfile).parent))
    validate_command = 'sudo ' + quote(caddy) + ' validate --config ' + quote(caddyfile) + ' --adapter caddyfile'
    add('caddy_validation', 'Caddy configuration', valid and adapted_ok,
        'The saved Caddy configuration is valid.' if valid and adapted_ok else
        'Caddy could not validate the saved configuration. Run the command below for details.', validate_command)
    try:
        live = live_config(admin)
        live_ok = isinstance(live, dict)
    except (OSError, ValueError, http.client.HTTPException):
        live, live_ok = None, False
    add('caddy_admin', 'Private Caddy connection', live_ok,
        'The private admin socket responds with live configuration.' if live_ok else
        'The configured private Caddy admin socket is missing, unreachable, or returned an invalid response.',
        'sudo curl --unix-socket ' + quote(admin) + ' -o /dev/null -w "%{http_code}\\n" http://localhost/config/')
    coherent = adapted_ok and live_ok and adapted == live
    add('caddy_coherence', 'Saved and live routes', coherent,
        'Saved routes match the running Caddy configuration.' if coherent else
        'Saved and live configurations differ or could not be compared. Resolve validation/admin failures first, then review and reload the saved configuration.',
        validate_command + ' &&\n  sudo ' + quote(caddy) + ' reload --config ' + quote(caddyfile) + ' --adapter caddyfile --address ' + quote('unix/' + admin))
    # Check the actual systemd mount namespace, rather than root SSH permissions.
    pid_ok, pid_bytes = run(['systemctl', 'show', 'dockyard', '--property=MainPID', '--value'])
    pid = pid_bytes.decode().strip()
    directories = [str(Path(caddyfile).parent), sites]
    writable = False
    daemon_root = False
    capability = False
    if pid_ok and pid.isdecimal() and int(pid) > 0:
        try:
            status = Path('/proc', pid, 'status').read_text()
            uid = re.search(r'^Uid:\s*\d+\s+(\d+)', status, re.M)
            daemon_root = bool(uid and uid[1] == '0')
            match = re.search(r'^CapEff:\s*([0-9a-f]+)$', status, re.M)
            capability = bool(match and int(match[1], 16) & (1 << 1))
        except OSError:
            pass
    if daemon_root:
        probe = 'import os,sys; sys.exit(0 if all(os.path.isdir(p) and os.access(p, os.W_OK | os.X_OK) for p in sys.argv[1:]) else 1)'
        writable, _ = run(['nsenter', '--target', pid, '--mount', '--', 'python3', '-c', probe, *directories])
    add('caddy_write', 'Caddy folder write access', writable,
        'Both Caddy folders are writable inside Dockyard’s service mount namespace.' if writable else
        'Could not confirm write access inside Dockyard’s running service. Check ReadWritePaths for both folders and ensure nsenter is installed.',
        'sudo systemctl show dockyard -p ProtectSystem -p ReadWritePaths -p MainPID\n# Required folders: ' + ', '.join(directories))
    add('caddy_capability', 'Private socket permissions', capability,
        'Dockyard has CAP_DAC_OVERRIDE to reach Caddy’s private socket.' if capability else
        'Dockyard could not confirm CAP_DAC_OVERRIDE. The private Caddy socket requires this capability with the standard service unit.',
        'sudo systemctl show dockyard -p CapabilityBoundingSet\n# Standard unit requires CapabilityBoundingSet=CAP_DAC_OVERRIDE')
    db_ok = False
    adopted = []
    try:
        with closing(sqlite3.connect(database.as_uri() + '?mode=ro', uri=True, timeout=2)) as db:
            rows = db.execute("SELECT data FROM jobs WHERE status IN ('failed','recovery_required') ORDER BY seq DESC LIMIT 5").fetchall()
            jobs = [job_summary(row) for row in rows]
            recovery = db.execute("SELECT count(*) FROM jobs WHERE status='recovery_required'").fetchone()[0]
            db_ok = True
            try:
                subjects = db.execute("SELECT p.data FROM projects p WHERE p.id IN (SELECT project_id FROM jobs WHERE status IN ('failed','recovery_required') ORDER BY seq DESC LIMIT 5)").fetchall()
                parsed = [json.loads(row[0]) for row in subjects]
                adopted = [project for project in parsed if isinstance(project, dict) and isinstance(project.get('adoption'), dict)]
            except (sqlite3.Error, ValueError, TypeError):
                pass
        add('job_recovery', 'Job recovery', recovery == 0,
            'No jobs are waiting for recovery.' if recovery == 0 else
            f'{recovery} jobs need recovery. Review the affected operations before retrying.')
    except (OSError, ValueError, TypeError, sqlite3.Error):
        add('job_recovery', 'Job recovery', False, 'Job history could not be read. Check the configured SQLite database.')
    add('database_read', 'Job history access', db_ok,
        'Recent failed jobs were read using Python’s built-in SQLite support.' if db_ok else
        'The configured job database is missing or unreadable. No sqlite3 command-line installation is needed.')
    for project in adopted:
        id = str(project.get('id', ''))[:64]
        name = str(project['adoption'].get('source_name', id))[:64]
        observed = inspect_adopted(config, project)
        identity = observed is not None and observed[0]
        add('compose_identity_' + id, 'Compose identity · ' + name, identity,
            'Current containers match the recorded migration identities or verified Dockyard ownership.' if identity else
            'Container identities could not be verified. Manual Compose recreation can change IDs and block lifecycle operations. Inspect the current stack before retrying.')
        if observed is not None:
            consistent = project.get('state') != 'stopped' or not observed[1]
            add('compose_state_' + id, 'Recorded Compose state · ' + name, consistent,
                'No running containers conflict with the recorded stopped state.' if consistent else
                'Docker has running containers while Dockyard records this project as stopped. A manual VPS start does not activate a Dockyard release.')
    logs_ok, output = run(['journalctl', '-u', 'dockyard', '-u', 'caddy', '--since', '2 hours ago', '-n', '300', '--no-pager', '-o', 'cat'], timeout=5)
    # Summaries keep request headers, credentials and raw application output private.
    signals = {'inventory warnings': 0, 'certificate errors': 0, 'upstream connection failures': 0}
    for line in output.decode('utf-8', errors='replace').splitlines():
        if 'inventory sources incomplete' in line or 'PROJECT_METADATA_INCOMPLETE' in line:
            signals['inventory warnings'] += 1
        try:
            entry = json.loads(line)
            certificate_error = isinstance(entry, dict) and ('certificate' in line or str(entry.get('logger', '')).startswith('tls')) and ('error' in entry or entry.get('level') == 'error')
        except ValueError:
            certificate_error = False
        if certificate_error:
            signals['certificate errors'] += 1
        if 'connection refused' in line and 'dial tcp' in line:
            signals['upstream connection failures'] += 1
    found = ', '.join(f'{count} {label}' for label, count in signals.items() if count)
    add('recent_logs', 'Recent server logs', logs_ok and not found,
        ('Last 300 journal entries within two hours: ' + found + '. These may concern other projects.') if found else
        'No recognized inventory, certificate or connection errors in the last 300 journal entries within two hours.' if logs_ok else
        'The service journal could not be read.',
        'sudo journalctl -u dockyard -u caddy --since "2 hours ago" -n 100 --no-pager -o short-iso', warning=logs_ok)
    return checks, jobs


def main(key_id):
    service, _ = run(['systemctl', 'is-active', '--quiet', 'dockyard'])
    report = {'root': os.geteuid() == 0, 'parent': info('/var/lib'),
              'stage': info('/var/lib/dockyard-desktop-updates'), 'bin_dir': info('/usr/local/bin'),
              'daemon': info('/usr/local/bin/dockyard'), 'ctl': info('/usr/local/bin/dockyardctl'),
              'database': info('/var/lib/dockyard/state.db'), 'service_active': service,
              'checks': [], 'recent_jobs': []}
    try:
        config = read_json(CONFIG)
        if not isinstance(config, dict):
            raise ValueError('invalid policy')
        report['database'] = info(Path(config.get('state_dir', '/var/lib/dockyard')) / 'state.db')
        report['checks'], report['recent_jobs'] = diagnostics(config, key_id)
    except (OSError, ValueError, TypeError, KeyError, AttributeError):
        report['checks'] = [{'id': 'server_config', 'label': 'Server configuration', 'status': 'failed',
                             'detail': 'The Dockyard policy could not be read. Review /etc/dockyard/config.json on the VPS.'}]
    return report


if __name__ == '__main__':
    print(json.dumps(main(sys.argv[1] if len(sys.argv) == 2 else ''), ensure_ascii=True))
