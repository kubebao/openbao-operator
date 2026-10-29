#!/usr/bin/env python3
"""Audit authenticated raw candidate evidence before signing a non-publishing qualification record."""

import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("qualification_runtime", ROOT / "hack/ci/qualification-runtime.py")
R = importlib.util.module_from_spec(spec)
spec.loader.exec_module(R)
A, P, O, require = R.A, R.P, R.O, R.require
CLAIM = "isolated-full-profile-candidate-qualification"
LIMITS = ["Experimental private helper trust does not qualify the default public helper policy.",
          "Runner image, vulnerability databases, remote Semgrep rule packs, network services and transitive fixture dependencies are not hermetically frozen. Available runner, scanner and before/after Pod observations are retained; they are not a complete dependency inventory.",
          "The OCI plugin fixture pins its plugin binary checksum and version; its OCI descriptor is not frozen.",
          "Only candidates with unchanged reviewed harness, gate configuration and test inputs are accepted.",
          "No production promotion, interruption recovery or release publication is authorized by this record."]


def read_report(path):
    require(path.stat().st_size <= 64 * 1024 * 1024, "runtime report exceeds 64 MiB")
    return json.loads(path.read_bytes(), object_pairs_hook=P.unique)


def no_retries(reports):
    require(type(reports) is list and len(reports) == 1, "expected one actual Ginkgo suite")
    report = reports[0]
    require(report["SuiteSucceeded"] is True and report["SuiteConfig"]["DryRun"] is False,
            "failed or verification-only suite does not qualify")
    require(report["SuiteConfig"].get("FlakeAttempts", 0) in (0, 1)
            and report["SuiteConfig"].get("Repeat", 0) == 0, "suite permits retries or repeated execution")
    for execution in report["SpecReports"]:
        if execution["State"] == "passed" and execution["LeafNodeType"] == "It":
            require(execution.get("NumAttempts") == 1, "runtime contains retried or unobserved passing execution")


def nodes(path, declared):
    data = read_report(path)
    require(type(data["items"]) is list and data["items"], "node observations are missing")
    versions = {item["status"]["nodeInfo"]["kubeletVersion"] for item in data["items"]}
    require(versions == {"v" + declared}, "actual kubelet version differs from planned Kubernetes version")
    return [{"name": item["metadata"]["name"], "uid": item["metadata"]["uid"],
             "kubeletVersion": item["status"]["nodeInfo"]["kubeletVersion"]} for item in data["items"]]


def result(plan, directory, stage):
    value = P.read_json(directory / "result.json")
    require(value["stage"] == stage and value["sources"] == plan["sources"]
            and value["execution"] == plan["context"] and value["planSHA256"] == P.fingerprint(plan),
            "stage result source or producing context differs")
    return value


def files(plan, output, origins):
    records = {}
    names = set(A.OCI.release_files(plan))
    for build in A.BUILDS:
        directory = output / (build + "-files")
        origins[build + "Files"] = O.download(plan["context"], plan["artifactPrefix"] + f"-{build}-files",
            f"Package ({build})", names | {"files.json"}, directory)
        record = P.read_json(directory / "files.json")
        require(record["sourceSHA"] == plan["sources"]["candidateSHA"] and record["build"] == build
                and record["execution"] == plan["context"] and record["planSHA256"] == P.fingerprint(plan),
                "packaged payload source or producing context differs")
        require(record["files"] == {name: O.file_digest(directory / name) for name in names},
                "packaged payload checksum differs from actual bytes")
        records[build] = record
    require(records["primary"]["files"] == records["rebuild"]["files"]
            and records["primary"]["imageDigests"] == records["rebuild"]["imageDigests"],
            "independent release payloads or image subjects differ")
    return records["primary"]


def attestation_envelopes(data):
    """Parse the bounded JSON-object stream emitted by pinned Cosign verification."""
    require(type(data) is bytes and 0 < len(data) <= 2 * 1024 * 1024,
            "Cosign attestation output is empty or exceeds two MiB")
    text = data.decode("utf-8")
    decoder = json.JSONDecoder(object_pairs_hook=P.unique)
    envelopes, offset = [], 0
    while offset < len(text):
        if text[offset] in " \t\r\n":
            offset += 1
            continue
        envelope, offset = decoder.raw_decode(text, offset)
        require(type(envelope) is dict and type(envelope.get("payload")) is str,
                "Cosign attestation output must contain envelope objects")
        require(len(envelopes) < 1024, "too many verified attestation envelopes")
        envelopes.append(envelope)
    require(envelopes, "Cosign attestation output has no envelopes")
    return envelopes


