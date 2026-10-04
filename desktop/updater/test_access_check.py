import importlib.util
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('access_check', Path(__file__).with_name('access_check.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class AccessCheckTests(unittest.TestCase):
    def test_adopted_identity_detects_manual_recreation_and_accepts_verified_owner(self):
        original, recreated = 'a' * 64, 'b' * 64
        project = {'id': 'demo', 'adoption': {'compose_project': 'legacy', 'source_name': 'legacy',
                   'config_files': ['/docker/legacy/compose.yml'], 'container_ids': [original]}}
        config = {'server_id': 'vps', 'projects_root': '/docker'}
        for case, id, server, owner, folder, expected in (
                ('original', original, '', '', '/docker/legacy', True),
                ('manually-recreated', recreated, '', '', '/docker/legacy', False),
                ('managed', recreated, 'vps', 'demo', '/docker/legacy', True),
                ('foreign-owner', recreated, 'other', 'demo', '/docker/legacy', False),
                ('wrong-folder', original, '', '', '/docker/other', False)):
            with self.subTest(case=case):
                metadata = {'id': id, 'running': True, 'compose': 'legacy', 'server': server,
                            'project': owner, 'folder': folder, 'files': '/docker/legacy/compose.yml'}
                with patch.object(m, 'run', side_effect=[(True, id.encode()), (True, json.dumps(metadata).encode())]) as run:
                    self.assertEqual(m.inspect_adopted(config, project), (expected, True))
                template = run.call_args_list[1].args[0]
                self.assertNotIn('.Config.Env', ' '.join(template))
                self.assertNotIn('json .Config.Labels', ' '.join(template))

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sites = self.root / 'sites'
        self.sites.mkdir()
        self.caddyfile = self.root / 'Caddyfile'
        self.config = {'caddyfile': str(self.caddyfile), 'caddy_sites': str(self.sites),
                       'state_dir': str(self.root), 'caddy_admin_socket': str(self.root / 'admin.sock'),
                       'keys': [{'id': 'mac', 'scopes': ['compose.admin'], 'projects': ['*'],
                                 'secret': 'DO_NOT_EXPOSE_CONFIG_SECRET'}]}
        self.caddyfile.write_text('app.example.com {\n reverse_proxy localhost:3000\n}\n')
        with closing(sqlite3.connect(self.root / 'state.db')) as db, db:
            db.execute('CREATE TABLE jobs (seq INTEGER PRIMARY KEY, status TEXT, data BLOB, input BLOB)')
            job = {'job_id': 'job-test', 'project_id': 'demo', 'action': 'service_update',
                   'status': 'failed', 'phase': 'service_route_intent',
                   'error_code': 'BLUE_GREEN_ROUTE_REVIEW_REQUIRED', 'finished_at': '2026-10-04T10:53:00Z',
                   'unexpected_secret': 'DO_NOT_EXPOSE_JOB_SECRET'}
            db.execute('INSERT INTO jobs VALUES (1, ?, ?, ?)', ('failed', json.dumps(job), 'DO_NOT_EXPOSE_INPUT_SECRET'))

    def checks(self, missing=(), live=None, journal=b'', writable=True):
        commands = []
        def run(args, **kwargs):
            commands.append(args)
            if 'adapt' in args:
                return 'adapt' not in missing, b'{"apps": {}}'
            if args[0] == 'journalctl':
                return 'journalctl' not in missing, journal
            if '--property=MainPID' in args:
                return True, b'1234\n'
            if args[0] == 'nsenter':
                return writable, b''
            return not any(word in args for word in missing), b''
        original = Path.read_text
        original_info = m.info
        def root_owned(path):
            result = original_info(path)
            if result['exists']:
                result['uid'] = 0
            return result
        def read_text(path, *args, **kwargs):
            if str(path) == '/proc/1234/status':
                return 'Uid:\t0\t0\t0\t0\nCapEff:\t0000000000000002\n'
            return original(path, *args, **kwargs)
        with patch.object(m, 'run', side_effect=run), patch.object(m, 'live_config', return_value={'apps': {}} if live is None else live), patch.object(Path, 'read_text', read_text), patch.object(m, 'info', side_effect=root_owned):
            checks, jobs = m.diagnostics(self.config, 'mac')
        return {check['id']: check for check in checks}, jobs, commands

    def test_missing_import_is_reported_without_edits(self):
        original = self.caddyfile.read_bytes()
        checks, jobs, commands = self.checks()
        self.assertEqual(checks['caddy_import']['status'], 'failed')
        self.assertIn(str(self.sites / '*.caddy'), checks['caddy_import']['repair'])
        self.assertEqual(checks['caddy_sites']['status'], 'ready')
        self.assertEqual(jobs[0]['phase'], 'service_route_intent')
        self.assertEqual(self.caddyfile.read_bytes(), original)
        self.assertEqual(list(self.sites.iterdir()), [])
        self.assertFalse(any('reload' in command or 'restart' in command for command in commands))

    def test_empty_import_directory_is_ready(self):
        self.caddyfile.write_text('import sites/*.caddy\n')
        checks, _, _ = self.checks()
        self.assertEqual(checks['caddy_import']['status'], 'ready')
        self.assertEqual(checks['caddy_coherence']['status'], 'ready')
        self.assertEqual(checks['caddy_write']['status'], 'ready')

    def test_commented_nested_and_specific_imports_do_not_pass(self):
        for text in ['# import sites/*.caddy\n', 'app.example.com {\nimport sites/*.caddy\n}\n',
                     '(snippet) {\nimport sites/*.caddy\n}\n', 'import sites/demo.caddy\n']:
            with self.subTest(text=text):
                self.caddyfile.write_text(text)
                self.assertFalse(m.managed_import(self.caddyfile, self.sites))

    def test_nested_files_quotes_and_import_cycles(self):
        self.caddyfile.write_text('import "other.conf"\n')
        other = self.root / 'other.conf'
        other.write_text('import Caddyfile\nimport "sites/*.caddy" # configured routes\n')
        self.assertTrue(m.managed_import(self.caddyfile, self.sites))
        other.write_text('import Caddyfile\n')
        self.assertFalse(m.managed_import(self.caddyfile, self.sites))

    def test_missing_directory_does_not_pass(self):
        self.sites.rmdir()
        checks, _, _ = self.checks()
        self.assertEqual(checks['caddy_sites']['status'], 'failed')

    def test_symlinks_and_writable_routes_need_review(self):
        route = self.sites / 'demo.caddy'
        route.symlink_to(self.caddyfile)
        checks, _, _ = self.checks()
        self.assertEqual(checks['caddy_sites']['status'], 'failed')
        route.unlink()
        route.write_text('app.example.com { reverse_proxy localhost:3000 }')
        route.chmod(0o666)
        checks, _, _ = self.checks()
        self.assertEqual(checks['caddy_sites']['status'], 'failed')

    def test_live_mismatch_and_namespace_denial_are_separate(self):
        checks, _, _ = self.checks(live={'apps': {'changed': True}}, writable=False)
        self.assertEqual(checks['caddy_admin']['status'], 'ready')
        self.assertEqual(checks['caddy_coherence']['status'], 'failed')
        self.assertEqual(checks['caddy_write']['status'], 'failed')

    def test_socket_failure_keeps_other_checks(self):
        with patch.object(m, 'run', return_value=(False, b'')), patch.object(m, 'live_config', side_effect=OSError()):
            checks, _ = m.diagnostics(self.config, 'mac')
        checks = {check['id']: check for check in checks}
        self.assertEqual(checks['caddy_admin']['status'], 'failed')
        self.assertEqual(checks['database_read']['status'], 'ready')

    def test_invalid_configuration_and_validation_fail(self):
        checks, _, _ = self.checks(missing=('adapt', 'validate'))
        self.assertEqual(checks['caddy_validation']['status'], 'failed')
        self.assertEqual(checks['caddy_coherence']['status'], 'failed')

    def test_secrets_stay_out_of_report_and_logs_are_summarized(self):
        journal = b'PROJECT_METADATA_INCOMPLETE SECRET_HEADER\n' + json.dumps({'level': 'warn', 'logger': 'tls', 'msg': 'certificate error', 'error': 'SECRET_KEY'}).encode() + b'\n{"msg":"dial tcp [::1]:3025: connection refused", "headers":{"Authorization":"SECRET_TOKEN"}}'
        checks, jobs, _ = self.checks(journal=journal)
        result = json.dumps([checks, jobs])
        for secret in ['DO_NOT_EXPOSE', 'SECRET_HEADER', 'SECRET_KEY', 'SECRET_TOKEN']:
            self.assertNotIn(secret, result)
        self.assertEqual(checks['recent_logs']['status'], 'warning')
        self.assertIn('1 certificate errors', checks['recent_logs']['detail'])
        self.assertIn('1 upstream connection failures', checks['recent_logs']['detail'])

    def test_database_open_is_read_only_and_recovers_job_summary(self):
        connect = m.sqlite3.connect
        with patch.object(m.sqlite3, 'connect', wraps=connect) as spy:
            checks, jobs, _ = self.checks()
        self.assertIn('?mode=ro', spy.call_args.args[0])
        self.assertEqual(checks['database_read']['status'], 'ready')
        self.assertEqual(jobs[0]['error_code'], 'BLUE_GREEN_ROUTE_REVIEW_REQUIRED')
        self.assertNotIn('input', jobs[0])

    def test_recovery_and_scoped_credential_do_not_pass(self):
        with closing(sqlite3.connect(self.root / 'state.db')) as db, db:
            db.execute("UPDATE jobs SET status='recovery_required'")
        self.config['keys'][0]['projects'] = ['demo']
        checks, _, _ = self.checks()
        self.assertEqual(checks['job_recovery']['status'], 'failed')
        self.assertEqual(checks['compose_access']['status'], 'failed')

    def test_absent_database_is_not_created(self):
        (self.root / 'state.db').unlink()
        checks, _, _ = self.checks()
        self.assertEqual(checks['database_read']['status'], 'failed')
        self.assertFalse((self.root / 'state.db').exists())

    def test_private_socket_uses_get_and_closes_on_bad_responses(self):
        for status, body, valid in [(200, b'{"apps":{}}', True), (503, b'{}', False),
                                    (200, b'not-json', False), (200, b'x' * (m.LIMIT + 1), False)]:
            with self.subTest(status=status, size=len(body)):
                connection = Mock()
                connection.getresponse.return_value.status = status
                connection.getresponse.return_value.read.return_value = body
                with patch.object(m, 'UnixHTTP', return_value=connection):
                    if valid:
                        self.assertEqual(m.live_config('/run/caddy/admin.sock'), {'apps': {}})
                    else:
                        with self.assertRaises(ValueError):
                            m.live_config('/run/caddy/admin.sock')
                connection.request.assert_called_once_with('GET', '/config/')
                connection.close.assert_called_once()

    def test_missing_policy_keeps_updater_checks_available(self):
        with patch.object(m, 'CONFIG', self.root / 'missing.json'), patch.object(m, 'run', return_value=(False, b'')):
            report = m.main('mac')
        self.assertIn('stage', report)
        self.assertEqual(report['checks'][0]['id'], 'server_config')
        self.assertEqual(report['recent_jobs'], [])


if __name__ == '__main__':
    unittest.main()
