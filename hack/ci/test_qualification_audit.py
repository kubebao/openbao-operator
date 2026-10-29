"""Reject incomplete, retried or substituted runtime evidence without workloads."""

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("audit", Path(__file__).with_name("qualification-audit.py"))
M = importlib.util.module_from_spec(spec)
spec.loader.exec_module(M)


class QualificationAuditTest(unittest.TestCase):
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
