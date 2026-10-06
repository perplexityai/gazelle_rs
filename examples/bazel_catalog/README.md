# Bazel-only catalog E2E

Run `./test.sh` from this directory, or set `BAZEL=bazelisk` to choose the Bazel
launcher. No package in this example has a Cargo manifest.

The local `fixture_crates` Bazel module supplies two versions of the same Rust
library and a procedural macro. The test exports their actual Bazel metadata,
regenerates the consumer BUILD from a seed missing its procedural-macro dependency,
and discovers a new standalone `.test.rs` target. It compiles and runs tests
asserting that a renamed import uses the older version and an explicit test
dependency selects the newer version. The standalone tests exercise first-party
library imports and procedural macros without inheriting dependencies from an
owner target. It also checks catalog reproducibility,
generation idempotence, and a nonzero exit with a diagnostic for unresolved or ambiguous
imports under `-strict`.

The script restores the committed consumer BUILD on exit. CI runs it on both
supported Bazel versions alongside the other examples.
