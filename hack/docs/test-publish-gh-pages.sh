#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/hack/docs/publish-gh-pages.sh"
REAL_RSYNC="$(command -v rsync)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

# Keep the fixture's commits and pushes independent of developer Git settings.
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=docs-publisher-test
export GIT_AUTHOR_EMAIL=docs-publisher-test@example.invalid
export GIT_COMMITTER_NAME="${GIT_AUTHOR_NAME}"
export GIT_COMMITTER_EMAIL="${GIT_AUTHOR_EMAIL}"

repo="${tmp_dir}/repo"
remote="${tmp_dir}/remote.git"
build="${tmp_dir}/build"
mkdir -p "${tmp_dir}/empty-template" "${tmp_dir}/bin" "${build}"
git init --bare --initial-branch=gh-pages --template="${tmp_dir}/empty-template" "${remote}" >/dev/null
git init --initial-branch=gh-pages --template="${tmp_dir}/empty-template" "${repo}" >/dev/null
git -C "${repo}" remote add origin "${remote}"

mkdir -p "${repo}/docs/reference/api" "${repo}/edge/latest" "${repo}/nightly/latest"
printf '%s\n' 'API reference for 0.5.2' > "${repo}/docs/reference/api/index.html"
printf '%s\n' 'Old home page' > "${repo}/index.html"
printf '%s\n' 'Obsolete root page' > "${repo}/stale.html"
printf '%s\n' 'Retained edge publication' > "${repo}/edge/latest/index.html"
printf '%s\n' 'Retained nightly publication' > "${repo}/nightly/latest/index.html"
git -C "${repo}" add -A
git -C "${repo}" commit -m 'Seed published site' >/dev/null
git -C "${repo}" push origin gh-pages >/dev/null
seed_commit="$(git -C "${repo}" rev-parse HEAD)"
git -C "${repo}" checkout -b main >/dev/null

mkdir -p "${build}/docs/reference/api" "${build}/edge/latest" "${build}/nightly/latest" "${build}/.git"
printf '%s\n' 'API reference for 0.5.3' > "${build}/docs/reference/api/index.html"
printf '%s\n' 'Updated home page' > "${build}/index.html"
printf '%s\n' 'New root page' > "${build}/new.html"
for namespace in edge nightly; do
  printf '%s\n' 'Must not replace reserved content' > "${build}/${namespace}/latest/index.html"
  printf '%s\n' 'Must not add reserved content' > "${build}/${namespace}/unexpected.html"
done
printf '%s\n' 'Must not replace worktree metadata' > "${build}/.git/unexpected"

old_size="$(git -C "${repo}" show "${seed_commit}:docs/reference/api/index.html" | wc -c | tr -d ' ')"
new_size="$(wc -c < "${build}/docs/reference/api/index.html" | tr -d ' ')"
[[ "${old_size}" == "${new_size}" ]] || { echo 'Fixture API pages must have equal sizes.' >&2; exit 1; }

# A checkout normally gives files fresh timestamps. Reproduce a timestamp
# collision immediately before the real copy, without changing its arguments.
cat > "${tmp_dir}/bin/rsync" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
source_dir="${@: -2:1}"
destination_dir="${@: -1}"
touch -r "${source_dir}/docs/reference/api/index.html" "${destination_dir}/docs/reference/api/index.html"
cp "${destination_dir}/.git" "${TEST_STATE_DIR}/worktree-git-before"
"${REAL_RSYNC}" "$@"
cmp "${TEST_STATE_DIR}/worktree-git-before" "${destination_dir}/.git"
SH
chmod +x "${tmp_dir}/bin/rsync"
export REAL_RSYNC TEST_STATE_DIR="${tmp_dir}"
export PATH="${tmp_dir}/bin:${PATH}"

publish() {
  (cd "${repo}" && GH_PAGES_REMOTE=origin GH_PAGES_BRANCH=gh-pages COMMIT_MESSAGE='Publish fixture site' bash "${SCRIPT}" "${build}")
}

publish
published_commit="$(git --git-dir="${remote}" rev-parse refs/heads/gh-pages)"
[[ "${published_commit}" != "${seed_commit}" ]] || { echo 'Publisher did not push a site update.' >&2; exit 1; }

for path in docs/reference/api/index.html index.html new.html; do
  git --git-dir="${remote}" show "${published_commit}:${path}" > "${tmp_dir}/published-file"
  cmp "${build}/${path}" "${tmp_dir}/published-file" || {
    echo "Published bytes differ from the build: ${path}" >&2
    exit 1
  }
done

for namespace in edge nightly; do
  git --git-dir="${remote}" show "${seed_commit}:${namespace}/latest/index.html" > "${tmp_dir}/reserved-before"
  git --git-dir="${remote}" show "${published_commit}:${namespace}/latest/index.html" > "${tmp_dir}/reserved-after"
  cmp "${tmp_dir}/reserved-before" "${tmp_dir}/reserved-after"
  if git --git-dir="${remote}" cat-file -e "${published_commit}:${namespace}/unexpected.html" 2>/dev/null; then
    echo "Publisher added build content to reserved namespace: ${namespace}" >&2
    exit 1
  fi
done
if git --git-dir="${remote}" cat-file -e "${published_commit}:stale.html" 2>/dev/null; then
  echo 'Publisher retained stale root content.' >&2
  exit 1
fi
git -C "${repo}" rev-parse --git-dir >/dev/null

publish
[[ "$(git --git-dir="${remote}" rev-parse refs/heads/gh-pages)" == "${published_commit}" ]] || {
  echo 'Publishing identical content created another commit.' >&2
  exit 1
}

echo 'Docs publisher content, namespace, deletion, and idempotency checks passed.'
