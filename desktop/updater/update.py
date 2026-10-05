#!/usr/bin/env python3
"""Fixed, root-only Dockyard binary update. Uploaded by the pinned Mac client."""
import hashlib
import fcntl
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import subprocess
import sys
import time
from update_jobs import inspect_jobs

BASE = Path(__file__).resolve().parent
BIN = Path('/usr/local/bin')
STATE = Path('/var/lib/dockyard/state.db')
CONFIG = Path('/etc/dockyard/config.json')
NAMES = ('dockyard', 'dockyardctl')
UNIT = Path('/etc/systemd/system/dockyard.service')


def fail(message):
    raise RuntimeError(message)


def run(*args, timeout=30):
    p = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                       stderr=subprocess.DEVNULL, timeout=timeout, check=False)
    if p.returncode:
        if args[0].endswith('/dockyard'):
            fail('The new Dockyard binary cannot validate the current server configuration. Nothing was replaced')
        if args[:2] == ('systemctl', 'stop'):
            fail('Could not stop the Dockyard service for the update')
        if args[:2] == ('systemctl', 'start'):
            fail('The Dockyard service did not start with the new binary')
        fail('A required server command failed during the update')
    return p.stdout.decode('utf-8', 'replace').strip()


def regular(path, owner=0):
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != owner or st.st_mode & 0o022:
        fail('Unsafe update file: ' + str(path))
    return st


def directory(path):
    st = path.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        fail('Unsafe update directory: ' + str(path))


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def sync_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def active():
    return subprocess.run(['systemctl', 'is-active', '--quiet', 'dockyard'],
                          stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=10).returncode == 0


def healthy():
    consecutive = 0
    for _ in range(35):
        if active():
            consecutive += 1
            if consecutive >= 8:
                return True
        else:
            consecutive = 0
        time.sleep(1)
    return False


def ubuntu():
    return 'ID=ubuntu' in Path('/etc/os-release').read_text().splitlines() and run('uname', '-m') == 'x86_64'


def reviewed_jobs(expected):
    if not STATE.exists():
        fail('Dockyard database is missing')
    regular(STATE)
    jobs = inspect_jobs(STATE)
    if jobs['active_count']:
        fail('A Dockyard job is queued or running. Wait for it to finish, then check the server again')
    if jobs['sha256'] != expected:
        fail('Dockyard jobs changed since the update review. Check the server again')
    return jobs


def main():
    if os.geteuid() != 0:
        fail('Root access is required')
    for path in (Path('/'), Path('/usr'), Path('/usr/local'), BIN,
                 Path('/var'), Path('/var/lib'), Path('/var/lib/dockyard-desktop-updates'), BASE):
        directory(path)
    lock_path = BASE.parent / 'update.lock'
    lock_fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    lock_stat = os.fstat(lock_fd)
    if not stat.S_ISREG(lock_stat.st_mode) or lock_stat.st_uid != 0 or lock_stat.st_mode & 0o077:
        fail('Unsafe updater lock file')
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return apply_update()
    finally:
        os.close(lock_fd)


