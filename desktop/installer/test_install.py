import importlib.util
import unittest
import json
import shutil
import subprocess
import tempfile
import time
import errno
import stat
import os
import socket
import sys
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install.py'))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

class ServicePermissions(unittest.TestCase):
    @unittest.skipUnless(sys.platform == 'linux' and os.geteuid() == 0 and shutil.which('setpriv'),
                         'requires a disposable root Ubuntu fixture with setpriv')
    def test_private_caddy_socket_requires_the_retained_capability(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory) / 'caddy'
            runtime.mkdir(mode=0o700)
            os.chown(runtime, 1001, 1001)
            address = str(runtime / 'admin.sock')
            with socket.socket(socket.AF_UNIX) as server:
                server.bind(address)
                os.chown(address, 1001, 1001)
                os.chmod(address, 0o600)
                server.listen(1)
                probe = "import os,socket,sys; os.lstat(sys.argv[1]); s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.close()"
                for capabilities, allowed in (('-all', False), ('-all,+dac_override', True)):
                    result = subprocess.run(['setpriv', '--bounding-set=' + capabilities,
                                             '--inh-caps=-all', '--ambient-caps=-all', '--no-new-privs',
                                             sys.executable, '-c', probe, address], capture_output=True, timeout=10)
                    self.assertEqual(result.returncode == 0, allowed, capabilities)
                self.assertEqual(stat.S_IMODE(runtime.stat().st_mode), 0o700)
                self.assertEqual(stat.S_IMODE(os.stat(address).st_mode), 0o600)

class SetupDiagnostics(unittest.TestCase):
    def metadata(self, path):
        # Actual incident: Caddyfile safe, parent owned by deployuser.
        return SimpleNamespace(st_uid=1000 if str(path) == '/etc/caddy' else 0,
                               st_mode=stat.S_IFREG | 0o644 if str(path).endswith('Caddyfile') else stat.S_IFDIR | 0o755)

    def test_unsafe_parent_reports_exact_path_and_reason(self):
        with patch.object(Path, 'lstat', autospec=True, side_effect=self.metadata):
            with self.assertRaises(installer.SetupError) as error:
                installer.protected('/etc/caddy/Caddyfile')
        self.assertEqual(installer.diagnostic(error.exception),
                         {'code': 'path_owner', 'path': '/etc/caddy', 'tool': ''})

    def test_symbolic_links_and_writable_paths_are_distinct(self):
        for mode, code in ((stat.S_IFLNK | 0o777, 'path_symlink'), (stat.S_IFDIR | 0o775, 'path_writable')):
            with patch.object(Path, 'lstat', return_value=SimpleNamespace(st_uid=0, st_mode=mode)):
                with self.assertRaises(installer.SetupError) as error:
                    installer.protected('/docker', True)
            self.assertEqual(error.exception.code, code)

    def test_command_error_and_timeout_never_echo_output(self):
        for result in (subprocess.CompletedProcess(['caddy'], 1, b'secret', b'secret'),
                       subprocess.TimeoutExpired(['caddy'], 1, output=b'secret')):
            with patch.object(installer.subprocess, 'run', **({'side_effect': result} if isinstance(result, Exception) else {'return_value': result})):
                with self.assertRaises(installer.SetupError) as error:
                    installer.run(['caddy', 'validate'])
            report = installer.diagnostic(error.exception)
            self.assertEqual(report['tool'], 'caddy')
            self.assertNotIn('secret', json.dumps(report))

    def test_unknown_exception_text_is_not_exposed(self):
        for error in (ValueError('secret'), RuntimeError('secret'), KeyError('secret')):
            self.assertEqual(installer.diagnostic(error), {'code': 'unknown'})
        self.assertEqual(installer.diagnostic(OSError(errno.ENOSPC, 'secret')), {'code': 'disk_full'})
        self.assertEqual(installer.diagnostic(PermissionError(errno.EACCES, 'secret')), {'code': 'permission_denied'})

