#!/usr/bin/env bash
set -euo pipefail
mkdir -p dist/plan
python3 -c 'import os,pathlib; pathlib.Path("dist/plan/release-body.txt").write_bytes(os.environ["RELEASE_BODY"].encode("utf-8"))'
python3 hack/ci/release-qualification.py freeze --controller "${CONTROLLER}" \
  --source dist/source/source.json --version "${RELEASE_VERSION}" --notes dist/plan/release-body.txt \
  --notes-sha "${RELEASE_BODY_SHA256}" --output dist/plan/plan.json
