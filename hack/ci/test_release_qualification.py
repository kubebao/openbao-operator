#!/usr/bin/env python3
"""Exercise frozen qualification planning without GitHub, signing or workloads."""

import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("qualification", Path(__file__).with_name("release-qualification.py"))
qualification = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qualification)


class QualificationPlanning(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Qualification test")
        self.git("config", "user.email", "qualification@example.invalid")
        for name in qualification.SOURCE_FILES:
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture\n")
        (self.repo / "charts/openbao-operator/Chart.yaml").write_text("name: operator\nversion: 0.6.0\nappVersion: 0.6.0\n")
        (self.repo / ".release-please-manifest.json").write_text('{".": "0.6.0"}\n')
        (self.repo / "hack/dev/tool-versions.env").write_bytes(
            (qualification.ROOT / "hack/dev/tool-versions.env").read_bytes())
        for name in ("test/e2e/e2e_versions.go", "test/e2e/upgradeversions/versions.go",
                     "test/fixtures/operator-upgrade/versions.env", "test/fixtures/operator-upgrade/cluster.yaml",
                     "test/fixtures/operator-upgrade/transit-cluster.yaml", "test/fixtures/operator-upgrade/hardened-cluster.yaml"):
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes((qualification.ROOT / name).read_bytes())
        for name in qualification.FROZEN_TEST_PATHS:
            path = self.repo / name
            if not path.exists():
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("fixture\n")
        self.controller = self.commit("controller")
        self.git("switch", "-c", "candidate")
        (self.repo / "Dockerfile").write_text("candidate fixture\n")
        self.head = self.commit("candidate")
        self.git("switch", "-c", "preview", self.controller)
        self.git("merge", "--no-ff", "-m", "genuine merge preview", self.head)
        self.candidate = self.git("rev-parse", "HEAD")
        self.git("checkout", "--detach", self.controller)
        self.pr = {"number": 17, "state": "open", "draft": False, "merged": False,
                   "head": {"sha": self.head, "repo": {"id": qualification.REPOSITORY_ID}},
                   "base": {"sha": self.controller, "ref": "main", "repo": {"id": qualification.REPOSITORY_ID}},
                   "merge_commit_sha": self.candidate}
        self.source = qualification.inspect(self.repo, self.controller, self.head, self.pr)
        self.context = {"repository": qualification.REPOSITORY, "repositoryID": qualification.REPOSITORY_ID,
                        "sourceSHA": self.controller, "workflow": qualification.WORKFLOW, "runID": 123, "attempt": 1}
        self.matrix = {"include": [{"id": "core", "k8s": "1.36.1", "coverage": "primary", "profile": "release-gate",
                                    "label_filter": "core", "expected_case_ids": ["case-1", "case-2"]},
                                   {"id": "core", "k8s": "1.35.1", "coverage": "compatibility", "profile": "release-gate",
                                    "label_filter": "core", "expected_case_ids": ["case-1", "case-2"]}]}
        self.notes = "# Release 0.6.0\n\nFrozen café body.\n\n\n## Changelog\n\n- Fixture.\n".encode()
        self.inputs = qualification.controller_inputs()

    def git(self, *args):
        # Temporary Git mutations must never inherit hook/wrapper repository locations or global configuration.
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        env.update({"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull,
                    "GIT_AUTHOR_DATE": "2030-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2030-01-01T00:00:00Z"})
        return subprocess.check_output(["git", "-C", str(self.repo), *args], env=env,
                                       stderr=subprocess.DEVNULL, text=True).strip()

    def commit(self, message):
        self.git("add", ".")
        self.git("-c", "commit.gpgsign=false", "commit", "-m", message)
        return self.git("rev-parse", "HEAD")

    def freeze(self, **changes):
        values = {"source": self.source, "version": "0.6.0", "notes": self.notes,
                  "notes_hash": qualification.digest(self.notes), "matrix": self.matrix,
                  "planned_matrix": self.matrix, "inputs": self.inputs, "execution": self.context}
        values.update(changes)
        return qualification.freeze(**values)

    def test_real_preview_identity_stays_distinct_from_controller_and_head(self):
        self.assertEqual(self.source["controllerSHA"], self.controller)
        self.assertEqual(self.source["prHeadSHA"], self.head)
        self.assertEqual(self.source["candidateSHA"], self.candidate)
        self.assertEqual(self.source["suiteSourceSHA"], self.candidate)
        self.assertEqual(len({self.controller, self.head, self.candidate}), 3)
        self.assertEqual(self.source["sourceDateEpoch"], 1893456000)

    def test_drifted_source_or_unreviewed_repository_is_rejected(self):
        for field, value in [("draft", True), ("state", "closed"), ("merged", True), ("merge_commit_sha", self.head)]:
            pr = copy.deepcopy(self.pr)
            pr[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                qualification.inspect(self.repo, self.controller, self.head, pr)
        for side in ("head", "base"):
            pr = copy.deepcopy(self.pr)
            pr[side]["repo"]["id"] += 1
            with self.subTest(side=side), self.assertRaises(ValueError):
                qualification.inspect(self.repo, self.controller, self.head, pr)

    def test_git_reads_ignore_inherited_repository_location(self):
        with patch.dict(os.environ, {"GIT_DIR": str(self.root / "absent"), "GIT_WORK_TREE": str(self.root)}):
            self.assertEqual(qualification.inspect(self.repo, self.controller, self.head, self.pr), self.source)

    def test_first_pilot_rejects_candidate_changes_to_the_reviewed_test_inputs(self):
        self.git("checkout", "candidate")
        (self.repo / "test/e2e/suites.yaml").write_text("changed profile\n")
        changed_head = self.commit("changed test profile")
        self.git("checkout", "-b", "changed-preview", self.controller)
        self.git("merge", "--no-ff", "-m", "changed preview", changed_head)
        preview = self.git("rev-parse", "HEAD")
        self.git("checkout", "--detach", self.controller)
        pr = copy.deepcopy(self.pr)
        pr["head"]["sha"], pr["merge_commit_sha"] = changed_head, preview
        with self.assertRaises(ValueError):
            qualification.inspect(self.repo, self.controller, changed_head, pr)

    def test_notes_epoch_matrix_inputs_and_context_are_bound(self):
        plan = self.freeze()
        self.assertEqual(plan["releaseBodySHA256"], qualification.digest(self.notes))
        self.assertEqual(plan["sources"]["sourceDateEpoch"], self.source["sourceDateEpoch"])
        self.assertEqual(plan["selectedExecutions"], 4)
        self.assertEqual(plan["uniqueCaseIDs"], 2)
        self.assertEqual(plan["artifactPrefix"], "release-qualification-123-1")
        self.assertFalse(plan["publicationAuthorized"])
        self.assertFalse(plan["runtimeQualificationVerified"])
        changed = copy.deepcopy(plan)
        changed["context"]["attempt"] += 1
        self.assertNotEqual(qualification.fingerprint(plan), qualification.fingerprint(changed))

    def freeze_command(self):
        source = self.root / "source.json"
        source.write_text(json.dumps(self.source))
        notes = self.root / "notes.txt"
        notes.write_bytes(self.notes)
        args = argparse.Namespace(controller=self.controller, repo=self.repo, source=source,
                                  version="0.6.0", notes=notes, notes_sha=qualification.digest(self.notes),
                                  output=self.root / "plan.json")
        with patch.object(qualification, "context", return_value=self.context), \
                patch.object(qualification, "pr_metadata", return_value=self.pr), \
                patch.object(qualification, "release_matrix", return_value=self.matrix), \
                patch.object(qualification, "external_inputs", return_value=({}, {"version": "0.5.3"})), \
                patch.dict(os.environ, {"GITHUB_OUTPUT": ""}):
            qualification.freeze_command(args)
        return qualification.read_json(args.output)

    def test_freeze_command_accepts_repository_root_manifest_version(self):
        plan = self.freeze_command()
        self.assertEqual(plan["version"], "0.6.0")
        self.assertEqual(plan["sources"]["candidateSHA"], self.candidate)
        self.assertEqual(plan["sources"]["sourceFileSHA256"][".release-please-manifest.json"],
                         qualification.digest(b'{".": "0.6.0"}\n'))

    def test_freeze_command_rejects_missing_or_wrong_root_manifest_version(self):
        original = qualification.git
        for manifest in ({"": "0.6.0"}, {".": "0.5.3", "": "0.6.0"}, {}):
            def read(repo, *args):
                if args == ("show", f"{self.candidate}:.release-please-manifest.json"):
                    return json.dumps(manifest).encode()
                return original(repo, *args)

            with self.subTest(manifest=manifest), patch.object(qualification, "git", side_effect=read), \
                    patch.object(qualification, "inspect", return_value=self.source), \
                    self.assertRaisesRegex(ValueError, "candidate release manifest version differs"):
                self.freeze_command()

    def test_notes_bytes_are_not_normalized(self):
        for changed in [self.notes.rstrip(), self.notes.replace(b"\n\n\n", b"\n\n"), self.notes.replace(b"\n", b"\r\n")]:
            with self.assertRaises(ValueError):
                self.freeze(notes=changed)
        with self.assertRaises(UnicodeDecodeError):
            self.freeze(notes=b"\xff", notes_hash=qualification.digest(b"\xff"))

    def test_wrong_versions_epoch_and_suite_source_are_rejected(self):
        for value in ("0.6.0-e2e", "v0.6.0", "0.6", "0.6.0\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.freeze(version=value)
        for field, value in [("sourceDateEpoch", True), ("sourceDateEpoch", -1),
                             ("suiteSourceSHA", self.head), ("controllerSHA", self.head)]:
            source = copy.deepcopy(self.source)
            source[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.freeze(source=source)

    def test_matrix_rejects_duplicates_skips_and_non_release_profile(self):
        for matrix in [{"include": []}, {"include": self.matrix["include"] * 2},
                       {"include": [{**self.matrix["include"][0], "expected_case_ids": []}]},
                       {"include": [{**self.matrix["include"][0], "expected_case_ids": ["case-1", "case-1"]}]},
                       {"include": [{**self.matrix["include"][0], "label_filter": ""}]},
                       {"include": [{**self.matrix["include"][0], "profile": "daily"}]}]:
            with self.subTest(matrix=matrix), self.assertRaises(ValueError):
                self.freeze(matrix=matrix)

    def test_package_allowlist_and_retention_cannot_be_substituted(self):
        for field, value in [("retentionDays", True), ("retentionDays", 0), ("retentionDays", 100),
                             ("schemaVersion", True), ("packages", {"init": "ghcr.io/kubebao/openbao-init"})]:
            inputs = copy.deepcopy(self.inputs)
            inputs[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.freeze(inputs=inputs)

    def test_reduced_or_invented_matrix_is_rejected(self):
        reduced = {"include": self.matrix["include"][:1]}
        invented = copy.deepcopy(self.matrix)
        invented["include"][0]["expected_case_ids"] = ["invented-case"]
        for matrix in (reduced, invented):
            with self.subTest(matrix=matrix), self.assertRaises(ValueError):
                self.freeze(matrix=matrix)

    def test_release_planner_receives_only_candidate_data_and_no_lane_filters(self):
        result = subprocess.CompletedProcess([], 0, json.dumps(self.matrix).encode(), b"")
        with patch.object(qualification.subprocess, "run", return_value=result) as run, \
                patch.object(qualification, "git", return_value=b"fixture"):
            self.assertEqual(qualification.release_matrix(self.repo, self.candidate), self.matrix)
        args, options = run.call_args
        self.assertEqual(args[0][:3], ["go", "run", "./hack/tools/e2e_plan"])
        self.assertEqual(args[0][-4:], ["--format", "github-nightly-matrix", "--profile", "release-gate"])
        self.assertNotIn("--lane", args[0])
        self.assertNotIn("--kubernetes", args[0])
        self.assertEqual(options["cwd"], qualification.ROOT)
        self.assertEqual(options["env"]["GOFLAGS"], "-mod=vendor")

    def test_output_creation_is_exclusive(self):
        output = self.root / "existing.json"
        qualification.write_json(output, self.source)
        old = output.read_bytes()
        with self.assertRaises(FileExistsError):
            qualification.write_json(output, {})
        self.assertEqual(output.read_bytes(), old)

    def test_candidate_cannot_change_controller_cli_pins(self):
        source = copy.deepcopy(self.source)
        source["sourceFileSHA256"]["hack/dev/tool-versions.env"] = "d" * 64
        with self.assertRaises(ValueError):
            self.freeze(source=source)

    def test_external_inputs_freeze_versions_nodes_baseline_and_existing_digests(self):
        matrix = copy.deepcopy(self.matrix)
        matrix["include"][0].update({"kind_node_image": "kindest/node:v1.36.1",
                                     "openbao_image": "ghcr.io/openbao/openbao:2.6.5",
                                     "upgrade_sequence": "2.5.5,2.6.4,2.7.2"})
        original = subprocess.run
        references = []

        def resolve(args, **kwargs):
            if args[0] != "oras":
                return original(args, **kwargs)
            self.assertEqual(args[:2], ["oras", "resolve"])
            references.append(args[2])
            value = args[2].split("@", 1)[1] if "@" in args[2] else "sha256:" + "a" * 64
            return subprocess.CompletedProcess(args, 0, (value + "\n").encode(), b"")

        with patch.object(qualification.subprocess, "run", side_effect=resolve):
            images, baseline = qualification.external_inputs(self.repo, self.controller, matrix)
        self.assertIn("kindest/node:v1.36.1", images)
        self.assertIn("openbao/openbao:2.7.2", images)
        self.assertIn("ghcr.io/openbao/openbao:2.6.2", images)
        self.assertEqual(baseline["version"], "0.5.3")
        self.assertEqual(baseline["chartDigest"], "sha256:" + "a" * 64)
        self.assertEqual(images[baseline["initImage"]], baseline["initImage"])
        self.assertEqual(len(references), len(set(references)))
        self.assertIn("docker.io/kindest/node:v1.36.1", references)
        self.assertIn("docker.io/openbao/openbao:2.7.2", references)
        self.assertFalse(any(reference.startswith(("kindest/", "openbao/")) for reference in references))

    def test_changed_pinned_external_digest_is_rejected(self):
        original = subprocess.run

        def resolve(args, **kwargs):
            if args[0] != "oras":
                return original(args, **kwargs)
            return subprocess.CompletedProcess(args, 0, ("sha256:" + "a" * 64).encode(), b"")

        with patch.object(qualification.subprocess, "run", side_effect=resolve), self.assertRaises(ValueError):
            qualification.external_inputs(self.repo, self.controller, self.matrix)

    def test_duplicate_json_is_rejected(self):
        output = self.root / "duplicate.json"
        output.write_text('{"source":1,"source":2}')
        with self.assertRaises(ValueError):
            qualification.read_json(output)


if __name__ == "__main__":
    unittest.main()
