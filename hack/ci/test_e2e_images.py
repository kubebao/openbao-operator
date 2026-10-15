#!/usr/bin/env python3
"""Exercise fork artifact identity, hostile archives, and local image preparation."""

import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("e2e_images", ROOT / "hack/ci/e2e-images.py")
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)
from e2e_artifacts import digest, run_identity, verify_identity


class ForkImages(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        self.sha = "a" * 40
        event = self.root / "event.json"
        event.write_text(json.dumps({"pull_request": {"head": {"sha": "b" * 40}}}))
        self.environment = {"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "owner/repo", "GITHUB_RUN_ID": "1234",
                            "GITHUB_RUN_ATTEMPT": "2", "GITHUB_SHA": self.sha, "GITHUB_EVENT_PATH": str(event)}
        scope = patch.dict(os.environ, self.environment)
        scope.start()
        self.addCleanup(scope.stop)
        self.files = {}
        self.manifest = []
        identity = run_identity(self.sha)
        refs = images.image_refs(identity, list(images.ROLES)[:4])
        self.records = {}
        for role, ref in refs.items():
            config = json.dumps({"os": "linux", "architecture": images.runner_platform().split("/")[1],
                                 "config": {"Env": ["ROLE=" + role]}, "rootfs": {"type": "layers", "diff_ids": []}}).encode()
            config_id = hashlib.sha256(config).hexdigest()
            path = "blobs/sha256/" + config_id
            self.files[path] = config
            self.manifest.append({"Config": path, "RepoTags": [ref], "Layers": []})
            self.records[role] = {"ref": ref, "id": "sha256:" + config_id}
        self.metadata = {"version": 1, "sourceSHA": self.sha, "workflow": identity,
                         "platform": images.runner_platform(), "images": self.records}
        self.write_bundle()

    def write_bundle(self, extra=None):
        with tarfile.open(self.bundle / "images.tar", "w") as archive:
            files = {**self.files, "manifest.json": json.dumps(self.manifest).encode(), **(extra or {})}
            for name, content in files.items():
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
        self.metadata["archiveSHA256"] = digest(self.bundle / "images.tar")
        self.write_metadata()

    def write_metadata(self):
        (self.bundle / "metadata.json").write_text(json.dumps(self.metadata))

    def test_identity_is_bound_to_run_repository_head_and_merge(self):
        images.verify(self.bundle, self.sha, 2)
        for field, value in {"repository": "other/repo", "runId": "9999", "headSHA": "c" * 40,
                             "testedSHA": "d" * 40, "buildAttempt": 3}.items():
            with self.subTest(field=field):
                original = copy.deepcopy(self.metadata)
                self.metadata["workflow"][field] = value
                self.write_metadata()
                with self.assertRaises(ValueError):
                    images.verify(self.bundle, self.sha, 2)
                self.metadata = original
                self.write_metadata()
        with self.assertRaises(ValueError):
            images.verify(self.bundle, "c" * 40, 2)

    def test_binary_bundle_uses_the_same_run_and_source_binding(self):
        spec = importlib.util.spec_from_file_location("binary_fixture", ROOT / "hack/ci/e2e-binaries.py")
        binaries = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(binaries)
        bundle = self.root / "binaries"
        bundle.mkdir()
        for name in binaries.BINARIES:
            (bundle / name).write_bytes(b"fixture")
        with patch.dict(os.environ, {"E2E_BUILD_GOOS": images.platform.system().lower(),
                                    "E2E_BUILD_GOARCH": images.runner_platform().split("/")[1],
                                    "E2E_BUILD_GOVERSION": binaries.versions()[0], "E2E_BUILD_CGO": "0"}):
            binaries.record(bundle, self.sha)
        binaries.verify(bundle, self.sha)
        with patch.dict(os.environ, {"GITHUB_RUN_ATTEMPT": "3"}):
            binaries.verify(bundle, self.sha)
        with patch.dict(os.environ, {"GITHUB_RUN_ID": "9999"}):
            with self.assertRaises(ValueError):
                binaries.verify(bundle, self.sha)

    def test_partial_rerun_reuses_only_selected_producer_attempt(self):
        with patch.dict(os.environ, {"GITHUB_RUN_ATTEMPT": "3"}):
            images.verify(self.bundle, self.sha, 2)
            with self.assertRaises(ValueError):
                images.verify(self.bundle, self.sha, 3)
        with patch.dict(os.environ, {"GITHUB_RUN_ATTEMPT": "1"}):
            with self.assertRaises(ValueError):
                images.verify(self.bundle, self.sha, 2)
        verify_identity(self.metadata["workflow"], self.sha)

    def test_bad_roles_refs_configs_platform_and_checksums_fail_before_load(self):
        for mutation in (lambda m: m["images"].pop("manager"),
                         lambda m: m["images"].update(unknown=m["images"]["manager"]),
                         lambda m: m["images"]["manager"].update(ref="ghcr.io/owner/trusted:main"),
                         lambda m: m["images"]["manager"].update(id="sha256:" + "0" * 64),
                         lambda m: m.update(platform="linux/unknown"),
                         lambda m: m.update(archiveSHA256="0" * 64)):
            original = copy.deepcopy(self.metadata)
            mutation(self.metadata)
            self.write_metadata()
            with patch.object(images.subprocess, "run") as docker:
                with self.assertRaises(ValueError):
                    images.load(self.bundle, self.sha, 2)
                docker.assert_not_called()
            self.metadata = original
            self.write_metadata()

    def test_seal_lane_requires_both_candidate_fixture_images(self):
        with patch.dict(os.environ, {"E2E_REQUIRE_SEAL_FIXTURES": "true"}), patch.object(images.subprocess, "run") as docker:
            with self.assertRaises(ValueError):
                images.load(self.bundle, self.sha, 2)
            docker.assert_not_called()

    def test_symlinks_traversal_aliases_and_duplicate_members_are_rejected(self):
        for name in ("../escape", "/absolute", "./index.json", "a//b"):
            with self.subTest(name=name):
                self.write_bundle({name: b"malicious"})
                with self.assertRaises(ValueError):
                    images.verify(self.bundle, self.sha, 2)
        self.write_bundle()
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.REGTYPE):
            with self.subTest(kind=kind):
                self.write_bundle()
                with tarfile.open(self.bundle / "images.tar", "a") as archive:
                    info = tarfile.TarInfo("manifest.json" if kind == tarfile.REGTYPE else "link")
                    info.type, info.linkname = kind, "../../escape"
                    archive.addfile(info)
                self.metadata["archiveSHA256"] = digest(self.bundle / "images.tar")
                self.write_metadata()
                with self.assertRaises(ValueError):
                    images.verify(self.bundle, self.sha, 2)
        self.write_bundle()
        metadata = self.bundle / "metadata.json"
        metadata.rename(self.root / "metadata.json")
        metadata.symlink_to(self.root / "metadata.json")
        with self.assertRaises(ValueError):
            images.verify(self.bundle, self.sha, 2)

    def test_divergent_oci_index_and_unreferenced_content_cannot_be_loaded(self):
        self.write_bundle({"oci-layout": b'{"imageLayoutVersion":"1.0.0"}',
                           "index.json": b'{"manifests":[{"annotations":{"io.containerd.image.name":"evil:main"}}]}',
                           "unreferenced": b"evil image"})
        observed = []
        def docker_load(args, **kwargs):
            self.assertEqual(args[:3], ["docker", "load", "--input"])
            with tarfile.open(args[3]) as archive:
                names = archive.getnames()
                self.assertNotIn("index.json", names)
                self.assertNotIn("oci-layout", names)
                self.assertNotIn("unreferenced", names)
                manifest = json.load(archive.extractfile("manifest.json"))
                observed.extend(row["RepoTags"][0] for row in manifest)
                self.assertEqual(set(names), {"manifest.json", *(row["Config"] for row in manifest)})
        def inspect(*args):
            return next(record["id"] for record in self.records.values() if record["ref"] == args[-1])
        with patch.object(images.subprocess, "run", side_effect=docker_load), patch.object(images, "command", side_effect=inspect):
            images.load(self.bundle, self.sha, 2)
        self.assertEqual(set(observed), {record["ref"] for record in self.records.values()})
        with patch.object(images.subprocess, "run"), patch.object(images, "command", return_value="sha256:" + "0" * 64):
            with self.assertRaises(ValueError):
                images.load(self.bundle, self.sha, 2)

    def test_candidate_preparation_never_pulls_or_rebuilds_missing_artifact_images(self):
        action = (ROOT / ".github/actions/prepare-e2e-lane/action.yml").read_text()
        script = action.split("      run: |\n", 1)[1].split("    - name:", 1)[0]
        import textwrap
        script = textwrap.dedent(script)
        fake_bin = self.root / "bin"
        fake_bin.mkdir()
        docker = fake_bin / "docker"
        docker.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$DOCKER_LOG"\n'
                          'if [ "$1" = image ]; then exit "$INSPECT_STATUS"; fi\n'
                          'if [ "$1" = tag ]; then exit 0; fi\n'
                          'if [ "$1" = push ] && [ "$2" = 127.0.0.1:5000/local-image ]; then exit 0; fi\nexit 99\n')
        docker.chmod(0o755)
        env = {**os.environ, "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
               "DOCKER_LOG": str(self.root / "docker.log"), "CANDIDATE_TRANSPORT": "artifact",
               "MANAGER_IMAGE": "local-manager:run", "CONFIG_INIT_IMAGE": "local-init:run",
               "BACKUP_EXECUTOR_IMAGE": "local-backup:run", "UPGRADE_EXECUTOR_IMAGE": "local-upgrade:run",
               "HARDENED_UPGRADE_EXECUTOR_IMAGE": "", "LOAD_BACKUP_EXECUTOR_IMAGE": "true",
               "LOAD_UPGRADE_EXECUTOR_IMAGE": "true", "INSPECT_STATUS": "0"}
        passed = subprocess.run(["bash", "-ec", script], env=env, capture_output=True, text=True)
        self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
        failed = subprocess.run(["bash", "-ec", script], env={**env, "INSPECT_STATUS": "1"}, capture_output=True)
        self.assertNotEqual(failed.returncode, 0)
        self.assertNotIn("pull", (self.root / "docker.log").read_text())
        upgrade = (ROOT / "hack/ci/operator-upgrade-e2e.sh").read_text()
        function = upgrade.split("prepare_candidate_image() {", 1)[1].split("\n}\n", 1)[0]
        invoke = 'registry_port=5000\nprepare_candidate_image() {' + function + '\n}\nprepare_candidate_image "$SOURCE" local-image unused-target'
        for source, expected in (("local-manager:run", 0), ("", 1)):
            result = subprocess.run(["bash", "-ec", invoke], env={**env, "SOURCE": source,
                                    "OPERATOR_UPGRADE_E2E_IMAGE_TRANSPORT": "artifact", "DOCKER_BIN": str(docker)},
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_normal_build_entrypoint_keeps_checkout_clean_without_bytecode_environment(self):
        repo = self.root / "repo"
        scripts = repo / "hack/ci"
        scripts.mkdir(parents=True)
        for name in ("e2e-images.py", "e2e_artifacts.py"):
            shutil.copyfile(ROOT / "hack/ci" / name, scripts / name)
        def git(*args):
            return subprocess.check_output(["git", *args], cwd=repo, text=True).strip()
        git("init", "--quiet")
        git("add", ".")
        git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture",
            "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
        sha = git("rev-parse", "HEAD")
        fake_bin = self.root / "fake-bin"
        fake_bin.mkdir()
        docker = fake_bin / "docker"
        docker.write_text('#!/usr/bin/env python3\nimport json,os,shutil,sys\n'
                          'if sys.argv[1] == "image":\n'
                          ' records=json.loads(os.environ["IMAGE_RECORDS"])\n'
                          ' print(next(r["id"] for r in records.values() if r["ref"] == sys.argv[-1]))\n'
                          'elif sys.argv[1] == "save": shutil.copyfile(os.environ["IMAGE_ARCHIVE"],sys.argv[3])\n')
        docker.chmod(0o755)
        env = {**os.environ, "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"], "GITHUB_SHA": sha,
               "GITHUB_OUTPUT": str(self.root / "outputs"), "IMAGE_RECORDS": json.dumps(self.records),
               "IMAGE_ARCHIVE": str(self.bundle / "images.tar"), "BUILD_SEAL_FIXTURES": "false"}
        env.pop("PYTHONDONTWRITEBYTECODE", None)
        result = subprocess.run([sys.executable, "hack/ci/e2e-images.py", "build", str(self.root / "built"), sha],
                                cwd=repo, env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(git("status", "--porcelain=v1"), "")
        self.assertEqual(list(scripts.rglob("*.pyc")), [])


if __name__ == "__main__":
    unittest.main()
