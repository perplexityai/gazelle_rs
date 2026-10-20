#[cfg(all(target_os = "windows", feature = "desktop"))] fn run() { platform_api::run(); }
#[cfg(not(feature = "desktop"))] fn other() { common::run(); }
