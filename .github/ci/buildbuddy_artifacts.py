"""Fetch cross-built smoke archives from BuildBuddy; never rebuild on native hosts."""
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
import urllib.parse
import urllib.request
import uuid

import artifacts

API_URL = "https://app.buildbuddy.io/api/v1/"
PLATFORMS = ("linux-amd64", "darwin-arm64", "windows-amd64")
MAX_BYTES = 512 * 1024 * 1024


def archive_digest(uri):
    parsed = urllib.parse.urlsplit(uri)
    match = re.fullmatch(r"/(?:[^/]+/)*blobs/([a-f0-9]{64})/([0-9]+)", parsed.path)
    if parsed.scheme != "bytestream" or parsed.netloc != "remote.buildbuddy.io" or not match:
        raise ValueError("Unexpected BuildBuddy artifact URI")
    size = int(match[2])
    if size > MAX_BYTES:
        raise ValueError("Smoke archive exceeds size limit")
    return match[1], size


def record(directory, platform, invocation, events):
    uris = set()
    for line in Path(events).read_text().splitlines():
        event = json.loads(line)
        for file in event.get("namedSetOfFiles", {}).get("files", []):
            if PurePosixPath(file.get("name", "")).name == "smoke.tar.gz":
                uris.add(file.get("uri", ""))
    if len(uris) != 1:
        raise ValueError("Expected one remote smoke archive in build events")
    uri = uris.pop()
    archive_digest(uri)
    plan = artifacts.identity(platform) | {"invocation": str(uuid.UUID(invocation)), "uri": uri}
    Path(directory, platform + ".json").write_text(json.dumps(plan) + "\n")


def verify_invocation(response, plan):
    invocations = response.get("invocation", [])
    if len(invocations) != 1:
        raise ValueError("Expected one BuildBuddy invocation")
    invocation = invocations[0]
    metadata = {item["key"]: item.get("value", "") for item in invocation.get("buildMetadata", [])}
    if (
        invocation.get("id", {}).get("invocationId") != plan["invocation"]
        or invocation.get("commitSha") != plan["commit"]
        or invocation.get("repoUrl", "").removesuffix(".git") != "https://github.com/perplexityai/gazelle_rs"
        or invocation.get("pattern") != "//.github/ci:smoke_archive"
        or not invocation.get("success")
        or invocation.get("invocationStatus") != "COMPLETE_INVOCATION_STATUS"
        or metadata.get("CI_RUN_ID") != plan["build"]
    ):
        raise ValueError("Cross-build invocation does not match successful CI build")


def extract_archive(content, uri, directory, platform):
    digest, size = archive_digest(uri)
    if len(content) != size or hashlib.sha256(content).hexdigest() != digest:
        raise ValueError("Smoke archive checksum mismatch")
    suffix = ".exe" if platform == "windows-amd64" else ""
    required = {name + suffix for name in artifacts.FILES}
    with tarfile.open(fileobj=io.BytesIO(content), mode="r:gz") as archive:
        files = {}
        total_size = 0
        for member in archive:
            if member.name in (".", "./") and member.isdir():
                continue
            path = PurePosixPath(member.name)
            if not member.isfile() or len(path.parts) != 1 or str(path) not in required or str(path) in files:
                raise ValueError("Unexpected smoke archive entry")
            total_size += member.size
            if total_size > MAX_BYTES:
                raise ValueError("Smoke binaries exceed size limit")
            files[str(path)] = member
        if set(files) != required:
            raise ValueError("Missing smoke binaries")
        directory.mkdir(parents=True, exist_ok=False)
        for name, member in files.items():
            with archive.extractfile(member) as source:
                (directory / name).write_bytes(source.read())
    artifacts.process("pack", directory, platform)
    artifacts.process("verify", directory, platform)


def api_request(method, body):
    key = os.environ["BB_RW_KEY"]
    if not key:
        raise ValueError("BuildBuddy credentials are required")
    request = urllib.request.Request(
        API_URL + method, data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "x-buildbuddy-api-key": key},
    )
    return urllib.request.urlopen(request, timeout=120)


def download(directory):
    root = Path(".ci-artifacts/smoke", os.environ["USE_BAZEL_VERSION"])
    for platform in PLATFORMS:
        plan = json.loads(Path(directory, platform + ".json").read_text())
        if any(plan.get(key) != value for key, value in artifacts.identity(platform).items()):
            raise ValueError("Smoke download plan does not match this CI run")
        identifier = str(uuid.UUID(plan["invocation"]))
        archive_digest(plan["uri"])
        with api_request("GetInvocation", {"selector": {"invocation_id": identifier}, "include_metadata": True}) as response:
            verify_invocation(json.load(response), plan)
        with api_request("GetFile", {"uri": plan["uri"]}) as response:
            content = response.read(MAX_BYTES + 1)
        extract_archive(content, plan["uri"], root / platform, platform)
        print(f"Downloaded {platform} smoke binaries from BuildBuddy invocation {identifier}")


if __name__ == "__main__":
    if sys.argv[1] == "record":
        record(*sys.argv[2:])
    elif sys.argv[1] == "download":
        download(*sys.argv[2:])
    else:
        raise ValueError("Unknown BuildBuddy artifact operation")
