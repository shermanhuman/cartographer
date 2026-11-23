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

pub mod a;
pub mod b;

/// a's Service over any b's Service: the generic bound names b's trait
/// before the header names a's. Still a's impl; self.0.run() is b's.
pub struct Wrap<T>(pub T);

impl<T: b::Service> a::Service for Wrap<T> {
    fn run(&self) -> u8 {
        self.0.run()
    }
}
