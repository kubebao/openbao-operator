#!/usr/bin/env bash

set -euo pipefail

build_dir="${1:-website/public}"
remote="${GH_PAGES_REMOTE:-origin}"
branch="${GH_PAGES_BRANCH:-gh-pages}"
commit_message="${COMMIT_MESSAGE:-docs: publish site}"
git_name="${GIT_AUTHOR_NAME:-github-actions[bot]}"
git_email="${GIT_AUTHOR_EMAIL:-github-actions[bot]@users.noreply.github.com}"

if [[ ! -d "${build_dir}" ]]; then
  echo "Build directory not found: ${build_dir}" >&2
  exit 1
fi

git config user.name "${git_name}"
git config user.email "${git_email}"

worktree_root="$(mktemp -d)"
site_root="${worktree_root}/site"

cleanup() {
  git worktree remove --force "${site_root}" >/dev/null 2>&1 || true
  rm -rf "${worktree_root}"
}
trap cleanup EXIT

if git ls-remote --exit-code --heads "${remote}" "${branch}" >/dev/null 2>&1; then
  git fetch "${remote}" "${branch}" --depth=1
  git worktree add -B "${branch}" "${site_root}" "${remote}/${branch}"
else
  git worktree add --detach "${site_root}"
  pushd "${site_root}" >/dev/null
  git checkout --orphan "${branch}"
  find . -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
  popd >/dev/null
fi

rsync -a --checksum --delete \
  --exclude '.git' \
  --exclude 'edge' \
  --exclude 'nightly' \
  "${build_dir}/" "${site_root}/"

pushd "${site_root}" >/dev/null
git add -A

if git diff --cached --quiet; then
  echo "No site changes to publish."
  exit 0
fi

git commit -m "${commit_message}"
git push "${remote}" "${branch}"
popd >/dev/null
