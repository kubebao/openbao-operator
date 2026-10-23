#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# shellcheck source=/dev/null
source "${ROOT_DIR}/hack/dev/tool-versions.env"

fail() {
  printf 'ERR  %s\n' "$1" >&2
  exit 1
}

ok() {
  printf 'OK   %s\n' "$1"
}

require_nix_command() {
  local name="$1"
  local command_path=""
  local resolved_path=""

  command_path="$(command -v "${name}" 2>/dev/null || true)"
  if [[ -z "${command_path}" ]]; then
    fail "${name} is missing from the devenv environment"
  fi

  resolved_path="$(realpath "${command_path}")"
  if [[ "${resolved_path}" != /nix/store/* ]]; then
    fail "${name} resolved outside the Nix store: ${resolved_path}"
  fi

  ok "${name}: ${resolved_path}"
}

if [[ -z "${DEVENV_ROOT:-}" ]]; then
  fail "DEVENV_ROOT is not set; run this check through 'devenv test' or 'devenv shell'"
fi

devenv_root="$(cd "${DEVENV_ROOT}" && pwd)"
if [[ "${devenv_root}" != "${ROOT_DIR}" ]]; then
  fail "DEVENV_ROOT points to ${devenv_root}, expected ${ROOT_DIR}"
fi

hooks_path="$(git config --local --get core.hooksPath 2>/dev/null || true)"
if [[ "${hooks_path}" == ".githooks" ]]; then
  fail "Devenv did not migrate the retired core.hooksPath=.githooks setting"
fi

hooks_dir="$(git rev-parse --path-format=absolute --git-path hooks)"

if [[ ! -L .pre-commit-config.yaml ]]; then
  fail "Devenv did not generate .pre-commit-config.yaml"
fi
config_target="$(readlink .pre-commit-config.yaml)"
if [[ "${config_target}" != /nix/store/* ]]; then
  fail "Devenv Git hook configuration resolved outside the Nix store: ${config_target}"
fi

for hook in "${hooks_dir}/pre-commit" "${hooks_dir}/pre-push" hack/dev/pre-commit.sh hack/dev/pre-push.sh; do
  if [[ ! -x "${hook}" ]]; then
    fail "Devenv-managed Git hook is not executable: ${hook}"
  fi
done
ok "native Devenv Git hooks are configured"

if [[ -z "${DEVENV_PROFILE:-}" ]]; then
  fail "DEVENV_PROFILE is not set inside devenv"
fi
if [[ "${PATH%%:*}" != "${DEVENV_PROFILE}/bin" ]]; then
  fail "the active devenv profile must take precedence in PATH"
fi
ok "active devenv profile takes precedence in PATH"

if [[ "${GOTOOLCHAIN:-}" != "local" ]]; then
  fail "GOTOOLCHAIN must be local inside devenv, found ${GOTOOLCHAIN:-unset}"
fi

goroot_path="${GOROOT%/}"
if [[ "${goroot_path}" != /nix/store/*/share/go ]]; then
  fail "GOROOT must point to the pinned Nix Go package, found ${GOROOT:-unset}"
fi
ok "GOROOT points to the pinned Nix Go package (${goroot_path})"

if [[ -z "${GOPATH:-}" ]]; then
  fail "GOPATH is not set inside devenv"
fi
if [[ "${GOPATH}" == "${ROOT_DIR}" || "${GOPATH}" == "${ROOT_DIR}/"* ]]; then
  fail "GOPATH must stay outside the repository so generators do not scan its module cache: ${GOPATH}"
fi
ok "GOPATH is outside the repository (${GOPATH})"

expected_go="$(sed -n 's/^go //p' go.mod | head -n 1)"
current_go="$(go env GOVERSION 2>/dev/null || true)"
if [[ "${current_go}" != "go${expected_go}" ]]; then
  fail "Go toolchain mismatch: expected go${expected_go}, found ${current_go:-unknown}"
fi
ok "Go ${expected_go} matches go.mod"

expected_hugo="$(tr -d '[:space:]' < .hugo-version)"
current_hugo="$(hugo version 2>/dev/null || true)"
if [[ "${current_hugo}" != *"v${expected_hugo}"* ]]; then
  fail "Hugo mismatch: expected ${expected_hugo}, found ${current_hugo:-unknown}"
fi
ok "Hugo ${expected_hugo} matches .hugo-version"

current_helm="$(helm version --short 2>/dev/null || true)"
if [[ "${current_helm}" != "${HELM_VERSION}" && "${current_helm}" != "${HELM_VERSION}"+* ]]; then
  fail "Helm mismatch: expected ${HELM_VERSION}, found ${current_helm:-unknown}"
fi
ok "Helm matches tool-versions.env (${current_helm})"

current_kubectl="$(kubectl version --client --output=json 2>/dev/null | jq -r '.clientVersion.gitVersion // empty')"
if [[ "${current_kubectl}" != "${KUBECTL_VERSION}" ]]; then
  fail "kubectl mismatch: expected ${KUBECTL_VERSION}, found ${current_kubectl:-unknown}"
fi
ok "kubectl matches tool-versions.env (${current_kubectl})"

current_kind="$(kind version 2>/dev/null || true)"
if [[ "${current_kind}" != *"${KIND_VERSION}"* ]]; then
  fail "Kind mismatch: expected ${KIND_VERSION}, found ${current_kind:-unknown}"
fi
ok "Kind matches tool-versions.env (${current_kind})"

current_trivy="$(trivy version 2>/dev/null | sed -n 's/^Version: //p' | head -n 1)"
if [[ "v${current_trivy}" != "${TRIVY_VERSION}" ]]; then
  fail "Trivy mismatch: expected ${TRIVY_VERSION}, found ${current_trivy:-unknown}"
fi
ok "Trivy matches tool-versions.env (v${current_trivy})"

current_tilt="$(tilt version 2>/dev/null | sed -n 's/^v\([^,]*\).*/v\1/p' | head -n 1)"
if [[ "${current_tilt}" != "${TILT_VERSION}" ]]; then
  fail "Tilt mismatch: expected ${TILT_VERSION}, found ${current_tilt:-unknown}"
fi
ok "Tilt matches tool-versions.env (${current_tilt})"

for command_name in \
  bash check-jsonschema curl docker git go helm hugo jq kind kubectl make perl python3 tilt trivy; do
  require_nix_command "${command_name}"
done

for schema_path in "${SPDX_SCHEMA_2_2:-}" "${SPDX_SCHEMA_2_3:-}"; do
  if [[ "${schema_path}" != /nix/store/* || ! -f "${schema_path}" ]]; then
    fail "SPDX schemas must resolve to immutable Nix store files, found ${schema_path:-unset}"
  fi
done
ok "SPDX 2.2 and 2.3 schemas resolve to immutable Nix store files"

printf '\nPinned devenv toolchain contract verified.\n'
