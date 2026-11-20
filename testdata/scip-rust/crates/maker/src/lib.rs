use proc_macro::TokenStream;

/// Expands to `pub fn generated() -> u8`: it resolves only when proc macros run.
#[proc_macro]
pub fn make_fn(_input: TokenStream) -> TokenStream {
    "pub fn generated() -> u8 { 7 }".parse().unwrap()
}
