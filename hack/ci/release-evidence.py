#!/usr/bin/env python3
"""Check declared release evidence offline without authenticating or authorizing it."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


SCHEMA = 1
CLAIM = "release-evidence-structure-only"
REPOSITORY = "kubebao/openbao-operator"
ROLES = {"qualification", "finalBuild", "comparison"}
IMAGES = {"operator", "init", "backup", "upgrade"}
ROOT_FIELDS = {"repository", "sources", "release", "inputsSHA256", "testPlanSHA256", "images", "files"}
PRODUCER_FIELDS = {"repository", "workflow", "sourceSHA", "signerSHA", "runID", "jobID", "attempt"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def keys(value, expected, name):
    require(type(value) is dict and set(value) == expected, f"{name}: unexpected or missing fields")


def pattern(value, expression, name):
    require(isinstance(value, str) and re.fullmatch(expression, value) is not None, f"{name}: invalid format")


def sha(value, name):
    pattern(value, r"[0-9a-f]{40}", name)


def digest(value, name):
    pattern(value, r"sha256:[0-9a-f]{64}", name)


def positive(value, name):
    require(type(value) is int and value > 0, f"{name}: expected a positive integer")


def file_hash(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def fingerprint(identity):
    return hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def load_json(path):
    with Path(path).open("rb") as stream:
        data = stream.read(1024 * 1024 + 1)
    require(len(data) <= 1024 * 1024, "JSON document exceeds one MiB")
    return json.loads(data, object_pairs_hook=no_duplicates)


def git(repo, *args):
    result = subprocess.run(["git", "-C", str(repo), *args], text=True, capture_output=True,
                            env={**os.environ, "GIT_NO_REPLACE_OBJECTS": "1", "GIT_NO_LAZY_FETCH": "1",
                                 "GIT_TERMINAL_PROMPT": "0"})
    require(result.returncode == 0, "Git identity or ancestry check failed")
    return result.stdout.strip()


def payload_names(version):
    return {"install.yaml", "crds.yaml", "operator-policy-approval.hcl",
            "operator-policy-approval-with-backup.hcl", "checksums.txt", f"openbao-operator-{version}.tgz",
            *{f"sbom-openbao-{image}.spdx.json" for image in IMAGES}}


def validate_chart(chart, expected_version):
    """Accept the project's plain root keys and literal version values, not general YAML."""
    declarations = {}
    active_key = None
    for line in chart.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line[0].isspace():
            require(active_key not in {"version", "appVersion"}, "release: multiline chart version")
            continue
        match = re.fullmatch(r"([A-Za-z][A-Za-z0-9_-]*):(?:[ \t].*)?", line)
        require(match is not None, "release: unsupported chart root syntax")
        active_key = match[1]
        require(active_key not in declarations, "release: duplicate chart root key")
        declarations[active_key] = line
    version = re.escape(expected_version)
    for field in ("version", "appVersion"):
        expression = rf"{field}:[ \t]*(?:\"{version}\"|'{version}'|{version})[ \t]*"
        require(field in declarations and re.fullmatch(expression, declarations[field]) is not None,
                "release: chart version differs")


