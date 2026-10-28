#!/usr/bin/env python3
"""Exercise real Git identities, OCI byte verification, and pilot rejection paths."""

import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("release_pilot", ROOT / "hack/ci/release-pilot.py")
pilot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pilot)


def fixture_plan():
    return {"schema": 1, "claim": pilot.CLAIM, "context": {"runID": "fixture"},
            "sources": {"candidate": "a" * 40, "merged": "b" * 40}, "reviewedBase": "c" * 40,
            "tree": "d" * 40, "version": "0.6.0", "sourceDateEpoch": 1, "helmVersion": "v4.2.3",
            "platforms": sorted(pilot.PLATFORMS), "inputs": json.loads((ROOT / "hack/ci/release-pilot-inputs.json").read_text())}


def oci_archive(path, architectures=("amd64", "arm64"), corrupt=False):
    blobs = {}

    def blob(data):
        digest = "sha256:" + hashlib.sha256(data).hexdigest()
        blobs["blobs/" + digest.replace(":", "/")] = data
        return {"digest": digest, "size": len(data)}

    manifests = []
    for architecture in architectures:
        config = blob(pilot.canonical({"os": "linux", "architecture": architecture, "created": "1970-01-01T00:00:01Z"}))
        layer = blob(b"fixture layer " + architecture.encode())
        descriptor = blob(pilot.canonical({"config": config, "layers": [layer]}))
        descriptor["platform"] = {"os": "linux", "architecture": architecture}
        manifests.append(descriptor)
    root = blob(pilot.canonical({"manifests": manifests}))
    if corrupt:
        blobs[next(iter(blobs))] += b"corrupt"
    blobs["index.json"] = pilot.canonical({"manifests": [root]})
    with tarfile.open(path, "w") as archive:
        for name, data in blobs.items():
            member = tarfile.TarInfo(name)
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))


