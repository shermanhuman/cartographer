use crate::Shape;

pub struct Circle {
    pub r: f64,
}

pub struct Square {
    pub side: f64,
}

impl Circle {
    /// An associated function.
    pub fn new(r: f64) -> Self {
        Circle { r }
    }

    pub fn scale(&mut self, k: f64) {
        self.r *= k;
    }
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        3.0 * self.r * self.r
    }
}

impl Shape for Square {
    fn area(&self) -> f64 {
        self.side * self.side
    }
}

// Two From impls on one type: distinct ids.
impl From<f64> for Circle {
    fn from(r: f64) -> Self {
        Circle::new(r)
    }
}

impl From<u32> for Circle {
    fn from(r: u32) -> Self {
        Circle::new(r as f64)
    }
}

/// A dyn call: dispatches to every Shape impl.
pub fn total(shapes: &[Box<dyn Shape>]) -> f64 {
    shapes.iter().map(|s| s.area()).sum()
}

/// A generic call: dispatches to every Shape impl.
pub fn area_of<S: Shape>(s: &S) -> f64 {
    s.area()
}

/// Concrete calls: precise.
pub fn grow(c: &mut Circle) -> f64 {
    c.scale(2.0);
    c.area()
}

#[cfg(target_os = "linux")]
pub fn linux_only() -> &'static str {
    "linux"
}

#[cfg(target_os = "macos")]
pub fn macos_only() -> &'static str {
    "macos"
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn grows() {
        let mut c = Circle::new(1.0);
        assert!(grow(&mut c) > helper());
    }

    fn helper() -> f64 {
        0.0
    }
}

#[derive(Clone, Default)]
pub struct Tile {
    pub n: u8,
}

/// A call into a derive impl (external) and a #[cfg]-off block (skipped,
/// not unresolved).
pub fn copy_tile(t: &Tile) -> Tile {
    #[cfg(target_os = "macos")]
    {
        return matches!(t.n, 0).then(|| mac_copy(t)).unwrap();
    }
    t.clone()
}
