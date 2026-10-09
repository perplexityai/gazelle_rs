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

## Recommended conventions

See [Rust package conventions](CONVENTIONS.md) for the Bazel-first contract:
package-wide file collection, flat crate roots, one aggregate for dedicated tests,
the independent `rust_test_main_gen` rule, central dependencies, and supported
explicit overrides. Cargo layouts are compatibility inputs, not requirements.

## Setup

The module is not published yet. In a consumer's `MODULE.bazel`, use a local
checkout until a release is available:

```starlark
bazel_dep(name = "gazelle_rs", version = "0.0.0")
local_path_override(module_name = "gazelle_rs", path = "../gazelle_rs")
bazel_dep(name = "gazelle", version = "0.51.3")
bazel_dep(name = "rules_rs", version = "0.0.111")
bazel_dep(name = "llvm", version = "0.8.18")
bazel_dep(name = "rules_go", version = "0.62.0", dev_dependency = True)
```

The root module must register toolchains for the plugin's Go, Rust, C++, and
prost builds. If your workspace does not already configure them, add:

```starlark
# Root modules select the toolchains used to build the plugin.
register_toolchains("@llvm//toolchain:all", dev_dependency = True)

toolchains = use_extension(
    "@rules_rs//rs/toolchains:module_extension.bzl",
    "toolchains",
    dev_dependency = True,
)
toolchains.toolchain(
    edition = "2024",
    version = "1.95.0",
)
use_repo(toolchains, "default_rust_toolchains")
register_toolchains("@default_rust_toolchains//...", dev_dependency = True)

rules_rust_prost = use_extension(
    "@rules_rs//rs:rules_rust_prost.bzl",
    "rules_rust_prost",
)
use_repo(rules_rust_prost, "rules_rust_prost")
register_toolchains("@rules_rust_prost//:default_prost_toolchain", dev_dependency = True)

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk", dev_dependency = True)
go_sdk.download(version = "1.24.12")
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

The setup above uses hermetic Rust/LLVM toolchains. Toolchain pins in
`gazelle_rs` itself are development-only and do not propagate to consumers.
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
Manifest-backed packages also discover Cargo's implicit `src/bin` binaries.
Dedicated `.test.rs` files are selected from Gazelle's `RegularFiles`, without
special test directories. With `# gazelle:generation_mode update_only`, Gazelle
includes descendant files up to nested BUILD boundaries and applies exclusions;
its default create mode supplies files for each directory separately. A file
reachable through a library, binary, or procedural macro's module graph remains
part of that crate instead of receiving an inferred standalone target. This
includes test and feature-gated modules and literal `#[path]` attributes.
Explicit BUILD test targets and Cargo test declarations still take precedence.
Unowned `.test.rs` files form one `<package>_test` aggregate with a build-generated
crate root. Explicit test roots own their reachable modules too. Subdirectories
owned by a crate do not generate duplicate crates.

The parser follows declared out-of-line and inline `mod` trees, including
literal `#[path = "..."]` attributes. Only reachable source files become
`srcs`. It collects `use` trees, re-exports, `extern crate`, qualified expression
and type paths, qualified macro invocations, expression-list macro arguments, and qualified derive paths. It
ignores standard-library paths, local modules, and imported aliases in ordinary
qualified expressions. Test-only sources and imports are separated from production
sources and dependencies, including `cfg(all(test, ...))`. A source reachable from
both production and test modules stays in the production set.

A library or binary with test-only module files gets one `<name>_test` target.
The production target contains production sources/dependencies only; the test
target compiles the same `crate_root` with the complete source graph and both
production and test dependencies. This keeps private-state tests in the same
Rust crate without putting test files in the library. Existing source-based test
targets for that root retain their names and feature variants; no extra default
target is added. Inline tests alone do not create a target.

New unit-test targets copy the owning target's crate name, edition, features,
compile data, Rust flags, and environment attributes. Subsequent edits to those
explicit test settings remain user-owned. Libraries with computed dependencies
need an explicit source-based test rule and emit a diagnostic instead of guessing
its dependency expressions. Procedural macro unit-test targets remain explicit.
Legacy `rust_test(crate = ...)` rules and libraries supplying their test-only
sources remain unchanged: migrate them to explicit `srcs` and the same
`crate_root` to adopt source partitioning. `crate` and `srcs` cannot be combined.

Production roots with computed
`srcs` still participate in ownership discovery without regenerating their rules.
If a production graph cannot be parsed, inferred `.test.rs` discovery in that
package is deferred; explicit test targets continue to be processed.

