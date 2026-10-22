#!/usr/bin/env python3
"""Check network-lab setup and ownership-scoped cleanup."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class NetworkKindSetup(unittest.TestCase):
    def create(self, profile, *, dns_ready=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stub = root / "docker"
            stub.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
tool = Path(sys.argv[0]).name
args = sys.argv[1:]
with Path(os.environ["COMMAND_LOG"]).open("a") as output:
    output.write(json.dumps([tool, *args]) + "\\n")
if tool == "docker":
    if args[:2] == ["network", "inspect"]:
        sys.exit(1)
    if args[:1] == ["inspect"]:
        print("fd00::2" if "GlobalIPv6Address" in args[-1] else "172.18.0.2")
if tool == "kubectl" and "rollout" in args and os.environ["DNS_READY"] == "false":
    sys.exit(1)
''')
            stub.chmod(0o755)
            for name in ["kind", "helm", "kubectl"]:
                (root / name).symlink_to(stub)
            env = {
                **os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                "KIND_CLUSTER": "lab", "KIND_NODE_IMAGE": "kindest/node:v1.36.1",
                "KUBECONFIG": str(root / "kubeconfig"), "E2E_NETWORK_PROFILE": profile,
                "E2E_REPORT_DIR": str(root / "report"), "COMMAND_LOG": str(root / "commands"),
                "DNS_READY": str(dns_ready).lower(),
            }
            result = subprocess.run(["bash", str(ROOT / "hack/ci/create-network-kind.sh")],
                                    cwd=ROOT, env=env, text=True, capture_output=True, check=False)
            commands = [json.loads(line) for line in (root / "commands").read_text().splitlines()]
            return result, (root / "report/cilium.yaml").read_text(), commands

    def test_profiles_use_initial_tunnels_and_wait_for_pod_network(self):
        for profile in ["ipv4", "ipv6", "dual-ipv4", "dual-ipv6"]:
            with self.subTest(profile=profile):
                result, values, commands = self.create(profile)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("routingMode: tunnel\n", values)
                self.assertIn("tunnelProtocol: vxlan\n", values)
                self.assertIn("autoDirectNodeRoutes: false\n", values)
                self.assertIn("bpf:\n  masquerade: true\n", values)
                self.assertIn('enable-remote-node-masquerade: "true"\n', values)
                self.assertIn(f"ipv4:\n  enabled: {str(profile != 'ipv6').lower()}\n", values)
                self.assertIn(f"ipv6:\n  enabled: {str(profile != 'ipv4').lower()}\n", values)
                self.assertIn('k8sServiceHost: "' + (
                    "fd00::2" if profile in ["ipv6", "dual-ipv6"] else "172.18.0.2") + '"', values)
                install = next(c for c in commands if c[:4] == ["helm", "upgrade", "--install", "cilium"])
                self.assertIn("--kubeconfig", install)
                ready = next(i for i, c in enumerate(commands) if "rollout" in c)
                self.assertIn("deployment/coredns", commands[ready])
                self.assertTrue(any("wait" in c for c in commands[:ready]))
                self.assertTrue(any("nodes" in c and "get" in c for c in commands[ready + 1:]))

    def test_unready_dns_fails_setup(self):
        result, _, commands = self.create("ipv6", dns_ready=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any("get" in c and "nodes" in c for c in commands))


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
