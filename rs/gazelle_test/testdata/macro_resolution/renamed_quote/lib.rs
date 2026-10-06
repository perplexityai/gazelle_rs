use quote::{quote as emit, quote_spanned as emit_spanned};
use syn::{parse_quote as parse, parse_quote_spanned as parse_spanned};
pub fn generate() {
    let _ = emit!(output_only::Value);
    let _ = parse!(another_output::Value);
    let _ = emit_spanned!(span => output_only::Value);
    let _ = parse_spanned!(span => another_output::Value);
    { use quote::quote as inner; let _ = inner!(inner_output::Value); }
    actual::run();
}
