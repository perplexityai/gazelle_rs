fn main() -> anyhow::Result<()> {
    println!("total={}", report_engine::total("[10,20,12]")?);
    Ok(())
}
