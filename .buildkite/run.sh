#!/usr/bin/env bash
set -euo pipefail
source .buildkite/setup.sh

case "${1:-}" in
  test)
    bazelisk test //...
    ;;
  smoke)
    bazelisk test //rs:rs_test
    ;;
  examples)
    for example in basic mixed_deps cargo_metadata; do
      echo "--- examples/$example"
      (cd "examples/$example"; bazelisk test //...; bazelisk run //:gazelle -- update -mode=diff)
    done
    BAZEL=bazelisk examples/basic/test.sh
    BAZEL=bazelisk examples/bazel_catalog/test.sh
    cd examples/cross_compile
    for triple in aarch64-apple-darwin x86_64-apple-darwin aarch64-pc-windows-gnullvm x86_64-pc-windows-gnullvm; do
      bazelisk build --nobuild "--platforms=@rules_rs//rs/platforms:$triple" //:gazelle_bin
    done
    ;;
  release-smoke)
    python3 .github/scripts/check_release.py
    ;;
  *) echo 'Unknown CI suite' >&2; exit 1 ;;
esac
