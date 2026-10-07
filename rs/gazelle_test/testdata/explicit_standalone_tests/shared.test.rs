fn identity(value: u32) -> u32 { value }
#[test]
fn explicit_test() { assert_eq!(identity(3), 3); }
