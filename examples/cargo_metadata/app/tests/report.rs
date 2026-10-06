#[test]
fn reports_total_through_undeclared_internal_chain() -> anyhow::Result<()> {
    let input = serde_json::json!([10, 20, 12]).to_string();
    assert_eq!(report_engine::total(&input)?, 42);
    Ok(())
}
