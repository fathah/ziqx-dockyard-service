"""Read-only update admission snapshot. Never return job inputs or edit jobs."""
import hashlib
import json
from pathlib import Path
import re
import sqlite3
import stat


def inspect_jobs(path):
    path = Path(path).absolute()
    db = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=5)
    try:
        rows = db.execute("SELECT id,project_id,status,data FROM jobs WHERE status IN ('queued','running','recovery_required') ORDER BY id LIMIT 1001").fetchall()
    finally:
        db.close()
    if len(rows) > 1000:
        raise RuntimeError('Too many unfinished jobs to review; nothing was changed')
    snapshot, active, recovery = [], [], []
    size = 0
    def token(value):
        if not isinstance(value, str) or len(value) > 128 or not re.fullmatch(r'[A-Za-z0-9_-]*', value):
            raise RuntimeError('Job metadata requires inspection; nothing was changed')
        return value
    for id, project, status, raw in rows:
        if isinstance(raw, bytes):
            raw = raw.decode('utf-8')
        size += len(raw)
        if size > 2 * 1024 * 1024:
            raise RuntimeError('Unfinished job data exceeds the review limit; nothing was changed')
        data = json.loads(raw)
        if not isinstance(data, dict) or data.get('job_id') != id or data.get('project_id') != project or data.get('status') != status:
            raise RuntimeError('Job metadata differs from its index; nothing was changed')
        summary = {'job_id': token(id), 'project_id': token(project), 'status': status,
                   'action': token(data.get('action', '')), 'phase': token(data.get('phase', '')),
                   'error_code': token(data.get('error_code', ''))}
        (recovery if status == 'recovery_required' else active).append(summary)
        snapshot.append([id, project, status, raw])
    digest = hashlib.sha256(json.dumps(snapshot, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()
    return {'sha256': digest, 'active_count': len(active), 'recovery_count': len(recovery),
            'active_jobs': active[:10], 'recovery_jobs': recovery[:10]}


if __name__ == '__main__':
    path = Path('/var/lib/dockyard/state.db')
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise RuntimeError('Dockyard database permissions require inspection')
    print(json.dumps(inspect_jobs(path)))
