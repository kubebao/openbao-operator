#!/usr/bin/env python3
"""Exercise release authorization and publication steps without remote writes."""

import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import runpy
import subprocess
import tempfile
import textwrap
import unittest
from unittest import mock

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
    def test_release_pr_requires_review_and_qualification(self):
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
    print(json.dumps({'head': {'sha': sha}, 'user': {'login': 'dc-tec' if case == 'self_authored' else 'release-bot'}}))
elif endpoint.endswith('/reviews'):
    if case not in ('missing_review', 'self_authored'):
        print(json.dumps({'user': {'login': 'dc-tec'}, 'state': 'APPROVED',
                          'commit_id': 'b' * 40 if case == 'stale_review' else sha, 'id': 1}))
elif '/runs?' in endpoint:
    print(json.dumps({'workflow_runs': [{'id': 101, 'path': '.github/workflows/release-candidate.yml',
        'head_sha': 'b' * 40 if case == 'changed_head' else sha, 'event': 'pull_request',
        'conclusion': 'success', 'head_repository': {'full_name': os.environ['REPO']}}]}))
elif '/jobs?' in endpoint:
    print(json.dumps({'jobs': [{'name': 'Qualified Release Candidate',
        'conclusion': 'skipped' if case == 'skipped' else 'success'}]}))
elif endpoint.endswith('/approvals'):
    print(json.dumps([] if case == 'no_approval' else [{'state': 'approved',
        'user': {'login': 'dc-tec', 'type': 'User'}, 'environments': [{'name': 'release-candidate'}]}]))
else:
    raise SystemExit('unexpected endpoint: ' + endpoint)
