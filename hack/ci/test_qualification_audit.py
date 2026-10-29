"""Reject incomplete, retried or substituted runtime evidence without workloads."""

import base64
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("audit", Path(__file__).with_name("qualification-audit.py"))
M = importlib.util.module_from_spec(spec)
spec.loader.exec_module(M)


class QualificationAuditTest(unittest.TestCase):
    def test_cosign_attestation_object_stream(self):
        envelope = {"payloadType": "application/vnd.in-toto+json", "payload": "e30="}
        compact = json.dumps(envelope).encode()
        for raw, count in ((compact, 1), (compact + b"\n" + compact + b"\n", 2),
                           (b" \n" + json.dumps(envelope, indent=2).encode() + b"\t", 1)):
            with self.subTest(raw=raw):
                self.assertEqual(M.attestation_envelopes(raw), [envelope] * count)
        for raw in (b"", b" \n", b"[]", b"null", b"1", b'"string"', b"{}", b'{"payload":1}',
                    b'{"payload":"x","payload":"y"}', compact + b"garbage", b"\xff",
                    compact + b"\v", compact + "\u00a0".encode(),
                    compact * 1025, b" " * (2 * 1024 * 1024 + 1)):
            with self.subTest(raw=raw[:100]), self.assertRaises(ValueError):
                M.attestation_envelopes(raw)

    def provenance_fixture(self):
        plan = {"sources": {"controllerSHA": "a" * 40, "candidateSHA": "b" * 40},
                "context": {"runID": 42, "attempt": 1}}
        images = {role: f"ghcr.io/kubebao/openbao-{role}-qualification@sha256:" + "c" * 64 for role in M.A.ROLES}
        record = {"origins": {role: {"artifactID": index + 1} for index, role in enumerate(M.A.ROLES)}}

        def statement(role):
            return {"predicateType": "https://kubebao.org/attestations/candidate-image-import/v1",
                    "subject": [{"digest": {"sha256": "c" * 64}}],
                    "predicate": {"planSHA256": M.P.fingerprint(plan), "subjectDigest": "sha256:" + "c" * 64,
                                  "sources": plan["sources"], "controller": plan["context"],
                                  "producingArtifact": record["origins"][role],
                                  "runtimeQualificationVerified": False, "publicationAuthorized": False}}

        return plan, images, record, statement

    def verify_provenance(self, transform=None, signature_type="https://sigstore.dev/cosign/sign/v1",
                          signature_digest=None):
        plan, images, record, statement = self.provenance_fixture()

        def run(command, **kwargs):
            role = next(role for role, image in images.items() if image == command[-1])
            self.assertEqual(command[command.index("--certificate-github-workflow-sha") + 1],
                             plan["sources"]["controllerSHA"])
            self.assertEqual(command[command.index("--certificate-oidc-issuer") + 1],
                             "https://token.actions.githubusercontent.com")
            self.assertEqual(command[command.index("--certificate-identity") + 1],
                             "https://github.com/kubebao/openbao-operator/.github/workflows/"
                             "release-qualification-pilot.yml@refs/heads/main")
            if command[1] == "verify":
                raw = json.dumps([{"critical": {"type": signature_type, "image": {
                    "docker-manifest-digest": signature_digest or images[role].split("@")[1]}}}]).encode()
            else:
                documents = transform(statement(role)) if transform else [statement(role)]
                raw = b"\n".join(json.dumps({"payloadType": "application/vnd.in-toto+json",
                    "payload": base64.b64encode(json.dumps(value).encode()).decode()}).encode() for value in documents)
            return subprocess.CompletedProcess(command, 0, raw, b"")

        with tempfile.TemporaryDirectory() as folder, patch.object(M.subprocess, "run", side_effect=run):
            return M.image_provenance(plan, images, record, Path(folder))

    def test_verified_single_and_multiple_attestations_select_exact_plan(self):
        self.assertEqual(set(self.verify_provenance()), set(M.A.ROLES))

        def older_and_selected(selected):
            older = copy.deepcopy(selected)
            older["predicate"]["planSHA256"] = "d" * 64
            return [older, selected]

        self.assertEqual(set(self.verify_provenance(older_and_selected)), set(M.A.ROLES))
        with self.assertRaisesRegex(ValueError, "exactly one provenance"):
            self.verify_provenance(lambda selected: [selected, selected])

    def test_verified_provenance_rejects_substituted_claims(self):
        for field in ("predicateType", "subject", "subjectDigest", "sources", "controller", "producingArtifact",
                      "runtimeQualificationVerified", "publicationAuthorized"):
            def changed(selected):
                if field == "predicateType":
                    selected[field] = "https://example.invalid/other"
                elif field == "subject":
                    selected[field][0]["digest"]["sha256"] = "d" * 64
                else:
                    selected["predicate"][field] = True if field.endswith("Verified") or field.endswith("Authorized") else "other"
                return [selected]

            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify_provenance(changed)

    def test_generic_signature_type_and_digest_are_required(self):
        with self.assertRaisesRegex(ValueError, "generic image signature"):
            self.verify_provenance(signature_type="https://kubebao.org/attestations/candidate-image-import/v1")
        with self.assertRaisesRegex(ValueError, "generic image signature"):
            self.verify_provenance(signature_digest="sha256:" + "d" * 64)

    def test_signing_uploads_before_retaining_remote_material(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            images = {role: f"ghcr.io/kubebao/openbao-{role}-qualification@sha256:" + "c" * 64 for role in M.A.ROLES}
            (root / "dist/import").mkdir(parents=True)
            (root / "dist/import/images.json").write_text(json.dumps(images))
            for role in images:
                (root / f"dist/import/{role}").mkdir()
            for name in ("policy", "import"):
                (root / f"dist/import/{name}.json").write_text("{}")
            cli = root / "cosign"
            cli.write_text("#!" + sys.executable + '\n' + '''import json, pathlib, sys
args = sys.argv[1:]
path = pathlib.Path("registry-state.json")
state = json.loads(path.read_text()) if path.exists() else {}
ref = args[-1]
if args[0] in ("sign", "attest"):
    if "--bundle" not in args:
        state.setdefault(ref, {})[args[0]] = True
elif not all(state.get(ref, {}).get(kind) for kind in ("sign", "attest")):
    sys.exit(10)
else:
    print(json.dumps({"remote": ref, "command": args[0]}))
path.write_text(json.dumps(state))
with pathlib.Path("calls.jsonl").open("a") as out:
    out.write(json.dumps(args) + "\\n")
''')
            cli.chmod(0o700)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "CONTROLLER": "a" * 40, "GITHUB_WORKFLOW_SHA": "a" * 40}
            result = subprocess.run(["bash", str(M.ROOT / "hack/ci/sign-qualification-images.sh")],
                                    cwd=root, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
            self.assertEqual(len(calls), 20)
            for index, role in enumerate(images):
                selected = calls[index * 5:index * 5 + 5]
                self.assertEqual([call[0] for call in selected], ["sign", "attest", "download", "verify", "verify-attestation"])
                self.assertEqual({call[-1] for call in selected}, {images[role]})
                for call in selected[-2:]:
                    self.assertEqual(call[call.index("--certificate-github-workflow-sha") + 1], "a" * 40)
                self.assertTrue((root / f"dist/import/{role}/registry-bundles.jsonl").stat().st_size)

    def test_actual_execution_required_and_suite_nodes_are_not_leaf_retries(self):
        report = [{"SuiteSucceeded": True, "SuiteConfig": {"DryRun": False}, "SpecReports": [
            {"LeafNodeType": "SynchronizedBeforeSuite", "State": "passed", "NumAttempts": 0},
            {"LeafNodeType": "It", "State": "passed", "NumAttempts": 1}]}]
        M.no_retries(report)
        for change in ("retry", "no-attempt", "dry-run", "failed", "repeat", "configured-retry", "extra-suite"):
            data = copy.deepcopy(report)
            if change == "retry":
                data[0]["SpecReports"][1]["NumAttempts"] = 2
            elif change == "no-attempt":
                del data[0]["SpecReports"][1]["NumAttempts"]
            elif change == "dry-run":
                data[0]["SuiteConfig"]["DryRun"] = True
            elif change == "failed":
                data[0]["SuiteSucceeded"] = False
            elif change == "repeat":
                data[0]["SuiteConfig"]["Repeat"] = 1
            elif change == "configured-retry":
                data[0]["SuiteConfig"]["FlakeAttempts"] = 3
            else:
                data.append(copy.deepcopy(data[0]))
            with self.subTest(change=change), self.assertRaises((ValueError, KeyError)):
                M.no_retries(data)

    def test_node_observations_must_match_every_planned_kubelet(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "nodes.json"
            data = {"items": [{"metadata": {"name": "node", "uid": "uid"},
                               "status": {"nodeInfo": {"kubeletVersion": "v1.36.1"}}}]}
            path.write_text(json.dumps(data))
            self.assertEqual(M.nodes(path, "1.36.1")[0]["kubeletVersion"], "v1.36.1")
            for changed in ({"items": []}, {"items": data["items"] + [{**data["items"][0],
                    "status": {"nodeInfo": {"kubeletVersion": "v1.35.1"}}}]}):
                path.write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    M.nodes(path, "1.36.1")

    def test_stage_identity_cannot_substitute_source_plan_or_attempt(self):
        plan = {"sources": {"candidateSHA": "a" * 40}, "context": {"runID": 42, "attempt": 2}}
        value = M.R.receipt(plan, "api")
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "result.json"
            path.write_text(json.dumps(value))
            self.assertEqual(M.result(plan, Path(folder), "api"), value)
            for key, changed in (("sources", {"candidateSHA": "b" * 40}),
                                 ("execution", {"runID": 42, "attempt": 1}), ("planSHA256", "c" * 64)):
                path.write_text(json.dumps({**value, key: changed}))
                with self.subTest(key=key), self.assertRaises(ValueError):
                    M.result(plan, Path(folder), "api")


if __name__ == "__main__":
    unittest.main()
