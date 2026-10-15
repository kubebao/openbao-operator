#!/usr/bin/env python3
"""Check that network-lab cleanup requires the creating run's identity."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class NetworkKindCleanup(unittest.TestCase):
    def cleanup(self, *, expected="", actual="run-1", cluster="lab", receipt=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stub = root / "docker"
            stub.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
with Path(os.environ["COMMAND_LOG"]).open("a") as output:
    output.write(json.dumps([Path(sys.argv[0]).name, *sys.argv[1:]]) + "\\n")
if sys.argv[1:3] == ["network", "inspect"]:
    print(os.environ["ACTUAL_OWNER"] if "openbao.test.owner" in sys.argv[-1] else os.environ["ACTUAL_CLUSTER"])
''')
            stub.chmod(0o755)
            (root / "kind").symlink_to(stub)
            kubeconfig = root / "kubeconfig"
            if receipt:
                (root / "kubeconfig.owner").write_text(expected + "\n")
            env = {
                **os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                "KIND_CLUSTER": "lab", "KUBECONFIG": str(kubeconfig),
                "E2E_NETWORK_OWNER": "" if receipt else expected,
                "ACTUAL_OWNER": actual, "ACTUAL_CLUSTER": cluster, "COMMAND_LOG": str(root / "commands"),
            }
            result = subprocess.run(["bash", str(ROOT / "hack/ci/delete-network-kind.sh")],
                                    env=env, text=True, capture_output=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            return [json.loads(line) for line in (root / "commands").read_text().splitlines()]

    def test_unowned_or_different_run_is_preserved(self):
        for expected, actual, cluster in [("", "run-1", "lab"), ("run-2", "run-1", "lab"),
                                          ("run-1", "run-1", "other-lab")]:
            with self.subTest(expected=expected, actual=actual, cluster=cluster):
                commands = self.cleanup(expected=expected, actual=actual, cluster=cluster)
                self.assertTrue(all(command[0:3] == ["docker", "network", "inspect"] for command in commands))

    def test_only_the_owned_lab_is_deleted(self):
        for receipt in [False, True]:
            with self.subTest(receipt=receipt):
                commands = self.cleanup(expected="run-1", receipt=receipt)
                self.assertEqual(commands[-2:], [["kind", "delete", "cluster", "--name", "lab"],
                                                 ["docker", "network", "rm", "lab-network"]])


if __name__ == "__main__":
    unittest.main()
