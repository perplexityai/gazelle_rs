#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
bazel_cmd="${BAZEL:-bazel}"
scratch="$(mktemp -d)"
unresolved="$(mktemp -d e2e_unresolved_XXXXXX)"
visibility="$(mktemp -d e2e_visibility_XXXXXX)"
cp consumer/BUILD.bazel "$scratch/BUILD.expected"
cp BUILD.bazel "$scratch/root.BUILD"
cleanup() {
    cp "$scratch/BUILD.expected" consumer/BUILD.bazel
    cp "$scratch/root.BUILD" BUILD.bazel
    rm -rf "$scratch" "$unresolved" "$visibility"
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

# The same consumer (without a manifest) also resolves through central Cargo files.
sed 's/# gazelle:rust_crate_catalog crates.json/# gazelle:rust_cargo_lock @fixture_crates Cargo.toml Cargo.lock crates.json/' "$scratch/root.BUILD" > BUILD.bazel
cp consumer/BUILD.seed consumer/BUILD.bazel
"$bazel_cmd" run //:gazelle -- -strict consumer
cmp "$scratch/BUILD.expected" consumer/BUILD.bazel
"$bazel_cmd" test //consumer:all
"$bazel_cmd" run //:gazelle -- -strict -mode=diff consumer
cp "$scratch/root.BUILD" BUILD.bazel

# Package defaults, rather than generated public attributes, control cross-package access.
mkdir -p "$visibility/public_api" "$visibility/private_api" "$visibility/client"
printf 'package(default_visibility = ["//visibility:public"])\n' > "$visibility/public_api/BUILD.bazel"
for api in public_api private_api; do
    printf 'pub fn value() -> u32 { 42 }\n' > "$visibility/$api/lib.rs"
done
printf '#[test] fn checks_value() { assert_eq!(public_api::value(), 42); }\n' > "$visibility/client/client.test.rs"
"$bazel_cmd" run //:gazelle -- -strict "$visibility"
"$bazel_cmd" test "//$visibility/client:client_test"
"$bazel_cmd" run //:gazelle -- -strict -mode=diff "$visibility"
printf '#[test] fn checks_value() { assert_eq!(private_api::value(), 42); }\n' > "$visibility/client/client.test.rs"
"$bazel_cmd" run //:gazelle -- -strict "$visibility"
if "$bazel_cmd" test "//$visibility/client:client_test" > "$scratch/visibility.log" 2>&1; then
    echo "private package unexpectedly accessible to another package" >&2
    exit 1
fi
if ! grep -q 'not visible' "$scratch/visibility.log"; then
    cat "$scratch/visibility.log" >&2
    exit 1
fi

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
