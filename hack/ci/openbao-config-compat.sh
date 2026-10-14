#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

for command in docker go git; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "error: $command is required" >&2
    exit 1
  fi
done

REPORT_SCHEMA_DRIFT="${REPORT_SCHEMA_DRIFT:-false}"
TMPMODULE_GOFLAGS="${TMPMODULE_GOFLAGS:--mod=mod}"
REPO_GOFLAGS="${REPO_GOFLAGS:--mod=vendor}"
REPORT_DIR="${COMPAT_REPORT_DIR:-${ROOT_DIR}/dist/test/openbao-config-compat}"
mkdir -p "$REPORT_DIR"
REPORT_DIR="$(cd "$REPORT_DIR" && pwd)"
REPORT_DIR="$(mktemp -d "$REPORT_DIR/run-XXXXXX")"
echo "Compatibility reports: $REPORT_DIR"
work_root="$(mktemp -d)"
trap 'rm -rf "$work_root"' EXIT
schema_tmp="$work_root/schema"
mkdir -p "$schema_tmp"
(
  cd "$ROOT_DIR"
  GOFLAGS="$REPO_GOFLAGS" go build -o "$work_root/prepare" ./hack/tools/openbao_config_compat
  if [ "$REPORT_SCHEMA_DRIFT" = true ]; then
    GOFLAGS="$REPO_GOFLAGS" go build -o "$work_root/operator-schema" ./hack/tools/openbao_operator_schema_drift
  fi
)
VERSIONS=("$@")
if [ ${#VERSIONS[@]} -eq 0 ]; then
  read -r -a VERSIONS <<< "$("$work_root/prepare" -root "$ROOT_DIR" -list-versions)"
fi
if [ ${#VERSIONS[@]} -eq 0 ]; then
  echo "error: empty version matrix" >&2
  exit 1
fi

for version in "${VERSIONS[@]}"; do
  "$work_root/prepare" -version "$version" -validate-version
  echo "==> OpenBao config compatibility: $version"

  sha="$(docker run --rm "openbao/openbao:${version}" version | sed -n 's/.*(\([0-9a-f]\{40\}\)).*/\1/p')"
  if [ -z "${sha}" ]; then
    echo "error: failed to determine git SHA for openbao/openbao:${version}" >&2
    exit 1
  fi
  echo "    SHA: ${sha}"

  tmpdir="$work_root/parser-$version"
  mkdir -p "$tmpdir"

  module_path="github.com/openbao/openbao"
  parser_path="command/server"
  harness_module="tmp.example/openbao-config-compat"
  IFS=. read -r major minor rest <<< "${version#v}"
  if [[ ! "$major" =~ ^[0-9]+$ || ! "$minor" =~ ^[0-9]+$ ]]; then
    echo "error: expected an OpenBao semantic version, got $version" >&2
    exit 1
  fi
  if (( major > 2 || (major == 2 && minor >= 7) )); then
    module_path="github.com/openbao/openbao/v2"
    parser_path="internal/command/server"
    # Go permits internal imports from another module under the same import
    # prefix. Keep this test harness isolated from the operator module.
    harness_module="${module_path}/operator-config-compat"
  fi

  sed "s|SERVERCONFIG|${module_path}/${parser_path}|" \
    "$ROOT_DIR/hack/tools/openbao_config_compat/harness.go.tmpl" > "$tmpdir/main.go"
  cp "$ROOT_DIR/hack/tools/openbao_config_compat/contract.go" "$tmpdir/contract.go"

  (
    cd "${tmpdir}"
    go mod init "${harness_module}" >/dev/null 2>&1

    go_get_args=("${module_path}@${sha}")
    if [ "${module_path}" = "github.com/openbao/openbao" ]; then
      # OpenBao tags submodules separately from the server module. Prefer the
      # exact SDK release tag when it exists, but use its VCS commit instead of
      # its SemVer name. A root tag with the same SemVer can otherwise cause Go
      # to resolve the SDK path against the root module. Older patch releases
      # do not always have a matching SDK tag, so retain the server SHA
      # fallback.
      sdk_ref="${sha}"
      sdk_metadata="$(
        GOFLAGS="${TMPMODULE_GOFLAGS}" go mod download -json \
          "github.com/openbao/openbao/sdk/v2@v${version}" 2>/dev/null || true
      )"
      sdk_tag_sha="$(
        printf '%s\n' "${sdk_metadata}" \
          | sed -n 's/.*"Hash": "\([0-9a-f]\{40\}\)".*/\1/p' \
          | head -n 1
      )"
      if [ -n "${sdk_tag_sha}" ]; then
        sdk_ref="${sdk_tag_sha}"
      fi
      go_get_args+=("github.com/openbao/openbao/sdk/v2@${sdk_ref}")
    else
      # From 2.7 onward the internal parser and profiles must use the SDK
      # from the server commit. The separately published 2.7.0 SDK tag
      # predates the server's CEL module migration and cannot compile this
      # parser. Build against the server commit's sdk directory, as the
      # upstream `replace ... => ./sdk` directive does. A pseudo-version for
      # the server commit is not usable: when that commit also carries the
      # root release tag (2.7.1), Go rejects the derived SDK pseudo-version.
      sdk_src="${tmpdir}/openbao-src"
      git init -q "${sdk_src}"
      git -C "${sdk_src}" sparse-checkout set --no-cone /sdk/
      git -C "${sdk_src}" fetch -q --depth 1 --filter=blob:none \
        https://github.com/openbao/openbao.git "${sha}"
      git -C "${sdk_src}" -c advice.detachedHead=false checkout -q FETCH_HEAD
      go mod edit -replace "github.com/openbao/openbao/sdk/v2=${sdk_src}/sdk"
    fi

    go_get_log="${tmpdir}/go-get.log"
    if ! GOFLAGS="${TMPMODULE_GOFLAGS}" go get \
      "${go_get_args[@]}" >"${go_get_log}" 2>&1; then
      cat "${go_get_log}" >&2
      exit 1
    fi
    if ! GOFLAGS="${TMPMODULE_GOFLAGS}" go mod tidy >"${tmpdir}/go-tidy.log" 2>&1; then
      cat "${tmpdir}/go-tidy.log" >&2
      exit 1
    fi
    moddir="$(GOFLAGS="${TMPMODULE_GOFLAGS}" go list -m -f '{{.Dir}}' "${module_path}")"
    "$work_root/prepare" -root "$ROOT_DIR" -version "$version" \
      -upstream-commit "$sha" \
      -upstream-module-dir "$moddir" -output "$tmpdir/cases.json" \
      -schema-output "$schema_tmp/keys-$version.txt"
    GOFLAGS="${TMPMODULE_GOFLAGS}" go run . "$tmpdir/cases.json" "$REPORT_DIR/cases-$version.json" \
      | tee "$REPORT_DIR/summary-$version.txt"
    if [ "$REPORT_SCHEMA_DRIFT" = true ]; then
      (
        cd "$ROOT_DIR"
        "$work_root/operator-schema" -upstream-module-dir "$moddir" \
          -openbao-image-tag "$version" -openbao-git-sha "$sha" -max-list 0
      ) | tee "$REPORT_DIR/operator-schema-$version.txt"
      cp "$schema_tmp/keys-$version.txt" "$REPORT_DIR/schema-keys-$version.txt"
    fi
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      printf '\n- ' >> "$GITHUB_STEP_SUMMARY"
      cat "$REPORT_DIR/summary-$version.txt" >> "$GITHUB_STEP_SUMMARY"
    fi
  )

done

if [ "${REPORT_SCHEMA_DRIFT}" = "true" ]; then
  echo
  echo "==> OpenBao config schema drift report"
  echo "    Source: qualified parser/profile paths, Raft map reads, and retry_join JSON fields"
  echo

  any=false
  for ((i = 1; i < ${#VERSIONS[@]}; i++)); do
    prev="${VERSIONS[$((i-1))]}"
    cur="${VERSIONS[$i]}"
    prev_file="${schema_tmp}/keys-${prev}.txt"
    cur_file="${schema_tmp}/keys-${cur}.txt"

    if [ ! -f "${prev_file}" ] || [ ! -f "${cur_file}" ]; then
      echo "error: missing schema snapshots for ${prev} -> ${cur}" >&2
      exit 1
    fi

    added="$(LC_ALL=C comm -13 "${prev_file}" "${cur_file}")"
    removed="$(LC_ALL=C comm -23 "${prev_file}" "${cur_file}")"

    if [ -n "${added}" ] || [ -n "${removed}" ]; then
      any=true
      echo "--- ${prev} -> ${cur}"
      if [ -n "${added}" ]; then
        echo "Added keys:"
        echo "${added}" | sed 's/^/  + /'
      fi
      if [ -n "${removed}" ]; then
        echo "Removed keys:"
        echo "${removed}" | sed 's/^/  - /'
      fi
      echo
    fi
  done

  if [ "${any}" = "false" ]; then
    echo "No changes in extracted configuration paths across: ${VERSIONS[*]}"
  fi

  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "### OpenBao config schema drift"
      echo
      echo "- Range: \`${VERSIONS[*]}\`"
      if [ "${any}" = "false" ]; then
        echo "- Result: no schema changes detected"
      else
        echo "- Result: schema changes detected (see job logs for details)"
      fi
    } >> "${GITHUB_STEP_SUMMARY}"
  fi
fi
