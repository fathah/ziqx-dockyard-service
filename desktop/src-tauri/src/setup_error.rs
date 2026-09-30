//! Translate bounded installer signals into advice. Never display remote stderr.
use serde::Serialize;
use serde_json::Value;
use ssh2::FileStat;

pub const DIRECTORIES: &[&str] = &[
    "/docker",
    "/etc/caddy/dockyard",
    "/etc/dockyard/tls",
    "/etc/dockyard/docker",
    "/var/lib/dockyard",
    "/usr/local/bin",
    "/etc/systemd/system",
    "/etc/ssh/sshd_config.d",
    "/var/lib/dockyard-desktop-setup",
    "/etc/apt/keyrings",
    "/etc/apt/sources.list.d",
    "/usr/share/keyrings",
    "/etc/systemd/system/caddy.service.d",
];

fn safe_stage_path(path: &str) -> bool {
    let Some(tail) = path.strip_prefix("/var/lib/dockyard-desktop-setup/") else {
        return false;
    };
    let (id, file) = tail.split_once('/').unwrap_or((tail, ""));
    id.len() == 32
        && id
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
        && matches!(
            file,
            "" | "server.crt"
                | "server.key"
                | "control-ca.crt"
                | "desktop.key"
                | "fingerprint.key"
                | "config.json"
                | "server.json"
                | "ssh.pub"
                | "dockyard.service"
                | "install.py"
                | "dockyard"
                | "dockyardctl"
                | "manifest.json"
                | "Caddyfile.before"
                | "ssh-user-created"
        )
}

fn safe_path(path: &str) -> bool {
    DIRECTORIES.iter().any(|p| {
        std::path::Path::new(p)
            .ancestors()
            .any(|a| a == std::path::Path::new(path))
    }) || safe_stage_path(path)
        || matches!(
            path,
            "/etc/caddy/Caddyfile"
                | "/etc/dockyard/config.json"
                | "/etc/dockyard/server.json"
                | "/etc/dockyard/.desktop-install-id"
                | "/etc/dockyard/desktop-01.key"
                | "/etc/dockyard/fingerprint.key"
                | "/etc/dockyard/tls/server.key"
                | "/etc/dockyard/tls/server.crt"
                | "/etc/dockyard/tls/control-ca.crt"
                | "/usr/local/bin/dockyard"
                | "/usr/local/bin/dockyardctl"
                | "/etc/systemd/system/dockyard.service"
                | "/etc/systemd/system/caddy.service.d/dockyard.conf"
                | "/etc/ssh/sshd_config.d/00-dockyard-link.conf"
                | "/etc/apt/keyrings/dockyard-docker.asc"
                | "/etc/apt/sources.list.d/dockyard-docker.sources"
                | "/etc/apt/sources.list.d/caddy-stable.list"
                | "/usr/share/keyrings/caddy-stable-archive-keyring.gpg"
                | "/etc/caddy/.dockyard-desktop.Caddyfile"
                | "/etc/caddy/.dockyard-before.Caddyfile"
                | "/etc/caddy/.dockyard-apply.Caddyfile"
        )
}

#[derive(Serialize)]
struct Advice {
    title: String,
    message: String,
    action: String,
    command: Option<String>,
    phase: Option<String>,
}

fn advice(
    title: &str,
    message: String,
    action: &str,
    command: Option<String>,
    phase: &str,
) -> String {
    let value = Advice {
        title: title.into(),
        message,
        action: action.into(),
        command,
        phase: crate::setup::PHASES.contains(&phase).then(|| phase.into()),
    };
    format!(
        "DOCKYARD_SETUP_ERROR:{}",
        serde_json::to_string(&value).expect("serializable advice")
    )
}

