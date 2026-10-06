pub fn parse(input: &str) -> Result<Vec<u64>, serde_json::Error> {
    serde_json::from_str(input)
}
