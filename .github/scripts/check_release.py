"""Exercise release packaging in a disposable repository without publishing."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
VERSION = "0.1.0"
TAG = f"v{VERSION}"
PREFIX = f"gazelle_rs-{VERSION}"
ARCHIVE = f"gazelle_rs-{TAG}.tar.gz"


def run(*args, cwd, **kwargs):
    try:
        return subprocess.run(args, cwd=cwd, check=True, text=True, capture_output=True, **kwargs)
    except subprocess.CalledProcessError as error:
        raise RuntimeError(f"{args}: {error.stdout}\n{error.stderr}") from error


config = json.loads((ROOT / "release-please-config.json").read_text())
assert {"type": "generic", "path": "MODULE.bazel"} in config["packages"]["."]["extra-files"]
module = (ROOT / "MODULE.bazel").read_text()
pattern = r'(version = ")[^"]+(",\s*# x-release-please-version)'
module, count = re.subn(pattern, rf'\g<1>{VERSION}\2', module)
assert count == 1, "MODULE.bazel must have exactly one release version marker"

with tempfile.TemporaryDirectory(prefix="gazelle-rs-release-") as temporary:
    repo = Path(temporary)
    # Include checked-out sources without copying .git, build output, or node_modules.
    tracked = run("git", "ls-files", "-z", cwd=ROOT).stdout.split("\0")
    for relative in filter(None, tracked):
        source = ROOT / relative
        if source.is_file():
            destination = repo / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)
    (repo / "MODULE.bazel").write_text(module)
    (repo / "version.txt").write_text(VERSION + "\n")
    run("git", "init", "--initial-branch=main", cwd=repo)
    run("git", "add", ".", cwd=repo)
    run("git", "-c", "user.name=Release smoke", "-c", "user.email=release-smoke@example.invalid",
        "-c", "core.hooksPath=/dev/null", "commit", "-m", "test: release fixture", cwd=repo)
    run("git", "tag", TAG, cwd=repo)
    env = dict(os.environ, GITHUB_REPOSITORY="perplexityai/gazelle_rs")
    notes = run("bash", ".github/workflows/release_prep.sh", TAG, cwd=repo, env=env).stdout
    archive = repo / ARCHIVE
    digest = base64.b64encode(hashlib.sha256(archive.read_bytes()).digest()).decode()
    assert f'sha256-{digest}' in notes, "release notes must contain the archive's actual SRI"
    assert f'https://github.com/perplexityai/gazelle_rs/releases/download/{TAG}/{ARCHIVE}' in notes
    assert f'version = "{VERSION}"' in notes
    with tarfile.open(archive) as packaged:
        names = set(packaged.getnames())
        for required in ["MODULE.bazel", "Cargo.lock", "go.mod", "LICENSE", "rs/BUILD.bazel",
                         "proto/message.proto", "crates/import_extractor/src/lib.rs"]:
            assert f"{PREFIX}/{required}" in names, f"missing release source: {required}"
        assert all(name == PREFIX or name.startswith(PREFIX + "/") for name in names)
        assert not any(".git" in Path(name).parts or "node_modules" in Path(name).parts for name in names)
        assert f'version = "{VERSION}"' in packaged.extractfile(f"{PREFIX}/MODULE.bazel").read().decode()
    template = json.loads((ROOT / ".bcr/source.template.json").read_text())
    values = dict(OWNER="perplexityai", REPO="gazelle_rs", VERSION=VERSION, TAG=TAG)
    assert template["strip_prefix"].format(**values) == PREFIX
    assert template["url"].format(**values) in notes
    env.pop("GITHUB_REF_NAME", None)
    missing_tag = subprocess.run(["bash", ".github/workflows/release_prep.sh"], cwd=repo,
                                 env=env, capture_output=True, text=True)
    assert missing_tag.returncode != 0 and "tag is required" in missing_tag.stderr
print("Release archive, version marker, SRI, BCR template, and missing-tag checks passed.")