pub fn path_error(code: &str, path: &str, phase: &str) -> String {
    if !safe_path(path) {
        return fallback(phase);
    }
    let (title, reason, action, command) = match code {
        "path_owner" => ("Server folder or file needs root ownership", "belongs to another user. Dockyard needs root ownership to keep its server access secure.", "Run this command in your VPS terminal. It changes only this path’s owner and preserves its group and contents.", Some(format!("sudo chown root {path}"))),
        "path_private" => ("Installation file needs private permissions", "can be read or accessed by other users. Setup files may contain private credentials.", "Run this command in your VPS terminal to remove other users’ access to this file, then resume this saved installation.", Some(format!("sudo chmod go-rwx {path}"))),
        "path_writable" => ("Server folder or file allows unsafe changes", "can be changed by users other than root.", "Run this command in your VPS terminal to remove group and public write access. It preserves read access and contents.", Some(format!("sudo chmod go-w {path}"))),
        "path_symlink" => ("Setup found a symbolic link", "is a symbolic link. Installation needs a real folder or file at this location.", "Ask your server administrator to review this link and prepare the real path. Keep existing services and data in place.", None),
        "path_type" => ("Setup found the wrong kind of file", "is not the folder or regular file required here.", "Ask your server administrator to review this path before retrying. Keep existing services and data in place.", None),
        "file_missing" => ("Caddy’s configuration file is missing", "could not be found, although Caddy is installed.", "Ask your server administrator to restore Caddy’s configuration file, then check the server again.", None),
        "file_large" => ("Caddy’s configuration file is too large", "exceeds the installer’s 1 MiB limit.", "Ask your server administrator to review the Caddy configuration and its imports before retrying.", None),
        "file_conflict" => ("An existing installation file is different", "does not match this saved installation.", "Ask your server administrator to review the existing file. Do not delete credentials or start a different setup; resume this saved installation after resolving the conflict.", None),
        _ => return fallback(phase),
    };
    advice(title, format!("{path} {reason}"), action, command, phase)
}

pub fn check_stat(path: &str, stat: &FileStat, directory: bool) -> Result<(), String> {
    let mode = stat.perm.ok_or_else(|| fallback(""))?;
    let code = if mode & 0o170000 == 0o120000 {
        Some("path_symlink")
    } else if stat.uid != Some(0) {
        Some("path_owner")
    } else if mode & 0o022 != 0 {
        Some("path_writable")
    } else if (directory && !stat.is_dir()) || (!directory && !stat.is_file()) {
        Some("path_type")
    } else {
        None
    };
    match code {
        Some(code) => Err(path_error(code, path, "")),
        None => Ok(()),
    }
}

pub fn fallback(phase: &str) -> String {
    if phase.is_empty() {
        return advice("Server checks need attention", "Dockyard could not verify the required server folders and files.".into(), "Ask your server administrator to check filesystem ownership, permissions and SSH file access, then try again.", None, phase);
    }
    advice("Server setup needs attention", "Dockyard could not finish this installation step.".into(),
        "Your setup receipt and credentials are saved. Ask your server administrator to review the private error.txt file in /var/lib/dockyard-desktop-setup, fix the problem, then resume this same installation.", None, phase)
}

pub fn installer_line(line: &str, phase: &str) -> Option<String> {
    if line.len() > 4096 {
        return None;
    }
    if let Some(payload) = line.strip_prefix("DOCKYARD_ERROR:") {
        let v: Value = serde_json::from_str(payload).ok()?;
        let code = v.get("code")?.as_str()?;
        let path = v.get("path").and_then(Value::as_str).unwrap_or("");
        return Some(match code {
            "path_owner" | "path_writable" | "path_symlink" | "path_type" | "file_conflict" => path_error(code, path, phase),
            "command_failed" | "command_timeout" => command_error(v.get("tool").and_then(Value::as_str).unwrap_or(""), code == "command_timeout", phase),
            "disk_full" => advice("The VPS has run out of disk space", "Setup could not save an installation file.".into(), "Free disk space on the VPS without deleting project data, then resume this saved installation.", Some("df -h".into()), phase),
            "permission_denied" => advice("The VPS refused an installation operation", "Root access could not write a required file.".into(), "Ask your server administrator to check filesystem permissions, read-only mounts and security policies, then resume this saved installation.", None, phase),
            "missing_account" => advice("Caddy’s service account is missing", "Setup needs the caddy user and group to protect its private connection.".into(), "Ask your server administrator to repair Caddy’s service installation, then resume this saved installation.", None, phase),
            "legacy" => legacy(v.get("reason").and_then(Value::as_str).unwrap_or(""), phase),
            _ => fallback(phase),
        });
    }
    line.strip_prefix("DOCKYARD_FAILED:")
        .map(|reason| legacy(reason, phase))
}

