# Bazel-only catalog E2E

Run `./test.sh` from this directory, or set `BAZEL=bazelisk` to choose the Bazel
launcher. No package in this example has a Cargo manifest.

The local `fixture_crates` Bazel module supplies two versions of the same Rust
library and a procedural macro. The test exports their actual Bazel metadata,
regenerates the consumer BUILD from a seed missing its procedural-macro dependency,
and compiles and runs a Rust test asserting that the explicitly selected older
version is used through a renamed import. It also checks catalog reproducibility,
generation idempotence, and a nonzero exit with a diagnostic for unresolved or ambiguous
imports under `-strict`.

The script restores the committed consumer BUILD on exit. CI runs it on both
supported Bazel versions alongside the other examples.
