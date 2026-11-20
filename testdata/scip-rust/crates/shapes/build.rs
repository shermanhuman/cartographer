// Sets cfg(from_build): `built_by_script` exists only when build scripts run.
fn main() {
    println!("cargo::rustc-check-cfg=cfg(from_build)");
    println!("cargo::rustc-cfg=from_build");
}
