mod shared;
#[cfg(any(test, feature = "optional"))]
mod optional;
#[cfg(test)]
#[path = "shared.rs"]
mod shared_in_tests;
#[cfg(test)]
#[path = "checks.test.rs"]
mod checks;
