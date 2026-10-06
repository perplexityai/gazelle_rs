#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
bazel_cmd="${BAZEL:-bazel}"
scratch="$(mktemp -d)"
unresolved="$(mktemp -d e2e_unresolved_XXXXXX)"
cp consumer/BUILD.bazel "$scratch/BUILD.expected"
cleanup() {
    cp "$scratch/BUILD.expected" consumer/BUILD.bazel
    rm -rf "$scratch" "$unresolved"
}
trap cleanup EXIT

"$bazel_cmd" query 'deps(@fixture_crates//:all, 1)' --output=xml > "$scratch/query.xml"
"$bazel_cmd" run @gazelle_rs//cmd/crate_catalog -- \
    -input "$scratch/query.xml" -prefix @fixture_crates//: > "$scratch/crates.json"
cmp crates.json "$scratch/crates.json"
cp consumer/BUILD.seed consumer/BUILD.bazel
"$bazel_cmd" run //:gazelle -- -strict consumer
cmp "$scratch/BUILD.expected" consumer/BUILD.bazel
"$bazel_cmd" test //consumer:all
"$bazel_cmd" run //:gazelle -- -strict -mode=diff consumer

for imported in missing_crate wire_codec; do
    printf 'pub use %s::Value;\n' "$imported" > "$unresolved/lib.rs"
    if "$bazel_cmd" run //:gazelle -- -strict "$unresolved" > "$scratch/strict.log" 2>&1; then
        echo "strict generation unexpectedly accepted $imported" >&2
        exit 1
    fi
    if [[ "$imported" == missing_crate ]]; then
        diagnostic='unresolved crate "missing_crate"'
    else
        diagnostic='ambiguous external crate "wire_codec"'
    fi
    if ! grep -q "$diagnostic" "$scratch/strict.log"; then
        cat "$scratch/strict.log" >&2
        exit 1
    fi
done
