fn identity(value: u32) -> u32 { value }
#[test]
fn manifest_test() { assert_eq!(identity(5), 5); }
