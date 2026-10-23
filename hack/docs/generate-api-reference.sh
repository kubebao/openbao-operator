#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CRD_REF_DOCS_BIN="${CRD_REF_DOCS_BIN:-}"
if [[ -z "${CRD_REF_DOCS_BIN}" ]]; then
  echo "CRD_REF_DOCS_BIN is required (set by 'make api-reference')." >&2
  exit 1
fi

if [[ ! -x "${CRD_REF_DOCS_BIN}" ]]; then
  echo "crd-ref-docs binary not found or not executable: ${CRD_REF_DOCS_BIN}" >&2
  exit 1
fi

if ! command -v perl >/dev/null 2>&1; then
  echo "perl is required to generate the API reference." >&2
  exit 1
fi

SOURCE_PATH="./api"
CONFIG_PATH="hack/docs/crd-ref-docs.yaml"
PUBLISHED_OUT_PATH="website/generated/api-reference.md"
OUT_PATH=""

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"; if [[ -n "${OUT_PATH}" ]]; then rm -f "${OUT_PATH}"; fi' EXIT

RAW_PATH="${TMP_DIR}/api-reference.raw.md"
KINDS_PATH="${TMP_DIR}/api-reference.kinds.txt"

"${CRD_REF_DOCS_BIN}" \
  --renderer=markdown \
  --source-path="${SOURCE_PATH}" \
  --config="${CONFIG_PATH}" \
  --output-path="${RAW_PATH}" \
  --log-level=ERROR

# Discover top-level CRD kinds from the generated Resource Types section.
awk '
/^### Resource Types$/ { in_types = 1; next }
in_types && /^$/ { exit }
in_types && /^- \[/ {
  kind = $0
  sub(/^- \[/, "", kind)
  sub(/\].*$/, "", kind)
  if (kind !~ /List$/) {
    print kind
  }
}
' "${RAW_PATH}" > "${KINDS_PATH}"

mapfile -t RESOURCE_KINDS < "${KINDS_PATH}"

mkdir -p "$(dirname "${PUBLISHED_OUT_PATH}")"
# Keep the temporary output on the destination filesystem for atomic publication.
OUT_PATH="$(mktemp "${PUBLISHED_OUT_PATH}.tmp.XXXXXX")"

cat > "${OUT_PATH}" <<'HDR'
# OpenBao Operator API reference source

This intermediate document is generated from `api/v1alpha1` by `make api-reference`.
Do not edit it manually. Hugo splits the resource sections into the versioned API reference pages.
HDR

if [[ "${#RESOURCE_KINDS[@]}" -gt 0 ]]; then
  {
    echo
    echo "## CRDs"
    echo
  } >> "${OUT_PATH}"

  for kind in "${RESOURCE_KINDS[@]}"; do
    KIND_BODY_PATH="${TMP_DIR}/${kind}.body.md"

    python3 - "$RAW_PATH" "$kind" "$KIND_BODY_PATH" <<'PY'
import collections
import re
import sys

raw_path, root_kind, out_path = sys.argv[1:4]

with open(raw_path, encoding="utf-8") as f:
    lines = [line.rstrip("\n") for line in f]

# Remove generated page title; wrapper page defines its own title.
stripped = []
skipped_title = False
for line in lines:
    if not skipped_title and line == "# API Reference":
        skipped_title = True
        continue
    stripped.append(line)
lines = stripped

section_starts = [i for i, line in enumerate(lines) if line.startswith("#### ")]
first_section_start = section_starts[0] if section_starts else len(lines)

header_start = 0
for i, line in enumerate(lines):
    if line.startswith("## Packages"):
        header_start = i
        break

header_lines = lines[header_start:first_section_start]

order = []
sections = {}
edges = collections.defaultdict(set)

for idx, start in enumerate(section_starts):
    end = section_starts[idx + 1] if idx + 1 < len(section_starts) else len(lines)
    section_name = lines[start][5:].strip()
    section_lines = lines[start:end]

    order.append(section_name)
    sections[section_name] = section_lines

    appears_idx = None
    for i, line in enumerate(section_lines):
        if line.strip() == "_Appears in:_":
            appears_idx = i
            break

    if appears_idx is None:
        continue

    i = appears_idx + 1
    while i < len(section_lines):
        candidate = section_lines[i].strip()
        if candidate.startswith("- ["):
            match = re.match(r"- \[([^\]]+)\]\(#", candidate)
            if match:
                parent = match.group(1)
                edges[parent].add(section_name)
            i += 1
            continue

        if candidate == "":
            i += 1
            continue

        break

reachable = {root_kind}
queue = collections.deque([root_kind])
while queue:
    parent = queue.popleft()
    for child in sorted(edges.get(parent, [])):
        if child not in reachable:
            reachable.add(child)
            queue.append(child)

# Filter resource type bullets in header to the selected CRD.
filtered_header = []
in_resource_types = False
for line in header_lines:
    if line.startswith("### Resource Types"):
        in_resource_types = True
        filtered_header.append(line)
        continue

    if in_resource_types:
        if line.startswith("- ["):
            if line.startswith(f"- [{root_kind}]("):
                filtered_header.append(line)
            continue

        if line.strip() == "":
            filtered_header.append(line)
            continue

        in_resource_types = False

    filtered_header.append(line)

while filtered_header and filtered_header[0] == "":
    filtered_header.pop(0)

out_lines = []
out_lines.extend(filtered_header)
if out_lines and out_lines[-1] != "":
    out_lines.append("")

for section_name in order:
    if section_name in reachable:
        # Anonymous structs have no section anchor in crd-ref-docs output.
        out_lines.extend(
            re.sub(r"\[([^\]]+)\]\(#struct\{[^)]*\}\)", r"`\1`", line)
            for line in sections[section_name]
        )
        if out_lines and out_lines[-1] != "":
            out_lines.append("")

while out_lines and out_lines[-1] == "":
    out_lines.pop()

with open(out_path, "w", encoding="utf-8") as f:
    f.write("\n".join(out_lines) + "\n")
PY

    perl -0pi -e 's/<([A-Za-z][A-Za-z0-9._-]*)>/&lt;$1&gt;/g' "${KIND_BODY_PATH}"

    {
      echo "<!-- BEGIN RESOURCE $(printf '%s' "${kind}" | tr '[:upper:]' '[:lower:]') -->"
      echo
      cat "${KIND_BODY_PATH}"
      echo
      echo "<!-- END RESOURCE -->"
      echo
    } >> "${OUT_PATH}"
  done
else
  {
    echo
    awk '
    BEGIN { skipped = 0 }
    !skipped && $0 == "# API Reference" { skipped = 1; next }
    { print }
    ' "${RAW_PATH}"
  } >> "${OUT_PATH}"
fi

# Keep the generated artifact stable and avoid accumulating blank lines at EOF.
perl -0pi -e 's/\n+\z/\n/' "${OUT_PATH}"

chmod 0644 "${OUT_PATH}"
mv "${OUT_PATH}" "${PUBLISHED_OUT_PATH}"
