"""Public cgo adapter used to validate the standalone source export."""

load("@rules_rs//rs:rust_library.bzl", "rust_library")
load("@rules_rust//rust:rust_common.bzl", "COMMON_PROVIDERS")
load("@with_cfg.bzl", "with_cfg")

cgo_rust_library, _cgo_rust_library_internal = (
    with_cfg(rust_library, extra_providers = COMMON_PROVIDERS)
        .set(Label("@rules_rust//rust/settings:experimental_use_allocator_libraries_with_mangled_symbols"), True)
        .build()
)
