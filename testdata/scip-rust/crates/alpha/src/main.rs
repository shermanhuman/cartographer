use shapes::Shape;
use shapes::geo::{self, Circle, Square};

fn main() {
    let c = Circle::new(2.0);
    let s = Square { side: 1.0 };
    let all: Vec<Box<dyn Shape>> = vec![Box::new(c), Box::new(s)];
    println!("{}", geo::total(&all));
    let a = Circle::from(1.5_f64);
    let b: Circle = 3u32.into();
    // A function value with a turbofish is a ref, not a call.
    let f = geo::area_of::<Circle>;
    println!("{} {}", f(&a), geo::area_of::<Circle>(&b));
}
