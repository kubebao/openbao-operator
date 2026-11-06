#!/usr/bin/env bash

set -euo pipefail

: "${CHART_VERSION:?CHART_VERSION is required}"
: "${OWNER:?OWNER is required}"

CHART_DIR="${CHART_DIR:-charts/openbao-operator}"
CHART_FILE="${CHART_DIR}/Chart.yaml"
CHANGELOG_FILE="${CHANGELOG_FILE:-CHANGELOG.md}"
PRESERVE_CHANGE_METADATA="${PRESERVE_CHANGE_METADATA:-false}"

if [[ "${PRESERVE_CHANGE_METADATA}" != "true" && "${PRESERVE_CHANGE_METADATA}" != "false" ]]; then
  echo "PRESERVE_CHANGE_METADATA must be true or false" >&2
  exit 1
fi

if [[ ! -f "${CHART_FILE}" ]]; then
  echo "chart file not found: ${CHART_FILE}" >&2
  exit 1
fi

if [[ ! -f "${CHANGELOG_FILE}" ]]; then
  echo "changelog file not found: ${CHANGELOG_FILE}" >&2
  exit 1
fi

if [[ "${CHART_VERSION}" == *-* ]]; then
  prerelease="true"
else
  prerelease="false"
fi

if ! grep -Eq '^[[:space:]]*artifacthub\.io/prerelease:' "${CHART_FILE}"; then
  echo "artifacthub.io/prerelease annotation not found in ${CHART_FILE}" >&2
  exit 1
fi

sed -E -i.bak \
  's|^([[:space:]]*artifacthub\.io/prerelease:[[:space:]]*).*$|\1"'"${prerelease}"'"|' \
  "${CHART_FILE}"
rm -f "${CHART_FILE}.bak"

python3 - "${CHART_FILE}" "${CHANGELOG_FILE}" "${CHART_VERSION}" "${PRESERVE_CHANGE_METADATA}" <<'PY'
import json
import re
import sys
from pathlib import Path

chart_path = Path(sys.argv[1])
changelog_path = Path(sys.argv[2])
version = sys.argv[3]
preserve_change_metadata = sys.argv[4] == "true"


def heading_version(line):
    match = re.match(r"^## \[([^\]]+)\]\([^)]+\)(?: \([^)]+\))?$", line)
    if match:
        return match.group(1).strip()

    match = re.match(r"^## ([^(\n]+?)(?: \([^)]+\))?$", line)
    if match:
        return match.group(1).strip()

    return None


def change_kind(section):
    normalized = section.lower()
    if "security" in normalized:
        return "security"
    if "bug" in normalized or "fix" in normalized:
        return "fixed"
    if "feature" in normalized:
        return "added"
    if "deprecat" in normalized:
        return "deprecated"
    if "remove" in normalized:
        return "removed"
    return "changed"


def is_security_description(description):
    normalized = description.lower()
    if re.match(r"^[a-z0-9_,./-]*security[a-z0-9_,./-]*\s*:", normalized):
        return True
    if re.search(r"\b(cve-[0-9-]+|ghsa-[0-9a-z-]+|vulnerab\w*)\b", normalized):
        return True
    return bool(
        not normalized.startswith("docs:")
        and re.search(r"\bdenial[ -]of[ -]service\b", normalized)
        and re.match(r"(?:[a-z0-9_,./-]+:\s*)?(fix|patch|prevent|resolve|address|mitigate)\b", normalized)
    )


def clean_description(raw):
    value = re.sub(r"\s+\(\[#\d+\]\([^)]+\)\)", "", raw)
    value = re.sub(r"\s+\(\[[0-9a-f]{7,40}\]\([^)]+\)\)", "", value)
    value = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", value)
    value = value.replace("**", "").replace("`", "")
    value = re.sub(r"\s+", " ", value).strip()
    return value


def section_changes(lines, start, end):
    changes = []
    current_kind = "changed"
    current_change = None

    for line in lines[start:end]:
        section_match = re.match(r"^### (.+)$", line)
        if section_match:
            current_kind = change_kind(section_match.group(1))
            current_change = None
            continue

        bullet_match = re.match(r"^\* (.+)$", line)
        if bullet_match:
            description = clean_description(bullet_match.group(1))
            if description:
                current_change = {"kind": current_kind, "description": description}
                changes.append(current_change)
            continue

        if current_change and line.startswith(("  ", "\t")):
            continuation = clean_description(line)
            if continuation:
                current_change["description"] = f"{current_change['description']} {continuation}"

    for change in changes:
        if is_security_description(change["description"]):
            change["kind"] = "security"

    return changes


def release_changes(changelog):
    lines = changelog.splitlines()
    headings = []
    for index, line in enumerate(lines):
        heading = heading_version(line)
        if heading is not None:
            headings.append((index, heading))

    if not any(heading == version for _, heading in headings):
        raise SystemExit(f"release {version} was not found in {changelog_path}")

    stable_version = re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version) is not None
    changes = []
    for position, (heading_index, heading) in enumerate(headings):
        include = heading == version
        if stable_version and heading.startswith(f"{version}-"):
            include = True
        if not include:
            continue

        start = heading_index + 1
        end = headings[position + 1][0] if position + 1 < len(headings) else len(lines)
        changes.extend(section_changes(lines, start, end))

    deduplicated = []
    seen = {}
    for change in changes:
        description = change["description"]
        if description in seen:
            if change["kind"] == "security":
                deduplicated[seen[description]]["kind"] = "security"
            continue
        seen[description] = len(deduplicated)
        deduplicated.append(change)

    changes = deduplicated
    if not changes:
        changes.append({"kind": "changed", "description": f"Release {version}"})

    return changes


