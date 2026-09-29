fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "session_info",
            "enroll",
            "unlock",
            "lock_session",
            "forget_device",
            "read_api",
            "mutate",
            "retry_pending",
            "pending_info",
            "import_compose",
            "setup_inspect",
            "setup_install",
            "setup_resume",
            "setup_cancel",
        ]),
    ))
    .expect("Tauri build failed");
}
