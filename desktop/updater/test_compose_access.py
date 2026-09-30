import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('compose_access', Path(__file__).with_name('compose_access.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ComposeAccessTests(unittest.TestCase):
    def test_grant_is_specific_and_idempotent(self):
        config = {'keys': [{'id': 'mac', 'scopes': ['deploy.read'], 'projects': ['demo']},
                           {'id': 'other', 'scopes': ['deploy.read'], 'projects': ['demo']}],
                  'templates': {'keep': {}}, 'reserved_domains': ['control.example.com']}
        m.grant(config, 'mac')
        once = json.dumps(config)
        m.grant(config, 'mac')
        self.assertEqual(json.dumps(config), once)
        self.assertEqual(config['keys'][1]['projects'], ['demo'])
        self.assertNotIn('compose.admin', config['keys'][1]['scopes'])
        self.assertEqual(config['keys'][0]['projects'], ['*'])
        self.assertIn('compose.admin', config['keys'][0]['scopes'])
        self.assertEqual(config['reserved_domains'], ['control.example.com'])
        with self.assertRaises(ValueError):
            m.grant(config, 'missing')

    def exercise(self, validate=True, healthy=True, idle=True):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'config.json'
            original = json.dumps({'state_dir': root, 'keys': [{'id': 'mac', 'scopes': ['deploy.read']}]}).encode()
            path.write_bytes(original)
            commands = []
            def run(*args):
                commands.append(args)
                return validate if args[0] == '/usr/local/bin/dockyard' else True
            with patch.object(m, 'CONFIG', path), patch.object(m, 'trusted'), \
                 patch.object(m.os, 'geteuid', return_value=0), \
                 patch.object(m.os, 'fstat', return_value=SimpleNamespace(st_mode=0o100600, st_uid=0)), \
                 patch.object(m, 'run', side_effect=run), patch.object(m, 'idle', return_value=idle), \
                 patch.object(m, 'healthy', return_value=healthy):
                if validate and healthy and idle:
                    self.assertEqual(m.main('mac'), {'enabled': True})
                    self.assertIn('compose.admin', json.loads(path.read_bytes())['keys'][0]['scopes'])
                else:
                    with self.assertRaises(ValueError):
                        m.main('mac')
                    self.assertEqual(path.read_bytes(), original)
            if not validate or not idle:
                self.assertFalse(any(c[0] == 'systemctl' for c in commands))
            if not healthy and validate and idle:
                self.assertIn(('systemctl', 'restart', 'dockyard'), commands)
            for backup in Path(root).glob('config-before-compose-*'):
                self.assertEqual(backup.stat().st_mode & 0o777, 0o600)

    def test_enables_with_private_backup(self): self.exercise()
    def test_old_binary_does_not_change_config(self): self.exercise(validate=False)
    def test_busy_service_is_not_interrupted(self): self.exercise(idle=False)
    def test_failed_start_restores_previous_config(self): self.exercise(healthy=False)


if __name__ == '__main__': unittest.main()
