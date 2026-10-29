"""Exercise OCI data verification and reject unsigned or substituted build declarations."""

import copy
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("artifacts", Path(__file__).with_name("qualification-artifacts.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def archive(path, architectures=("amd64", "arm64"), corrupt=False, duplicate_json=False, link=False):
    blobs = {}

    def blob(data):
        raw = MODULE.PLAN.canonical(data) if isinstance(data, dict) else data
        digest = "sha256:" + hashlib.sha256(raw).hexdigest()
        blobs["blobs/" + digest.replace(":", "/")] = raw
        return {"digest": digest, "size": len(raw)}

    manifests = []
    for architecture in architectures:
        config = {"os": "linux", "architecture": architecture, "created": "1970-01-01T00:00:01Z"}
        raw = MODULE.PLAN.canonical(config)
        if duplicate_json:
            raw = raw.replace(b'"os":"linux"', b'"os":"linux","os":"linux"')
        descriptor = blob({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                           "config": blob(raw), "layers": [blob(b"fixture-layer-" + architecture.encode())]})
        descriptor["platform"] = {"os": "linux", "architecture": architecture}
        manifests.append(descriptor)
    index = blob({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": manifests})
    blobs["index.json"] = MODULE.PLAN.canonical({"schemaVersion": 2, "manifests": [index]})
    blobs["oci-layout"] = b'{"imageLayoutVersion":"1.0.0"}'
    if corrupt:
        blobs[next(iter(blobs))] += b"changed"
    with tarfile.open(path, "w") as bundle:
        for name, data in blobs.items():
            member = tarfile.TarInfo(name)
            member.size = len(data)
            bundle.addfile(member, io.BytesIO(data))
        if link:
            member = tarfile.TarInfo("unsafe-link")
            member.type = tarfile.SYMTYPE
            member.linkname = "../../outside"
            bundle.addfile(member)


class ArtifactDataTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.archive = self.root / "init.tar"
        archive(self.archive)
        self.plan = {"schemaVersion": 1, "claim": MODULE.PLAN.CLAIM, "publicationAuthorized": False,
                     "runtimeQualificationVerified": False, "artifactPrefix": "release-qualification-42-1",
                     "sources": {"candidateSHA": "a" * 40, "tree": "b" * 40, "sourceDateEpoch": 1,
                                 "controllerSHA": "c" * 40},
                     "context": {"runID": 42, "attempt": 1, "sourceSHA": "c" * 40},
                     "inputs": {"packages": {role: f"ghcr.io/kubebao/openbao-{role}-qualification"
                                             for role in MODULE.ROLES}}}

    def test_actual_dual_platform_image_bytes_and_epoch_are_checked(self):
        record = MODULE.image_record(self.plan, "primary", "init", self.archive)
        self.assertEqual(set(record["platforms"]), {"linux/amd64", "linux/arm64"})
        self.assertEqual(record["sourceDateEpoch"], 1)
        self.assertEqual(record["claim"], "unqualified-candidate-build-data")
        changed = copy.deepcopy(self.plan)
        changed["sources"]["sourceDateEpoch"] += 1
        with self.assertRaises(ValueError):
            MODULE.image_record(changed, "primary", "init", self.archive)

    def test_corruption_duplicate_json_missing_platform_and_links_fail(self):
        for options in ({"corrupt": True}, {"duplicate_json": True}, {"architectures": ("amd64",)}, {"link": True}):
            archive(self.archive, **options)
            with self.subTest(options=options), self.assertRaises(ValueError):
                MODULE.image_bytes(self.archive)

    def test_receipt_cannot_substitute_source_build_role_or_bytes(self):
        record = MODULE.image_record(self.plan, "primary", "init", self.archive)
        receipt = self.root / "init.json"
        MODULE.PLAN.write_json(receipt, record)
        self.assertEqual(MODULE.verify_image(self.plan, "primary", "init", self.root), record)
        for field, value in (("sourceSHA", "d" * 40), ("build", "rebuild"), ("role", "backup"),
                             ("digest", "sha256:" + "e" * 64), ("planSHA256", "f" * 64)):
            receipt.write_text(json.dumps({**record, field: value}))
            with self.subTest(field=field), self.assertRaises(ValueError):
                MODULE.verify_image(self.plan, "primary", "init", self.root)

    def test_raw_bounds_precede_tar_parser_materialization(self):
        compressed = self.root / "compressed.tar.gz"
        compressed.write_bytes(gzip.compress(self.archive.read_bytes()))
        with patch.object(MODULE.tarfile, "open") as parser, self.assertRaises((ValueError, tarfile.TarError)):
            MODULE.image_bytes(compressed)
        parser.assert_not_called()
        excessive = self.root / "excessive.tar"
        header = tarfile.TarInfo("blobs")
        header.type = tarfile.DIRTYPE
        excessive.write_bytes(header.tobuf() * 4097 + bytes(1024))
        with patch.object(MODULE.tarfile, "open") as parser, self.assertRaises(ValueError):
            MODULE.image_bytes(excessive)
        parser.assert_not_called()
        extended = self.root / "extended.tar"
        header = tarfile.TarInfo("extended")
        header.type = tarfile.XHDTYPE
        header.size = 256 * 1024 * 1024
        extended.write_bytes(header.tobuf() + bytes(1024))
        with patch.object(MODULE.tarfile, "open") as parser, self.assertRaises(ValueError):
            MODULE.image_bytes(extended)
        parser.assert_not_called()

    def test_plan_must_match_trusted_output_and_current_execution(self):
        path = self.root / "plan.json"
        MODULE.PLAN.write_json(path, self.plan)
        with patch.dict(os.environ, {"QUALIFICATION_PLAN_SHA256": MODULE.PLAN.fingerprint(self.plan)}), \
                patch.object(MODULE.PLAN, "context", return_value=self.plan["context"]):
            self.assertEqual(MODULE.load_plan(path), self.plan)
            changed = copy.deepcopy(self.plan)
            changed["context"]["attempt"] += 1
            path.write_text(json.dumps(changed))
            with self.assertRaises(ValueError):
                MODULE.load_plan(path)

    def test_sparse_logical_size_and_contiguous_type_are_rejected_before_parsing(self):
        sparse = self.root / "sparse.tar"
        member = tarfile.TarInfo("blobs/sha256/" + "a" * 64)
        member.type = tarfile.GNUTYPE_SPARSE
        raw = bytearray(member.tobuf(format=tarfile.GNU_FORMAT))
        logical_size = 4 * 1024 * 1024 * 1024
        raw[483:495] = f"{logical_size:011o}\0".encode()
        raw[148:156] = b" " * 8
        raw[148:156] = f"{sum(raw):06o}\0 ".encode()
        sparse.write_bytes(raw + bytes(1024))
        with tarfile.open(sparse, mode="r:") as parsed:
            self.assertEqual(parsed.next().size, logical_size)
        with patch.object(MODULE.tarfile, "open") as parser, self.assertRaises(ValueError):
            MODULE.image_bytes(sparse)
        parser.assert_not_called()
        member.type = tarfile.CONTTYPE
        sparse.write_bytes(member.tobuf() + bytes(1024))
        with patch.object(MODULE.tarfile, "open") as parser, self.assertRaises(ValueError):
            MODULE.image_bytes(sparse)
        parser.assert_not_called()

    def test_import_rejects_public_missing_or_substituted_packages_before_writes(self):
        from types import SimpleNamespace
        valid = {"name": "openbao-operator-qualification", "package_type": "container",
                 "visibility": "private", "owner": {"login": "kubebao"}}
        for changed in ({**valid, "visibility": "public"}, {**valid, "name": "openbao-operator"},
                        {**valid, "owner": {"login": "other"}}):
            with patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                returncode=0, stdout=json.dumps(changed).encode())), self.assertRaises(ValueError):
                MODULE.private_packages(self.plan)
        with patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
            returncode=1, stdout=b"")), self.assertRaises(ValueError):
            MODULE.private_packages(self.plan)

    def test_import_requires_authenticated_plan_before_any_registry_operation(self):
        with patch.dict(os.environ, {"GITHUB_JOB": "import"}), \
                patch.object(MODULE.ORIGIN, "download", side_effect=ValueError("wrong producing attempt")), \
                patch.object(MODULE.subprocess, "run") as run, self.assertRaises(ValueError):
            MODULE.import_images(self.plan, self.root / "import")
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
