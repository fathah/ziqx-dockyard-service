import json
from contextlib import closing
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
import repair


class RepairTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.config = self.root / 'config.json'
        self.caddyfile = self.root / 'Caddyfile'
        self.sites = self.root / 'sites'
        self.sites.mkdir()
        self.caddyfile.write_text('app.example.com {\n\treverse_proxy 127.0.0.1:3031\n}\n')
        self.config.write_text(json.dumps({'state_dir': str(self.root), 'caddyfile': str(self.caddyfile), 'caddy_sites': str(self.sites)}))
        with closing(sqlite3.connect(self.root / 'state.db')) as db:
            db.executescript('CREATE TABLE projects(id TEXT, data BLOB); CREATE TABLE jobs(status TEXT);')
        self.calls = []
        self.saved = {'apps': {'http': {'servers': {'test': {'routes': [{'match': [{'host': ['app.example.com']}]}]}}}}}
        self.observed = {'apps': {}}
        self.validation = True
        self.after_reload = self.saved
        def run(args, **kwargs):
            self.calls.append(args)
            if args[0] == 'systemctl':
                return (not (args[1] == 'is-active' and args[-1].startswith('dockyard-repair-')), b'')
            if args[1] == 'adapt':
                return True, json.dumps(self.saved).encode()
            if args[1] == 'validate':
                return self.validation, b''
            if args[1] == 'reload':
                self.observed = self.after_reload
            return True, b''
        original_stat = os.fstat
        def root_stat(fd):
            fields = list(original_stat(fd))
            fields[4] = 0
            return os.stat_result(fields)
        self.patches = [patch.object(repair, 'CONFIG', self.config), patch.object(repair, 'trusted'), patch.object(repair.os, 'fstat', side_effect=root_stat),
                        patch.object(repair, 'run', side_effect=run), patch.object(repair, 'live', side_effect=lambda _: self.observed),
                        patch.object(repair.time, 'sleep'), patch.object(repair.os, 'geteuid', return_value=0)]
        for item in self.patches:
            item.start()

    def tearDown(self):
        for item in reversed(self.patches):
            item.stop()
        self.temp.cleanup()

    def test_review_has_no_raw_config_or_credentials(self):
        self.caddyfile.write_text('app.example.com {\n basicauth { user secret_hash }\n}\n')
        review = repair.main('caddy_coherence')
        self.assertEqual(review['saved_domains'], ['app.example.com'])
        self.assertNotIn('secret_hash', json.dumps(review))
        self.assertFalse(any(call[1] == 'stop' for call in self.calls if call[0] == 'systemctl'))

    def test_changed_config_or_live_state_never_stops_daemon(self):
        review = repair.main('caddy_coherence')
        self.observed = {'changed': True}
        with self.assertRaisesRegex(ValueError, 'changed'):
            repair.main('caddy_coherence', review['review_sha256'])
        self.assertFalse(any(call[1] == 'stop' for call in self.calls if call[0] == 'systemctl'))

    def test_active_or_recovery_job_blocks_repair(self):
        with closing(sqlite3.connect(self.root / 'state.db')) as db:
            db.execute("INSERT INTO jobs VALUES('recovery_required')")
            db.commit()
        with self.assertRaisesRegex(ValueError, 'reconcile'):
            repair.main('caddy_coherence')

    def test_rejected_configuration_restores_file_and_restarts_daemon(self):
        original = self.caddyfile.read_bytes()
        review = repair.main('caddy_import')
        self.validation = False
        with self.assertRaisesRegex(ValueError, 'validation failed'):
            repair.main('caddy_import', review['review_sha256'])
        self.assertEqual(self.caddyfile.read_bytes(), original)
        self.assertIn(['systemctl', 'start', 'dockyard'], self.calls)
        self.assertFalse(any(call[1] == 'reload' for call in self.calls if call[0].endswith('caddy')))

    def test_success_is_observed_even_when_reload_exit_fails(self):
        review = repair.main('caddy_coherence')
        result = repair.main('caddy_coherence', review['review_sha256'])
        self.assertTrue(result['repaired'])
        self.assertTrue(Path(result['backup'], 'evidence.json').exists())
        self.assertIn(['systemctl', 'start', 'dockyard'], self.calls)

    def test_unknown_reload_keeps_reviewed_intent_and_private_backup(self):
        review = repair.main('caddy_import')
        self.after_reload = {'different': True}
        with self.assertRaisesRegex(ValueError, 'preserved'):
            repair.main('caddy_import', review['review_sha256'])
        self.assertIn('import', self.caddyfile.read_text())
        self.assertTrue(list((self.root / 'repair-backups').glob('*/evidence.json')))
        self.assertIn(['systemctl', 'start', 'dockyard'], self.calls)

    def test_launch_survives_ssh_disconnect_and_has_restart_cleanup(self):
        review = repair.main('caddy_coherence')
        with patch.object(repair, 'SOURCE', 'fixed bundled worker', create=True):
            result = repair.launch('caddy_coherence', review['review_sha256'])
        self.assertTrue(result['running'])
        launch = next(call for call in self.calls if call[0] == 'systemd-run')
        self.assertIn('--property=ExecStopPost=/bin/systemctl start dockyard', launch)
        self.assertIn('fixed bundled worker', launch)
        self.assertEqual(launch[-2:], ['caddy_coherence', review['review_sha256']])

    def test_admin_repair_preserves_site_and_uses_private_mode(self):
        site = 'app.example.com {\n reverse_proxy localhost:3000\n}\n'
        updated = repair.admin_option('{\n admin localhost:2019\n email admin@example.com\n}\n' + site, '/run/caddy/admin.sock')
        self.assertIn('admin unix//run/caddy/admin.sock|0600', updated)
        self.assertIn('email admin@example.com', updated)
        self.assertTrue(updated.endswith(site))
        self.assertEqual(repair.admin_option(updated, '/run/caddy/admin.sock'), updated)
        with self.assertRaises(ValueError):
            repair.admin_option('{ admin off }\n' + site, '/run/caddy/admin.sock')

    def test_missing_socket_review_includes_runtime_directory_and_restart(self):
        with patch.object(repair, 'live', side_effect=OSError('missing socket')), patch.object(repair, 'legacy_live', return_value=None):
            preview, _, files, snapshot = repair.plan('caddy_admin')
        self.assertIsNone(snapshot['live'])
        self.assertFalse(preview['live_available'])
        self.assertIn(b'RuntimeDirectory=caddy\nRuntimeDirectoryMode=0700', files[Path('/etc/systemd/system/caddy.service.d/30-dockyard-admin.conf')])
        self.assertTrue(any('Restart Caddy briefly' in change for change in preview['changes']))
        self.assertTrue(preview['reloads_caddy'])

    def test_service_routes_are_not_duplicated_during_repair(self):
        value = repair.snippet({'state': 'running', 'active_slot': 'blue', 'blue_port': 3001,
                                'domains': ['www.example.com', 'api.example.com'],
                                'service_routes': [{'domains': ['api.example.com'], 'host_port': 3002}]})
        self.assertEqual(value.count(b'api.example.com {'), 1)
        self.assertIn(b'reverse_proxy 127.0.0.1:3002', value)

    def test_unrecognized_actions_and_result_paths_are_rejected(self):
        for action in ['restart arbitrary.service', '../caddy_admin', 'recent_logs']:
            with self.assertRaises(ValueError):
                repair.main(action)
        with self.assertRaises(ValueError):
            repair.result_path('../config')


if __name__ == '__main__':
    unittest.main()
