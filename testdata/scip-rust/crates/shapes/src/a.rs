//! A trait named like b's: calls through it dispatch only to its own impls.

pub trait Service {
    fn run(&self) -> u8;
}

pub struct A;

impl Service for A {
    fn run(&self) -> u8 {
        1
    }
}

/// b's Service, implemented in this file beside a's: still b's.
pub struct A2;

impl crate::b::Service for A2 {
    fn run(&self) -> u8 {
        3
    }
}

pub fn call_a(s: &dyn Service) -> u8 {
    s.run()
}
