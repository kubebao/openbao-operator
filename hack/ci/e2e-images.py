#!/usr/bin/env python3
"""Build and transport untrusted fork E2E images without registry credentials."""

import hashlib
import io
import json
import os
import platform
import re
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

sys.dont_write_bytecode = True

from e2e_artifacts import digest, run_identity, verify_identity

ROLES = {
    "manager": ("hack/ci/Dockerfile.fast.manager", ".", ["BINARY=bin/linux_amd64/manager", "TARGET=/manager"]),
    "config_init": ("hack/ci/Dockerfile.fast.init", ".", []),
    "backup_executor": ("hack/ci/Dockerfile.fast.backup", ".", []),
    "upgrade_executor": ("hack/ci/Dockerfile.fast.upgrade", ".", []),
    "openbao_softhsm": ("test/e2e/images/openbao-softhsm/Dockerfile", "test/e2e/images/openbao-softhsm", []),
    "pykmip_server": ("test/e2e/images/pykmip-server/Dockerfile", "test/e2e/images/pykmip-server", []),
}
BASE_ROLES = set(list(ROLES)[:4])


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def image_refs(identity, roles):
    suffix = f"{identity['runId']}-{identity['buildAttempt']}"
    return {role: f"openbao-fork-e2e-{role.replace('_', '-')}:{suffix}" for role in roles}


def runner_platform():
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if arch is None:
        raise ValueError("unsupported E2E image architecture")
    return "linux/" + arch


