#[wire_derive::identity]
pub fn selected_version() -> u32 {
    renamed::VERSION
}
