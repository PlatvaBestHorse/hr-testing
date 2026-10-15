#!/usr/bin/env python3
"""Scheduled production backups and local health checks; no external service is enabled."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time

import production as p

ARTIFACT = re.compile(r'(hr-[A-Za-z0-9-]+\.hrbak|erasure-[A-Za-z0-9-]+\.hrledger)')


def directory(path):
    if path.is_symlink() or not path.is_dir() or stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise ValueError('Operations directories must exist, be private (0700) and not be symlinks.')
    return path


def read_json(path):
    if path.is_symlink():
        raise ValueError('Operations files must not be symlinks.')
    p.private_file(path)
    return json.loads(path.read_text())


def atomic_json(path, data):
    fd, name = tempfile.mkstemp(prefix='.operations-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as out:
            json.dump(data, out, sort_keys=True)
            out.write('\n')
            out.flush()
            os.fsync(out.fileno())
        os.replace(name, path)
        sync_directory(path.parent)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def identity(config):
    return {'project': config['HR_PROJECT'], 'key_identity': p.fingerprint(config)}


def load(config, path):
    settings = read_json(p.outside_checkout(path))
    if not isinstance(settings, dict) or set(settings) != {'version', 'identity', 'replica_dir', 'rehearsal', 'alert_program'}:
        raise ValueError('Unexpected operations configuration.')
    if settings['version'] != 1 or settings['identity'] != identity(config) or type(settings['rehearsal']) is not bool:
        raise ValueError('Operations configuration belongs to a different installation.')
    if not isinstance(settings['replica_dir'], str):
        raise ValueError('Invalid replica directory.')
    replica = p.outside_checkout(settings['replica_dir'])
    state = Path(config['HR_STATE_DIR'])
    if replica == state or state in replica.parents or replica in state.parents:
        raise ValueError('Replica directory must be separate from application state.')
    hook = settings['alert_program']
    if hook is not None:
        if not isinstance(hook, str):
            raise ValueError('Invalid alert program.')
        program = p.outside_checkout(hook)
        p.private_file(program)
        if program.is_symlink() or not os.access(program, os.X_OK):
            raise ValueError('Alert program must be a private executable outside the checkout.')
    return settings


def check_replica(config, settings):
    replica = directory(Path(settings['replica_dir']))
    if not settings['rehearsal'] and replica.stat().st_dev == Path(config['HR_STATE_DIR']).stat().st_dev:
        raise ValueError('Independent replica filesystem is absent. Do not fall back to the application disk.')
    if read_json(replica / '.hr-replica.json') != identity(config):
        raise ValueError('Replica identity is missing or differs from this installation.')


def configure(config, args):
    path = p.outside_checkout(args.ops)
    directory(path.parent)
    replica = directory(p.outside_checkout(args.replica_dir))
    state = Path(config['HR_STATE_DIR'])
    settings = dict(version=1, identity=identity(config), replica_dir=str(replica),
                    rehearsal=args.rehearsal, alert_program=args.alert_program)
    # Validate before publishing either file. Existing markers are never replaced.
    marker = replica / '.hr-replica.json'
    if marker.exists() and read_json(marker) != identity(config):
        raise ValueError('Replica already belongs to another installation.')
    if path.exists() or path.is_symlink():
        raise ValueError('Operations configuration exists; preserve it.')
    if replica == state or state in replica.parents or replica in state.parents:
        raise ValueError('Replica directory must be separate from application state.')
    if not args.rehearsal and replica.stat().st_dev == state.stat().st_dev:
        raise ValueError('Use an independent mounted filesystem; --rehearsal is only for synthetic checks.')
    if not marker.exists():
        p.write_private(marker, identity(config))
    p.write_private(path, settings)
    try:
        check_replica(config, load(config, path))
    except Exception:
        path.unlink()
        raise
    print('Operations configured. Timers and external notifications have not been enabled.')


def digest(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError('Backup artifacts must be private regular files.')
        checksum = hashlib.file_digest(source, 'sha256').hexdigest()
    return {'name': path.name, 'sha256': checksum, 'size': info.st_size}


def replicate(source, destination):
    if not ARTIFACT.fullmatch(source.name):
        raise ValueError('Only encrypted archives and erasure journals may be replicated.')
    expected = digest(source)
    target = destination / source.name
    if target.exists() or target.is_symlink():
        if digest(target) != expected:
            raise ValueError('Existing replica differs; it will not be overwritten.')
        return expected
    fd, name = tempfile.mkstemp(prefix='.incomplete-', dir=destination)
    try:
        source_fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(source_fd, 'rb') as src, os.fdopen(fd, 'wb') as out:
            shutil.copyfileobj(src, out, 1024 * 1024)
            out.flush()
            os.fsync(out.fileno())
        copied = digest(Path(name))
        if copied['sha256'] != expected['sha256'] or copied['size'] != expected['size']:
            raise ValueError('Replica checksum mismatch.')
        # Publish without replacing another writer's completed file.
        os.link(name, target)
        sync_directory(destination)
        return expected
    finally:
        os.unlink(name)


@contextlib.contextmanager
def lock(state):
    fd = os.open(state / 'operations.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Another backup/journal operation is running; retry on the next timer.') from None
        yield
    finally:
        os.close(fd)


def receipt_path(config):
    return Path(config['HR_STATE_DIR']) / 'operations-receipt.json'


def receipt(config):
    path = receipt_path(config)
    value = read_json(path) if path.exists() else {'identity': identity(config)}
    if value.get('identity') != identity(config):
        raise ValueError('Operations receipt identity mismatch.')
    return value


def new_artifact(config, mode, suffix):
    archives = Path(config['HR_STATE_DIR']) / 'backup/.backups'
    before = set(archives.glob('*' + suffix))
    p.backup(config, mode)
    created = set(archives.glob('*' + suffix)) - before
    if len(created) != 1:
        raise RuntimeError('Backup operator did not publish exactly one new artifact.')
    return created.pop()


def scheduled(config, settings, mode):
    state = directory(Path(config['HR_STATE_DIR']))
    with lock(state):
        saved = receipt(config)
        destination = Path(settings['replica_dir'])
        if mode == 'backup':
            archive = new_artifact(config, 'create', '.hrbak')
            check_replica(config, settings)
            saved['backup'] = dict(at=time.time(), artifact=replicate(archive, destination))
        journal = new_artifact(config, 'erasures', '.hrledger')
        check_replica(config, settings)
        saved['ledger'] = dict(at=time.time(), artifact=replicate(journal, destination))
        atomic_json(receipt_path(config), saved)
    print(json.dumps({'event': mode + '_completed', 'project': config['HR_PROJECT'], 'rehearsal': settings['rehearsal']}))


def health(config, settings, now=None):
    now = time.time() if now is None else now
    issues = []
    state = Path(config['HR_STATE_DIR'])
    for label, path in (('state', state), ('replica', Path(settings['replica_dir']))):
        usage = shutil.disk_usage(path)
        if usage.free < max(2 * 1024**3, usage.total // 10):
            issues.append(label + '_disk_low')
    # No raw logs, contacts, SQL or invitation URLs enter the health event.
    status = p.compose(config, 'ps', '--format', 'json', capture=True, check=False, timeout=20)
    try:
        parsed = json.loads(status.stdout) if status.stdout.lstrip().startswith('[') else [json.loads(line) for line in status.stdout.splitlines() if line.strip()]
        services = {item['Service']: item for item in parsed}
        for name in ('db', 'api', 'web'):
            if services.get(name, {}).get('State') != 'running' or services.get(name, {}).get('Health') != 'healthy':
                issues.append(name + '_unhealthy')
    except (ValueError, TypeError, KeyError):
        issues.append('compose_status_unreadable')
    if status.returncode:
        issues.append('compose_status_failed')
    ready = p.compose(config, 'exec', '-T', 'api', 'wget', '-q', '-O', '-', 'http://127.0.0.1:8080/readyz', capture=True, check=False, timeout=20)
    if ready.returncode:
        issues.append('api_readiness_failed')
    try:
        p.load_tls_certificate(config, minimum_seconds=7 * 86400)
    except (ValueError, OSError):
        issues.append('tls_invalid_or_expires_within_7_days')
    saved = receipt(config)
    for kind, maximum in (('backup', 26 * 3600), ('ledger', 15 * 60)):
        entry = saved.get(kind)
        if not entry:
            issues.append(kind + '_missing')
            continue
        if not isinstance(entry.get('at'), (int, float)) or not 0 <= now - entry['at'] <= maximum:
            issues.append(kind + '_stale')
        artifact = entry.get('artifact', {})
        name = artifact.get('name', '')
        if not isinstance(name, str) or not ARTIFACT.fullmatch(name):
            issues.append(kind + '_receipt_invalid')
            continue
        for label, parent in (('local', state / 'backup/.backups'), ('replica', Path(settings['replica_dir']))):
            try:
                if digest(parent / name) != artifact:
                    issues.append(kind + '_' + label + '_damaged')
            except (OSError, ValueError):
                issues.append(kind + '_' + label + '_missing_or_unsafe')
    if settings['rehearsal']:
        issues.append('rehearsal_replica_not_independent')
    return sorted(set(issues))


def notify(config, settings, issues):
    path = Path(config['HR_STATE_DIR']) / 'operations-health.json'
    previous = read_json(path) if path.exists() else {}
    event = {'event': 'health', 'project': config['HR_PROJECT'], 'at': time.time(), 'issues': issues}
    changed = previous.get('issues') != issues
    repeat = bool(issues) and event['at'] - previous.get('notified_at', 0) >= 3600
    delivered = previous.get('notified_at', 0)
    if settings.get('alert_program') and (changed or repeat):
        try:
            result = subprocess.run([settings['alert_program']], input=json.dumps(event), text=True,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10, check=False)
            if result.returncode:
                raise RuntimeError('Alert program failed.')
            delivered = event['at']
        except (OSError, subprocess.TimeoutExpired, RuntimeError):
            event['issues'] = sorted(set(issues + ['notification_failed']))
    event['notified_at'] = delivered
    atomic_json(path, event)
    print(json.dumps(event, sort_keys=True))
    return bool(event['issues'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--ops', required=True)
    commands = parser.add_subparsers(dest='command', required=True)
    setup = commands.add_parser('configure')
    setup.add_argument('--replica-dir', required=True)
    setup.add_argument('--alert-program', default=None, help='Optional private executable, JSON stdin; operator enables delivery')
    setup.add_argument('--rehearsal', action='store_true', help='Synthetic data only: permit a replica on the same filesystem')
    for name in ('backup', 'ledger', 'check'):
        commands.add_parser(name)
    args = parser.parse_args()
    config = p.load(args.config, tls=False)
    if args.command == 'configure':
        configure(config, args)
        return 0
    settings = None
    try:
        settings = load(config, args.ops)
        check_replica(config, settings)
        if args.command == 'check':
            return int(notify(config, settings, health(config, settings)))
        scheduled(config, settings, args.command)
        return 0
    except (ValueError, OSError, RuntimeError, subprocess.TimeoutExpired):
        # A missing replica also raises an alert through the previously validated
        # local hook when available. Never include private exception text.
        if settings:
            notify(config, settings, [args.command + '_failed'])
        else:
            print(json.dumps({'event': 'operations_failed', 'project': config['HR_PROJECT'], 'issues': ['configuration_or_replica_unavailable']}))
        return 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError, RuntimeError):
        print('Operations failed; inspect private configuration, filesystem and Docker.', file=sys.stderr)
        sys.exit(1)
