from collections import Counter
import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import check_resilience as r


class ResilienceHarnessTests(unittest.TestCase):
    def test_production_and_development_targets_cannot_be_crashed(self):
        config = {'HR_PROJECT': 'hr-prod-test-abc', 'HR_STATE_DIR': '/tmp/hr-production-smoke-abc/state',
                  'HR_BIND_ADDRESS': '127.0.0.1', 'HR_PUBLIC_HOST': 'hr-testing.test'}
        r.owned_fixture(config)
        for key, value in [('HR_PROJECT', 'hr-testing'), ('HR_PROJECT', 'hr-testing-prod'),
                           ('HR_STATE_DIR', '/var/lib/hr-testing'), ('HR_BIND_ADDRESS', '0.0.0.0'),
                           ('HR_PUBLIC_HOST', 'real-company.example')]:
            with self.subTest(key=key, value=value), self.assertRaises(r.CheckFailure):
                r.owned_fixture(dict(config, **{key: value}))

    def test_nested_candidate_answer_key_leak_is_rejected(self):
        r.no_answer_keys({'items': [{'options': [{'label': 'A'}]}], 'score': {'percent': '100.00'}})
        for name in ('is_correct', 'explanation', 'token_hash', 'email', 'score_points'):
            with self.subTest(name=name), self.assertRaises(r.CheckFailure):
                r.no_answer_keys({'items': [{'options': [{name: 'PRIVATE'}]}]})

    def test_small_sample_p95_keeps_the_slow_tail(self):
        self.assertEqual(r.percentile([1] * 9 + [100], .95), 100)
        self.assertEqual(r.percentile(list(range(1, 21)), .95), 19)
        with self.assertRaises(r.CheckFailure):
            r.percentile([], .95)

    def test_http_failure_does_not_expose_bearer_or_response_body(self):
        class Response:
            status = 500

            def read(self):
                return b'{"message":"PRIVATE RESPONSE"}'

        class Connection:
            def request(self, *args, **kwargs):
                pass

            def getresponse(self):
                return Response()

            def close(self):
                pass

        metrics = r.Metrics()
        client = r.Client({'HR_HTTPS_PORT': '18443', 'HR_PUBLIC_ORIGIN': 'https://hr-testing.test:18443'}, None, metrics)
        with patch.object(r.rehearsal, 'LocalTLS', return_value=Connection()):
            with self.assertRaises(r.CheckFailure) as error:
                client.call('GET', '/api/v1/invite/PRIVATE_BEARER', label='load_start')
        self.assertNotIn('PRIVATE', str(error.exception))
        self.assertIn('500', str(error.exception))
        self.assertEqual(metrics.statuses['load_start'], Counter({'500': 1}))
        self.assertEqual(metrics.active, 0)

    def test_socket_failure_is_sanitized(self):
        class Connection:
            def request(self, *args, **kwargs):
                raise OSError('PRIVATE_BEARER')

            def close(self):
                pass

        metrics = r.Metrics()
        client = r.Client({'HR_HTTPS_PORT': '18443', 'HR_PUBLIC_ORIGIN': 'https://hr-testing.test:18443'}, None, metrics)
        with patch.object(r.rehearsal, 'LocalTLS', return_value=Connection()), self.assertRaises(r.CheckFailure) as error:
            client.call('GET', '/api/v1/invite/PRIVATE_BEARER', label='load_save')
        self.assertNotIn('PRIVATE', str(error.exception))
        self.assertEqual(metrics.statuses['load_save'], Counter({'0': 1}))

    def test_failed_rehearsal_cannot_leave_a_success_report(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'report.json'
            with patch.object(sys, 'argv', ['check_resilience.py', '--report', str(path)]), \
                 patch.object(r.rehearsal, 'main', side_effect=RuntimeError('PRIVATE_PAYLOAD')), \
                 contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(r.main(), 1)
            report = json.loads(path.read_text())
            self.assertFalse(report['passed'])
            self.assertNotIn('PRIVATE', path.read_text() + output.getvalue())
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_unbounded_concurrency_is_rejected_before_starting_threads(self):
        phases = r.Phases()
        for jobs in ([], [lambda: None] * 21):
            with self.assertRaises(r.CheckFailure):
                phases.run(jobs)


if __name__ == '__main__':
    unittest.main()
