#!/usr/bin/env python3
"""Execute isolated candidate checks with controller-selected inputs and no signing authority."""

import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("qualification_artifacts", ROOT / "hack/ci/qualification-artifacts.py")
A = importlib.util.module_from_spec(spec)
spec.loader.exec_module(A)
P, O, require = A.PLAN, A.ORIGIN, A.require
SOURCE = ROOT / "source-under-test"
BINARY_FILES = {"e2e.test", "ginkgo", "binaries.json"}
IMPORT_FILES = {"images.json", "policy.json", "import.json"}


def run(command, *, cwd=ROOT, env=None, output=None, timeout=3600):
    with (output.open("xb") if output else open(os.devnull, "w")) as stream:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=stream if output else None,
                                stderr=subprocess.STDOUT if output else None, timeout=timeout)
    require(result.returncode == 0, "candidate check failed; inspect its retained public log")


def receipt(plan, stage, **fields):
    return {"schemaVersion": 1, "stage": stage, "planSHA256": P.fingerprint(plan),
            "execution": plan["context"], "sources": plan["sources"], **fields}


def imported(plan, directory):
    O.download(plan["context"], plan["artifactPrefix"] + "-import", "Import Experimental Helpers",
               IMPORT_FILES, directory)
    images, policy, record = (P.read_json(directory / name) for name in ("images.json", "policy.json", "import.json"))
    require(record["planSHA256"] == P.fingerprint(plan) and record["images"] == images
            and record["runtimeQualificationVerified"] is False and record["publicationAuthorized"] is False,
            "import receipt differs")
    require(policy["controllerSHA"] == plan["sources"]["controllerSHA"]
            and policy["planSHA256"] == P.fingerprint(plan) and policy["images"] == images,
            "helper verification policy differs")
    for role in A.ROLES:
        require(images[role].startswith(plan["inputs"]["packages"][role] + "@sha256:")
                and len(images[role].split("@sha256:")[1]) == 64, "imported image coordinates differ")
    return images, policy


def compile_suite(plan, output):
    A.source(plan, SOURCE)
    output.mkdir(parents=True, exist_ok=False)
    env = {**os.environ, "GOFLAGS": "-mod=vendor", "CGO_ENABLED": "0", "GOWORK": "off"}
    run(["go", "test", "-c", "-tags=e2e", "-o", str(output / "e2e.test"), "./test/e2e"], cwd=SOURCE, env=env)
    # The runner comes from the reviewed controller, the suite comes from candidate M.
    run(["go", "build", "-o", str(output / "ginkgo"), "github.com/onsi/ginkgo/v2/ginkgo"], env=env)
    A.source(plan, SOURCE)
    P.write_json(output / "binaries.json", receipt(plan, "compile", sha256={
        name: O.file_digest(output / name) for name in ("e2e.test", "ginkgo")}))


def verify_binaries(plan, directory):
    record = P.read_json(directory / "binaries.json")
    expected = receipt(plan, "compile", sha256={name: O.file_digest(directory / name)
                                               for name in ("e2e.test", "ginkgo")})
    require(record == expected, "candidate executable source, context or checksum differs")
    for name in ("e2e.test", "ginkgo"):
        (directory / name).chmod(0o755)
    return record


def selected_row(plan, key):
    rows = [row for row in plan["matrix"]["include"] if row["qualification_row_key"] == key]
    require(len(rows) == 1, "selected runtime row differs from the full plan")
    return rows[0]


def pin(plan, reference):
    require(reference in plan["externalImages"], "external runtime input is absent from the frozen map")
    return plan["externalImages"][reference]


def image_map_file(plan):
    path = ROOT / "dist/plan/external-images.json"
    require(P.read_json(path) == plan["externalImages"]
            and P.fingerprint(plan["externalImages"]) == plan["externalImagesSHA256"],
            "runtime image-map artifact differs from the frozen plan")
    return path


