#!/usr/bin/env python3
"""Produce and authenticate a metadata-only GitHub Actions identity probe."""

import argparse
import copy
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import zipfile


REPOSITORY = "kubebao/openbao-operator"
REPOSITORY_ID = 1127385211
OWNER_ID = 339230794
ROOT = f"https://github.com/{REPOSITORY}"
PRODUCER = ".github/workflows/release-evidence-identity-producer.yml"
CONSUMER = ".github/workflows/release-evidence-identity-consumer.yml"
REF = "refs/heads/main"
JOB = "Sign Identity Probe"
CLAIM = "release-identity-probe-only"
LIMIT = 2 * 1024 * 1024


def require(condition, message):
    if not condition:
        raise ValueError(message)


def number(value):
    require(isinstance(value, str) and re.fullmatch(r"[1-9][0-9]{0,19}", value), "invalid numeric identifier")
    return int(value)


def sha(value):
    require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value), "invalid controller SHA")
    return value


def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def decode(data):
    require(len(data) <= LIMIT, "JSON exceeds size limit")
    return json.loads(data, object_pairs_hook=pairs)


def write(path, value):
    with path.open("x") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")


def cli(*args):
    # Never forward API bodies, tokens, redirects or command stderr to the report.
    result = subprocess.run(["gh", *args], capture_output=True, timeout=120)
    require(result.returncode == 0, "GitHub CLI request or verification failed")
    return result.stdout


def api(path, paginated=False):
    args = ["api", "--hostname", "github.com", f"repos/{REPOSITORY}/{path}"]
    if paginated:
        args.extend(["--paginate", "--slurp"])
    return decode(cli(*args))


def context(expected, workflow):
    sha(expected)
    required = {
        "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com",
        "GITHUB_REPOSITORY": REPOSITORY, "GITHUB_REPOSITORY_ID": str(REPOSITORY_ID),
        "GITHUB_REPOSITORY_OWNER_ID": str(OWNER_ID), "GITHUB_REF": REF,
        "GITHUB_SHA": expected, "GITHUB_WORKFLOW_SHA": expected,
        "GITHUB_WORKFLOW_REF": f"{REPOSITORY}/{workflow}@{REF}",
        "GITHUB_EVENT_NAME": "workflow_dispatch", "RUNNER_ENVIRONMENT": "github-hosted",
    }
    require(all(os.environ.get(key) == value for key, value in required.items()), "unapproved workflow context")


def jobs(run, attempt):
    pages = api(f"actions/runs/{run}/attempts/{attempt}/jobs?per_page=20", paginated=True)
    return [job for page in pages for job in page["jobs"]]


def selected_job(records, run, attempt, controller, completed):
    require(len(records) == 1, "expected exactly one producer job")
    job = records[0]
    require(type(job["id"]) is int and job["id"] > 0, "invalid job ID")
    require(job["name"] == JOB and job["run_id"] == run and job["run_attempt"] == attempt
            and job["head_sha"] == controller, "producer job identity differs")
    if completed:
        require(job["status"] == "completed" and job["conclusion"] == "success", "producer job did not succeed")
    else:
        require(job["status"] == "in_progress", "producer job is not running")
    return job["id"]


def probe(controller, run, attempt, job):
    return {"schemaVersion": 1, "claim": CLAIM, "repository": REPOSITORY, "repositoryID": REPOSITORY_ID,
            "controllerSHA": controller, "workflow": PRODUCER, "ref": REF,
            "runID": run, "attempt": attempt, "jobID": job,
            "runtimeQualificationVerified": False, "publicationAuthorized": False}


def produce(args):
    context(args.controller, PRODUCER)
    require(os.environ.get("GITHUB_JOB") == "produce", "unexpected producer job key")
    run, attempt = number(os.environ["GITHUB_RUN_ID"]), number(os.environ["GITHUB_RUN_ATTEMPT"])
    job = selected_job(jobs(run, attempt), run, attempt, args.controller, False)
    args.output.mkdir(parents=True, exist_ok=False)
    write(args.output / "probe.json", probe(args.controller, run, attempt, job))


