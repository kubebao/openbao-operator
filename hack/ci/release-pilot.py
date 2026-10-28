#!/usr/bin/env python3
"""Compare candidate and squash-merge artifacts without authorizing publication."""

import argparse
import copy
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parents[2]
IMAGES = {"operator": "MANAGER", "init": "CONFIG_INIT", "backup": "BACKUP_EXECUTOR", "upgrade": "UPGRADE_EXECUTOR"}
PLATFORMS = {"linux/amd64", "linux/arm64"}
CLAIM = "artifact-equivalence-only"


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def fingerprint(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def file_hash(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def git(directory, *args):
    return subprocess.check_output(["git", "-C", str(directory), *args], text=True).strip()


def require_sha(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise ValueError("source commits must be full lowercase Git SHAs")
    return value


def chart_version(text):
    values = []
    for key in ("version", "appVersion"):
        matches = re.findall(rf'^{key}:\s*[\"\']?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)[\"\']?\s*$', text, re.MULTILINE)
        if len(matches) != 1:
            raise ValueError(f"Chart.yaml must declare one SemVer {key}")
        values.append(matches[0])
    if values[0] != values[1]:
        raise ValueError("chart version and appVersion differ")
    return values[0]


def prepare(directory, candidate, merged, context):
    candidate, merged = require_sha(candidate), require_sha(merged)
    if candidate == merged:
        raise ValueError("pilot requires distinct candidate and merged commits")
    for sha in (candidate, merged):
        if git(directory, "rev-parse", f"{sha}^{{commit}}") != sha:
            raise ValueError("source must identify a commit")
    subprocess.run(["git", "-C", str(directory), "merge-base", "--is-ancestor", merged, "refs/remotes/origin/main"], check=True)
    parents = git(directory, "show", "-s", "--format=%P", merged).split()
    if len(parents) != 1:
        raise ValueError("merged commit must have one parent")
    subprocess.run(["git", "-C", str(directory), "merge-base", "--is-ancestor", parents[0], candidate], check=True)
    trees = {label: git(directory, "rev-parse", f"{sha}^{{tree}}") for label, sha in (("candidate", candidate), ("merged", merged))}
    if trees["candidate"] != trees["merged"]:
        raise ValueError("candidate and merged source trees differ")
    version = chart_version(git(directory, "show", f"{candidate}:charts/openbao-operator/Chart.yaml"))
    epoch = int(git(directory, "show", "-s", "--format=%ct", candidate))
    if epoch <= 0:
        raise ValueError("candidate timestamp must be positive")
    inputs = json.loads((ROOT / "hack/ci/release-pilot-inputs.json").read_text())
    helm = re.findall(r"^HELM_VERSION=(v[0-9]+\.[0-9]+\.[0-9]+)$", git(directory, "show", f"{candidate}:hack/dev/tool-versions.env"), re.MULTILINE)
    if len(helm) != 1:
        raise ValueError("source must pin one Helm version")
    return {"schema": 1, "claim": CLAIM, "context": context, "sources": {"candidate": candidate, "merged": merged},
            "reviewedBase": parents[0], "tree": trees["candidate"], "version": version,
            "sourceDateEpoch": epoch, "platforms": sorted(PLATFORMS), "helmVersion": helm[0], "inputs": inputs}


def load_plan(path):
    plan = json.loads(Path(path).read_text())
    if plan.get("schema") != 1 or plan.get("claim") != CLAIM or set(plan.get("sources", {})) != {"candidate", "merged"}:
        raise ValueError("unsupported pilot plan")
    for sha in [*plan["sources"].values(), plan["reviewedBase"]]:
        require_sha(sha)
    if set(plan["platforms"]) != PLATFORMS or not isinstance(plan["sourceDateEpoch"], int) or plan["sourceDateEpoch"] <= 0:
        raise ValueError("invalid frozen build inputs")
    chart_version(f'version: {plan["version"]}\nappVersion: {plan["version"]}\n')
    if plan["inputs"] != json.loads((ROOT / "hack/ci/release-pilot-inputs.json").read_text()):
        raise ValueError("pilot tool inputs changed after preparation")
    if os.environ.get("GITHUB_ACTIONS") == "true" and plan["context"] != execution_context():
        raise ValueError("plan belongs to a different workflow run or controller")
    return plan


def execution_context():
    return {"repository": os.environ.get("GITHUB_REPOSITORY", "kubebao/openbao-operator"),
            "workflowSHA": os.environ.get("GITHUB_WORKFLOW_SHA", git(ROOT, "rev-parse", "HEAD")),
            "runID": os.environ.get("GITHUB_RUN_ID", "local"), "attempt": os.environ.get("GITHUB_RUN_ATTEMPT", "1"),
            "controllerHasTrackedChanges": bool(git(ROOT, "status", "--porcelain", "--untracked-files=no")),
            "controllerFiles": {name: file_hash(ROOT / name) for name in [
                ".github/workflows/release-artifact-pilot.yml", "hack/ci/release-pilot.py", "hack/ci/release-pilot-inputs.json",
                "hack/ci/package-release-pilot.sh", "hack/ci/verify-byte-reproducibility.sh",
                "hack/ci/prepare-release-chart.sh", "hack/ci/normalize-spdx-json.sh"]}}


def verify_source(plan, label, directory):
    if git(directory, "rev-parse", "HEAD") != plan["sources"][label] or git(directory, "rev-parse", "HEAD^{tree}") != plan["tree"]:
        raise ValueError("checkout does not match frozen source identity")
    if git(directory, "status", "--porcelain", "--untracked-files=no"):
        raise ValueError("source has tracked changes")


def inspect_image(archive):
    """Verify the OCI root, both platforms, configs, and every referenced layer."""
    with tarfile.open(archive) as bundle:
        files = {member.name: member for member in bundle.getmembers() if member.isfile()}
        if len(files) != sum(member.isfile() for member in bundle.getmembers()):
            raise ValueError("duplicate OCI archive members")

        def blob(descriptor):
            digest = descriptor["digest"]
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                raise ValueError("invalid OCI digest")
            stream = bundle.extractfile(files["blobs/" + digest.replace(":", "/")])
            data = stream.read()
            if len(data) != descriptor["size"] or "sha256:" + hashlib.sha256(data).hexdigest() != digest:
                raise ValueError("corrupt OCI blob")
            return data

        layout = json.load(bundle.extractfile(files["index.json"]))
        if len(layout["manifests"]) != 1:
            raise ValueError("OCI archive must contain one image index")
        root = layout["manifests"][0]
        index = json.loads(blob(root))
        platforms = {}
        for descriptor in index["manifests"]:
            platform = descriptor["platform"]
            key = platform["os"] + "/" + platform["architecture"]
            if key in platforms or key not in PLATFORMS:
                raise ValueError("duplicate or unexpected image platform")
            manifest = json.loads(blob(descriptor))
            config = json.loads(blob(manifest["config"]))
            if config["architecture"] != platform["architecture"] or config["os"] != platform["os"]:
                raise ValueError("image config platform mismatch")
            for layer in manifest["layers"]:
                blob(layer)
            platforms[key] = {"manifest": descriptor["digest"], "config": manifest["config"]["digest"],
                              "layers": [layer["digest"] for layer in manifest["layers"]], "created": config["created"]}
        if set(platforms) != PLATFORMS:
            raise ValueError("missing image platform")
        return {"digest": root["digest"], "platforms": platforms, "archiveSHA256": file_hash(archive)}


def identity(plan, label):
    return {"planSHA256": fingerprint(plan), "source": label, "sourceSHA": plan["sources"][label],
            "tree": plan["tree"], "version": plan["version"], "sourceDateEpoch": plan["sourceDateEpoch"], "claim": CLAIM}


def image_receipt(plan, label, image, archive):
    result = inspect_image(archive)
    created = datetime.datetime.fromtimestamp(plan["sourceDateEpoch"], datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    if any(platform["created"] != created for platform in result["platforms"].values()):
        raise ValueError("image timestamp differs from frozen build input")
    return {**identity(plan, label), "image": image, **result}


def check_identity(plan, label, receipt):
    if any(receipt.get(key) != value for key, value in identity(plan, label).items()):
        raise ValueError("artifact identity differs from frozen plan")


def verify_images(plan, label, directory):
    results = {}
    for image in IMAGES:
        receipt = json.loads((directory / f"{image}.json").read_text())
        check_identity(plan, label, receipt)
        actual = image_receipt(plan, label, image, directory / f"{image}.tar")
        if receipt != actual:
            raise ValueError(f"{image} receipt does not describe the OCI bytes")
        results[image] = actual["digest"]
    return results


def release_files(plan):
    return ["install.yaml", "crds.yaml", "operator-policy-approval.hcl", "operator-policy-approval-with-backup.hcl",
            "checksums.txt", f'openbao-operator-{plan["version"]}.tgz', *[f"sbom-openbao-{image}.spdx.json" for image in IMAGES]]


def record_files(plan, label, images, directory):
    return {**identity(plan, label), "imageDigests": verify_images(plan, label, images),
            "files": {name: file_hash(directory / name) for name in release_files(plan)}}


def verify_files(plan, label, images, directory):
    receipt = json.loads((directory / "files.json").read_text())
    check_identity(plan, label, receipt)
    if receipt != record_files(plan, label, images, directory):
        raise ValueError("release file receipt does not describe the artifact bytes")
    return receipt


def compare(plan, candidate_images, merged_images, candidate_files, merged_files, output):
    output.mkdir(parents=True, exist_ok=True)
    report = {"claim": CLAIM, "planSHA256": fingerprint(plan), "passed": False, "publicationAuthorized": False}
    try:
        receipts = {"candidate": verify_files(plan, "candidate", candidate_images, candidate_files),
                    "merged": verify_files(plan, "merged", merged_images, merged_files)}
        env = {**os.environ, "PRIMARY_DIR": str(candidate_files), "REBUILD_DIR": str(merged_files),
               "REPRO_REQUIRED_FILES": " ".join(release_files(plan)), "REPRO_OPTIONAL_FILES": ""}
        for image, key in IMAGES.items():
            env[key + "_DIGEST"] = receipts["candidate"]["imageDigests"][image]
            env[key + "_REBUILD_DIGEST"] = receipts["merged"]["imageDigests"][image]

        def byte_gate(name, changes, expected):
            result = subprocess.run(["bash", str(ROOT / "hack/ci/verify-byte-reproducibility.sh")], env={**env, **changes},
                                    text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            (output / f"{name}.log").write_text(result.stdout)
            if (result.returncode == 0) != expected:
                raise ValueError(f"unexpected byte gate result: {name}")
            return {"name": name, "expectedPass": expected, "exitCode": result.returncode}

        report["byteGate"] = [byte_gate("unchanged", {}, True)]
        with tempfile.TemporaryDirectory() as temporary:
            changed = Path(temporary) / "changed"
            shutil.copytree(merged_files, changed)
            with (changed / "install.yaml").open("ab") as stream:
                stream.write(b"\n# pilot negative control\n")
            report["byteGate"].append(byte_gate("changed-file", {"REBUILD_DIR": str(changed)}, False))
            report["byteGate"].append(byte_gate("changed-image", {"MANAGER_REBUILD_DIGEST": "sha256:" + "0" * 64}, False))
            shutil.copyfile(merged_files / "install.yaml", changed / "install.yaml")
            (changed / "checksums.txt").unlink()
            report["byteGate"].append(byte_gate("missing-file", {"REBUILD_DIR": str(changed)}, False))
        report["identityRejections"] = []
        for key, value in {"planSHA256": "0" * 64, "sourceSHA": "0" * 40, "source": "merged",
                           "tree": "0" * 40, "version": "0.0.0", "sourceDateEpoch": 0}.items():
            changed = copy.deepcopy(receipts["candidate"])
            changed[key] = value
            try:
                check_identity(plan, "candidate", changed)
            except ValueError:
                report["identityRejections"].append(key)
            else:
                raise ValueError(f"identity negative control accepted: {key}")
        report.update(passed=True, images=receipts["merged"]["imageDigests"], files=receipts["merged"]["files"])
    except (ValueError, KeyError, OSError, tarfile.TarError) as error:
        report["error"] = str(error)
        raise
    finally:
        write_json(output / "comparison.json", report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["validate-refs", "prepare", "source", "image", "files", "compare"])
    parser.add_argument("--candidate", default="")
    parser.add_argument("--merged", default="")
    parser.add_argument("--plan", type=Path)
    parser.add_argument("--source", choices=["candidate", "merged"])
    parser.add_argument("--source-dir", type=Path, default=ROOT)
    parser.add_argument("--image", choices=IMAGES)
    parser.add_argument("--archive", type=Path)
    parser.add_argument("--images", type=Path)
    parser.add_argument("--files", type=Path)
    parser.add_argument("--candidate-images", type=Path)
    parser.add_argument("--merged-images", type=Path)
    parser.add_argument("--candidate-files", type=Path)
    parser.add_argument("--merged-files", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.command == "validate-refs":
        require_sha(args.candidate)
        require_sha(args.merged)
        return
    if args.command == "prepare":
        plan = prepare(args.source_dir, args.candidate, args.merged, execution_context())
        write_json(args.output, plan)
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
                values = {**plan["sources"], "version": plan["version"], "epoch": plan["sourceDateEpoch"],
                          "helmVersion": plan["helmVersion"], **plan["inputs"]}
                for key, value in values.items():
                    stream.write(f"{key}={value}\n")
        return
    plan = load_plan(args.plan)
    if args.command == "source":
        verify_source(plan, args.source, args.source_dir)
    elif args.command == "image":
        write_json(args.output, image_receipt(plan, args.source, args.image, args.archive))
    elif args.command == "files":
        write_json(args.output, record_files(plan, args.source, args.images, args.files))
    else:
        compare(plan, args.candidate_images, args.merged_images, args.candidate_files, args.merged_files, args.output)


if __name__ == "__main__":
    main()