Explicit rules with literal sources and crate roots retain their names and
crate names. Unrelated attributes survive Gazelle's merge. Computed `srcs`
remain unmanaged; computed `deps`/`proc_macro_deps` are preserved, including
custom `select` conditions and helper calls. Gazelle's
`# keep` comments remain available for hand-maintained values. Failed parsing,
missing modules, and source graphs crossing Bazel package boundaries leave the
affected target unchanged and produce diagnostics.

Resolution order:

1. `# gazelle:resolve rs <crate> <label>` override.
2. A unique indexed internal library or proc-macro crate, regardless of Cargo
   dependency declarations.
3. A unique external crate from the configured Cargo metadata catalog.
4. An unresolved/ambiguous-import diagnostic. Existing dependency attributes
   are left unchanged when any import cannot be resolved; no external labels
   are guessed. These diagnostics do not make Gazelle exit nonzero.

Internal proc-macro rules are indexed separately so their imports land in
`proc_macro_deps`. External crates can use a Cargo metadata catalog (below) or explicit overrides, for example:

```starlark
# gazelle:resolve rs serde @crates//:serde
```

Without Cargo metadata, external procedural macro dependencies should be
maintained explicitly with `# keep`; a label override alone does not reveal
their rule kind.

## Configuration

| Directive | Default | Meaning |
| --- | --- | --- |
| `rust_extension` | `enabled` | `disabled` skips generation; inherited. |
| `rust_edition` | `2021` | Edition used when the local manifest does not supply one; inherited. |
| `rust_crate_name` | Package/directory name, with hyphens replaced by underscores | Override the default crate name in this package only. |
| `rust_visibility` | unset | Space-separated visibility labels for new targets; inherited. Unset or empty omits the attribute, respecting `package(default_visibility)` (private when absent). |
| `rust_cargo_metadata` | Unset | `@repository workspace-relative/path.json` for a rules_rs Cargo metadata catalog; inherited. |

Gazelle's standard `exclude` directive also applies to crate-root discovery, including inherited directory exclusions and glob patterns. It leaves existing excluded rules untouched; source files reached through another crate's module graph remain part of that crate.

Gazelle's `resolve` and `map_kind` directives work normally. For rules_rust or
custom wrappers, map each desired kind, for example:

```starlark
# gazelle:map_kind rust_library rust_library @rules_rust//rust:defs.bzl
# gazelle:map_kind rust_binary rust_binary @rules_rust//rust:defs.bzl
# gazelle:map_kind rust_test rust_test @rules_rust//rust:defs.bzl
```

### Root mappings with package opt-in

Use Gazelle's standard `lang` directive to select Rust per package. For example,
keep Go and Proto enabled at the root and opt a subtree into Rust:

```starlark
# Root BUILD.bazel
# gazelle:lang go,proto
# gazelle:map_kind rust_library custom_rust_library //:rules.bzl
```

```starlark
# Opted-in package/BUILD.bazel
# gazelle:lang go,proto,rs
```

Each list replaces the inherited list and applies to descendants, so include all
languages that should remain enabled. An empty `# gazelle:lang` selects all
languages. The command-line `-lang` filter also controls mapping activation when
not overridden by a BUILD directive. The existing `rust_extension` directive is
retained for compatibility; it is not required for package opt-in.
Excluded packages retain their existing rule kinds, loads, and attributes.
As with other languages, `lang` also excludes their Rust rules from the dependency
index. Opt in a dependency's package or use an explicit `resolve` directive to
reference it during migration. Mappings are inherited through excluded directories
and can be overridden in a child BUILD.
Other languages' mappings are unaffected.

Opting in still applies the configured mappings to existing Rust rules in that
package. A wrapper may add tests or dependencies compared with a raw rule, so
review the expanded Bazel targets when migrating.

### External crates from Cargo metadata

For a `rules_rs` crate hub, one workspace-level Cargo metadata snapshot replaces
per-import `resolve` directives. Generate it from the same central Cargo manifest,
lockfile, and feature selection used by `crate.from_cargo`:

```sh
cargo metadata --manifest-path third_party/Cargo.toml --locked --format-version=1 > third_party/cargo-metadata.json
```

Configure the hub's apparent repository name and the workspace-relative JSON path:

```starlark
# gazelle:rust_cargo_metadata @crates third_party/cargo-metadata.json
```

