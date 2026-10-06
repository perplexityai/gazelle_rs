use anyhow::{ensure, Context, Result};
use model::User;

pub fn load_user(input: &str) -> Result<User> {
    let mut user = model::parse_user(input).context("invalid user JSON")?;
    user.name = user.name.trim().to_owned();
    ensure!(!user.name.is_empty(), "name must not be empty");
    Ok(user)
}

#[cfg(test)]
mod tests {
    #[test]
    fn rejects_invalid_json_and_blank_names() {
        assert!(super::load_user("not JSON").is_err());
        assert!(super::load_user(r#"{"name":"  "}"#).is_err());
    }
}