''')
            binary.chmod(0o755)
            (root / "release-pr-files.txt").write_text(".release-please-manifest.json\nCHANGELOG.md\n")
            for case, success in [("reviewed", True), ("missing_review", False), ("self_authored", False),
                                  ("stale_review", False), ("no_approval", False),
                                  ("changed_head", False), ("skipped", False)]:
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
                    elif case in ("missing_review", "stale_review", "self_authored"):
                        self.assertIn("Missing current-head approval", result.stderr)
                    elif case == "no_approval":
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


class ReleasePublication(unittest.TestCase):
    @staticmethod
    def release(tag, **overrides):
        return {"tag_name": tag, "draft": False, "prerelease": False,
                "published_at": "2026-11-07T00:00:00Z", **overrides}

    def publish(self, version, pages, api_error=False, draft=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "hack").symlink_to(ROOT / "hack", target_is_directory=True)
            (root / "dist").mkdir()
            (root / "dist/checksums.txt").write_text("qualified payload checksums\n")
            binary = root / "bin"
            binary.mkdir()
            gh = binary / "gh"
            gh.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ['CALLS'], 'a') as calls:
    calls.write(json.dumps(args) + '\\n')
if args[:2] == ['release', 'view']:
    print(json.dumps({'isDraft': os.environ['DRAFT'] == 'true'}))
elif args[:2] == ['release', 'download']:
    out = pathlib.Path(args[args.index('--dir') + 1])
    (out / 'checksums.txt').write_text('qualified payload checksums\\n')
elif args[0] == 'api':
    assert args[1] == 'repos/kubebao/openbao-operator/releases'
    assert '--paginate' in args and '--slurp' in args
    if os.environ['API_ERROR'] == 'true':
        print('GitHub release lookup failed (HTTP 403)', file=sys.stderr)
        raise SystemExit(1)
    print(os.environ['PAGES'])
elif args[:2] not in (['release', 'upload'], ['release', 'edit']):
    raise SystemExit('unexpected gh call: ' + repr(args))
''')
            gh.chmod(0o755)
            calls = root / "calls"
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                       REPO="kubebao/openbao-operator", CALLS=str(calls),
                       PAGES=json.dumps(pages), API_ERROR=str(api_error).lower(), DRAFT=str(draft).lower())
            script = step("release.yml", "Publish GitHub Release").replace(
                "${{ needs.prepare.outputs.version }}", version)
            result = subprocess.run(["bash", "-c", script], cwd=root, env=env,
                                    text=True, capture_output=True)
            return result, [json.loads(line) for line in calls.read_text().splitlines()]

    def test_stable_latest_uses_numeric_versions_and_all_pages(self):
        cases = [
            ("first", "0.1.0", [[]], True),
            ("new_patch", "0.6.1", [[self.release("0.6.0")]], True),
            ("older_patch", "0.6.1", [[self.release("0.6.2")]], False),
            ("older_line", "0.5.4", [[self.release("0.6.1")]], False),
            ("numeric_minor", "0.10.0", [[self.release("0.9.9")]], True),
            ("numeric_patch", "0.6.12", [[self.release("0.6.9")]], True),
            ("later_page", "0.6.2", [[self.release("0.6.1")], [self.release("0.7.0")]], False),
            ("prerelease", "0.6.1", [[self.release("1.0.0-rc.1", prerelease=True)]], True),
            ("draft", "0.6.1", [[self.release("1.0.0", draft=True, published_at=None)]], True),
            ("legacy_prefix", "0.6.1", [[self.release("v0.7.0")]], False),
            ("build_metadata", "0.6.1", [[self.release("0.6.1+build.2")]], True),
            ("non_version", "0.6.1", [[self.release("nightly")]], True),
        ]
        for case, version, pages, latest in cases:
            with self.subTest(case=case):
                result, calls = self.publish(version, pages)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                edits = [args for args in calls if args[:2] == ["release", "edit"]]
                self.assertEqual(len(edits), 1)
                self.assertIn("--latest=" + str(latest).lower(), edits[0])
                writes = [args[1] for args in calls if args[:2] in
                          (["release", "upload"], ["release", "edit"])]
                self.assertEqual(writes, ["upload", "edit"])

    def test_latest_lookup_failures_stop_before_upload_and_publication(self):
        cases = [
            ("api_error", [[]], True),
            ("wrong_pages", {"message": "not release pages"}, False),
            ("wrong_page", [{}], False),
            ("missing_flags", [[{"tag_name": "0.7.0"}]], False),
            ("missing_date", [[self.release("0.7.0", published_at=None)]], False),
            ("string_flag", [[self.release("0.7.0", draft="false")]], False),
            ("invalid_later_record", [[self.release("0.7.0"), {}]], False),
        ]
        for case, pages, error in cases:
            with self.subTest(case=case):
                result, calls = self.publish("0.6.1", pages, api_error=error)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(args[:2] in (["release", "upload"], ["release", "edit"])
                                     for args in calls), calls)
                if error:
                    self.assertIn("HTTP 403", result.stderr)

    def test_invalid_stable_version_does_not_publish(self):
        for version in ["v0.6.1", "0.06.1", "release"]:
            with self.subTest(version=version):
                result, calls = self.publish(version, [[]])
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(args[:2] in (["release", "upload"], ["release", "edit"])
                                     for args in calls), calls)

    def test_latest_lookup_timeout_fails_with_a_diagnostic(self):
        output, errors = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, REPO="kubebao/openbao-operator", VERSION="0.6.1"), \
                mock.patch("subprocess.run", side_effect=subprocess.TimeoutExpired("gh", 60)) as lookup, \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            with self.assertRaises(SystemExit) as failure:
                runpy.run_path(str(ROOT / "hack/ci/resolve-release-latest.py"), run_name="__main__")
        self.assertEqual(failure.exception.code, 1)
        self.assertEqual(output.getvalue(), "")
        self.assertIn("Cannot select Latest:", errors.getvalue())
        self.assertIn("timed out", errors.getvalue())
        self.assertNotIn("Traceback", errors.getvalue())
        self.assertGreater(lookup.call_args.kwargs["timeout"], 0)
        self.assertLessEqual(lookup.call_args.kwargs["timeout"], 60)

    def test_prerelease_does_not_query_or_claim_latest(self):
        result, calls = self.publish("0.7.0-rc.1", [[]], api_error=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(args[0] == "api" for args in calls))
        edit = next(args for args in calls if args[:2] == ["release", "edit"])
        self.assertIn("--prerelease", edit)
        self.assertFalse(any(arg.startswith("--latest") for arg in edit))

    def test_published_release_rerun_is_read_only(self):
        result, calls = self.publish("0.6.1", [[]], api_error=True, draft=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(args[0] == "api" or args[:2] in
                             (["release", "upload"], ["release", "edit"]) for args in calls))

    def test_publication_is_serialized_across_versions(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        promote = workflow.split("\n  promote:\n", 1)[1].split("\n  cleanup-release-state:", 1)[0]
        self.assertIn("    concurrency:\n      group: release-promotion-${{ github.repository }}\n"
                      "      cancel-in-progress: false\n      queue: max\n", promote)
        # This is the only queue key excluded from actionlint's older schema.
        self.assertEqual(workflow.count("queue:"), 1)

    def test_verification_evidence_precedes_comment_credentials_and_comment(self):
        source = (ROOT / ".github/workflows/post-release-verification.yml").read_text()
        names = ["Run post-release verification", "Upload verification evidence",
                 "Mint GitHub App token for release PR comment", "Comment on release PR"]
        positions = [source.index("      - name: " + name) for name in names]
        self.assertEqual(positions, sorted(positions))
        upload = source.split("      - name: Upload verification evidence", 1)[1].split("\n      - name:", 1)[0]
        self.assertIn("if-no-files-found: error", upload)
        self.assertNotIn("if:", upload)
        self.assertEqual(source.count("      - name: Upload verification evidence"), 1)


if __name__ == "__main__":
    unittest.main()
