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
                 "chart": {"ref": "ghcr.io/kubebao/charts/openbao-operator", "digest": "sha256:" + "a" * 64,
                           "oci_subject": "ghcr.io/kubebao/charts/openbao-operator@sha256:" + "a" * 64},
                 "images": [image, helper]}
        self.assertIn("OPERATOR_UPGRADE_BASELINE_MANAGER_IMAGE", BASELINE.release_pins(index, "0.6.0"))
        with self.assertRaisesRegex(ValueError, "does not match the baseline"):
            BASELINE.release_pins(index, "0.6.1")
        helper["oci_subject"] = helper["ref"] + "@sha256:" + "b" * 64
        with self.assertRaisesRegex(ValueError, "subject does not match"):
            BASELINE.release_pins(index, "0.6.0")

    def test_chart_digest_must_match_its_provenance_subject(self):
        chart = "ghcr.io/kubebao/charts/openbao-operator"
        for digest, subject, message in [
            ("not-a-digest", chart + "@sha256:" + "a" * 64, "chart digest is invalid"),
            ("sha256:" + "a" * 64, chart + "@sha256:" + "b" * 64, "chart subject does not match"),
        ]:
            with self.subTest(digest=digest, subject=subject):
                index = {"release": {"tag": "0.6.0", "source_ref": "refs/tags/0.6.0"},
                         "chart": {"ref": chart, "digest": digest, "oci_subject": subject}, "images": []}
                with self.assertRaisesRegex(ValueError, message):
                    BASELINE.release_pins(index, "0.6.0")

    def test_refresh_updates_pins_and_hardened_fixture_together(self):
        old = "ghcr.io/kubebao/openbao-init@sha256:" + "b" * 64
        digest = "sha256:" + "a" * 64
        chart = "ghcr.io/kubebao/charts/openbao-operator"
        images = [{"name": name, "ref": "ghcr.io/kubebao/" + name, "digest": digest,
                   "oci_subject": "ghcr.io/kubebao/" + name + "@" + digest}
                  for name in ["openbao-operator", "openbao-init"]]
        index = {"release": {"tag": "0.6.0", "source_ref": "refs/tags/0.6.0"},
                 "chart": {"ref": chart, "digest": digest, "oci_subject": chart + "@" + digest},
                 "images": images}
        responses = [{"tagName": "0.6.0", "isDraft": False, "isPrerelease": False}, index]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "test/fixtures/operator-upgrade/hardened-cluster.yaml"
            fixture.parent.mkdir(parents=True)
            fixture.write_text('spec:\n  initContainer:\n    image: "' + old + '"\n')
            pins = fixture.with_name("versions.env")
            pins.write_text("OPERATOR_UPGRADE_BASELINE=0.5.3\n"
                            "OPERATOR_UPGRADE_LEGACY_SEED=0.5.3\n"
                            "OPERATOR_UPGRADE_BASELINE_INIT_IMAGE=" + old + "\n")
            argv = ["operator-upgrade-baseline.py", "refresh", "--version", "0.6.0"]
            with patch.object(BASELINE, "ROOT", root), patch("sys.argv", argv), \
                    patch.object(BASELINE, "gh", side_effect=responses):
                BASELINE.main()
            expected = BASELINE.release_pins(index, "0.6.0")
            current = BASELINE.read_pins(pins)
            for key, value in expected.items():
                self.assertEqual(current[key], value)
            self.assertEqual(current["OPERATOR_UPGRADE_LEGACY_SEED"], "0.5.3")
            self.assertIn('    image: "' + expected["OPERATOR_UPGRADE_BASELINE_INIT_IMAGE"] + '"', fixture.read_text())
            self.assertNotIn(old, fixture.read_text())

    def test_default_target_follows_manifest_without_another_pin(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ["hack/ci/operator-upgrade-e2e.sh", "hack/ci/operator-upgrade-jwt-migration.py",
                         "release-notes/0.5.1.md"]:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(ROOT / name, target)
            shutil.copytree(ROOT / "test/fixtures", root / "test/fixtures")
            pins = root / "test/fixtures/operator-upgrade/versions.env"
            pins.write_text("\n".join(line for line in pins.read_text().splitlines()
                                      if not line.startswith("OPERATOR_UPGRADE_TARGET_RELEASE=")) + "\n")
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
            pins.write_text(pins.read_text() + "OPERATOR_UPGRADE_TARGET_RELEASE=0.7.0\n")
            for override, expected in [("", "0.7.0"), ("9.8.7-rc.1", "9.8.7-rc.1")]:
                env["OPERATOR_UPGRADE_E2E_TARGET_RELEASE"] = override
                result = subprocess.run(["bash", str(root / "hack/ci/operator-upgrade-e2e.sh")],
                                        env=env, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f" -> {expected}-e2e (baseline chart:", result.stdout)

    def test_candidate_check_rejects_stale_pins(self):
        image = {"name": "openbao-operator", "ref": "ghcr.io/kubebao/openbao-operator", "digest": "sha256:" + "a" * 64}
        image["oci_subject"] = image["ref"] + "@" + image["digest"]
        helper = dict(image, name="openbao-init", ref="ghcr.io/kubebao/openbao-init")
        helper["oci_subject"] = helper["ref"] + "@" + helper["digest"]
        index = {"release": {"tag": "0.6.0", "source_ref": "refs/tags/0.6.0"},
                 "chart": {"ref": "ghcr.io/kubebao/charts/openbao-operator", "digest": "sha256:" + "a" * 64,
                           "oci_subject": "ghcr.io/kubebao/charts/openbao-operator@sha256:" + "a" * 64},
                 "images": [image, helper]}
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

    def test_hardened_wait_requires_complete_voter_rollout(self):
        source = (ROOT / "hack/ci/operator-upgrade-e2e.sh").read_text()
        functions = "\n".join(
            name + "() {" + source.split(name + "() {", 1)[1].split("\n}\n", 1)[0] + "\n}"
            for name in ("wait_for_workloads_settled", "wait_for_hardened_cluster")
        )
        cases = [
            ("partition holds all old voters", 3, 0, "old", 2, False),
            ("partition holds remaining old voters", 2, 1, "old", 2, False),
            ("latest generation is not observed", 0, 3, "candidate", 1, False),
            ("all voters use the current revision", 0, 3, "candidate", 2, True),
        ]
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / "kubectl"
            binary.write_text('#!/usr/bin/env bash\n'
                              'if [[ "$1" == get && "$2" == statefulsets ]]; then\n'
                              '  printf "%s" "${STATEFULSETS_JSON}"\n'
                              'fi\n')
            binary.chmod(0o700)
            for description, partition, updated, revision, observed, success in cases:
                with self.subTest(description=description):
                    statefulset = {
                        "metadata": {"name": "operator-upgrade-hardened", "generation": 2},
                        "spec": {"replicas": 3, "updateStrategy": {"type": "RollingUpdate",
                                                                  "rollingUpdate": {"partition": partition}}},
                        "status": {"observedGeneration": observed, "readyReplicas": 3,
                                   "updatedReplicas": updated, "currentRevision": revision,
                                   "updateRevision": "candidate"},
                    }
                    env = dict(os.environ, KUBECTL_BIN=str(binary), TENANT_NAMESPACE="upgrade-test",
                               HARDENED_CLUSTER_RESOURCE="openbaocluster/operator-upgrade-hardened",
                               STATEFULSETS_JSON=json.dumps({"items": [statefulset]}),
                               WAIT_STEP="5" if success else "901")
                    # Conditions and kubectl's partition-aware rollout status already succeed.
                    script = 'set -euo pipefail\n' + functions + (
                        '\nsleep() { SECONDS=$((SECONDS + WAIT_STEP)); }\n'
                        'wait_for_hardened_cluster\necho hardened-ready\n'
                    )
                    result = subprocess.run(["bash", "-c", script], env=env,
                                            capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, success, result.stderr)
                    self.assertEqual("hardened-ready" in result.stdout, success)
                    if not success:
                        self.assertIn("fixture StatefulSets did not settle", result.stderr)

    def test_settlement_restarts_after_incomplete_or_failed_samples(self):
        source = (ROOT / "hack/ci/operator-upgrade-e2e.sh").read_text()
        functions = "\n".join(
            name + "() {" + source.split(name + "() {", 1)[1].split("\n}\n", 1)[0] + "\n}"
            for name in ("wait_for_workloads_settled", "wait_for_hardened_cluster")
        )
        settled = {
            "metadata": {"name": "operator-upgrade-hardened", "generation": 2},
            "spec": {"replicas": 3},
            "status": {"observedGeneration": 2, "readyReplicas": 3, "updatedReplicas": 3,
                       "currentRevision": "candidate", "updateRevision": "candidate"},
        }
        complete = {"items": [settled]}
        incomplete = {"items": [{**settled, "status": {**settled["status"], "updatedReplicas": 2}}]}
        other_fixtures = {"items": [{**settled, "metadata": {**settled["metadata"], "name": name}}
                                    for name in ("operator-upgrade", "operator-upgrade-transit")]}
        cases = [
            ("incomplete poll restarts stability", [complete] * 3 + [incomplete] + [complete] * 7, 5, True, 11),
            ("failed poll restarts stability", [complete] * 3 + [None] + [complete] * 7, 5, True, 11),
            ("missing Hardened fixture", [other_fixtures], 100, False, 9),
            ("empty list", [{"items": []}], 100, False, 9),
            ("persistent failure respects remaining budget", [None], 890, False, 2),
        ]
        with tempfile.TemporaryDirectory() as directory:
            count_file = Path(directory) / "count"
            args_file = Path(directory) / "args.jsonl"
            binary = Path(directory) / "kubectl"
            binary.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
if sys.argv[1:3] == ["get", "statefulsets"]:
    count_file = pathlib.Path(os.environ["POLL_COUNT_FILE"])
    index = int(count_file.read_text())
    count_file.write_text(str(index + 1))
    with open(os.environ["POLL_ARGS_FILE"], "a") as output:
        output.write(json.dumps(sys.argv[1:]) + "\\n")
    polls = json.loads(os.environ["SETTLEMENT_POLLS"])
    sample = polls[min(index, len(polls) - 1)]
    if sample is None:
        print("temporary API list failure", file=sys.stderr)
        sys.exit(1)
    print(json.dumps(sample))
''')
            binary.chmod(0o700)
            for description, samples, step, success, expected_polls in cases:
                with self.subTest(description=description):
                    count_file.write_text("0")
                    args_file.write_text("")
                    env = dict(os.environ, KUBECTL_BIN=str(binary), TENANT_NAMESPACE="upgrade-test",
                               HARDENED_CLUSTER_RESOURCE="openbaocluster/operator-upgrade-hardened",
                               SETTLEMENT_POLLS=json.dumps(samples), WAIT_STEP=str(step),
                               POLL_COUNT_FILE=str(count_file), POLL_ARGS_FILE=str(args_file))
                    script = 'set -euo pipefail\nSECONDS=0\n' + functions + (
                        '\nsleep() { SECONDS=$((SECONDS + WAIT_STEP)); }\n'
                        'wait_for_hardened_cluster\necho hardened-ready\n'
                    )
                    result = subprocess.run(["bash", "-c", script], env=env,
                                            capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, success, result.stderr)
                    self.assertEqual("hardened-ready" in result.stdout, success)
                    self.assertEqual(int(count_file.read_text()), expected_polls)
                    requests = [json.loads(line) for line in args_file.read_text().splitlines()]
                    timeouts = []
                    for args in requests:
                        values = [arg.split("=", 1)[1] for arg in args if arg.startswith("--request-timeout=")]
                        self.assertEqual(len(values), 1, args)
                        self.assertTrue(values[0].endswith("s"))
                        timeout = int(values[0][:-1])
                        self.assertGreater(timeout, 0)
                        self.assertLessEqual(timeout, 30)
                        timeouts.append(timeout)
                    if step == 890:
                        self.assertEqual(timeouts[0], 30)
                        self.assertLessEqual(timeouts[-1], 10)
                    if None in samples:
                        self.assertIn("temporary API list failure", result.stderr)
                    if not success:
                        self.assertIn("fixture StatefulSets did not settle", result.stderr)


if __name__ == "__main__":
    unittest.main()
