# Cargo metadata with undeclared internal dependencies

This example uses Cargo package names and editions while letting Gazelle infer
internal dependencies entirely from Rust source imports:

```text
//app:report_cli → //service:report_engine → //model:line_items
       anyhow            anyhow                serde_json
```

The directory names (`app`, `service`, `model`) differ from their Cargo package
names (`report-cli`, `report-engine`, `line-items`). Generated `crate_name`
values normalize hyphens to underscores, matching the Rust imports.

- `model` parses a JSON array of unsigned integers with `serde_json`.
- `service` imports `line_items` and uses `anyhow` for parse context and checked
  sum errors. Its manifest deliberately does not declare `line-items`.
- `app` imports `report_engine` and returns `anyhow::Result`. Its manifest
  deliberately does not declare `report-engine`.
- The integration test combines a direct external `serde_json` dependency with
  the internal chain; service unit tests cover malformed input and overflow.

The root Cargo workspace and lockfile supply **external** dependencies to
`crate.from_cargo`. Root BUILD directives map `anyhow` and `serde_json` to
`@metadata_example_crates`. Gazelle's index resolves the internal crate names;
no internal `resolve` directives or Cargo path dependencies are present.

From this directory:

```sh
bazel run //:gazelle
bazel run //:gazelle -- -mode=diff
bazel test //...
bazel run //app:report_cli
# total=42
```

To intentionally refresh external dependencies, run `cargo generate-lockfile`
in this directory. Cargo can generate the lockfile without compiling source.
`cargo build` and `cargo test` are intentionally unsuitable here because the
internal dependency declarations are omitted. Use Bazel for compilation and
tests; the manifests demonstrate metadata support, not Cargo build parity.