def origin(run, artifact, job, controller, run_id, attempt, artifact_id, now):
    require(run["repository"]["id"] == REPOSITORY_ID and run["repository"]["full_name"] == REPOSITORY
            and run["repository"]["owner"]["id"] == OWNER_ID, "producer repository differs")
    require(run["id"] == run_id and run["run_attempt"] == attempt and run["head_sha"] == controller
            and run["head_branch"] == "main" and run["event"] == "workflow_dispatch"
            and run["path"] == PRODUCER, "producer run identity differs")
    require(run["status"] == "completed" and run["conclusion"] == "success", "producer run did not succeed")
    job_id = selected_job(job, run_id, attempt, controller, True)
    require(artifact["id"] == artifact_id and type(artifact["id"]) is int
            and artifact["name"] == f"release-identity-{run_id}-{attempt}", "artifact selection differs")
    require(artifact["expired"] is False and datetime.datetime.fromisoformat(
        artifact["expires_at"].replace("Z", "+00:00")) > now, "artifact expired")
    source = artifact["workflow_run"]
    require(source["id"] == run_id and source["repository_id"] == REPOSITORY_ID
            and source["head_repository_id"] == REPOSITORY_ID and source["head_sha"] == controller
            and source["head_branch"] == "main", "artifact origin differs")
    require(type(artifact["size_in_bytes"]) is int and 0 < artifact["size_in_bytes"] <= LIMIT,
            "artifact exceeds size limit")
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", artifact["digest"] or ""), "missing artifact SHA256")
    return job_id


def unpack(data, artifact, output):
    require(len(data) == artifact["size_in_bytes"]
            and "sha256:" + hashlib.sha256(data).hexdigest() == artifact["digest"], "archive digest differs")
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        entries = archive.infolist()
        require(len(entries) == 2 and {x.filename for x in entries} == {"probe.json", "bundle.json"},
                "unexpected archive members")
        require(sum(x.file_size for x in entries) <= LIMIT, "expanded archive exceeds size limit")
        for entry in entries:
            require(entry.create_system != 3 or (entry.external_attr >> 16) & 0o170000 != 0o120000,
                    "archive contains a symbolic link")
            with (output / entry.filename).open("xb") as stream:
                stream.write(archive.read(entry))


def verify_command(subject, bundle, controller, trusted_root, **overrides):
    policy = {"cert-identity": f"{ROOT}/{PRODUCER}@{REF}",
              "cert-oidc-issuer": "https://token.actions.githubusercontent.com",
              "source-ref": REF, "source-digest": controller, "signer-digest": controller}
    policy.update(overrides)
    return ["attestation", "verify", str(subject), "--hostname", "github.com", "--bundle", str(bundle),
            "--custom-trusted-root", str(trusted_root), "--repo", REPOSITORY,
            "--deny-self-hosted-runners", "--format", "json",
            *[part for key, value in policy.items() for part in ("--" + key, value)]]


def verified_identity(verified, controller, run, attempt, subject):
    require(type(verified) is list and len(verified) == 1, "expected exactly one verified attestation")
    result = verified[0]["verificationResult"]
    # Pinned sigstore-go certificate.Summary embeds and flattens Extensions.
    extensions = result["signature"]["certificate"]
    expected = {
        "issuer": "https://token.actions.githubusercontent.com", "runnerEnvironment": "github-hosted",
        "sourceRepositoryURI": ROOT, "sourceRepositoryIdentifier": str(REPOSITORY_ID),
        "sourceRepositoryOwnerIdentifier": str(OWNER_ID), "sourceRepositoryDigest": controller,
        "sourceRepositoryRef": REF, "buildSignerURI": f"{ROOT}/{PRODUCER}@{REF}",
        "buildSignerDigest": controller, "buildConfigURI": f"{ROOT}/{PRODUCER}@{REF}",
        "buildConfigDigest": controller, "buildTrigger": "workflow_dispatch",
        "runInvocationURI": f"{ROOT}/actions/runs/{run}/attempts/{attempt}",
    }
    require(all(extensions.get(key) == value for key, value in expected.items()), "certificate context differs")
    statement = result["statement"]
    require(statement["predicateType"] == "https://slsa.dev/provenance/v1"
            and statement["subject"] == [{"name": "probe.json", "digest": {"sha256": subject}}],
            "signed subject differs")
    require(result["verifiedTimestamps"], "no verified signing timestamp")


def rejection(check):
    try:
        check()
    except ValueError:
        return
    raise ValueError("negative control unexpectedly accepted")


