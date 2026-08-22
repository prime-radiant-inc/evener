const COMMANDS: &[&str] = &["ping", "scan_and_preview_pairing"];

fn main() {
    tauri_plugin::Builder::new(COMMANDS)
        .android_path("android")
        .ios_path("ios")
        .build();
}
