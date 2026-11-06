"""Regression checks for reproducible operator-upgrade baseline selection."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("upgrade_baseline", ROOT / "hack/ci/operator-upgrade-baseline.py")
BASELINE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BASELINE)


def release(version):
    return dict(tag_name=version, draft=False, prerelease=False, published_at="2026-11-06")


class BaselineSelection(unittest.TestCase):
    def test_historical_candidate_ignores_later_and_unpublished_releases(self):
        releases = [release(version) for version in ["0.5.3", "0.6.0", "0.6.1", "0.7.0"]]
        releases += [dict(release("0.6.2"), draft=True), dict(release("0.6.3"), prerelease=True)]
        self.assertEqual(BASELINE.previous_stable(releases, "0.6.1"), "0.6.0")
        self.assertEqual(BASELINE.previous_stable(releases, "0.6.1-rc.1"), "0.6.0")
        self.assertEqual(BASELINE.previous_stable(releases, "0.7.0-rc.1"), "0.6.1")

    def test_rejects_missing_baseline(self):
        with self.assertRaisesRegex(ValueError, "no published stable"):
            BASELINE.previous_stable([release("0.6.1")], "0.6.1")

    def test_provenance_rejects_mismatched_release_and_digest(self):
        image = {"name": "openbao-operator", "ref": "ghcr.io/kubebao/openbao-operator", "digest": "sha256:" + "a" * 64}
        image["oci_subject"] = image["ref"] + "@" + image["digest"]
        helper = dict(image, name="openbao-init", ref="ghcr.io/kubebao/openbao-init")
        helper["oci_subject"] = helper["ref"] + "@" + helper["digest"]
        index = {"release": {"tag": "0.6.0", "source_ref": "refs/tags/0.6.0"},
                 "chart": {"ref": "ghcr.io/kubebao/charts/openbao-operator"}, "images": [image, helper]}
        self.assertIn("OPERATOR_UPGRADE_BASELINE_MANAGER_IMAGE", BASELINE.release_pins(index, "0.6.0"))
        with self.assertRaisesRegex(ValueError, "does not match the baseline"):
            BASELINE.release_pins(index, "0.6.1")
        helper["oci_subject"] = helper["ref"] + "@sha256:" + "b" * 64
        with self.assertRaisesRegex(ValueError, "subject does not match"):
            BASELINE.release_pins(index, "0.6.0")

    def test_default_target_follows_manifest_without_another_pin(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ["hack/ci/operator-upgrade-e2e.sh", "hack/ci/operator-upgrade-jwt-migration.py",
                         "release-notes/0.5.1.md"]:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(ROOT / name, target)
            shutil.copytree(ROOT / "test/fixtures", root / "test/fixtures")
            (root / "test/e2e").mkdir(parents=True)
            shutil.copy(ROOT / "test/e2e/suites.yaml", root / "test/e2e/suites.yaml")
            env = dict(os.environ, OPERATOR_UPGRADE_E2E_VERIFY_ONLY="true",
                       OPERATOR_UPGRADE_E2E_TARGET_RELEASE="", OPERATOR_UPGRADE_E2E_TARGET_VERSION="")
            for version in ["0.6.1", "0.7.0-rc.1"]:
                (root / ".release-please-manifest.json").write_text(json.dumps({".": version}))
                result = subprocess.run(["bash", str(root / "hack/ci/operator-upgrade-e2e.sh")],
                                        env=env, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f" -> {version}-e2e (baseline chart:", result.stdout)

    def test_candidate_check_rejects_stale_pins(self):
        image = {"name": "openbao-operator", "ref": "ghcr.io/kubebao/openbao-operator", "digest": "sha256:" + "a" * 64}
        image["oci_subject"] = image["ref"] + "@" + image["digest"]
        helper = dict(image, name="openbao-init", ref="ghcr.io/kubebao/openbao-init")
        helper["oci_subject"] = helper["ref"] + "@" + helper["digest"]
        index = {"release": {"tag": "0.6.0", "source_ref": "refs/tags/0.6.0"},
                 "chart": {"ref": "ghcr.io/kubebao/charts/openbao-operator"}, "images": [image, helper]}
        responses = [[[release("0.6.0"), release("0.6.1")]],
                     {"tagName": "0.6.0", "isDraft": False, "isPrerelease": False}, index]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "test/fixtures/operator-upgrade/versions.env"
            fixture.parent.mkdir(parents=True)
            fixture.write_text("OPERATOR_UPGRADE_BASELINE=0.5.3\n")
            argv = ["operator-upgrade-baseline.py", "check", "--target", "0.6.1"]
            with patch.object(BASELINE, "ROOT", root), patch("sys.argv", argv):
                with patch.object(BASELINE, "gh", side_effect=responses):
                    with self.assertRaisesRegex(ValueError, "upgrade baseline is stale"):
                        BASELINE.main()
                pins = BASELINE.release_pins(index, "0.6.0")
                fixture.write_text("".join(f"{key}={value}\n" for key, value in pins.items()))
                with patch.object(BASELINE, "gh", side_effect=responses):
                    BASELINE.main()

    def test_baseline_requires_ready_pods_with_the_pinned_helper(self):
        source = (ROOT / "hack/ci/operator-upgrade-e2e.sh").read_text()
        function = "wait_for_baseline_pods() {" + source.split("wait_for_baseline_pods() {", 1)[1].split("\n}\n", 1)[0] + "\n}"
        expected = "ghcr.io/kubebao/openbao-init@sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / "kubectl"
            binary.write_text('#!/usr/bin/env bash\nprintf "%s" "${PODS_JSON}"\n')
            binary.chmod(0o700)
            for image, ready, count, success in [(expected, "True", 3, True),
                                                  (expected, "False", 3, False),
                                                  (expected, "True", 2, False),
                                                  (expected.replace("a" * 64, "b" * 64), "True", 3, False)]:
                with self.subTest(image=image, ready=ready, count=count):
                    pod = {"metadata": {}, "spec": {"initContainers": [{"name": "bao-config-init", "image": image}]},
                           "status": {"conditions": [{"type": "Ready", "status": ready}]}}
                    env = dict(os.environ, KUBECTL_BIN=str(binary), TENANT_NAMESPACE="upgrade-test",
                               DEFAULT_HARDENED_INIT_IMAGE=expected, FROM_VERSION="0.6.0",
                               PODS_JSON=json.dumps({"items": [pod] * count}))
                    script = function + '\nsleep() { SECONDS=$((SECONDS + 601)); }\nwait_for_baseline_pods test 3'
                    result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, success, result.stderr)


if __name__ == "__main__":
    unittest.main()