def build(bundle, sha):
    identity = run_identity(sha)
    if identity is None:
        raise ValueError("fork image builds require workflow identity")
    if command("git", "rev-parse", "HEAD") != sha or command("git", "status", "--porcelain=v1"):
        raise ValueError("build fork images from the clean workflow checkout")
    roles = list(ROLES) if os.environ.get("BUILD_SEAL_FIXTURES") == "true" else list(ROLES)[:4]
    refs = image_refs(identity, roles)
    bundle.mkdir(parents=True, exist_ok=True)
    for role, ref in refs.items():
        dockerfile, context, build_args = ROLES[role]
        args = ["docker", "buildx", "build", "--platform", runner_platform(), "--load",
                "--provenance=false", "--sbom=false", "--file", dockerfile, "--tag", ref]
        for arg in build_args:
            args += ["--build-arg", arg]
        subprocess.run([*args, context], check=True)
    images = {role: {"ref": ref, "id": command("docker", "image", "inspect", "--format", "{{.Id}}", ref)}
              for role, ref in refs.items()}
    subprocess.run(["docker", "save", "--output", str(bundle / "images.tar"), *refs.values()], check=True)
    metadata = {"version": 1, "sourceSHA": sha, "workflow": identity, "platform": runner_platform(),
                "images": images, "archiveSHA256": digest(bundle / "images.tar")}
    (bundle / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    verify(bundle, sha, identity["buildAttempt"])
    with open(os.environ["GITHUB_OUTPUT"], "a") as outputs:
        for role, ref in refs.items():
            outputs.write(f"{role}_ref={ref}\n")
        outputs.write(f"artifact_name=e2e-images-{identity['runId']}-{identity['buildAttempt']}\n")
        outputs.write(f"build_attempt={identity['buildAttempt']}\n")


def regular_file(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("image bundle requires regular metadata and archive files")


def archive_manifest(archive):
    members = {}
    for member in archive:
        path = PurePosixPath(member.name)
        if (path.is_absolute() or ".." in path.parts or str(path) != (member.name.rstrip("/") if member.isdir() else member.name)
                or not (member.isfile() or member.isdir())):
            raise ValueError("unsafe image archive member")
        if member.name in members:
            raise ValueError("duplicate image archive member")
        members[member.name] = member
    manifest = members.get("manifest.json")
    if manifest is None or not manifest.isfile() or manifest.size > 1024 * 1024:
        raise ValueError("missing or oversized Docker image manifest")
    return members, json.load(archive.extractfile(manifest))


def verify(bundle, sha, producer_attempt):
    regular_file(bundle / "metadata.json")
    regular_file(bundle / "images.tar")
    metadata = json.loads((bundle / "metadata.json").read_text())
    if not isinstance(metadata, dict):
        raise ValueError("image metadata must be an object")
    if metadata.get("version") != 1 or metadata.get("sourceSHA") != sha:
        raise ValueError("image bundle source or schema mismatch")
    if run_identity(sha) is None:
        raise ValueError("fork image verification requires workflow identity")
    verify_identity(metadata.get("workflow"), sha, producer_attempt)
    if metadata.get("platform") != runner_platform():
        raise ValueError("image bundle platform mismatch")
    images = metadata.get("images")
    if not isinstance(images, dict) or set(images) not in (BASE_ROLES, set(ROLES)):
        raise ValueError("image bundle roles are incomplete or unknown")
    if os.environ.get("E2E_REQUIRE_SEAL_FIXTURES", "false") == "true" and set(images) != set(ROLES):
        raise ValueError("seal lane requires both candidate seal fixture images")
    refs = image_refs(metadata["workflow"], images)
    for role, image in images.items():
        if not isinstance(image, dict) or image.get("ref") != refs[role] or not re.fullmatch(
                r"sha256:[0-9a-f]{64}", image.get("id", "")):
            raise ValueError("invalid image reference or config digest")
    if digest(bundle / "images.tar") != metadata.get("archiveSHA256"):
        raise ValueError("image archive checksum mismatch")
    with tarfile.open(bundle / "images.tar", "r:") as archive:
        members, manifest = archive_manifest(archive)
        expected = {image["ref"]: image["id"] for image in images.values()}
        if not isinstance(manifest, list) or len(manifest) != len(expected):
            raise ValueError("image archive contains unexpected images")
        seen = set()
        canonical = []
        for image in manifest:
            tags = image.get("RepoTags", [])
            if len(tags) != 1 or tags[0] not in expected or tags[0] in seen:
                raise ValueError("image archive tags differ from the planned roles")
            seen.add(tags[0])
            if not isinstance(image.get("Config"), str) or not isinstance(image.get("Layers"), list):
                raise ValueError("invalid image config or layers")
            config = members.get(image["Config"])
            if config is None or not config.isfile() or config.size > 1024 * 1024:
                raise ValueError("missing or oversized image config")
            config_bytes = archive.extractfile(config).read()
            if "sha256:" + hashlib.sha256(config_bytes).hexdigest() != expected[tags[0]]:
                raise ValueError("image config digest mismatch")
            config_data = json.loads(config_bytes)
            if config_data.get("os", "") + "/" + config_data.get("architecture", "") != metadata["platform"]:
                raise ValueError("image config platform mismatch")
            for layer in image["Layers"]:
                if not isinstance(layer, str) or layer not in members or not members[layer].isfile():
                    raise ValueError("image layer is missing")
            referenced = [image["Config"], *image["Layers"]]
            if any(name in ("manifest.json", "index.json", "oci-layout") for name in referenced):
                raise ValueError("reserved archive file used as image content")
            canonical.append({"Config": image["Config"], "RepoTags": tags, "Layers": image["Layers"]})
    config_names = {image["Config"] for image in canonical}
    layer_names = {name for image in canonical for name in image["Layers"]}
    if config_names & layer_names:
        raise ValueError("image config cannot also be a layer")
    return images, canonical


def canonicalize(bundle, manifest, destination):
    # Docker/containerd can prefer index.json over manifest.json in docker-save
    # archives. Rebuild a Docker-only archive from exactly the validated images.
    # Never extract contributor-controlled paths into the runner filesystem.
    with tarfile.open(bundle / "images.tar", "r:") as source, tarfile.open(destination, "w") as target:
        configs = {image["Config"] for image in manifest}
        layers = sorted({name for image in manifest for name in image["Layers"]})
        renamed = {name: f"layers/{index}/layer.tar" for index, name in enumerate(layers)}
        for name in configs:
            content = source.extractfile(name).read()
            renamed[name] = hashlib.sha256(content).hexdigest() + ".json"
        canonical = [{"Config": renamed[image["Config"]], "RepoTags": image["RepoTags"],
                      "Layers": [renamed[name] for name in image["Layers"]]} for image in manifest]
        encoded = json.dumps(canonical).encode()
        header = tarfile.TarInfo("manifest.json")
        header.size = len(encoded)
        target.addfile(header, io.BytesIO(encoded))
        written = set()
        for name, output_name in sorted(renamed.items()):
            if output_name in written:
                continue
            written.add(output_name)
            member = source.getmember(name)
            if not member.isfile():
                raise ValueError("image archive content is not a regular file")
            header = tarfile.TarInfo(output_name)
            header.size = member.size
            target.addfile(header, source.extractfile(member))


def load(bundle, sha, producer_attempt):
    images, manifest = verify(bundle, sha, producer_attempt)
    with tempfile.TemporaryDirectory(prefix="e2e-images-", dir=bundle.parent) as temporary:
        canonical = Path(temporary) / "images.tar"
        canonicalize(bundle, manifest, canonical)
        subprocess.run(["docker", "load", "--input", str(canonical)], check=True)
    for image in images.values():
        if command("docker", "image", "inspect", "--format", "{{.Id}}", image["ref"]) != image["id"]:
            raise ValueError("loaded image identity mismatch")
    print("Verified and loaded untrusted E2E images for " + sha)


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "build":
        build(Path(sys.argv[2]), sys.argv[3])
    elif len(sys.argv) == 5 and sys.argv[1] == "load":
        load(Path(sys.argv[2]), sys.argv[3], int(sys.argv[4]))
    else:
        raise ValueError("usage: e2e-images.py build DIR SHA | load DIR SHA PRODUCER_ATTEMPT")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, KeyError, tarfile.TarError, subprocess.CalledProcessError) as error:
        print(f"E2E image bundle: {error}", file=sys.stderr)
        sys.exit(1)
