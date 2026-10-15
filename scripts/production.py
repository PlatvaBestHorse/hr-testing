#!/usr/bin/env python3
"""Explicit, standalone Compose operations; configuration is private JSON, not shell code."""
import argparse
import getpass
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import stat
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parent.parent
SECRET_KEYS = ('POSTGRES_PASSWORD', 'APP_DB_PASSWORD', 'AUTH_ENCRYPTION_KEY')
FIELDS = set(SECRET_KEYS) | {
    'HR_PROJECT', 'HR_IMAGE_PREFIX', 'HR_RELEASE_TAG', 'HR_STATE_DIR', 'HR_TLS_CERT',
    'HR_TLS_KEY', 'HR_PUBLIC_HOST', 'HR_PUBLIC_ORIGIN', 'HR_HTTP_PORT', 'HR_HTTPS_PORT',
    'HR_BIND_ADDRESS', 'HR_DOCKER_SUBNET', 'HR_WEB_INTERNAL_IP',
}


def outside_checkout(path):
    path = Path(path).expanduser()
    if not path.is_absolute():
        raise ValueError('Use absolute paths outside the source checkout.')
    path = path.resolve()
    if path == ROOT or ROOT in path.parents:
        raise ValueError('Production configuration, keys and data must be outside the checkout.')
    return path


def private_file(path):
    if not path.is_file() or stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise ValueError('Configuration/key file must exist and be private (0600 or 0400).')


def fingerprint(config):
    return hashlib.sha256(json.dumps({k: config[k] for k in SECRET_KEYS}, sort_keys=True).encode()).hexdigest()


