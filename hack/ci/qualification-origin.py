#!/usr/bin/env python3
"""Authenticate same-attempt qualification artifacts before parsing their data."""

import datetime
import hashlib
from functools import lru_cache
import json
from pathlib import Path
import re
import subprocess
import zipfile


REPOSITORY = "kubebao/openbao-operator"
REPOSITORY_ID = 1127385211
WORKFLOW = ".github/workflows/release-qualification-pilot.yml"
MAX_BYTES = 2 * 1024 * 1024 * 1024


class OriginPolicyError(ValueError):
    """A controlled producing-origin or archive validation reason."""


def require(condition, message):
    if not condition:
        raise OriginPolicyError(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate metadata key")
        result[key] = value
    return result


@lru_cache(maxsize=8)
def api(path, paginated=False):
    args = ["gh", "api", "--hostname", "github.com", f"repos/{REPOSITORY}/{path}"]
    if paginated:
        args += ["--paginate", "--slurp"]
    result = subprocess.run(args, capture_output=True, timeout=120)
    require(result.returncode == 0 and len(result.stdout) <= 16 * 1024 * 1024, "artifact metadata request failed")
    return json.loads(result.stdout, object_pairs_hook=unique)


def timestamp(value):
    parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    require(parsed.tzinfo is not None, "metadata timestamp lacks timezone")
    return parsed


def origin(run, jobs, artifacts, context, name, job_name, now):
    """Reject clones from prior attempts even when GitHub reports them as successful."""
    run_id, attempt, controller = context["runID"], context["attempt"], context["sourceSHA"]
    require(type(run_id) is int and run_id > 0 and type(attempt) is int and attempt > 0
            and re.fullmatch(r"[0-9a-f]{40}", controller), "invalid selected execution")
    require(run["id"] == run_id and run["run_attempt"] == attempt and run["head_sha"] == controller
            and run["head_branch"] == "main" and run["event"] == "workflow_dispatch"
            and run["path"] == WORKFLOW, "qualification run identity differs")
    require(run["repository"]["id"] == REPOSITORY_ID and run["repository"]["full_name"] == REPOSITORY,
            "qualification repository differs")
    require(run["status"] in ("in_progress", "completed"), "qualification attempt has not started")
    selected = [job for job in jobs if job["name"] == job_name]
    require(len(selected) == 1, "expected exactly one producing job")
    job = selected[0]
    require(type(job["id"]) is int and job["id"] > 0 and job["run_id"] == run_id
            and job["run_attempt"] == attempt and job["head_sha"] == controller
            and job["status"] == "completed" and job["conclusion"] == "success",
            "producing job identity or result differs")
    started, completed = timestamp(job["started_at"]), timestamp(job["completed_at"])
    require(timestamp(run["run_started_at"]) <= started <= completed <= now,
            "producing job was not freshly executed in this attempt")
    selected = [artifact for artifact in artifacts if artifact["name"] == name]
    require(len(selected) == 1, "expected exactly one immutable artifact")
    artifact = selected[0]
    require(type(artifact["id"]) is int and artifact["id"] > 0 and artifact["expired"] is False
            and timestamp(artifact["expires_at"]) > now, "artifact is invalid or expired")
    require(started <= timestamp(artifact["created_at"]) <= completed,
            "artifact was not created during the producing job")
    source = artifact["workflow_run"]
    require(source["id"] == run_id and source["repository_id"] == REPOSITORY_ID
            and source["head_repository_id"] == REPOSITORY_ID and source["head_sha"] == controller
            and source["head_branch"] == "main", "artifact source identity differs")
    require(type(artifact["size_in_bytes"]) is int and 0 < artifact["size_in_bytes"] <= MAX_BYTES
            and type(artifact["digest"]) is str and re.fullmatch(r"sha256:[0-9a-f]{64}", artifact["digest"]),
            "artifact size or API digest is invalid")
    return job, artifact


def file_digest(path):
    value = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return "sha256:" + value.hexdigest()


def unpack(archive, artifact, allowed, output):
    require(Path(archive).stat().st_size == artifact["size_in_bytes"] and file_digest(archive) == artifact["digest"],
            "artifact archive bytes differ from the immutable API digest")
    with zipfile.ZipFile(archive) as bundle:
        entries = bundle.infolist()
        require(len(entries) == len(allowed) and {entry.filename for entry in entries} == set(allowed),
                "artifact contains missing, duplicate or unexpected members")
        require(sum(entry.file_size for entry in entries) <= MAX_BYTES, "expanded artifact exceeds size limit")
        for entry in entries:
            require(entry.filename == Path(entry.filename).name and entry.filename not in (".", "..")
                    and not entry.flag_bits & 1 and entry.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED),
                    "artifact member is not a supported plain unencrypted file")
            mode = (entry.external_attr >> 16) & 0o170000
            require(entry.create_system != 3 or mode in (0, 0o100000), "artifact contains a non-regular file")
        for entry in entries:
            with bundle.open(entry) as source, (output / entry.filename).open("xb") as target:
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    target.write(block)


def download(context, name, job_name, allowed, output):
    """Select origins from GitHub, never from a caller-authored success receipt."""
    run_id, attempt = context["runID"], context["attempt"]
    run = api(f"actions/runs/{run_id}/attempts/{attempt}")
    jobs = [job for page in api(f"actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=20", True)
            for job in page["jobs"]]
    artifacts = [artifact for page in api(f"actions/runs/{run_id}/artifacts?per_page=20", True)
                 for artifact in page["artifacts"]]
    job, artifact = origin(run, jobs, artifacts, context, name, job_name, datetime.datetime.now(datetime.timezone.utc))
    output.mkdir(parents=True, exist_ok=False)
    # Retain origin metadata before downloading or parsing untrusted artifact data.
    (output / "origin.json").write_text(json.dumps({"run": run, "job": job, "artifact": artifact}, indent=2) + "\n")
    archive = output / "artifact.zip"
    with archive.open("xb") as stream:
        result = subprocess.run(["gh", "api", "--hostname", "github.com",
                                 f"repos/{REPOSITORY}/actions/artifacts/{artifact['id']}/zip"],
                                stdout=stream, stderr=subprocess.PIPE, timeout=300)
    require(result.returncode == 0, "artifact download failed")
    unpack(archive, artifact, allowed, output)
    return {"jobID": job["id"], "artifactID": artifact["id"], "archiveDigest": artifact["digest"],
            "runID": run_id, "attempt": attempt, "name": name}
