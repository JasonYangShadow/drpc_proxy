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