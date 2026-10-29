#!/usr/bin/env python3
"""Verify isolated qualification build data without executing candidate code in import jobs."""

import argparse
import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile


ROOT = Path(__file__).resolve().parents[2]
ROLES = ("operator", "init", "backup", "upgrade")
BUILDS = ("primary", "rebuild")


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / "hack/ci" / filename)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


PLAN = module("qualification_plan", "release-qualification.py")
OCI = module("qualification_oci", "release-pilot.py")
ORIGIN = module("qualification_origin", "qualification-origin.py")
require = PLAN.require


def load_plan(path):
    plan = PLAN.read_json(path)
    require(plan["schemaVersion"] == 1 and plan["claim"] == PLAN.CLAIM
            and plan["publicationAuthorized"] is False and plan["runtimeQualificationVerified"] is False,
            "unexpected plan claim or authority")
    expected = os.environ["QUALIFICATION_PLAN_SHA256"]
    require(re.fullmatch(r"[0-9a-f]{64}", expected) and PLAN.fingerprint(plan) == expected,
            "qualification plan hash differs from the trusted planner output")
    require(plan["context"] == PLAN.context(plan["sources"]["controllerSHA"]), "selected execution differs")
    require(plan["inputs"]["packages"] == {
        role: f"ghcr.io/kubebao/openbao-{role}-qualification" for role in ROLES}, "package allowlist differs")
    return plan


def source(plan, directory):
    selected = plan["sources"]
    require(PLAN.git(directory, "rev-parse", "HEAD").decode().strip() == selected["candidateSHA"],
            "candidate checkout differs")
    require(PLAN.git(directory, "rev-parse", "HEAD^{tree}").decode().strip() == selected["tree"],
            "candidate checkout tree differs")
    require(not PLAN.git(directory, "status", "--porcelain=v1").strip(), "candidate checkout is not clean")


