#!/usr/bin/env python3
"""Fixed first-install operations. Receives only a native-generated, private staged kit."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time

SOCKET = 'unix//run/caddy/admin.sock|0600'
IMPORT = 'import /etc/caddy/dockyard/*.caddy'
PHASES = ('verify', 'dependencies', 'credentials', 'caddy', 'ssh', 'service', 'complete')

def phase(name):
    print('DOCKYARD_STAGE:' + name, flush=True)

def run(args, timeout=120):
    result = subprocess.run(args, capture_output=True, timeout=timeout, check=False,
                            env={**os.environ, 'DEBIAN_FRONTEND': 'noninteractive', 'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'})
    if len(result.stdout) + len(result.stderr) > 8 * 1024 * 1024:
        raise ValueError('command output exceeded limit')
    if result.returncode:
        raise ValueError('command failed: ' + Path(args[0]).name)
    return result.stdout

def protected(path, directory=False):
    p = Path(path)
    # Refuse symlinks and writable ancestors, including pre-existing install trees.
    for ancestor in (p, *p.parents):
        s = ancestor.lstat()
        if stat.S_ISLNK(s.st_mode) or s.st_uid != 0 or s.st_mode & 0o022:
            raise ValueError('unsafe installation path')
    s = p.lstat()
    if directory and not stat.S_ISDIR(s.st_mode):
        raise ValueError('installation directory required')

def mkdir(path, mode=0o700, group=None):
    p = Path(path)
    if p.exists() or p.is_symlink():
        protected(p, True)
    else:
        protected(p.parent, True)
        p.mkdir(mode=mode)
    if group:
        import grp
        os.chown(p, 0, grp.getgrnam(group).gr_gid)
    os.chmod(p, mode)

def write(path, data, mode=0o600):
    p = Path(path)
    protected(p.parent, True)
    if p.exists() or p.is_symlink():
        protected(p)
        if not p.is_file() or p.read_bytes() != data:
            raise ValueError('existing installation file differs; operator review required')
        return
    with os.fdopen(os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode), 'wb') as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())

def tokens(text):
    """Positions of Caddyfile tokens; keep strings/comments/placeholders intact."""
    result, i = [], 0
    while i < len(text):
        if text[i].isspace():
            i += 1
            continue
        if text[i] == '#':
            i = text.find('\n', i)
            if i < 0: break
            continue
        start = i
        if text[i] in ('"', '`'):
            quote = text[i]
            i += 1
            while i < len(text) and text[i] != quote:
                if text[i] == '\\' and quote == '"': i += 1
                i += 1
            if i == len(text): raise ValueError('unterminated Caddy string')
            i += 1
        else:
            while i < len(text) and not text[i].isspace(): i += 1
        result.append((text[start:i], start, i))
    return result

def caddy_candidate(text):
    ts = tokens(text)
    if not ts: return '{\n\tadmin ' + SOCKET + '\n}\n' + IMPORT + '\n'
    edits = []
    if ts[0][0] == '{':
        depth, admins = 0, []
        for word, start, end in ts:
            if word == '{': depth += 1
            elif word == '}':
                depth -= 1
                if depth == 0: break
            elif depth == 1 and word == 'admin':
                line_start = text.rfind('\n', 0, start) + 1
                if text[line_start:start].strip():
                    raise ValueError('custom Caddy admin directive requires manual preparation')
                newline = text.find('\n', end)
                if newline < 0: raise ValueError('invalid admin directive')
                line = text[start:newline].split('#', 1)[0].strip()
                if line not in ('admin localhost:2019', 'admin 127.0.0.1:2019', 'admin ' + SOCKET):
                    raise ValueError('custom Caddy admin configuration requires manual preparation')
                admins.append((line_start, newline, '\tadmin ' + SOCKET))
        if depth != 0 or len(admins) > 1: raise ValueError('invalid Caddy global options')
        edits.extend(admins or [(ts[0][2], ts[0][2], '\n\tadmin ' + SOCKET + '\n')])
    else:
        edits.append((ts[0][1], ts[0][1], '{\n\tadmin ' + SOCKET + '\n}\n'))
    # Exact managed import only; unusual imports are caught by semantic comparison.
    has_import = any(a[0] == 'import' and b[0].strip('"') == '/etc/caddy/dockyard/*.caddy'
                     for a, b in zip(ts, ts[1:]))
    for start, end, replacement in reversed(sorted(edits)):
        text = text[:start] + replacement + text[end:]
    return text.rstrip() + '\n' + ('' if has_import else IMPORT + '\n')

def same_sites(before, after):
    before, after = dict(before), dict(after)
    before.pop('admin', None)
    after.pop('admin', None)
    return before == after

def docker_dependencies(stage):
    if not shutil.which('docker'):
        # No replacement/removal of existing runtimes or packages.
        if shutil.which('containerd') or shutil.which('podman'):
            raise ValueError('existing container runtime requires manual Docker installation')
        run(['apt-get', 'update'], 600)
        run(['apt-get', 'install', '-y', 'ca-certificates', 'curl', 'gnupg'], 600)
        key = stage / 'docker.asc'
        run(['curl', '--fail', '--silent', '--show-error', '--proto', '=https', '--tlsv1.2',
             'https://download.docker.com/linux/ubuntu/gpg', '-o', str(key)])
        fingerprints = run(['gpg', '--batch', '--show-keys', '--with-colons', str(key)]).decode()
        if '9DC858229FC7DD38854AE2D88D81803C0EBFCD88' not in fingerprints:
            raise ValueError('Docker signing key mismatch')
        mkdir('/etc/apt/keyrings', 0o755)
        write('/etc/apt/keyrings/dockyard-docker.asc', key.read_bytes(), 0o644)
        release = dict(line.strip().split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
        codename = release.get('VERSION_CODENAME', '').strip('"')
        if codename not in ('jammy', 'noble'): raise ValueError('unsupported Ubuntu version')
        source = ('Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: ' + codename +
                  '\nComponents: stable\nArchitectures: amd64\nSigned-By: /etc/apt/keyrings/dockyard-docker.asc\n').encode()
        write('/etc/apt/sources.list.d/dockyard-docker.sources', source, 0o644)
        run(['apt-get', 'update'], 600)
        run(['apt-get', 'install', '-y', 'docker-ce', 'docker-ce-cli', 'containerd.io', 'docker-buildx-plugin', 'docker-compose-plugin'], 900)
        run(['systemctl', 'enable', '--now', 'docker'])
    compose = run(['docker', 'compose', 'version', '--short']).decode().strip().lstrip('v')
    match = re.match(r'(\d+)\.(\d+)', compose)
    if not match or tuple(map(int, match.groups())) < (2, 30):
        raise ValueError('existing Docker Compose needs an operator upgrade to 2.30 or newer')
    run(['docker', 'info', '--format', '{{.ServerVersion}}'])
    if not shutil.which('caddy'):
        run(['apt-get', 'update'], 600)
        run(['apt-get', 'install', '-y', 'ca-certificates', 'curl', 'gnupg', 'debian-keyring', 'debian-archive-keyring', 'apt-transport-https'], 600)
        # Caddy's official stable Ubuntu repository; never execute a downloaded script.
        key = stage / 'caddy.asc'
        binary_key = stage / 'caddy.gpg'
        run(['curl', '--fail', '--silent', '--show-error', '--location', '--proto', '=https', '--proto-redir', '=https', '--tlsv1.2',
             'https://dl.cloudsmith.io/public/caddy/stable/gpg.key', '-o', str(key)])
        run(['gpg', '--batch', '--yes', '--dearmor', '--output', str(binary_key), str(key)])
        write('/usr/share/keyrings/caddy-stable-archive-keyring.gpg', binary_key.read_bytes(), 0o644)
        source = b'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main\n'
        write('/etc/apt/sources.list.d/caddy-stable.list', source, 0o644)
        run(['apt-get', 'update'], 600)
        run(['apt-get', 'install', '-y', 'caddy'], 600)

def prepare_caddy(stage):
    path = Path('/etc/caddy/Caddyfile')
    if not path.exists(): write(path, b'# Dockyard initial Caddyfile\n', 0o644)
    protected(path)
    if path.stat().st_size > 1024 * 1024: raise ValueError('Caddyfile too large')
    before = stage / 'Caddyfile.before'
    if not before.exists(): write(before, path.read_bytes())
    candidate = Path('/etc/caddy/.dockyard-desktop.Caddyfile')
    content = caddy_candidate(before.read_text()).encode()
    write(candidate, content, 0o644)
    # The original snapshot is in a different directory: resolve relative imports there using a same-directory copy.
    snapshot = Path('/etc/caddy/.dockyard-before.Caddyfile')
    write(snapshot, before.read_bytes(), 0o644)
    original = json.loads(run(['caddy', 'adapt', '--adapter', 'caddyfile', '--config', str(snapshot)]))
    proposed = json.loads(run(['caddy', 'adapt', '--adapter', 'caddyfile', '--config', str(candidate)]))
    if not same_sites(original, proposed): raise ValueError('Caddy routes would change; manual preparation required')
    run(['caddy', 'validate', '--adapter', 'caddyfile', '--config', str(candidate)])
    if path.read_bytes() not in (before.read_bytes(), content): raise ValueError('Caddyfile changed during setup')
    mkdir('/etc/systemd/system/caddy.service.d', 0o755)
    write('/etc/systemd/system/caddy.service.d/dockyard.conf', b'[Service]\nRuntimeDirectory=caddy\nRuntimeDirectoryMode=0700\n', 0o644)
    import pwd, grp
    runtime = Path('/run/caddy')
    if runtime.is_symlink(): raise ValueError('unsafe Caddy runtime directory')
    runtime.mkdir(mode=0o700, exist_ok=True)
    s = runtime.stat()
    uid = pwd.getpwnam('caddy').pw_uid
    if s.st_uid not in (0, uid) or s.st_mode & 0o022: raise ValueError('unsafe Caddy runtime owner')
    os.chown(runtime, uid, grp.getgrnam('caddy').gr_gid)
    os.chmod(runtime, 0o700)
    run(['systemctl', 'daemon-reload'])
    active = subprocess.run(['systemctl', 'is-active', '--quiet', 'caddy']).returncode == 0
    if path.read_bytes() != content:
        # Atomic disk update; preserve the backup and installed state on uncertain reloads.
        replacement = Path('/etc/caddy/.dockyard-apply.Caddyfile')
        write(replacement, content, 0o644)
        os.replace(replacement, path)
    if active:
        # If a prior attempt already changed the admin address, use the new socket.
        address = 'unix//run/caddy/admin.sock' if Path('/run/caddy/admin.sock').exists() else original.get('admin', {}).get('listen', 'localhost:2019')
        if address not in ('localhost:2019', '127.0.0.1:2019', 'unix//run/caddy/admin.sock'):
            raise ValueError('custom Caddy admin endpoint requires manual setup')
        run(['caddy', 'reload', '--adapter', 'caddyfile', '--config', str(path), '--address', address])
    else:
        run(['systemctl', 'enable', '--now', 'caddy'])
    if not Path('/run/caddy/admin.sock').exists(): raise ValueError('Caddy admin socket unavailable')

def configure_ssh(stage):
    username = 'dockyard-link'
    import pwd
    marker = stage / 'ssh-user-created'
    try:
        account = pwd.getpwnam(username)
        if not marker.exists(): raise ValueError('existing dockyard-link account requires manual review')
    except KeyError:
        run(['useradd', '--system', '--create-home', '--home-dir', '/var/lib/dockyard-link', '--shell', '/usr/sbin/nologin', username])
        write(marker, b'created\n')
        run(['usermod', '--password', '*', username])
        account = pwd.getpwnam(username)
    home = Path(account.pw_dir)
    if str(home) != '/var/lib/dockyard-link' or account.pw_uid == 0: raise ValueError('invalid connector account')
    # Root-owned immutable key file; this account cannot alter its SSH authorization.
    for path in (home, home / '.ssh'):
        if path.is_symlink(): raise ValueError('unsafe SSH home')
        path.mkdir(mode=0o755, exist_ok=True)
        os.chown(path, 0, 0)
        os.chmod(path, 0o755)
    public = (stage / 'ssh.pub').read_text().strip()
    if not re.fullmatch(r'ssh-ed25519 [A-Za-z0-9+/=]+(?: [A-Za-z0-9_-]+)?', public): raise ValueError('invalid connector public key')
    authorized = ('restrict,port-forwarding,permitopen="127.0.0.1:9123" ' + public + '\n').encode()
    mkdir('/etc/ssh/sshd_config.d', 0o755)
    # OpenSSH Match prevents remote forwarding as well as login/commands.
    text = b'Match User dockyard-link\n  AuthenticationMethods publickey\n  PasswordAuthentication no\n  KbdInteractiveAuthentication no\n  AllowTcpForwarding local\n  PermitOpen 127.0.0.1:9123\n  X11Forwarding no\n  AllowAgentForwarding no\n  PermitTTY no\n  ForceCommand /usr/bin/false\nMatch all\n'
    write('/etc/ssh/sshd_config.d/00-dockyard-link.conf', text, 0o644)
    run(['/usr/sbin/sshd', '-t'])
    import ipaddress
    client_address = os.environ.get('SSH_CONNECTION', '127.0.0.1').split()[0]
    ipaddress.ip_address(client_address)
    effective = run(['/usr/sbin/sshd', '-T', '-C', 'user=dockyard-link,host=localhost,addr=' + client_address]).decode()
    settings = dict(line.split(' ', 1) for line in effective.splitlines() if ' ' in line)
    expected = {'allowtcpforwarding': 'local', 'permitopen': '127.0.0.1:9123',
                'passwordauthentication': 'no', 'kbdinteractiveauthentication': 'no',
                'permittty': 'no', 'allowagentforwarding': 'no', 'forcecommand': '/usr/bin/false'}
    if any(settings.get(k) != v for k, v in expected.items()):
        raise ValueError('custom SSH configuration prevents a restricted connector; manual preparation required')
    write(home / '.ssh/authorized_keys', authorized, 0o644)
    run(['systemctl', 'reload', 'ssh'])

def install(stage):
    phase('verify')
    if os.geteuid() != 0: raise ValueError('root required')
    protected(stage, True)
    manifest = json.loads((stage / 'manifest.json').read_text())
    if manifest.get('version') != 1 or not re.fullmatch(r'[a-f0-9]{32}', manifest.get('id', '')): raise ValueError('invalid manifest')
    for name, digest in manifest['files'].items():
        if '/' in name or not re.fullmatch(r'[A-Za-z0-9_.-]+', name): raise ValueError('invalid staged filename')
        path = stage / name
        protected(path)
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest: raise ValueError('staged file checksum mismatch')
    if run(['uname', '-m']).strip() != b'x86_64': raise ValueError('Ubuntu x86-64 required')
    release = dict(line.strip().split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
    if release.get('ID', '').strip('"') != 'ubuntu' or release.get('VERSION_ID', '').strip('"') not in ('22.04', '24.04'): raise ValueError('Ubuntu 22.04/24.04 required')
    mkdir('/etc/dockyard')
    marker = Path('/etc/dockyard/.desktop-install-id')
    if Path('/etc/dockyard/config.json').exists() and not marker.exists(): raise ValueError('existing Dockyard installation refused')
    write(marker, manifest['id'].encode())
    phase('dependencies')
    docker_dependencies(stage)
    phase('credentials')
    for directory in ('/etc/dockyard/tls', '/etc/dockyard/docker', '/var/lib/dockyard'):
        mkdir(directory)
    # Preserve /docker without changing permissions or existing Compose files.
    if Path('/docker').exists(): protected('/docker', True)
    else: mkdir('/docker')
    mkdir('/etc/caddy/dockyard', 0o750, 'caddy')
    for source, dest, mode in (
        ('server.crt', '/etc/dockyard/tls/server.crt', 0o600), ('server.key', '/etc/dockyard/tls/server.key', 0o600),
        ('control-ca.crt', '/etc/dockyard/tls/control-ca.crt', 0o600), ('desktop.key', '/etc/dockyard/desktop-01.key', 0o600),
        ('fingerprint.key', '/etc/dockyard/fingerprint.key', 0o600), ('config.json', '/etc/dockyard/config.json', 0o600),
        ('server.json', '/etc/dockyard/server.json', 0o600), ('dockyard', '/usr/local/bin/dockyard', 0o755),
        ('dockyardctl', '/usr/local/bin/dockyardctl', 0o755), ('dockyard.service', '/etc/systemd/system/dockyard.service', 0o644)):
        write(dest, (stage / source).read_bytes(), mode)
    phase('caddy')
    prepare_caddy(stage)
    phase('ssh')
    configure_ssh(stage)
    phase('service')
    run(['/usr/local/bin/dockyard', '-config', '/etc/dockyard/config.json', '-check'])
    # This imports existing services as production observations only.
    if subprocess.run(['systemctl', 'is-active', '--quiet', 'dockyard']).returncode != 0:
        run(['/usr/local/bin/dockyard', '-config', '/etc/dockyard/config.json', '-sync-existing'])
    run(['systemctl', 'daemon-reload'])
    run(['systemctl', 'enable', '--now', 'dockyard'])
    for _ in range(30):
        if subprocess.run(['systemctl', 'is-active', '--quiet', 'dockyard']).returncode == 0:
            time.sleep(3)
            if subprocess.run(['systemctl', 'is-active', '--quiet', 'dockyard']).returncode == 0:
                phase('complete')
                return
        time.sleep(1)
    raise ValueError('Dockyard did not remain active')

if __name__ == '__main__':
    current = 'verify'
    try:
        if len(sys.argv) != 1: raise ValueError('no arguments accepted')
        lock = open('/run/dockyard-desktop-setup.lock', 'w')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        install(Path(__file__).resolve().parent)
    except Exception as error:
        # Never echo certificate material, environment values, or root passwords.
        print('DOCKYARD_FAILED:' + str(error)[:250], file=sys.stderr)
        try:
            error_path = Path(__file__).resolve().parent / 'error.txt'
            protected(error_path.parent, True)
            if error_path.exists(): protected(error_path)
            with os.fdopen(os.open(error_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), 'w') as stream:
                stream.write(str(error)[:1000] + '\n')
        except Exception:
            pass
        sys.exit(1)
