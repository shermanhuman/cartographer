// A crate at the repository root: its ids start with ./ (no leading dot
// pair), like the Go root package's.
pub fn top() {}

pub mod a {
    pub fn call_a(x: i32) -> i32 {
        super::top();
        x
    }
}
