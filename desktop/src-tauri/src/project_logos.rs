use base64::{engine::general_purpose::STANDARD, Engine};
use sha2::{Digest, Sha256};
use std::{
    fs,
    path::{Path, PathBuf},
};
use tauri::Manager;

fn logo_path(root: &Path, scope: &str, project: &str) -> PathBuf {
    let key = Sha256::digest(format!("{scope}\0{project}").as_bytes());
    root.join(format!("{}.image", hex::encode(key)))
}
fn validate(bytes: &[u8]) -> Result<(), String> {
    if bytes.len() <= 4_000_000
        && bytes.len() >= 24
        && &bytes[..8] == b"\x89PNG\r\n\x1a\n"
        && &bytes[12..16] == b"IHDR"
    {
        let w = u32::from_be_bytes(bytes[16..20].try_into().unwrap());
        let h = u32::from_be_bytes(bytes[20..24].try_into().unwrap());
        if w > 0 && w == h && w <= 900 {
            return Ok(());
        }
        return Err("Logo must be square and at most 900 pixels".into());
    }
    if bytes.len() > 4_000_000
        || bytes.len() < 30
        || &bytes[..4] != b"RIFF"
        || &bytes[8..12] != b"WEBP"
    {
        return Err("Choose a valid WebP logo under 4 MB".into());
    }
    let (w, h) = match &bytes[12..16] {
        b"VP8X" => {
            let size =
                |at: usize| u32::from_le_bytes([bytes[at], bytes[at + 1], bytes[at + 2], 0]) + 1;
            (size(24), size(27))
        }
        b"VP8 " if &bytes[23..26] == b"\x9d\x01\x2a" => (
            u16::from_le_bytes([bytes[26], bytes[27]]) as u32 & 0x3fff,
            u16::from_le_bytes([bytes[28], bytes[29]]) as u32 & 0x3fff,
        ),
        b"VP8L" if bytes[20] == 0x2f => {
            let bits = u32::from_le_bytes([bytes[21], bytes[22], bytes[23], bytes[24]]);
            ((bits & 0x3fff) + 1, ((bits >> 14) & 0x3fff) + 1)
        }
        _ => return Err("Unsupported logo format".into()),
    };
    if w == 0 || w != h || w > 900 {
        return Err("Logo must be square and at most 900 pixels".into());
    }
    Ok(())
}
fn root(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    Ok(app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("project-logos"))
}
#[tauri::command]
pub fn project_logo_read(
    app: tauri::AppHandle,
    scope: String,
    project: String,
) -> Result<Option<String>, String> {
    read_logo(&root(&app)?, &scope, &project)
}
fn read_logo(root: &Path, scope: &str, project: &str) -> Result<Option<String>, String> {
    match fs::read(logo_path(root, scope, project)) {
        Ok(bytes) => {
            validate(&bytes)?;
            let mime = if bytes.starts_with(b"\x89PNG") {
                "png"
            } else {
                "webp"
            };
            Ok(Some(format!(
                "data:image/{mime};base64,{}",
                STANDARD.encode(bytes)
            )))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e.to_string()),
    }
}
#[tauri::command]
pub fn project_logo_save(
    app: tauri::AppHandle,
    scope: String,
    project: String,
    data: Option<String>,
) -> Result<(), String> {
    save_logo(&root(&app)?, &scope, &project, data)
}
fn save_logo(root: &Path, scope: &str, project: &str, data: Option<String>) -> Result<(), String> {
    let path = logo_path(root, scope, project);
    if let Some(data) = data {
        if data.len() > 5_400_000 {
            return Err("Logo is too large".into());
        }
        let bytes = STANDARD
            .decode(
                data.strip_prefix("data:image/webp;base64,")
                    .or_else(|| data.strip_prefix("data:image/png;base64,"))
                    .ok_or("Invalid logo format")?,
            )
            .map_err(|e| e.to_string())?;
        validate(&bytes)?;
        fs::create_dir_all(&root).map_err(|e| e.to_string())?;
        let temp = root.join(format!("{}.tmp", uuid::Uuid::new_v4()));
        fs::write(&temp, bytes).map_err(|e| e.to_string())?;
        if let Err(e) = fs::rename(&temp, &path) {
            let _ = fs::remove_file(&temp);
            return Err(e.to_string());
        }
    } else if let Err(e) = fs::remove_file(path) {
        if e.kind() != std::io::ErrorKind::NotFound {
            return Err(e.to_string());
        }
    }
    Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn main_window_has_logo_command_permissions() {
        let capability: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/main.json")).unwrap();
        let permissions = capability["permissions"].as_array().unwrap();
        assert!(capability["windows"]
            .as_array()
            .unwrap()
            .contains(&serde_json::json!("main")));
        for command in ["project_logo_read", "project_logo_save"] {
            assert!(include_str!("../build.rs").contains(&format!("\"{command}\"")));
            assert!(permissions.contains(&serde_json::json!(format!(
                "allow-{}",
                command.replace('_', "-")
            ))));
        }
    }
    #[test]
    fn keys_are_local_and_server_scoped() {
        let root = Path::new("/logos");
        assert_ne!(
            logo_path(root, "server1", "project"),
            logo_path(root, "server2", "project")
        );
        assert_eq!(logo_path(root, "../", "../../escape").parent(), Some(root));
    }
    #[test]
    fn persists_png_fallback_and_removes_without_touching_other_projects() {
        let root =
            std::env::temp_dir().join(format!("dockyard-logo-test-{}", uuid::Uuid::new_v4()));
        let mut b = vec![0; 24];
        b[..8].copy_from_slice(b"\x89PNG\r\n\x1a\n");
        b[12..16].copy_from_slice(b"IHDR");
        b[16..20].copy_from_slice(&900u32.to_be_bytes());
        b[20..24].copy_from_slice(&900u32.to_be_bytes());
        let data = format!("data:image/png;base64,{}", STANDARD.encode(&b));
        save_logo(&root, "server1", "p", Some(data.clone())).unwrap();
        save_logo(&root, "server1", "q", Some(data.clone())).unwrap();
        assert_eq!(
            read_logo(&root, "server1", "p").unwrap(),
            Some(data.clone())
        );
        assert_eq!(read_logo(&root, "server2", "p").unwrap(), None);
        save_logo(&root, "server1", "p", None).unwrap();
        assert_eq!(read_logo(&root, "server1", "p").unwrap(), None);
        assert_eq!(read_logo(&root, "server1", "q").unwrap(), Some(data));
        fs::remove_dir_all(root).unwrap();
    }
    #[test]
    fn accepts_only_bounded_square_webp() {
        let mut b = vec![0; 30];
        b[..4].copy_from_slice(b"RIFF");
        b[8..16].copy_from_slice(b"WEBPVP8X");
        b[24..27].copy_from_slice(&[0x83, 3, 0]);
        b[27..30].copy_from_slice(&[0x83, 3, 0]);
        assert!(validate(&b).is_ok()); // 900 x 900
        b[27] = 0x84;
        assert!(validate(&b).is_err());
        b[24] = 0x84;
        assert!(validate(&b).is_err());
        assert!(validate(b"not an image").is_err());
    }
}
