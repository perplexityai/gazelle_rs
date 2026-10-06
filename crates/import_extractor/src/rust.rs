//! Parse crate source graphs without invoking Cargo or expanding macros.
use crate::pb;
use std::{
    collections::BTreeSet,
    fs,
    path::{Path, PathBuf},
};
use syn::parse::Parser;
use syn::{
    Attribute, Item, UseTree,
    visit::{self, Visit},
};

type Names = BTreeSet<String>;
fn ident(id: &syn::Ident) -> String {
    id.to_string().trim_start_matches("r#").into()
}
fn test_only(attrs: &[Attribute]) -> bool {
    attrs.iter().any(|a| {
        a.path().is_ident("cfg")
            && a.parse_args::<syn::Path>()
                .is_ok_and(|p| p.is_ident("test"))
    })
}
fn bindings(tree: &UseTree, names: &mut Names, parent: Option<&syn::Ident>) {
    match tree {
        UseTree::Path(p) => bindings(&p.tree, names, Some(&p.ident)),
        UseTree::Name(n) => {
            names.insert(ident(if n.ident == "self" {
                parent.unwrap_or(&n.ident)
            } else {
                &n.ident
            }));
        }
        UseTree::Rename(n) => {
            names.insert(ident(&n.rename));
        }
        UseTree::Group(g) => {
            for t in &g.items {
                bindings(t, names, parent);
            }
        }
        UseTree::Glob(_) => {}
    }
}
fn glob_paths(tree: &UseTree, path: &mut Vec<String>, out: &mut Vec<Vec<String>>) {
    match tree {
        UseTree::Path(p) => {
            path.push(ident(&p.ident));
            glob_paths(&p.tree, path, out);
            path.pop();
        }
        UseTree::Group(g) => {
            for tree in &g.items {
                glob_paths(tree, path, out);
            }
        }
        UseTree::Glob(_) => out.push(path.clone()),
        _ => {}
    }
}

fn locals(items: &[Item]) -> Names {
    let mut names = Names::new();
    for item in items {
        match item {
            Item::Mod(m) => {
                names.insert(ident(&m.ident));
            }
            Item::Use(u) => bindings(&u.tree, &mut names, None),
            Item::ExternCrate(e) => {
                names.insert(ident(e.rename.as_ref().map_or(&e.ident, |(_, n)| n)));
            }
            Item::Struct(i) => {
                names.insert(ident(&i.ident));
            }
            Item::Enum(i) => {
                names.insert(ident(&i.ident));
            }
            Item::Type(i) => {
                names.insert(ident(&i.ident));
            }
            Item::Trait(i) => {
                names.insert(ident(&i.ident));
            }
            _ => {}
        }
    }
    names
}

