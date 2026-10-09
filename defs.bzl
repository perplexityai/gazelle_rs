"""Generate a Rust test crate root from dedicated test modules."""

def _root_impl(ctx):
    root = ctx.actions.declare_file(ctx.label.name + ".rs")
    if not ctx.files.srcs:
        fail("generated test roots require at least one .test.rs source")
    modules = {}
    lines = []
    for src in ctx.files.srcs:
        if src.owner.package != ctx.label.package or src.owner.workspace_name != ctx.label.workspace_name or not src.basename.endswith(".test.rs"):
            fail("generated test roots require package-local .test.rs files; set crate_root explicitly")
        module = src.basename.removesuffix(".test.rs").replace("-", "_")
        if not module or module in ["self", "super", "crate", "Self", "_"] or module[0] in "0123456789" or any([c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" for c in module.elems()]):
            fail("invalid test module {}; set crate_root explicitly".format(module))
        if module in modules:
            fail("duplicate test module {}; set crate_root explicitly".format(module))
        modules[module] = True
        relative = src.short_path.removeprefix(ctx.label.package + "/") if ctx.label.package else src.short_path
        lines.append("#[path = {}] mod r#{};".format(json.encode(relative), module))
    ctx.actions.write(root, "\n".join(lines) + "\n")
    return [DefaultInfo(files = depset([root]))]

rust_test_main_gen = rule(
    implementation = _root_impl,
    attrs = {"srcs": attr.label_list(allow_files = [".rs"])},
)
