#!/usr/bin/env python3
"""Exercise post-release publisher routing without contacting GitHub or GHCR."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = "kubebao/openbao-operator"
DIGEST = "sha256:" + "1" * 64
HEAD = "a" * 40
IMAGES = ["openbao-operator", "openbao-init", "openbao-backup", "openbao-upgrade"]

MOCK = r'''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys

command = Path(sys.argv[0]).name
args = sys.argv[1:]
fixture = Path(os.environ["TEST_FIXTURE"])
repository = os.environ["TEST_REPOSITORY"]
publisher = os.environ["TEST_PUBLISHER"]
owner = publisher.split("/")[0]
version = os.environ["VERSION"]
digest = "sha256:" + "1" * 64
head = "a" * 40

with open(os.environ["TEST_COMMAND_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps([command, *args]) + "\n")

def option(name):
    return args[args.index(name) + 1]

if command == "gh":
    if args[:2] == ["attestation", "verify"]:
        assert option("--repo") == publisher, args
        assert option("--source-ref") == "refs/tags/" + version, args
        assert option("--cert-oidc-issuer") == "https://token.actions.githubusercontent.com", args
        assert "--deny-self-hosted-runners" in args, args
        subject = args[2]
        workflow = "release.yml"
        if subject.startswith("oci://"):
            assert "--bundle-from-oci" in args, "repository attestation API unavailable"
            assert "--bundle" not in args, args
            assert subject.startswith("oci://ghcr.io/" + owner + "/"), args
            assert subject.endswith("@" + digest), args
            attestation = json.loads((fixture / "oci-attestation.json").read_text())
            assert attestation["publisher"] == publisher, "attestation publisher mismatch"
            if "/charts/" not in subject:
                workflow = "reusable-build.yml"
        else:
            assert Path(subject).name == "checksums.txt", args
            assert "--bundle-from-oci" not in args, args
            if "--bundle" in args:
                attestation = json.loads(Path(option("--bundle")).read_text())
                assert attestation["publisher"] == publisher, "attestation publisher mismatch"
                assert attestation["digest"] == hashlib.sha256(Path(subject).read_bytes()).hexdigest(), "attestation digest mismatch"
            else:
                assert publisher == repository, "repository attestation API unavailable"
        assert option("--signer-workflow") == publisher + "/.github/workflows/" + workflow, args
    elif args[:2] == ["release", "view"]:
        assert option("--repo") == repository, args
        print(json.dumps({
            "assets": [{"name": path.name} for path in (fixture / "assets").iterdir()],
            "isDraft": False,
            "isPrerelease": "-" in version,
            "tagName": version,
            "url": "https://github.com/" + repository + "/releases/tag/" + version,
        }))
    elif args[:2] == ["release", "download"]:
        assert option("--repo") == repository, args
        for path in (fixture / "assets").iterdir():
            shutil.copy(path, option("--dir"))
    elif args[:2] == ["pr", "list"]:
        assert option("--repo") == repository, args
        if option("--state") == "merged":
            print(json.dumps([{
                "number": 42,
                "title": "chore(main): release " + version,
                "url": "https://github.com/" + repository + "/pull/42",
                "mergeCommit": {"oid": head},
                "labels": [],
            }]))
    else:
        raise AssertionError(args)
elif command == "git":
    assert args[0] == "ls-remote", args
    assert args[2] == "https://github.com/" + repository + ".git", args
    if args[1] == "--tags":
        print(head + "\trefs/tags/" + version)
    else:
        assert args[1] == "--heads", args
elif command == "cosign":
    expected = "https://github.com/" + publisher + "/.github/workflows/release.yml@refs/tags/" + version
    if option("--certificate-identity") != expected:
        sys.exit("certificate identity mismatch")
    assert option("--certificate-oidc-issuer") == "https://token.actions.githubusercontent.com", args
    if args[0] == "verify":
        assert args[-1].startswith("ghcr.io/" + owner + "/"), args
    else:
        assert args[0] == "verify-blob", args
elif command == "docker":
    assert args[:3] == ["buildx", "imagetools", "inspect"], args
    assert args[3].startswith("ghcr.io/" + owner + "/"), args
    assert args[3].endswith(":" + version), args
    print(json.dumps(digest))
elif command == "helm":
    if args[0] == "pull":
        assert args[1] == "oci://ghcr.io/" + owner + "/charts/openbao-operator", args
        assert option("--version") == version, args
        chart = Path(option("--untardir")) / "openbao-operator"
        chart.mkdir()
        shutil.copy(fixture / "Chart.yaml", chart)
    else:
        assert args[:2] == ["show", "chart"], args
        print((Path(args[2]) / "Chart.yaml").read_text(), end="")
else:
    raise AssertionError(command)
'''


class PostReleasePublisherTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.assets = self.directory / "assets"
        self.assets.mkdir()
        self.bin = self.directory / "bin"
        self.bin.mkdir()
        mock = self.bin / "mock"
        mock.write_text(MOCK, encoding="utf-8")
        mock.chmod(0o755)
        for name in ["gh", "git", "cosign", "docker", "helm"]:
            (self.bin / name).symlink_to(mock)

    def fixture(self, version, publisher):
        names = [
            "install.yaml", "crds.yaml", "checksums.txt.bundle",
            "checksums.txt.sigstore.json", "checksums.intoto.jsonl",
            *["sbom-" + image + ".spdx.json" for image in IMAGES],
        ]
        for name in names:
            (self.assets / name).write_text(name + "\n", encoding="utf-8")
        checksums = "".join(
            hashlib.sha256((self.assets / name).read_bytes()).hexdigest() + "  " + name + "\n"
            for name in names
            if not name.startswith("checksums.")
        )
        (self.assets / "checksums.txt").write_text(checksums, encoding="utf-8")
        # These claims model verifier routing and rejection, not cryptography.
        (self.assets / "checksums.intoto.jsonl").write_text(json.dumps({
            "publisher": publisher, "digest": hashlib.sha256(checksums.encode()).hexdigest(),
        }), encoding="utf-8")
        (self.directory / "oci-attestation.json").write_text(
            json.dumps({"publisher": publisher}), encoding="utf-8"
        )
        owner = publisher.split("/")[0]
        self.provenance = {
            "release": {
                "repository": publisher, "owner": owner, "tag": version,
                "source_ref": "refs/tags/" + version,
            },
            "identity_constraints": {
                "reusable_build_signer_workflow": publisher + "/.github/workflows/reusable-build.yml",
            },
            "release_artifacts": {
                "checksums_txt": {"digest": "sha256:" + hashlib.sha256(checksums.encode()).hexdigest()},
            },
            "images": [
                {"name": image, "ref": "ghcr.io/" + owner + "/" + image, "digest": DIGEST}
                for image in IMAGES
            ],
            "chart": {"digest": DIGEST},
        }
        (self.directory / "Chart.yaml").write_text(
            "apiVersion: v2\nname: openbao-operator\nversion: " + version + "\n", encoding="utf-8"
        )
        self.version = version
        self.publisher = publisher

    def environment(self):
        environment = os.environ.copy()
        for name in [
            "PUBLISHER_REPO", "GIT_REMOTE", "ALLOW_DRAFT", "SIGNER_WORKFLOW",
            "SOURCE_REF", "CHART_IMAGE", "CHECKSUMS_ATTESTATION_BUNDLE",
        ]:
            environment.pop(name, None)
        environment.update({
            "PATH": str(self.bin) + os.pathsep + environment["PATH"],
            "TEST_FIXTURE": str(self.directory),
            "TEST_REPOSITORY": REPOSITORY,
            "TEST_PUBLISHER": self.publisher,
            "TEST_COMMAND_LOG": str(self.directory / "commands.jsonl"),
            "VERSION": self.version,
            "REPO": REPOSITORY,
            "EXPECTED_CHART_FILE": str(self.directory / "Chart.yaml"),
            "EVIDENCE_OUT": str(self.directory / "evidence.json"),
            "MAX_ATTEMPTS": "1",
            "RETRY_SECONDS": "0",
        })
        return environment

    def verify(self, publisher_override=None):
        (self.assets / "provenance-index.json").write_text(json.dumps(self.provenance), encoding="utf-8")
        environment = self.environment()
        if publisher_override is not None:
            environment["PUBLISHER_REPO"] = publisher_override
        result = subprocess.run(
            ["bash", str(ROOT / "hack/ci/verify-post-release.sh")],
            env=environment, capture_output=True, text=True, check=False,
        )
        return result

    def verify_checksums(self, bundle=None):
        environment = self.environment()
        environment.update({
            "REPO": self.publisher,
            "OWNER": self.publisher.split("/")[0],
            "VERIFY_CHART": "false",
            "CHECKSUMS_PATH": str(self.assets / "checksums.txt"),
        })
        if bundle is not None:
            environment["CHECKSUMS_ATTESTATION_BUNDLE"] = str(bundle)
        return subprocess.run(
            ["bash", str(ROOT / "hack/ci/verify-release-artifact-attestations.sh")],
            env=environment, capture_output=True, text=True, check=False,
        )

    def attestation_commands(self):
        log = self.directory / "commands.jsonl"
        commands = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return [command for command in commands if command[:3] == ["gh", "attestation", "verify"]]

    def assert_success(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        evidence = json.loads((self.directory / "evidence.json").read_text())
        self.assertEqual(evidence["repository"], REPOSITORY)
        self.assertEqual(evidence["publisher_repository"], self.publisher)
        self.assertEqual(
            evidence["identity_constraints"]["certificate_identity"],
            "https://github.com/" + self.publisher + "/.github/workflows/release.yml@refs/tags/" + self.version,
        )
        self.assertEqual(
            evidence["chart"]["ref"],
            "ghcr.io/" + self.publisher.split("/")[0] + "/charts/openbao-operator:" + self.version,
        )
        commands = [json.loads(line) for line in (self.directory / "commands.jsonl").read_text().splitlines()]
        self.assertEqual(sum(command[:3] == ["gh", "attestation", "verify"] for command in commands), 6)
        self.assertEqual(sum(command[0] == "cosign" for command in commands), 6)
        attestations = self.attestation_commands()
        self.assertEqual(sum("--bundle-from-oci" in command for command in attestations), 5)
        checksum_command = attestations[-1]
        self.assertEqual(Path(checksum_command[checksum_command.index("--bundle") + 1]).name, "checksums.intoto.jsonl")

    def test_historical_release_uses_original_publisher_and_current_repository(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        self.assert_success(self.verify("dc-tec/openbao-operator"))

    def test_new_release_defaults_to_current_publisher(self):
        self.fixture("0.6.0", REPOSITORY)
        self.assert_success(self.verify())

    def test_registry_attestation_rejects_wrong_publisher_without_api_fallback(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        (self.directory / "oci-attestation.json").write_text(json.dumps({"publisher": REPOSITORY}))
        result = self.verify(self.publisher)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("attestation publisher mismatch", result.stderr)
        self.assertEqual(len(self.attestation_commands()), 1)
        self.assertFalse((self.directory / "evidence.json").exists())

    def test_checksum_bundle_rejects_wrong_publisher_without_api_fallback(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        bundle = self.assets / "checksums.intoto.jsonl"
        claims = json.loads(bundle.read_text())
        claims["publisher"] = REPOSITORY
        bundle.write_text(json.dumps(claims))
        result = self.verify(self.publisher)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("attestation publisher mismatch", result.stderr)
        self.assertEqual(len(self.attestation_commands()), 6)
        self.assertFalse((self.directory / "evidence.json").exists())

    def test_missing_or_empty_explicit_bundle_fails_before_verification(self):
        self.fixture("0.6.0", REPOSITORY)
        bundle = self.directory / "missing.jsonl"
        for empty in [False, True]:
            with self.subTest(empty=empty):
                if empty:
                    bundle.touch()
                result = self.verify_checksums(bundle)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("checksums attestation bundle missing or empty", result.stderr)
                self.assertEqual(self.attestation_commands(), [])

    def test_invalid_bundle_fails_without_api_fallback(self):
        self.fixture("0.6.0", REPOSITORY)
        bundle = self.assets / "checksums.intoto.jsonl"
        bundle.write_text("invalid JSON\n")
        result = self.verify_checksums(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(self.attestation_commands()), 1)
        self.assertIn("--bundle", self.attestation_commands()[0])

    def test_checksum_bundle_must_match_subject_digest(self):
        self.fixture("0.6.0", REPOSITORY)
        bundle = self.assets / "checksums.intoto.jsonl"
        claims = json.loads(bundle.read_text())
        claims["digest"] = "0" * 64
        bundle.write_text(json.dumps(claims))
        result = self.verify_checksums(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("attestation digest mismatch", result.stderr)
        self.assertEqual(len(self.attestation_commands()), 1)

    def test_in_flight_publication_can_verify_checksums_without_exported_bundle(self):
        self.fixture("0.6.0", REPOSITORY)
        result = self.verify_checksums()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        commands = self.attestation_commands()
        self.assertEqual(len(commands), 1)
        self.assertNotIn("--bundle", commands[0])

    def test_historical_release_rejects_current_publisher_without_fallback(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("certificate identity mismatch", result.stderr)
        commands = [json.loads(line) for line in (self.directory / "commands.jsonl").read_text().splitlines()]
        self.assertEqual(sum(command[0] == "cosign" for command in commands), 1)
        self.assertFalse((self.directory / "evidence.json").exists())

    def test_new_release_rejects_historical_publisher(self):
        self.fixture("0.6.0", REPOSITORY)
        result = self.verify("dc-tec/openbao-operator")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("certificate identity mismatch", result.stderr)

    def test_provenance_repository_must_match_selected_publisher(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        self.provenance["release"]["repository"] = REPOSITORY
        result = self.verify("dc-tec/openbao-operator")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("provenance repository", result.stderr)

    def test_provenance_owner_must_match_selected_publisher(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        self.provenance["release"]["owner"] = "kubebao"
        result = self.verify("dc-tec/openbao-operator")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("provenance owner", result.stderr)

    def test_image_namespace_must_match_selected_publisher(self):
        self.fixture("0.5.1", "dc-tec/openbao-operator")
        self.provenance["images"][0]["ref"] = "ghcr.io/kubebao/openbao-operator"
        result = self.verify("dc-tec/openbao-operator")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("provenance image openbao-operator ref", result.stderr)


if __name__ == "__main__":
    unittest.main()
