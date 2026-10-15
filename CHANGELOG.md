# Changelog

## [1.2.2](https://github.com/perplexityai/gazelle_rs/compare/v1.2.1...v1.2.2) (2026-10-15)


### Bug Fixes

* **ci:** smoke-test artifacts from one cross-build ([#33](https://github.com/perplexityai/gazelle_rs/issues/33)) ([f4e30f2](https://github.com/perplexityai/gazelle_rs/commit/f4e30f208939a00d03064dd6631fc697e5ba0104))
* respect local bindings in renamed Rust imports ([#37](https://github.com/perplexityai/gazelle_rs/issues/37)) ([448dd94](https://github.com/perplexityai/gazelle_rs/commit/448dd947384986be83fbd1479634b8917510c96f))

## [1.2.1](https://github.com/perplexityai/gazelle_rs/compare/v1.2.0...v1.2.1) (2026-10-11)


### Bug Fixes

* allow repeated sibling test filenames in generated roots ([#26](https://github.com/perplexityai/gazelle_rs/issues/26)) ([885ba21](https://github.com/perplexityai/gazelle_rs/commit/885ba210217e6cb5fd890096f940194fedeb98d7))
* generate snake-case sibling test module names ([#31](https://github.com/perplexityai/gazelle_rs/issues/31)) ([b776ea6](https://github.com/perplexityai/gazelle_rs/commit/b776ea6a85663af87ba859f4c064e1b2d4958c4d))

## 1.2.0 (2026-10-09)

## What's Changed
* fix: honor Gazelle strict mode for Rust diagnostics by @longlho in https://github.com/perplexityai/gazelle_rs/pull/10
* feat: resolve Rust dependencies from a Bazel crate catalog by @longlho in https://github.com/perplexityai/gazelle_rs/pull/11
* feat: generate standalone Rust test targets by @longlho in https://github.com/perplexityai/gazelle_rs/pull/14
* fix: respect package visibility for new Rust targets by @longlho in https://github.com/perplexityai/gazelle_rs/pull/15
* feat: separate Rust production and test sources by @longlho in https://github.com/perplexityai/gazelle_rs/pull/16
* feat: resolve crates from central Cargo manifests and lockfiles by @longlho in https://github.com/perplexityai/gazelle_rs/pull/18
* feat: support directive-only external proc-macro resolution by @longlho in https://github.com/perplexityai/gazelle_rs/pull/19
* fix: resolve dependencies from sparse registries by @longlho in https://github.com/perplexityai/gazelle_rs/pull/20
* fix: prefer root-selected crate aliases by @longlho in https://github.com/perplexityai/gazelle_rs/pull/21
* fix: keep toolchain registration local to root modules by @longlho in https://github.com/perplexityai/gazelle_rs/pull/17
* feat: generate one aggregate target for dedicated tests by @longlho in https://github.com/perplexityai/gazelle_rs/pull/22
* fix: leave aggregate test main generation to caller macros by @longlho in https://github.com/perplexityai/gazelle_rs/pull/23
* fix: honor explicit crate-root ownership by @longlho in https://github.com/perplexityai/gazelle_rs/pull/25
* fix: preserve computed dependency expressions during merges by @longlho in https://github.com/perplexityai/gazelle_rs/pull/24


**Full Changelog**: https://github.com/perplexityai/gazelle_rs/compare/v1.1.0...v1.2.0

## 1.1.0 (2026-10-06)

## What's Changed
* feat: respect language filters and resolve Cargo metadata by @longlho in https://github.com/perplexityai/gazelle_rs/pull/6
* fix: handle grouped imports, tool attributes, and sibling modules by @longlho in https://github.com/perplexityai/gazelle_rs/pull/8
* fix: preserve dependencies and respect Rust module and test ownership by @longlho in https://github.com/perplexityai/gazelle_rs/pull/9


**Full Changelog**: https://github.com/perplexityai/gazelle_rs/compare/v1.0.1...v1.1.0

## 1.0.1 (2026-10-06)

## What's Changed
* fix: match BCR maintainers and releaser to gazelle_py by @longlho in https://github.com/perplexityai/gazelle_rs/pull/4


**Full Changelog**: https://github.com/perplexityai/gazelle_rs/compare/v1.0.0...v1.0.1

## 1.0.0 (2026-10-06)

## What's Changed
* feat: add Rust Gazelle plugin with manifest-free internal imports by @longlho in https://github.com/perplexityai/gazelle_rs/pull/1
* ci: align automation and releases with gazelle_py by @longlho in https://github.com/perplexityai/gazelle_rs/pull/3

## New Contributors
* @longlho made their first contribution in https://github.com/perplexityai/gazelle_rs/pull/1

**Full Changelog**: https://github.com/perplexityai/gazelle_rs/commits/v1.0.0

## Changelog

Releases are managed by Release Please.