The plugin reads the file; it never invokes Cargo, rustc, or Bazel to resolve an
import. Regenerate the snapshot when the central dependency catalog changes.
Do not use `--no-deps`: external library names and proc-macro target kinds are
absent from that output. `Cargo.lock` alone also lacks those details.

External imports resolve to the hub's versioned aliases, for example
`serde_json` to `@crates//:serde_json-1.0.150`. Package names that differ from their
library name use the actual Cargo library target name. Proc-macro libraries go
into `proc_macro_deps`, and renamed dependencies populate `aliases`, preserving
existing aliases and comments. This assumes the `rules_rs` convention
`@repository//:<package>-<version>`; custom label layouts still need explicit
`resolve` directives.

Resolution prefers explicit overrides, then first-party libraries, then Cargo
metadata. A workspace package's resolved normal/dev dependency edges can select
a version or a renamed import. Otherwise, the catalog resolves only unique
library names or unique dependency aliases. Thus new and migrated packages need
no per-package Cargo manifest. Multiple matching versions remain ambiguous:
provide an explicit versioned override instead of relying on a highest-version
heuristic. Two different sources with the same package name and version are
rejected because they would map to the same Bazel label.

The snapshot does not generate external repositories, enable features, or infer
platform selects. Preserve computed dependencies and validate the resulting
configured targets against the existing build before removing Cargo manifests.

## Strict diagnostics

Use `bazel run //:gazelle -- -strict` to fail on invalid configuration,
unreadable or unparseable crate sources, and unresolved or ambiguous imports.
Without strict mode, Gazelle reports these diagnostics and preserves existing
dependencies when inference is incomplete. Strict mode does not detect imports
hidden by arbitrary macro expansion or validate configurations it cannot model.

## Bazel-only dependency catalog

Use a versioned crate catalog when BUILD files own first-party dependencies.
It records actual Bazel labels, library import names and procedural-macro kinds;
no first-party Cargo manifests or Cargo metadata invocation are required.

```starlark
# gazelle:rust_crate_catalog tools/rust/crates.json
```

The path is relative to the repository root. This directive and
`rust_cargo_metadata` select alternative catalogs; the nearest configuration
wins (the last directive wins if both appear in one file).

Export from a crate repository's aliases and their Rust targets:

```sh
bazel query 'deps(attr(actual, "^@@?[^/]+//", @crates//:all), 1)' \
  --output=xml > /tmp/rust-crates.xml
bazel run @gazelle_rs//cmd/crate_catalog -- \
  -input /tmp/rust-crates.xml -prefix @crates//: > tools/rust/crates.json
```

Run the second command only if the query succeeds. The example filters out
aliases to first-party targets; Gazelle indexes those from their BUILD files.
Increase the query depth for repositories with chains of aliases. The exporter
fails on missing alias targets or cycles, ignores non-Rust targets, and strips
machine paths and canonical repository names from its output. It does not build
or run the dependency crates. Refresh the catalog when the Bazel dependency
repository changes; ordinary Gazelle runs only read the checked-in JSON.

Vendored repositories can use a query over their targets and a prefix such as
`//third_party/crates:`. Custom exporters can produce the same schema:

```json
{
  "version": 1,
  "crates": [
    {"name": "wire_codec", "label": "@crates//:codec-1.2.3", "aliases": ["@crates//:codec"]},
    {"name": "wire_derive", "label": "@crates//:wire-derive-2.0.0", "proc_macro": true}
  ]
}
```

`aliases` lists alternative Bazel labels for the same target, not Rust import
renames. Rust renames belong in the consuming rule's `aliases` attribute.
Existing literal deps disambiguate multiple catalog versions. If a new import
has multiple candidates, use the standard `gazelle:resolve rs` directive to
choose explicitly; Gazelle never chooses the newest version implicitly.

The [`examples/bazel_catalog`](examples/bazel_catalog) E2E test exports a real
Bazel catalog, regenerates BUILD files from incomplete declarations, builds and
tests the result, checks idempotence, and verifies strict unresolved-import
failure. It contains no Cargo manifests and runs in the example CI matrix.

## Scope

This is source-level dependency inference, not Cargo or rustc execution. It does
not expand macros, execute build scripts, discover `include!` inputs, evaluate
feature/platform `cfg` expressions, or translate Cargo dependency tables into
external repositories. Feature/platform branches are inspected conservatively;
all declared module files must exist. A `cfg` expression is test-only when it
is guaranteed false without `test`; unknown feature/platform predicates stay
conservative (for example, `cfg(any(test, feature = "extra"))` is production-capable).
Macro-generated imports and unusual lexical shadowing may need
explicit BUILD maintenance. Existing computed source rules are indexed by
crate name but not regenerated. Workspace-inherited Cargo metadata, auto-target
disabling flags, examples/benches, and nested independently built crates inside
another crate's source graph are not modeled.

