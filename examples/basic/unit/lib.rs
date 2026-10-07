fn private_value() -> u32 { 42 }

pub fn value() -> u32 { private_value() }

#[cfg(test)]
#[path = "first.test.rs"]
mod first;

#[cfg(all(test, not(feature = "disabled")))]
#[path = "second.test.rs"]
mod second;
