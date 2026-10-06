use serde_json::Value;

pub struct User {
    pub id: u64,
    pub name: String,
}

pub fn parse_user(input: &str) -> Result<User, serde_json::Error> {
    let value: Value = serde_json::from_str(input)?;
    Ok(User {
        id: value["id"].as_u64().unwrap_or_default(),
        name: value["name"].as_str().unwrap_or_default().to_owned(),
    })
}
