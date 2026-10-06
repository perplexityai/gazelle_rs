#[test]
fn parses_normalizes_and_renders_across_internal_crates() -> anyhow::Result<()> {
    let input = serde_json::json!({"id": 7, "name": " Ada "}).to_string();
    let user = service::load_user(&input)?;
    assert_eq!(presentation::render(&user), "Ada (#7)");
    Ok(())
}
