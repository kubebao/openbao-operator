"""Validate candidate execution bindings without credentials, clusters or registry writes."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("runtime", Path(__file__).with_name("qualification-runtime.py"))
M = importlib.util.module_from_spec(spec)
spec.loader.exec_module(M)


class QualificationRuntimeTest(unittest.TestCase):
    def test_binary_receipt_binds_actual_bytes_candidate_and_attempt(self):
        plan = {"sources": {"candidateSHA": "a" * 40}, "context": {"runID": 42, "attempt": 1}}
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            for name in ("e2e.test", "ginkgo"):
                (root / name).write_bytes(name.encode())
            record = M.receipt(plan, "compile", sha256={name: M.O.file_digest(root / name)
                                                       for name in ("e2e.test", "ginkgo")})
            (root / "binaries.json").write_text(json.dumps(record))
            self.assertEqual(M.verify_binaries(plan, root), record)
            for changed in ({**plan, "context": {"runID": 42, "attempt": 2}},
                            {**plan, "sources": {"candidateSHA": "b" * 40}}):
                with self.assertRaises(ValueError):
                    M.verify_binaries(changed, root)
            (root / "e2e.test").write_bytes(b"changed")
            with self.assertRaises(ValueError):
                M.verify_binaries(plan, root)

    def test_all_upgrade_fixture_image_scalars_are_frozen(self):
        import re
        for fixture in (M.ROOT / "test/fixtures/operator-upgrade").glob("*.yaml"):
            text = fixture.read_text()
            declared = re.findall(r"^\s*image:\s*[\"']?([^\s\"'#]+)", text, re.MULTILINE)
            images = {ref: ref if "@sha256:" in ref else ref + "@sha256:" + "a" * 64 for ref in declared}
            frozen = M.freeze_fixture_images(text, images)
            for actual in re.findall(r"^\s*image:\s*[\"']?([^\s\"'#]+)", frozen, re.MULTILINE):
                self.assertIn("@sha256:", actual)
            if images:
                with self.assertRaises(ValueError):
                    M.freeze_fixture_images(text, {})

    def test_unknown_or_duplicate_row_is_rejected(self):
        row = {"qualification_row_key": "a" * 64}
        self.assertEqual(M.selected_row({"matrix": {"include": [row]}}, "a" * 64), row)
        for rows in ([], [row, row]):
            with self.assertRaises(ValueError):
                M.selected_row({"matrix": {"include": rows}}, "a" * 64)

    def test_pull_auth_is_private_and_not_in_the_artifact_tree(self):
        with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {
            "RUNNER_TEMP": folder, "GH_TOKEN": "test-read-only-token", "GITHUB_ACTOR": "fixture"}):
            path = Path(M.auth_file())
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(set(json.loads(path.read_text())["auths"]), {"ghcr.io"})
            with self.assertRaises(FileExistsError):
                M.auth_file()


if __name__ == "__main__":
    unittest.main()
