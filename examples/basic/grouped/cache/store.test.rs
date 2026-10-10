pub(crate) fn value() -> u32 { api::answer() + 1 }

#[test]
fn cache_store_uses_public_api() { assert_eq!(value(), 43); }
