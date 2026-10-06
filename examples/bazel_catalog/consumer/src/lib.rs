#[wire_derive::identity]
pub fn selected_version() -> u32 {
    renamed::VERSION
}

#[cfg(test)]
mod tests {
    #[test]
    fn preserves_the_build_selected_version() {
        assert_eq!(super::selected_version(), 1);
    }
}