class SourceIdentity(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.run_git("init", "-b", "main")
        self.run_git("config", "user.email", "pilot@example.invalid")
        self.run_git("config", "user.name", "Pilot fixture")
        chart = self.directory / "charts/openbao-operator/Chart.yaml"
        chart.parent.mkdir(parents=True)
        chart.write_text("version: 0.6.0\nappVersion: 0.6.0\n")
        tools = self.directory / "hack/dev/tool-versions.env"
        tools.parent.mkdir(parents=True)
        tools.write_text("HELM_VERSION=v4.2.3\n")
        self.commit("base", 1000000000)
        self.base = self.run_git("rev-parse", "HEAD")
        self.run_git("checkout", "-b", "candidate")
        (self.directory / "payload").write_text("candidate content\n")
        self.commit("candidate", 1000000100)
        self.candidate = self.run_git("rev-parse", "HEAD")
        self.run_git("checkout", "main")
        self.run_git("merge", "--squash", "candidate")
        self.commit("squash", 1000000300)
        self.merged = self.run_git("rev-parse", "HEAD")
        self.run_git("update-ref", "refs/remotes/origin/main", self.merged)

    def run_git(self, *args, env=None):
        return subprocess.check_output(["git", "-C", str(self.directory), *args], text=True,
                                       env={**os.environ, **(env or {})}, stderr=subprocess.DEVNULL).strip()

    def commit(self, message, timestamp):
        self.run_git("add", ".")
        dates = {"GIT_AUTHOR_DATE": f"{timestamp} +0000", "GIT_COMMITTER_DATE": f"{timestamp} +0000"}
        self.run_git("commit", "-m", message, env=dates)

    def test_actual_squash_freezes_candidate_epoch(self):
        plan = pilot.prepare(self.directory, self.candidate, self.merged, {"runID": "test"})
        self.assertNotEqual(self.candidate, self.merged)
        self.assertEqual(plan["sourceDateEpoch"], 1000000100)
        self.assertEqual(plan["reviewedBase"], self.base)
        pilot.verify_source(plan, "merged", self.directory)
        (self.directory / "payload").write_text("dirty source")
        with self.assertRaisesRegex(ValueError, "tracked changes"):
            pilot.verify_source(plan, "merged", self.directory)

    def test_changed_tree_and_non_main_source_are_rejected(self):
        with self.assertRaises(ValueError):
            pilot.prepare(self.directory, self.base, self.merged, {})
        self.run_git("update-ref", "refs/remotes/origin/main", self.base)
        with self.assertRaises(subprocess.CalledProcessError):
            pilot.prepare(self.directory, self.candidate, self.merged, {})

    def test_branch_names_and_identical_commits_are_rejected(self):
        for value in ["main", "--upload-pack=unexpected", "a" * 39, "A" * 40, "a" * 40 + "\n"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                pilot.require_sha(value)
        with self.assertRaises(ValueError):
            pilot.prepare(self.directory, self.merged, self.merged, {})


class ArtifactIdentity(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.plan = fixture_plan()
        self.images = {}
        self.files = {}
        for label in ("candidate", "merged"):
            images, files = self.directory / label / "images", self.directory / label / "files"
            images.mkdir(parents=True)
            files.mkdir()
            self.images[label], self.files[label] = images, files
            for image in pilot.IMAGES:
                archive = images / f"{image}.tar"
                oci_archive(archive)
                pilot.write_json(images / f"{image}.json", pilot.image_receipt(self.plan, label, image, archive))
            for name in pilot.release_files(self.plan):
                (files / name).write_text("fixture " + name)
            pilot.write_json(files / "files.json", pilot.record_files(self.plan, label, images, files))

    def test_positive_comparison_and_real_byte_gate_negative_controls(self):
        result = pilot.compare(self.plan, self.images["candidate"], self.images["merged"],
                               self.files["candidate"], self.files["merged"], self.directory / "comparison")
        self.assertTrue(result["passed"])
        self.assertFalse(result["publicationAuthorized"])
        self.assertEqual(len(result["byteGate"]), 4)
        self.assertEqual(len(result["identityRejections"]), 6)
        missing = (self.directory / "comparison/missing-file.log").read_text()
        self.assertIn("required file missing for reproducibility check: checksums.txt", missing)
        self.assertNotIn("byte mismatch", missing)
        self.assertNotIn("digest mismatch", missing)

    def test_corrupt_missing_and_duplicate_platforms_are_rejected(self):
        for platforms, corrupt in [(('amd64',), False), (('amd64', 'amd64'), False), (('amd64', 'arm64'), True)]:
            with self.subTest(platforms=platforms, corrupt=corrupt):
                archive = self.directory / "bad.tar"
                oci_archive(archive, platforms, corrupt)
                with self.assertRaises(ValueError):
                    pilot.inspect_image(archive)

    def test_archive_substitution_missing_receipt_and_changed_files_fail(self):
        label = "candidate"
        (self.images[label] / "operator.tar").write_bytes(b"not an OCI archive")
        with self.assertRaises(tarfile.TarError):
            pilot.verify_images(self.plan, label, self.images[label])
        oci_archive(self.images[label] / "operator.tar")
        (self.files[label] / "install.yaml").write_text("changed bytes")
        with self.assertRaises(ValueError):
            pilot.verify_files(self.plan, label, self.images[label], self.files[label])
        (self.images[label] / "init.json").unlink()
        with self.assertRaises(FileNotFoundError):
            pilot.verify_images(self.plan, label, self.images[label])

    def test_epoch_and_receipt_roles_are_checked(self):
        changed = copy.deepcopy(self.plan)
        changed["sourceDateEpoch"] += 1
        with self.assertRaisesRegex(ValueError, "timestamp"):
            pilot.image_receipt(changed, "candidate", "operator", self.images["candidate"] / "operator.tar")
        receipt = json.loads((self.images["candidate"] / "operator.json").read_text())
        with self.assertRaises(ValueError):
            pilot.check_identity(self.plan, "merged", receipt)
        changed["claim"] = "full-release-qualification"
        path = self.directory / "plan.json"
        pilot.write_json(path, changed)
        with self.assertRaises(ValueError):
            pilot.load_plan(path)

    def test_failed_comparison_preserves_a_failed_report(self):
        (self.files["merged"] / "checksums.txt").unlink()
        output = self.directory / "failed"
        with self.assertRaises(FileNotFoundError):
            pilot.compare(self.plan, self.images["candidate"], self.images["merged"],
                          self.files["candidate"], self.files["merged"], output)
        result = json.loads((output / "comparison.json").read_text())
        self.assertFalse(result["passed"])
        self.assertFalse(result["publicationAuthorized"])

    def test_other_run_and_tool_changes_are_rejected(self):
        path = self.directory / "plan.json"
        pilot.write_json(path, self.plan)
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}), self.assertRaises(ValueError):
            pilot.load_plan(path)
        changed = copy.deepcopy(self.plan)
        changed["inputs"]["syftVersion"] = "0.0.0"
        pilot.write_json(path, changed)
        with self.assertRaises(ValueError):
            pilot.load_plan(path)


if __name__ == "__main__":
    unittest.main()
