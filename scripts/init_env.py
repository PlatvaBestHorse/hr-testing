#!/usr/bin/env python3
"""Generate local credentials once, never print or overwrite them."""
from pathlib import Path
import os
import secrets

root = Path(__file__).resolve().parent.parent
target = root / '.env'
if target.exists():
    print('Existing .env preserved.')
else:
    content = (root / '.env.example').read_text()
    content = content.replace('POSTGRES_PASSWORD=\n', f'POSTGRES_PASSWORD={secrets.token_hex(24)}\n')
    content = content.replace('APP_DB_PASSWORD=\n', f'APP_DB_PASSWORD={secrets.token_hex(24)}\n')
    content = content.replace('AUTH_ENCRYPTION_KEY=\n', f'AUTH_ENCRYPTION_KEY={secrets.token_hex(32)}\n')
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as out:
        out.write(content)
    print('Local .env created with unique credentials (mode 0600).')

# Upgrade older local configs once. The marker prevents silently replacing a
# lost encryption key after MFA secrets or recovery mail have been encrypted.
values = dict(line.split('=', 1) for line in target.read_text().splitlines() if '=' in line and not line.lstrip().startswith('#'))
marker = root / '.env.auth-key-installed'
if not values.get('AUTH_ENCRYPTION_KEY'):
    if marker.exists():
        raise SystemExit('Original AUTH_ENCRYPTION_KEY is missing; recover it from protected configuration.')
    with target.open('a') as out:
        out.write('\nAUTH_ENCRYPTION_KEY=' + secrets.token_hex(32) + '\n')
        if 'AUTH_MAIL_MODE' not in values: out.write('AUTH_MAIL_MODE=file\n')
        if 'APP_PUBLIC_URL' not in values: out.write('APP_PUBLIC_URL=http://127.0.0.1:8088\n')
    print('Local account encryption key added without changing database credentials.')
if not marker.exists():
    fd = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
mailbox = root / '.mailbox'
mailbox.mkdir(mode=0o700, exist_ok=True)
if mailbox.is_symlink(): raise SystemExit('Mailbox must not be a symlink')
mailbox.chmod(0o700)
values = dict(line.split('=', 1) for line in target.read_text().splitlines() if '=' in line and not line.lstrip().startswith('#'))
local_uid = str(os.getuid() or 10001)
local_gid = str(os.getgid() if os.getuid() else 10001)
with target.open('a') as out:
    if 'API_UID' not in values: out.write('API_UID=' + local_uid + '\n')
    if 'API_GID' not in values: out.write('API_GID=' + local_gid + '\n')
if os.getuid() == 0:
    os.chown(mailbox, int(values.get('API_UID', local_uid)), int(values.get('API_GID', local_gid)))
