"""Bind test artifacts to their workflow run. These checks do not establish trust."""

import hashlib
import json
import os
import re
from pathlib import Path


def digest(path):
    checksum = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(chunk)
    return checksum.hexdigest()


def run_identity(sha):
    if os.environ.get("GITHUB_ACTIONS") != "true":
        return None
    required = ("GITHUB_REPOSITORY", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_SHA", "GITHUB_EVENT_PATH")
    if any(not os.environ.get(key) for key in required):
        raise ValueError("GitHub artifact identity is incomplete")
    if sha != os.environ["GITHUB_SHA"] or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("artifact source differs from the workflow commit")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    head = event["pull_request"]["head"]["sha"] if "pull_request" in event else sha
    if not re.fullmatch(r"[0-9a-f]{40}", head):
        raise ValueError("invalid PR head SHA")
    run_id, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    if not run_id.isdecimal() or not attempt.isdecimal() or int(attempt) < 1:
        raise ValueError("invalid workflow run or attempt")
    return {"repository": os.environ["GITHUB_REPOSITORY"], "runId": run_id,
            "buildAttempt": int(attempt), "headSHA": head, "testedSHA": sha}


def verify_identity(recorded, sha, producer_attempt=None):
    current = run_identity(sha)
    if current is None:
        if recorded is not None:
            raise ValueError("workflow artifact requires its GitHub run context")
        return
    if not isinstance(recorded, dict):
        raise ValueError("workflow artifact identity is missing")
    attempt = recorded.get("buildAttempt")
    if type(attempt) is not int or not 1 <= attempt <= current["buildAttempt"]:
        raise ValueError("artifact build attempt is invalid or from the future")
    if producer_attempt is not None and attempt != producer_attempt:
        raise ValueError("artifact differs from the selected producer attempt")
    # Failed-job reruns retain successful producers from an earlier attempt.
    if recorded != {**current, "buildAttempt": attempt}:
        raise ValueError("artifact belongs to another repository, run, or source")
