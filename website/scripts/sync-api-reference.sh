#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSION_DATA="${REPO_DIR}/website/data/version_lines.yaml"
MODE="write"
LINE="all"

usage() {
  echo "usage: $0 [--check] [--line 0.4.x|0.5.x|next|--all]" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check)
      MODE="check"
      ;;
    --line)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      LINE="$2"
      shift
      ;;
    --all)
      LINE="all"
      ;;
    *)
      usage
      exit 2
      ;;
  esac
  shift
done

version_field() {
  local line="$1"
  local field="$2"
  awk -v section="\"${line}\":" -v field="${field}:" '
    $0 == section { in_section = 1; next }
    in_section && /^[^[:space:]]/ { exit }
    in_section && $1 == field {
      value = $2
      gsub(/^"|"$/, "", value)
      print value
      exit
    }
  ' "${VERSION_DATA}"
}

SYNC_TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${SYNC_TMP_DIR}"' EXIT

generate_line() {
  local line="$1"
  local source_ref
  local fallback_source_ref
  local read_ref
  local content_root
  local output_dir
  local source_path
  local source_location
  local source_marker
  local source_end_marker
  local apply_errata="false"
  local line_tmp="${SYNC_TMP_DIR}/${line//./-}"

  source_ref="$(version_field "${line}" sourceRef)"
  fallback_source_ref="$(version_field "${line}" fallbackSourceRef)"
  content_root="$(version_field "${line}" contentRoot)"
  if [[ -z "${source_ref}" || -z "${content_root}" ]]; then
    echo "missing sourceRef or contentRoot for documentation line ${line}" >&2
    return 1
  fi

  output_dir="${REPO_DIR}/${content_root}/docs/reference/api"
  source_path="${line_tmp}/api.md"
  mkdir -p "${line_tmp}"

  read_ref="${source_ref}"
  if ! git -C "${REPO_DIR}" cat-file -e "${source_ref}^{commit}" 2>/dev/null; then
    if [[ -z "${fallback_source_ref}" ]]; then
      echo "documentation source ref not found for ${line}: ${source_ref}" >&2
      return 1
    fi
    read_ref="${fallback_source_ref}"
  fi

  if [[ "${line}" == "next" && "${source_ref}" == "HEAD" ]]; then
    # Unreleased API docs follow the checked-out generated source, including
    # local changes. Stable lines always read their pinned release source.
    source_location="website/generated/api-reference.md"
    source_marker='<!-- BEGIN RESOURCE '
    source_end_marker='<!-- END RESOURCE -->'
  elif git -C "${REPO_DIR}" cat-file -e "${read_ref}:website/generated/api-reference.md" 2>/dev/null; then
    source_location="website/generated/api-reference.md"
    source_marker='<!-- BEGIN RESOURCE '
    source_end_marker='<!-- END RESOURCE -->'
  elif git -C "${REPO_DIR}" cat-file -e "${read_ref}:docs/reference/api.md" 2>/dev/null; then
    # Release lines created before the Hugo migration retain the historical
    # generated Docusaurus source in Git. Read it without keeping that site in
    # the current tree.
    source_location="docs/reference/api.md"
    source_marker='<TabItem value="'
    source_end_marker='</TabItem>'
  else
    echo "generated API source not found at ${read_ref} for ${line}" >&2
    return 1
  fi
  if [[ "${line}" == "next" && "${source_ref}" == "HEAD" ]]; then
    cp "${REPO_DIR}/${source_location}" "${source_path}"
  else
    git -C "${REPO_DIR}" show "${read_ref}:${source_location}" > "${source_path}"
  fi

  if [[ "${line}" == "0.4.x" ]]; then
    apply_errata="true"
  fi

  generate_page() {
    local value="$1"
    local title="$2"
    local description="$3"
    local weight="$4"
    local body_path="${line_tmp}/${value}.body.md"
    local page_path="${line_tmp}/${value}.md"

    awk -v marker="${source_marker}${value}" -v end_marker="${source_end_marker}" -v resource="${value}" -v apply_errata="${apply_errata}" '
      index($0, marker) == 1 { in_resource = 1; next }
      in_resource && $0 == end_marker { found_end = 1; exit }
      in_resource {
        line = $0
        gsub(/https:\/\/openbao.org\/api-docs\/system\/policies-acl\//, "https://openbao.org/api-docs/system/policy/", line)
        gsub(/https:\/\/openbao.org\/docs\/configuration\/seal\/static-key\//, "https://openbao.org/docs/configuration/seal/static/", line)
        gsub(/\"https:\/\/acme-v02.api.letsencrypt.org\/directory\"/, "`https://acme-v02.api.letsencrypt.org/directory`", line)
        while (match(line, /https?:\/\/[^[:space:]|<]*&lt;[^[:space:]|<]*&gt;/)) {
          url = substr(line, RSTART, RLENGTH)
          gsub(/&lt;/, "<", url)
          gsub(/&gt;/, ">", url)
          line = substr(line, 1, RSTART - 1) "`" url "`" substr(line, RSTART + RLENGTH)
        }
        if (resource == "openbaocluster") {
          gsub(/\[RestoreSource\]\(#restoresource\)/, "[RestoreSource](../openbaorestore/#restoresource)", line)
          gsub(/\[OpenBaoRestoreSpec\]\(#openbaorestorespec\)/, "[OpenBaoRestoreSpec](../openbaorestore/#openbaorestorespec)", line)
        }
        if (resource == "openbaorestore") {
          gsub(/\[OpenBaoConfiguration\]\(#openbaoconfiguration\)/, "[OpenBaoConfiguration](../openbaocluster/#openbaoconfiguration)", line)
          gsub(/\[ReadReplicaTemplateConfig\]\(#readreplicatemplateconfig\)/, "[ReadReplicaTemplateConfig](../openbaocluster/#readreplicatemplateconfig)", line)
          gsub(/\[BackupSchedule\]\(#backupschedule\)/, "[BackupSchedule](../openbaocluster/#backupschedule)", line)
          gsub(/\[OpenBaoClusterSpec\]\(#openbaoclusterspec\)/, "[OpenBaoClusterSpec](../openbaocluster/#openbaoclusterspec)", line)
          gsub(/\[RestoreTest\]\(#restoretest\)/, "[RestoreTest](../openbaocluster/#restoretest)", line)
          gsub(/\[UnsealConfig\]\(#unsealconfig\)/, "[UnsealConfig](../openbaocluster/#unsealconfig)", line)
        }
        if (apply_errata == "true" && resource == "openbaocluster" && index(line, "| `tokenSecretRef`") == 1 && index(line, "backup operations") > 0) {
          line = "| `tokenSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | TokenSecretRef optionally references a same-namespace Secret containing an OpenBao API token for backup operations.<br />Set either `jwtAuthRole` or `tokenSecretRef`; 0.4.2 runtime readiness does not fall back to the generated root-token Secret.<br />If `jwtAuthRole` is set, this field is ignored in favor of JWT Auth. |  | Optional: \\{\\} <br /> |"
        }
        if (apply_errata == "true" && resource == "openbaocluster" && index(line, "| `DeleteAll`") == 1) {
          line = "| `DeleteAll` | DeletionPolicyDeleteAll currently deletes StatefulSets and PVCs.<br />External backup deletion is not implemented in 0.4.2; remove those objects explicitly. |"
        }
        if (apply_errata == "true" && resource == "openbaocluster" && index(line, "| `extraSANs`") == 1) {
          line = "| `extraSANs` _string array_ | ExtraSANs lists additional subject alternative names required in server certificates.<br />OperatorManaged includes them in generated certificates; External validates them against the supplied certificate. |  | Optional: \\{\\} <br /> |"
        }
        print line
      }
      END {
        if (!in_resource || !found_end) {
          exit 42
        }
      }
    ' "${source_path}" > "${body_path}"

    {
      printf '%s\n' '---'
      printf 'title: %s\n' "${title}"
      printf 'description: %s\n' "${description}"
      printf '%s\n' 'eyebrow: Reference · Generated API'
      printf 'weight: %s\n' "${weight}"
      printf '%s\n' 'verifiedBy:'
      printf '  - api/v1alpha1 at %s\n' "${source_ref}"
      printf '  - %s at %s\n' "${source_location}" "${source_ref}"
      if [[ "${value}" == "openbaocluster" && "${apply_errata}" == "true" ]]; then
        printf '  - internal/service/workloadidentity/readiness.go at %s\n' "${source_ref}"
        printf '  - internal/app/openbaocluster/deletionops/handler.go at %s\n' "${source_ref}"
        printf '  - internal/platform/openbaotls/validation.go at %s\n' "${source_ref}"
      fi
      printf '%s\n\n' '---'
      printf '%s\n\n' '{{< callout type="note" title="Generated reference" >}}'
      # shellcheck disable=SC2016 # Markdown backticks are literal; values use printf placeholders.
      printf 'This page is synchronized from the generated API reference at `%s` for the `%s` documentation line.\n' "${source_ref}" "${line}"
      if [[ "${value}" == "openbaocluster" && "${apply_errata}" == "true" ]]; then
        printf '%s\n' 'The sync also corrects three known 0.4.2 comment mismatches where runtime behavior is authoritative.'
      fi
      printf '%s\n\n' '{{< /callout >}}'
      cat "${body_path}"
    } > "${page_path}"

    perl -0pi -e 's/\n+\z/\n/' "${page_path}"
  }

  generate_page "openbaocluster" "OpenBaoCluster API" "Fields, defaults, and validation for the OpenBaoCluster API." 1
  generate_page "openbaorestore" "OpenBaoRestore API" "Fields, defaults, and validation for the OpenBaoRestore API." 2
  generate_page "openbaotenant" "OpenBaoTenant API" "Fields, defaults, and validation for the OpenBaoTenant API." 3

  local status=0
  local value
  for value in openbaocluster openbaorestore openbaotenant; do
    local generated_path="${line_tmp}/${value}.md"
    local destination_path="${output_dir}/${value}.md"

    if [[ "${MODE}" == "check" ]]; then
      if [[ ! -f "${destination_path}" ]] || ! cmp -s "${generated_path}" "${destination_path}"; then
        echo "Hugo API reference is out of date for ${line}: ${destination_path}" >&2
        status=1
      fi
      continue
    fi

    mkdir -p "${output_dir}"
    cp "${generated_path}" "${destination_path}"
  done

  return "${status}"
}

status=0
if [[ "${LINE}" == "all" ]]; then
  for line in 0.4.x 0.5.x next; do
    generate_line "${line}" || status=1
  done
else
  case "${LINE}" in
    0.4.x|0.5.x|next)
      generate_line "${LINE}" || status=1
      ;;
    *)
      usage
      exit 2
      ;;
  esac
fi

exit "${status}"
