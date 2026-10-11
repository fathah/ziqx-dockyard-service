"""Lossless route recovery checked against a real, disposable Caddy adapter."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import repair


@unittest.skipUnless(os.environ.get('DOCKYARD_TEST_CADDY_BINARY'), 'requires a real Caddy adapter')
class RealCaddyRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.binary = os.environ['DOCKYARD_TEST_CADDY_BINARY']

    def tearDown(self):
        self.temp.cleanup()

    def adapt(self, source):
        path = self.root / 'Caddyfile'
        path.write_text(source)
        result = subprocess.run([self.binary, 'adapt', '--config', str(path), '--adapter', 'caddyfile'],
                                capture_output=True, check=True)
        return json.loads(result.stdout)

    def test_each_service_cutover_preserves_the_other_site_and_rules(self):
        project = {'state': 'running', 'active_slot': 'blue',
                   'domains': ['api.example.com', 'web.example.com'],
                   'service_routes': [
                       {'service': 'api', 'host_port': 3002, 'domains': ['api.example.com']},
                       {'service': 'web', 'host_port': 3001, 'domains': ['web.example.com']}],
                   'service_instances': {'web': {'port': 3011}},
                   'caddy_routes': [{'domain': 'api.example.com', 'address': 'api.example.com',
                                     'body': '\n header X-Site api\n reverse_proxy {dockyard.upstream} {\n transport http {\n keepalive 30s\n }\n }\n'}]}
        def adapted():
            config = self.adapt('{\n admin off\n}\n' + repair.snippet(project).decode())
            return {name: repair.recovered_site(config, name) for name in project['domains']}
        before = adapted()
        project['service_instances']['api'] = {'port': 3010}
        after = adapted()
        self.assertEqual(before['web.example.com'], after['web.example.com'])
        old_proxy = next(handler for handler in before['api.example.com']['handlers'] if handler['handler'] == 'reverse_proxy')
        new_proxy = next(handler for handler in after['api.example.com']['handlers'] if handler['handler'] == 'reverse_proxy')
        self.assertEqual(old_proxy['upstreams'], [{'dial': '127.0.0.1:3002'}])
        self.assertEqual(new_proxy['upstreams'], [{'dial': '127.0.0.1:3010'}])
        old_proxy['upstreams'] = new_proxy['upstreams']
        self.assertEqual(before['api.example.com'], after['api.example.com'])
        del project['service_instances']['api']
        self.assertEqual(adapted()['web.example.com'], after['web.example.com'])

    def test_recovery_preserves_real_handler_chains(self):
        base = '{\n admin off\n}\napp.example.com {\n reverse_proxy localhost:9000\n}\n'
        examples = {
            'proxy': 'reverse_proxy localhost:3000',
            'www redirect': 'redir https://example.com{uri} permanent',
            'compression and headers': 'header X-Frame-Options SAMEORIGIN\n encode gzip zstd\n reverse_proxy localhost:3000',
            'response': 'respond "Site ready" 200',
            'proxy headers': 'reverse_proxy localhost:3000 {\n header_up X-Origin {host}\n header_down -Server\n flush_interval -1\n}',
            'TLS upstream': 'reverse_proxy localhost:3000 {\n transport http {\n tls\n tls_server_name backend.example.com\n}\n}',
            'authentication': 'basic_auth {\n user $2a$14$Z3C8Vevp4HDyMLBPE16wQefHbDggKJECMQNQ4AQ42mr4SXqrCPCty\n}\n reverse_proxy localhost:3000',
            'header removal': 'header {\n -Server\n +X-Tag test\n}\n reverse_proxy localhost:3000',
            'replica health and keepalive': '''reverse_proxy localhost:3011 localhost:3012 localhost:3013 {
 lb_policy least_conn
 health_uri /api/health
 health_interval 10s
 health_timeout 3s
 health_status 2xx
 lb_try_duration 5s
 lb_try_interval 250ms
 transport http {
  keepalive 30s
  keepalive_idle_conns_per_host 32
 }
}
encode zstd gzip''',
            'active and passive health': '''reverse_proxy localhost:3000 {
 health_uri /status?check=1
 health_upstream localhost:8081
 health_method POST
 health_request_body "health check"
 health_body "^ready$"
 health_passes 2
 health_fails 3
 health_follow_redirects
 health_headers {
  Host health.example.com
  X-Check one two
 }
 fail_duration 30s
 max_fails 2
 unhealthy_status 5xx 429
 unhealthy_latency 4s
 unhealthy_request_count 20
}''',
            'streaming and trusted proxies': '''reverse_proxy localhost:3000 {
 request_buffers 64KiB
 response_buffers unlimited
 stream_buffer_size 16KiB
 stream_timeout 2h
 stream_close_delay 10s
 trusted_proxies private_ranges 192.0.2.0/24
 verbose_logs
 method POST
 rewrite /backend{uri}
}''',
            'retry matching': '''reverse_proxy localhost:3000 localhost:3001 {
 lb_policy round_robin
 lb_retries 3
 lb_retry_match {
  method GET HEAD
  path /api/*
  header X-Retry yes
 }
}''',
            'response replacement': '''reverse_proxy localhost:3000 {
 @failed {
  status 5xx 429
  header X-Error yes
 }
 replace_status @failed 503
}''',
            'response handling': '''reverse_proxy localhost:3000 {
 @failed status 5xx
 handle_response @failed {
  header X-Guard enabled
  respond "Upstream unavailable" 503
 }
}''',
            'HTTP transport settings': '''reverse_proxy localhost:3000 {
 transport http {
  keepalive off
  keepalive_interval 10s
  keepalive_idle_conns 32
  dial_timeout 5s
  dial_fallback_delay 250ms
  response_header_timeout 20s
  expect_continue_timeout 2s
  read_timeout 30s
  write_timeout 30s
  compression off
  max_conns_per_host 10
  versions 1.1 2
 }
}''',
        }
        for policy in ('random', 'first', 'ip_hash', 'client_ip_hash', 'uri_hash', 'random_choose 2', 'weighted_round_robin 1 2', 'header X-Client', 'query session', 'cookie session test-secret', 'cookie session test-secret {\n max_age 1h\n fallback first\n}'):
            examples['policy ' + policy] = 'reverse_proxy localhost:3000 localhost:3001 {\n lb_policy ' + policy + '\n}'
        for label, body in examples.items():
            with self.subTest(label=label):
                saved = self.adapt(base)
                current = self.adapt(base + 'example.com {\n' + body + '\n}\n')
                recovered, names = repair.live_only_sites(saved, current)
                desired = self.adapt(base + recovered)
                repair.verify_recovered_sites(current, desired, names)
                self.assertEqual(names, ['example.com'])
                self.assertIn('app.example.com', repair.hosts(desired))
                repair.preview_recovered_sites(self.binary, self.root / 'Caddyfile', (base + recovered).encode(), current, names)
                self.assertFalse(list(self.root.glob('.dockyard-review-*')))

    def test_real_recovery_rejects_lost_rules_and_identifies_unsupported_site(self):
        base = '{\n admin off\n}\n'
        current = self.adapt(base + 'example.com {\n header X-Guard enabled\n reverse_proxy localhost:3000\n}\n')
        weakened = self.adapt(base + 'example.com {\n reverse_proxy localhost:3000\n}\n')
        with self.assertRaisesRegex(ValueError, 'preserve all active rules for example.com'):
            repair.verify_recovered_sites(current, weakened, ['example.com'])
        matched = self.adapt(base + 'example.com {\n handle /api/* {\n reverse_proxy localhost:3000\n}\n}\n')
        with self.assertRaisesRegex(ValueError, 'example.com: matched or grouped routes'):
            repair.live_only_sites({}, matched)


if __name__ == '__main__':
    unittest.main()
