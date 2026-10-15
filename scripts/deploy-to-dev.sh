#!/usr/bin/env bash
# Deploy compiled webkvm binary to webkvm-dev VM (192.168.1.40)
set -Eeuo pipefail

DEV_IP="${WEBKVM_DEV_IP:-192.168.1.40}"
DEV_USER="${WEBKVM_DEV_USER:-alvin}"
LOCAL_BIN="$(dirname "$0")/../backend/webkvm"

if [[ ! -f "${LOCAL_BIN}" ]]; then
  echo "ERROR: ${LOCAL_BIN} not found. Run 'make build-backend' first." >&2
  exit 1
fi

echo "==> Deploying $(ls -lh "${LOCAL_BIN}" | awk '{print $9, "(" $5 ")"}') to ${DEV_USER}@${DEV_IP}..."
scp -o StrictHostKeyChecking=no -q "${LOCAL_BIN}" "${DEV_USER}@${DEV_IP}:/tmp/webkvm.new"

ssh -o StrictHostKeyChecking=no "${DEV_USER}@${DEV_IP}" '
  sudo cp -a /usr/local/bin/webkvm /usr/local/bin/webkvm.previous 2>/dev/null || true
  sudo install -m 0755 /tmp/webkvm.new /usr/local/bin/webkvm
  rm -f /tmp/webkvm.new
  sudo systemctl restart webkvm
'

echo "==> Verifying health on https://${DEV_IP}:8080/api/health..."
for i in $(seq 1 15); do
  if RES=$(curl -kfsS -m 2 "https://${DEV_IP}:8080/api/health" 2>/dev/null); then
    echo "==> Deploy successful! Health: ${RES}"
    exit 0
  fi
  sleep 1
done

echo "ERROR: Health check failed after 15s!" >&2
exit 1