def apply_update():
    release = json.loads((BASE / 'request.json').read_text())
    if set(release) != {'candidate', 'expected', 'jobs_sha256'} or set(release['candidate']) != set(NAMES) or set(release['expected']) != set(NAMES):
        fail('Invalid update request')
    if not ubuntu():
        fail('Only Ubuntu x86-64 servers are supported')
    regular(CONFIG)
    if json.loads(CONFIG.read_text()).get('state_dir') != str(STATE.parent):
        fail('This server uses a custom state directory; update it with a reviewed backup procedure')
    if not active():
        fail('Dockyard service must be running before an update')
    jobs = reviewed_jobs(release['jobs_sha256'])
    for name in NAMES:
        staged = regular(BASE / name)
        if name == 'dockyard' and not staged.st_mode & stat.S_IXUSR:
            fail('Uploaded Dockyard binary is not executable. Retry with the latest Mac app')
        regular(BIN / name)
        if digest(BASE / name) != release['candidate'][name]:
            fail('Uploaded ' + name + ' failed its checksum')
        if digest(BIN / name) != release['expected'][name]:
            fail('Installed ' + name + ' changed since the update check. Check again.')
    # Older Mac apps do not upload the unit; then it is left unchanged.
    staged_unit = BASE / 'dockyard.service'
    new_unit = staged_unit.read_bytes() if staged_unit.exists() else None
    if new_unit is not None:
        regular(staged_unit)
        regular(UNIT)
    unit_changed = new_unit is not None and UNIT.read_bytes() != new_unit
    if all(release['candidate'][n] == release['expected'][n] for n in NAMES) and not unit_changed:
        return {'status': 'current'}
    run(str(BASE / 'dockyard'), '-config', '/etc/dockyard/config.json', '-check', timeout=30)
    backups = BASE / 'backup'
    backups.mkdir(mode=0o700)
    directory(backups)
    for name in NAMES:
        shutil.copy2(BIN / name, backups / name)
        regular(backups / name)
        sync_file(backups / name)
    if unit_changed:
        shutil.copy2(UNIT, backups / 'dockyard.service')
        sync_file(backups / 'dockyard.service')
    sync_dir(backups)
    # Docker and Caddy remain running; only the Dockyard control service stops.
    changed = False
    try:
        run('systemctl', 'stop', 'dockyard', timeout=60)
        reviewed_jobs(release['jobs_sha256'])
        # A consistent SQLite snapshot allows rollback if a new binary migrates its schema.
        source = sqlite3.connect('file:' + str(STATE) + '?mode=ro', uri=True)
        target = sqlite3.connect(str(backups / 'state.db'))
        try:
            source.backup(target)
        finally:
            target.close()
            source.close()
        os.chmod(backups / 'state.db', 0o600)
        sync_file(backups / 'state.db')
        sync_dir(backups)
        for name in NAMES:
            replacement = BIN / ('.' + name + '.dockyard-update')
            if replacement.exists() or replacement.is_symlink():
                fail('An unfinished update file needs operator review')
            with (BASE / name).open('rb') as src, replacement.open('xb') as dst:
                shutil.copyfileobj(src, dst)
                dst.flush()
                os.fsync(dst.fileno())
            os.chmod(replacement, 0o755)
            os.replace(replacement, BIN / name)
            changed = True
        sync_dir(BIN)
        if unit_changed:
            replacement = UNIT.with_name('.dockyard.service.dockyard-update')
            replacement.unlink(missing_ok=True)
            with replacement.open('xb') as dst:
                dst.write(new_unit)
                dst.flush()
                os.fsync(dst.fileno())
            os.chmod(replacement, 0o644)
            os.replace(replacement, UNIT)
            changed = True
            sync_dir(UNIT.parent)
            run('systemctl', 'daemon-reload', timeout=60)
        run('systemctl', 'start', 'dockyard', timeout=60)
        if not healthy():
            fail('Updated Dockyard service did not stay active')
        # Recovery blocks admission and the worker. A binary update must retain
        # that exact job/lock; it does not claim to have repaired the deployment.
        if jobs['recovery_count']:
            reviewed_jobs(release['jobs_sha256'])
        return {'status': 'updated', 'hashes': {n: digest(BIN / n) for n in NAMES}}
    except Exception as error:
        if changed:
            try:
                run('systemctl', 'stop', 'dockyard', timeout=60)
            except Exception:
                if active():
                    fail('Could not safely stop the failed update. Inspect ' + str(BASE))
            if active():
                fail('Could not safely stop the failed update. Inspect ' + str(BASE))
            for name in NAMES:
                if (backups / name).exists():
                    os.replace(backups / name, BIN / name)
            sync_dir(BIN)
            if (backups / 'dockyard.service').exists():
                os.replace(backups / 'dockyard.service', UNIT)
                sync_dir(UNIT.parent)
                try:
                    run('systemctl', 'daemon-reload', timeout=60)
                except Exception:
                    pass
            if (backups / 'state.db').exists():
                # Stop closed SQLite; replace its database and remove WAL companions.
                for suffix in ('-wal', '-shm'):
                    (Path(str(STATE) + suffix)).unlink(missing_ok=True)
                os.replace(backups / 'state.db', STATE)
                sync_dir(STATE.parent)
        try:
            run('systemctl', 'start', 'dockyard', timeout=60)
            restored = healthy()
        except Exception:
            restored = False
        fail(str(error) + ('. Previous Dockyard version restored.' if restored else '. Automatic recovery failed; inspect ' + str(BASE)))


if __name__ == '__main__':
    try:
        print(json.dumps(main()))
    except PermissionError:
        print(json.dumps({'status': 'error', 'message': 'The VPS denied access to a required updater file. Check Required server access in Server details, then retry'}))
    except Exception as exc:
        print(json.dumps({'status': 'error', 'message': str(exc)[:300]}))
