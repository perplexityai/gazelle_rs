# Rust conventions

Bazel builds. Gazelle writes BUILD files. No per-package Cargo.toml needed.

## Flat packages

Put sources next to BUILD. **Do not add a `src/` wrapper directory.**

```text
widget/
  BUILD.bazel
  lib.rs
  behavior.test.rs
  support.test.rs
```

- `lib.rs`: library root. `main.rs`: binary root.
- Use subdirectories when they help organize modules. No special directory names.
- A nested BUILD starts a separate package.
- Production sources follow `mod` and literal `#[path]` declarations. An unrelated
  `.rs` file does not enter the library just because it exists.
- Extra binaries or unusual roots: declare BUILD targets with `crate_root`.

## Let Gazelle find files

Set this at the repo root:

```starlark
# gazelle:generation_mode update_only
```

Create each package's BUILD first. Empty is fine. Gazelle supplies `RegularFiles`
for the whole package, including subdirectories without BUILD files. Exclusions
and nested package boundaries are already handled. Rust filters that list.
Default create mode supplies files per directory instead.

Opt in with `# gazelle:lang rs`, adding other languages as needed. Root `map_kind`
directives map rules to custom macros only where Rust is enabled.

Use `gazelle:exclude` to exclude files. Do not use it to fix duplicate test targets.

## One test target

- Name test modules `*.test.rs`. Plain `.rs` files are not auto-discovered tests.
- Unowned test modules form one `<package>_test`. No automatic per-file mode.
- Test through the library's public API. These tests compile as a separate crate.
- Share test helpers through sibling paths such as `crate::support`.
- Keep standalone test sources and dependencies out of production targets.

Gazelle emits only the test target. Map `rust_test` to a caller macro that owns
the generated main:

```starlark
# gazelle:map_kind rust_test project_rust_test //:defs.bzl

project_rust_test(
    name = "widget_test",
    srcs = ["behavior.test.rs", "support.test.rs"],
    crate_name = "widget_test",
    edition = "2021",
    deps = [":widget"],
)
```

Use a symbolic macro to call `rust_test_main_gen` from `@gazelle_rs//:defs.bzl`
when no explicit root is supplied. It writes the root; the macro adds that output
to `srcs` and passes it as `crate_root` to the native test rule.
See `test_support/test_rules.bzl` for a working caller. Do not check in generated mains.
Gazelle does not generate helper targets, loads, or labels.

Module name = basename minus `.test.rs`, with `-` changed to `_`.
Repeated basenames use package-relative paths: `cache/store.test.rs` becomes
`cache__store`, and `db/store.test.rs` becomes `db__store`. `/` becomes `__`;
hyphens become underscores. Unique basenames keep their existing module names.

Names must be ASCII Rust identifiers. No `_`, `self`, `super`, `crate`, or `Self`.
Other keywords use raw identifiers. If normalized names still collide, or a
custom module layout is needed, supply an explicit root.

Existing aggregates keep their names. New files join the single aggregate.
Multiple aggregates need explicit member assignments. Explicit roots own their
reachable modules. Name conflicts produce errors, not extra per-file targets.

## Central dependencies

Declare external requirements in the root Cargo.toml. Resolve them with:

```starlark
# gazelle:rust_cargo_lock @crates Cargo.toml Cargo.lock
# gazelle:rust_proc_macro serde_derive
```

No Cargo build. No per-package manifests. No metadata file required.

- Import external crates by their Rust names. Use `crate`, `self`, and `super`
  for local modules. Package hyphens usually become Rust underscores.
- Internal dependencies resolve by crate name and namespace.
- The root manifest supplies direct versions and renames. The lockfile supplies
  resolved packages.
- One selected direct version uses an unversioned label: `@crates//:serde`.
  Multiple direct versions and transitive-only crates retain versioned labels
  where needed. The crate hub must export those labels.
- Use `gazelle:resolve rs <import> <label>` for exceptions. Use `resolve_regexp`
  for a real naming convention. Skip mappings Gazelle can derive.
- Cargo.lock has no proc-macro classification. Declare external proc macros with
  `rust_proc_macro`. Internal proc-macro targets are indexed automatically.
- Proc macros go in `proc_macro_deps`. Other crates go in `deps`.

## Defaults and overrides

- Crate name: directory name, replacing `-` with `_`. Override with
  `rust_crate_name`. Library plus binary: `<name>` and `<name>_bin`.
- Edition: 2021. Set `rust_edition` to change it; children inherit it.
- Visibility: Bazel's package default, private if absent. `rust_visibility`
  overrides the default for new targets.
- Existing names, roots, editions, visibility, and test settings remain explicit.
- Keep feature variants and computed dependency expressions in BUILD.
- Use `# keep` when inference cannot see a real dependency, such as generated
  bindings. External dependencies and nested tests do not need keeps by default.

## Existing layouts

Old Cargo layouts still work. That is compatibility, not the recommended layout.
Legacy source roots take precedence over flat roots when both exist. Local
manifests can supply names, editions, targets, and implicit Cargo binaries.
See [crate discovery](README.md#crate-discovery-and-imports) for details.

Existing in-crate tests can access private state. Gazelle separates test-only
module files from production sources and can generate a test compiling the
original crate root. Inline tests alone do not create a target. New standalone
tests should use the public API instead.

`rust_test(crate = ...)` stays manually maintained. Explicit BUILD and Cargo test
declarations still support custom layouts. Gazelle does not migrate them for you.
Catalog and Cargo metadata dependency providers remain optional alternatives.

## Check it

```sh
bazel run //:gazelle -- -strict
bazel run //:gazelle -- -strict -mode=diff
bazel test //widget:all
```

Fix unresolved imports, ambiguity, and incomplete module graphs. Do not guess
dependencies. Build and run tests; a clean BUILD diff does not prove Rust compiles.