def freeze_fixture_images(text, images):
    def replace(match):
        reference = match[3]
        require(reference in images, "upgrade fixture image is absent from the frozen map")
        return match[1] + '"' + images[reference] + '"' + match[4]
    return re.sub(r"^(\s*image:\s*)([\"']?)([^\s\"'#]+)\2([ \t]*(?:#.*)?)$", replace, text, flags=re.MULTILINE)


def auth_file():
    path = Path(os.environ["RUNNER_TEMP"]) / "qualification-auth.json"
    token = os.environ["GH_TOKEN"]
    data = {"auths": {"ghcr.io": {"auth": base64.b64encode(
        (os.environ["GITHUB_ACTOR"] + ":" + token).encode()).decode()}}}
    with open(path, "x", opener=lambda name, flags: os.open(name, flags, 0o600)) as stream:
        json.dump(data, stream)
    return str(path)


def configure(plan, output, key):
    A.source(plan, SOURCE)
    row = selected_row(plan, key)
    image_map_file(plan)
    output.mkdir(parents=True, exist_ok=False)
    images, _ = imported(plan, output / "import")
    binaries = Path(os.environ["RUNNER_TEMP"]) / "qualification-binaries"
    O.download(plan["context"], plan["artifactPrefix"] + "-binaries", "Compile Candidate Suite", BINARY_FILES, binaries)
    metadata = verify_binaries(plan, binaries)
    # Digest pulls are verified by Docker. Local aliases transport those same configs into Kind.
    aliases, transport = {}, {}
    for role, image in images.items():
        alias = f"qualification.local/openbao-{role}:{plan['context']['runID']}-{plan['context']['attempt']}"
        run(["docker", "pull", image])
        data = subprocess.check_output(["docker", "image", "inspect", image], timeout=120)
        inspected = json.loads(data)[0]
        require(image in inspected["RepoDigests"], "Docker did not retain the selected repository digest")
        run(["docker", "tag", image, alias])
        aliases[role] = alias
        transport[role] = {"source": image, "alias": alias, "configDigest": inspected["Id"]}
    env = {**os.environ, "LANE_JSON": json.dumps(row), "E2E_IMAGE_TRANSPORT": "artifact",
           "SELECTED_SUITE_SOURCE_SHA": plan["sources"]["suiteSourceSHA"],
           "MANAGER_IMAGE": aliases["operator"], "CONFIG_INIT_IMAGE": aliases["init"],
           "BACKUP_EXECUTOR_IMAGE": aliases["backup"], "UPGRADE_EXECUTOR_IMAGE": aliases["upgrade"],
           "HARDENED_INIT_IMAGE": images["init"], "HARDENED_UPGRADE_IMAGE": images["upgrade"],
           "OPENBAO_IMAGE": pin(plan, row["openbao_image"]), "KMIP_SERVER_IMAGE": "",
           "RELEASE_OPERATOR_VERSION": plan["version"], "REPORT_DIR": str(output),
           "ARTIFACT_NAME": plan["artifactPrefix"] + "-row-" + key}
    run(["python3", "hack/ci/configure-e2e-lane.py"], env=env)
    values = {"KIND_NODE_IMAGE": pin(plan, row["kind_node_image"]),
              "E2E_UPGRADE_EXECUTOR_IMAGE": aliases["upgrade"],
              "E2E_PRELOAD_HARDENED_ASSETS": "false",
              "E2E_QUALIFICATION_POLICY_FILE": str(output / "import/policy.json"),
              "E2E_QUALIFICATION_POLICY_SHA256": O.file_digest(output / "import/policy.json").removeprefix("sha256:"),
              "E2E_QUALIFICATION_AUTH_FILE": auth_file(),
              "E2E_QUALIFICATION_IMAGE_MAP_FILE": str(ROOT / "dist/plan/external-images.json"),
              "E2E_QUALIFICATION_IMAGE_MAP_SHA256": O.file_digest(ROOT / "dist/plan/external-images.json").removeprefix("sha256:"),
              "QUALIFICATION_BINARY_DIR": str(binaries), "QUALIFICATION_ROW_KEY": key}
    for field in ("rustfs", "fake_gcs", "azurite"):
        if row.get(field + "_image"):
            values["E2E_" + field.upper() + "_IMAGE"] = pin(plan, row[field + "_image"])
    with Path(os.environ["GITHUB_ENV"]).open("a") as stream:
        for name, value in values.items():
            require("\n" not in value and "\r" not in value, "runtime environment contains multiple lines")
            stream.write(name + "=" + value + "\n")
    P.write_json(output / "runtime-inputs.json", receipt(plan, "e2e", row=row, images=images,
                                                        transport=transport, binaries=metadata, runtimePolicy=plan["runtimePolicy"]))


