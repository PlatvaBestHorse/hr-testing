import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import subprocess
import contextlib
import io

spec = importlib.util.spec_from_file_location('production', Path(__file__).resolve().parents[1] / 'production.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


class ProductionConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='hr-prod-unit-')
        self.root = Path(self.temp.name)
        self.args = argparse.Namespace(config=str(self.root / 'config.json'), state=str(self.root / 'state'),
            host='hr-testing.test', cert=str(self.root / 'cert.pem'), key=str(self.root / 'key.pem'),
            release='test', project='hr-prod-unit', http_port=18080, https_port=18443,
            bind='127.0.0.1', subnet='172.30.252.0/24', web_ip='172.30.252.10')
        p.initialize(self.args)
        self.config = p.load(self.args.config, tls=False)

    def tearDown(self):
        self.temp.cleanup()

    def test_repeat_init_preserves_configuration_and_existing_state(self):
        original = Path(self.args.config).read_bytes()
        (self.root / 'state/postgres/marker').write_text('existing database')
        with self.assertRaises(ValueError):
            p.initialize(self.args)
        self.assertEqual(Path(self.args.config).read_bytes(), original)
        Path(self.args.config).unlink()
        with self.assertRaises(ValueError):
            p.initialize(self.args)
        self.assertTrue((self.root / 'state/postgres/marker').exists())

    def test_changed_auth_or_db_key_rejected(self):
        for key in p.SECRET_KEYS:
            changed = dict(self.config)
            changed[key] = 'a' * len(changed[key])
            Path(self.args.config).write_text(json.dumps(changed))
            with self.assertRaisesRegex(ValueError, 'identity mismatch'):
                p.load(self.args.config, tls=False)

    def test_public_or_checkout_configuration_rejected(self):
        Path(self.args.config).chmod(0o644)
        with self.assertRaises(ValueError):
            p.load(self.args.config, tls=False)
        for target in (p.ROOT / 'secret.json', self.root / 'relative'):
            if target.name == 'relative':
                target = Path('relative.json')
            with self.assertRaises(ValueError):
                p.outside_checkout(target)
        symlink = self.root / 'linked-checkout'
        symlink.symlink_to(p.ROOT, target_is_directory=True)
        with self.assertRaises(ValueError):
            p.outside_checkout(symlink / 'data')

    def test_origin_proxy_and_identifiers_rejected(self):
        for key, value in [('HR_PUBLIC_HOST', 'example.com;return 200;'),
                           ('HR_PUBLIC_ORIGIN', 'http://hr-testing.test'),
                           ('HR_WEB_INTERNAL_IP', '172.30.251.10'),
                           ('HR_WEB_INTERNAL_IP', '172.30.252.1'),
                           ('HR_PROJECT', 'hr-testing'), ('HR_RELEASE_TAG', 'x\nINJECT=y')]:
            invalid = dict(self.config)
            invalid[key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                p.validate(invalid)

    def test_compose_cannot_inherit_development_database_or_files(self):
        result = subprocess.CompletedProcess([], 0, '', '')
        with patch.dict(p.os.environ, {'COMPOSE_FILE': 'compose.yaml', 'POSTGRES_PASSWORD': 'wrong', 'HR_STATE_DIR': '/wrong'}), patch.object(p.subprocess, 'run', return_value=result) as run:
            p.compose(self.config, 'config', '--quiet')
            args, kwargs = run.call_args
            self.assertNotIn('COMPOSE_FILE', kwargs['env'])
            self.assertEqual(kwargs['env']['POSTGRES_PASSWORD'], self.config['POSTGRES_PASSWORD'])
            self.assertEqual(kwargs['env']['HR_STATE_DIR'], self.config['HR_STATE_DIR'])
            self.assertIn(str(p.ROOT / 'compose.production.yaml'), args[0])
            self.assertIn('/dev/null', args[0])

    def test_compose_failure_redacts_all_configuration_secrets(self):
        result = subprocess.CompletedProcess([], 1, '', ' '.join(self.config[key] for key in p.SECRET_KEYS))
        output = io.StringIO()
        with patch.object(p.subprocess, 'run', return_value=result), contextlib.redirect_stdout(output), self.assertRaises(RuntimeError):
            p.compose(self.config, 'config', '--quiet')
        for key in p.SECRET_KEYS:
            self.assertNotIn(self.config[key], output.getvalue())
        self.assertEqual(output.getvalue().count('[REDACTED]'), 3)

    def test_release_update_preserves_key_identity_and_private_mode(self):
        before = (self.root / 'state/identity.json').read_bytes()
        with patch.object(p.sys, 'argv', ['production.py', '--config', self.args.config, 'set-release', '--release', 'next-commit']), patch.object(p, 'load', return_value=dict(self.config)):
            p.main()
        updated = p.load(self.args.config, tls=False)
        self.assertEqual(updated['HR_RELEASE_TAG'], 'next-commit')
        self.assertEqual(updated['AUTH_ENCRYPTION_KEY'], self.config['AUTH_ENCRYPTION_KEY'])
        self.assertEqual((self.root / 'state/identity.json').read_bytes(), before)
        self.assertEqual(Path(self.args.config).stat().st_mode & 0o777, 0o600)


if __name__ == '__main__':
    unittest.main()
