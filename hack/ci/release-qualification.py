#!/usr/bin/env python3
"""Freeze isolated candidate qualification inputs without granting publication authority."""

import argparse
import hashlib
import importlib.util
import json
import logging
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = "kubebao/openbao-operator"
REPOSITORY_ID = 1127385211
WORKFLOW = ".github/workflows/release-qualification-pilot.yml"
CLAIM = "release-candidate-qualification-pilot"
SOURCE_FILES = (
    "charts/openbao-operator/Chart.yaml", ".release-please-manifest.json", "go.mod", "go.sum",
    "test/e2e/suites.yaml", "test/e2e/catalog/cases.json", "test/fixtures/operator-upgrade/versions.env",
    "Dockerfile", "Dockerfile.init", "Dockerfile.backup", "Dockerfile.upgrade", "hack/dev/tool-versions.env",
    "hack/ci/operator-upgrade-e2e.sh", "hack/ci/operator-upgrade-jwt-migration.py",
    "test/utils/qualification/policy.go",
)
FROZEN_TEST_PATHS = ("test/e2e", "test/fixtures", "test/utils", "test/manifests", "hack/ci/operator-upgrade-e2e.sh",
                     "hack/ci/operator-upgrade-jwt-migration.py", "hack", "mk", "Makefile",
                     ".github/actions", ".semgrep", ".semgrepignore", ".govulnignore", ".gitignore",
                     "go.mod", "go.sum", "vendor")


class PolicyError(ValueError):
    """A controlled validation reason that contains no caller-authored data."""


def require(condition, message):
    if not condition:
        raise PolicyError(message)


def sha(value):
    require(type(value) is str and re.fullmatch(r"[0-9a-f]{40}", value), "expected a full lowercase Git SHA")
    return value


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def fingerprint(value):
    return digest(canonical(value))


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def read_json(path):
    with Path(path).open("rb") as stream:
        data = stream.read(2 * 1024 * 1024 + 1)
    require(len(data) <= 2 * 1024 * 1024, "JSON input exceeds two MiB")
    return json.loads(data, object_pairs_hook=unique)


def write_json(path, value):
    with Path(path).open("x") as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write("\n")