Bazel 8.5+ is required. CI targets 8.6 and 9.0 with separate Linux test and
example jobs, a macOS plugin smoke test on non-PR events, and macOS/Windows
cross-target analysis from Linux. Cross-target analysis does not link or execute
those binaries.

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

### GitHub Actions

| Workflow | Trigger and purpose |
| --- | --- |
| `ci.yaml` | PRs, merge queue, and pushes to main: Linux Bazel tests, all runnable examples and generation diffs, and Darwin/Windows target analysis. Non-PR events also run a native macOS smoke test. |
| `verify-hooks.yml` | PRs, merge queue, and main: install pinned pnpm/lefthook tooling; PRs validate the title and commits using the same Conventional Commit rules as gazelle_py. |
| `release-smoke.yml` | PRs, merge queue, and main: create a disposable local tag, build a source archive, and verify its contents, SRI, version marker, and BCR source template without publishing. |
| `release-please.yml` | Main pushes or manual dispatch: maintain the release PR, changelog, `version.txt`, and `MODULE.bazel`; create the release tag. |
| `module_release.yaml` | Version tags or manual dispatch of an existing tag: publish the source archive using bazel-contrib's reusable workflow, then submit to BCR. |

Release/manual workflows must be merged onto the default branch before they
appear as dispatchable workflows in GitHub Actions. To run the local checks:

```sh
pnpm install --frozen-lockfile
pnpm exec commitlint --from main --to HEAD
python3 .github/scripts/check_release.py
```

Release Please starts at `0.0.0`. Publishing requires the same organization
setup as gazelle_py: `GH_RELEASE_TOKEN`, `BCR_PUBLISH_TOKEN`, a
`perplexityai/bazel-central-registry` fork, and the publish-to-bcr app installed
on that fork. No releases or registry submissions have been created by this
setup. Confirm the maintainer entries in `.bcr/metadata.template.json` before
publishing.

## Breaking change: standalone test generation

Gazelle no longer creates or updates owner-based `rust_test(crate = ":owner")`
targets. Existing owner-based targets are left manually maintained, including
sources, aliases, and dependency expressions. The `rust_generate_unit_tests`
directive has been removed; delete it from BUILD files before upgrading.

For generated tests, move tests into a standalone `*.test.rs` file at the package
root or directly under `src/`, and import the library by its crate name. Do not
include this file in the library with `mod` or `#[path]`. Run Gazelle to create a
`rust_test` with explicit `srcs` and dependencies, and a caller-generated crate root. Existing
Cargo test declarations and explicit BUILD
test roots continue to work.

For ordinary library unit tests, prefer one explicit owner-based test target
running adjacent `.test.rs` modules together. This preserves Rust's normal module
privacy and avoids a separate test binary per source file. Attach each file to
its owning module without per-file exclusions:

```rust
#[cfg(test)]
#[path = "widget.test.rs"]
mod tests;
```

One explicit `rust_test(crate = ":owner")` runs the owner's test modules together;
separate files do not require separate targets. Gazelle does not create or update
that owner-based target. Use independent standalone test crates when a consumer
boundary, separate dependencies, or different runtime configuration is useful;
those tests can access only the library's public API.

Remove an old owner-based target only after its replacement builds and runs;
Gazelle does not delete or convert it automatically. When retaining its target
name, replace `crate` with the new `crate_root` and `srcs` before generation.
Version choices and renamed imports belong on each standalone test's own
`deps` and `aliases`, or in a `gazelle:resolve` directive.

This deliberately removes implicit unit-test generation, owner-target refresh,
and the associated policy flag. It avoids inheriting dependency choices from
another target and makes every generated test use ordinary crate resolution.

## Visibility defaults

New targets no longer receive `//visibility:public` automatically. Without a
`rust_visibility` directive, Gazelle omits the attribute so Bazel applies
`package(default_visibility)` or its private default. Existing target visibility
is preserved, including targets that already omit the attribute.

To keep newly generated targets public, explicitly configure
`# gazelle:rust_visibility //visibility:public` at the desired BUILD scope.
An empty `# gazelle:rust_visibility` clears an inherited override and restores
package-default behavior. This changes visibility only for newly discovered
targets; Gazelle does not narrow existing public targets automatically.