def observations():
    data = json.loads(subprocess.check_output(["kubectl", "get", "pods", "-A", "-o", "json"], timeout=120))
    return {"runner": {name: os.environ.get(name) for name in
                       ("ImageOS", "ImageVersion", "RUNNER_OS", "RUNNER_ARCH")},
            "pods": [{"namespace": pod["metadata"]["namespace"], "name": pod["metadata"]["name"],
                      "images": [c["image"] for c in pod["spec"].get("initContainers", []) + pod["spec"].get("containers", [])],
                      "imageIDs": [c.get("imageID") for c in pod.get("status", {}).get("initContainerStatuses", [])
                                   + pod.get("status", {}).get("containerStatuses", [])]}
                     for pod in data["items"]]}


def execute(plan, output, key):
    row = selected_row(plan, key)
    bundle = Path(os.environ["QUALIFICATION_BINARY_DIR"])
    verify_binaries(plan, bundle)
    run(["kubectl", "get", "nodes", "-o", "json"], output=output / "nodes.json")
    observed = {"before": observations()}
    # Install the candidate suite next to its matching fixture tree. No event SHA is forged.
    executable = SOURCE / "test/e2e/qualification.test"
    shutil.copyfile(bundle / "e2e.test", executable)
    executable.chmod(0o755)
    env = {**os.environ, "E2E_BINARY_DIR": "", "GINKGO_EDITOR_INTEGRATION": "true"}
    run([str(bundle / "ginkgo"), "--no-color", "--fail-on-empty", "--flake-attempts=1",
         "--procs=" + str(row["parallel_nodes"]), "--timeout=" + row["e2e_timeout"],
         "--label-filter=" + row["label_filter"], "--json-report=" + str(output / "ginkgo.json"),
         "--junit-report=" + str(output / "junit.xml"), str(executable)], cwd=SOURCE / "test/e2e", env=env,
         output=output / "runtime.log", timeout=int(row["timeout_minutes"]) * 60)
    observed["after"] = observations()
    P.write_json(output / "observations.json", observed)


def checks(plan, output, stage):
    A.source(plan, SOURCE)
    output.mkdir(parents=True, exist_ok=False)
    env = {**os.environ, "GOFLAGS": "-mod=vendor", "CRD_COMPAT_TARGET": plan["version"]}
    commands = {
        "api": [["go", "test", "-count=1", "./hack/tools/api_inventory", "./hack/tools/crd_compatibility",
                 "./test/manifests/apicontract"], ["make", "verify-generated"], ["make", "verify-api-contract"]],
        "source-security": [["make", "vulncheck"], ["make", "semgrep-ci"]],
    }[stage]
    for index, command in enumerate(commands):
        run(command, cwd=SOURCE, env=env, output=output / f"check-{index}.log")
    A.source(plan, SOURCE)
    P.write_json(output / "result.json", receipt(plan, stage, commands=commands,
                 logs={path.name: O.file_digest(path) for path in sorted(output.glob("*.log"))}))