def git(repo, *args):
    # Repository-location/config overrides must not redirect candidate object reads.
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update({"GIT_NO_REPLACE_OBJECTS": "1", "GIT_NO_LAZY_FETCH": "1", "GIT_OPTIONAL_LOCKS": "0",
                "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull})
    result = subprocess.run(["git", "-C", str(repo), *args], env=env, capture_output=True, timeout=120)
    require(result.returncode == 0, "Git object or ancestry check failed")
    return result.stdout


def context(controller):
    sha(controller)
    expected = {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_REPOSITORY_ID": str(REPOSITORY_ID),
                "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
                "GITHUB_SHA": controller, "GITHUB_WORKFLOW_SHA": controller,
                "GITHUB_WORKFLOW_REF": f"{REPOSITORY}/{WORKFLOW}@refs/heads/main",
                "GITHUB_SERVER_URL": "https://github.com", "RUNNER_ENVIRONMENT": "github-hosted"}
    require(all(os.environ.get(key) == value for key, value in expected.items()), "unapproved controller context")
    run, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    require(re.fullmatch(r"[1-9][0-9]{0,19}", run) and re.fullmatch(r"[1-9][0-9]{0,5}", attempt),
            "invalid run or attempt")
    return {"repository": REPOSITORY, "repositoryID": REPOSITORY_ID, "workflow": WORKFLOW,
            "sourceSHA": controller, "runID": int(run), "attempt": int(attempt)}


def pr_metadata(number):
    require(type(number) is int and number > 0, "invalid pull request number")
    result = subprocess.run(["gh", "api", "--hostname", "github.com",
                             f"repos/{REPOSITORY}/pulls/{number}"], capture_output=True, timeout=120)
    require(result.returncode == 0, "cannot read the selected candidate PR")
    require(len(result.stdout) <= 2 * 1024 * 1024, "PR metadata exceeds size limit")
    return json.loads(result.stdout, object_pairs_hook=unique)


def inspect(repo, controller, head, pr):
    """Keep the real controller, reviewed PR head and tested merge preview distinct."""
    sha(controller)
    sha(head)
    require(pr["state"] == "open" and pr["draft"] is False and pr["merged"] is False,
            "qualification requires an open, non-draft candidate PR")
    require(pr["head"]["repo"]["id"] == pr["base"]["repo"]["id"] == REPOSITORY_ID,
            "this experiment accepts only canonical-repository candidates")
    require(pr["head"]["sha"] == head and pr["base"]["sha"] == controller
            and pr["base"]["ref"] == "main", "candidate head or selected base changed")
    candidate = sha(pr["merge_commit_sha"])
    require(git(repo, "rev-parse", "HEAD").decode().strip() == controller, "controller checkout differs")
    require(git(repo, "rev-parse", candidate + "^{commit}").decode().strip() == candidate,
            "candidate is not the selected genuine merge preview")
    parents = git(repo, "show", "-s", "--format=%P", candidate).decode().strip().split()
    require(parents == [controller, head], "candidate merge-preview parents differ")
    tree = sha(git(repo, "show", "-s", "--format=%T", candidate).decode().strip())
    epoch = int(git(repo, "show", "-s", "--format=%ct", candidate))
    require(epoch > 0, "invalid candidate epoch")
    files = {name: digest(git(repo, "show", f"{candidate}:{name}")) for name in SOURCE_FILES}
    test_trees = {}
    for name in FROZEN_TEST_PATHS:
        selected = sha(git(repo, "rev-parse", f"{candidate}:{name}").decode().strip())
        require(selected == git(repo, "rev-parse", f"{controller}:{name}").decode().strip(),
                "the first qualification pilot requires unchanged controller test inputs")
        test_trees[name] = selected
    return {"controllerSHA": controller, "baseSHA": controller, "prNumber": pr["number"],
            "prHeadSHA": head, "candidateSHA": candidate, "suiteSourceSHA": candidate,
            "tree": tree, "sourceDateEpoch": epoch, "sourceFileSHA256": files, "testInputTrees": test_trees}


def matrix_rows(matrix):
    require(type(matrix) is dict and set(matrix) == {"include"}, "invalid release matrix")
    rows = matrix["include"]
    require(type(rows) is list and 0 < len(rows) <= 256, "empty or oversized release matrix")
    seen = set()
    selected = 0
    unique_cases = set()
    frozen = []
    for row in rows:
        require(type(row) is dict and "qualification_row_key" not in row, "invalid matrix row")
        require(row["profile"] == "release-gate", "matrix is not the full release profile")
        key = fingerprint(row)
        require(key not in seen, "duplicate release matrix row")
        seen.add(key)
        cases = row["expected_case_ids"]
        require(type(cases) is list and cases and all(type(value) is str and value for value in cases)
                and len(set(cases)) == len(cases), "empty or duplicate selected case IDs")
        require(type(row["label_filter"]) is str and row["label_filter"], "missing effective selector")
        selected += len(cases)
        unique_cases.update(cases)
        frozen.append({**row, "qualification_row_key": key})
    return frozen, selected, len(unique_cases)


def freeze(source, version, notes, notes_hash, matrix, planned_matrix, inputs, execution):
    require(set(source) == {"controllerSHA", "baseSHA", "prNumber", "prHeadSHA", "candidateSHA", "suiteSourceSHA",
                            "tree", "sourceDateEpoch", "sourceFileSHA256", "testInputTrees"}, "unexpected source fields")
    for field in ("controllerSHA", "baseSHA", "prHeadSHA", "candidateSHA", "suiteSourceSHA", "tree"):
        sha(source[field])
    require(type(source["prNumber"]) is int and source["prNumber"] > 0, "invalid source PR")
    require(type(source["sourceDateEpoch"]) is int and source["sourceDateEpoch"] > 0, "invalid frozen epoch")
    require(set(source["testInputTrees"]) == set(FROZEN_TEST_PATHS), "frozen test input paths differ")
    for value in source["testInputTrees"].values():
        sha(value)
    require(set(source["sourceFileSHA256"]) == set(SOURCE_FILES)
            and all(type(value) is str and re.fullmatch(r"[0-9a-f]{64}", value)
                    for value in source["sourceFileSHA256"].values()), "source-file hash set differs")
    require(type(version) is str and re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version),
            "qualification version must be stable SemVer")
    require(0 < len(notes) <= 65535 and digest(notes) == notes_hash, "exact release-body bytes differ")
    notes.decode("utf-8")
    require(matrix == planned_matrix, "matrix differs from the complete trusted release plan")
    rows, count, unique_count = matrix_rows(matrix)
    require(source["controllerSHA"] == execution["sourceSHA"], "controller identity differs")
    require(source["candidateSHA"] == source["suiteSourceSHA"], "suite and product source differ")
    require(set(inputs) == {"schemaVersion", "retentionDays", "orasVersion", "orasLinuxAMD64SHA256",
                            "cosignVersion", "packages", "build", "tools"}
            and type(inputs["schemaVersion"]) is int and inputs["schemaVersion"] == 1, "invalid qualification inputs")
    require(type(inputs["retentionDays"]) is int and 1 <= inputs["retentionDays"] <= 90,
            "invalid explicit experimental retention")
    expected = {image: f"ghcr.io/kubebao/openbao-{image}-qualification"
                for image in ("operator", "init", "backup", "upgrade")}
    require(inputs["packages"] == expected, "experimental package allowlist differs")
    require(source["sourceFileSHA256"]["hack/dev/tool-versions.env"] == inputs["tools"]["fileSHA256"],
            "candidate CLI pins differ from the reviewed controller")
    return {"schemaVersion": 1, "claim": CLAIM, "context": execution, "sources": source,
            "version": version, "releaseBodySHA256": notes_hash,
            "releaseBodySource": "explicit complete workflow-dispatch input; frozen unchanged",
            "matrix": {"include": rows}, "originalMatrixSHA256": fingerprint(matrix),
            "selectedExecutions": count, "uniqueCaseIDs": unique_count, "inputs": inputs,
            "inputsSHA256": fingerprint(inputs), "platforms": ["linux/amd64", "linux/arm64"],
            "artifactPrefix": f"release-qualification-{execution['runID']}-{execution['attempt']}",
            "runtimePolicy": {"hardenedAssetPreload": False,
                              "candidateTransport": "digest-verified local aliases; namespace-authenticated Hardened pulls"},
            "runtimeQualificationVerified": False, "publicationAuthorized": False}


def inspect_command(args):
    execution = context(args.controller)
    sha(args.head)
    pr = pr_metadata(args.pr)
    require(pr["head"]["sha"] == args.head, "candidate head changed before object fetch")
    preview = sha(pr["merge_commit_sha"])
    origin = git(args.repo, "remote", "get-url", "origin").decode().strip()
    require(origin in (f"https://github.com/{REPOSITORY}", f"https://github.com/{REPOSITORY}.git"),
            "unexpected controller Git origin")
    git(args.repo, "fetch", "--no-tags", "origin", args.head, preview)
    source = inspect(args.repo, args.controller, args.head, pr)
    args.output.mkdir(parents=True, exist_ok=False)
    write_json(args.output / "source.json", source)
    for name, relative in [("suites.yaml", "test/e2e/suites.yaml"), ("cases.json", "test/e2e/catalog/cases.json")]:
        with (args.output / name).open("xb") as stream:
            stream.write(git(args.repo, "show", f"{source['candidateSHA']}:{relative}"))
    write_json(args.output / "context.json", execution)
    if os.environ.get("GITHUB_OUTPUT"):
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as stream:
            stream.write(f"candidate={source['candidateSHA']}\n")


def freeze_command(args):
    execution = context(args.controller)
    source = read_json(args.source)
    live = inspect(args.repo, args.controller, source["prHeadSHA"], pr_metadata(source["prNumber"]))
    require(source == live, "candidate identity changed before planning completed")
    chart = git(args.repo, "show", f"{source['candidateSHA']}:charts/openbao-operator/Chart.yaml").decode()
    spec = importlib.util.spec_from_file_location("advisory_contract", ROOT / "hack/ci/release-evidence.py")
    contract = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(contract)
    contract.validate_chart(chart, args.version)
    release_manifest = json.loads(git(args.repo, "show", f"{source['candidateSHA']}:.release-please-manifest.json"))
    require(release_manifest.get("") == args.version, "candidate release manifest version differs")
    inputs = controller_inputs()
    # Compile and execute only controller tools. Candidate files are planner data;
    # candidate Makefiles, scripts and Go source never run in this boundary.
    matrix = release_matrix(args.repo, source["candidateSHA"])
    plan = freeze(source, args.version, args.notes.read_bytes(), args.notes_sha, matrix, matrix, inputs, execution)
    external, baseline = external_inputs(args.repo, source["controllerSHA"], matrix)
    plan["externalImages"] = external
    plan["externalImagesSHA256"] = fingerprint(external)
    plan["upgradeBaseline"] = baseline
    write_json(args.output, plan)
    write_json(args.output.parent / "external-images.json", external)
    if os.environ.get("GITHUB_OUTPUT"):
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as stream:
            stream.write(f"plan_sha256={fingerprint(plan)}\n")
            stream.write("matrix=" + canonical(plan["matrix"]).decode() + "\n")
            outputs = {"epoch": source["sourceDateEpoch"], "version": args.version,
                       "helm_version": inputs["tools"]["versions"]["HELM_VERSION"],
                       "buildkit_image": inputs["build"]["buildkitImage"],
                       "buildx_version": inputs["build"]["buildxVersion"],
                       "syft_version": inputs["build"]["syftVersion"],
                       "syft_sha256": inputs["build"]["syftLinuxAMD64SHA256"],
                       "cosign_version": inputs["cosignVersion"]}
            for key, value in outputs.items():
                stream.write(f"{key}={value}\n")
            stream.write("image_map_sha256=" + digest((args.output.parent / "external-images.json").read_bytes()) + "\n")


def controller_inputs():
    data = (ROOT / "hack/dev/tool-versions.env").read_bytes()
    versions = {}
    for line in data.decode().splitlines():
        if not line or line.startswith("#"):
            continue
        match = re.fullmatch(r"([A-Z][A-Z0-9_]*)=(v[0-9]+\.[0-9]+\.[0-9]+)", line)
        require(match and match[1] not in versions, "invalid or duplicate controller CLI pin")
        versions[match[1]] = match[2]
    return {**read_json(ROOT / "hack/ci/release-qualification-inputs.json"),
            "build": read_json(ROOT / "hack/ci/release-pilot-inputs.json"),
            "tools": {"fileSHA256": digest(data), "versions": versions}}


def external_inputs(repo, controller, matrix):
    references = {"curlimages/curl:8.16.0", "amazon/aws-cli:latest",
                  "mcr.microsoft.com/azure-cli:latest", "registry.k8s.io/pause:3.9"}
    for row in matrix["include"]:
        for key in ("kind_node_image", "openbao_image", "rustfs_image", "fake_gcs_image", "azurite_image"):
            if row.get(key):
                references.add(row[key])
    constants = git(repo, "show", f"{controller}:test/e2e/e2e_versions.go").decode()
    versions = set()
    for name in ("defaultOpenBaoVersion", "defaultUpgradeFromVersion", "defaultUpgradeToVersion",
                 "defaultBlueGreenUpgradeFromVersion", "defaultBlueGreenUpgradeToVersion"):
        values = re.findall(r"\b" + name + r'\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"', constants)
        require(len(values) == 1, "controller must declare one default fixture version")
        versions.add(values[0])
    sequences = git(repo, "show", f"{controller}:test/e2e/upgradeversions/versions.go").decode()
    defaults = re.findall(r'const DefaultSequence = "([0-9.,]+)"', sequences)
    require(len(defaults) == 1, "controller must declare one default upgrade sequence")
    versions.update(defaults[0].split(","))
    for row in matrix["include"]:
        if row.get("upgrade_sequence"):
            versions.update(row["upgrade_sequence"].split(","))
    for version in versions:
        require(re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version), "invalid fixture version")
        references.add("openbao/openbao:" + version)
        references.add("ghcr.io/openbao/openbao:" + version)
    for fixture in ("cluster.yaml", "transit-cluster.yaml", "hardened-cluster.yaml"):
        data = git(repo, "show", f"{controller}:test/fixtures/operator-upgrade/{fixture}").decode()
        references.update(re.findall(r'image:\s*"(ghcr\.io/openbao/openbao:[0-9.]+)"', data))
    values = {}
    for line in git(repo, "show", f"{controller}:test/fixtures/operator-upgrade/versions.env").decode().splitlines():
        if line.startswith("OPERATOR_UPGRADE_"):
            key, value = line.split("=", 1)
            require(key not in values, "duplicate baseline input")
            values[key] = value
    baseline_version = values["OPERATOR_UPGRADE_BASELINE"]
    require(re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", baseline_version), "invalid baseline version")
    chart = values["OPERATOR_UPGRADE_BASELINE_CHART"]
    require(chart == "oci://ghcr.io/kubebao/charts/openbao-operator", "unapproved baseline chart")
    chart_reference = chart.removeprefix("oci://") + ":" + baseline_version
    references.update((chart_reference, values["OPERATOR_UPGRADE_BASELINE_INIT_IMAGE"],
                       "ghcr.io/kubebao/openbao-operator:" + baseline_version))
    resolved = {}
    for reference in sorted(references):
        require(len(reference) <= 1024 and re.fullmatch(
            r"(?:ghcr\.io/(?:openbao|kubebao)/|openbao/|kindest/|docker\.io/|mcr\.microsoft\.com/|"
            r"curlimages/|amazon/|registry\.k8s\.io/)"
            r"[a-z0-9][a-z0-9._/-]*(?::[A-Za-z0-9_][A-Za-z0-9._-]*)?(?:@sha256:[0-9a-f]{64})?", reference),
            "external image reference is outside the approved public fixture namespaces")
        logging.getLogger("qualification.inputs").info("Freezing external input %s", reference)
        registry_reference = "docker.io/" + reference if reference.startswith(("openbao/", "kindest/", "curlimages/", "amazon/")) else reference
        result = subprocess.run(["oras", "resolve", registry_reference], capture_output=True, timeout=120)
        value = result.stdout.decode().strip()
        require(result.returncode == 0 and re.fullmatch(r"sha256:[0-9a-f]{64}", value),
                "external image could not be frozen")
        require("@" not in reference or reference.endswith("@" + value), "pinned external digest differs")
        resolved[reference] = reference if "@" in reference else reference + "@" + value
    return resolved, {"version": baseline_version, "chart": chart,
                      "chartDigest": resolved[chart_reference].split("@", 1)[1],
                      "initImage": values["OPERATOR_UPGRADE_BASELINE_INIT_IMAGE"],
                      "managerImage": resolved["ghcr.io/kubebao/openbao-operator:" + baseline_version]}


def release_matrix(repo, candidate):
    sha(candidate)
    with tempfile.TemporaryDirectory(prefix="qualification-plan-") as directory:
        manifest, catalog = Path(directory) / "suites.yaml", Path(directory) / "cases.json"
        manifest.write_bytes(git(repo, "show", f"{candidate}:test/e2e/suites.yaml"))
        catalog.write_bytes(git(repo, "show", f"{candidate}:test/e2e/catalog/cases.json"))
        env = {**os.environ, "GOFLAGS": "-mod=vendor", "GOWORK": "off", "GOENV": "off"}
        result = subprocess.run(["go", "run", "./hack/tools/e2e_plan", "--manifest", str(manifest),
                                 "--catalog", str(catalog), "--format", "github-nightly-matrix",
                                 "--profile", "release-gate"], cwd=ROOT, env=env, capture_output=True, timeout=300)
    require(result.returncode == 0 and len(result.stdout) <= 2 * 1024 * 1024,
            "trusted full release planning failed")
    matrix = json.loads(result.stdout, object_pairs_hook=unique)
    matrix_rows(matrix)
    return matrix


def main():
    logging.basicConfig(level=logging.INFO, format="%(message)s")
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    inspect_parser = commands.add_parser("inspect-source")
    inspect_parser.add_argument("--pr", type=int, required=True)
    inspect_parser.add_argument("--head", required=True)
    freeze_parser = commands.add_parser("freeze")
    freeze_parser.add_argument("--source", type=Path, required=True)
    freeze_parser.add_argument("--version", required=True)
    freeze_parser.add_argument("--notes", type=Path, required=True)
    freeze_parser.add_argument("--notes-sha", required=True)
    for command in (inspect_parser, freeze_parser):
        command.add_argument("--repo", type=Path, default=ROOT)
        command.add_argument("--controller", required=True)
        command.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        (inspect_command if args.command == "inspect-source" else freeze_command)(args)
    except (ValueError, KeyError, TypeError, OSError, RecursionError, subprocess.TimeoutExpired) as error:
        detail = str(error) if isinstance(error, PolicyError) else type(error).__name__
        print(f"qualification planning failed: {detail}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
