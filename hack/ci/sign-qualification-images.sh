#!/usr/bin/env bash
set -euo pipefail
: "${CONTROLLER:?}"
[[ "${CONTROLLER}" =~ ^[0-9a-f]{40}$ && "${CONTROLLER}" == "${GITHUB_WORKFLOW_SHA:?}" ]]
subject=https://github.com/kubebao/openbao-operator/.github/workflows/release-qualification-pilot.yml@refs/heads/main
for image in operator init backup upgrade; do
  ref="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' dist/import/images.json "${image}")"
  [[ "${ref}" =~ ^ghcr\.io/kubebao/openbao-${image}-qualification@sha256:[0-9a-f]{64}$ ]]
  # In the pinned signing-config mode, --bundle writes locally instead of uploading.
  cosign sign --yes "${ref}"
  cosign attest --yes --type https://kubebao.org/attestations/candidate-image-import/v1 \
    --predicate "dist/import/${image}/predicate.json" "${ref}"
  cosign download signature "${ref}" > "dist/import/${image}/registry-bundles.jsonl"
  [[ -s "dist/import/${image}/registry-bundles.jsonl" ]]
  cosign verify --certificate-identity "${subject}" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-github-workflow-sha "${CONTROLLER}" "${ref}" > "dist/import/${image}/signature-verification.json"
  cosign verify-attestation --type https://kubebao.org/attestations/candidate-image-import/v1 \
    --certificate-identity "${subject}" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-github-workflow-sha "${CONTROLLER}" "${ref}" > "dist/import/${image}/attestation-verification.json"
done

mkdir -p dist/import-public
cp dist/import/images.json dist/import/policy.json dist/import/import.json dist/import-public/
