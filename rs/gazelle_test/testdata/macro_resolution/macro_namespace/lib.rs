macro_rules! external { () => {}; }
pub(crate) use external;
use external::Value;
pub fn run() { external::run(); }
