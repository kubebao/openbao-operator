"""Reject artifact and producing-attempt substitution before trusted qualification."""

import copy
import datetime
import hashlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
import warnings
import zipfile


SPEC = importlib.util.spec_from_file_location("qualification_origin", Path(__file__).with_name("qualification-origin.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class OriginTest(unittest.TestCase):
    def setUp(self):
        self.context = {"runID": 42, "attempt": 2, "sourceSHA": "a" * 40}
        self.now = datetime.datetime(2026, 10, 28, 12, 0, tzinfo=datetime.timezone.utc)
        self.name = "release-qualification-42-2-primary-image-init"
        self.job_name = "Build (primary / init)"
        self.run = {"id": 42, "run_attempt": 2, "head_sha": "a" * 40, "head_branch": "main",
                    "event": "workflow_dispatch", "path": MODULE.WORKFLOW, "status": "in_progress",
                    "run_started_at": "2026-10-28T10:00:00Z",
                    "repository": {"id": MODULE.REPOSITORY_ID, "full_name": MODULE.REPOSITORY}}
        self.job = {"id": 100, "name": self.job_name, "run_id": 42, "run_attempt": 2, "head_sha": "a" * 40,
                    "status": "completed", "conclusion": "success", "started_at": "2026-10-28T10:01:00Z",
                    "completed_at": "2026-10-28T11:00:00Z"}
        self.artifact = {"id": 200, "name": self.name, "expired": False, "expires_at": "2026-11-28T11:00:00Z",
                         "created_at": "2026-10-28T10:59:00Z", "size_in_bytes": 123, "digest": "sha256:" + "b" * 64,
                         "workflow_run": {"id": 42, "repository_id": MODULE.REPOSITORY_ID,
                                          "head_repository_id": MODULE.REPOSITORY_ID,
                                          "head_sha": "a" * 40, "head_branch": "main"}}

    def verify(self, run=None, job=None, artifact=None):
        return MODULE.origin(run or self.run, [job or self.job], [artifact or self.artifact],
                             self.context, self.name, self.job_name, self.now)

    def test_current_successful_job_and_immutable_artifact_are_accepted(self):
        self.assertEqual(self.verify(), (self.job, self.artifact))

    def test_wrong_run_attempt_source_or_failed_job_is_rejected(self):
        for field, value in (("run_id", 43), ("run_attempt", 1), ("head_sha", "c" * 40),
                             ("conclusion", "failure"), ("status", "in_progress"), ("name", "Other job")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify(job={**self.job, field: value})
        for field, value in (("id", 43), ("run_attempt", 1), ("head_sha", "c" * 40),
                             ("event", "pull_request"), ("path", "other.yml"), ("head_branch", "other")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify(run={**self.run, field: value})

    def test_cloned_previous_attempt_success_cannot_claim_fresh_execution(self):
        job = {**self.job, "started_at": "2026-10-28T09:00:00Z", "completed_at": "2026-10-28T09:30:00Z"}
        with self.assertRaises(ValueError):
            self.verify(job=job)

    def test_expired_missing_digest_wrong_artifact_origin_and_creation_time_are_rejected(self):
        changes = (("expired", True), ("expires_at", "2026-10-28T11:00:00Z"), ("digest", None),
                   ("size_in_bytes", MODULE.MAX_BYTES + 1), ("created_at", "2026-10-28T09:59:00Z"),
                   ("created_at", "2026-10-28T11:01:00Z"), ("name", self.name.replace("-2-", "-1-")))
        for field, value in changes:
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify(artifact={**self.artifact, field: value})
        for field, value in (("id", 43), ("repository_id", 1), ("head_repository_id", 1), ("head_sha", "c" * 40)):
            artifact = copy.deepcopy(self.artifact)
            artifact["workflow_run"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify(artifact=artifact)

    def test_duplicate_job_or_artifact_selection_is_rejected(self):
        for jobs, artifacts in (([self.job] * 2, [self.artifact]), ([self.job], [self.artifact] * 2)):
            with self.assertRaises(ValueError):
                MODULE.origin(self.run, jobs, artifacts, self.context, self.name, self.job_name, self.now)

    def archive(self, members):
        stream = io.BytesIO()
        with warnings.catch_warnings(), zipfile.ZipFile(stream, "w") as archive:
            warnings.simplefilter("ignore", UserWarning)
            for name, data in members:
                archive.writestr(name, data)
        return stream.getvalue()

    def test_archive_digest_and_exact_plain_member_set_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            archive = output / "input.zip"
            valid = self.archive([("init.json", b"{}"), ("init.tar", b"fixture")])
            archive.write_bytes(valid)
            expected = {"size_in_bytes": len(valid), "digest": "sha256:" + hashlib.sha256(valid).hexdigest()}
            MODULE.unpack(archive, expected, {"init.json", "init.tar"}, output)
            self.assertEqual((output / "init.tar").read_bytes(), b"fixture")
            archive.write_bytes(valid + b"changed")
            with self.assertRaises(ValueError):
                MODULE.unpack(archive, expected, {"init.json", "init.tar"}, output)
            for members in (([('../escape', b'data')]), ([('extra', b'data')]),
                            [('init.json', b'{}'), ('init.json', b'duplicate')]):
                raw = self.archive(members)
                archive.write_bytes(raw)
                expected = {"size_in_bytes": len(raw), "digest": "sha256:" + hashlib.sha256(raw).hexdigest()}
                with self.assertRaises(ValueError):
                    MODULE.unpack(archive, expected, {"init.json", "init.tar"}, output)

    def test_links_and_non_uploader_compression_are_rejected_before_extraction(self):
        link = zipfile.ZipInfo("init.tar")
        link.create_system = 3
        link.external_attr = 0o120777 << 16
        compressed = zipfile.ZipInfo("init.tar")
        compressed.compress_type = zipfile.ZIP_LZMA
        for entry in (link, compressed):
            with tempfile.TemporaryDirectory() as directory:
                output = Path(directory)
                raw = self.archive([("init.json", b"{}"), (entry, b"fixture")])
                archive = output / "input.zip"
                archive.write_bytes(raw)
                metadata = {"size_in_bytes": len(raw), "digest": "sha256:" + hashlib.sha256(raw).hexdigest()}
                with self.assertRaises(ValueError):
                    MODULE.unpack(archive, metadata, {"init.json", "init.tar"}, output)
                self.assertFalse((output / "init.json").exists())
                self.assertFalse((output / "init.tar").exists())


if __name__ == "__main__":
    unittest.main()
