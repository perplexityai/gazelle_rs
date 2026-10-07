use super::private_value;
#[test]
fn private_value_is_available() { assert_eq!(private_value(), 42); }
