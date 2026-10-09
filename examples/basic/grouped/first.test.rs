pub(crate) fn answer() -> u32 { api::answer() }

#[test]
fn uses_public_dependency() { assert_eq!(answer(), 42); }
