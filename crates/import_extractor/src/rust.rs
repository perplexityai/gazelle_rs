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
// Evaluate only what is known when compiling without the test harness.
fn without_test(meta: &syn::Meta) -> Option<bool> {
    match meta {
        syn::Meta::Path(p) if p.is_ident("test") => Some(false),
        syn::Meta::List(list) => {
            let args = list
                .parse_args_with(
                    syn::punctuated::Punctuated::<syn::Meta, syn::Token![,]>::parse_terminated,
                )
                .ok()?;
            let values: Vec<_> = args.iter().map(without_test).collect();
            if list.path.is_ident("all") {
                if values.contains(&Some(false)) {
                    Some(false)
                } else if values.iter().all(|v| *v == Some(true)) {
                    Some(true)
                } else {
                    None
                }
            } else if list.path.is_ident("any") {
                if values.contains(&Some(true)) {
                    Some(true)
                } else if values.iter().all(|v| *v == Some(false)) {
                    Some(false)
                } else {
                    None
                }
            } else if list.path.is_ident("not") && values.len() == 1 {
                values[0].map(|v| !v)
            } else {
                None
            }
        }
        _ => None,
    }
}
fn test_only(attrs: &[Attribute]) -> bool {
    attrs.iter().any(|a| {
        a.path().is_ident("cfg")
            && a.parse_args::<syn::Meta>()
                .is_ok_and(|m| without_test(&m) == Some(false))
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

fn is_quote_path(path: &[String]) -> bool {
    matches!(path, [krate, name] if
        (krate == "quote" && matches!(name.as_str(), "quote" | "quote_spanned")) ||
        (krate == "syn" && matches!(name.as_str(), "parse_quote" | "parse_quote_spanned")))
}

fn quote_bindings(tree: &UseTree, path: &mut Vec<String>, names: &mut Names) {
    match tree {
        UseTree::Path(p) => {
            path.push(ident(&p.ident));
            quote_bindings(&p.tree, path, names);
            path.pop();
        }
        UseTree::Group(g) => {
            for tree in &g.items {
                quote_bindings(tree, path, names);
            }
        }
        UseTree::Name(n) => {
            path.push(ident(&n.ident));
            if is_quote_path(path) {
                names.insert(ident(&n.ident));
            }
            path.pop();
        }
        UseTree::Rename(n) => {
            path.push(ident(&n.ident));
            if is_quote_path(path) {
                names.insert(ident(&n.rename));
            }
            path.pop();
        }
        UseTree::Glob(_) => {}
    }
}

fn local_macros(items: &[Item]) -> Names {
    items
        .iter()
        .filter_map(|item| match item {
            Item::Macro(mac) => mac.ident.as_ref().map(ident),
            _ => None,
        })
        .collect()
}

// Imported quote macros are scoped just like other item bindings. A local
// declaration or a different import can shadow an inherited quote alias.
fn scope_quote_macros(items: &[Item], names: &mut Names) {
    for name in locals(items).into_iter().chain(local_macros(items)) {
        names.remove(&name);
    }
    for item in items {
        if let Item::Use(u) = item {
            quote_bindings(&u.tree, &mut Vec::new(), names);
        }
    }
}

struct Scope {
    names: Names,
    quote_macros: Names,
}

fn type_bindings(tree: &UseTree, macros: &Names, names: &mut Names) {
    match tree {
        UseTree::Name(n) if macros.contains(&ident(&n.ident)) => {}
        UseTree::Rename(n) if macros.contains(&ident(&n.ident)) => {}
        UseTree::Group(g) => {
            for tree in &g.items {
                type_bindings(tree, macros, names);
            }
        }
        _ => bindings(tree, names, None),
    }
}

fn locals(items: &[Item]) -> Names {
    let mut names = Names::new();
    let macros = local_macros(items);
    for item in items {
        match item {
            Item::Mod(m) => {
                names.insert(ident(&m.ident));
            }
            Item::Use(u) => type_bindings(&u.tree, &macros, &mut names),
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

// Only inspect transcribers, not matcher tokens or quoted strings. Nested token
// groups are opaque except repetitions, whose bodies can emit module items.
fn emits_file_module(mut cursor: syn::buffer::Cursor<'_>) -> bool {
    while !cursor.eof() {
        if let Some((name, mut next)) = cursor.ident()
            && name == "mod"
        {
            if let Some((punct, rest)) = next.punct()
                && punct.as_char() == '$'
            {
                next = rest;
            }
            if let Some((_, rest)) = next.ident()
                && rest.punct().is_some_and(|(p, _)| p.as_char() == ';')
            {
                return true;
            }
        }
        if let Some((punct, next)) = cursor.punct()
            && punct.as_char() == '$'
            && let Some((body, _, _, _)) = next.any_group()
            && emits_file_module(body)
        {
            return true;
        }
        let Some((_, next)) = cursor.token_tree() else {
            break;
        };
        cursor = next;
    }
    false
}

fn macro_emits_file_module(mac: &syn::Macro) -> bool {
    let buffer = syn::buffer::TokenBuffer::new2(mac.tokens.clone());
    let mut cursor = buffer.begin();
    while !cursor.eof() {
        if let Some((equals, next)) = cursor.punct()
            && equals.as_char() == '='
            && let Some((arrow, next)) = next.punct()
            && arrow.as_char() == '>'
            && let Some((body, _, _, _)) = next.any_group()
            && emits_file_module(body)
        {
            return true;
        }
        let Some((_, next)) = cursor.token_tree() else {
            break;
        };
        cursor = next;
    }
    false
}

struct Extractor {
    sources: Names,
    production_sources: Names,
    imports: Names,
    test_imports: Names,
    has_tests: bool,
    active: Names,
    scopes: Vec<Scope>,
    module_macros: Vec<Names>,
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
        let testing = testing || test_only(&ast.attrs);
        if !testing {
            self.production_sources
                .insert(file.to_string_lossy().into_owned());
        }
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
        self.module_macros
            .push(self.module_macros.last().cloned().unwrap_or_default());
        let mut local = locals(items);
        let mut quote_macros = Names::new();
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
                        local.extend(scope.names.iter().cloned());
                        quote_macros.extend(scope.quote_macros.iter().cloned());
                    }
                }
            }
        }
        scope_quote_macros(items, &mut quote_macros);
        self.scopes.push(Scope {
            names: local.clone(),
            quote_macros: quote_macros.clone(),
        });
        let macros = local_macros(items);
        let modules: Names = items
            .iter()
            .filter_map(|item| match item {
                Item::Mod(module) => Some(ident(&module.ident)),
                _ => None,
            })
            .collect();
        for item in items {
            if let Item::Macro(mac) = item {
                if let Some(name) = &mac.ident {
                    let module_macros = self.module_macros.last_mut().unwrap();
                    module_macros.remove(&ident(name));
                    if mac.mac.path.is_ident("macro_rules") && macro_emits_file_module(&mac.mac) {
                        module_macros.insert(ident(name));
                    }
                }
                if mac.ident.is_none()
                    && mac.mac.path.get_ident().is_some_and(|name| {
                        self.module_macros.last().unwrap().contains(&ident(name))
                    })
                {
                    return Err(format!(
                        "macro {} declares file modules under {}; macro expansion is unavailable, retain explicit srcs and deps with a rule-level # keep",
                        mac.mac.path.get_ident().unwrap(),
                        module_dir.display()
                    ));
                }
            }
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
                        if let syn::Meta::NameValue(v) = &a.meta
                            && let syn::Expr::Lit(l) = &v.value
                            && let syn::Lit::Str(s) = &l.lit
                        {
                            return Some(s.value());
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
                    macros: macros.clone(),
                    quote_macros: quote_macros.clone(),
                    imports: &mut self.imports,
                    tests: &mut self.test_imports,
                    testing,
                    has_tests: &mut self.has_tests,
                };
                visitor.visit_item(item);
            }
        }
        self.scopes.pop();
        self.module_macros.pop();
        Ok(())
    }
}
struct Imports<'a> {
    local: Names,
    modules: Names,
    macros: Names,
    quote_macros: Names,
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
            UseTree::Name(n) => {
                if absolute || !self.macros.contains(&ident(&n.ident)) {
                    self.add(ident(&n.ident), true);
                }
            }
            UseTree::Rename(n) => {
                let name = ident(&n.ident);
                // A re-export can rename a previously declared or imported local item.
                if absolute
                    || (!self.macros.contains(&name)
                        && (!self.local.contains(&name) || own_bindings.contains(&name)))
                {
                    self.add(name, true);
                }
            }
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
        // Quoted Rust is output syntax, not code evaluated by this crate.
        let path: Vec<String> = mac.path.segments.iter().map(|s| ident(&s.ident)).collect();
        if is_quote_path(&path)
            || (mac.path.leading_colon.is_none()
                && path.len() == 1
                && self.quote_macros.contains(&path[0]))
        {
            return;
        }
        // Common Rust macros (assert_eq!, println!, vec!, etc.) take expression
        // lists. Inspect those expressions without expanding the macro.
        if let Ok(args) = syn::punctuated::Punctuated::<syn::Expr, syn::Token![,]>::parse_terminated
            .parse2(mac.tokens.clone())
        {
            for arg in &args {
                let mut nested = Imports {
                    local: self.local.clone(),
                    modules: self.modules.clone(),
                    macros: self.macros.clone(),
                    quote_macros: self.quote_macros.clone(),
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
        if attr.path().is_ident("derive")
            && let Ok(paths) = attr.parse_args_with(
                syn::punctuated::Punctuated::<syn::Path, syn::Token![,]>::parse_terminated,
            )
        {
            for path in &paths {
                if path.segments.len() > 1 {
                    self.add(ident(&path.segments[0].ident), false);
                }
            }
        }
        visit::visit_attribute(self, attr);
    }
    fn visit_block(&mut self, block: &'ast syn::Block) {
        let previous = self.local.clone();
        let previous_macros = self.macros.clone();
        let previous_quotes = self.quote_macros.clone();
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
        self.macros.extend(local_macros(&items));
        scope_quote_macros(&items, &mut self.quote_macros);
        visit::visit_block(self, block);
        self.local = previous;
        self.macros = previous_macros;
        self.quote_macros = previous_quotes;
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
        production_sources: Names::new(),
        imports: Names::new(),
        test_imports: Names::new(),
        has_tests: false,
        active: Names::new(),
        scopes: Vec::new(),
        module_macros: Vec::new(),
    };
    extractor.file(
        &root,
        root.parent().ok_or("crate root has no parent")?,
        false,
    )?;
    Ok(pb::CrateResult {
        test_sources: extractor
            .sources
            .difference(&extractor.production_sources)
            .cloned()
            .collect(),
        sources: extractor.sources.into_iter().collect(),
        imports: extractor.imports.into_iter().collect(),
        test_imports: extractor.test_imports.into_iter().collect(),
        has_tests: extractor.has_tests,
    })
}

#[cfg(test)]
#[path = "rust.test.rs"]
mod tests;
