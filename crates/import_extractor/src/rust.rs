//! Parse crate source graphs without invoking Cargo or expanding macros.
use crate::pb;
use std::{
    collections::BTreeSet,
    fs,
    path::{Path, PathBuf},
};
use syn::parse::Parser;
use syn::{
    visit::{self, Visit},
    Attribute, Item, UseTree,
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
fn cfg_meta(meta: &syn::Meta) -> pb::Cfg {
    let mut cfg = pb::Cfg::default();
    match meta {
        syn::Meta::Path(path) => cfg.key = path.get_ident().map(ident).unwrap_or_default(),
        syn::Meta::NameValue(value) => {
            cfg.key = value.path.get_ident().map(ident).unwrap_or_default();
            if let syn::Expr::Lit(lit) = &value.value {
                if let syn::Lit::Str(value) = &lit.lit {
                    cfg.value = value.value();
                }
            }
        }
        syn::Meta::List(list) => {
            if let Ok(args) = list.parse_args_with(
                syn::punctuated::Punctuated::<syn::Meta, syn::Token![,]>::parse_terminated,
            ) {
                cfg.op = list.path.get_ident().map(ident).unwrap_or_default();
                cfg.args = args.iter().map(cfg_meta).collect();
            }
        }
    }
    cfg
}
fn with_cfg(parent: &Option<pb::Cfg>, attrs: &[Attribute]) -> Option<pb::Cfg> {
    let mut args: Vec<_> = parent.iter().cloned().collect();
    for attr in attrs {
        if attr.path().is_ident("cfg") {
            args.push(
                attr.parse_args::<syn::Meta>()
                    .map(|meta| cfg_meta(&meta))
                    .unwrap_or_default(),
            );
        } else if attr.path().is_ident("cfg_attr")
            && attr
                .parse_args_with(
                    syn::punctuated::Punctuated::<syn::Meta, syn::Token![,]>::parse_terminated,
                )
                .is_ok_and(|args| {
                    args.iter()
                        .skip(1)
                        .any(|meta| meta.path().is_ident("cfg") || meta.path().is_ident("cfg_attr"))
                })
        {
            // Expanding conditional attributes requires more than inspecting cfg.
            args.push(pb::Cfg {
                key: "cfg_attr".into(),
                ..Default::default()
            });
        }
    }
    match args.len() {
        0 => None,
        1 => args.pop(),
        _ => Some(pb::Cfg {
            op: "all".into(),
            args,
            ..Default::default()
        }),
    }
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
        if let Some((name, mut next)) = cursor.ident() {
            if name == "mod" {
                if let Some((punct, rest)) = next.punct() {
                    if punct.as_char() == '$' {
                        next = rest;
                    }
                }
                if let Some((_, rest)) = next.ident() {
                    if rest.punct().is_some_and(|(p, _)| p.as_char() == ';') {
                        return true;
                    }
                }
            }
        }
        if let Some((punct, next)) = cursor.punct() {
            if punct.as_char() == '$' {
                if let Some((body, _, _, _)) = next.any_group() {
                    if emits_file_module(body) {
                        return true;
                    }
                }
            }
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
        if let Some((equals, next)) = cursor.punct() {
            if equals.as_char() == '=' {
                if let Some((arrow, next)) = next.punct() {
                    if arrow.as_char() == '>' {
                        if let Some((body, _, _, _)) = next.any_group() {
                            if emits_file_module(body) {
                                return true;
                            }
                        }
                    }
                }
            }
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
    conditional_imports: Vec<pb::ConditionalImport>,
    condition: Option<pb::Cfg>,
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
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, &ast.attrs);
        if !testing {
            self.production_sources
                .insert(file.to_string_lossy().into_owned());
        }
        self.items(&ast.items, module_dir, file.parent().unwrap(), testing)?;
        self.condition = previous;
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
                let previous = self.condition.clone();
                self.condition = with_cfg(&previous, &m.attrs);
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
                self.condition = previous;
            } else {
                let mut visitor = Imports {
                    local: local.clone(),
                    modules: modules.clone(),
                    macros: macros.clone(),
                    quote_macros: quote_macros.clone(),
                    imports: &mut self.imports,
                    tests: &mut self.test_imports,
                    testing,
                    condition: self.condition.clone(),
                    conditional_imports: &mut self.conditional_imports,
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
    condition: Option<pb::Cfg>,
    conditional_imports: &'a mut Vec<pb::ConditionalImport>,
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
        self.conditional_imports.push(pb::ConditionalImport {
            name: name.clone(),
            condition: self.condition.clone(),
            test_only: self.testing,
        });
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
            Item::Mod(x) => &x.attrs,
            Item::Union(x) => &x.attrs,
            Item::TraitAlias(x) => &x.attrs,
            Item::ForeignMod(x) => &x.attrs,
            _ => return visit::visit_item(self, item),
        };
        let previous_condition = self.condition.clone();
        self.condition = with_cfg(&previous_condition, attrs);
        let previous = self.testing;
        self.testing |= test_only(attrs) || attrs.iter().any(|a| a.path().is_ident("test"));
        *self.has_tests |= self.testing;
        visit::visit_item(self, item);
        self.testing = previous;
        self.condition = previous_condition;
    }
    fn visit_expr(&mut self, expr: &'ast syn::Expr) {
        let attrs = match expr {
            syn::Expr::Array(x) => &x.attrs,
            syn::Expr::Assign(x) => &x.attrs,
            syn::Expr::Async(x) => &x.attrs,
            syn::Expr::Await(x) => &x.attrs,
            syn::Expr::Binary(x) => &x.attrs,
            syn::Expr::Block(x) => &x.attrs,
            syn::Expr::Break(x) => &x.attrs,
            syn::Expr::Call(x) => &x.attrs,
            syn::Expr::Cast(x) => &x.attrs,
            syn::Expr::Closure(x) => &x.attrs,
            syn::Expr::Const(x) => &x.attrs,
            syn::Expr::Continue(x) => &x.attrs,
            syn::Expr::Field(x) => &x.attrs,
            syn::Expr::ForLoop(x) => &x.attrs,
            syn::Expr::Group(x) => &x.attrs,
            syn::Expr::If(x) => &x.attrs,
            syn::Expr::Index(x) => &x.attrs,
            syn::Expr::Infer(x) => &x.attrs,
            syn::Expr::Let(x) => &x.attrs,
            syn::Expr::Lit(x) => &x.attrs,
            syn::Expr::Loop(x) => &x.attrs,
            syn::Expr::Macro(x) => &x.attrs,
            syn::Expr::Match(x) => &x.attrs,
            syn::Expr::MethodCall(x) => &x.attrs,
            syn::Expr::Paren(x) => &x.attrs,
            syn::Expr::Path(x) => &x.attrs,
            syn::Expr::Range(x) => &x.attrs,
            syn::Expr::RawAddr(x) => &x.attrs,
            syn::Expr::Reference(x) => &x.attrs,
            syn::Expr::Repeat(x) => &x.attrs,
            syn::Expr::Return(x) => &x.attrs,
            syn::Expr::Struct(x) => &x.attrs,
            syn::Expr::Try(x) => &x.attrs,
            syn::Expr::TryBlock(x) => &x.attrs,
            syn::Expr::Tuple(x) => &x.attrs,
            syn::Expr::Unary(x) => &x.attrs,
            syn::Expr::Unsafe(x) => &x.attrs,
            syn::Expr::While(x) => &x.attrs,
            syn::Expr::Yield(x) => &x.attrs,
            _ => &Vec::new(),
        };
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, attrs);
        visit::visit_expr(self, expr);
        self.condition = previous;
    }
    fn visit_field(&mut self, field: &'ast syn::Field) {
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, &field.attrs);
        visit::visit_field(self, field);
        self.condition = previous;
    }
    fn visit_impl_item(&mut self, item: &'ast syn::ImplItem) {
        let attrs = match item {
            syn::ImplItem::Const(x) => &x.attrs,
            syn::ImplItem::Fn(x) => &x.attrs,
            syn::ImplItem::Type(x) => &x.attrs,
            syn::ImplItem::Macro(x) => &x.attrs,
            _ => &Vec::new(),
        };
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, attrs);
        visit::visit_impl_item(self, item);
        self.condition = previous;
    }
    fn visit_trait_item(&mut self, item: &'ast syn::TraitItem) {
        let attrs = match item {
            syn::TraitItem::Const(x) => &x.attrs,
            syn::TraitItem::Fn(x) => &x.attrs,
            syn::TraitItem::Type(x) => &x.attrs,
            syn::TraitItem::Macro(x) => &x.attrs,
            _ => &Vec::new(),
        };
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, attrs);
        visit::visit_trait_item(self, item);
        self.condition = previous;
    }
    fn visit_local(&mut self, local: &'ast syn::Local) {
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, &local.attrs);
        visit::visit_local(self, local);
        self.condition = previous;
    }
    fn visit_arm(&mut self, arm: &'ast syn::Arm) {
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, &arm.attrs);
        visit::visit_arm(self, arm);
        self.condition = previous;
    }
    fn visit_variant(&mut self, variant: &'ast syn::Variant) {
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, &variant.attrs);
        visit::visit_variant(self, variant);
        self.condition = previous;
    }
    fn visit_foreign_item(&mut self, item: &'ast syn::ForeignItem) {
        let attrs = match item {
            syn::ForeignItem::Fn(x) => &x.attrs,
            syn::ForeignItem::Static(x) => &x.attrs,
            syn::ForeignItem::Type(x) => &x.attrs,
            syn::ForeignItem::Macro(x) => &x.attrs,
            _ => &Vec::new(),
        };
        let previous = self.condition.clone();
        self.condition = with_cfg(&previous, attrs);
        visit::visit_foreign_item(self, item);
        self.condition = previous;
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
                    condition: self.condition.clone(),
                    conditional_imports: self.conditional_imports,
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
        conditional_imports: Vec::new(),
        condition: None,
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
        conditional_imports: extractor.conditional_imports,
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
    fn cfg_guards_follow_file_inline_and_item_scopes() {
        let f = Fixture::new(&[
            (
                "lib.rs",
                r#"
                #[cfg(target_os = "windows")] mod windows;
                #[cfg(target_os = "macos")] mod inline {
                    #[cfg(feature = "desktop")] fn run() { platform_api::run(); }
                }
                fn run() { #[cfg(target_os = "linux")] linux_api::run(); }
                use shared::Client;
                #[cfg(target_os = "windows")] fn other() { shared::run(); }
            "#,
            ),
            (
                "windows.rs",
                "#![cfg(not(target_os = \"linux\"))] use win_api::Client;",
            ),
        ]);
        let result = f.extract().unwrap();
        let imports: Vec<_> = result
            .conditional_imports
            .iter()
            .filter(|i| i.name == "win_api")
            .collect();
        assert_eq!(imports.len(), 1);
        let condition = imports[0].condition.as_ref().unwrap();
        assert_eq!(condition.op, "all");
        assert_eq!(condition.args[0].value, "windows");
        assert_eq!(condition.args[1].op, "not");
        assert_eq!(condition.args[1].args[0].value, "linux");
        let platform = result
            .conditional_imports
            .iter()
            .find(|i| i.name == "platform_api")
            .unwrap();
        let condition = platform.condition.as_ref().unwrap();
        assert_eq!(condition.args[0].value, "macos");
        assert_eq!(condition.args[1].key, "feature");
        let linux = result
            .conditional_imports
            .iter()
            .find(|i| i.name == "linux_api")
            .unwrap();
        assert_eq!(linux.condition.as_ref().unwrap().value, "linux");
        assert!(result
            .conditional_imports
            .iter()
            .any(|i| i.name == "shared" && i.condition.is_none()));
        assert!(result
            .conditional_imports
            .iter()
            .any(|i| i.name == "shared" && i.condition.is_some()));
    }
    #[test]
    fn cfg_guards_apply_to_variant_arm_local_and_foreign_paths() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            enum E { #[cfg(target_os = "windows")] Win(variant_api::Type), Other }
            fn run() {
                #[cfg(target_os = "windows")] let value = local_api::run();
                match 1 { #[cfg(target_os = "windows")] 0 => arm_api::run(), _ => () }
            }
            #[cfg(target_os = "windows")] extern "C" { fn a(value: foreign_mod_api::Type); }
            extern "C" { #[cfg(target_os = "windows")] fn b(value: foreign_item_api::Type); }
        "#,
        )]);
        let result = f.extract().unwrap();
        assert_eq!(
            result.imports,
            [
                "arm_api",
                "foreign_item_api",
                "foreign_mod_api",
                "local_api",
                "variant_api"
            ]
        );
        for import in result.conditional_imports {
            let condition = import.condition.unwrap();
            assert_eq!(condition.key, "target_os", "{}", import.name);
            assert_eq!(condition.value, "windows", "{}", import.name);
        }
    }
    #[test]
    fn cfg_attr_is_conservative_without_guarding_ordinary_derives() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            #[cfg_attr(feature = "extra", derive(Clone))] struct A(plain::Type);
            #[cfg_attr(feature = "extra", cfg(target_os = "windows"))]
            fn run() { conditional::run(); }
        "#,
        )]);
        let result = f.extract().unwrap();
        let plain = result
            .conditional_imports
            .iter()
            .find(|i| i.name == "plain")
            .unwrap();
        assert!(plain.condition.is_none());
        let guarded = result
            .conditional_imports
            .iter()
            .find(|i| i.name == "conditional")
            .unwrap();
        assert_eq!(guarded.condition.as_ref().unwrap().key, "cfg_attr");
    }
    #[test]
    fn rejects_invoked_module_macros_instead_of_returning_partial_sources() {
        for source in [
            "macro_rules! implementation_module { ($name:ident) => { pub mod $name; }; } implementation_module!(auth);",
            "macro_rules! modules { ($($name:ident),*) => { $(mod $name;)* }; } modules!(auth);",
            "macro_rules! modules { () => { mod auth; }; } mod child;",
            "macro_rules! modules { () => { mod auth; }; } mod child { modules!(); }",
        ] {
            let f = Fixture::new(&[
                ("lib.rs", source),
                ("child.rs", "modules!();"),
                ("auth.rs", "use external::Client;"),
            ]);
            let error = f.extract().unwrap_err();
            assert!(error.contains("declares file modules"), "{error}");
            assert!(error.contains("rule-level # keep"), "{error}");
        }
    }

    #[test]
    fn opaque_macros_do_not_infer_modules_from_names_or_quoted_tokens() {
        for source in [
            "macro_rules! implementation_module { ($name:ident) => { fn $name() {} }; } implementation_module!(auth);",
            "macro_rules! modules { () => { mod auth; }; }", // unused definition
            "macro_rules! inspect { (mod $name:ident;) => {}; } inspect!(mod auth;);",
            r#"macro_rules! text { () => { const TEXT: &str = "mod auth;"; }; } text!();"#,
            "macro_rules! quoted { () => { quote::quote! { mod auth; } }; } quoted!();",
            "macro_rules! modules { () => { mod auth; }; } mod child { macro_rules! modules { () => {}; } modules!(); }",
        ] {
            let f = Fixture::new(&[("lib.rs", source), ("auth.rs", "use unrelated::Client;")]);
            let result = f.extract().unwrap();
            assert_eq!(result.sources, vec![f.0.join("lib.rs").to_string_lossy()]);
            assert!(!result.imports.contains(&"unrelated".into()));
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
    fn quoted_paths_are_not_dependencies_of_the_generator() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            fn generate() {
                let _ = quote::quote!(output_only::Value);
                let _ = syn::parse_quote!(another_output::Value);
                assert_eq!(actual::value(), 1);
            }
        "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["actual", "quote", "syn"]);
    }

    #[test]
    fn local_macros_do_not_shadow_external_crate_paths() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            macro_rules! external { () => {}; }
            pub(crate) use external;
            use external::Value;
            fn run() { external::run(); }
            "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["external"]);
    }

    #[test]
    fn renamed_quote_macros_keep_output_paths_opaque() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use quote::{quote as emit, quote_spanned as emit_spanned};
            use syn::{parse_quote as parse, parse_quote_spanned as parse_spanned};
            fn generate() {
                let _ = emit!(output_only::Value);
                let _ = parse!(another_output::Value);
                let _ = emit_spanned!(span => output_only::Value);
                let _ = parse_spanned!(span => another_output::Value);
                { use quote::quote as inner; let _ = inner!(inner_output::Value); }
                assert_eq!(actual::value(), 1);
            }
            "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["actual", "quote", "syn"]);
    }

    #[test]
    fn quote_aliases_follow_scope_and_shadowing() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use quote::quote as emit;
            mod child {
                use super::*;
                fn generate() { let _ = emit!(output_only::Value); }
            }
            fn generate() {
                {
                    use unrelated::quote as emit;
                    emit!(actual::value());
                }
                let _ = emit!(output_only::Value);
            }
            "#,
        )]);
        assert_eq!(
            f.extract().unwrap().imports,
            ["actual", "quote", "unrelated"]
        );
    }

    #[test]
    fn unrelated_quote_macros_still_visit_expression_arguments() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use unrelated::quote;
            fn run() {
                unrelated::quote!(qualified_dep::value());
                quote!(imported_dep::value());
                unrelated::parse_quote!(parse_dep::value());
                unrelated::quote_spanned!(spanned_dep::value());
                unrelated::parse_quote_spanned!(parse_spanned_dep::value());
            }
            "#,
        )]);
        assert_eq!(
            f.extract().unwrap().imports,
            [
                "imported_dep",
                "parse_dep",
                "parse_spanned_dep",
                "qualified_dep",
                "spanned_dep",
                "unrelated",
            ]
        );
    }

    #[test]
    fn local_macro_reexports_are_not_external_crates() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            mod diagnostics {
                macro_rules! report { () => {}; }
                pub(crate) use report;
                pub(crate) use report as renamed_report;
            }
            use external;
            fn run() { diagnostics::report!(); }
            "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["external"]);
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
    fn explicitly_local_generated_type_reexports_are_not_crates() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            macro_rules! call_error {
                ($name:ident) => { pub enum $name { Closed } };
            }
            call_error!(CallError);
            pub use self::CallError as Error;
            use model::*;
            pub use model::PublicType;
            use actual_crate as renamed;
            "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["actual_crate", "model"]);
    }

    #[test]
    fn qualified_enum_variant_imports_after_external_globs_are_not_crates() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use api::*;
            fn run() {
                use api::Backend::{Local, Remote};
                use api::OtherBackend::*;
            }
            "#,
        )]);
        assert_eq!(f.extract().unwrap().imports, ["api"]);
    }

    #[test]
    fn explicitly_rooted_uppercase_crates_remain_dependencies() {
        let f = Fixture::new(&[(
            "lib.rs",
            r#"
            use ::Uppercase::{self, Client};
            use ::Renamed as Alias;
            use ::Bare;
            extern crate Legacy;
            pub use lowercase;
            pub use another as renamed;
            "#,
        )]);
        assert_eq!(
            f.extract().unwrap().imports,
            [
                "Bare",
                "Legacy",
                "Renamed",
                "Uppercase",
                "another",
                "lowercase"
            ]
        );
    }

    #[test]
    fn external_use_roots_preserve_case_and_aliases() {
        for (source, expected) in [
            ("use lower::Client;", "lower"),
            ("use Upper::Client;", "Upper"),
            ("use Upper;", "Upper"),
            ("use Upper as alias;", "Upper"),
            ("use Upper::{self, Client};", "Upper"),
            ("use Upper::*;", "Upper"),
            ("use r#Upper::Client;", "Upper"),
            (
                "extern crate Upper as Äpi; fn f() { use Äpi::Client; }",
                "Upper",
            ),
            ("pub use Upper::Client;", "Upper"),
            ("use ::Upper::Client;", "Upper"),
            ("extern crate Upper as alias;", "Upper"),
            (
                "use external as Upper; fn f() { use Upper::Client; }",
                "external",
            ),
        ] {
            let f = Fixture::new(&[("lib.rs", source)]);
            assert_eq!(f.extract().unwrap().imports, [expected], "{source}");
        }
    }

    #[test]
    fn local_type_aliases_preserve_case() {
        for source in [
            "pub struct Local; pub use Local as Alias;",
            "pub struct lowercase; pub use lowercase as Alias;",
            "pub struct Élément; pub use Élément as Alias;",
            "use api::lowercase; pub use lowercase as Alias;",
            "use api::Upper; pub use Upper as Alias;",
        ] {
            let f = Fixture::new(&[("lib.rs", source)]);
            let expected: Vec<&str> = if source.starts_with("use api") {
                vec!["api"]
            } else {
                vec![]
            };
            assert_eq!(f.extract().unwrap().imports, expected, "{source}");
        }
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
