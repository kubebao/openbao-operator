#!/usr/bin/env bash
set -euo pipefail

image="${1:?image reference is required}"
attempts="${2:-11}"
delay_seconds="${3:-30}"

for ((attempt = 1; attempt <= attempts; attempt++)); do
  echo "Waiting for release image ${image} (${attempt}/${attempts})"
  if docker pull "${image}"; then
    echo "Release image is available; restore compatibility testing can start."
    exit 0
  fi
  if ((attempt < attempts)); then
    sleep "${delay_seconds}"
  fi
done

echo "::error::Release image ${image} is unavailable; restore compatibility tests have not started."
exit 1
