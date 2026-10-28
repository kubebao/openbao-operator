#!/usr/bin/env python3
"""Test advisory evidence bindings with real Git identities and changed bytes."""

import copy
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("release_evidence", ROOT / "hack/ci/release-evidence.py")
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)


class EvidenceContract(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Evidence fixture")
        self.git("config", "user.email", "evidence@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        chart = self.repo / "charts/openbao-operator/Chart.yaml"
        chart.parent.mkdir(parents=True)
        chart.write_text("version: 0.6.1\nappVersion: 0.6.1\n")
        (self.repo / ".release-please-manifest.json").write_text('{".":"0.6.1"}')
        self.commit("base", 1000000000)
        base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-b", "candidate")
        (self.repo / "source").write_text("candidate content\n")
        self.commit("candidate", 1000000100)
        candidate = self.git("rev-parse", "HEAD")
        self.git("checkout", "main")
        self.git("merge", "--squash", "candidate")
        self.commit("final", 1000000300)
        final = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", final)
        self.paths = {name: self.root / name for name in ("notes", "inputs", "plan", "payloads", "archives")}
        for name in ("notes", "inputs", "plan"):
            self.paths[name].write_text(name + "\n\n")
        for name in ("payloads", "archives"):
            self.paths[name].mkdir()
        files = {}
        for name in evidence.payload_names("0.6.1"):
            file = self.paths["payloads"] / name
            file.write_text("fixture " + name)
            files[name] = evidence.file_hash(file)
        identity = {"repository": evidence.REPOSITORY,
                    "sources": {"candidateSHA": candidate, "finalSHA": final, "baseSHA": base,
                                "tree": self.git("rev-parse", "HEAD^{tree}"), "sourceDateEpoch": 1000000100},
                    "release": {"version": "0.6.1", "bodySHA256": evidence.file_hash(self.paths["notes"])},
                    "inputsSHA256": evidence.file_hash(self.paths["inputs"]),
                    "testPlanSHA256": evidence.file_hash(self.paths["plan"]),
                    "images": {name: "sha256:" + "a" * 64 for name in evidence.IMAGES}, "files": files}
        identity_hash = evidence.fingerprint(identity)
        records = {}
        for index, role in enumerate(sorted(evidence.ROLES), 1):
            source = candidate if role == "qualification" else final
            producer = {"repository": evidence.REPOSITORY, "workflow": f".github/workflows/{role.lower()}.yml",
                        "sourceSHA": base if role == "qualification" else final, "signerSHA": base,
                        "runID": index, "jobID": index * 10, "attempt": 1}
            archive = self.paths["archives"] / f"{index}.zip"
            archive.write_bytes(b"opaque fixture archive " + role.encode())
            artifacts = [{"id": index, "digest": "sha256:" + evidence.file_hash(archive),
                          "expiresAt": "2100-01-01T00:00:00Z"}]
            records[role] = {"identitySHA256": identity_hash, "sourceSHA": source,
                             "producer": producer, "artifacts": artifacts}
        self.manifest = {"schema": evidence.SCHEMA, "claim": evidence.CLAIM,
                         "identity": identity, "records": records}
        self.policy = {"schema": evidence.SCHEMA, "identitySHA256": identity_hash,
                       "records": {role: {key: copy.deepcopy(record[key]) for key in ("producer", "artifacts")}
                                   for role, record in records.items()}}
        self.now = datetime.datetime(2030, 1, 1, tzinfo=datetime.timezone.utc)

    def git(self, *args, env=None):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True,
                                       stderr=subprocess.DEVNULL, env={**os.environ, **(env or {})}).strip()

    def commit(self, message, epoch):
        self.git("add", ".")
        self.git("commit", "-m", message,
                 env={"GIT_AUTHOR_DATE": f"{epoch} +0000", "GIT_COMMITTER_DATE": f"{epoch} +0000"})

    def validate(self, manifest=None, policy=None):
        return evidence.validate(manifest if manifest is not None else self.manifest,
                                 policy if policy is not None else self.policy,
                                 repo=self.repo, now=self.now, **self.paths)

    def test_valid_declarations_remain_unauthenticated_and_non_authorizing(self):
        report = self.validate()
        self.assertTrue(report["consistent"])
        self.assertFalse(report["authenticationPerformed"])
        self.assertFalse(report["runtimeQualificationVerified"])
        self.assertFalse(report["publicationAuthorized"])
        self.assertNotEqual(self.manifest["records"]["qualification"]["producer"]["sourceSHA"],
                            self.manifest["identity"]["sources"]["candidateSHA"])

    def test_source_and_frozen_identity_mutations_are_rejected(self):
        for section, field, value in [
            ("sources", "candidateSHA", "a" * 40), ("sources", "finalSHA", "b" * 40),
            ("sources", "baseSHA", "c" * 40), ("sources", "tree", "d" * 40),
            ("sources", "sourceDateEpoch", 1), ("sources", "sourceDateEpoch", True),
            ("release", "version", "0.6.2"), ("release", "bodySHA256", "0" * 64),
        ]:
            with self.subTest(field=field):
                manifest = copy.deepcopy(self.manifest)
                manifest["identity"][section][field] = value
                with self.assertRaises(ValueError):
                    self.validate(manifest)
        for field in ("inputsSHA256", "testPlanSHA256"):
            manifest = copy.deepcopy(self.manifest)
            manifest["identity"][field] = "0" * 64
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(manifest)

    def test_image_substitution_missing_payload_and_policy_identity_are_rejected(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["identity"]["images"]["operator"] = "sha256:" + "b" * 64
        with self.assertRaisesRegex(ValueError, "selected policy"):
            self.validate(manifest)
        manifest = copy.deepcopy(self.manifest)
        del manifest["identity"]["files"]["checksums.txt"]
        with self.assertRaisesRegex(ValueError, "missing fields"):
            self.validate(manifest)
        policy = copy.deepcopy(self.policy)
        policy["identitySHA256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "selected policy"):
            self.validate(policy=policy)

    def test_ambient_git_replacements_do_not_change_source_identity(self):
        sources = self.manifest["identity"]["sources"]
        self.git("replace", sources["candidateSHA"], sources["baseSHA"])
        self.assertTrue(self.validate()["consistent"])

    def test_ambiguous_source_version_syntax_is_rejected(self):
        original_git = evidence.git
        candidate = self.manifest["identity"]["sources"]["candidateSHA"]
        chart_path = f"{candidate}:charts/openbao-operator/Chart.yaml"
        chart = "version: 0.6.1\nappVersion: 0.6.1\n"
        replacements = [(chart_path, value) for value in [
            "version: 0.6.1\nversion: 0.6.2\nappVersion: 0.6.1\n",
            chart + '\"version\": 9.9.9\n', chart + "'appVersion': 9.9.9\n",
            chart + "version : 9.9.9\n", chart + "<<: *other\n",
            "version: 0.6.1\n  continuation\nappVersion: 0.6.1\n",
            "---\n" + chart, chart + "? version\n: 9.9.9\n",
        ]]
        replacements.append((f"{candidate}:.release-please-manifest.json", '{".":"0.6.2",".":"0.6.1"}'))
        for path, value in replacements:
            def read_git(repo, *args):
                return value if args == ("show", path) else original_git(repo, *args)
            with self.subTest(value=value), patch.object(evidence, "git", side_effect=read_git):
                with self.assertRaises(ValueError):
                    self.validate()

    def test_policy_selection_rejects_producer_and_artifact_substitution(self):
        for field, value in [("repository", "other/repo"), ("workflow", ".github/workflows/other.yml"),
                             ("sourceSHA", "a" * 40), ("signerSHA", "b" * 40),
                             ("runID", 999), ("jobID", 999), ("attempt", 2), ("attempt", True)]:
            manifest = copy.deepcopy(self.manifest)
            manifest["records"]["qualification"]["producer"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(manifest)
        for field, value in [("id", 999), ("digest", "sha256:" + "0" * 64),
                             ("expiresAt", "2099-01-01T00:00:00Z")]:
            manifest = copy.deepcopy(self.manifest)
            manifest["records"]["qualification"]["artifacts"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(manifest)

    def test_record_bindings_and_final_event_context_are_checked(self):
        for field, value in [("identitySHA256", "0" * 64), ("sourceSHA", "a" * 40)]:
            manifest = copy.deepcopy(self.manifest)
            manifest["records"]["finalBuild"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(manifest)
        manifest, policy = copy.deepcopy(self.manifest), copy.deepcopy(self.policy)
        for record in (manifest["records"]["finalBuild"], policy["records"]["finalBuild"]):
            record["producer"]["sourceSHA"] = self.manifest["identity"]["sources"]["baseSHA"]
        with self.assertRaisesRegex(ValueError, "event source"):
            self.validate(manifest, policy)

    def test_changed_payload_and_input_bytes_fail_without_changing_claims(self):
        for file in [self.paths[name] for name in ("notes", "inputs", "plan")] + [
            self.paths["payloads"] / "install.yaml", self.paths["archives"] / "1.zip"
        ]:
            original = file.read_bytes()
            file.write_bytes(original + b"\n")
            with self.subTest(path=file.name), self.assertRaises(ValueError):
                self.validate()
            file.write_bytes(original)
        (self.paths["archives"] / "1.zip").unlink()
        with self.assertRaises(FileNotFoundError):
            self.validate()

    def test_expired_and_duplicate_artifact_ids_fail_even_in_selected_policy(self):
        for expiry in ("2000-01-01T00:00:00Z", "2100-13-01T00:00:00Z", "2100-01-01"):
            manifest, policy = copy.deepcopy(self.manifest), copy.deepcopy(self.policy)
            for value in (manifest, policy):
                value["records"]["qualification"]["artifacts"][0]["expiresAt"] = expiry
            with self.subTest(expiry=expiry), self.assertRaises(ValueError):
                self.validate(manifest, policy)
        manifest, policy = copy.deepcopy(self.manifest), copy.deepcopy(self.policy)
        for value in (manifest, policy):
            value["records"]["finalBuild"]["artifacts"] = copy.deepcopy(
                value["records"]["qualification"]["artifacts"])
        with self.assertRaisesRegex(ValueError, "more than once"):
            self.validate(manifest, policy)

    def test_strict_schema_and_non_authorizing_claim_are_required(self):
        for field, value in [("schema", 2), ("schema", True), ("claim", "release-qualified"),
                             ("publicationAuthorized", True)]:
            manifest = copy.deepcopy(self.manifest)
            manifest[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(manifest)
        for role in evidence.ROLES:
            manifest = copy.deepcopy(self.manifest)
            del manifest["records"][role]
            with self.subTest(role=role), self.assertRaises(ValueError):
                self.validate(manifest)

    def test_duplicate_json_keys_and_large_documents_are_rejected(self):
        file = self.root / "invalid.json"
        file.write_text('{"schema":1,"schema":1}')
        with self.assertRaisesRegex(ValueError, "duplicate"):
            evidence.load_json(file)
        file.write_text(" " * (1024 * 1024 + 1))
        with self.assertRaisesRegex(ValueError, "one MiB"):
            evidence.load_json(file)

    def cli_command(self, output):
        command = [sys.executable, str(ROOT / "hack/ci/release-evidence.py"),
                   "--manifest", str(self.root / "manifest.json"), "--policy", str(self.root / "policy.json"),
                   "--repo", str(self.repo), "--output", str(output)]
        for name, path in self.paths.items():
            command.extend(["--" + name, str(path)])
        return command

    def test_cli_rejects_policy_boolean_and_float_artifact_ids(self):
        (self.root / "manifest.json").write_text(json.dumps(self.manifest))
        for index, value in enumerate((True, 1.0)):
            policy = copy.deepcopy(self.policy)
            policy["records"]["comparison"]["artifacts"][0]["id"] = value
            (self.root / "policy.json").write_text(json.dumps(policy))
            output = self.root / f"invalid-id-{index}.json"
            result = subprocess.run(self.cli_command(output), capture_output=True, text=True)
            with self.subTest(value=value):
                self.assertNotEqual(result.returncode, 0)
                report = json.loads(output.read_text())
                self.assertFalse(report["consistent"])
                self.assertFalse(report["authenticationPerformed"])
                self.assertFalse(report["publicationAuthorized"])

    def test_cli_deep_json_writes_a_failed_report(self):
        nested = "[" * 5000 + "0" + "]" * 5000
        for name, value in (("manifest", self.manifest), ("policy", self.policy)):
            changed = copy.deepcopy(value)
            changed["records"]["comparison"]["artifacts"] = "nested-fixture"
            raw = json.dumps(changed).replace('"nested-fixture"', nested)
            self.assertLess(len(raw), 1024 * 1024)
            (self.root / f"{name}.json").write_text(raw)
        output = self.root / "deep-report.json"
        result = subprocess.run(self.cli_command(output), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(output.read_text())
        self.assertFalse(report["consistent"])
        self.assertFalse(report["authenticationPerformed"])
        self.assertFalse(report["publicationAuthorized"])

    def test_cli_always_writes_a_non_authorizing_report(self):
        manifest, policy, output = (self.root / name for name in ("manifest.json", "policy.json", "report.json"))
        manifest.write_text(json.dumps(self.manifest))
        policy.write_text(json.dumps(self.policy))
        command = self.cli_command(output)
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(output.read_text())["consistent"])
        first_report = output.read_bytes()
        failed_output = self.root / "failed-report.json"
        command[command.index("--output") + 1] = str(failed_output)
        manifest.write_text('{"schema":false}')
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(output.read_bytes(), first_report)
        report = json.loads(failed_output.read_text())
        self.assertFalse(report["consistent"])
        self.assertFalse(report["authenticationPerformed"])
        self.assertFalse(report["publicationAuthorized"])
        original = failed_output.read_bytes()
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(failed_output.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
