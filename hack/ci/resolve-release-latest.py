#!/usr/bin/env python3
"""Select Latest only for a release at least as new as every published stable release."""

import json
import os
import re
import subprocess
import sys

STABLE_VERSION = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
)


def stable_version(tag):
    match = STABLE_VERSION.fullmatch(tag.removeprefix("v"))
    return tuple(int(value) for value in match.groups()) if match else None


def make_latest(version, releases):
    target = stable_version(version)
    if target is None or version.startswith("v"):
        raise ValueError("VERSION must be a stable SemVer tag without a v prefix")
    latest = True
    for release in releases:
        if not isinstance(release, dict) or any(
            type(release.get(field)) is not bool for field in ("draft", "prerelease")
        ):
            raise ValueError("GitHub returned malformed release metadata")
        if release["draft"] or release["prerelease"]:
            continue
        tag = release.get("tag_name")
        published_at = release.get("published_at")
        if not isinstance(tag, str) or not isinstance(published_at, str) or not published_at:
            raise ValueError("GitHub returned a stable release without its tag or publication time")
        published = stable_version(tag)
        if published is not None and published > target:
            latest = False
    return latest


def main():
    repo = os.environ.get("REPO", "")
    version = os.environ.get("VERSION", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo):
        raise ValueError("REPO must be owner/repository")
    result = subprocess.run(
        ["gh", "api", f"repos/{repo}/releases", "--paginate", "--slurp"],
        text=True, capture_output=True, check=True,
    )
    pages = json.loads(result.stdout)
    if not isinstance(pages, list) or not all(isinstance(page, list) for page in pages):
        raise ValueError("GitHub returned malformed release pages")
    latest = make_latest(version, [release for page in pages for release in page])
    print("true" if latest else "false")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr.rstrip(), file=sys.stderr)
        print(f"Cannot select Latest: {error}", file=sys.stderr)
        sys.exit(1)