### Central manifest and lockfile

Repositories with conventional crate-hub labels can avoid a full generated catalog:

```starlark
# gazelle:rust_cargo_lock @crates Cargo.toml Cargo.lock crate_exceptions.json
```

Paths are workspace-relative; the final exceptions path is optional. Gazelle
reads these files directly, without invoking Cargo or requiring manifests in
consumer packages. Direct dependencies declared in `[dependencies]` or
`[workspace.dependencies]` use `@crates//:<package>` when their Cargo version
requirements select one locked version. Multiple transitive versions in the
lockfile do not prevent this. Renamed dependencies are filtered by their own
requirements and generate Rust import aliases. Matching uses Cargo's `semver`
implementation, including implicit caret ranges and prerelease rules.

The hub must export the unversioned alias for that selected version. If the
central manifest selects multiple direct versions of the same package, Gazelle
keeps versioned labels for all of them rather than guessing which is the hub's
default. Transitive-only packages also retain `@crates//:<package>-<version>`.
Rust import names are inferred by replacing package-name hyphens with underscores.

This is an opt-in convention, not an inspection of external crate sources. The
manifest and lockfile do not expose library target names or procedural-macro
kinds. Record those exceptions using the existing version-1 crate catalog format:

```json
{"version":1,"crates":[
  {"name":"derive_api","label":"@crates//:derive-package-1.0.0","proc_macro":true}
]}
```

An exception replaces inference for its label. For a custom label, include the
conventional versioned label in its `aliases` array. Exceptions may also supply
git dependencies; workspace/path and git packages are not inferred as registry
labels. Conflicting registry sources for the same package/version are rejected.
The crate hub must actually export the conventional labels, and imports from
non-library packages still need an explicit mapping.

If a requirement matches multiple locked versions, existing BUILD dependencies
can disambiguate; otherwise strict generation reports ambiguity. A requirement
matching no locked version leaves the import unresolved instead of selecting an
incompatible version. Explicit `gazelle:resolve` directives, catalog labels, and
BUILD rename aliases remain authoritative. Use Cargo metadata resolution when
package-specific Cargo feature/version selection is desired instead.

For repositories that standardize imports on actual Rust library names, ordinary
Gazelle directives can replace the exceptions catalog entirely:

```starlark
# gazelle:rust_cargo_lock @crates Cargo.toml Cargo.lock
# gazelle:resolve rs actual_library @crates//:different-package-1.0.0
# gazelle:resolve_regexp rs ^vendor_(.*)$ @vendor//:$1
# gazelle:rust_proc_macro serde_derive tokio_macros
```

`resolve` and `resolve_regexp` map imports to labels. `rust_proc_macro` separately
classifies whitespace-separated import names as compiler plugins, placing them
in `proc_macro_deps` rather than `deps`. It inherits into child packages; child
directives add names without changing sibling scopes. It neither adds unused
dependencies nor determines their labels. Internal indexed proc-macro rules and
explicit catalog/metadata classifications continue to work without this directive.

An explicit mapping is authoritative about the import name when Cargo.lock only
inferred a name from the package. It does not create a rename alias from that
guess. Actual renamed imports should use explicit BUILD `aliases` or central
manifest dependency renames. Prefer exact mappings for isolated exceptions and
regex mappings only for a real label-naming convention; neither guesses versions.

### Aggregate test roots

Automatically discovered `.test.rs` files form one test target per package.
Gazelle emits only that target, its source list, and dependencies. It never emits
a test-main generator or a generated-root label.

Map `rust_test` to a caller-owned macro with the standard `gazelle:map_kind`
directive. The macro generates a main when no explicit `crate_root` is supplied.
The independent `rust_test_main_gen` rule in `@gazelle_rs//:defs.bzl` remains
available for this; `test_support/test_rules.bzl` shows a symbolic macro using it.
Member module names come from file basenames with `.test.rs` removed and hyphens
replaced by underscores. Duplicate names require an explicit crate root.

Existing rootless aggregates keep their names and gain newly discovered test
files. Multiple aggregates require explicit member assignments. Explicit roots
keep their module ownership. `examples/basic/grouped` tests empty-BUILD
generation, compilation through the caller macro, and regeneration idempotence.

When upgrading an existing aggregate, remove its BUILD-level `rust_test_main_gen`
and remove that generated label from the test's `srcs` and `crate_root`. Configure
the caller macro before regeneration. Hand-maintained explicit roots stay supported.
