fn private_value() -> u32 { 42 }
pub fn value() -> u32 { private_value() }
#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
mod detail;
#[cfg(all(test, feature = "extra"))]
#[path = "feature.test.rs"]
mod feature_tests;
#[cfg(test)]
#[path = "src/lib.test.rs"]
mod other_tests;
