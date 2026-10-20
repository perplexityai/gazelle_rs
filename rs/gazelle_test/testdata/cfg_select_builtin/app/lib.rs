#[cfg(target_os = "windows")] #[path = "shared.rs"] mod windows;
#[cfg(target_os = "linux")] #[path = "shared.rs"] mod linux;
#[cfg(target_os = "macos")] fn apple() { apple_api::run(); }
