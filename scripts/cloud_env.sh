# Source from the repository root: . scripts/cloud_env.sh
# Activates retained cloud tools and writable caches. Never prints proxy values.
export PATH="/workspace/.tools/go/bin:$PATH"
export GOPATH=/workspace/.cache/go
export GOCACHE=/workspace/.cache/go-build
export GOMODCACHE=/workspace/.cache/go-mod
export npm_config_cache=/workspace/.cache/npm
export DOCKER_CONFIG=/workspace/.cache/docker
export COMPOSE_FILE=compose.yaml:compose.cloud.yaml
export PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
export HR_BUILD_PROXY_HOST="$(python3 -c 'import os,urllib.parse; print(urllib.parse.urlsplit(os.environ.get("HTTPS_PROXY", "http://proxy")).hostname)')"
export HR_BUILD_PROXY_IP="$(python3 -c 'import os,socket; print(socket.gethostbyname(os.environ["HR_BUILD_PROXY_HOST"]))')"
