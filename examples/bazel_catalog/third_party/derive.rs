use proc_macro::TokenStream;

#[proc_macro_attribute]
pub fn identity(_: TokenStream, item: TokenStream) -> TokenStream {
    item
}