def write_private(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as out:
        json.dump(data, out, indent=2, ensure_ascii=False)
        out.write('\n')


def validate(config):
    if not isinstance(config, dict) or set(config) != FIELDS or any(not isinstance(v, str) for v in config.values()):
        raise ValueError('Unexpected or missing configuration fields.')
    for key, size in zip(SECRET_KEYS, (48, 48, 64)):
        if not re.fullmatch('[0-9a-f]{' + str(size) + '}', config[key]):
            raise ValueError('Invalid secret format; recover the original configuration, do not regenerate keys.')
    host = config['HR_PUBLIC_HOST']
    if len(host) > 253 or '.' not in host or any(not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in host.split('.')):
        raise ValueError('Use a DNS hostname without scheme, port, wildcard or path.')
    for key in ('HR_PROJECT', 'HR_IMAGE_PREFIX', 'HR_RELEASE_TAG'):
        if not re.fullmatch(r'[a-z0-9][a-z0-9_.-]{0,62}', config[key]):
            raise ValueError('Invalid project/image/release identifier.')
    if config['HR_PROJECT'] == 'hr-testing':
        raise ValueError('The development Compose project name is reserved.')
    try:
        ports = [int(config[k]) for k in ('HR_HTTP_PORT', 'HR_HTTPS_PORT')]
    except ValueError:
        raise ValueError('Invalid HTTP/HTTPS port format.') from None
    if any(p < 1 or p > 65535 for p in ports) or ports[0] == ports[1]:
        raise ValueError('HTTP and HTTPS ports must be distinct and between 1 and 65535.')
    if config['HR_BIND_ADDRESS'] not in ('127.0.0.1', '0.0.0.0'):
        raise ValueError('Unsupported bind address.')
    suffix = '' if ports[1] == 443 else ':' + str(ports[1])
    if config['HR_PUBLIC_ORIGIN'] != 'https://' + host + suffix:
        raise ValueError('Public origin must exactly match the HTTPS hostname and port.')
    try:
        network = ipaddress.ip_network(config['HR_DOCKER_SUBNET'])
        web = ipaddress.ip_address(config['HR_WEB_INTERNAL_IP'])
    except ValueError:
        raise ValueError('Invalid Docker subnet or web address.') from None
    if network.version != 4 or not network.is_private or network.prefixlen < 24 or web not in network or web in (network.network_address, network.broadcast_address, network.network_address + 1):
        raise ValueError('Use a private IPv4 subnet /24 or smaller and a free web address inside it.')
    state = outside_checkout(config['HR_STATE_DIR'])
    if state == Path('/') or len(state.parts) < 3:
        raise ValueError('Use a dedicated state directory.')
    for key in ('HR_TLS_CERT', 'HR_TLS_KEY'):
        outside_checkout(config[key])
    return state


def initialize(args):
    path = outside_checkout(args.config)
    state = outside_checkout(args.state)
    port = str(args.https_port)
    config = dict(zip(SECRET_KEYS, (secrets.token_hex(24), secrets.token_hex(24), secrets.token_hex(32))))
    config.update(HR_PROJECT=args.project, HR_IMAGE_PREFIX=args.project, HR_RELEASE_TAG=args.release,
                  HR_STATE_DIR=str(state), HR_TLS_CERT=str(outside_checkout(args.cert)),
                  HR_TLS_KEY=str(outside_checkout(args.key)), HR_PUBLIC_HOST=args.host,
                  HR_PUBLIC_ORIGIN='https://' + args.host + ('' if port == '443' else ':' + port),
                  HR_HTTP_PORT=str(args.http_port), HR_HTTPS_PORT=port, HR_BIND_ADDRESS=args.bind,
                  HR_DOCKER_SUBNET=args.subnet, HR_WEB_INTERNAL_IP=args.web_ip)
    validate(config)
    # Refuse any existing state, even when its key/configuration has been lost.
    if path.exists() or state.exists():
        raise ValueError('Configuration/state already exists. Preserve it; recover original keys instead of reinitializing.')
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if stat.S_IMODE(path.parent.stat().st_mode) & 0o077:
        raise ValueError('Configuration parent directory must be private (0700).')
    state.mkdir(mode=0o700, parents=True)
    (state / 'postgres').mkdir(mode=0o700)
    write_private(state / 'identity.json', {'secret_fingerprint': fingerprint(config), 'project': config['HR_PROJECT']})
    write_private(path, config)
    print('Private configuration created once. Store an independent protected copy of configuration and keys.')


def load(path, tls=True):
    path = outside_checkout(path)
    private_file(path)
    config = json.loads(path.read_text())
    state = validate(config)
    if not state.is_dir() or stat.S_IMODE(state.stat().st_mode) & 0o077:
        raise ValueError('State directory must exist and be private (0700).')
    identity = json.loads((state / 'identity.json').read_text())
    if identity != {'secret_fingerprint': fingerprint(config), 'project': config['HR_PROJECT']}:
        raise ValueError('Database/key identity mismatch. Restore the original configuration.')
    if tls:
        private_file(Path(config['HR_TLS_KEY']))
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(config['HR_TLS_CERT'], config['HR_TLS_KEY'], password=lambda: '')
        for options in (['-checkhost', config['HR_PUBLIC_HOST']], ['-checkend', '86400']):
            result = subprocess.run(['openssl', 'x509', '-in', config['HR_TLS_CERT'], '-noout', *options], capture_output=True)
            if result.returncode:
                raise ValueError('TLS certificate must match the hostname and remain valid for at least 24 hours.')
    return config


def compose(config, *args, input_data=None, capture=False, check=True):
    env = os.environ.copy()
    # Host/development .env and COMPOSE_FILE must never select a different DB.
    for key in list(env):
        if key.startswith(('HR_', 'COMPOSE_', 'POSTGRES_', 'APP_DB_', 'AUTH_ENCRYPTION_')) and key not in ('HR_BUILD_PROXY_HOST', 'HR_BUILD_PROXY_IP'):
            env.pop(key)
    env.update(config)
    command = ['docker', 'compose', '--env-file', '/dev/null', '-f', str(ROOT / 'compose.production.yaml')]
    if env.get('HR_BUILD_PROXY_IP'):
        command += ['-f', str(ROOT / 'compose.production.cloud.yaml')]
    result = subprocess.run([*command, *args], env=env, cwd=ROOT, input=input_data,
                            text=True, capture_output=True)
    # Never stream Compose errors: interpolation failures can include credentials.
    if not capture or (check and result.returncode):
        output = result.stdout + result.stderr
        for key in SECRET_KEYS:
            output = output.replace(config[key], '[REDACTED]')
        print(output, end='')
    if check and result.returncode:
        raise RuntimeError('Compose operation failed; configuration secrets were redacted.')
    return result


def start(config):
    compose(config, 'config', '--quiet')
    compose(config, 'up', '-d', '--wait', 'db')
    compose(config, 'run', '--rm', '--no-deps', '-T', 'migrate')
    # Recreate edge after API: Nginx resolves the new upstream address on startup.
    compose(config, 'up', '-d', '--no-deps', '--force-recreate', '--wait', 'api')
    compose(config, 'up', '-d', '--no-deps', '--force-recreate', '--wait', 'web')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, help='Private absolute JSON path outside the checkout')
    commands = parser.add_subparsers(dest='command', required=True)
    init = commands.add_parser('init')
    for flag in ('state', 'host', 'cert', 'key', 'release'):
        init.add_argument('--' + flag, required=True)
    init.add_argument('--project', default='hr-testing-prod')
    init.add_argument('--http-port', type=int, default=80)
    init.add_argument('--https-port', type=int, default=443)
    init.add_argument('--bind', default='0.0.0.0')
    init.add_argument('--subnet', default='172.30.253.0/24')
    init.add_argument('--web-ip', default='172.30.253.10')
    for name in ('check', 'build', 'up', 'status', 'down', 'admin', 'set-password', 'resume', 'reload-tls'):
        commands.add_parser(name)
    release = commands.add_parser('set-release')
    release.add_argument('--release', required=True)
    args = parser.parse_args()
    if args.command == 'init':
        initialize(args)
        return
    config = load(args.config, tls=args.command not in ('status', 'down'))
    if args.command == 'set-release':
        config['HR_RELEASE_TAG'] = args.release
        validate(config)
        path = outside_checkout(args.config)
        temporary = path.with_name(path.name + '.new-' + secrets.token_hex(4))
        write_private(temporary, config)
        os.replace(temporary, path)
        print('Release tag changed; build and up remain explicit operations. Database/key identity preserved.')
    elif args.command == 'check':
        compose(config, 'config', '--quiet')
        print('Configuration, key identity and certificate checked offline. Public TLS chain/DNS are not checked.')
    elif args.command == 'build':
        compose(config, 'build', 'api', 'web', 'admin')
    elif args.command == 'up':
        start(config)
    elif args.command == 'status':
        compose(config, 'ps')
    elif args.command == 'down':
        compose(config, 'down')  # No volumes or data deletion.
    elif args.command == 'reload-tls':
        # Recreate to pick up an atomically replaced bind-mounted certificate.
        compose(config, 'up', '-d', '--no-deps', '--force-recreate', '--wait', 'web')
    else:
        if args.command == 'resume':
            org = str(uuid.UUID(input('ID организации для возобновления: ').strip()))
            data, service, binary = {'organization_id': org}, 'maintenance', 'org-resume'
        else:
            data = {}
            if args.command == 'admin':
                data.update(organization_name=input('Название организации: ').strip(), display_name=input('Имя администратора: ').strip())
            data['email'] = input('Email: ').strip()
            data['password'] = getpass.getpass('Пароль (минимум 12 символов): ')
            if data['password'] != getpass.getpass('Повторите пароль: '):
                raise ValueError('Пароли не совпадают.')
            service, binary = 'admin', ('bootstrap' if args.command == 'admin' else 'set-password')
        compose(config, 'run', '--rm', '--no-deps', '-T', service, '/app/' + binary, input_data=json.dumps(data))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, RuntimeError) as error:
        # Avoid underlying exception text containing malformed private config.
        if isinstance(error, ValueError) and not isinstance(error, json.JSONDecodeError):
            print(str(error), file=sys.stderr)
        else:
            print('Operation failed. Check private configuration, certificate, permissions and Docker.', file=sys.stderr)
        sys.exit(1)
