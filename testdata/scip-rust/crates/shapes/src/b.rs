//! A trait named like a's.

pub trait Service {
    fn run(&self) -> u8;
}

pub struct B;

impl Service for B {
    fn run(&self) -> u8 {
        2
    }
}

pub fn call_b(s: &dyn Service) -> u8 {
    s.run()
}
