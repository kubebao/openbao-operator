"""Offline rejection checks for the isolated Hardened upgrade overlay."""

import base64
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("qualification_helper", ROOT / "hack/ci/qualification-helper-policy.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
KEYS = ("E2E_QUALIFICATION_POLICY_FILE", "E2E_QUALIFICATION_POLICY_SHA256", "E2E_QUALIFICATION_AUTH_FILE")


class HelperPolicyTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.policy = Path(self.directory.name) / "policy.json"
        self.auth = Path(self.directory.name) / "auth.json"
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("E2E_QUALIFICATION_")}
        self.selected = {"schemaVersion": 1, "claim": "release-candidate-qualification-pilot",
                         "planSHA256": "a" * 64, "controllerSHA": "b" * 40,
                         "issuer": MODULE.ISSUER, "subject": MODULE.SUBJECT,
                         "images": {role: f"ghcr.io/kubebao/openbao-{role}-qualification@sha256:" + "c" * 64
                                    for role in ("operator", "init", "backup", "upgrade")}}
        self.image = self.selected["images"]["init"]
        self.auth.write_text(json.dumps({"auths": {"ghcr.io": {
            "auth": base64.b64encode(b"reader:private-fixture").decode()}}}))
        self.save(self.selected)

    def save(self, selected):
        self.policy.write_text(json.dumps(selected))
        self.env.update(dict(zip(KEYS, (str(self.policy), hashlib.sha256(self.policy.read_bytes()).hexdigest(),
                                       str(self.auth)))))

    def run_harness(self, env):
        return subprocess.run(["bash", str(ROOT / "hack/ci/operator-upgrade-e2e.sh")],
                              env={**env, "OPERATOR_UPGRADE_E2E_VERIFY_ONLY": "true",
                                   "OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE": self.image},
                              capture_output=True, text=True, timeout=20)

    def test_patch_contains_only_public_blocking_policy(self):
        with patch.dict(os.environ, self.env, clear=True):
            selected = MODULE.load(self.image)
        rendered = MODULE.patch(selected)
        self.assertEqual(rendered, {"spec": {"imagePullSecrets": [{"name": MODULE.PULL_REFERENCE_NAME}],
            "operatorImageVerification": {"enabled": True, "issuer": MODULE.ISSUER,
                "subject": MODULE.SUBJECT, "failurePolicy": "Block", "ignoreTlog": False,
                "imagePullSecrets": [{"name": MODULE.PULL_REFERENCE_NAME}]}}})
        self.assertNotIn("private-fixture", json.dumps(rendered))

    def test_exact_opt_in_required_by_upgrade_harness(self):
        result = self.run_harness(self.env)
        self.assertEqual(result.returncode, 0, result.stderr)
        for missing in (*KEYS, "all"):
            with self.subTest(missing=missing):
                env = {key: value for key, value in self.env.items()
                       if key != missing and not (missing == "all" and key in KEYS)}
                self.assertNotEqual(self.run_harness(env).returncode, 0)

    def test_patch_stdout_does_not_depend_on_credential_contents(self):
        outputs = []
        for credential in (b"reader:private-fixture", b"other:separate-fixture"):
            encoded = base64.b64encode(credential).decode()
            self.auth.write_text(json.dumps({"auths": {"ghcr.io": {"auth": encoded}}}))
            result = subprocess.run(["python3", str(ROOT / "hack/ci/qualification-helper-policy.py"),
                                     "patch", "--init-image", self.image], env=self.env,
                                    capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn(credential.decode(), result.stdout + result.stderr)
            self.assertNotIn(encoded, result.stdout + result.stderr)
            outputs.append(result.stdout)
        self.assertEqual(outputs[0], outputs[1])

    def test_rejects_policy_and_helper_substitution(self):
        changes = (("issuer", "https://untrusted.invalid"), ("subject", MODULE.SUBJECT + "-other"),
                   ("schemaVersion", True), ("planSHA256", "d" * 63),
                   ("controllerSHA", "main"), ("unexpected", True))
        for key, value in changes:
            with self.subTest(key=key):
                selected = {**self.selected, key: value}
                self.save(selected)
                self.assertNotEqual(self.run_harness(self.env).returncode, 0)
        for image in (self.image.replace("-qualification", ""), self.image + "0", self.image.split("@")[0] + ":edge"):
            selected = copy.deepcopy(self.selected)
            selected["images"]["init"] = image
            self.save(selected)
            with patch.dict(os.environ, self.env, clear=True), self.assertRaises(ValueError):
                MODULE.load(image)

    def test_rejects_changed_hash_duplicate_keys_and_credential_without_disclosure(self):
        self.policy.write_text(self.policy.read_text() + " ")
        self.assertNotEqual(self.run_harness(self.env).returncode, 0)
        self.save(self.selected)
        self.policy.write_text(self.policy.read_text().replace('"schemaVersion": 1', '"schemaVersion": 1,"schemaVersion": 1'))
        self.env[KEYS[1]] = hashlib.sha256(self.policy.read_bytes()).hexdigest()
        self.assertNotEqual(self.run_harness(self.env).returncode, 0)
        self.save(self.selected)
        self.auth.write_text('{"private-fixture":"unterminated')
        result = self.run_harness(self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("private-fixture", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
