#[wire_derive::identity]
#[test]
fn preserves_the_build_selected_version() {
    assert_eq!(consumer::selected_version(), 1);
    assert_eq!(renamed::VERSION, 1);
}
