"""Exercise CI credential selection at the Bazel process boundary."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


WRAPPER = Path(__file__).with_name("bazel.sh").resolve()


class BazelCredentialTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name)
        binary = self.root / "bin"
        binary.mkdir()
        bazel = binary / "bazelisk-real"
        bazel.write_text(f"#!{sys.executable}\nimport json, sys\nprint(json.dumps(sys.argv[1:]))\n")
        bazel.chmod(0o755)
        git = binary / "git"
        git.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_CHECKOUT_SHA"\n')
        git.chmod(0o755)
        self.environment = os.environ | {
            "CI_CACHE_DIR": str(self.root), "GITHUB_WORKSPACE": str(self.root),
            "PATH": str(binary) + os.pathsep + os.environ["PATH"],
            "BB_RO_KEY": "public-cas-key", "BB_RW_KEY": "private-writer-key",
            "GITHUB_EVENT_NAME": "pull_request", "GITHUB_REF": "refs/pull/1/merge",
            "GITHUB_REPOSITORY": "perplexityai/gazelle_rs", "GITHUB_SHA": "a" * 40,
            "TEST_CHECKOUT_SHA": "a" * 40, "CI_RUN_ID": "123-1", "USE_BAZEL_VERSION": "9.0.0",
        }

    def run_bazel(self, changes=None, command=("--batch", "build", "//...")):
        return subprocess.run(
            ["bash", str(WRAPPER), *command], env=self.environment | (changes or {}),
            text=True, capture_output=True,
        )

    def check_key(self, result, key, upload):
        self.assertEqual(result.returncode, 0, result.stderr)
        arguments = json.loads(result.stdout)
        self.assertIn("--remote_header=x-buildbuddy-api-key=" + key, arguments)
        self.assertIn("--bes_header=x-buildbuddy-api-key=" + key, arguments)
        self.assertIn(upload, arguments)
        self.assertIn("--batch", arguments)
        self.assertEqual(arguments[-1], "//...")
        return arguments

    def test_untrusted_contexts_cannot_use_writer_even_if_present(self):
        for changes in [
            {}, {"GITHUB_EVENT_NAME": "merge_group"},
            {"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/feature"},
            {"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main"},
            {"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "TEST_CHECKOUT_SHA": "b" * 40},
            {"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "GITHUB_REPOSITORY": "fork/gazelle_rs"},
        ]:
            with self.subTest(changes=changes):
                result = self.run_bazel(changes)
                self.check_key(result, "public-cas-key", "--noremote_upload_local_results")
                self.assertNotIn("private-writer-key", result.stdout)
                self.assertNotIn("--remote_upload_local_results", json.loads(result.stdout))

    def test_post_merge_main_can_populate_cache(self):
        self.check_key(self.run_bazel({"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main"}),
                       "private-writer-key", "--remote_upload_local_results")

    def test_main_without_writer_fails(self):
        result = self.run_bazel({"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "BB_RW_KEY": ""})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Main CI requires the BuildBuddy writer key", result.stderr)

    def test_pr_without_public_key_never_falls_back_to_writer(self):
        result = self.run_bazel({"BB_RO_KEY": ""})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("remote_header" in value for value in json.loads(result.stdout)))
        self.assertNotIn("private-writer-key", result.stdout)

    def test_local_query_does_not_receive_credentials(self):
        result = self.run_bazel(command=("version",))
        self.assertEqual(json.loads(result.stdout), ["version"])


if __name__ == "__main__":
    unittest.main()
