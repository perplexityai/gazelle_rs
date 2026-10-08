#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
bazel_cmd="${BAZEL:-bazel}"
scratch="$(mktemp -d)"
unresolved="$(mktemp -d e2e_unresolved_XXXXXX)"
visibility="$(mktemp -d e2e_visibility_XXXXXX)"
defaults="$(mktemp -d e2e_defaults_XXXXXX)"
cp consumer/BUILD.bazel "$scratch/BUILD.expected"
cp BUILD.bazel "$scratch/root.BUILD"
cp Cargo.toml "$scratch/Cargo.toml"
cleanup() {
    cp "$scratch/BUILD.expected" consumer/BUILD.bazel
    cp "$scratch/root.BUILD" BUILD.bazel
    cp "$scratch/Cargo.toml" Cargo.toml
    rm -rf "$scratch" "$unresolved" "$visibility" "$defaults"
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
sed 's/# gazelle:rust_crate_catalog crates.json/# gazelle:rust_cargo_lock @fixture_crates Cargo.toml Cargo.lock/' "$scratch/root.BUILD" > BUILD.bazel
cat >> BUILD.bazel <<'DIRECTIVES'
# gazelle:resolve rs wire_codec @fixture_crates//:codec-2.0.0
# gazelle:resolve_regexp rs ^wire_derive$ @fixture_crates//:derive-1.0.0
# gazelle:rust_proc_macro wire_derive
DIRECTIVES
cp consumer/BUILD.seed consumer/BUILD.bazel
"$bazel_cmd" run //:gazelle -- -strict consumer
cmp "$scratch/BUILD.expected" consumer/BUILD.bazel
"$bazel_cmd" test //consumer:all
"$bazel_cmd" run //:gazelle -- -strict -mode=diff consumer

# Root requirements select v1 despite v2 also being locked. No BUILD deps or
# aliases seed this resolution; compilation verifies the generated rename too.
printf '#[test] fn selected_version() { assert_eq!(plain::VERSION, 1); }\n' > "$defaults/default.test.rs"
printf '#[test] fn selected_version() { assert_eq!(renamed_plain::VERSION, 1); }\n' > "$defaults/renamed.test.rs"
"$bazel_cmd" run //:gazelle -- -strict "$defaults"
if ! grep -q '"@fixture_crates//:plain"' "$defaults/BUILD.bazel" || grep -q 'plain-[12]' "$defaults/BUILD.bazel"; then
    cat "$defaults/BUILD.bazel" >&2
    echo "central direct dependencies did not use the default alias" >&2
    exit 1
fi
"$bazel_cmd" test "//$defaults:all"
"$bazel_cmd" run //:gazelle -- -strict -mode=diff "$defaults"

# Two direct versions need distinct labels, even when a default alias exists.
printf '\nnewer = { package = "plain", version = "2" }\n' >> Cargo.toml
printf '#[test] fn selected_version() { assert_eq!(newer::VERSION, 2); }\n' > "$defaults/newer.test.rs"
rm "$defaults/BUILD.bazel"
"$bazel_cmd" run //:gazelle -- -strict "$defaults"
"$bazel_cmd" test "//$defaults:all"
"$bazel_cmd" run //:gazelle -- -strict -mode=diff "$defaults"
cp "$scratch/Cargo.toml" Cargo.toml
rm -rf "$defaults"
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
