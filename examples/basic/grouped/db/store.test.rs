#[test]
fn db_store_uses_both_sibling_modules() {
    assert_eq!(api::answer(), 42);
    assert_eq!(crate::cache__store::value(), 43);
}