def consume(args):
    context(args.controller, CONSUMER)
    run_id, attempt, artifact_id = number(args.run), number(args.attempt), number(args.artifact)
    args.output.mkdir(parents=True, exist_ok=False)
    report = {"claim": CLAIM, "identityVerified": False, "authenticationPerformed": False,
              "runtimeQualificationVerified": False, "publicationAuthorized": False,
              "controllerSHA": args.controller, "runID": run_id, "attempt": attempt, "artifactID": artifact_id,
              "phase": "origin"}
    try:
        run = api(f"actions/runs/{run_id}/attempts/{attempt}")
        artifact = api(f"actions/artifacts/{artifact_id}")
        records = jobs(run_id, attempt)
        write(args.output / "origin.json", {"run": run, "artifact": artifact, "jobs": records})
        now = datetime.datetime.now(datetime.timezone.utc)
        job = origin(run, artifact, records, args.controller, run_id, attempt, artifact_id, now)
        report["phase"] = "archive"
        data = cli("api", "--hostname", "github.com", f"repos/{REPOSITORY}/actions/artifacts/{artifact_id}/zip")
        with (args.output / "artifact.zip").open("xb") as stream:
            stream.write(data)
        unpack(data, artifact, args.output)
        subject, bundle = args.output / "probe.json", args.output / "bundle.json"
        expected = (json.dumps(probe(args.controller, run_id, attempt, job), indent=2, sort_keys=True) + "\n").encode()
        require(subject.read_bytes() == expected, "probe identity or exact encoding differs")
        report["phase"] = "signature"
        trusted_root = args.output / "trusted-root.jsonl"
        with trusted_root.open("xb") as stream:
            stream.write(cli("attestation", "trusted-root", "--hostname", "github.com"))
        command = verify_command(subject, bundle, args.controller, trusted_root)
        verified = decode(cli(*command))
        write(args.output / "verification.json", verified)
        subject_digest = hashlib.sha256(subject.read_bytes()).hexdigest()
        verified_identity(verified, args.controller, run_id, attempt, subject_digest)
        report["phase"] = "negative-controls"
        crypto_controls = {
            "wrongSource": {"source-digest": "0" * 40}, "wrongSigner": {"signer-digest": "0" * 40},
            "wrongWorkflow": {"cert-identity": f"{ROOT}/{CONSUMER}@{REF}"},
        }
        for changes in crypto_controls.values():
            rejection(lambda: cli(*verify_command(subject, bundle, args.controller, trusted_root, **changes)))
        changed = args.output / "changed-probe.json"
        with changed.open("xb") as stream:
            stream.write(subject.read_bytes() + b" ")
        rejection(lambda: cli(*verify_command(changed, bundle, args.controller, trusted_root)))
        for field, value in [("id", artifact_id + 1), ("expired", True), ("digest", None)]:
            altered = copy.deepcopy(artifact)
            altered[field] = value
            rejection(lambda: origin(run, altered, records, args.controller, run_id, attempt, artifact_id, now))
        rejection(lambda: unpack(data + b" ", artifact, args.output))
        rejection(lambda: origin(run, artifact, records, args.controller, run_id + 1, attempt, artifact_id, now))
        rejection(lambda: origin(run, artifact, records, args.controller, run_id, attempt + 1, artifact_id, now))
        rejection(lambda: verified_identity(verified, args.controller, run_id, attempt + 1, subject_digest))
        report.update({"identityVerified": True, "authenticationPerformed": True, "jobID": job,
                       "phase": "complete",
                       "subjectSHA256": subject_digest, "artifactSHA256": artifact["digest"],
                       "trustedRootSHA256": hashlib.sha256(trusted_root.read_bytes()).hexdigest(),
                       "cryptographicRejections": [*crypto_controls, "changedSubject"],
                       "metadataRejections": ["artifactID", "expired", "missingDigest", "archiveBytes",
                                              "run", "attempt", "certificateAttempt"],
                       "verificationTool": cli("--version").decode().splitlines()[0]})
    except (ValueError, KeyError, TypeError, OSError, RecursionError, subprocess.TimeoutExpired, zipfile.BadZipFile) as error:
        report["error"] = type(error).__name__
        # CLI stderr is never included: only local validation messages are safe report diagnostics.
        if type(error) is ValueError:
            report["reason"] = str(error)
    write(args.output / "report.json", report)
    require(report["identityVerified"], "identity pilot failed; see retained report")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("produce", "consume"):
        command = commands.add_parser(name)
        command.add_argument("--controller", required=True)
        command.add_argument("--output", type=Path, required=True)
        if name == "consume":
            for option in ("run", "attempt", "artifact"):
                command.add_argument("--" + option, required=True)
    args = parser.parse_args()
    try:
        (produce if args.command == "produce" else consume)(args)
    except (ValueError, KeyError, TypeError, OSError, RecursionError, subprocess.TimeoutExpired) as error:
        print(f"release identity pilot failed: {type(error).__name__}: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
