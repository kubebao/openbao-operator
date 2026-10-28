#!/usr/bin/env python3
"""Validate the opt-in qualification helper policy and emit its public fixture patch."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import sys


ISSUER = "https://token.actions.githubusercontent.com"
SUBJECT = ("https://github.com/kubebao/openbao-operator/.github/workflows/"
           "release-qualification-pilot.yml@refs/heads/main")
PULL_REFERENCE_NAME = "release-qualification-registry"


def require(condition):
    if not condition:
        raise ValueError("unapproved qualification helper policy")


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result)
        result[key] = value
    return result


def bounded(path):
    with Path(path).open("rb") as stream:
        data = stream.read(65537)
    require(len(data) <= 65536)
    return data


def load(image):
    path, expected, auth_path = (os.environ.get(key, "") for key in (
        "E2E_QUALIFICATION_POLICY_FILE", "E2E_QUALIFICATION_POLICY_SHA256", "E2E_QUALIFICATION_AUTH_FILE"))
    require(path and re.fullmatch(r"[0-9a-f]{64}", expected) and auth_path)
    data = bounded(path)
    require(hashlib.sha256(data).hexdigest() == expected)
    selected = json.loads(data, object_pairs_hook=unique)
    require(type(selected) is dict and set(selected) == {
        "schemaVersion", "claim", "planSHA256", "controllerSHA", "issuer", "subject", "images"})
    require(type(selected["schemaVersion"]) is int and selected["schemaVersion"] == 1
            and selected["claim"] == "release-candidate-qualification-pilot"
            and selected["issuer"] == ISSUER and selected["subject"] == SUBJECT)
    for key, length in (("controllerSHA", 40), ("planSHA256", 64)):
        require(type(selected[key]) is str and re.fullmatch(r"[0-9a-f]{" + str(length) + "}", selected[key]))
    require(type(selected["images"]) is dict and set(selected["images"]) == {"operator", "init", "backup", "upgrade"})
    for role, ref in selected["images"].items():
        require(type(ref) is str and re.fullmatch(
            r"ghcr\.io/kubebao/openbao-" + role + r"-qualification@sha256:[0-9a-f]{64}", ref))
    require(image == selected["images"]["init"])
    auth = json.loads(bounded(auth_path), object_pairs_hook=unique)
    require(type(auth) is dict and set(auth) == {"auths"} and set(auth["auths"]) == {"ghcr.io"}
            and set(auth["auths"]["ghcr.io"]) == {"auth"})
    decoded = base64.b64decode(auth["auths"]["ghcr.io"]["auth"], validate=True).decode("utf-8")
    username, separator, password = decoded.partition(":")
    require(username and separator and password)
    return selected


def patch(selected):
    # These are public object references, never registry credential contents.
    pull_references = [{"name": PULL_REFERENCE_NAME}]
    return {"spec": {"operatorImageVerification": {
        "enabled": True, "issuer": selected["issuer"], "subject": selected["subject"],
        "failurePolicy": "Block", "ignoreTlog": False, "imagePullSecrets": pull_references},
        "imagePullSecrets": pull_references}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("validate", "patch"))
    parser.add_argument("--init-image", required=True)
    args = parser.parse_args()
    try:
        selected = load(args.init_image)
        if args.command == "patch":
            print(json.dumps(patch(selected), separators=(",", ":")))
    except (ValueError, TypeError, KeyError, OSError, RecursionError):
        # Never include credential bytes or parser excerpts in job logs.
        print("qualification helper policy validation failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
