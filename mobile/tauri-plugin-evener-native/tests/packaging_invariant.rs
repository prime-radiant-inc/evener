//! Packaging invariant: the SwiftPM static library product name must equal the
//! Cargo package/links name so the Xcode build can find the native static library.
//!
//! See: `cargo build` emits a static archive named after the `links` field; the
//! SwiftPM `.library(name:)` product name must match so the linker resolves it.
//! The Swift target/module name may remain `EvenerNativePlugin`.

use std::fs;

const PACKAGE_SWIFT: &str = include_str!("../ios/Package.swift");
const CARGO_TOML: &str = include_str!("../Cargo.toml");

/// Extract the Cargo `links` name from Cargo.toml.
fn cargo_links() -> String {
    for line in CARGO_TOML.lines() {
        let trimmed = line.trim();
        if let Some(rest) = trimmed.strip_prefix("links") {
            let rest = rest.trim_start();
            if let Some(rest) = rest.strip_prefix('=') {
                let val = rest.trim().trim_matches('"');
                return val.to_owned();
            }
        }
    }
    panic!("Cargo.toml has no `links` field");
}

/// Extract the Cargo package name from Cargo.toml.
fn cargo_package_name() -> String {
    for line in CARGO_TOML.lines() {
        let trimmed = line.trim();
        if let Some(rest) = trimmed.strip_prefix("name") {
            let rest = rest.trim_start();
            if let Some(rest) = rest.strip_prefix('=') {
                let val = rest.trim().trim_matches('"');
                return val.to_owned();
            }
        }
    }
    panic!("Cargo.toml has no package `name` field");
}

/// Extract the SwiftPM `.library(name: "...")` product name from Package.swift.
fn swiftpm_library_product_name() -> String {
    let mut in_products = false;
    let mut in_library = false;
    for line in PACKAGE_SWIFT.lines() {
        let trimmed = line.trim();
        if trimmed.contains("products:") {
            in_products = true;
            continue;
        }
        if in_products {
            if trimmed.contains(".library(") {
                in_library = true;
            }
            if in_library {
                if let Some(idx) = trimmed.find("name:") {
                    let after = trimmed[idx + "name:".len()..].trim();
                    if let Some(rest) = after.strip_prefix('"') {
                        if let Some(end) = rest.find('"') {
                            return rest[..end].to_owned();
                        }
                    }
                }
            }
            if trimmed == "]" || trimmed.starts_with("],") {
                in_products = false;
                in_library = false;
            }
        }
    }
    panic!("Package.swift has no .library product name");
}

/// Extract the Swift target name (the first `.target(name: "...")` block).
/// The `name:` may be on the same line as `.target(` or on the next line.
fn swiftpm_target_name() -> String {
    let lines: Vec<&str> = PACKAGE_SWIFT.lines().collect();
    let mut in_targets = false;
    let mut i = 0;
    while i < lines.len() {
        let trimmed = lines[i].trim();
        if trimmed.contains("targets:") {
            in_targets = true;
            i += 1;
            continue;
        }
        if in_targets && trimmed.contains(".target(") {
            // Search this line and the next few lines for name: "..."
            let end = std::cmp::min(i + 5, lines.len());
            for (j, s) in lines.iter().enumerate().take(end).skip(i) {
                let s = s.trim();
                if let Some(idx) = s.find("name:") {
                    let after = s[idx + "name:".len()..].trim();
                    if let Some(rest) = after.strip_prefix('"') {
                        if let Some(end) = rest.find('"') {
                            return rest[..end].to_owned();
                        }
                    }
                }
                // Stop if we hit a closing paren or the next .testTarget/.target
                if j > i
                    && (s.contains(')') || s.contains(".target(") || s.contains(".testTarget("))
                {
                    break;
                }
            }
        }
        i += 1;
    }
    panic!("Package.swift has no .target name");
}

#[test]
fn swiftpm_static_library_product_name_equals_cargo_links() {
    let links = cargo_links();
    let pkg = cargo_package_name();
    // Sanity: Cargo package name equals links name (standard for Tauri plugins)
    assert_eq!(
        pkg, links,
        "Cargo package name and links name should match for Tauri plugins"
    );

    let product_name = swiftpm_library_product_name();
    assert_eq!(
        product_name, links,
        "SwiftPM static library product name `{product_name}` must equal Cargo links name `{links}` \
         so the Xcode linker can find the native static library. \
         The Swift target/module name may differ."
    );
}

#[test]
fn swiftpm_target_name_may_differ_from_product_name() {
    // The Swift module/target name can remain EvenerNativePlugin for ergonomics;
    // only the product name must match Cargo links.
    let target = swiftpm_target_name();
    let product = swiftpm_library_product_name();
    assert!(!target.is_empty(), "Swift target name must be non-empty");
    let links = cargo_links();
    assert_eq!(product, links, "Product name must match Cargo links name");
}

// Keep fs import alive for potential future reuse.
#[allow(dead_code)]
fn _read_file(_p: &str) -> std::io::Result<String> {
    fs::read_to_string(_p)
}