def image_provenance(plan, images, record, output):
    subject = "https://github.com/" + P.REPOSITORY + "/" + P.WORKFLOW + "@refs/heads/main"
    proofs = {}
    for role in A.ROLES:
        image = images[role]
        common = ["--certificate-identity", subject, "--certificate-oidc-issuer",
                  "https://token.actions.githubusercontent.com", "--certificate-github-workflow-sha",
                  plan["sources"]["controllerSHA"]]
        for kind in ("signature", "attestation"):
            command = ["cosign", "verify" if kind == "signature" else "verify-attestation"]
            if kind == "attestation":
                command += ["--type", "https://kubebao.org/attestations/candidate-image-import/v1"]
            data = subprocess.run([*command, *common, image], capture_output=True, timeout=180)
            require(data.returncode == 0 and len(data.stdout) <= 2 * 1024 * 1024,
                    "isolated image signature or provenance verification failed")
            (output / f"{role}-{kind}.json").write_bytes(data.stdout)
            if kind == "signature":
                signatures = json.loads(data.stdout, object_pairs_hook=P.unique)
                require(type(signatures) is list and any(
                    type(signature.get("critical")) is dict
                    and signature["critical"].get("type") == "https://sigstore.dev/cosign/sign/v1"
                    and type(signature["critical"].get("image")) is dict
                    and signature["critical"]["image"].get("docker-manifest-digest") == image.split("@")[1]
                    for signature in signatures if type(signature) is dict),
                    "verified generic image signature is missing or names a different digest")
            else:
                envelopes = attestation_envelopes(data.stdout)
                matches = []
                for envelope in envelopes:
                    statement = json.loads(base64.b64decode(envelope["payload"], validate=True), object_pairs_hook=P.unique)
                    predicate = statement["predicate"]
                    if predicate.get("planSHA256") == P.fingerprint(plan):
                        matches.append((statement, predicate))
                require(len(matches) == 1, "expected exactly one provenance statement for the selected plan")
                statement, predicate = matches[0]
                require(statement["predicateType"] == "https://kubebao.org/attestations/candidate-image-import/v1"
                        and len(statement["subject"]) == 1
                        and statement["subject"][0]["digest"] == {"sha256": image.split("@sha256:")[1]}
                        and predicate["subjectDigest"] == image.split("@")[1]
                        and predicate["sources"] == plan["sources"] and predicate["controller"] == plan["context"]
                        and predicate["producingArtifact"] == record["origins"][role]
                        and predicate["runtimeQualificationVerified"] is False
                        and predicate["publicationAuthorized"] is False, "verified image provenance content differs")
        proofs[role] = {"image": image, "signature": O.file_digest(output / f"{role}-signature.json"),
                       "provenance": O.file_digest(output / f"{role}-attestation.json")}
    return proofs


