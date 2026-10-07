pub fn value() -> u32 { 42 }
#[cfg(test)]
#[path = "shared.test.rs"]
mod shared_tests;
#[cfg(test)]
#[path = "manifest.test.rs"]
mod manifest_tests;
