#!/usr/bin/env python3
"""Record and verify the run-scoped E2E executable bundle before execution."""

import json
import os
from pathlib import Path
import platform
import re
import sys

sys.dont_write_bytecode = True

from e2e_artifacts import digest, run_identity, verify_identity

BINARIES = ("e2e.test", "ginkgo", "e2e_report")


def versions():
    module = Path("go.mod").read_text()
    go = re.search(r"^go ([0-9.]+)$", module, re.MULTILINE)
    ginkgo = re.search(r"^\s*github.com/onsi/ginkgo/v2 (v\S+)", module, re.MULTILINE)
    if not go or not ginkgo:
        raise ValueError("go.mod must declare Go and Ginkgo versions")
    return "go" + go[1], ginkgo[1]


def record(bundle, sha):
    go_version, ginkgo_version = versions()
    built_go = os.environ["E2E_BUILD_GOVERSION"]
    if built_go != go_version:
        raise ValueError(f"build toolchain {built_go} differs from pinned {go_version}")
    metadata = {
        "version": 1,
        "workflow": run_identity(sha),
        "sha": sha,
        "goos": os.environ["E2E_BUILD_GOOS"],
        "goarch": os.environ["E2E_BUILD_GOARCH"],
        "goVersion": built_go,
        "ginkgoVersion": ginkgo_version,
        "cgoEnabled": os.environ["E2E_BUILD_CGO"],
        "buildTags": ["e2e"],
        "sha256": {name: digest(bundle / name) for name in BINARIES},
    }
    (bundle / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")


def verify(bundle, sha):
    metadata = json.loads((bundle / "metadata.json").read_text())
    if not isinstance(metadata, dict):
        raise ValueError("E2E bundle metadata must be an object")
    verify_identity(metadata.get("workflow"), sha)
    go_version, ginkgo_version = versions()
    machine = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    expected = {
        "version": 1,
        "sha": sha,
        "goos": platform.system().lower(),
        "goarch": machine,
        "goVersion": go_version,
        "ginkgoVersion": ginkgo_version,
        "buildTags": ["e2e"],
    }
    if not sha or machine is None:
        raise ValueError("source SHA and supported runner architecture are required")
    for key, value in expected.items():
        if metadata.get(key) != value:
            raise ValueError(f"E2E bundle {key}: got {metadata.get(key)!r}, expected {value!r}")
    if metadata.get("cgoEnabled") not in ("0", "1"):
        raise ValueError("E2E bundle CGO identity is missing")
    checksums = metadata.get("sha256", {})
    if not isinstance(checksums, dict) or set(checksums) != set(BINARIES):
        raise ValueError("E2E bundle must identify all three executables")
    for name in BINARIES:
        binary = bundle / name
        if binary.is_symlink() or not binary.is_file() or digest(binary) != checksums[name]:
            raise ValueError(f"E2E bundle checksum mismatch or missing executable: {name}")
    print(f"Verified E2E binaries for {sha} ({metadata['goos']}/{metadata['goarch']}, {go_version}).")


def main():
    if len(sys.argv) != 4 or sys.argv[1] not in ("record", "verify"):
        raise ValueError("usage: e2e-binaries.py record|verify BUNDLE_DIR SOURCE_SHA")
    command, directory, sha = sys.argv[1:]
    if not sha:
        raise ValueError("source SHA is required")
    if command == "record":
        record(Path(directory), sha)
    else:
        verify(Path(directory), sha)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"e2e-binaries: {error}", file=sys.stderr)
        sys.exit(1)
