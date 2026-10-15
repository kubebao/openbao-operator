#!/usr/bin/env python3
"""Exercise bundle identity, lane configuration, and real precompiled Ginkgo workers."""

import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("e2e_binaries", ROOT / "hack/ci/e2e-binaries.py")
binaries = importlib.util.module_from_spec(spec)
spec.loader.exec_module(binaries)


def command(args, cwd=ROOT, env=None):
    return subprocess.run(args, cwd=cwd, env={**os.environ, "GITHUB_ACTIONS": "false", **(env or {})},
                          text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


class BundleIdentity(unittest.TestCase):
    def setUp(self):
        scope = patch.dict(os.environ, {"GITHUB_ACTIONS": "false"})
        scope.start()
        self.addCleanup(scope.stop)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bundle = Path(self.tmp.name)
        for name in binaries.BINARIES:
            (self.bundle / name).write_bytes(b"fixture-executable")
        go, _ = binaries.versions()
        self.build_env = {"E2E_BUILD_GOOS": platform.system().lower(),
                          "E2E_BUILD_GOARCH": {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()],
                          "E2E_BUILD_GOVERSION": go, "E2E_BUILD_CGO": "0"}
        with patch.dict(os.environ, self.build_env):
            binaries.record(self.bundle, "source-sha")

    def test_identity_and_each_binary_are_required(self):
        original = json.loads((self.bundle / "metadata.json").read_text())
        binaries.verify(self.bundle, "source-sha")
        for field, value in {"version": 2, "sha": "old-sha", "goos": "wrong-os", "goarch": "wrong-arch",
                             "goVersion": "go0.0.0", "ginkgoVersion": "v0.0.0", "buildTags": [],
                             "cgoEnabled": None, "sha256": {}}.items():
            with self.subTest(field=field):
                changed = {**original, field: value}
                (self.bundle / "metadata.json").write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    binaries.verify(self.bundle, "source-sha")
        (self.bundle / "metadata.json").write_text(json.dumps(original))
        for name in binaries.BINARIES:
            with self.subTest(binary=name):
                (self.bundle / name).write_bytes(b"tampered")
                with self.assertRaises(ValueError):
                    binaries.verify(self.bundle, "source-sha")
                (self.bundle / name).unlink()
                with self.assertRaises(ValueError):
                    binaries.verify(self.bundle, "source-sha")
                (self.bundle / name).write_bytes(b"fixture-executable")
        with self.assertRaises(ValueError):
            binaries.verify(self.bundle, "")

    def test_wrong_build_toolchain_is_rejected(self):
        with patch.dict(os.environ, {**self.build_env, "E2E_BUILD_GOVERSION": "go0.0.0"}):
            with self.assertRaises(ValueError):
                binaries.record(self.bundle, "source-sha")


class SourceArguments(unittest.TestCase):
    def test_source_runner_quotes_filters_and_report_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            ginkgo = root / "ginkgo"
            ginkgo.write_text("#!/usr/bin/env python3\nimport json,os,sys\n"
                              "open(os.environ['ARGV_LOG'],'w').write(json.dumps(sys.argv[1:]))\n")
            ginkgo.chmod(0o755)
            focus = "quotes ' and $(touch unexpected)"
            env = {"GINKGO": str(ginkgo), "ARGV_LOG": str(root / "args"), "E2E_BINARY_DIR": "",
                   "E2E_LABEL_FILTER": "smoke && !other", "E2E_FOCUS": focus, "E2E_TIMEOUT": "13m",
                   "E2E_PARALLEL_NODES": "2", "E2E_FAIL_ON_EMPTY": "true", "E2E_TRACE": "true",
                   "E2E_NO_COLOR": "true", "E2E_KEEP_GOING": "true", "E2E_DRY_RUN": "true",
                   "E2E_JSON_REPORT": "reports with spaces/ginkgo.json", "E2E_POLL_PROGRESS_AFTER": "2m"}
            result = command(["bash", str(ROOT / "hack/ci/run-e2e.sh")], cwd=root, env=env)
            self.assertEqual(result.returncode, 0, result.stdout)
            args = json.loads((root / "args").read_text())
            for arg in ("--focus=" + focus, "--label-filter=smoke && !other", "--procs=1", "--timeout=13m",
                        "--trace", "--no-color", "--keep-going", "--dry-run", "--fail-on-empty",
                        "--poll-progress-after=2m", "--json-report=reports with spaces/ginkgo.json", "--output-dir=" + str(root),
                        "--tags=e2e", "./test/e2e/"):
                self.assertIn(arg, args)
            self.assertFalse((root / "unexpected").exists())


class LaneConfiguration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        result = command(["go", "run", "-mod=vendor", "./hack/tools/e2e_plan"])
        if result.returncode:
            raise RuntimeError(result.stdout)
        cls.rows = json.loads(result.stdout)["include"]

    def configure(self, row, **overrides):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            env = {
                "LANE_JSON": json.dumps(row), "GITHUB_ENV": str(root / "environment"),
                "GITHUB_SHA": "source", "RUNNER_TEMP": str(root), "REPORT_DIR": str(root / "reports"),
                "ARTIFACT_NAME": "e2e-lane-2", "MANAGER_IMAGE": "manager:run", "CONFIG_INIT_IMAGE": "init:run",
                "BACKUP_EXECUTOR_IMAGE": "backup:run", "UPGRADE_EXECUTOR_IMAGE": "upgrade:run",
                "HARDENED_INIT_IMAGE": "", "HARDENED_UPGRADE_IMAGE": "", "OPENBAO_IMAGE": "",
                "KMIP_SERVER_IMAGE": "", "RELEASE_OPERATOR_VERSION": "", **overrides,
            }
            result = command(["python3", "hack/ci/configure-e2e-lane.py"], env=env)
            if result.returncode:
                return result, {}
            values = dict(line.split("=", 1) for line in (root / "environment").read_text().splitlines())
            self.assertEqual(json.loads((root / "reports/expected-cases.json").read_text()), row["expected_case_ids"])
            return result, values

    def test_every_lane_retains_its_execution_settings(self):
        for row in self.rows:
            with self.subTest(lane=row["id"]):
                result, values = self.configure(row, OPENBAO_IMAGE="softhsm:run" if row["id"] == "hsm" else "",
                                                KMIP_SERVER_IMAGE="kmip:run" if row["id"] == "hsm" else "")
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertEqual(values["E2E_LABEL_FILTER"], row["label_filter"])
                self.assertEqual(json.loads(values["EXPECTED_CASE_IDS"]), row["expected_case_ids"])
                self.assertEqual(values["E2E_PARALLEL_NODES"], str(row["parallel_nodes"]))
                self.assertEqual(values["E2E_TIMEOUT"], row["e2e_timeout"])
                self.assertEqual(values["E2E_ENABLE_HARDENED_SIGNED_SUITE"], row["hardened_signed"])
                for field in ("load_backup_executor_image", "load_upgrade_executor_image", "preload_upgrade_images",
                              "preload_hardened_assets", "preload_storage_emulators", "preload_rustfs_image",
                              "preload_fake_gcs_image", "preload_azurite_image", "install_csi_hostpath"):
                    self.assertEqual(values["E2E_" + field.upper()], row[field])
                self.assertEqual(values["E2E_HARDENED_CONFIG_INIT_IMAGE"], row["hardened_init_image"])
                self.assertEqual(values["E2E_HARDENED_UPGRADE_EXECUTOR_IMAGE"], row["hardened_upgrade_executor_image"])
                self.assertEqual(values["E2E_UPGRADE_EXECUTOR_IMAGE"], row["hardened_upgrade_executor_image"] or "upgrade:run")
                self.assertEqual(values["E2E_ENABLE_SOFTHSM_SUITE"], str(row["id"] == "hsm").lower())
                self.assertEqual(values["E2E_ENABLE_KMIP_SUITE"], str(row["id"] == "hsm").lower())
                self.assertEqual(values["E2E_FAIL_ON_EMPTY"], "true")
                self.assertEqual(values["E2E_SKIP_CLEANUP"], "true")
                self.assertEqual(values["E2E_DRY_RUN"], "false")

    def test_seal_references_and_release_overrides(self):
        row = next(row for row in self.rows if row["id"] == "hsm")
        result, _ = self.configure(row)
        self.assertNotEqual(result.returncode, 0)
        row = next(row for row in self.rows if row["id"] == "hardened")
        result, values = self.configure(row, HARDENED_INIT_IMAGE="init:release", HARDENED_UPGRADE_IMAGE="upgrade:release",
                                        RELEASE_OPERATOR_VERSION="release-tag")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(values["E2E_HARDENED_CONFIG_INIT_IMAGE"], "init:release")
        self.assertEqual(values["E2E_UPGRADE_EXECUTOR_IMAGE"], "upgrade:release")
        self.assertEqual(values["OPERATOR_VERSION"], "release-tag")
        result, _ = self.configure({**row, "expected_case_ids": []})
        self.assertNotEqual(result.returncode, 0)


class PrecompiledWorkers(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        artifacts = ROOT / "artifacts"
        artifacts.mkdir(exist_ok=True)
        cls.build = tempfile.TemporaryDirectory(prefix="_e2e-worker-fixture-", dir=artifacts)
        cls.build_root = Path(cls.build.name)
        fixture = cls.build_root / "fixture"
        fixture.mkdir()
        (fixture / "fixture_test.go").write_text('''package fixture
import (
    "fmt"
    "os"
    "os/exec"
    "testing"
    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "github.com/kubebao/openbao-operator/test/utils"
)
func TestFixture(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "fixture") }
func mark(value string) {
    f, err := os.OpenFile(os.Getenv("FIXTURE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
    Expect(err).NotTo(HaveOccurred()); defer f.Close()
    _, err = fmt.Fprintln(f, value); Expect(err).NotTo(HaveOccurred())
}
var _ = SynchronizedBeforeSuite(func() []byte { mark("setup"); return nil }, func(_ []byte) {
    cwd, err := os.Getwd(); Expect(err).NotTo(HaveOccurred())
    mark("cwd:"+cwd); mark(fmt.Sprintf("worker:%d", GinkgoParallelProcess()))
})
var _ = SynchronizedAfterSuite(func() {}, func() { mark("cleanup") })
var _ = Describe("fixture", Label("fixture"), func() {
    It("accepts quotes and $(touch unexpected)", Label("smoke", "case:fixture-selected"), func() {
        root, err := utils.GetProjectDir(); Expect(err).NotTo(HaveOccurred()); mark("root:"+root)
        out, err := utils.Run(exec.Command("pwd")); Expect(err).NotTo(HaveOccurred())
        Expect(out).To(Equal(root+"\\n"))
        if os.Getenv("FIXTURE_SKIP") == "true" { Skip("selected skip") }
        if os.Getenv("FIXTURE_FAIL") == "true" { Fail("selected failure") }
    })
    It("other case", Label("other", "case:fixture-other"), func() {})
    It("serial case", Serial, Label("serial", "case:fixture-serial"), func() { mark("serial") })
})
''')
        cls.bundle = cls.build_root / "bundle"
        cls.bundle.mkdir()
        for args in (
            ["go", "test", "-c", "-mod=vendor", "-o", str(cls.bundle / "e2e.test"), str(fixture)],
            ["go", "build", "-mod=vendor", "-o", str(cls.bundle / "ginkgo"), "github.com/onsi/ginkgo/v2/ginkgo"],
            ["go", "build", "-mod=vendor", "-o", str(cls.bundle / "e2e_report"), "./hack/tools/e2e_report"],
        ):
            result = command(args)
            if result.returncode:
                cls.build.cleanup()
                raise RuntimeError(result.stdout)
        cls.build_env = {"GITHUB_ACTIONS": "false", "E2E_BUILD_GOOS": platform.system().lower(),
                         "E2E_BUILD_GOARCH": command(["go", "env", "GOARCH"]).stdout.strip(),
                         "E2E_BUILD_GOVERSION": command(["go", "env", "GOVERSION"]).stdout.strip(),
                         "E2E_BUILD_CGO": command(["go", "env", "CGO_ENABLED"]).stdout.strip()}
        with patch.dict(os.environ, cls.build_env):
            binaries.record(cls.bundle, "fixture-source")

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def run_fixture(self, nodes=1, selector="fixture", **overrides):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name).resolve()
        (root / "test/e2e").mkdir(parents=True)
        (root / "hack/ci").mkdir(parents=True)
        (root / "go.mod").write_bytes((ROOT / "go.mod").read_bytes())
        (root / "vendor").symlink_to(ROOT / "vendor", target_is_directory=True)
        (root / "test/e2e/fixture_test.go").write_bytes((self.build_root / "fixture/fixture_test.go").read_bytes())
        for name in ("run-e2e.sh", "e2e-binaries.py", "e2e_artifacts.py"):
            shutil.copyfile(ROOT / "hack/ci" / name, root / "hack/ci" / name)
        # Ginkgo queries go list for its version check. Any compilation or
        # Go compilation and report commands fail here, so success proves binary reuse.
        fake_bin = root / "bin"
        fake_bin.mkdir()
        real_go = shutil.which("go")
        (fake_bin / "go").write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$GO_CALLS"\n'
                                    'test "$1" = list || exit 1\nexec "' + real_go + '" "$@"\n')
        (fake_bin / "go").chmod(0o755)
        env = {"PATH": str(fake_bin) + os.pathsep + os.environ["PATH"], "GO_CALLS": str(root / "go-calls"),
               "GOFLAGS": "-mod=vendor", "E2E_BINARY_DIR": str(self.bundle), "E2E_SOURCE_SHA": "fixture-source", "E2E_PARALLEL_NODES": str(nodes),
               "E2E_TIMEOUT": "1m", "E2E_LABEL_FILTER": selector, "E2E_FAIL_ON_EMPTY": "true", "E2E_NO_COLOR": "true",
               "E2E_JSON_REPORT": "reports/ginkgo.json", "E2E_JUNIT_REPORT": "reports/junit.xml",
               "E2E_GOJSON_REPORT": "reports/go.json", "FIXTURE_LOG": str(root / "workers"), **overrides}
        result = command(["bash", "hack/ci/run-e2e.sh"], cwd=root, env=env)
        for invocation in (root / "go-calls").read_text().splitlines() if (root / "go-calls").exists() else []:
            self.assertEqual(invocation.split()[0], "list")
        self.assertFalse((root / "unexpected").exists())
        return result, root

    def test_serial_and_parallel_setup_cleanup_and_reports(self):
        for nodes in (1, 2):
            with self.subTest(nodes=nodes):
                result, root = self.run_fixture(nodes=nodes)
                self.assertEqual(result.returncode, 0, result.stdout)
                lines = (root / "workers").read_text().splitlines()
                self.assertEqual(lines.count("setup"), 1)
                self.assertEqual(lines.count("cleanup"), 1)
                self.assertEqual(lines.count("serial"), 1)
                self.assertEqual(lines.count("cwd:" + str(root / "test/e2e")), nodes)
                self.assertIn("root:" + str(root), lines)
                self.assertEqual(len([line for line in lines if line.startswith("worker:")]), nodes)
                self.assertTrue((root / "reports/junit.xml").is_file())
                self.assertTrue((root / "reports/go.json").is_file())
                reports = json.loads((root / "reports/ginkgo.json").read_text())
                self.assertEqual(len(reports), 1)
                self.assertEqual(reports[0]["PreRunStats"]["SpecsThatWillRun"], 3)
                selected = [spec for spec in reports[0]["SpecReports"] if spec["LeafNodeType"] == "It" and spec["State"] == "passed"]
                self.assertEqual(len(selected), 3)

    def test_dry_run_is_serial_and_does_not_run_hooks(self):
        result, root = self.run_fixture(nodes=2, E2E_DRY_RUN="true")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse((root / "workers").exists())
        report = json.loads((root / "reports/ginkgo.json").read_text())[0]
        self.assertTrue(report["SuiteConfig"]["DryRun"])
        self.assertEqual(report["SuiteConfig"]["ParallelTotal"], 1)

    def test_filters_empty_selection_skips_and_failures(self):
        result, root = self.run_fixture(nodes=2, selector="smoke", E2E_FOCUS=re.escape("accepts quotes and $(touch unexpected)"))
        self.assertEqual(result.returncode, 0, result.stdout)
        reports = json.loads((root / "reports/ginkgo.json").read_text())
        self.assertEqual(reports[0]["PreRunStats"]["SpecsThatWillRun"], 1)
        result, _ = self.run_fixture(selector="absent-label")
        self.assertNotEqual(result.returncode, 0)
        result, root = self.run_fixture(selector="smoke", FIXTURE_SKIP="true")
        self.assertEqual(result.returncode, 0, result.stdout)
        expected = root / "expected.json"
        expected.write_text('["fixture-selected"]')
        result = command([str(self.bundle / "e2e_report"), "--json-report", str(root / "reports/ginkgo.json"),
                          "--selector", "smoke", "--expected-cases", str(expected)])
        self.assertNotEqual(result.returncode, 0, result.stdout)
        result, _ = self.run_fixture(selector="smoke", FIXTURE_FAIL="true")
        self.assertNotEqual(result.returncode, 0)
        result, root = self.run_fixture(E2E_SOURCE_SHA="wrong-source")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((root / "workers").exists())


if __name__ == "__main__":
    unittest.main()
