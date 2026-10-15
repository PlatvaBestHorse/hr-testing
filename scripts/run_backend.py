#!/usr/bin/env python3
"""Launch a Go command with local database configuration, without shell expansion."""
import os
from pathlib import Path
import sys
from urllib.parse import quote

root = Path(__file__).resolve().parent.parent
values = {}
path = root / '.env'
if path.exists():
    for line in path.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            values[key] = value
environment = dict(os.environ)
for name in ('AUTH_ENCRYPTION_KEY', 'AUTH_MAIL_MODE', 'APP_PUBLIC_URL', 'SMTP_ADDR', 'SMTP_USERNAME', 'SMTP_PASSWORD', 'SMTP_FROM', 'CANDIDATE_IP_LIMIT_PER_MINUTE'):
    if name in values: environment.setdefault(name, values[name])
environment.setdefault('TRUSTED_PROXY_CIDRS', '127.0.0.1/32,::1/128')
environment.setdefault('AUTH_MAIL_DIR', str(root / '.mailbox'))
mode = sys.argv[1] if len(sys.argv) > 1 else 'api'
if mode not in ('api', 'migrate', 'bootstrap', 'set-password', 'integration', 'backup', 'org-resume'):
    raise SystemExit('Expected api, migrate, bootstrap, set-password, integration or backup')
def url(owner=False):
    user = values.get('POSTGRES_USER', 'hr_owner') if owner else 'hr_app'
    password = values.get('POSTGRES_PASSWORD' if owner else 'APP_DB_PASSWORD', '')
    if not password:
        raise SystemExit('Missing local database credentials. Run make init first, or supply DATABASE_URL.')
    db = quote(values.get('POSTGRES_DB', 'hr_testing'), safe='')
    port = values.get('DB_PORT', '5432')
    return f'postgres://{quote(user, safe="")}:{quote(password, safe="")}@127.0.0.1:{port}/{db}?sslmode=disable'
if mode == 'integration':
    environment['TEST_DATABASE_URL'] = url(True)
    environment['TEST_APP_DATABASE_URL'] = url(False)
    command = ['go', 'test', '-count=1', '-v', './internal/database', './internal/httpapi', './cmd/backup', './cmd/org-resume']
else:
    if 'DATABASE_URL' not in environment:
        environment['DATABASE_URL'] = url(mode in ('migrate', 'backup', 'org-resume'))
    environment.setdefault('HTTP_ADDR', '127.0.0.1:' + values.get('API_PORT', '8080'))
    environment.setdefault('COOKIE_SECURE', values.get('COOKIE_SECURE', 'false'))
    environment.setdefault('APP_ORIGINS', values.get('APP_ORIGINS', 'http://127.0.0.1:8088,http://localhost:8088,http://127.0.0.1:5173,http://localhost:5173'))
    command = ['go', 'run', './cmd/' + mode] + sys.argv[2:]
    if mode == 'backup':
        environment['HR_PROJECT_ROOT'] = str(root)
# Replace the helper so SIGTERM/SIGINT reach the Go launcher directly.
os.chdir(root / 'backend')
os.execvpe(command[0], command, environment)
