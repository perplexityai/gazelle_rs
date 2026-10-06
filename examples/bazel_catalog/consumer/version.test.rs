#[test]
fn selects_the_explicit_version() {
    assert_eq!(wire_codec::VERSION, 2);
}