struct Extractor {
    sources: Names,
    imports: Names,
    test_imports: Names,
    has_tests: bool,
    active: Names,
    scopes: Vec<Names>,
}
impl Extractor {
    fn file(&mut self, file: &Path, module_dir: &Path, testing: bool) -> Result<(), String> {
        let canonical = fs::canonicalize(file).map_err(|e| format!("{}: {e}", file.display()))?;
        let key = canonical.to_string_lossy().into_owned();
        if !self.active.insert(key.clone()) {
            return Err(format!("cyclic module: {}", file.display()));
        }
        self.sources.insert(file.to_string_lossy().into_owned());
        let text = fs::read_to_string(file).map_err(|e| format!("{}: {e}", file.display()))?;
        let ast = syn::parse_file(&text).map_err(|e| format!("{}: {e}", file.display()))?;
        self.items(&ast.items, module_dir, file.parent().unwrap(), testing)?;
        self.active.remove(&key);
        Ok(())
    }
    fn items(
        &mut self,
        items: &[Item],
        module_dir: &Path,
        path_dir: &Path,
        testing: bool,
    ) -> Result<(), String> {
        let mut local = locals(items);
        for item in items {
            if let Item::Use(u) = item {
                let mut paths = Vec::new();
                glob_paths(&u.tree, &mut Vec::new(), &mut paths);
                for path in paths {
                    let scope = if path == ["crate"] {
                        self.scopes.first()
                    } else if !path.is_empty() && path.iter().all(|part| part == "super") {
                        self.scopes
                            .len()
                            .checked_sub(path.len())
                            .and_then(|i| self.scopes.get(i))
                    } else {
                        None
                    };
                    if let Some(scope) = scope {
                        local.extend(scope.iter().cloned());
                    }
                }
            }
        }
        self.scopes.push(local.clone());
        let modules: Names = items
            .iter()
            .filter_map(|i| {
                if let Item::Mod(m) = i {
                    Some(ident(&m.ident))
                } else {
                    None
                }
            })
            .collect();
        for item in items {
            if let Item::Mod(m) = item {
                let testing = testing || test_only(&m.attrs);
                self.has_tests |= testing;
                let name = ident(&m.ident);
                if let Some((_, items)) = &m.content {
                    let dir = module_dir.join(name);
                    self.items(items, &dir, &dir, testing)?;
                } else {
                    let explicit = m.attrs.iter().find_map(|a| {
                        if !a.path().is_ident("path") {
                            return None;
                        }
                        if let syn::Meta::NameValue(v) = &a.meta {
                            if let syn::Expr::Lit(l) = &v.value {
                                if let syn::Lit::Str(s) = &l.lit {
                                    return Some(s.value());
                                }
                            }
                        }
                        None
                    });
                    let file = if let Some(path) = &explicit {
                        path_dir.join(path)
                    } else {
                        let flat = module_dir.join(format!("{name}.rs"));
                        let nested = module_dir.join(&name).join("mod.rs");
                        match (flat.exists(), nested.exists()) {
                            (true, false) => flat,
                            (false, true) => nested,
                            _ => {
                                return Err(format!(
                                    "missing or ambiguous module {name} under {}",
                                    module_dir.display()
                                ));
                            }
                        }
                    };
                    let next =
                        if explicit.is_some() || file.file_name().is_some_and(|n| n == "mod.rs") {
                            file.parent().unwrap().to_path_buf()
                        } else {
                            module_dir.join(name)
                        };
                    self.file(&file, &next, testing)?;
                }
            } else {
                let mut visitor = Imports {
                    local: local.clone(),
                    modules: modules.clone(),
                    imports: &mut self.imports,
                    tests: &mut self.test_imports,
                    testing,
                    has_tests: &mut self.has_tests,
                };
                visitor.visit_item(item);
            }
        }
        self.scopes.pop();
        Ok(())
    }
}
struct Imports<'a> {
    local: Names,
    modules: Names,
    imports: &'a mut Names,
    tests: &'a mut Names,
    testing: bool,
    has_tests: &'a mut bool,
}
impl Imports<'_> {
    fn add(&mut self, name: String, explicit: bool) {
        if matches!(
            name.as_str(),
            "std" | "core" | "alloc" | "proc_macro" | "crate" | "self" | "super" | "Self"
        ) {
            return;
        }
        if self.modules.contains(&name) {
            return;
        }
        if !explicit && (self.local.contains(&name) || name.starts_with(char::is_uppercase)) {
            return;
        }
        if matches!(
            name.as_str(),
            "bool"
                | "char"
                | "str"
                | "u8"
                | "u16"
                | "u32"
                | "u64"
                | "u128"
                | "usize"
                | "i8"
                | "i16"
                | "i32"
                | "i64"
                | "i128"
                | "isize"
                | "f32"
                | "f64"
        ) {
            return;
        }
        if self.testing {
            self.tests.insert(name);
        } else {
            self.imports.insert(name);
        }
    }
    fn use_tree(&mut self, tree: &UseTree, own_bindings: &Names, absolute: bool) {
        match tree {
            UseTree::Path(p) => {
                let name = ident(&p.ident);
                // A qualified use can start at an already imported module or enum.
                // Keep roots introduced by this use itself (`external::{self, X}`).
                if absolute || !self.local.contains(&name) || own_bindings.contains(&name) {
                    self.add(name, true);
                }
            }
            UseTree::Name(n) => self.add(ident(&n.ident), true),
            UseTree::Rename(n) => self.add(ident(&n.ident), true),
            UseTree::Group(g) => {
                for t in &g.items {
                    self.use_tree(t, own_bindings, absolute);
                }
            }
            UseTree::Glob(_) => {}
        }
    }
}
impl<'ast> Visit<'ast> for Imports<'_> {
    fn visit_item(&mut self, item: &'ast Item) {
        let attrs = match item {
            Item::Fn(x) => &x.attrs,
            Item::Use(x) => &x.attrs,
            Item::ExternCrate(x) => &x.attrs,
            Item::Impl(x) => &x.attrs,
            Item::Struct(x) => &x.attrs,
            Item::Enum(x) => &x.attrs,
            Item::Const(x) => &x.attrs,
            Item::Static(x) => &x.attrs,
            Item::Type(x) => &x.attrs,
            Item::Trait(x) => &x.attrs,
            Item::Macro(x) => &x.attrs,
            _ => return visit::visit_item(self, item),
        };
        let previous = self.testing;
        self.testing |= test_only(attrs) || attrs.iter().any(|a| a.path().is_ident("test"));
        *self.has_tests |= self.testing;
        visit::visit_item(self, item);
        self.testing = previous;
    }
    fn visit_macro(&mut self, mac: &'ast syn::Macro) {
        self.visit_path(&mac.path);
        // Common Rust macros (assert_eq!, println!, vec!, etc.) take expression
        // lists. Inspect those expressions without expanding the macro.
        if let Ok(args) = syn::punctuated::Punctuated::<syn::Expr, syn::Token![,]>::parse_terminated
            .parse2(mac.tokens.clone())
        {
            for arg in &args {
                let mut nested = Imports {
                    local: self.local.clone(),
                    modules: self.modules.clone(),
                    imports: self.imports,
                    tests: self.tests,
                    testing: self.testing,
                    has_tests: self.has_tests,
                };
                nested.visit_expr(arg);
            }
        }
    }
    fn visit_attribute(&mut self, attr: &'ast Attribute) {
        // Compiler and registered tool attributes are not crate references.
        if attr.path().segments.first().is_some_and(|segment| {
            matches!(
                ident(&segment.ident).as_str(),
                "diagnostic" | "clippy" | "rustfmt"
            )
        }) {
            return;
        }
        if attr.path().is_ident("derive") {
            if let Ok(paths) = attr.parse_args_with(
                syn::punctuated::Punctuated::<syn::Path, syn::Token![,]>::parse_terminated,
            ) {
                for path in &paths {
                    if path.segments.len() > 1 {
                        self.add(ident(&path.segments[0].ident), false);
                    }
                }
            }
        }
        visit::visit_attribute(self, attr);
    }
    fn visit_block(&mut self, block: &'ast syn::Block) {
        let previous = self.local.clone();
        let items: Vec<Item> = block
            .stmts
            .iter()
            .filter_map(|s| {
                if let syn::Stmt::Item(i) = s {
                    Some(i.clone())
                } else {
                    None
                }
            })
            .collect();
        self.local.extend(locals(&items));
        visit::visit_block(self, block);
        self.local = previous;
    }
    fn visit_item_use(&mut self, item: &'ast syn::ItemUse) {
        let mut own_bindings = Names::new();
        bindings(&item.tree, &mut own_bindings, None);
        self.use_tree(&item.tree, &own_bindings, item.leading_colon.is_some());
    }
    fn visit_item_extern_crate(&mut self, item: &'ast syn::ItemExternCrate) {
        self.add(ident(&item.ident), true);
    }
    fn visit_path(&mut self, path: &'ast syn::Path) {
        if path.segments.len() > 1 {
            self.add(ident(&path.segments[0].ident), path.leading_colon.is_some());
        }
        visit::visit_path(self, path);
    }
}
pub fn extract(root: &str) -> Result<pb::CrateResult, String> {
    let root = PathBuf::from(root);
    let mut extractor = Extractor {
        sources: Names::new(),
        imports: Names::new(),
        test_imports: Names::new(),
        has_tests: false,
        active: Names::new(),
        scopes: Vec::new(),
    };
    extractor.file(
        &root,
        root.parent().ok_or("crate root has no parent")?,
        false,
    )?;
    Ok(pb::CrateResult {
        sources: extractor.sources.into_iter().collect(),
        imports: extractor.imports.into_iter().collect(),
        test_imports: extractor.test_imports.into_iter().collect(),
        has_tests: extractor.has_tests,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    struct Fixture(PathBuf);
    impl Fixture {
        fn new(files: &[(&str, &str)]) -> Self {
            let dir = std::env::temp_dir().join(format!(
                "gazelle-rs-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            for (name, text) in files {
                let file = dir.join(name);
                fs::create_dir_all(file.parent().unwrap()).unwrap();
                fs::write(file, text).unwrap();
            }
            Self(dir)
        }
        fn extract(&self) -> Result<pb::CrateResult, String> {
            extract(self.0.join("lib.rs").to_str().unwrap())
        }
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }
    #[test]
    fn discovers_imports_and_separates_tests_without_cargo() {
        let f = Fixture::new(&[
            (
                "lib.rs",
                r#"
                mod local;
                use local::Thing;
                use internal_api::{Client, nested::Other};
                use renamed_crate as alias;
                extern crate legacy as legacy_alias;
                pub fn run() { direct::run(); assert_eq!(macro_dep::answer(), 42); alias::run(); Thing::run(); legacy_alias::run(); }
                #[cfg(test)] mod tests { use test_support::Helper; #[test] fn test() {} }
                // use comment_is_not_a_crate::X;
                const TEXT: &str = "fake::import";
            "#,
            ),
            (
                "local.rs",
                "pub struct Thing; impl Thing { pub fn run() { sub_dep::run(); } }",
            ),
        ]);
        let result = f.extract().unwrap();
        assert_eq!(
            result.imports,
            [
                "direct",
                "internal_api",
                "legacy",
                "macro_dep",
                "renamed_crate",
                "sub_dep"
            ]
        );
        assert_eq!(result.test_imports, ["test_support"]);
        assert!(result.has_tests);
        assert_eq!(result.sources.len(), 2);
    }
    #[test]
    fn follows_nested_modules_paths_and_qualified_macros() {
        let f = Fixture::new(&[
            (
                "lib.rs",
                "mod nested; #[path = \"alternate.rs\"] mod custom;",
            ),
            (
                "nested/mod.rs",
                "mod child; pub fn f() { tracing::info!(\"hello\"); }",
            ),
            (
                "nested/child.rs",
                "use r#async::Value; use std::io; use crate::nested;",
            ),
            ("alternate.rs", "#[derive(serde::Serialize)] struct X;"),
        ]);
        let result = f.extract().unwrap();
        assert_eq!(result.imports, ["async", "serde", "tracing"]);
        assert_eq!(result.sources.len(), 4);
    }
    #[test]
    fn grouped_self_imports_bind_the_parent_module() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use std::fs::{self, File};
            use external::nested::{self, Value};
            use other::{self as renamed, Thing};
            fn run() {
                fs::read("file");
                nested::run();
                renamed::run();
                use std::io::{self, Write};
                io::stdout();
            }
        "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["external", "other"]);
    }

    #[test]
    fn compiler_attribute_namespaces_are_not_dependencies() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            #[diagnostic::on_unimplemented(message = "missing implementation")]
            trait Contract {}
            #[rustfmt::skip]
            #[clippy::msrv = "1.80"]
            #[tracing::instrument]
            fn run() { diagnostic::report(); }
        "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["diagnostic", "tracing"]);
        let f = Fixture::new(&[(
            "lib.rs",
            "#[diagnostic::on_unimplemented(message = \"missing\")] trait Contract {}",
        )]);
        assert!(f.extract().unwrap().imports.is_empty());
    }

    #[test]
    fn explicit_paths_in_flat_modules_are_relative_to_the_source_file() {
        let f = Fixture::new(&[
            ("lib.rs", "mod vault;"),
            (
                "vault.rs",
                r#"
                mod child;
                #[cfg(test)] #[path = "vault.test.rs"] mod tests;
                mod inline { #[path = "helper.rs"] mod helper; }
            "#,
            ),
            ("vault/child.rs", "use runtime_dep::Value;"),
            ("vault.test.rs", "use test_dep::Fixture;"),
            ("vault/inline/helper.rs", "use inline_dep::Helper;"),
        ]);
        let result = f.extract().unwrap();
        assert_eq!(result.imports, ["inline_dep", "runtime_dep"]);
        assert_eq!(result.test_imports, ["test_dep"]);
        assert!(result.has_tests);
        assert_eq!(result.sources.len(), 5);
    }

    #[test]
    fn ancestor_globs_carry_local_bindings_into_inline_and_file_modules() {
        let f = Fixture::new(&[
            (
                "lib.rs",
                r#"
                use std::fs;
                use external::{self, Value};
                mod sibling {}
                #[cfg(test)] mod inline {
                    use super::*;
                    fn run() { fs::read("file"); sibling::run(); external::run(); new_test_dep::run(); }
                    mod nested { use super::super::*; fn run() { fs::read("file"); } }
                }
                #[cfg(test)] #[path = "other.rs"] mod other;
                mod independent { fn run() { fs::external_crate(); } }
            "#,
            ),
            (
                "other.rs",
                r#"use crate::*; fn run() { fs::read("file"); sibling::run(); external::run(); }"#,
            ),
        ]);
        let result = f.extract().unwrap();
        assert_eq!(result.imports, ["external", "fs"]);
        assert_eq!(result.test_imports, ["new_test_dep"]);
        assert_eq!(result.sources.len(), 2);
    }

    #[test]
    fn qualified_uses_of_imported_modules_and_enums_are_local() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use filesystem::fs::{self, Dir};
            use events::Event;
            fn run() {
                use fs::OpenOptionsExt;
                use Event::*;
                use actual_crate::{self, Client};
                actual_crate::run();
            }
        "#,
        )]);
        assert_eq!(
            f.extract().unwrap().imports,
            ["actual_crate", "events", "filesystem"]
        );
    }

    #[test]
    fn errors_do_not_return_partial_graphs() {
        for text in [
            "mod missing;",
            "this is invalid rust",
            "#[path = \"lib.rs\"] mod cycle;",
        ] {
            let f = Fixture::new(&[("lib.rs", text)]);
            assert!(f.extract().is_err(), "{text}");
        }
    }
}