def scan(plan, output, role):
    output.mkdir(parents=True, exist_ok=False)
    images, _ = imported(plan, output / "import")
    run(["trivy", "image", "--severity", "HIGH,CRITICAL", "--ignore-unfixed", "--exit-code", "1",
         "--format", "json", "--output", str(output / "trivy.json"), images[role]])
    run(["trivy", "version", "--format", "json"], output=output / "scanner.json")
    P.write_json(output / "result.json", receipt(plan, "image-security", role=role, image=images[role],
             reportSHA256=O.file_digest(output / "trivy.json"), scannerSHA256=O.file_digest(output / "scanner.json"),
             policy={"severity": ["HIGH", "CRITICAL"], "ignoreUnfixed": True},
             runner={name: os.environ.get(name) for name in ("ImageOS", "ImageVersion", "RUNNER_OS", "RUNNER_ARCH")}))


def upgrade(plan, output):
    A.source(plan, SOURCE)
    output.mkdir(parents=True, exist_ok=False)
    images, _ = imported(plan, output / "import")
    image_map_file(plan)
    fixture = Path(os.environ["RUNNER_TEMP"]) / "qualification-upgrade-fixtures"
    shutil.copytree(SOURCE / "test/fixtures/operator-upgrade", fixture)
    for path in fixture.glob("*.yaml"):
        path.write_text(freeze_fixture_images(path.read_text(), plan["externalImages"]))
    env = {**os.environ, "OPERATOR_UPGRADE_E2E_FIXTURE_DIR": str(fixture),
           "OPERATOR_UPGRADE_E2E_BASELINE_MANAGER_IMAGE": plan["upgradeBaseline"]["managerImage"],
           "OPERATOR_UPGRADE_E2E_FROM_VERSION": plan["upgradeBaseline"]["version"],
           "OPERATOR_UPGRADE_E2E_TARGET_VERSION": plan["version"],
           "OPERATOR_UPGRADE_E2E_MANAGER_SOURCE": images["operator"],
           "OPERATOR_UPGRADE_E2E_INIT_SOURCE": images["init"],
           "OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE": images["init"],
           "OPERATOR_UPGRADE_E2E_BASELINE_CHART_DIGEST": plan["upgradeBaseline"]["chartDigest"],
           "E2E_QUALIFICATION_POLICY_FILE": str(output / "import/policy.json"),
           "E2E_QUALIFICATION_POLICY_SHA256": O.file_digest(output / "import/policy.json").removeprefix("sha256:"),
           "E2E_QUALIFICATION_AUTH_FILE": auth_file(),
           "E2E_QUALIFICATION_IMAGE_MAP_FILE": str(ROOT / "dist/plan/external-images.json"),
           "E2E_QUALIFICATION_IMAGE_MAP_SHA256": O.file_digest(ROOT / "dist/plan/external-images.json").removeprefix("sha256:"),
           "OPERATOR_UPGRADE_E2E_REPORT_DIR": str(output),
           "KIND_NODE_IMAGE": pin(plan, plan["matrix"]["include"][0]["kind_node_image"])}
    run(["bash", str(SOURCE / "hack/ci/operator-upgrade-e2e.sh")], cwd=SOURCE, env=env,
         output=output / "runtime.log", timeout=5400)
    P.write_json(output / "result.json", receipt(plan, "operator-upgrade", images=images,
                 baseline=plan["upgradeBaseline"], targetVersion=plan["version"], logSHA256=O.file_digest(output / "runtime.log")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("command", choices=("compile", "configure", "execute", "api", "source-security", "scan", "upgrade"))
    parser.add_argument("--row")
    parser.add_argument("--role", choices=A.ROLES)
    args = parser.parse_args()
    try:
        plan = A.load_plan(args.plan)
        output = args.output.resolve()
        if args.command == "compile":
            compile_suite(plan, output)
        elif args.command in ("configure", "execute"):
            (configure if args.command == "configure" else execute)(plan, output, args.row)
        elif args.command in ("api", "source-security"):
            checks(plan, output, args.command)
        elif args.command == "scan":
            require(args.role in A.ROLES, "image scan role is required")
            scan(plan, output, args.role)
        else:
            upgrade(plan, output)
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        print("qualification runtime stage failed; inspect retained stage evidence", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
