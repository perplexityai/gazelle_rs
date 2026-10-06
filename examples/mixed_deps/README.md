# Internal diamond with external dependencies

Four internal crates have **no Cargo manifests**. Gazelle infers the complete
internal graph from source imports:

```text
app ──→ service ──────→ model
  └──→ presentation ──→ model
```

External dependencies enter at multiple levels:

| Target | Direct internal dependencies | Direct external dependencies |
| --- | --- | --- |
| `//model` | — | `serde_json` |
| `//service` | `//model` | `anyhow` |
| `//presentation` | `//model` | `itoa` |
| `//app` | `//service`, `//presentation` | `anyhow` |
| `//app:workflow_test` | `//service`, `//presentation` | `anyhow`, `serde_json` |

The model parses a JSON user. The service trims and validates the name. The
presentation crate formats the numeric ID. The application combines both
branches, while the integration test creates input with `serde_json` and
checks the complete transformation.

`third_party/Cargo.toml` is only an external dependency catalog. Its empty
library makes it a valid Cargo package; it does not declare any internal crate.
`MODULE.bazel` exposes its locked dependencies through `@mixed_example_crates`.
The root BUILD file maps external import names explicitly, for example:

```starlark
# gazelle:resolve rs serde_json @mixed_example_crates//:serde_json
```

There are no internal `resolve` overrides: Gazelle finds internal libraries by
`crate_name` in its rule index. The catalog package disables Rust generation so
its placeholder library does not become an application target.

From this directory:

```sh
bazel run //:gazelle
bazel run //:gazelle -- -mode=diff
bazel test //...
bazel run //app
# Ada (#7)
```

To intentionally refresh external dependencies:

```sh
cargo generate-lockfile --manifest-path third_party/Cargo.toml
```

The Cargo catalog is isolated from the plugin's development workspace. Build
and test the application through Bazel; it has no Cargo workspace.
