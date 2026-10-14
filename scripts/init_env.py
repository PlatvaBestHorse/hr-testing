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
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as out:
        out.write(content)
    print('Local .env created with unique credentials (mode 0600).')