def validate_identity(identity, repo, notes, inputs, plan, payloads):
    keys(identity, ROOT_FIELDS, "identity")
    require(identity["repository"] == REPOSITORY, "identity: repository differs")
    sources = identity["sources"]
    keys(sources, {"candidateSHA", "finalSHA", "tree", "baseSHA", "sourceDateEpoch"}, "sources")
    for field in ("candidateSHA", "finalSHA", "tree", "baseSHA"):
        sha(sources[field], field)
    positive(sources["sourceDateEpoch"], "sourceDateEpoch")
    candidate, final, base = (sources[key] for key in ("candidateSHA", "finalSHA", "baseSHA"))
    require(candidate != final, "sources: candidate and final commit must differ")
    for commit in (candidate, final):
        require(git(repo, "rev-parse", f"{commit}^{{commit}}") == commit, "sources: expected a commit")
        require(git(repo, "rev-parse", f"{commit}^{{tree}}") == sources["tree"], "sources: tree differs")
    require(git(repo, "show", "-s", "--format=%P", final) == base, "sources: final parent differs")
    git(repo, "merge-base", "--is-ancestor", base, candidate)
    git(repo, "merge-base", "--is-ancestor", final, "refs/remotes/origin/main")
    require(int(git(repo, "show", "-s", "--format=%ct", candidate)) == sources["sourceDateEpoch"],
            "sources: candidate epoch differs")

    release = identity["release"]
    keys(release, {"version", "bodySHA256"}, "release")
    pattern(release["version"], r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", "version")
    chart = git(repo, "show", f"{candidate}:charts/openbao-operator/Chart.yaml")
    validate_chart(chart, release["version"])
    versions = json.loads(git(repo, "show", f"{candidate}:.release-please-manifest.json"),
                          object_pairs_hook=no_duplicates)
    require(type(versions) is dict and versions.get(".") == release["version"],
            "release: manifest version differs")
    for value, label, path in [(release["bodySHA256"], "bodySHA256", notes),
                               (identity["inputsSHA256"], "inputsSHA256", inputs),
                               (identity["testPlanSHA256"], "testPlanSHA256", plan)]:
        pattern(value, r"[0-9a-f]{64}", label)
        require(file_hash(path) == value, f"{label}: bytes differ")
    Path(notes).read_bytes().decode("utf-8")  # Hash the exact body without normalizing it.
    keys(identity["images"], IMAGES, "images")
    for value in identity["images"].values():
        digest(value, "image digest")
    keys(identity["files"], payload_names(release["version"]), "files")
    for name, expected in identity["files"].items():
        pattern(expected, r"[0-9a-f]{64}", "payload hash")
        require(file_hash(Path(payloads) / name) == expected, "payload: bytes differ")


def validate_producer(producer):
    keys(producer, PRODUCER_FIELDS, "producer")
    require(producer["repository"] == REPOSITORY, "producer: repository differs")
    pattern(producer["workflow"], r"\.github/workflows/[a-z0-9-]+\.yml", "workflow")
    sha(producer["sourceSHA"], "producer sourceSHA")
    sha(producer["signerSHA"], "producer signerSHA")
    for key in ("runID", "jobID", "attempt"):
        positive(producer[key], key)


def validate_artifact_references(artifacts, now):
    require(type(artifacts) is list and artifacts, "record: missing artifact references")
    for artifact in artifacts:
        keys(artifact, {"id", "digest", "expiresAt"}, "artifact")
        positive(artifact["id"], "artifact ID")
        digest(artifact["digest"], "artifact digest")
        pattern(artifact["expiresAt"], r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z",
                "artifact expiresAt")
        expiry = datetime.datetime.fromisoformat(artifact["expiresAt"].replace("Z", "+00:00"))
        require(expiry > now, "artifact: expired at assessment time")


def validate(manifest, policy, *, repo, notes, inputs, plan, payloads, archives, now=None):
    """Check consistency against caller-selected policy, not an authenticated trust root."""
    now = now or datetime.datetime.now(datetime.timezone.utc)
    require(now.tzinfo is not None, "assessment time must include a timezone")
    keys(manifest, {"schema", "claim", "identity", "records"}, "manifest")
    keys(policy, {"schema", "identitySHA256", "records"}, "policy")
    require(type(manifest["schema"]) is int and manifest["schema"] == SCHEMA, "unsupported manifest schema")
    require(type(policy["schema"]) is int and policy["schema"] == SCHEMA, "unsupported policy schema")
    require(manifest["claim"] == CLAIM, "unsupported evidence claim")
    identity = manifest["identity"]
    validate_identity(identity, repo, notes, inputs, plan, payloads)
    identity_hash = fingerprint(identity)
    pattern(policy["identitySHA256"], r"[0-9a-f]{64}", "policy identitySHA256")
    require(identity_hash == policy["identitySHA256"], "identity: differs from selected policy")
    keys(manifest["records"], ROLES, "records")
    keys(policy["records"], ROLES, "policy records")
    artifact_ids = set()
    for role, record in manifest["records"].items():
        keys(record, {"identitySHA256", "sourceSHA", "producer", "artifacts"}, "record")
        require(record["identitySHA256"] == identity_hash, "record: identity differs")
        expected_source = identity["sources"]["candidateSHA" if role == "qualification" else "finalSHA"]
        require(record["sourceSHA"] == expected_source, "record: subject source differs")
        validate_producer(record["producer"])
        selection = policy["records"][role]
        keys(selection, {"producer", "artifacts"}, "policy record")
        validate_producer(selection["producer"])
        require(record["producer"] == selection["producer"], "producer: differs from selected policy")
        validate_artifact_references(record["artifacts"], now)
        validate_artifact_references(selection["artifacts"], now)
        require(record["artifacts"] == selection["artifacts"], "artifacts: differ from selected policy")
        if role == "finalBuild":
            require(record["producer"]["sourceSHA"] == expected_source,
                    "final build: declared event source differs from final commit")
        for artifact in record["artifacts"]:
            require(artifact["id"] not in artifact_ids, "artifact ID appears more than once")
            artifact_ids.add(artifact["id"])
            require("sha256:" + file_hash(Path(archives) / f'{artifact["id"]}.zip') == artifact["digest"],
                    "artifact: archive bytes differ")
    return {"schema": SCHEMA, "claim": CLAIM, "consistent": True, "identitySHA256": identity_hash,
            "assessedAt": now.astimezone(datetime.timezone.utc).isoformat(),
            "declaredArtifactCount": len(artifact_ids), "authenticationPerformed": False,
            "runtimeQualificationVerified": False, "publicationAuthorized": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("manifest", "policy", "repo", "notes", "inputs", "plan", "payloads", "archives", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        parser.error("output already exists; select a new report path")
    report = {"schema": SCHEMA, "claim": CLAIM, "consistent": False,
              "authenticationPerformed": False, "runtimeQualificationVerified": False,
              "publicationAuthorized": False}
    result = 1
    try:
        report = validate(load_json(args.manifest), load_json(args.policy),
                          repo=args.repo, notes=args.notes, inputs=args.inputs, plan=args.plan,
                          payloads=args.payloads, archives=args.archives)
        result = 0
    except RecursionError:
        report["error"] = "JSON nesting exceeds supported depth"
    except (ValueError, OSError) as error:
        report["error"] = str(error)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("x", encoding="utf-8") as stream:
        stream.write(json.dumps(report, indent=2, sort_keys=True) + "\n")
    return result


if __name__ == "__main__":
    sys.exit(main())
