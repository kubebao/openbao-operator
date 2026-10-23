#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
mkdir -p "${tmp_dir}/bin"
cat > "${tmp_dir}/bin/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == "api repos/kubebao/openbao-operator/releases/tags/${TEST_RELEASE}" ]]
cat "${TEST_RESPONSE}"
exit "${TEST_EXIT}"
SH
chmod +x "${tmp_dir}/bin/gh"
export PATH="${tmp_dir}/bin:${PATH}" GITHUB_REPOSITORY=kubebao/openbao-operator
export TEST_RESPONSE="${tmp_dir}/response.json" TEST_EXIT=0 TEST_RELEASE=0.6.0
export GITHUB_OUTPUT="${tmp_dir}/output"

write_config() {
  printf 'defaultContentVersion = "%s"\n' "${TEST_RELEASE%.*}.x" > "${tmp_dir}/hugo.toml"
  printf '"%s":\n  release: "%s"\n  sourceRef: "%s"\n' \
    "${TEST_RELEASE%.*}.x" "${TEST_RELEASE}" "${TEST_RELEASE}" > "${tmp_dir}/versions.yaml"
}
check() {
  : > "${GITHUB_OUTPUT}"
  bash "${ROOT_DIR}/hack/docs/check-published-stable.sh" "${tmp_dir}/hugo.toml" "${tmp_dir}/versions.yaml"
}
expect_published() {
  check
  grep -Fqx "published=$1" "${GITHUB_OUTPUT}"
  grep -Fqx "release_version=${TEST_RELEASE}" "${GITHUB_OUTPUT}"
}
expect_failure() {
  if check > "${tmp_dir}/check.log" 2>&1; then
    echo 'Expected publication eligibility check to fail.' >&2
    exit 1
  fi
  [[ ! -s "${GITHUB_OUTPUT}" ]]
}

# Existing published stable documentation still deploys on ordinary main pushes.
TEST_RELEASE=0.5.3
write_config
printf '%s\n' '{"tag_name":"0.5.3","draft":false,"prerelease":false,"published_at":"2026-10-20T12:00:00Z"}' > "${TEST_RESPONSE}"
expect_published true

TEST_RELEASE=0.6.0
write_config
printf '%s\n' '{"tag_name":"0.6.0","draft":false,"prerelease":false,"published_at":"2026-10-23T12:00:00Z"}' > "${TEST_RESPONSE}"
expect_published true
printf '%s\n' '{"status":"404","message":"Not Found"}' > "${TEST_RESPONSE}"
TEST_EXIT=1
expect_published false
TEST_EXIT=0
printf '%s\n' '{"tag_name":"0.6.0","draft":true,"prerelease":false,"published_at":null}' > "${TEST_RESPONSE}"
expect_published false
printf '%s\n' '{"tag_name":"0.6.0","draft":false,"prerelease":true,"published_at":"2026-10-23T12:00:00Z"}' > "${TEST_RESPONSE}"
expect_published false

# Unexpected API, authentication, transport, and configuration failures stop publication.
for response in '{"status":"403"}' '{"status":"503"}' ''; do
  printf '%s\n' "${response}" > "${TEST_RESPONSE}"
  TEST_EXIT=1
  expect_failure
done
TEST_EXIT=0
for response in \
  '{"tag_name":"0.5.3","draft":false,"prerelease":false}' \
  '{"tag_name":"0.6.0","draft":"false","prerelease":false}' \
  '{"tag_name":"0.6.0","draft":false,"prerelease":false,"published_at":null}'; do
  printf '%s\n' "${response}" > "${TEST_RESPONSE}"
  expect_failure
done
printf 'defaultContentVersion = "next"\n' > "${tmp_dir}/hugo.toml"
expect_failure
write_config
printf '"0.6.x":\n  release: "0.6.0"\n  sourceRef: "HEAD"\n' > "${tmp_dir}/versions.yaml"
expect_failure

echo 'Docs stable-release publication eligibility checks passed.'
