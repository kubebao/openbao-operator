#!/usr/bin/env python3
"""Exercise release authorization and publication steps without remote writes."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
SHA = "a" * 40


def step(workflow, name):
    lines = (ROOT / ".github/workflows" / workflow).read_text().splitlines()
    start = lines.index("      - name: " + name)
    start = next(i + 1 for i in range(start, len(lines)) if lines[i] == "        run: |")
    end = next((i for i in range(start, len(lines))
                if lines[i].strip() and not lines[i].startswith("          ")), len(lines))
    return textwrap.dedent("\n".join(lines[start:end]))


class ReleaseAuthorization(unittest.TestCase):
    def test_solo_and_reviewed_release_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "gh"
            binary.write_text('''#!/usr/bin/env python3
import json, os, sys
sha = os.environ['HEAD_SHA']
case = os.environ['CASE']
endpoint = next(arg for arg in sys.argv[1:] if arg.startswith('repos/'))
if endpoint.endswith('/labels'):
    print('release:ready')
elif endpoint.endswith('/pulls/7'):
    print(json.dumps({'head': {'sha': sha}, 'user': {'login': 'dc-tec' if case.startswith('solo') else 'release-bot'}}))
elif endpoint.endswith('/reviews'):
    if case != 'missing_review':
        print(json.dumps({'user': {'login': 'dc-tec'}, 'state': 'APPROVED',
                          'commit_id': 'b' * 40 if case == 'stale_review' else sha, 'id': 1}))
elif '/runs?' in endpoint:
    print(json.dumps({'workflow_runs': [{'id': 101, 'path': '.github/workflows/release-candidate.yml',
        'head_sha': 'b' * 40 if case == 'solo_changed_head' else sha, 'event': 'pull_request',
        'conclusion': 'success', 'head_repository': {'full_name': os.environ['REPO']}}]}))
elif '/jobs?' in endpoint:
    print(json.dumps({'jobs': [{'name': 'Qualified Release Candidate',
        'conclusion': 'skipped' if case == 'solo_skipped' else 'success'}]}))
elif endpoint.endswith('/approvals'):
    print(json.dumps([] if case == 'solo_no_approval' else [{'state': 'approved',
        'user': {'login': 'dc-tec', 'type': 'User'}, 'environments': [{'name': 'release-candidate'}]}]))
else:
    raise SystemExit('unexpected endpoint: ' + endpoint)
''')
            binary.chmod(0o755)
            (root / "release-pr-files.txt").write_text(".release-please-manifest.json\nCHANGELOG.md\n")
            for case, success in [("solo", True), ("reviewed", True), ("missing_review", False),
                                  ("stale_review", False), ("solo_no_approval", False),
                                  ("solo_changed_head", False), ("solo_skipped", False)]:
                with self.subTest(case=case):
                    env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                               REPO="kubebao/openbao-operator", PR_NUMBER="7", HEAD_SHA=SHA,
                               RUNNER_TEMP=str(root), REQUIRED_LABEL="release:ready",
                               REQUIRED_APPROVER="dc-tec", CASE=case)
                    result = subprocess.run(["bash", "-c", step("release-pr-gate.yml", "Evaluate release PR gate")],
                                            cwd=ROOT, env=env, text=True, capture_output=True)
                    self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                    if success:
                        self.assertIn("Qualified by Release Candidate run 101", result.stdout)
                    elif case in ("missing_review", "stale_review"):
                        self.assertIn("Missing current-head approval", result.stderr)
                    elif case == "solo_no_approval":
                        self.assertIn("requires an approval from dc-tec", result.stderr)
                    else:
                        self.assertIn("no successful 'Qualified Release Candidate' job", result.stderr)

    def test_environment_approval_is_specific(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "gh"
            binary.write_text("#!/usr/bin/env bash\nprintf '%s' \"${REVIEWS}\"\n")
            binary.chmod(0o755)
            for environment in ["release-candidate", "release-publish"]:
                for state, user, kind, target, expected in [
                    ("approved", "dc-tec", "User", environment, True),
                    ("rejected", "dc-tec", "User", environment, False),
                    ("approved", "other", "User", environment, False),
                    ("approved", "dc-tec", "Bot", environment, False),
                    ("approved", "dc-tec", "User", "other", False),
                ]:
                    with self.subTest(environment=environment, state=state, user=user, kind=kind, target=target):
                        reviews = [{"state": state, "user": {"login": user, "type": kind},
                                    "environments": [{"name": target}]}]
                        env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                                   REPO="kubebao/openbao-operator", RUN_ID="101",
                                   ENVIRONMENT_NAME=environment, REVIEWS=json.dumps(reviews))
                        result = subprocess.run(["bash", "hack/ci/check-release-approval.sh"],
                                                cwd=ROOT, env=env, capture_output=True)
                        self.assertEqual(result.returncode == 0, expected, result.stderr)


class RegistryPublication(unittest.TestCase):
    def test_lookup_failure_never_becomes_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "hack").symlink_to(ROOT / "hack", target_is_directory=True)
            (root / "dist").mkdir()
            package = root / "dist/openbao-operator-0.7.0.tgz"
            package.write_bytes(b"qualified chart")
            binary = root / "bin"
            binary.mkdir()
            curl = binary / "curl"
            curl.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
if 'https://ghcr.io/token' in sys.argv:
    if os.environ['CASE'] == 'token_error':
        raise SystemExit(22)
    print(json.dumps({'token': 'local-test-token'}))
else:
    if os.environ['CASE'] == 'network_error':
        raise SystemExit(7)
    pathlib.Path(sys.argv[sys.argv.index('--output') + 1]).write_text(os.environ['BODY'])
    print(os.environ['STATUS'], end='')
''')
            curl.chmod(0o755)
            for name in ["docker", "helm"]:
                command = binary / name
                command.write_text('#!/usr/bin/env bash\nprintf "%s\\n" "$*" >> "${CALLS}"\n')
                command.chmod(0o755)
            manifest = '{"schemaVersion":2,"manifests":[]}'
            digest = "sha256:" + hashlib.sha256(manifest.encode()).hexdigest()
            chart = json.dumps({"schemaVersion": 2, "layers": [{
                "mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
                "digest": "sha256:" + hashlib.sha256(package.read_bytes()).hexdigest()}]})
            missing = '{"errors":[{"code":"MANIFEST_UNKNOWN"}]}'
            cases = [("missing", "404", missing, True, True),
                     ("server_error", "503", missing, False, False),
                     ("denied", "401", missing, False, False),
                     ("ambiguous_404", "404", '{"errors":[{"code":"UNAUTHORIZED"}]}', False, False),
                     ("bad_json", "200", 'not json', False, False),
                     ("wrong_schema", "200", '{}', False, False),
                     ("network_error", "200", manifest, False, False),
                     ("token_error", "200", manifest, False, False),
                     ("matching", "200", None, True, False),
                     ("conflict", "200", '{"schemaVersion":2,"layers":[]}', False, False)]
            for name, status, body, success, writes in cases:
                for title, matching in [("Promote tags (no rebuild)", manifest), ("Push Helm chart (OCI)", chart)]:
                    with self.subTest(case=name, step=title):
                        calls = root / "calls"
                        calls.write_text("")
                        env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                                   GH_TOKEN="unused-test-token", GITHUB_ACTOR="dc-tec", VERSION="0.7.0",
                                   CHART_VERSION="0.7.0", OWNER="example", CALLS=str(calls),
                                   STATUS=status, BODY=matching if body is None else body, CASE=name)
                        for key in ["MANAGER", "CONFIG_INIT", "BACKUP_EXECUTOR", "UPGRADE_EXECUTOR"]:
                            env[key + "_IMAGE"] = "ghcr.io/example/" + key.lower()
                            env[key + "_DIGEST"] = digest
                        result = subprocess.run(["bash", "-c", step("release.yml", title)],
                                                cwd=root, env=env, text=True, capture_output=True)
                        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                        mutations = [x for x in calls.read_text().splitlines()
                                     if x.startswith("push ") or x.startswith("buildx imagetools create ")]
                        self.assertEqual(bool(mutations), writes, calls.read_text())

    def test_upgrade_fixture_scopes_candidate_trust_to_target(self):
        source = (ROOT / "hack/ci/operator-upgrade-e2e.sh").read_text()
        render = source.split("render_hardened_fixture() {", 1)[1].split("\n}\n", 1)[0]
        fixture = ROOT / "test/fixtures/operator-upgrade/hardened-cluster.yaml"
        baseline = next(x.split('"')[1] for x in fixture.read_text().splitlines() if 'image: "ghcr.io/kubebao/openbao-init@' in x)
        env = dict(os.environ, DEFAULT_HARDENED_INIT_IMAGE=baseline, HARDENED_INIT_IMAGE="ghcr.io/kubebao/openbao-init@sha256:" + "a" * 64,
                   FIXTURE_DIR=str(fixture.parent), E2E_CANDIDATE_PR="7")
        result = subprocess.run(["bash", "-c", render], env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("failurePolicy: Block", result.stdout)
        self.assertIn("subject: https://github.com/kubebao/openbao-operator/.github/workflows/reusable-build.yml@refs/pull/7/merge", result.stdout)
        self.assertNotIn("operatorImageVerification", fixture.read_text())


if __name__ == "__main__":
    unittest.main()
