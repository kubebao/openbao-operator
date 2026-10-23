#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

failures=0

ok() {
  printf 'OK   %s\n' "$1"
}

warn() {
  printf 'WARN %s\n' "$1"
}

err() {
  printf 'ERR  %s\n' "$1"
  failures=1
}

check_cmd() {
  local cmd="$1"
  local description="$2"
  local hint="$3"

  if command -v "${cmd}" >/dev/null 2>&1; then
    ok "${description}: $(command -v "${cmd}")"
  else
    err "${description}: missing (${hint})"
  fi
}

printf 'OpenBao Operator development environment check\n'
printf 'Repository: %s\n\n' "${ROOT_DIR}"

expected_go="$(sed -n 's/^go //p' go.mod | head -n 1)"
expected_actionlint="$(sed -n 's/^ACTIONLINT_VERSION ?= v\{0,1\}//p' mk/dependencies.mk | head -n 1)"
expected_semgrep="$(sed -n 's/^SEMGREP_VERSION ?= //p' mk/dependencies.mk | head -n 1)"
expected_ast_grep="$(sed -n 's/^AST_GREP_VERSION ?= //p' mk/dependencies.mk | head -n 1)"
expected_hugo="$(tr -d '[:space:]' < .hugo-version)"
if command -v go >/dev/null 2>&1; then
  current_go="$(go env GOVERSION 2>/dev/null || true)"
  if [[ "${current_go}" == "go${expected_go}"* ]]; then
    ok "Go toolchain matches go.mod (${current_go})"
  else
    err "Go toolchain mismatch: expected go${expected_go}, found ${current_go:-unknown}"
  fi
else
  err "Go toolchain missing (install Go ${expected_go})"
fi

if command -v hugo >/dev/null 2>&1; then
  current_hugo="$(hugo version 2>/dev/null || true)"
  if [[ "${current_hugo}" == *"v${expected_hugo}"* ]]; then
    ok "Hugo matches .hugo-version (${expected_hugo})"
  else
    warn "Hugo version mismatch: expected ${expected_hugo}, found ${current_hugo:-unknown}"
  fi
else
  warn "Hugo missing (required only for documentation work; use the Nix command in website/README.md)"
fi

check_cmd git "git" "required for repository workflows"
check_cmd curl "curl" "required to install pinned release tools"
check_cmd docker "docker" "required for image builds and config compatibility checks"
check_cmd kubectl "kubectl" "required for install/deploy workflows"
check_cmd helm "helm" "required for verify-helm and helm-test"
check_cmd trivy "trivy" "required for security-ci and security-scan"
check_cmd perl "perl" "required for API/reference generation"
check_cmd python3 "python3" "required for manifest patch helpers, Tilt manifest rendering, and API/reference generation"

if command -v kind >/dev/null 2>&1; then
  ok "kind: $(command -v kind)"
else
  warn "kind missing (required only for E2E and perf workflows)"
fi

if command -v tilt >/dev/null 2>&1; then
  ok "tilt: $(command -v tilt)"
else
  warn "tilt missing (optional for the local Kubernetes dev loop)"
fi

if [ -x "${ROOT_DIR}/bin/ast-grep" ]; then
  current_ast_grep="$("${ROOT_DIR}/bin/ast-grep" --version 2>/dev/null || true)"
  if [ "${current_ast_grep}" = "ast-grep ${expected_ast_grep}" ]; then
    ok "ast-grep bootstrapped locally (${current_ast_grep})"
  else
    warn "ast-grep version mismatch: expected ${expected_ast_grep}, found ${current_ast_grep:-unknown} (run 'make ast-grep')"
  fi
else
  warn "ast-grep not bootstrapped locally yet (run 'make bootstrap' or 'make ast-grep')"
fi

if [ -x "${ROOT_DIR}/bin/semgrep-venv/bin/python" ]; then
  current_semgrep="$(
    "${ROOT_DIR}/bin/semgrep-venv/bin/python" \
      -c 'import importlib.metadata; print(importlib.metadata.version("semgrep"))' \
      2>/dev/null || true
  )"
  if [ "${current_semgrep}" = "${expected_semgrep}" ]; then
    ok "semgrep bootstrapped locally (${current_semgrep})"
  else
    warn "semgrep version mismatch: expected ${expected_semgrep}, found ${current_semgrep:-unknown} (run 'make semgrep')"
  fi
else
  warn "semgrep not bootstrapped locally yet (run 'make bootstrap' or 'make semgrep')"
fi

if [ -x "${ROOT_DIR}/bin/actionlint" ]; then
  current_actionlint="$("${ROOT_DIR}/bin/actionlint" -version 2>/dev/null | head -n 1 || true)"
  current_actionlint="${current_actionlint#v}"
  if [ "${current_actionlint}" = "${expected_actionlint}" ]; then
    ok "actionlint bootstrapped locally (${current_actionlint})"
  else
    warn "actionlint version mismatch: expected ${expected_actionlint}, found ${current_actionlint:-unknown} (run 'make actionlint')"
  fi
else
  warn "actionlint not bootstrapped locally yet (run 'make bootstrap' or 'make actionlint')"
fi

if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    ok "docker daemon reachable"
  else
    warn "docker CLI found but daemon is not reachable"
  fi
fi

if command -v kubectl >/dev/null 2>&1; then
  if kubectl config current-context >/dev/null 2>&1; then
    ok "kubectl context: $(kubectl config current-context)"
  else
    warn "kubectl has no current context (needed for deploy/e2e workflows)"
  fi
fi

printf '\n'
if [ "${failures}" -ne 0 ]; then
  printf "Doctor found blocking issues.\n"
  printf "Run 'make bootstrap' for repo-managed tools, then fix the missing external prerequisites above.\n"
  exit 1
fi

printf "Doctor found no blocking issues.\n"
