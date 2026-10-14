#!/bin/bash
set -euo pipefail
cd /workspace/hr-testing
mkdir -p /workspace/.tools /workspace/.cache/{go,go-build,go-mod,npm,docker}
if ! /workspace/.tools/go/bin/go version 2>/dev/null | rg -q 'go1\.26\.9 '; then
  archive=$(mktemp /tmp/hr-go-XXXXXX.tar.gz)
  trap 'rm -f "$archive"' EXIT
  curl --fail --silent --show-error --location https://dl.google.com/go/go1.26.9.linux-amd64.tar.gz --output "$archive"
  printf '%s  %s\n' '42d158b4d8f7b61ac0a830567c940a86098fb7aac52e467a5ebec03ef5cc2f8d' "$archive" | sha256sum --check --status
  tar -xzf "$archive" -C /workspace/.tools
fi
. scripts/cloud_env.sh
make init install
docker compose pull db
docker compose build api web
