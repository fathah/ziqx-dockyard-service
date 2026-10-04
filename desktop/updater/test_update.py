"""Exercise rollback against disposable binaries and a real SQLite snapshot."""
from contextlib import closing
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('dockyard_update', Path(__file__).with_name('update.py'))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class UpdateTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.stage = root / 'updates' / 'attempt'
        self.stage.mkdir(parents=True)
        self.bin = root / 'bin'
        self.bin.mkdir()
        self.db = root / 'state.db'
        config = root / 'config.json'
        config.write_text(json.dumps({'state_dir': str(root)}))
        for name in update.NAMES:
            (self.bin / name).write_bytes(('old-' + name).encode())
            (self.stage / name).write_bytes(('new-' + name).encode())
            (self.stage / name).chmod(0o700)
        with closing(sqlite3.connect(self.db)) as conn, conn:
            conn.executescript("CREATE TABLE jobs(id TEXT,project_id TEXT,status TEXT,data BLOB); CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('original');")
        (self.stage / 'request.json').write_text(json.dumps({
            'candidate': {n: update.digest(self.stage / n) for n in update.NAMES},
            'expected': {n: update.digest(self.bin / n) for n in update.NAMES},
            'jobs_sha256': update.inspect_jobs(self.db)['sha256'],
        }))
        for attr, value in [('BASE', self.stage), ('BIN', self.bin), ('STATE', self.db), ('CONFIG', config)]:
            patcher = mock.patch.object(update, attr, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        for attr, value in [('directory', lambda _: None), ('regular', lambda path: path.stat()),
                            ('ubuntu', lambda: True), ('active', lambda: True),
                            ('healthy', lambda: True)]:
            patcher = mock.patch.object(update, attr, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = mock.patch.object(update.os, 'geteuid', return_value=0)
        patcher.start()
        self.addCleanup(patcher.stop)

        real_fstat = os.fstat
        patcher = mock.patch.object(update.os, 'fstat', side_effect=lambda fd: SimpleNamespace(
            st_mode=real_fstat(fd).st_mode, st_uid=0))
        patcher.start()
        self.addCleanup(patcher.stop)

    def job(self, status):
        data = {'job_id': 'job-example', 'project_id': 'tasks', 'status': status,
                'action': 'start', 'phase': 'draining', 'error_code': 'CONTAINER_STOP_FAILED',
                'input': {'private': 'must-stay-private'}}
        with closing(sqlite3.connect(self.db)) as conn, conn:
            conn.execute('INSERT INTO jobs VALUES (?,?,?,?)', ('job-example', 'tasks', status, json.dumps(data)))
        return data

    def review_jobs(self):
        request = json.loads((self.stage / 'request.json').read_text())
        request['jobs_sha256'] = update.inspect_jobs(self.db)['sha256']
        (self.stage / 'request.json').write_text(json.dumps(request))

    def test_active_work_blocks_before_any_service_changes(self):
        for status in ('queued', 'running'):
            with self.subTest(status=status):
                with closing(sqlite3.connect(self.db)) as conn, conn:
                    conn.execute('DELETE FROM jobs')
                self.job(status)
                self.review_jobs()
                with mock.patch.object(update, 'run') as run:
                    with self.assertRaisesRegex(RuntimeError, 'queued or running'):
                        update.main()
                    run.assert_not_called()
                self.assertEqual((self.bin / 'dockyard').read_bytes(), b'old-dockyard')

    def test_recovery_update_preserves_exact_job_and_snapshot(self):
        data = self.job('recovery_required')
        self.review_jobs()
        before = update.inspect_jobs(self.db)
        self.assertNotIn('must-stay-private', json.dumps(before))
        with mock.patch.object(update, 'run'):
            self.assertEqual(update.main()['status'], 'updated')
        self.assertEqual(update.inspect_jobs(self.db), before)
        for path in (self.db, self.stage / 'backup' / 'state.db'):
            with closing(sqlite3.connect(path)) as conn, conn:
                self.assertEqual(json.loads(conn.execute('SELECT data FROM jobs').fetchone()[0]), data)

    def test_new_recovery_job_invalidates_review(self):
        self.job('recovery_required')
        with mock.patch.object(update, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'jobs changed since'):
                update.main()
            run.assert_not_called()

    def test_changed_recovery_input_invalidates_review_without_exposing_it(self):
        data = self.job('recovery_required')
        self.review_jobs()
        data['input']['private'] = 'changed-private-input'
        with closing(sqlite3.connect(self.db)) as conn, conn:
            conn.execute('UPDATE jobs SET data=?', (json.dumps(data),))
        self.assertNotIn('changed-private-input', json.dumps(update.inspect_jobs(self.db)))
        with mock.patch.object(update, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'jobs changed since'):
                update.main()
            run.assert_not_called()

    def test_job_accepted_during_shutdown_cancels_replacement(self):
        def command(*args, **_kwargs):
            if args[:2] == ('systemctl', 'stop'):
                self.job('queued')
            return ''
        with mock.patch.object(update, 'run', side_effect=command):
            with self.assertRaisesRegex(RuntimeError, 'queued or running'):
                update.main()
        self.assertEqual((self.bin / 'dockyard').read_bytes(), b'old-dockyard')
        self.assertEqual(update.inspect_jobs(self.db)['active_count'], 1)

    def test_restart_that_clears_recovery_rolls_back_job_and_binary(self):
        self.job('recovery_required')
        self.review_jobs()
        before = update.inspect_jobs(self.db)
        starts = 0
        def command(*args, **_kwargs):
            nonlocal starts
            if args[:2] == ('systemctl', 'start'):
                starts += 1
                if starts == 1:
                    with closing(sqlite3.connect(self.db)) as conn, conn:
                        conn.execute('DELETE FROM jobs')
            return ''
        with mock.patch.object(update, 'run', side_effect=command), mock.patch.object(update, 'active', side_effect=[True, False]):
            with self.assertRaisesRegex(RuntimeError, 'Previous Dockyard version restored'):
                update.main()
        self.assertEqual(update.inspect_jobs(self.db), before)
        self.assertEqual((self.bin / 'dockyard').read_bytes(), b'old-dockyard')

    def test_rejects_changed_binary_before_stopping(self):
        (self.bin / 'dockyard').write_bytes(b'changed')
        with mock.patch.object(update, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'changed since the update check'):
                update.main()
        run.assert_not_called()

    def test_restores_binaries_and_database_when_new_service_fails(self):
        starts = 0
        running = True

        def command(*args, **_kwargs):
            nonlocal starts, running
            if args[:2] == ('systemctl', 'stop'):
                running = False
            if args[:2] == ('systemctl', 'start'):
                starts += 1
                if starts == 1:
                    with closing(sqlite3.connect(self.db)) as conn, conn:
                        conn.execute("UPDATE marker SET value='migrated'")
                    raise RuntimeError('new service failed')
                running = True
            return ''

        with mock.patch.object(update, 'run', side_effect=command), mock.patch.object(update, 'active', side_effect=lambda: running):
            with self.assertRaisesRegex(RuntimeError, 'Previous Dockyard version restored'):
                update.main()
        self.assertEqual(starts, 2)
        for name in update.NAMES:
            self.assertEqual((self.bin / name).read_bytes(), ('old-' + name).encode())
        with closing(sqlite3.connect(self.db)) as conn, conn:
            self.assertEqual(conn.execute('SELECT value FROM marker').fetchone()[0], 'original')

    def test_success_replaces_only_binaries_and_keeps_backup(self):
        running = True

        def command(*args, **_kwargs):
            nonlocal running
            if args[:2] == ('systemctl', 'stop'):
                running = False
            elif args[:2] == ('systemctl', 'start'):
                running = True
            return ''

        with mock.patch.object(update, 'run', side_effect=command), mock.patch.object(update, 'active', side_effect=lambda: running):
            result = update.main()
        self.assertEqual(result['status'], 'updated')
        for name in update.NAMES:
            self.assertEqual((self.bin / name).read_bytes(), ('new-' + name).encode())
            self.assertEqual((self.stage / 'backup' / name).read_bytes(), ('old-' + name).encode())
        with closing(sqlite3.connect(self.db)) as conn, conn:
            self.assertEqual(conn.execute('SELECT value FROM marker').fetchone()[0], 'original')


if __name__ == '__main__':
    unittest.main()