def audit(plan, output):
    require(os.environ.get("GITHUB_JOB") == "qualify", "unexpected qualification job")
    output.mkdir(parents=True, exist_ok=False)
    origins = {}
    origins["plan"] = O.download(plan["context"], plan["artifactPrefix"] + "-plan", "Plan Candidate",
                                 {"plan.json", "release-body.txt", "external-images.json"}, output / "plan")
    require(P.read_json(output / "plan/plan.json") == plan
            and O.file_digest(output / "plan/release-body.txt") == "sha256:" + plan["releaseBodySHA256"]
            and P.read_json(output / "plan/external-images.json") == plan["externalImages"], "original plan bytes differ")
    images, _ = R.imported(plan, output / "import")
    import_record = P.read_json(output / "import/import.json")
    origins["import"] = P.read_json(output / "import/origin.json")["artifact"]["id"]
    payloads = files(plan, output, origins)
    require(payloads["imageDigests"] == {role: images[role].split("@")[1] for role in A.ROLES},
            "runtime and packaged image subjects differ")
    provenance = image_provenance(plan, images, import_record, output)
    origins["compile"] = O.download(plan["context"], plan["artifactPrefix"] + "-binaries",
                                    "Compile Candidate Suite", R.BINARY_FILES, output / "binaries")
    binaries = R.verify_binaries(plan, output / "binaries")
    for stage, count, job in (("api", 3, "API Contract Gate"), ("source-security", 2, "Source Security Gate")):
        directory = output / stage
        origins[stage] = O.download(plan["context"], plan["artifactPrefix"] + "-" + stage, job,
            {"result.json", *[f"check-{n}.log" for n in range(count)]}, directory)
        verified = result(plan, directory, stage)
        require(verified["logs"] == {f"check-{n}.log": O.file_digest(directory / f"check-{n}.log")
                                     for n in range(count)}, "source/API check logs differ")
    scans = {}
    for role in A.ROLES:
        directory = output / (role + "-scan")
        origins[role + "Scan"] = O.download(plan["context"], plan["artifactPrefix"] + "-scan-" + role,
            f"Image Security ({role})", {"result.json", "trivy.json", "scanner.json"}, directory)
        verified = result(plan, directory, "image-security")
        require(verified["role"] == role and verified["image"] == images[role]
                and verified["reportSHA256"] == O.file_digest(directory / "trivy.json")
                and verified["scannerSHA256"] == O.file_digest(directory / "scanner.json")
                and verified["policy"] == {"severity": ["HIGH", "CRITICAL"], "ignoreUnfixed": True}, "image scan target or policy differs")
        scans[role] = {"scanner": read_report(directory / "scanner.json"), "result": verified}
        scan = read_report(directory / "trivy.json")
        require(scan["ArtifactName"] == images[role] and scan["ArtifactType"] == "container_image",
                "Trivy report describes another artifact")
        for scan_result in scan.get("Results", []):
            require(not any(v["Severity"] in ("HIGH", "CRITICAL") and v.get("FixedVersion")
                            for v in scan_result.get("Vulnerabilities", [])), "image vulnerability gate failed")
    rows = []
    reporter = Path(os.environ["RUNNER_TEMP"]) / "qualification-report"
    R.run(["go", "build", "-mod=vendor", "-o", str(reporter), "./hack/tools/e2e_report"])
    for row in plan["matrix"]["include"]:
        key = row["qualification_row_key"]
        directory = output / key
        origins[key] = O.download(plan["context"], plan["artifactPrefix"] + "-row-" + key,
            f"Runtime ({key})", {"runtime-inputs.json", "nodes.json", "ginkgo.json", "junit.xml", "runtime.log", "expected-cases.json", "observations.json"}, directory)
        inputs = P.read_json(directory / "runtime-inputs.json")
        require(inputs["row"] == row and inputs["images"] == images and inputs["binaries"] == binaries
                and inputs["sources"] == plan["sources"] and inputs["execution"] == plan["context"]
                and inputs["planSHA256"] == P.fingerprint(plan) and inputs["runtimePolicy"] == plan["runtimePolicy"],
                "runtime row, transport policy, image or executable identity differs")
        require(P.read_json(directory / "expected-cases.json") == row["expected_case_ids"], "row selection changed")
        no_retries(read_report(directory / "ginkgo.json"))
        observed = nodes(directory / "nodes.json", row["k8s"])
        R.run([str(reporter), "--json-report", str(directory / "ginkgo.json"), "--manifest", "test/e2e/suites.yaml",
               "--fail-on-selected-skips", "--expected-cases", str(directory / "expected-cases.json"),
               "--selector", row["label_filter"], "--json-out", str(directory / "audited-summary.json")])
        summary = P.read_json(directory / "audited-summary.json")
        require(summary["suiteSucceeded"] is True and not summary.get("selectionErrors")
                and sorted(summary["passedCaseIds"]) == sorted(row["expected_case_ids"]), "raw runtime coverage differs")
        observation = read_report(directory / "observations.json")
        rows.append({"key": key, "passedCaseIDs": summary["passedCaseIds"], "nodes": observed,
                     "observationsSHA256": O.file_digest(directory / "observations.json"),
                     "runner": observation["after"]["runner"],
                     "observedPodCounts": {phase: len(observation[phase]["pods"]) for phase in ("before", "after")}})
    directory = output / "upgrade"
    origins["upgrade"] = O.download(plan["context"], plan["artifactPrefix"] + "-upgrade", "Operator Upgrade",
        {"result.json", "runtime.log", "upgrade.json", "nodes.json", "images.json"}, directory)
    verified = result(plan, directory, "operator-upgrade")
    upgrade = P.read_json(directory / "upgrade.json")
    require(verified["images"] == images and verified["baseline"] == plan["upgradeBaseline"]
            and verified["targetVersion"] == plan["version"]
            and verified["logSHA256"] == O.file_digest(directory / "runtime.log"), "upgrade target or evidence differs")
    require(upgrade["fromVersion"] == plan["upgradeBaseline"]["version"]
            and upgrade["targetVersion"] == plan["version"] and upgrade["finalSuccess"] is True
            and all(upgrade[field] == "passed" for field in ("controllerJWTMigration", "legacyStaticRestoreGuardrail", "retainCustody")),
            "incomplete actual baseline-to-candidate upgrade")
    for field in ("hardenedUID", "developmentUID", "pvcUID", "rootSecretUID", "unsealSecretUID"):
        require(re.fullmatch(r"[0-9a-f-]{36}", upgrade[field]), "upgrade custody UID is missing")
    require(len(upgrade["hardenedPVCUIDs"]) == 3 and len(set(upgrade["hardenedPVCUIDs"])) == 3,
            "Hardened PVC custody is incomplete")
    log = (directory / "runtime.log").read_text()
    for marker in ('"controllerJWTMigration": "passed"', '"sharedAndUnrelatedJWTsRejected": true',
                   '"autopilotRepair": "passed"', '"preUpgradeData": "preserved"',
                   '"legacyStaticRestoreGuardrail": "passed"', "Operator upgrade E2E succeeded:"):
        require(marker in log, "upgrade runtime assertion receipt is missing")
    record = {"schemaVersion": 1, "claim": CLAIM, "context": plan["context"], "sources": plan["sources"],
              "planSHA256": P.fingerprint(plan), "version": plan["version"], "releaseBodySHA256": plan["releaseBodySHA256"],
              "inputsSHA256": plan["inputsSHA256"], "runtimePolicy": plan["runtimePolicy"], "externalImagesSHA256": plan["externalImagesSHA256"],
              "images": images, "imageProvenance": provenance, "scans": scans, "payloads": payloads["files"], "rows": rows,
              "selectedExecutions": sum(len(row["passedCaseIDs"]) for row in rows),
              "uniqueCaseIDs": len({case for row in rows for case in row["passedCaseIDs"]}),
              "upgrade": upgrade, "upgradeImagesSHA256": O.file_digest(directory / "images.json"), "upgradeNodes": nodes(directory / "nodes.json", plan["matrix"]["include"][0]["k8s"]),
              "origins": origins, "materialLimits": LIMITS, "runtimeQualificationVerified": True,
              "publicationAuthorized": False}
    require(record["selectedExecutions"] == plan["selectedExecutions"]
            and record["uniqueCaseIDs"] == plan["uniqueCaseIDs"], "full-profile execution totals differ")
    P.write_json(output / "qualification.json", record)
    require((output / "qualification.json").stat().st_size <= 2 * 1024 * 1024, "qualification record exceeds the consumer limit")


