#!/usr/bin/env python3
"""Bootstrap interactively, or generate credentials for this local dev machine."""
import getpass
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

root = Path(__file__).resolve().parent.parent
dev = '--dev' in sys.argv
target = root / '.env.admin.json'
reset = '--reset-dev' in sys.argv
if dev and 'DATABASE_URL' in os.environ:
    raise SystemExit('Development bootstrap requires the local .env database, not an external DATABASE_URL.')
if reset and not dev:
    raise SystemExit('--reset-dev requires --dev')
if dev and target.exists() and not reset:
    print('Existing local administrator credentials preserved in .env.admin.json.')
    raise SystemExit(0)
if dev:
    if target.exists():
        data = json.loads(target.read_text())
    else:
        data = {'email': 'admin@local.test', 'password': secrets.token_urlsafe(24), 'display_name': 'Администратор', 'organization_name': 'Моя организация'}
        # Persist before creating an account so a filesystem error cannot lose its password.
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as out:
            json.dump(data, out, ensure_ascii=False)
else:
    data = {'organization_name': input('Название организации: ').strip(), 'display_name': input('Имя администратора: ').strip(), 'email': input('Почта администратора: ').strip(), 'password': getpass.getpass('Пароль (минимум 12 символов): ')}
    if data['password'] != getpass.getpass('Повторите пароль: '):
        raise SystemExit('Пароли не совпадают. Аккаунт не создан.')
mode = 'set-password' if reset else 'bootstrap'
input_data = {'email': data['email'], 'password': data['password']} if reset else data
result = subprocess.run([sys.executable, str(root / 'scripts/run_backend.py'), mode], input=json.dumps(input_data), text=True, cwd=root)
if result.returncode:
    raise SystemExit(result.returncode)
if dev:
    print('Development administrator prepared. Credentials stored only in .env.admin.json (0600); not printed.')
