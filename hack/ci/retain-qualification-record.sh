#!/usr/bin/env bash
set -euo pipefail
mkdir -p dist/qualified
cp -- "${BUNDLE_PATH:?signed qualification bundle is required}" dist/qualified/bundle.json
cp dist/audit/qualification.json dist/qualified/qualification.json