fn command_error(tool: &str, timeout: bool, phase: &str) -> String {
    let (name, action, command) = match tool {
        "apt-get" | "curl" | "gpg" => ("Dependency installation", "Check the VPS internet connection, Ubuntu package repositories and package-manager locks, then resume this saved installation.", None),
        "docker" => ("Docker", "Check that Docker is running and Docker Compose 2.30 or newer is installed, then resume this saved installation.", Some("sudo systemctl status docker --no-pager".into())),
        "caddy" => ("Caddy configuration or reload", "Ask your server administrator to review Caddy’s configuration and service log, then resume this saved installation.", Some("sudo journalctl -u caddy -n 50 --no-pager".into())),
        "sshd" | "useradd" | "usermod" => ("Restricted SSH connection", "Ask your server administrator to review the SSH configuration and dockyard-link account, then resume this saved installation.", None),
        "systemctl" | "dockyard" => ("Server service setup", "Ask your server administrator to review the service log and configuration, then resume this saved installation.", Some("sudo journalctl -u dockyard -u caddy -u docker -n 50 --no-pager".into())),
        _ => return fallback(phase),
    };
    advice(
        &format!(
            "{name} {}",
            if timeout { "took too long" } else { "failed" }
        ),
        format!("The server could not complete {name}."),
        action,
        command,
        phase,
    )
}

fn legacy(reason: &str, phase: &str) -> String {
    if let Some(tool) = reason.strip_prefix("command failed: ") {
        return command_error(tool, false, phase);
    }
    let (title, message, action) = match reason {
        "existing Docker Compose needs an operator upgrade to 2.30 or newer" => ("Docker Compose needs an upgrade", "This server has an older or unsupported Docker Compose version.", "Upgrade Docker Compose to version 2.30 or newer on the VPS, then resume this saved installation."),
        "existing container runtime requires manual Docker installation" => ("Docker needs manual preparation", "Setup found an existing container runtime and cannot safely install Docker automatically.", "Ask your server administrator to install a compatible Docker Engine and Compose without disturbing existing services, then resume."),
        "Docker signing key mismatch" => ("Docker download could not be trusted", "The downloaded repository key did not match Docker’s expected signing key.", "Ask your server administrator to check the repository and network connection. Resume only after the cause is resolved."),
        "Caddy routes would change; manual preparation required" | "live Caddy routes differ from disk; manual reconciliation required" | "Caddyfile changed during setup" | "Caddy reload outcome is uncertain; preserve existing containers and reconcile manually" => ("Caddy configuration needs review", "Caddy’s saved configuration and running routes could not be safely reconciled.", "Ask your server administrator to reconcile the Caddyfile and live configuration while preserving existing routes, then resume this same installation."),
        "custom Caddy admin directive requires manual preparation" | "custom Caddy admin configuration requires manual preparation" | "custom Caddy admin endpoint requires manual setup" | "cannot verify live Caddy configuration" | "Caddy admin socket unavailable" | "invalid live Caddy configuration" | "invalid admin directive" | "invalid Caddy global options" | "unterminated Caddy string" | "unsafe Caddy runtime directory" | "unsafe Caddy runtime owner" => ("Caddy’s private connection needs preparation", "Setup could not safely configure or verify Caddy’s private admin connection.", "Ask your server administrator to review Caddy’s configuration and service, then resume this saved installation."),
        "custom SSH configuration prevents a restricted connector; manual preparation required" | "existing dockyard-link account requires manual review" | "invalid connector account" | "unsafe SSH home" => ("SSH configuration needs review", "Setup could not create the restricted API connection safely.", "Ask your server administrator to review the dockyard-link account and effective SSH policy, then resume this saved installation."),
        "Dockyard did not remain active" => ("Dockyard could not stay running", "The server service stopped after installation.", "Ask your server administrator to review Dockyard’s service log and configuration, then resume this saved installation."),
        "staged file checksum mismatch" | "invalid manifest" | "invalid staged filename" | "invalid connector public key" => ("Installation files could not be verified", "The staged files did not pass integrity checks.", "Keep the saved receipt. Ask your server administrator to review the staged installation files before resuming."),
        "Caddyfile too large" => return path_error("file_large", "/etc/caddy/Caddyfile", phase),
        _ => return fallback(phase),
    };
    advice(title, message.into(), action, None, phase)
}

