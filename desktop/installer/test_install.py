import importlib.util
import unittest
import json
import shutil
import subprocess
import tempfile
import time
from pathlib import Path

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install.py'))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

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
