#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> Building WebKVM binary using isolated Docker container..."
sudo docker build --target backend-builder -t webkvm-builder:latest .

mkdir -p dist
CONTAINER_ID=$(sudo docker create webkvm-builder:latest)
sudo docker cp "${CONTAINER_ID}:/out/webkvm" dist/webkvm
sudo docker cp "${CONTAINER_ID}:/out/webkvm-cli" dist/webkvm-cli
sudo docker rm "${CONTAINER_ID}"

chmod +x dist/webkvm dist/webkvm-cli
echo "==> Binaries generated at dist/webkvm and dist/webkvm-cli"
