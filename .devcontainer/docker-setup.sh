#!/usr/bin/env bash
set -e

SOCK=/var/run/docker.sock

if [ ! -S "$SOCK" ]; then
    echo ">>> docker.sock not found, skipping setup"
    exit 0
fi

GID=$(stat -c '%g' $SOCK)
GROUP_NAME=dockerhost

echo ">>> docker.sock GID = $GID"

if ! getent group $GID >/dev/null; then
    echo ">>> Creating group $GROUP_NAME with GID $GID"
    sudo groupadd -g $GID $GROUP_NAME || true
else
    GROUP_NAME=$(getent group $GID | cut -d: -f1)
    echo ">>> Group with GID $GID already exists: $GROUP_NAME"
fi

echo ">>> Adding vscode to group $GROUP_NAME"
sudo usermod -aG $GROUP_NAME vscode

echo ">>> Switching current shell to group $GROUP_NAME"
exec sg $GROUP_NAME newgrp `id -gn`

# ── Auto-join the drpc docker-compose network ─────────────────────────────────
# Runs after Docker socket access is granted. Silently no-ops if the network
# doesn't exist yet (it's created on first 'docker compose up').
DRPC_NETWORK=drpc_proxy_drpc
SELF=$(cat /proc/self/cgroup | grep 'docker' | head -1 | sed 's/.*docker[-/]//' | cut -c1-12)
if docker network ls --format '{{.Name}}' | grep -q "^${DRPC_NETWORK}$"; then
    docker network connect "$DRPC_NETWORK" "$SELF" 2>/dev/null \
        && echo ">>> Joined Docker network: $DRPC_NETWORK" \
        || echo ">>> Already on network: $DRPC_NETWORK"
else
    echo ">>> Network $DRPC_NETWORK not found yet — run 'task docker:up:infra' to create it"
fi