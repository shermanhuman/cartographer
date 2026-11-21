// An integration test target: its own crate (crates/shapes/tests/api), and
// #[test] marks a function outside any #[cfg(test)] module.
use shapes::geo::Circle;

#[test]
fn api() {
    assert!(setup().r > 0.0);
}

fn setup() -> Circle {
    Circle::new(1.0)
}