def replace_annotation_block(lines, key, replacement):
    start = None
    for index, line in enumerate(lines):
        if re.match(rf"^  {re.escape(key)}:", line):
            start = index
            break

    if start is None:
        insert_at = None
        for index, line in enumerate(lines):
            if re.match(r"^  artifacthub\.io/images:", line):
                insert_at = index
                break
        if insert_at is None:
            raise SystemExit(f"{key} annotation not found and artifacthub.io/images insertion point is missing")
        return lines[:insert_at] + replacement + lines[insert_at:]

    end = start + 1
    while end < len(lines):
        line = lines[end]
        if re.match(r"^  artifacthub\.io/", line) or re.match(r"^[^ ]", line):
            break
        end += 1

    return lines[:start] + replacement + lines[end:]


def replace_contains_security_updates(lines, enabled):
    replacement = f"  artifacthub.io/containsSecurityUpdates: '{str(enabled).lower()}'"
    for index, line in enumerate(lines):
        if re.match(r"^  artifacthub\.io/containsSecurityUpdates:", line):
            lines[index] = replacement
            return lines
    raise SystemExit("artifacthub.io/containsSecurityUpdates annotation not found")


chart_lines = chart_path.read_text().splitlines()
if not preserve_change_metadata:
    changes = release_changes(changelog_path.read_text())
    has_security_changes = any(change["kind"] == "security" for change in changes)

    changes_block = ["  artifacthub.io/changes: |"]
    for change in changes:
        changes_block.append(f"    - kind: {change['kind']}")
        changes_block.append(f"      description: {json.dumps(change['description'])}")

    chart_lines = replace_contains_security_updates(chart_lines, has_security_changes)
    chart_lines = replace_annotation_block(chart_lines, "artifacthub.io/changes", changes_block)
chart_path.write_text("\n".join(chart_lines) + "\n")
PY

if ! grep -Eq '^[[:space:]]*artifacthub\.io/images:' "${CHART_FILE}"; then
  echo "artifacthub.io/images annotation not found in ${CHART_FILE}" >&2
  exit 1
fi

awk -v owner="${OWNER}" -v version="${CHART_VERSION}" '
  BEGIN {in_images=0; replaced=0}
  /^[[:space:]]*artifacthub\.io\/images:[[:space:]]*\|[[:space:]]*$/ {
    print "  artifacthub.io/images: |"
    print "    - name: openbao-operator"
    print "      image: ghcr.io/" owner "/openbao-operator:" version
    print "    - name: openbao-init"
    print "      image: ghcr.io/" owner "/openbao-init:" version
    print "    - name: openbao-backup"
    print "      image: ghcr.io/" owner "/openbao-backup:" version
    print "    - name: openbao-upgrade"
    print "      image: ghcr.io/" owner "/openbao-upgrade:" version
    in_images=1
    replaced=1
    next
  }
  in_images {
    if ($0 ~ /^  artifacthub\.io\//) {
      in_images=0
    } else {
      next
    }
  }
  { print }
  END {
    if (replaced != 1) {
      exit 44
    }
  }
' "${CHART_FILE}" > "${CHART_FILE}.tmp" || {
  code="$?"
  rm -f "${CHART_FILE}.tmp"
  if [[ "${code}" == "44" ]]; then
    echo "failed to replace artifacthub.io/images annotation block" >&2
  fi
  exit "${code}"
}
mv "${CHART_FILE}.tmp" "${CHART_FILE}"

actual_prerelease="$(sed -nE 's/^[[:space:]]*artifacthub\.io\/prerelease:[[:space:]]*"(true|false)"[[:space:]]*$/\1/p' "${CHART_FILE}" | head -n1)"
if [[ -z "${actual_prerelease}" || "${actual_prerelease}" != "${prerelease}" ]]; then
  echo "artifacthub.io/prerelease mismatch: expected=${prerelease} actual=${actual_prerelease:-<empty>}" >&2
  exit 1
fi

if ! grep -Eq '^[[:space:]]*artifacthub\.io/changes:[[:space:]]*\|[[:space:]]*$' "${CHART_FILE}"; then
  echo "artifacthub.io/changes annotation not found in ${CHART_FILE}" >&2
  exit 1
fi

if ! grep -Eq "^[[:space:]]*artifacthub\.io/containsSecurityUpdates:[[:space:]]*['\"]?(true|false)['\"]?[[:space:]]*$" "${CHART_FILE}"; then
  echo "artifacthub.io/containsSecurityUpdates annotation missing or invalid in ${CHART_FILE}" >&2
  exit 1
fi

if ! grep -Eq '^[[:space:]]*-[[:space:]]*kind:[[:space:]]*(added|changed|deprecated|removed|fixed|security)[[:space:]]*$' "${CHART_FILE}"; then
  echo "artifacthub.io/changes entries missing or invalid in ${CHART_FILE}" >&2
  exit 1
fi

images=(
  openbao-operator
  openbao-init
  openbao-backup
  openbao-upgrade
)
for image in "${images[@]}"; do
  if ! grep -Eq "^[[:space:]]*image:[[:space:]]*ghcr\\.io/${OWNER}/${image}:${CHART_VERSION}[[:space:]]*$" "${CHART_FILE}"; then
    echo "artifacthub.io/images entry missing or invalid for ${image}" >&2
    exit 1
  fi
done
