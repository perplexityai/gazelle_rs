# Rust package conventions

This is the recommended layout and generation contract for Bazel-first users of
`gazelle_rs`. Packages do not need Cargo manifests. Cargo integration and explicit
BUILD declarations support existing layouts; they are not prerequisites for new
packages.

## Package boundaries and file discovery

Use a BUILD file at each Rust package boundary and set this at the repository root:

```starlark
# gazelle:generation_mode update_only
```

Gazelle supplies `RegularFiles` including descendants without their own BUILD
files, relative to the package directory. Gazelle applies its exclusions and
stops collection at nested BUILD boundaries. The Rust plugin filters that list
for dedicated tests; it does not search special `tests/` or `src/` directories.

`update_only` requires creating the package's BUILD file first, even if it is
empty. Gazelle's default create mode instead visits directories individually;
it does not provide the same package-wide file list. Use `update_only` when a
crate has source subdirectories.

Opt packages into Rust with `# gazelle:lang rs`, or add `rs` to the repository's
other enabled languages. Root `map_kind` directives can map Rust rules to custom
macros; they apply only where Rust is enabled. Use `gazelle:exclude` for files
that should actually be outside generation, not to prevent duplicate test
membership.

## Crate roots and production sources

Prefer flat packages with `lib.rs` for a library and `main.rs` for a binary.
Subdirectories are ordinary module organization, not separate crates. An
explicit BUILD `crate_root` selects a nonstandard root. The Rust parser follows
`mod` declarations and literal `#[path]` attributes; unrelated `.rs` files do not
automatically enter the library.

The inferred crate name is the package directory name with hyphens changed to
underscores. A package with a library and binary gets `<name>` and `<name>_bin`.
`rust_crate_name` overrides the inferred name in that package. Existing target
names and explicit crate roots take precedence. Additional binaries should have
explicit BUILD targets and roots; an arbitrary source file is not assumed to be
a binary.

The compatibility defaults also recognize `src/lib.rs` and `src/main.rs`, taking
precedence over flat roots if both exist. A local Cargo manifest can supply crate
names, edition, explicit targets, and implicit `src/bin/*.rs` or
`src/bin/*/main.rs` binaries. Those binary path conventions apply only to
manifest-backed packages. Avoid competing roots in new packages.

## Dedicated tests

Name independently compiled test modules `*.test.rs`. All unowned dedicated test
files in a package form **one** `<package>_test` target, including files in source
subdirectories supplied by Gazelle. There is no automatic per-file test mode.
Plain `.rs` files are not automatically discovered as integration tests.

Test the library through its public crate path. Aggregate test modules are a
separate crate and cannot access private library state. They may share helpers
through sibling module paths such as `crate::support`. Production targets do not
gain standalone test members or their dependencies.

Gazelle generates two declarations for the aggregate:

```starlark
load("@gazelle_rs//:defs.bzl", "rust_test_main_gen")
load("@rules_rs//rs:rust_test.bzl", "rust_test")

rust_test_main_gen(
    name = "widget_test_main",
    testonly = True,
    srcs = ["behavior.test.rs", "nested/support.test.rs"],
    visibility = ["//visibility:private"],
)

rust_test(
    name = "widget_test",
    srcs = ["behavior.test.rs", "nested/support.test.rs", ":widget_test_main"],
    crate_name = "widget_test",
    crate_root = ":widget_test_main",
    edition = "2021",
    deps = [":widget"],
)
```

`rust_test_main_gen` only writes one Rust source file. It does not compile, run
tests, or depend on a Rust rule implementation. A custom macro can call it and
pass its output as `crate_root`, including the output and member sources in its
test rule's `srcs`. Gazelle's `map_kind rust_test` mapping receives the same
explicit generated-root relationship; a root-generating test wrapper is not
required. Do not check in generated test mains.

Member module names come from the basename with `.test.rs` removed and `-`
changed to `_`, regardless of directory. Names must be valid ASCII Rust
identifiers, must be unique within the aggregate, and cannot be `_`, `self`,
`super`, `crate`, or `Self`. Other keywords are emitted as raw identifiers.
Use distinct basenames or an explicit root for a custom module layout.

An existing aggregate keeps its target and generator names. Newly discovered
members join it, and dependency extraction includes every member. With multiple
explicit aggregates, assign new members explicitly; Gazelle reports ambiguous
ownership. An existing test root owns its reachable modules and prevents an
additional inferred test for those members. A conflicting generated target name
is an error rather than a reason to create per-file tests.

### Existing in-crate tests

Private-state tests remain possible through an explicit crate root and module
graph. When a production crate declares test-only module files, Gazelle separates
production sources from test sources and can emit a source-based test target that
compiles that crate root with the full test graph. Inline tests alone do not
trigger target creation. This compatibility path is distinct from standalone
public-API tests; new dedicated tests should use the aggregate layout above.

Existing `rust_test(crate = ...)` rules remain manually maintained. Explicit
BUILD or Cargo test declarations can also describe nonstandard test sources.
Gazelle does not automatically rewrite these into the recommended layout.

## Imports and dependencies

Use the actual Rust crate name in external imports and normal `crate`, `self`,
and `super` paths for local modules. Hyphenated package names conventionally
become underscore-separated Rust names. Gazelle resolves internal crates by
crate name and namespace, not by guessing labels from directory names. It
extracts imports from the reachable source graph and unions aggregate members'
imports. Generated bindings or dynamic includes may need explicit dependencies
that the parser cannot observe.

For a Bazel-only repository, declare external requirements centrally and use:

```starlark
# gazelle:rust_cargo_lock @crates Cargo.toml Cargo.lock
# gazelle:rust_proc_macro serde_derive
```

The central manifest chooses direct versions and renames; the lockfile supplies
resolved packages. No per-package Cargo manifest, Cargo invocation, or generated
metadata file is required. The crate hub must export labels matching the chosen
convention. Direct dependencies with one selected version use unversioned aliases
such as `@crates//:serde`. Multiple direct versions and transitive-only crates
retain versioned labels where needed. Gazelle does not invent a missing hub alias.

Use `gazelle:resolve rs <import> <label>` for individual exceptions and
`gazelle:resolve_regexp rs <pattern> <replacement>` for a real naming convention.
Prefer derivable names over redundant mappings. Cargo.lock does not identify
proc-macro crates: classify external proc macros with `rust_proc_macro`, or use
an explicitly configured catalog/metadata provider. Internal proc-macro targets
are indexed directly. Proc macros go in `proc_macro_deps`; ordinary crates go in
`deps`.

## Defaults, overrides, and verification

The default edition is 2021; `rust_edition` inherits into child packages. Existing
explicit editions remain authoritative. New targets omit visibility unless
`rust_visibility` is configured, so Bazel's `package(default_visibility)` applies
(private when absent). Existing explicit visibility and test settings remain
user-owned.

Keep computed dependency expressions and explicit feature variants in BUILD
files. Use `# keep` only where inference cannot supply a required attribute or
dependency, such as opaque generated code. A keep is not needed just because a
dependency is external or a test module is nested.

Run `bazel run //:gazelle -- -strict` to update BUILD files and
`bazel run //:gazelle -- -strict -mode=diff` to check idempotence. Strict mode
reports unresolved imports, ambiguity, and incomplete source graphs rather than
accepting guessed dependencies. Build and run the generated targets as well:
source-to-BUILD snapshots alone cannot verify Rust compilation or module paths.