def consume(plan, output):
    output.mkdir(parents=True, exist_ok=False)
    O.download(plan["context"], plan["artifactPrefix"] + "-qualification", "Qualify Candidate",
               {"qualification.json", "bundle.json"}, output / "signed")
    subject, bundle = output / "signed/qualification.json", output / "signed/bundle.json"
    root = output / "trusted-root.jsonl"
    root.write_bytes(subprocess.check_output(["gh", "attestation", "trusted-root", "--hostname", "github.com"], timeout=120))
    identity_spec = importlib.util.spec_from_file_location("identity", ROOT / "hack/ci/release-identity.py")
    identity = importlib.util.module_from_spec(identity_spec)
    identity_spec.loader.exec_module(identity)
    command = identity.verify_command(subject, bundle, plan["sources"]["controllerSHA"], root,
        **{"cert-identity": "https://github.com/" + P.REPOSITORY + "/" + P.WORKFLOW + "@refs/heads/main"})
    data = identity.cli(*command)
    verified = json.loads(data, object_pairs_hook=P.unique)
    # Reuse the reviewed flat certificate verifier with this workflow and subject name.
    identity.PRODUCER = P.WORKFLOW
    expected_digest = O.file_digest(subject).removeprefix("sha256:")
    identity.verified_identity(verified, plan["sources"]["controllerSHA"], plan["context"]["runID"],
                               plan["context"]["attempt"], expected_digest, subject_name="qualification.json")
    record = P.read_json(subject)
    require(record["claim"] == CLAIM and record["context"] == plan["context"]
            and record["sources"] == plan["sources"] and record["planSHA256"] == P.fingerprint(plan)
            and record["runtimeQualificationVerified"] is True and record["publicationAuthorized"] is False,
            "signed qualification content differs")
    for overrides in ({"source-digest": "0" * 40}, {"signer-digest": "0" * 40},
                      {"cert-identity": "https://github.com/" + P.REPOSITORY + "/wrong.yml@refs/heads/main"}):
        rejected = identity.verify_command(subject, bundle, plan["sources"]["controllerSHA"], root,
            **{"cert-identity": "https://github.com/" + P.REPOSITORY + "/" + P.WORKFLOW + "@refs/heads/main", **overrides})
        require(subprocess.run(["gh", *rejected], capture_output=True, timeout=120).returncode != 0,
                "cryptographic rejection control unexpectedly accepted")
    changed = output / "changed.json"
    changed.write_bytes(subject.read_bytes() + b"\n")
    rejected = list(command)
    rejected[2] = str(changed)
    require(subprocess.run(["gh", *rejected], capture_output=True, timeout=120).returncode != 0,
            "changed subject bytes unexpectedly authenticated")
    (output / "verification.json").write_bytes(data)
    P.write_json(output / "result.json", {"authenticationPerformed": True, "qualificationSHA256": expected_digest,
        "planSHA256": P.fingerprint(plan), "cryptographicRejectionControls": 4, "publicationAuthorized": False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("command", choices=("audit", "consume"))
    args = parser.parse_args()
    try:
        plan = A.load_plan(args.plan)
        (audit if args.command == "audit" else consume)(plan, args.output.resolve())
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        print("qualification evidence audit failed; no qualification or publication authorization issued", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
