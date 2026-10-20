use common::Client;
#[cfg(target_os = "windows")] mod windows;
#[cfg(target_os = "macos")] mod apple { use platform_api::Client; }
#[cfg(not(target_os = "windows"))] fn portable() { nonwindows::run(); }
#[cfg(any(target_os = "windows", target_os = "macos"))]
fn shared_platform() { platform_api::run(); }
#[cfg(target_os = "windows")] fn also_common() { common::run(); }
