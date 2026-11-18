#!/usr/bin/env bash
set -euo pipefail
source .github/ci/setup.sh
: "${BB_RW_KEY:?BuildBuddy credentials are required for cross-builds}"
python3 -B .github/ci/artifacts_test.py
python3 -B .github/ci/buildbuddy_artifacts_test.py
directory="$PWD/.ci-artifacts/invocations/$USE_BAZEL_VERSION"
mkdir -p "$directory"
for platform in linux-amd64 darwin-arm64 windows-amd64; do
  case "$platform" in
    linux-amd64) target_platform=@rules_rs//rs/platforms:x86_64-unknown-linux-gnu ;;
    darwin-arm64) target_platform=@rules_rs//rs/platforms:aarch64-apple-darwin ;;
    windows-amd64) target_platform=@rules_rs//rs/platforms:x86_64-pc-windows-gnullvm ;;
  esac
  echo "--- Build $platform binaries"
  invocation=$(python3 -c 'import uuid; print(uuid.uuid4())')
  events="$RUNNER_TEMP/smoke-$platform.bep.json"
  bazelisk build --use_target_platform_for_tests "--platforms=$target_platform" \
    "--invocation_id=$invocation" "--build_event_json_file=$events" \
    --remote_download_outputs=minimal //.github/ci:smoke_archive
  python3 .github/ci/buildbuddy_artifacts.py record "$directory" "$platform" "$invocation" "$events"
done
