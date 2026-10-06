use anyhow::{anyhow, Context, Result};

pub fn total(input: &str) -> Result<u64> {
    let values = line_items::parse(input).context("invalid line items")?;
    values.into_iter().try_fold(0_u64, |sum, value| {
        sum.checked_add(value)
            .ok_or_else(|| anyhow!("total overflow"))
    })
}

#[cfg(test)]
mod tests {
    #[test]
    fn rejects_invalid_items_and_overflow() {
        assert!(super::total(r#"["not a number"]"#).is_err());
        assert!(super::total("[18446744073709551615,1]").is_err());
        assert_eq!(super::total("[]").unwrap(), 0);
    }
}
