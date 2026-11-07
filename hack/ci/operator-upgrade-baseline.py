#!/usr/bin/env python3
"""Refresh reviewed upgrade pins or check them against published release metadata."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
STABLE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


def gh(*args):
    return json.loads(subprocess.check_output(["gh", *args], text=True))


def previous_stable(releases, target):
    version = STABLE.fullmatch(target.split("-", 1)[0].split("+", 1)[0])
    if not version:
        raise ValueError("target must be a semantic version")
    ceiling = tuple(map(int, version.groups()))
    versions = []
    for release in releases:
        match = STABLE.fullmatch(release["tag_name"])
        if match and not release["draft"] and not release["prerelease"] and release["published_at"]:
            number = tuple(map(int, match.groups()))
            if number < ceiling:
                versions.append((number, release["tag_name"]))
    if not versions:
        raise ValueError(f"no published stable release precedes {target}")
    return max(versions)[1]


def release_pins(index, version):
    release = index["release"]
    if release["tag"] != version or release["source_ref"] != f"refs/tags/{version}":
        raise ValueError("provenance index does not match the baseline release")
    chart = index["chart"]["ref"]
    if not re.fullmatch(r"ghcr\.io/(kubebao|dc-tec)/charts/openbao-operator", chart):
        raise ValueError("baseline chart is not an official chart")
    chart_digest = index["chart"]["digest"]
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", chart_digest):
        raise ValueError("baseline chart digest is invalid")
    if index["chart"]["oci_subject"] != f"{chart}@{chart_digest}":
        raise ValueError("baseline chart subject does not match its digest")
    pins = {"OPERATOR_UPGRADE_BASELINE": version, "OPERATOR_UPGRADE_BASELINE_CHART": f"oci://{chart}",
            "OPERATOR_UPGRADE_BASELINE_CHART_DIGEST": chart_digest}
    for name, field in [("openbao-operator", "MANAGER"), ("openbao-init", "INIT")]:
        images = [image for image in index["images"] if image["name"] == name]
        if len(images) != 1:
            raise ValueError(f"provenance index must contain exactly one {name} image")
        image = images[0]
        if not re.fullmatch(rf"ghcr\.io/(kubebao|dc-tec)/{name}", image["ref"]):
            raise ValueError(f"baseline {name} image is not official")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", image["digest"]):
            raise ValueError(f"baseline {name} digest is invalid")
        subject = f"{image['ref']}@{image['digest']}"
        if image["oci_subject"] != subject:
            raise ValueError(f"baseline {name} subject does not match its digest")
        pins[f"OPERATOR_UPGRADE_BASELINE_{field}_IMAGE"] = subject
    return pins


def read_pins(path):
    return dict(line.split("=", 1) for line in path.read_text().splitlines() if line and not line.startswith("#"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["check", "refresh"])
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", "kubebao/openbao-operator"))
    parser.add_argument("--target", default=os.environ.get("OPERATOR_UPGRADE_E2E_TARGET_RELEASE"))
    parser.add_argument("--version", help="Published stable baseline to pin with refresh")
    args = parser.parse_args()
    path = ROOT / "test/fixtures/operator-upgrade/versions.env"
    current = read_pins(path)
    if args.operation == "check":
        target = args.target or json.loads((ROOT / ".release-please-manifest.json").read_text())["."]
        pages = gh("api", "--paginate", "--slurp", f"repos/{args.repo}/releases?per_page=100")
        version = previous_stable([release for page in pages for release in page], target)
    else:
        version = args.version
        if not version or not STABLE.fullmatch(version):
            raise ValueError("refresh requires --version with a stable release")
    release = gh("release", "view", version, "--repo", args.repo, "--json", "tagName,isDraft,isPrerelease")
    if release["tagName"] != version or release["isDraft"] or release["isPrerelease"]:
        raise ValueError("baseline must be a published stable release")
    index = gh("release", "download", version, "--repo", args.repo,
               "--pattern", "provenance-index.json", "--output", "-")
    expected = release_pins(index, version)
    if args.operation == "check":
        mismatches = [key for key, value in expected.items() if current.get(key) != value]
        if mismatches:
            raise ValueError("upgrade baseline is stale: " + ", ".join(mismatches) +
                             f"; run make update-operator-upgrade-baseline BASELINE_VERSION={version}")
    else:
        contents = path.read_text()
        for key, value in expected.items():
            pattern = rf"(?m)^{key}=.*$"
            if re.search(pattern, contents):
                contents = re.sub(pattern, f"{key}={value}", contents)
            else:
                contents += f"{key}={value}\n"
        fixture = ROOT / "test/fixtures/operator-upgrade/hardened-cluster.yaml"
        old = current["OPERATOR_UPGRADE_BASELINE_INIT_IMAGE"]
        if fixture.read_text().count(f'    image: "{old}"') != 1:
            raise ValueError("Hardened fixture does not contain the current baseline init pin exactly once")
        fixture.write_text(fixture.read_text().replace(old, expected["OPERATOR_UPGRADE_BASELINE_INIT_IMAGE"]))
        path.write_text(contents)
    print(f"Operator upgrade baseline {args.operation} passed: {version}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
