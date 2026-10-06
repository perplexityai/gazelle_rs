use unrelated::quote;
pub fn run() {
    unrelated::quote!(qualified_dep::value());
    quote!(imported_dep::value());
    unrelated::parse_quote!(parse_dep::value());
    unrelated::quote_spanned!(spanned_dep::value());
    unrelated::parse_quote_spanned!(parse_spanned_dep::value());
}
