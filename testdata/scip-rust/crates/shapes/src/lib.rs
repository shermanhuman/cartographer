pub mod geo;

/// Shape is implemented by Circle and Square: calls through dyn or a generic
/// parameter dispatch.
pub trait Shape {
    fn area(&self) -> f64;
    fn label(&self) -> String {
        format!("{:.1}", self.area())
    }
}

#[cfg(from_build)]
pub fn built_by_script() -> bool {
    true
}

maker::make_fn!();
