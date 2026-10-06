use anyhow::Result;

fn main() -> Result<()> {
    let user = service::load_user(r#"{"id":7,"name":" Ada "}"#)?;
    println!("{}", presentation::render(&user));
    Ok(())
}
