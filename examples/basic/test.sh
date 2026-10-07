#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
bazel_cmd="${BAZEL:-bazel}"
scratch="$(mktemp -d)"
cp unit/BUILD.bazel "$scratch/BUILD.expected"
cleanup() {
    cp "$scratch/BUILD.expected" unit/BUILD.bazel
    rm -rf "$scratch"
}
trap cleanup EXIT

: > unit/BUILD.bazel
"$bazel_cmd" run //:gazelle -- -strict unit
cmp "$scratch/BUILD.expected" unit/BUILD.bazel
"$bazel_cmd" test //unit:all
"$bazel_cmd" run //:gazelle -- -strict -mode=diff unit