def bounded_tar(path):
    """Check raw TAR headers before a parser can allocate extended-header data."""
    size, position, count, expanded = path.stat().st_size, 0, 0, 0
    require(0 < size <= ORIGIN.MAX_BYTES and size % 512 == 0, "OCI archive exceeds size limit or is not raw TAR")
    with path.open("rb") as stream:
        while position < size:
            stream.seek(position)
            header = stream.read(512)
            if header == bytes(512):
                require(stream.read(512) == bytes(512), "OCI archive lacks a complete TAR end marker")
                for block in iter(lambda: stream.read(1024 * 1024), b""):
                    require(not any(block), "OCI archive has data after its TAR end marker")
                return
            member = tarfile.TarInfo.frombuf(header, "utf-8", "strict")
            count += 1
            expanded += member.size
            require(count <= 4096 and 0 <= member.size <= 512 * 1024 * 1024
                    and expanded <= ORIGIN.MAX_BYTES, "OCI archive entry or expanded-size limit exceeded")
            require(member.type in (tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE),
                    "OCI archive contains a sparse file, link, extended header or special file")
            if member.type == tarfile.DIRTYPE:
                require(member.size == 0 and member.name in ("blobs", "blobs/sha256"), "unexpected OCI directory")
            else:
                require(member.name in ("index.json", "oci-layout") or
                        re.fullmatch(r"blobs/sha256/[0-9a-f]{64}", member.name), "unexpected OCI archive member")
            position += 512 + ((member.size + 511) // 512) * 512
            require(position <= size, "OCI archive member extends beyond the actual bytes")
    raise ValueError("OCI archive lacks a TAR end marker")


def image_bytes(path):
    bounded_tar(path)
    # Do not extract candidate tar members onto the signing runner's filesystem.
    with tarfile.open(path, mode="r:") as archive:
        members = archive.getmembers()
        files = {member.name: member for member in members if member.isfile()}
        require(len(files) == sum(member.isfile() for member in members), "duplicate OCI archive file")

        def decoded(name):
            member = files[name]
            require(member.size <= 2 * 1024 * 1024, "OCI JSON exceeds size limit")
            return json.loads(archive.extractfile(member).read(), object_pairs_hook=PLAN.unique)

        def descriptor_json(descriptor):
            require(type(descriptor["size"]) is int and descriptor["size"] > 0
                    and re.fullmatch(r"sha256:[0-9a-f]{64}", descriptor["digest"]), "invalid OCI descriptor")
            return decoded("blobs/" + descriptor["digest"].replace(":", "/"))

        require(decoded("oci-layout") == {"imageLayoutVersion": "1.0.0"}, "unexpected OCI layout version")
        layout = decoded("index.json")
        require(layout["schemaVersion"] == 2 and len(layout["manifests"]) == 1, "unexpected OCI layout index")
        index = descriptor_json(layout["manifests"][0])
        require(index["schemaVersion"] == 2 and index["mediaType"] == "application/vnd.oci.image.index.v1+json",
                "archive subject is not an OCI image index")
        for manifest in index["manifests"]:
            data = descriptor_json(manifest)
            require(data["schemaVersion"] == 2 and data["mediaType"] == "application/vnd.oci.image.manifest.v1+json",
                    "archive platform is not an OCI image manifest")
            descriptor_json(data["config"])
    return OCI.inspect_image(path)


def image_record(plan, build, role, archive):
    require(build in BUILDS and role in ROLES, "unexpected build or image role")
    image = image_bytes(archive)
    require(all(datetime.datetime.fromisoformat(platform["created"].replace("Z", "+00:00")).timestamp()
                == plan["sources"]["sourceDateEpoch"] for platform in image["platforms"].values()),
            "image creation time differs from the frozen candidate epoch")
    return {"schemaVersion": 1, "claim": "unqualified-candidate-build-data", "build": build, "role": role,
            "planSHA256": PLAN.fingerprint(plan), "sourceSHA": plan["sources"]["candidateSHA"],
            "tree": plan["sources"]["tree"], "sourceDateEpoch": plan["sources"]["sourceDateEpoch"],
            "execution": plan["context"], **image}


def verify_image(plan, build, role, directory):
    actual = image_record(plan, build, role, directory / f"{role}.tar")
    require(PLAN.read_json(directory / f"{role}.json") == actual, "image receipt does not describe the actual bytes")
    return actual


def private_packages(plan):
    # Never create or change a package's visibility in this experiment. A maintainer
    # must prepare the four private packages and grant this repository access first.
    for role in ROLES:
        name = plan["inputs"]["packages"][role].removeprefix("ghcr.io/kubebao/")
        response = subprocess.run(["gh", "api", "--hostname", "github.com",
                                   "orgs/kubebao/packages/container/" + name], capture_output=True, timeout=120)
        require(response.returncode == 0 and len(response.stdout) <= 2 * 1024 * 1024,
                "prepare the four private experimental packages and grant workflow access before dispatch")
        package = json.loads(response.stdout, object_pairs_hook=PLAN.unique)
        require(package["name"] == name and package["package_type"] == "container"
                and package["visibility"] == "private" and package["owner"]["login"] == "kubebao",
                "experimental destination is not an approved private package")


def import_images(plan, output):
    require(os.environ.get("GITHUB_JOB") == "import", "unexpected import job")
    output.mkdir(parents=True, exist_ok=False)
    ORIGIN.download(plan["context"], plan["artifactPrefix"] + "-plan", "Plan Candidate",
                    {"plan.json", "release-body.txt", "external-images.json"}, output / "frozen-plan")
    require(PLAN.read_json(output / "frozen-plan/plan.json") == plan
            and PLAN.digest((output / "frozen-plan/release-body.txt").read_bytes()) == plan["releaseBodySHA256"],
            "authenticated plan or release-body artifact differs")
    require(PLAN.read_json(output / "frozen-plan/external-images.json") == plan["externalImages"],
            "external image map differs from the frozen plan")
    private_packages(plan)
    origins, images = {}, {}
    for role in ROLES:
        directory = output / role
        origins[role] = ORIGIN.download(
            plan["context"], f"{plan['artifactPrefix']}-primary-image-{role}", f"Build (primary / {role})",
            {f"{role}.json", f"{role}.tar"}, directory)
        actual = verify_image(plan, "primary", role, directory)
        rebuilt = output / (role + "-rebuild")
        origins[role + "Rebuild"] = ORIGIN.download(
            plan["context"], f"{plan['artifactPrefix']}-rebuild-image-{role}", f"Build (rebuild / {role})",
            {f"{role}.json", f"{role}.tar"}, rebuilt)
        independent = verify_image(plan, "rebuild", role, rebuilt)
        require(actual["digest"] == independent["digest"] and actual["platforms"] == independent["platforms"],
                "independent image builds differ")
        PLAN.write_json(directory / "predicate.json", {"schemaVersion": 1,
            "claim": "isolated-candidate-image-import", "controller": plan["context"],
            "sources": plan["sources"], "planSHA256": PLAN.fingerprint(plan),
            "producingArtifact": origins[role], "subjectDigest": actual["digest"],
            "runtimeQualificationVerified": False, "publicationAuthorized": False})
        digest = actual["digest"]
        target = plan["inputs"]["packages"][role] + "@" + digest
        # A digest-only destination creates no mutable transport tag to overwrite.
        result = subprocess.run(["oras", "cp", "--from-oci-layout", str(directory / f"{role}.tar") + "@" + digest,
                                 target], capture_output=True, timeout=300)
        require(result.returncode == 0, "isolated OCI import failed")
        resolved = subprocess.run(["oras", "resolve", target], capture_output=True, timeout=120)
        require(resolved.returncode == 0 and resolved.stdout.decode().strip() == digest,
                "registry index digest differs from the verified archive")
        images[role] = target
    policy = {"schemaVersion": 1, "claim": PLAN.CLAIM, "planSHA256": PLAN.fingerprint(plan),
              "controllerSHA": plan["sources"]["controllerSHA"],
              "issuer": "https://token.actions.githubusercontent.com",
              "subject": "https://github.com/" + PLAN.REPOSITORY + "/" + PLAN.WORKFLOW + "@refs/heads/main",
              "images": images}
    PLAN.write_json(output / "images.json", images)
    PLAN.write_json(output / "policy.json", policy)
    PLAN.write_json(output / "import.json", {"planSHA256": PLAN.fingerprint(plan), "origins": origins,
                                             "images": images, "runtimeQualificationVerified": False,
                                             "publicationAuthorized": False})
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as stream:
        stream.write("images=" + PLAN.canonical(images).decode() + "\n")
        stream.write("policy_sha256=" + PLAN.digest((output / "policy.json").read_bytes()) + "\n")


def files_record(plan, build, images, directory):
    digests = {role: verify_image(plan, build, role, images)["digest"] for role in ROLES}
    return {"schemaVersion": 1, "claim": "unqualified-candidate-build-data", "build": build,
            "planSHA256": PLAN.fingerprint(plan), "sourceSHA": plan["sources"]["candidateSHA"],
            "execution": plan["context"], "imageDigests": digests,
            "files": {name: ORIGIN.file_digest(directory / name) for name in OCI.release_files(plan)}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    commands = parser.add_subparsers(dest="command", required=True)
    selected = commands.add_parser("source")
    selected.add_argument("--directory", type=Path, required=True)
    image = commands.add_parser("record-image")
    image.add_argument("--role", choices=ROLES, required=True)
    image.add_argument("--archive", type=Path, required=True)
    image.add_argument("--output", type=Path, required=True)
    files = commands.add_parser("record-files")
    files.add_argument("--images", type=Path, required=True)
    files.add_argument("--directory", type=Path, required=True)
    files.add_argument("--output", type=Path, required=True)
    for command in (image, files):
        command.add_argument("--build", choices=BUILDS, required=True)
    importer = commands.add_parser("import-images")
    importer.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        plan = load_plan(args.plan)
        if args.command == "source":
            source(plan, args.directory)
        elif args.command == "record-image":
            PLAN.write_json(args.output, image_record(plan, args.build, args.role, args.archive))
        elif args.command == "record-files":
            PLAN.write_json(args.output, files_record(plan, args.build, args.images, args.directory))
        else:
            import_images(plan, args.output)
    except (ValueError, TypeError, KeyError, OSError, RecursionError, tarfile.TarError, subprocess.TimeoutExpired) as error:
        controlled = isinstance(error, (PLAN.PolicyError, ORIGIN.OriginPolicyError))
        detail = str(error) if controlled else type(error).__name__
        print(f"qualification artifact verification failed: {detail}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