class CaddyPreparation(unittest.TestCase):
    def test_existing_sites_and_nested_global_options_remain_literal(self):
        source = '# preserve me\n{\n email owner@example.com\n servers {\n protocols h1 h2\n }\n}\nexample.com {\n reverse_proxy localhost:3000\n header X-Example "a { b }"\n}\n'
        result = installer.caddy_candidate(source)
        self.assertIn('# preserve me', result)
        self.assertIn('email owner@example.com', result)
        self.assertIn('protocols h1 h2', result)
        self.assertIn('example.com {\n reverse_proxy localhost:3000\n header X-Example "a { b }"\n}', result)
        self.assertEqual(result.count('admin ' + installer.SOCKET), 1)
        self.assertEqual(installer.caddy_candidate(result), result)

    def test_comments_and_placeholders_do_not_change_brace_depth(self):
        source = '{\n # admin off }\n email {$EMAIL}\n}\nexample.com {\n respond `braces { inside } a raw string`\n}\n'
        result = installer.caddy_candidate(source)
        self.assertIn('email {$EMAIL}', result)
        self.assertIn('respond `braces { inside } a raw string`', result)
        self.assertEqual(result.count('admin ' + installer.SOCKET), 1)

    def test_custom_admin_is_rejected(self):
        for line in ('admin off', 'admin :2020', 'admin localhost:2019 {', 'admin {$ADDRESS}'):
            with self.assertRaises(ValueError):
                installer.caddy_candidate('{\n ' + line + '\n}\nexample.com {\n respond ok\n}\n')

    def test_no_global_block_preserves_site_text(self):
        source = '# comment\nexample.com {\n reverse_proxy 127.0.0.1:3000\n}\n'
        result = installer.caddy_candidate(source)
        self.assertIn('example.com {\n reverse_proxy 127.0.0.1:3000\n}', result)
        self.assertEqual(result.count(installer.IMPORT), 1)

    def test_site_comparison_rejects_any_route_change(self):
        original = {'apps': {'http': {'servers': {'srv0': {'routes': [{'match': 'a'}]}}}}}
        only_admin = {**original, 'admin': {'listen': installer.SOCKET}}
        changed = {'apps': {'http': {'servers': {'srv0': {'routes': [{'match': 'b'}]}}}}}
        self.assertTrue(installer.same_sites(original, only_admin))
        self.assertFalse(installer.same_sites(original, changed))
        self.assertNotIn('admin', original)

    @unittest.skipUnless(shutil.which('caddy'), 'requires the real Caddy binary')
    def test_real_adapter_preserves_relative_imports_and_existing_routes(self):
        Path('/etc/caddy/dockyard').mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'sites.caddy').write_text('http://imported.example.com {\n reverse_proxy 127.0.0.1:4200\n}\n')
            source = '{\n admin localhost:2019\n}\nimport sites.caddy\nhttp://example.com {\n reverse_proxy 127.0.0.1:3000\n header X-Example "a { b }"\n}\n'
            original = root / 'original.Caddyfile'
            candidate = root / 'candidate.Caddyfile'
            original.write_text(source)
            candidate.write_text(installer.caddy_candidate(source))
            def adapt(path):
                result = subprocess.run(['caddy', 'adapt', '--adapter', 'caddyfile', '--config', str(path)], capture_output=True, check=True)
                return json.loads(result.stdout)
            before, after = adapt(original), adapt(candidate)
            self.assertTrue(installer.same_sites(before, after))
            self.assertEqual(after['admin']['listen'], installer.SOCKET)
            self.assertIn('imported.example.com', json.dumps(after))
            Path('/run/caddy').mkdir(parents=True, exist_ok=True)
            process = subprocess.Popen(['caddy', 'run', '--adapter', 'caddyfile', '--config', str(candidate)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                live = None
                for _ in range(50):
                    try:
                        live = installer.live_caddy_config('unix//run/caddy/admin.sock')
                        break
                    except (OSError, ConnectionError):
                        time.sleep(0.05)
                self.assertIsNotNone(live, 'real Caddy must become reachable on its private Unix socket')
                self.assertTrue(installer.same_sites(after, live))
                changed = json.loads(json.dumps(live))
                changed['apps']['http']['servers']['srv0']['routes'][0]['match'] = [{'host': ['changed.example.com']}]
                self.assertFalse(installer.same_sites(after, changed))
            finally:
                process.terminate()
                process.wait(timeout=10)

if __name__ == '__main__':
    unittest.main()
