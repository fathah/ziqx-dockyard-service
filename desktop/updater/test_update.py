"""Exercise rollback against disposable binaries and a real SQLite snapshot."""
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
        with sqlite3.connect(self.db) as conn:
            conn.executescript("CREATE TABLE jobs(status TEXT); CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('original');")
        (self.stage / 'request.json').write_text(json.dumps({
            'candidate': {n: update.digest(self.stage / n) for n in update.NAMES},
            'expected': {n: update.digest(self.bin / n) for n in update.NAMES},
        }))
        for attr, value in [('BASE', self.stage), ('BIN', self.bin), ('STATE', self.db), ('CONFIG', config)]:
            patcher = mock.patch.object(update, attr, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        for attr, value in [('directory', lambda _: None), ('regular', lambda *_: None),
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
                    with sqlite3.connect(self.db) as conn:
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
        with sqlite3.connect(self.db) as conn:
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
        with sqlite3.connect(self.db) as conn:
            self.assertEqual(conn.execute('SELECT value FROM marker').fetchone()[0], 'original')


if __name__ == '__main__':
    unittest.main()
