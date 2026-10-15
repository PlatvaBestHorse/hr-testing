#!/usr/bin/env python3
"""Offline-CA HTTPS rehearsal using only its own temporary database and containers."""
import argparse
from http.client import HTTPConnection, HTTPSConnection, RemoteDisconnected
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import tempfile

import production as p


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Certificate preparation failed (private output suppressed).')


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class LocalTLS(HTTPSConnection):
    def connect(self):
        raw = socket.create_connection(('127.0.0.1', self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)


def main():
    os.umask(0o077)
    root = Path(tempfile.mkdtemp(prefix='hr-production-smoke-'))
    project = 'hr-prod-test-' + secrets.token_hex(4)
    config = None
    try:
        ca, cert, key = root / 'ca.pem', root / 'cert.pem', root / 'key.pem'
        run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=HR smoke CA', '-keyout', str(root / 'ca-key.pem'), '-out', str(ca))
        run('openssl', 'req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=hr-testing.test', '-keyout', str(key), '-out', str(root / 'req.pem'))
        (root / 'extensions').write_text('subjectAltName=DNS:hr-testing.test\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n')
        run('openssl', 'x509', '-req', '-in', str(root / 'req.pem'), '-CA', str(ca), '-CAkey', str(root / 'ca-key.pem'), '-CAcreateserial', '-days', '2', '-extfile', str(root / 'extensions'), '-out', str(cert))
        args = argparse.Namespace(config=str(root / 'config.json'), state=str(root / 'state'),
            host='hr-testing.test', cert=str(cert), key=str(key), release='smoke', project=project,
            http_port=free_port(), https_port=free_port(), bind='127.0.0.1',
            subnet='172.30.252.0/24', web_ip='172.30.252.10')
        p.initialize(args)
        config = p.load(args.config)
        p.compose(config, 'config', '--quiet')
        p.compose(config, 'build', 'api', 'web', 'admin')
        p.start(config)
        credentials = {'organization_name': 'Synthetic production rehearsal', 'display_name': 'Synthetic admin',
                       'email': 'admin@production.test', 'password': secrets.token_urlsafe(24)}
        bootstrap = json.dumps(credentials)
        p.compose(config, 'run', '--rm', '--no-deps', '-T', 'admin', '/app/bootstrap', input_data=bootstrap)
        duplicate = p.compose(config, 'run', '--rm', '--no-deps', '-T', 'admin', '/app/bootstrap', input_data=bootstrap, capture=True, check=False)
        assert duplicate.returncode, 'Bootstrap must refuse a populated database'
        context = ssl.create_default_context(cafile=str(ca))
        origin, cookie, csrf = config['HR_PUBLIC_ORIGIN'], '', ''

        def request(method, path, data=None, expected=200, authenticated=True):
            nonlocal cookie
            client = LocalTLS('hr-testing.test', int(config['HR_HTTPS_PORT']), context=context, timeout=20)
            headers = {'Origin': origin, 'Content-Type': 'application/json'}
            if authenticated:
                headers.update(Cookie=cookie, **{'X-CSRF-Token': csrf})
            client.request(method, path, body=json.dumps(data) if data is not None else None, headers=headers)
            response = client.getresponse()
            body, response_headers, status = response.read(), dict(response.getheaders()), response.status
            client.close()
            assert status == expected, 'Unexpected HTTPS response status for ' + path
            if response_headers.get('Set-Cookie'):
                parsed = SimpleCookie(response_headers['Set-Cookie'])
                assert parsed['hr_session']['secure'] and parsed['hr_session']['httponly']
                cookie = 'hr_session=' + parsed['hr_session'].value
            assert 'max-age=' in response_headers.get('Strict-Transport-Security', '')
            return json.loads(body) if body else None

        assert request('GET', '/api/v1/status')['status'] == 'ready'
        request('GET', '/api/v1/questions', expected=401, authenticated=False)
        login = request('POST', '/api/v1/auth/login', {'email': credentials['email'], 'password': credentials['password']})
        csrf = login['csrf_token']
        assert request('GET', '/api/v1/me')['role'] == 'org_admin'
        question = request('POST', '/api/v1/questions', {'body': 'Persistent synthetic question', 'type': 'single_choice', 'difficulty': 1,
            'options': [{'label': 'A', 'is_correct': True}, {'label': 'B', 'is_correct': False}]}, expected=201)
        # CSRF and exact Origin are still required over HTTPS.
        saved = csrf
        csrf = ''
        request('POST', '/api/v1/questions', {}, expected=403)
        csrf = saved
        client = HTTPConnection('127.0.0.1', int(config['HR_HTTP_PORT']), timeout=10)
        client.request('GET', '/login?check=1', headers={'Host': 'hr-testing.test'})
        response = client.getresponse()
        assert response.status == 308 and response.getheader('Location') == origin + '/login?check=1'
        response.read()
        client.close()
        client = HTTPConnection('127.0.0.1', int(config['HR_HTTP_PORT']), timeout=10)
        client.request('GET', '/', headers={'Host': 'untrusted.test'})
        try:
            client.getresponse()
            raise AssertionError('Unknown HTTP host accepted')
        except RemoteDisconnected:
            pass
        finally:
            client.close()
        # A real stop/start with persistent DB, followed by idempotent migration.
        p.compose(config, 'down')
        p.start(config)
        assert request('GET', '/api/v1/me')['role'] == 'org_admin'
        assert request('GET', '/api/v1/questions/' + question['id'])['id'] == question['id']
        assert p.load(args.config)['AUTH_ENCRYPTION_KEY'] == config['AUTH_ENCRYPTION_KEY']
        # Operator image contains the existing maintenance commands; it has no Go toolchain.
        password = secrets.token_urlsafe(24)
        p.compose(config, 'run', '--rm', '--no-deps', '-T', 'admin', '/app/set-password',
                  input_data=json.dumps({'email': credentials['email'], 'password': password}))
        request('GET', '/api/v1/me', expected=401)
        csrf = request('POST', '/api/v1/auth/login', {'email': credentials['email'], 'password': password})['csrf_token']
        org = request('GET', '/api/v1/me')['organization_id']
        p.compose(config, 'exec', '-T', 'db', 'psql', '-U', 'hr_owner', '-d', 'hr_testing', '-v', 'ON_ERROR_STOP=1',
                  '-c', "UPDATE organizations SET status='suspended' WHERE id='" + org + "'", capture=True)
        request('GET', '/api/v1/me', expected=401)
        resume_input = json.dumps({'organization_id': org})
        denied = p.compose(config, 'run', '--rm', '--no-deps', '-T', 'admin', '/app/org-resume', input_data=resume_input, capture=True, check=False)
        assert denied.returncode, 'App-role operator must not resume organizations'
        p.compose(config, 'run', '--rm', '--no-deps', '-T', 'maintenance', '/app/org-resume', input_data=resume_input)
        request('GET', '/api/v1/me', expected=401)
        csrf = request('POST', '/api/v1/auth/login', {'email': credentials['email'], 'password': password})['csrf_token']
        assert request('GET', '/api/v1/me')['role'] == 'org_admin'
        inspection = p.compose(config, 'ps', '--format', 'json', capture=True).stdout
        # API and DB must not publish any host port.
        entries = [json.loads(line) for line in inspection.splitlines() if line.strip()]
        assert all(not port.get('PublishedPort') for item in entries if item['Service'] in ('db', 'api') for port in (item.get('Publishers') or []))
        role = p.compose(config, 'exec', '-T', 'db', 'psql', '-U', 'hr_owner', '-d', 'hr_testing', '-Atc',
                         "SELECT rolsuper OR rolcreatedb OR rolcreaterole FROM pg_roles WHERE rolname='hr_app'", capture=True)
        assert role.stdout.strip() == 'f'
        print('PASS: verified HTTPS, Secure session, bootstrap/password/resume operators, CSRF, private ports and data/session persistence after full restart.')
    finally:
        if config:
            stopped = p.compose(config, 'down', '--remove-orphans', check=False)
            if stopped.returncode:
                raise RuntimeError('Temporary stack could not stop; files retained: ' + str(root))
            # Only this random temporary PG bind is mounted; never any existing DB.
            cleanup = subprocess.run(['docker', 'run', '--rm', '--user', '0', '--entrypoint', 'sh',
                '-v', str(root / 'state/postgres') + ':/cleanup', 'postgres:17-bookworm',
                '-c', 'find /cleanup -mindepth 1 -delete && chown "$1:$2" /cleanup', 'cleanup',
                str(os.getuid()), str(os.getgid())], capture_output=True)
            if cleanup.returncode:
                raise RuntimeError('Temporary PostgreSQL files retained for manual cleanup: ' + str(root))
        shutil.rmtree(root)


if __name__ == '__main__':
    main()
