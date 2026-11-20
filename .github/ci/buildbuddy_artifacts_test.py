import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import artifacts
import buildbuddy_artifacts as buildbuddy


class BuildBuddyArtifactTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name)
        self.environment = patch.dict(os.environ, {
            "GITHUB_SHA": "a" * 40, "CI_RUN_ID": "123-1",
            "GITHUB_REPOSITORY": "perplexityai/gazelle_rs", "USE_BAZEL_VERSION": "9.0.0",
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.identifier = "12345678-1234-1234-1234-123456789abc"

    def archive(self, names, link=False):
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:gz") as archive:
            for name in names:
                member = tarfile.TarInfo(name)
                if link:
                    member.type = tarfile.SYMTYPE
                    member.linkname = "/tmp/outside"
                    archive.addfile(member)
                else:
                    member.size = 6
                    archive.addfile(member, io.BytesIO(b"binary"))
        content = output.getvalue()
        return content, f"bytestream://remote.buildbuddy.io/blobs/{hashlib.sha256(content).hexdigest()}/{len(content)}"

    def test_download_round_trip(self):
        for platform in buildbuddy.PLATFORMS:
            with self.subTest(platform=platform):
                suffix = ".exe" if platform == "windows-amd64" else ""
                content, uri = self.archive(["./" + name + suffix for name in artifacts.FILES])
                events = self.root / "events.json"
                events.write_text(json.dumps({"namedSetOfFiles": {"files": [
                    {"name": ".github/ci/smoke.tar.gz", "uri": uri}
                ]}}) + "\n")
                buildbuddy.record(self.root, platform, self.identifier, events)
                plan = json.loads((self.root / (platform + ".json")).read_text())
                self.assertEqual(plan, artifacts.identity(platform) | {"invocation": self.identifier, "uri": uri})
                destination = self.root / platform
                buildbuddy.extract_archive(content, plan["uri"], destination, platform)
                artifacts.process("verify", destination, platform)
                self.assertEqual((destination / ("gazelle" + suffix)).read_bytes(), b"binary")

    def test_reject_corrupt_download(self):
        content, uri = self.archive(artifacts.FILES)
        with self.assertRaisesRegex(ValueError, "checksum"):
            buildbuddy.extract_archive(content + b"corrupt", uri, self.root / "out", "linux-amd64")
        self.assertFalse((self.root / "out").exists())

    def test_reject_different_run_before_network(self):
        plan = artifacts.identity("linux-amd64") | {"build": "other-run"}
        (self.root / "linux-amd64.json").write_text(json.dumps(plan))
        with patch.object(buildbuddy, "api_request") as request:
            with self.assertRaisesRegex(ValueError, "does not match this CI run"):
                buildbuddy.download(self.root)
            request.assert_not_called()

    def test_download_api_uses_public_key_even_when_writer_is_present(self):
        def respond(request, timeout):
            self.assertEqual(request.full_url, buildbuddy.API_URL + "GetFile")
            self.assertEqual(request.get_header("X-buildbuddy-api-key"), "public-cas-key")
            self.assertEqual(json.loads(request.data), {"uri": "artifact"})
            self.assertEqual(timeout, 120)
            return "download"

        with patch.dict(os.environ, {"BB_RO_KEY": "public-cas-key", "BB_RW_KEY": "private-writer-key"}):
            with patch.object(buildbuddy.urllib.request, "urlopen", side_effect=respond):
                self.assertEqual(buildbuddy.api_request("GetFile", {"uri": "artifact"}), "download")

    def test_download_api_never_falls_back_to_writer(self):
        with patch.dict(os.environ, {"BB_RO_KEY": "", "BB_RW_KEY": "private-writer-key"}):
            with patch.object(buildbuddy.urllib.request, "urlopen") as request:
                with self.assertRaisesRegex(ValueError, "BB_RO_KEY"):
                    buildbuddy.api_request("GetFile", {"uri": "artifact"})
                request.assert_not_called()

    def test_reject_unsafe_or_incomplete_archive_before_writing(self):
        for names, link in [(["../gazelle"], False), (["/gazelle"], False),
                            (["gazelle", "gazelle"], False), (["gazelle"], True),
                            (["gazelle"], False), (["gazelle", "unit-test", "extra"], False)]:
            with self.subTest(names=names, link=link):
                content, uri = self.archive(names, link)
                with self.assertRaises(ValueError):
                    buildbuddy.extract_archive(content, uri, self.root / "out", "linux-amd64")
                self.assertFalse((self.root / "out").exists())

    def test_reject_untrusted_uri(self):
        for uri in ["https://example.com/file", "file:///tmp/file",
                    "bytestream://other.example/blobs/" + "a" * 64 + "/1",
                    "bytestream://remote.buildbuddy.io/blobs/" + "a" * 64 + f"/{buildbuddy.MAX_BYTES + 1}"]:
            with self.subTest(uri=uri), self.assertRaises(ValueError):
                buildbuddy.archive_digest(uri)

    def test_invocation_must_match_successful_producer(self):
        plan = artifacts.identity("linux-amd64") | {"invocation": self.identifier}
        invocation = {
            "id": {"invocationId": self.identifier}, "commitSha": plan["commit"],
            "repoUrl": "https://github.com/perplexityai/gazelle_rs.git",
            "pattern": "//.github/ci:smoke_archive", "success": True,
            "invocationStatus": "COMPLETE_INVOCATION_STATUS",
            "buildMetadata": [{"key": "CI_RUN_ID", "value": plan["build"]}],
        }
        buildbuddy.verify_invocation({"invocation": [invocation]}, plan)
        for key, value in {
            "id": {"invocationId": "other"}, "commitSha": "b" * 40,
            "repoUrl": "https://github.com/other/repo", "pattern": "//other",
            "success": False, "invocationStatus": "PARTIAL_INVOCATION_STATUS",
            "buildMetadata": [{"key": "CI_RUN_ID", "value": "other-run"}],
        }.items():
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "invocation"):
                buildbuddy.verify_invocation({"invocation": [invocation | {key: value}]}, plan)


if __name__ == "__main__":
    unittest.main()