#[cfg(test)]
mod tests {
    use super::*;
    fn stat(uid: u32, mode: u32) -> FileStat {
        FileStat {
            size: Some(3035),
            uid: Some(uid),
            gid: Some(1000),
            perm: Some(mode),
            atime: None,
            mtime: None,
        }
    }
    #[test]
    fn parent_owner_is_actionable_even_when_caddyfile_is_safe() {
        assert!(check_stat("/etc/caddy/Caddyfile", &stat(0, 0o100644), false).is_ok());
        let error = check_stat("/etc/caddy", &stat(1000, 0o040755), true).unwrap_err();
        assert!(error.contains("sudo chown root /etc/caddy"));
        assert!(!error.contains("-R"));
    }
    #[test]
    fn symlinks_and_writable_directories_have_distinct_advice() {
        assert!(check_stat("/docker", &stat(0, 0o040775), true)
            .unwrap_err()
            .contains("sudo chmod go-w /docker"));
        let error = check_stat("/docker", &stat(0, 0o120777), true).unwrap_err();
        assert!(error.contains("symbolic link"));
        assert!(error.contains("\"command\":null"));
    }
    #[test]
    fn staged_paths_must_have_fixed_names_and_valid_receipt_ids() {
        let path = "/var/lib/dockyard-desktop-setup/0123456789abcdef0123456789abcdef/server.key";
        assert!(path_error("path_private", path, "verify").contains("sudo chmod go-rwx"));
        for path in [
            "/var/lib/dockyard-desktop-setup/../server.key",
            "/var/lib/dockyard-desktop-setup/0123456789abcdef0123456789abcdef/server.key;id",
            "/docker/customer-data",
        ] {
            assert!(path_error("path_owner", path, "verify").contains("\"command\":null"));
        }
    }
    #[test]
    fn new_and_saved_installers_have_readable_errors_without_remote_text() {
        assert!(installer_line(
            r#"DOCKYARD_ERROR:{"code":"path_owner","path":"/etc/caddy"}"#,
            "credentials"
        )
        .unwrap()
        .contains("sudo chown root /etc/caddy"));
        assert!(
            installer_line("DOCKYARD_FAILED:command failed: caddy", "caddy")
                .unwrap()
                .contains("Caddy configuration or reload failed")
        );
        for line in [
            r#"DOCKYARD_ERROR:{"code":"path_owner","path":"/etc/caddy; secret"}"#,
            "DOCKYARD_FAILED:secret",
            r#"DOCKYARD_ERROR:{"code":"command_failed","tool":"secret"}"#,
        ] {
            assert!(!installer_line(line, "credentials")
                .unwrap()
                .contains("secret"));
        }
        assert!(installer_line("DOCKYARD_ERROR:not-json-secret", "").is_none());
    }
}
