# gazelle_rs

A Gazelle language extension for Rust, using the same architecture as
[gazelle_py](https://github.com/perplexityai/gazelle_py): a Go language plugin,
a Rust parser linked in-process through cgo, and protobuf across the FFI boundary.
It generates `rules_rs` `rust_library`, `rust_binary`, `rust_test`, and
`rust_proc_macro` targets.

**Internal crate imports do not need declarations in `Cargo.toml`.** A manifest
is optional. Gazelle indexes each library's `crate_name` and resolves source
imports against that index, including crates generated during the same run.

```text
api/src/lib.rs       → //api:api
app/src/main.rs      → //app:app
  use api::answer;   → deps = ["//api"]
```

Neither directory needs a Cargo manifest. If one exists, its package name,
edition, library path/name, `proc-macro`, and explicit binary/test targets inform
rule generation. Its dependency tables never gate internal resolution.

## Setup

The module is not published yet. In a consumer's `MODULE.bazel`, use a local
checkout until a release is available:

```starlark
bazel_dep(name = "gazelle_rs", version = "0.0.0")
local_path_override(module_name = "gazelle_rs", path = "../gazelle_rs")
bazel_dep(name = "gazelle", version = "0.51.3")
bazel_dep(name = "rules_rs", version = "0.0.111")
bazel_dep(name = "llvm", version = "0.8.18")
```

Compose a Gazelle binary in `BUILD.bazel`:

```starlark
load("@gazelle//:def.bzl", "gazelle", "gazelle_binary")

gazelle_binary(
    name = "gazelle_bin",
    languages = ["@gazelle_rs//rs"],
)

gazelle(name = "gazelle", gazelle = ":gazelle_bin")
```

The plugin inherits the reference project's hermetic Rust/LLVM toolchain setup.
Bazel reads the consumer's `.bazelrc`, so copy these settings there:

```text
common --enable_platform_specific_config
common:linux --host_platform=@gazelle_rs//platforms:local_gnu
common --repo_env=BAZEL_DO_NOT_DETECT_CPP_TOOLCHAIN=1
common --repo_env=BAZEL_NO_APPLE_CPP_TOOLCHAIN=1
common --@llvm//config:experimental_stub_libgcc_s=True
build:linux --linkopt=-no-pie
```

Then run `bazel run //:gazelle`. Use `bazel run //:gazelle -- -mode=diff` to
check that generated BUILD files are current. Keep Gazelle's default full index
(or use `-index=all` when updating a subset) for cross-package resolution.

[examples/basic](examples/basic) is a standalone consumer with a library,
binary, unit test, and integration test, with no Cargo manifests:

```sh
cd examples/basic
bazel run //:gazelle
bazel run //:gazelle -- -mode=diff
bazel test //...
bazel run //app
```

## More examples

| Example | Internal graph | External dependencies |
| --- | --- | --- |
| [mixed_deps](examples/mixed_deps/README.md) | `app → service → model` and `app → presentation → model`; no internal Cargo manifests | `anyhow`, `serde_json`, `itoa`, supplied by a separate dependency catalog |
| [cargo_metadata](examples/cargo_metadata/README.md) | `report_cli → report_engine → line_items`; Cargo names differ from directory names and internal dependencies are omitted | `anyhow`, `serde_json`, supplied by a Cargo workspace |

Each example is a standalone Bazel workspace with checked-in lockfiles,
external `gazelle:resolve` mappings, generated BUILD files, and tests. CI
regenerates and compiles all examples and checks generation idempotency.

## Architecture

```text
Gazelle GenerateRules → protobuf → cgo → syn parser + module traversal
                     ← crate sources / imports / test imports
Gazelle Imports       → index libraries by crate_name
Gazelle Resolve       → overrides / internal index → deps / proc_macro_deps
```

- `rs/`: Go language lifecycle, crate discovery, and resolution.
- `crates/import_extractor/`: Rust parser, graph traversal, and namespaced
  `gazelle_rs_ie_dispatch` / `gazelle_rs_ie_free` C ABI. Bazel links the rlib
  with its transitive Rust dependencies; a staticlib target is also available.
- `proto/`: shared wire schema; Go and Rust bindings are generated at build time.
- `platforms/`: toolchain host constraints.
- `rs/gazelle_test/`: source-to-BUILD fixtures driven by Gazelle's test harness.
- `.github/`, `.bcr/`, `bcr_test/`: CI and release/registry setup.

## Crate discovery and imports

The conventional roots are `src/lib.rs` or `lib.rs`, and `src/main.rs` or
`main.rs`. A package containing both gets `<name>` and `<name>_bin` targets.
`src/bin/*.rs`, `src/bin/*/main.rs`, and `tests/*.rs` are discovered too.
Integration test targets use the filename plus `_test`. Subdirectories owned
by a crate do not generate duplicate crates.

The parser follows declared out-of-line and inline `mod` trees, including
literal `#[path = "..."]` attributes. Only reachable source files become
`srcs`. It collects `use` trees, re-exports, `extern crate`, qualified expression
and type paths, qualified macro invocations, expression-list macro arguments, and qualified derive paths. It
ignores standard-library paths, local modules, and imported aliases in ordinary
qualified expressions. Direct `#[cfg(test)]` imports are separated from ordinary
dependencies; detected unit tests get a `rust_test(crate = ":owner")` target.

Explicit rules with literal sources and crate roots retain their names and
crate names. Unrelated attributes survive Gazelle's merge. Computed `srcs`
remain unmanaged; computed `deps`/`proc_macro_deps` are preserved. Gazelle's
`# keep` comments remain available for hand-maintained values. Failed parsing,
missing modules, and source graphs crossing Bazel package boundaries leave the
affected target unchanged and produce diagnostics.

Resolution order:

1. `# gazelle:resolve rs <crate> <label>` override.
2. A unique indexed internal library or proc-macro crate, regardless of Cargo
   dependency declarations.
3. An unresolved/ambiguous-import diagnostic. Existing dependency attributes
   are left unchanged when any import cannot be resolved; no external labels
   are guessed. These diagnostics do not make Gazelle exit nonzero.

Internal proc-macro rules are indexed separately so their imports land in
`proc_macro_deps`. External crates need explicit overrides, for example:

```starlark
# gazelle:resolve rs serde @crates//:serde
```

External procedural macro dependencies should be maintained explicitly with
`# keep` (their rule kind cannot be inferred from a label override).

## Configuration

| Directive | Default | Meaning |
| --- | --- | --- |
| `rust_extension` | `enabled` | `disabled` skips generation; inherited. |
| `rust_edition` | `2021` | Edition used when the local manifest does not supply one; inherited. |
| `rust_crate_name` | Package/directory name, with hyphens replaced by underscores | Override the default crate name in this package only. |
| `rust_visibility` | `//visibility:public` | Space-separated labels for new targets; inherited. |

Gazelle's `resolve` and `map_kind` directives work normally. For rules_rust or
custom wrappers, map each desired kind, for example:

```starlark
# gazelle:map_kind rust_library rust_library @rules_rust//rust:defs.bzl
# gazelle:map_kind rust_binary rust_binary @rules_rust//rust:defs.bzl
# gazelle:map_kind rust_test rust_test @rules_rust//rust:defs.bzl
```

## Scope

This is source-level dependency inference, not Cargo or rustc execution. It does
not expand macros, execute build scripts, discover `include!` inputs, evaluate
feature/platform `cfg` expressions, or translate Cargo dependency tables into
external repositories. Feature/platform branches are inspected conservatively;
all declared module files must exist. Only direct `cfg(test)` is separated as
test-only. Macro-generated imports and unusual lexical shadowing may need
explicit BUILD maintenance. Existing computed source rules are indexed by
crate name but not regenerated. Workspace-inherited Cargo metadata, auto-target
disabling flags, examples/benches, and nested independently built crates inside
another crate's source graph are not modeled.

Bazel 8.5+ is required; CI targets 8.6 and 9.0 on Linux. Other platforms are
configured following the reference, but native execution needs validation.

## Development and releases

```sh
bazel test //...
cargo test --workspace
UPDATE_SNAPSHOTS=true bazel run //rs/gazelle_test:gazelle_test
```

Review snapshot changes before accepting them. `cargo test` generates its Rust
protobuf bindings with a vendored protoc; the Bazel build uses the reference's
`rust_prost_library` and `go_proto_library` setup. Go tests run through Bazel so
the native library and generated Go bindings are supplied automatically.

Release Please starts at `0.0.0`. Publishing requires the same organization
setup as gazelle_py: `GH_RELEASE_TOKEN`, `BCR_PUBLISH_TOKEN`, a
`perplexityai/bazel-central-registry` fork, and the publish-to-bcr app installed
on that fork. No releases or registry submissions have been created by this
setup. Confirm the OSS maintainer entry in `.bcr/metadata.template.json` before
publishing.
