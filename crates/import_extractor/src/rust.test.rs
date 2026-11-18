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
