#!/usr/bin/env python3
"""Local policy tests; synthetic verifier output is not cryptographic evidence."""

import argparse
import copy
import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile


spec = importlib.util.spec_from_file_location("identity", Path(__file__).with_name("release-identity.py"))
identity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(identity)
SHA = "a" * 40


class IdentityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.now = datetime.datetime(2030, 1, 1, tzinfo=datetime.timezone.utc)
        self.run, self.attempt, self.artifact_id, self.job_id = 123, 2, 456, 789
        self.record = identity.probe(SHA, self.run, self.attempt, self.job_id)
        self.subject = (json.dumps(self.record, sort_keys=True, indent=2) + "\n").encode()
        self.data = self.archive({"probe.json": self.subject, "bundle.json": b"synthetic bundle"})
        self.run_data = {"id": self.run, "run_attempt": self.attempt, "head_sha": SHA, "head_branch": "main",
                         "path": identity.PRODUCER, "event": "workflow_dispatch",
                         "status": "completed", "conclusion": "success",
                         "repository": {"id": identity.REPOSITORY_ID, "full_name": identity.REPOSITORY,
                                        "owner": {"id": identity.OWNER_ID}}}
        self.jobs = [{"id": self.job_id, "name": identity.JOB, "run_id": self.run,
                      "run_attempt": self.attempt, "head_sha": SHA, "status": "completed", "conclusion": "success"}]
        self.artifact = {"id": self.artifact_id, "name": f"release-identity-{self.run}-{self.attempt}",
                         "expired": False, "expires_at": "2099-01-01T00:00:00Z", "size_in_bytes": len(self.data),
                         "digest": "sha256:" + hashlib.sha256(self.data).hexdigest(),
                         "workflow_run": {"id": self.run, "repository_id": identity.REPOSITORY_ID,
                                          "head_repository_id": identity.REPOSITORY_ID,
                                          "head_sha": SHA, "head_branch": "main"}}
        self.extensions = {
            "issuer": "https://token.actions.githubusercontent.com", "runnerEnvironment": "github-hosted",
            "sourceRepositoryURI": identity.ROOT, "sourceRepositoryIdentifier": str(identity.REPOSITORY_ID),
            "sourceRepositoryOwnerIdentifier": str(identity.OWNER_ID), "sourceRepositoryDigest": SHA,
            "sourceRepositoryRef": identity.REF, "buildSignerURI": f"{identity.ROOT}/{identity.PRODUCER}@{identity.REF}",
            "buildSignerDigest": SHA, "buildConfigURI": f"{identity.ROOT}/{identity.PRODUCER}@{identity.REF}",
            "buildConfigDigest": SHA, "buildTrigger": "workflow_dispatch",
            "runInvocationURI": f"{identity.ROOT}/actions/runs/{self.run}/attempts/{self.attempt}",
        }
        self.digest = hashlib.sha256(self.subject).hexdigest()
        # gh v2.96.0 uses sigstore-go v1.2.1 certificate.Summary, which flattens its embedded Extensions.
        # https://github.com/sigstore/sigstore-go/blob/v1.2.1/pkg/fulcio/certificate/summarize.go
        self.verified = [{"verificationResult": {"signature": {"certificate": self.extensions},
                          "verifiedTimestamps": [{"type": "synthetic"}],
                          "statement": {"predicateType": "https://slsa.dev/provenance/v1",
                                        "subject": [{"name": "probe.json", "digest": {"sha256": self.digest}}]}}}]
        self.env = {
            "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com",
            "GITHUB_REPOSITORY": identity.REPOSITORY, "GITHUB_REPOSITORY_ID": str(identity.REPOSITORY_ID),
            "GITHUB_REPOSITORY_OWNER_ID": str(identity.OWNER_ID), "GITHUB_REF": identity.REF,
            "GITHUB_SHA": SHA, "GITHUB_WORKFLOW_SHA": SHA,
            "GITHUB_WORKFLOW_REF": f"{identity.REPOSITORY}/{identity.CONSUMER}@{identity.REF}",
            "GITHUB_EVENT_NAME": "workflow_dispatch", "RUNNER_ENVIRONMENT": "github-hosted",
            "GITHUB_RUN_ID": str(self.run), "GITHUB_RUN_ATTEMPT": str(self.attempt), "GITHUB_JOB": "produce",
        }

    @staticmethod
    def archive(files):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            for name, value in files.items():
                archive.writestr(name, value)
        return stream.getvalue()

    def origin(self, **changes):
        values = dict(run=self.run_data, artifact=self.artifact, job=self.jobs, controller=SHA,
                      run_id=self.run, attempt=self.attempt, artifact_id=self.artifact_id, now=self.now)
        values.update(changes)
        return identity.origin(**values)

    def test_origin_binds_the_producing_execution(self):
        self.assertEqual(self.origin(), self.job_id)
        for field, value in [("id", self.run + 1), ("run_attempt", self.attempt + 1), ("head_sha", "b" * 40),
                             ("path", identity.CONSUMER), ("event", "pull_request"), ("conclusion", "failure")]:
            with self.subTest(field=field):
                altered = copy.deepcopy(self.run_data)
                altered[field] = value
                with self.assertRaises(ValueError):
                    self.origin(run=altered)
        changed = copy.deepcopy(self.run_data)
        changed["repository"]["id"] += 1
        with self.assertRaises(ValueError):
            self.origin(run=changed)

    def test_artifact_origin_expiry_and_selection(self):
        for field, value in [("id", self.artifact_id + 1), ("name", "other"), ("expired", True),
                             ("expires_at", "2029-01-01T00:00:00Z"), ("digest", None), ("size_in_bytes", True)]:
            with self.subTest(field=field):
                changed = copy.deepcopy(self.artifact)
                changed[field] = value
                with self.assertRaises(ValueError):
                    self.origin(artifact=changed)
        changed = copy.deepcopy(self.artifact)
        changed["workflow_run"]["head_repository_id"] += 1
        with self.assertRaises(ValueError):
            self.origin(artifact=changed)

    def test_cloned_or_additional_jobs_are_rejected(self):
        with self.assertRaises(ValueError):
            self.origin(job=self.jobs * 2)
        changed = copy.deepcopy(self.jobs)
        changed[0]["run_attempt"] -= 1
        with self.assertRaises(ValueError):
            self.origin(job=changed)

    def test_certificate_fields_are_bound_after_verification(self):
        identity.verified_identity(self.verified, SHA, self.run, self.attempt, self.digest)
        for field in self.extensions:
            with self.subTest(field=field):
                changed = copy.deepcopy(self.verified)
                changed[0]["verificationResult"]["signature"]["certificate"][field] = "wrong"
                with self.assertRaises(ValueError):
                    identity.verified_identity(changed, SHA, self.run, self.attempt, self.digest)

    def test_signed_subject_and_timestamp_are_required(self):
        for field, value in [("verifiedTimestamps", []), ("statement", {"predicateType": "wrong", "subject": []})]:
            changed = copy.deepcopy(self.verified)
            changed[0]["verificationResult"][field] = value
            with self.assertRaises(ValueError):
                identity.verified_identity(changed, SHA, self.run, self.attempt, self.digest)
        with self.assertRaises(ValueError):
            identity.verified_identity(self.verified, SHA, self.run, self.attempt, "0" * 64)

    def test_pinned_certificate_shape_is_flat(self):
        # This archived upstream verification guards the fixture shape independently of our synthetic values.
        path = Path(__file__).with_name("testdata") / "release-identity-gh-2.96-shape.json"
        actual = json.loads(path.read_text())["verificationResult"]["signature"]["certificate"]
        self.assertNotIn("extensions", actual)
        self.assertTrue(all(field in actual for field in self.extensions))
        nested = copy.deepcopy(self.verified)
        nested[0]["verificationResult"]["signature"]["certificate"] = {"extensions": self.extensions}
        with self.assertRaises(ValueError):
            identity.verified_identity(nested, SHA, self.run, self.attempt, self.digest)
        identity.verified_identity(self.verified, SHA, self.run, self.attempt, self.digest)

    def test_verifier_command_pins_all_crypto_policy_and_offline_root(self):
        command = identity.verify_command("subject", "bundle", SHA, "root")
        expected = ["--hostname", "github.com", "--custom-trusted-root", "root", "--repo", identity.REPOSITORY,
                    "--deny-self-hosted-runners", "--source-ref", identity.REF,
                    "--source-digest", "--signer-digest", "--cert-identity", "--cert-oidc-issuer"]
        self.assertTrue(all(item in command for item in expected))
        self.assertEqual(command.count(SHA), 2)

    def test_untrusted_contexts_stop_before_github_calls(self):
        with patch.dict(os.environ, self.env, clear=True):
            identity.context(SHA, identity.CONSUMER)
            for name in ["GITHUB_REPOSITORY_ID", "GITHUB_REF", "GITHUB_WORKFLOW_SHA", "GITHUB_WORKFLOW_REF",
                         "GITHUB_EVENT_NAME", "RUNNER_ENVIRONMENT"]:
                with self.subTest(name=name), patch.dict(os.environ, {name: "wrong"}):
                    with self.assertRaises(ValueError):
                        identity.context(SHA, identity.CONSUMER)

    def test_archive_bytes_members_and_expansion_are_checked(self):
        identity.unpack(self.data, self.artifact, self.root)
        self.assertEqual((self.root / "probe.json").read_bytes(), self.subject)
        with self.assertRaises(ValueError):
            identity.unpack(self.data + b" ", self.artifact, self.root)
        for files in [{"../probe.json": b"bad", "bundle.json": b"bad"},
                      {"probe.json": b"bad", "bundle.json": b"bad", "other": b"bad"},
                      {"probe.json": b"x" * (identity.LIMIT + 1), "bundle.json": b""}]:
            data = self.archive(files)
            artifact = {**self.artifact, "size_in_bytes": len(data), "digest": "sha256:" + hashlib.sha256(data).hexdigest()}
            with self.assertRaises(ValueError):
                identity.unpack(data, artifact, self.root)

    def test_duplicate_json_and_identifiers_fail_closed(self):
        with self.assertRaises(ValueError):
            identity.decode(b'{"x":1,"x":2}')
        for value in ["0", "-1", "1.0", "1/../../", " 1", True]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                identity.number(value)

    def test_producer_has_no_candidate_input_and_preserves_output(self):
        self.env["GITHUB_WORKFLOW_REF"] = f"{identity.REPOSITORY}/{identity.PRODUCER}@{identity.REF}"
        self.jobs[0]["status"] = "in_progress"
        output = self.root / "producer"
        with patch.dict(os.environ, self.env, clear=True), patch.object(identity, "jobs", return_value=self.jobs):
            identity.produce(argparse.Namespace(controller=SHA, output=output))
            self.assertEqual((output / "probe.json").read_bytes(), self.subject)
            with self.assertRaises(FileExistsError):
                identity.produce(argparse.Namespace(controller=SHA, output=output))

    def consume(self, fail_verification=False):
        def fake_api(path):
            return self.artifact if path.startswith("actions/artifacts/") else self.run_data

        def fake_cli(*args):
            if args[0] == "api":
                return self.data
            if args[0] == "--version":
                return b"gh version 2.96.0\n"
            if args[1] == "trusted-root":
                return b"synthetic trusted root"
            if fail_verification or "0" * 40 in args or str(self.root / "consumer" / "changed-probe.json") in args:
                raise ValueError("synthetic cryptographic rejection")
            if f"{identity.ROOT}/{identity.CONSUMER}@{identity.REF}" in args:
                raise ValueError("synthetic identity rejection")
            return json.dumps(self.verified).encode()

        output = self.root / "consumer"
        args = argparse.Namespace(controller=SHA, run=str(self.run), attempt=str(self.attempt),
                                  artifact=str(self.artifact_id), output=output)
        with patch.dict(os.environ, self.env, clear=True), patch.object(identity, "api", side_effect=fake_api), \
                patch.object(identity, "jobs", return_value=self.jobs), patch.object(identity, "cli", side_effect=fake_cli):
            if fail_verification:
                with self.assertRaises(ValueError):
                    identity.consume(args)
            else:
                identity.consume(args)
        return json.loads((output / "report.json").read_text())

    def test_consumer_control_flow_never_authorizes_publication(self):
        report = self.consume()
        self.assertTrue(report["identityVerified"])
        self.assertTrue(report["authenticationPerformed"])
        self.assertFalse(report["publicationAuthorized"])
        self.assertFalse(report["runtimeQualificationVerified"])
        self.assertEqual(len(report["cryptographicRejections"]), 4)
        self.assertEqual(len(report["metadataRejections"]), 7)

    def test_crypto_failure_retains_a_non_authorizing_failure(self):
        report = self.consume(fail_verification=True)
        self.assertFalse(report["identityVerified"])
        self.assertFalse(report["authenticationPerformed"])
        self.assertFalse(report["publicationAuthorized"])
        self.assertEqual(report["phase"], "signature")
        for name in ["origin.json", "artifact.zip", "probe.json", "bundle.json", "trusted-root.jsonl"]:
            self.assertTrue((self.root / "consumer" / name).is_file())

    def test_expired_origin_retains_metadata_without_downloading(self):
        self.artifact["expired"] = True
        report = self.consume(fail_verification=True)
        self.assertEqual(report["phase"], "origin")
        self.assertEqual(report["reason"], "artifact expired")
        self.assertTrue((self.root / "consumer" / "origin.json").is_file())
        self.assertFalse((self.root / "consumer" / "artifact.zip").exists())

    def test_cli_failure_and_timeout_cannot_produce_verified_output(self):
        result = subprocess.CompletedProcess([], 1, b'[{"verificationResult":{}}]', b"private stderr")
        with patch.object(subprocess, "run", return_value=result):
            with self.assertRaisesRegex(ValueError, "GitHub CLI request or verification failed"):
                identity.cli("attestation", "verify")
        with patch.object(subprocess, "run", side_effect=subprocess.TimeoutExpired("gh", 120)):
            with self.assertRaises(subprocess.TimeoutExpired):
                identity.cli("api")

    def test_unexpected_negative_acceptance_fails(self):
        with self.assertRaisesRegex(ValueError, "unexpectedly accepted"):
            identity.rejection(lambda: None)


if __name__ == "__main__":
    unittest.main()
