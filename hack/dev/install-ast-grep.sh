#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AST_GREP_VERSION="${AST_GREP_VERSION:?AST_GREP_VERSION is required}"
AST_GREP_DESTINATION="${AST_GREP_DESTINATION:-${ROOT_DIR}/bin/ast-grep}"

# These digests come from the GitHub release asset metadata. Update them together
# with AST_GREP_VERSION in mk/dependencies.mk.
case "$(uname -s):$(uname -m)" in
  Darwin:arm64 | Darwin:aarch64)
    target="aarch64-apple-darwin"
    expected_sha256="12a870c414c90208f338649b0b53d9659b724b680edaf9da9c151275dad3e41a"
    ;;
  Darwin:x86_64 | Darwin:amd64)
    target="x86_64-apple-darwin"
    expected_sha256="af5a04a43c062974634296f692ab93c03755e5b6f33e70e226a434cde1355a1f"
    ;;
  Linux:arm64 | Linux:aarch64)
    target="aarch64-unknown-linux-gnu"
    expected_sha256="46f7ffedb5f770f58bf59bd8792009dc71ec34c94e0bd1b4575ba639f32a9889"
    ;;
  Linux:x86_64 | Linux:amd64)
    target="x86_64-unknown-linux-gnu"
    expected_sha256="4191ac4247d183c502778e740a68b7cf45fe477b6423c43b8b8d6e732ba3b333"
    ;;
  *)
    echo "unsupported ast-grep platform: $(uname -s) $(uname -m)" >&2
    exit 1
    ;;
esac

command -v curl >/dev/null 2>&1 || {
  echo "curl is required to install ast-grep ${AST_GREP_VERSION}" >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  echo "python3 is required to verify and extract ast-grep ${AST_GREP_VERSION}" >&2
  exit 1
}

destination_dir="$(dirname "${AST_GREP_DESTINATION}")"
mkdir -p "${destination_dir}"
destination_dir="$(cd "${destination_dir}" && pwd)"
AST_GREP_DESTINATION="${destination_dir}/$(basename "${AST_GREP_DESTINATION}")"
versioned_binary="${AST_GREP_DESTINATION}-${AST_GREP_VERSION}-${target}"

if [[ -x "${versioned_binary}" ]] &&
  [[ "$("${versioned_binary}" --version 2>/dev/null || true)" == "ast-grep ${AST_GREP_VERSION}" ]]; then
  ln -sfn "${versioned_binary}" "${AST_GREP_DESTINATION}"
  echo "ast-grep ${AST_GREP_VERSION} already installed at ${AST_GREP_DESTINATION}"
  exit 0
fi

archive_name="app-${target}.zip"
archive_url="https://github.com/ast-grep/ast-grep/releases/download/${AST_GREP_VERSION}/${archive_name}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
archive_path="${tmp_dir}/${archive_name}"

echo "Downloading ast-grep ${AST_GREP_VERSION} for ${target}"
curl --fail --silent --show-error --location --retry 3 \
  "${archive_url}" \
  --output "${archive_path}"

actual_sha256="$(python3 - "${archive_path}" <<'PY'
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
digest = hashlib.sha256()
with path.open("rb") as archive:
    for chunk in iter(lambda: archive.read(1024 * 1024), b""):
        digest.update(chunk)
print(digest.hexdigest())
PY
)"
if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
  echo "ast-grep checksum mismatch for ${archive_name}" >&2
  echo "expected: ${expected_sha256}" >&2
  echo "actual:   ${actual_sha256}" >&2
  exit 1
fi

python3 -m zipfile -e "${archive_path}" "${tmp_dir}/extracted"
downloaded_binary="${tmp_dir}/extracted/ast-grep"
if [[ ! -f "${downloaded_binary}" ]]; then
  echo "ast-grep release archive did not contain the ast-grep binary" >&2
  exit 1
fi
chmod 0755 "${downloaded_binary}"

# The pinned devenv supplies an immutable loader and library path on Linux.
# NixOS does not provide the GNU archive's /lib64 ELF interpreter.
linux_loader="${AST_GREP_LINUX_LOADER:-}"
linux_library_path="${AST_GREP_LINUX_LIBRARY_PATH:-}"
runner=("${downloaded_binary}")
if [[ "${target}" == *-linux-gnu && -n "${linux_loader}${linux_library_path}" ]]; then
  if [[ ! -x "${linux_loader}" || -z "${linux_library_path}" ]]; then
    echo "ast-grep Linux loader must be executable and its library path must be set" >&2
    exit 1
  fi
  runner=("${linux_loader}" --library-path "${linux_library_path}" "${downloaded_binary}")
fi
if ! downloaded_version="$("${runner[@]}" --version 2>"${tmp_dir}/runtime-error")"; then
  cat "${tmp_dir}/runtime-error" >&2
  echo "ast-grep ${AST_GREP_VERSION} could not execute; use the pinned devenv on NixOS" >&2
  exit 1
fi
if [[ "${downloaded_version}" != "ast-grep ${AST_GREP_VERSION}" ]]; then
  echo "ast-grep version mismatch: expected ${AST_GREP_VERSION}, found ${downloaded_version:-unknown}" >&2
  exit 1
fi

staged_binary="${versioned_binary}.tmp.$$"
if [[ "${#runner[@]}" -gt 1 ]]; then
  raw_binary="${versioned_binary}.elf"
  cp "${downloaded_binary}" "${raw_binary}.tmp.$$"
  chmod 0755 "${raw_binary}.tmp.$$"
  mv "${raw_binary}.tmp.$$" "${raw_binary}"
  python3 - "${staged_binary}" "${linux_loader}" "${linux_library_path}" "${raw_binary}" <<'PYWRAPPER'
import pathlib
import shlex
import sys

output, loader, libraries, binary = sys.argv[1:]
command = " ".join(map(shlex.quote, [loader, "--library-path", libraries, binary]))
pathlib.Path(output).write_text("#!/bin/sh\nexec " + command + ' "$@"\n')
PYWRAPPER
else
  cp "${downloaded_binary}" "${staged_binary}"
fi
chmod 0755 "${staged_binary}"
mv "${staged_binary}" "${versioned_binary}"
ln -sfn "${versioned_binary}" "${AST_GREP_DESTINATION}"
echo "Installed ast-grep ${AST_GREP_VERSION} at ${AST_GREP_DESTINATION}"
