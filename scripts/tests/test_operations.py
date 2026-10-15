import argparse
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import operations as o
import production as p


class OperationsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='hr-ops-test-')
        self.root = Path(self.temp.name)
        self.root.chmod(0o700)
        self.state = self.root / 'state'
        self.state.mkdir(mode=0o700)
        self.replica = self.root / 'replica'
        self.replica.mkdir(mode=0o700)
        self.config = dict(HR_STATE_DIR=str(self.state), HR_PROJECT='hr-ops-test',
                           POSTGRES_PASSWORD='test-db', APP_DB_PASSWORD='test-app', AUTH_ENCRYPTION_KEY='test-auth')
        self.path = self.root / 'ops.json'
        args = argparse.Namespace(ops=str(self.path), replica_dir=str(self.replica), rehearsal=True, alert_program=None)
        o.configure(self.config, args)
        self.settings = o.load(self.config, self.path)
        self.archives = self.state / 'backup/.backups'
        self.archives.mkdir(parents=True, mode=0o700)

    def tearDown(self):
        self.temp.cleanup()

    def artifact(self, name, data=b'encrypted-fixture'):
        path = self.archives / name
        path.write_bytes(data)
        path.chmod(0o600)
        return path

    def test_replica_refuses_missing_identity_wrong_installation_and_same_disk(self):
        o.check_replica(self.config, self.settings)
        strict = dict(self.settings, rehearsal=False)
        with self.assertRaises(ValueError):
            o.check_replica(self.config, strict)
        (self.replica / '.hr-replica.json').unlink()
        with self.assertRaises(ValueError):
            o.check_replica(self.config, self.settings)
        o.atomic_json(self.replica / '.hr-replica.json', {'project': 'another'})
        with self.assertRaises(ValueError):
            o.check_replica(self.config, self.settings)

    def test_atomic_replica_rejects_changed_files_symlinks_and_plaintext(self):
        source = self.artifact('hr-test.hrbak')
        copied = o.replicate(source, self.replica)
        self.assertEqual(o.digest(self.replica / source.name), copied)
        self.assertEqual((self.replica / source.name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(o.replicate(source, self.replica), copied)
        (self.replica / source.name).write_bytes(b'damaged')
        with self.assertRaises(ValueError):
            o.replicate(source, self.replica)
        self.assertEqual((self.replica / source.name).read_bytes(), b'damaged')
        for name in ('.env.backup.key', 'config.json'):
            with self.assertRaises(ValueError):
                o.replicate(self.artifact(name), self.replica)
        link = self.archives / 'hr-link.hrbak'
        link.symlink_to(source)
        with self.assertRaises(OSError):
            o.replicate(link, self.replica)
        self.assertFalse(list(self.replica.glob('.incomplete-*')))

    def test_failed_replication_does_not_advance_success_receipt(self):
        previous = {'identity': o.identity(self.config), 'backup': {'at': 1}}
        o.atomic_json(o.receipt_path(self.config), previous)
        archive = self.artifact('hr-new.hrbak')
        with patch.object(o, 'new_artifact', return_value=archive), patch.object(o, 'replicate', side_effect=OSError('disk full')):
            with self.assertRaises(OSError):
                o.scheduled(self.config, self.settings, 'backup')
        self.assertEqual(o.receipt(self.config), previous)

    def test_backup_and_ledger_are_serialized(self):
        with o.lock(self.state):
            with self.assertRaises(RuntimeError):
                with o.lock(self.state):
                    self.fail('concurrent operation admitted')
        with o.lock(self.state):
            pass

    def test_health_detects_stale_journal_corrupt_replica_and_down_api(self):
        saved = {'identity': o.identity(self.config)}
        for kind, name in (('backup', 'hr-test.hrbak'), ('ledger', 'erasure-test.hrledger')):
            saved[kind] = {'at': 10000, 'artifact': o.replicate(self.artifact(name), self.replica)}
        o.atomic_json(o.receipt_path(self.config), saved)
        healthy = subprocess.CompletedProcess([], 0, json.dumps([{'Service': n, 'State': 'running', 'Health': 'healthy'} for n in ('db', 'api', 'web')]), '')
        ready = subprocess.CompletedProcess([], 0, 'ready', '')
        ample = type('Usage', (), {'total': 100 * 1024**3, 'free': 50 * 1024**3})()
        with patch.object(o.shutil, 'disk_usage', return_value=ample), patch.object(p, 'load_tls_certificate'), patch.object(p, 'compose', side_effect=[healthy, ready]):
            self.assertEqual(o.health(self.config, self.settings, now=10001), ['rehearsal_replica_not_independent'])
        (self.replica / 'hr-test.hrbak').write_bytes(b'corrupted')
        down = subprocess.CompletedProcess([], 1, '', '')
        with patch.object(o.shutil, 'disk_usage', return_value=ample), patch.object(p, 'load_tls_certificate', side_effect=ValueError()), patch.object(p, 'compose', side_effect=[down, down]):
            issues = o.health(self.config, self.settings, now=12000)
        for expected in ('ledger_stale', 'backup_replica_damaged', 'api_unhealthy', 'api_readiness_failed', 'tls_invalid_or_expires_within_7_days'):
            self.assertIn(expected, issues)

    def test_alert_hook_uses_json_without_shell_deduplicates_and_reports_recovery(self):
        settings = dict(self.settings, alert_program='/private/operator-hook')
        success = subprocess.CompletedProcess([], 0)
        with patch.object(o.subprocess, 'run', return_value=success) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(o.notify(self.config, settings, ['api_unhealthy']))
            self.assertTrue(o.notify(self.config, settings, ['api_unhealthy']))
            self.assertEqual(run.call_count, 1)
            self.assertFalse(o.notify(self.config, settings, []))
            self.assertEqual(run.call_count, 2)
            args, kwargs = run.call_args
            self.assertEqual(args[0], ['/private/operator-hook'])
            self.assertFalse(kwargs.get('shell', False))
            self.assertEqual(json.loads(kwargs['input'])['issues'], [])
            self.assertNotIn('test-auth', kwargs['input'])

    def test_notification_failure_is_visible_and_retried(self):
        settings = dict(self.settings, alert_program='/private/operator-hook')
        with patch.object(o.subprocess, 'run', side_effect=subprocess.TimeoutExpired('hook', 10)) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(o.notify(self.config, settings, ['api_unhealthy']))
            self.assertIn('notification_failed', o.read_json(self.state / 'operations-health.json')['issues'])
            o.notify(self.config, settings, ['api_unhealthy'])
            self.assertEqual(run.call_count, 2)

    def test_missing_replica_reaches_real_local_alert_program(self):
        sink = self.root / 'events.jsonl'
        hook = self.root / 'alert-hook'
        hook.write_text('#!/usr/bin/env python3\nimport json, sys\nfrom pathlib import Path\nevent=json.load(sys.stdin)\nwith Path(' + repr(str(sink)) + ').open("a") as f: f.write(json.dumps(event)+"\\n")\n')
        hook.chmod(0o700)
        self.settings['alert_program'] = str(hook)
        o.atomic_json(self.path, self.settings)
        (self.replica / '.hr-replica.json').unlink()
        argv = ['operations.py', '--config', str(self.root / 'config.json'), '--ops', str(self.path), 'check']
        with patch.object(o.sys, 'argv', argv), patch.object(p, 'load', return_value=self.config), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(o.main(), 1)
        events = [json.loads(line) for line in sink.read_text().splitlines()]
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]['issues'], ['check_failed'])


if __name__ == '__main__':
    unittest.main()
