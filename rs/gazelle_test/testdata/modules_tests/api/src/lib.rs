mod detail; pub fn answer() -> u32 { detail::answer() }
#[cfg(test)] mod tests { #[test] fn check() { assert_eq!(super::answer(), 42); } }
