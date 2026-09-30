"""Grant this enrolled Mac full Compose management after native root approval."""
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import sqlite3
import time
import subprocess
import sys
import tempfile

CONFIG = Path('/etc/dockyard/config.json')
SCOPES = ['compose.admin', 'projects.write', 'sites.write', 'dns.write', 'deploy.read',
          'deploy.logs', 'deploy.execute', 'deploy.environment', 'deploy.rollback',
          'deploy.lifecycle', 'deploy.stop']


def trusted(path, directory=False):
    st = path.lstat()
    expected = stat.S_ISDIR if directory else stat.S_ISREG
    if not expected(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise ValueError('Review root ownership of /etc/dockyard before enabling Compose management.')


def run(*args):
    return subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=45).returncode == 0


def grant(config, key_id):
    keys = [key for key in config['keys'] if key['id'] == key_id]
    if len(keys) != 1:
        raise ValueError('This Mac credential was not found in the server configuration.')
    key = keys[0]
    key['scopes'] = sorted(set(key.get('scopes', [])) | set(SCOPES))
    key['projects'] = ['*']
    return config


def idle(config):
    path = Path(config['state_dir']) / 'state.db'
    trusted(path)
    with sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=5) as db:
        return db.execute("SELECT count(*) FROM jobs WHERE status IN ('queued','running','recovery_required')").fetchone()[0] == 0


def healthy():
    for _ in range(8):
        if not run('systemctl', 'is-active', '--quiet', 'dockyard'):
            return False
        time.sleep(1)
    return True


def main(key_id):
    if os.geteuid() != 0 or not re.fullmatch(r'[a-z][a-z0-9-]{0,47}', key_id):
        raise ValueError('Root access and a valid enrolled credential are required.')
    trusted(CONFIG.parent, True)
    trusted(CONFIG)
    # Serialize against changes to this configuration without locking/replacing
    # the config inode itself. Only this fixed private directory is writable.
    lock_fd = os.open(CONFIG.parent / 'compose-access.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, 'w') as lock:
        st = os.fstat(lock.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o077:
            raise ValueError('Review the Compose access lock permissions on the server.')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        original = CONFIG.read_bytes()
        if len(original) > 1024 * 1024:
            raise ValueError('Server configuration exceeds its size limit.')
        config = grant(json.loads(original), key_id)
        if config == json.loads(original):
            return {'enabled': True}
        if not idle(config):
            raise ValueError("Finish or reconcile active Dockyard jobs before enabling Compose management.")
        updated = json.dumps(config, indent=2).encode() + b'\n'
        fd, name = tempfile.mkstemp(prefix='.compose-access-', suffix='.json', dir=CONFIG.parent)
        candidate = Path(name)
        try:
            with os.fdopen(fd, 'wb') as f:
                f.write(updated)
                f.flush()
                os.fsync(f.fileno())
            if not run('/usr/local/bin/dockyard', '-config', name, '-check'):
                raise ValueError('Update the Dockyard server first, then enable Compose management.')
            if CONFIG.read_bytes() != original:
                raise ValueError('Server configuration changed. Try enabling Compose management again.')
            backup_fd, backup_name = tempfile.mkstemp(prefix='config-before-compose-', suffix='.json', dir=CONFIG.parent)
            with os.fdopen(backup_fd, 'wb') as backup:
                backup.write(original)
                backup.flush()
                os.fsync(backup.fileno())
            try:
                if not run('systemctl', 'stop', 'dockyard'):
                    raise RuntimeError('stop failed')
                if not idle(config):
                    raise RuntimeError('jobs changed while stopping')
                os.replace(candidate, CONFIG)
                if not run('systemctl', 'start', 'dockyard') or not healthy():
                    raise RuntimeError('restart failed')
            except Exception:
                os.replace(backup_name, CONFIG)
                if not run('systemctl', 'restart', 'dockyard'):
                    raise ValueError('Previous configuration restored. Dockyard needs a service restart on the VPS.')
                raise ValueError('Compose access could not be enabled. Previous configuration restored.')
        finally:
            candidate.unlink(missing_ok=True)
    return {'enabled': True}


if __name__ == '__main__':
    try:
        print(json.dumps(main(sys.argv[1])))
    except ValueError as error:
        print(json.dumps({'error': str(error)}))
    except Exception:
        print(json.dumps({'error': 'Could not enable Compose management. Check server configuration and service status.'}))
