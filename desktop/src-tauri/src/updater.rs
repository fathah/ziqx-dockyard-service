use crate::{native, protocol::SshIdentity, setup, terminal};
use serde::{Deserialize, Serialize};
use serde_json::json;
use sha2::{Digest, Sha256};
use ssh2::{OpenFlags, OpenType, Session};
use std::{
    io::{Read, Write},
    path::Path,
};
use tauri::AppHandle;

const MANIFEST: &str = include_str!("../resources/ubuntu/manifest.json");
const SCRIPT: &[u8] = include_bytes!("../../updater/update.py");
const JOB_CHECK: &str = include_str!("../../updater/update_jobs.py");
// The service unit ships with updates so hardening changes reach existing servers.
const UNIT: &[u8] = include_bytes!("../../../deploy/dockyard.service");
const INSPECT: &str = "set -eu; . /etc/os-release; test \"$ID\" = ubuntu; test \"$(uname -m)\" = x86_64; test \"$(id -u)\" = 0; test -f /usr/local/bin/dockyard; test -f /usr/local/bin/dockyardctl; sha256sum /usr/local/bin/dockyard /usr/local/bin/dockyardctl; systemctl is-active dockyard; /usr/local/bin/dockyard -version-json 2>/dev/null || echo legacy";
const ACCESS_CHECK: &str = include_str!("../../updater/access_check.py");
const ACCESS_PREPARE: &str = "set -eu; test \"$(id -u)\" = 0; test \"$(stat -c %u /var/lib)\" = 0; test ! -L /var/lib/dockyard-desktop-updates; if test -e /var/lib/dockyard-desktop-updates; then test -d /var/lib/dockyard-desktop-updates; test \"$(stat -c %u /var/lib/dockyard-desktop-updates)\" = 0; fi; install -d -o root -g root -m 0700 /var/lib/dockyard-desktop-updates";
const BINARY_UPLOAD_MODE: i32 = 0o700;
const DATA_UPLOAD_MODE: i32 = 0o600;

#[derive(Clone, Deserialize)]
struct PathInfo {
    exists: bool,
    kind: String,
    uid: i64,
    mode: u32,
}
#[derive(Clone, Deserialize)]
struct RawAccess {
    root: bool,
    parent: PathInfo,
    stage: PathInfo,
    bin_dir: PathInfo,
    daemon: PathInfo,
    ctl: PathInfo,
    database: PathInfo,
    service_active: bool,
    #[serde(default)]
    checks: Vec<AccessCheck>,
    #[serde(default)]
    recent_jobs: Vec<AccessJob>,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct AccessCheck {
    pub id: String,
    pub label: String,
    pub status: String,
    pub detail: String,
    pub repair: Option<String>,
}
#[derive(Clone, Serialize, Deserialize)]
pub struct AccessJob {
    pub job_id: String,
    pub project_id: String,
    pub action: String,
    pub status: String,
    pub phase: String,
    pub error_code: String,
    pub finished_at: String,
}
#[derive(Clone, Serialize)]
pub struct AccessReport {
    pub root_ssh: bool,
    pub update_directory: String,
    pub installed_binaries: bool,
    pub database: bool,
    pub service_active: bool,
    pub checks: Vec<AccessCheck>,
    pub recent_jobs: Vec<AccessJob>,
}
fn secure_directory(p: &PathInfo) -> bool {
    p.exists
        && p.kind == "directory"
        && p.uid == 0
        && p.mode & 0o022 == 0
        && p.mode & 0o700 == 0o700
}
fn secure_file(p: &PathInfo, executable: bool) -> bool {
    p.exists
        && p.kind == "regular"
        && p.uid == 0
        && p.mode & 0o022 == 0
        && (!executable || p.mode & 0o100 != 0)
}
fn access_report(raw: RawAccess) -> AccessReport {
    let parent_ready = secure_directory(&raw.parent);
    let update_directory =
        if parent_ready && secure_directory(&raw.stage) && raw.stage.mode & 0o700 == 0o700 {
            "ready"
        } else if parent_ready
            && (!raw.stage.exists || (raw.stage.kind == "directory" && raw.stage.uid == 0))
        {
            "can_prepare"
        } else {
            "manual_review"
        };
    AccessReport {
        root_ssh: raw.root,
        update_directory: update_directory.into(),
        installed_binaries: secure_directory(&raw.bin_dir)
            && secure_file(&raw.daemon, true)
            && secure_file(&raw.ctl, true),
        database: secure_file(&raw.database, false),
        service_active: raw.service_active,
        checks: raw.checks,
        recent_jobs: raw.recent_jobs,
    }
}

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Manifest {
    source_commit: String,
    version: String,
    dockyard_sha256: String,
    dockyardctl_sha256: String,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Preview {
    pub source_commit: String,
    pub candidate_version: String,
    pub installed_version: Option<String>,
    pub installed_commit: Option<String>,
    pub version_status: String,
    pub candidate_dockyard: String,
    pub candidate_dockyardctl: String,
    pub installed_dockyard: String,
    pub installed_dockyardctl: String,
    pub update_available: bool,
    pub jobs: UpdateJobs,
}

#[derive(Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct UpdateJob {
    pub job_id: String,
    pub project_id: String,
    pub status: String,
    pub action: String,
    pub phase: String,
    pub error_code: String,
}
#[derive(Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct UpdateJobs {
    pub sha256: String,
    pub active_count: usize,
    pub recovery_count: usize,
    pub active_jobs: Vec<UpdateJob>,
    pub recovery_jobs: Vec<UpdateJob>,
}
impl UpdateJobs {
    fn valid(&self) -> bool {
        let token = |s: &str| s.len() <= 128 && s.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_');
        valid_hash(&self.sha256) && self.active_count <= 1000 && self.recovery_count <= 1000
            && self.active_count + self.recovery_count <= 1000
            && self.active_jobs.len() == self.active_count.min(10)
            && self.recovery_jobs.len() == self.recovery_count.min(10)
            && self.active_jobs.iter().all(|j| j.status == "queued" || j.status == "running")
            && self.recovery_jobs.iter().all(|j| j.status == "recovery_required")
            && self.active_jobs.iter().chain(&self.recovery_jobs).all(|j|
                [&j.job_id, &j.project_id, &j.action, &j.phase, &j.error_code].iter().all(|s| token(s)))
    }
}

fn hash(bytes: &[u8]) -> String {
    hex::encode(Sha256::digest(bytes))
}
fn valid_hex(s: &str, len: usize) -> bool {
    s.len() == len
        && s.bytes()
            .all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase())
}
fn valid_hash(s: &str) -> bool {
    valid_hex(s, 64)
}
fn version_parts(s: &str) -> Option<(u32, u32, u32)> {
    let parts: Vec<_> = s.split('.').collect();
    if parts.len() != 3 {
        return None;
    }
    Some((
        parts[0].parse().ok()?,
        parts[1].parse().ok()?,
        parts[2].parse().ok()?,
    ))
}
#[derive(Deserialize)]
struct InstalledBuild {
    version: String,
    commit: String,
    target: String,
}
fn installed_build(line: &str) -> Result<Option<InstalledBuild>, String> {
    if line == "legacy" {
        return Ok(None);
    }
    let build: InstalledBuild =
        serde_json::from_str(line).map_err(|_| "VPS returned invalid version information")?;
    if version_parts(&build.version).is_none()
        || !(valid_hex(&build.commit, 40) || build.commit == "local")
        || build.target != "linux/amd64"
    {
        return Err("VPS returned invalid version information".into());
    }
    Ok(Some(build))
}
fn update_status(different: bool, candidate: &str, installed: Option<&str>) -> &'static str {
    if !different {
        return "current";
    }
    if candidate == "unversioned" {
        return if installed.is_none() {
            "legacy_bundle"
        } else {
            "bundle_unversioned"
        };
    }
    if let Some(version) = installed {
        if version_parts(version) > version_parts(candidate) {
            "server_newer"
        } else if version == candidate {
            "same_version_different_build"
        } else {
            "update_available"
        }
    } else {
        "legacy"
    }
}
fn manifest() -> Result<Manifest, String> {
    let m: Manifest =
        serde_json::from_str(MANIFEST).map_err(|_| "Bundled update manifest is invalid")?;
    if !(valid_hex(&m.source_commit, 40) || m.source_commit == "local")
        || (m.version != "unversioned" && version_parts(&m.version).is_none())
        || !valid_hash(&m.dockyard_sha256)
        || !valid_hash(&m.dockyardctl_sha256)
    {
        return Err("Bundled update manifest has invalid version or checksums".into());
    }
    Ok(m)
}
// CI publishes each release's Ubuntu binaries with SHA256SUMS to GitHub.
const RELEASES: &str = "https://api.github.com/repos/fathah/ziqx-dockyard-service/releases/latest";

type Build = (Vec<u8>, Vec<u8>, Manifest);
static RELEASE_CACHE: std::sync::Mutex<Option<(String, Build)>> = std::sync::Mutex::new(None);

fn http() -> Result<reqwest::blocking::Client, String> {
    reqwest::blocking::Client::builder()
        .user_agent("dockyard-desktop")
        .connect_timeout(std::time::Duration::from_secs(10))
        .timeout(std::time::Duration::from_secs(180))
        .build()
        .map_err(|_| "Cannot start the download client".into())
}

/// Latest published release, verified against its SHA256SUMS. Cached per tag.
fn latest_release() -> Result<Build, String> {
    #[derive(Deserialize)]
    struct Asset {
        name: String,
        browser_download_url: String,
    }
    #[derive(Deserialize)]
    struct Release {
        tag_name: String,
        assets: Vec<Asset>,
    }
    let client = http()?;
    let release: Release = client
        .get(RELEASES)
        .header("Accept", "application/vnd.github+json")
        .send()
        .and_then(|r| r.error_for_status())
        .and_then(|r| r.json())
        .map_err(|_| "Cannot read the latest release from GitHub")?;
    if let Some((tag, build)) = RELEASE_CACHE.lock().map_err(|_| "Release cache unavailable")?.as_ref() {
        if *tag == release.tag_name {
            return Ok(build.clone());
        }
    }
    let fetch = |name: &str| -> Result<Vec<u8>, String> {
        let url = release
            .assets
            .iter()
            .find(|a| a.name == name)
            .map(|a| a.browser_download_url.clone())
            .ok_or_else(|| format!("The latest release has no {name}"))?;
        client
            .get(url)
            .send()
            .and_then(|r| r.error_for_status())
            .and_then(|r| r.bytes())
            .map(|b| b.to_vec())
            .map_err(|_| format!("Download of {name} failed"))
    };
    let sums = String::from_utf8(fetch("SHA256SUMS")?).map_err(|_| "Invalid SHA256SUMS")?;
    let info = String::from_utf8(fetch("BUILD-INFO.txt")?).map_err(|_| "Invalid BUILD-INFO.txt")?;
    let field = |key: &str| {
        info.lines()
            .find_map(|l| l.strip_prefix(key).map(|v| v.trim().to_string()))
            .unwrap_or_default()
    };
    let expected = |name: &str| {
        sums.lines().find_map(|l| {
            let (sum, file) = l.split_once(char::is_whitespace)?;
            (file.trim().trim_start_matches('*') == name).then(|| sum.to_string())
        })
    };
    let m = Manifest {
        source_commit: field("Source commit:"),
        version: field("Version:"),
        dockyard_sha256: expected("dockyard-linux-amd64").ok_or("SHA256SUMS has no dockyard")?,
        dockyardctl_sha256: expected("dockyardctl-linux-amd64").ok_or("SHA256SUMS has no dockyardctl")?,
    };
    if !valid_hex(&m.source_commit, 40) || version_parts(&m.version).is_none() || !valid_hash(&m.dockyard_sha256) || !valid_hash(&m.dockyardctl_sha256) {
        return Err("The latest release has invalid build information".into());
    }
    let agent = fetch("dockyard-linux-amd64")?;
    let ctl = fetch("dockyardctl-linux-amd64")?;
    if hash(&agent) != m.dockyard_sha256 || hash(&ctl) != m.dockyardctl_sha256 {
        return Err("The downloaded release failed its checksum".into());
    }
    let build = (agent, ctl, m);
    *RELEASE_CACHE.lock().map_err(|_| "Release cache unavailable")? = Some((release.tag_name, build.clone()));
    Ok(build)
}

/// The newest verified build: the latest GitHub release when it is newer than
/// the one bundled in this app, otherwise (or offline) the bundled build.
fn binaries(app: &AppHandle) -> Result<Build, String> {
    let bundled = bundled_binaries(app);
    match (latest_release(), bundled) {
        (Ok(remote), Ok(local)) => {
            let newer = version_parts(&remote.2.version) > version_parts(&local.2.version)
                || (remote.2.version == local.2.version && local.2.source_commit == "local");
            Ok(if newer { remote } else { local })
        }
        (Ok(remote), Err(_)) => Ok(remote),
        (Err(_), local) => local,
    }
}
fn bundled_binaries(app: &AppHandle) -> Result<Build, String> {
    let m = manifest()?;
    let agent = setup::binary(app, "dockyard")?;
    let ctl = setup::binary(app, "dockyardctl")?;
    if hash(&agent) != m.dockyard_sha256 || hash(&ctl) != m.dockyardctl_sha256 {
        return Err(
            "Bundled Ubuntu update failed its checksum. Reinstall a verified Mac build".into(),
        );
    }
    Ok((agent, ctl, m))
}
fn ssh(app: &AppHandle, identity: &SshIdentity, reason: Option<&str>) -> Result<Session, String> {
    if let Some(reason) = reason {
        native::authenticate_reason(reason)?;
    }
    let (session, _) = setup::connect(
        &identity.server_ip,
        identity.port,
        Some(&identity.host_sha256),
    )?;
    let account = terminal::account(&identity.server_ip, identity.port, &identity.host_sha256);
    let password = native::root_credential(app, format!("{}:{}", identity.server_ip, identity.port), &account, false)?;
    if session.userauth_password("root", &password).is_err() || !session.authenticated() {
        native::terminal_invalidate(&account);
        return Err("Root SSH login failed. Try again to enter the current password.".into());
    }
    native::terminal_remember_for_run(&account, password.as_bytes())?;
    Ok(session)
}
fn command(session: &Session, cmd: &str, timeout: u32) -> Result<String, String> {
    command_limited(session, cmd, timeout, 8192)
}
fn command_limited(session: &Session, cmd: &str, timeout: u32, limit: usize) -> Result<String, String> {
    session.set_timeout(timeout);
    let mut channel = session
        .channel_session()
        .map_err(|_| "Cannot open update command channel")?;
    channel.exec(cmd).map_err(|_| "Cannot run update command")?;
    let mut output = String::new();
    Read::by_ref(&mut channel)
        .take((limit + 1) as u64)
        .read_to_string(&mut output)
        .map_err(|_| {
            "Server update response was interrupted; inspect the service before retrying"
        })?;
    if output.len() > limit {
        return Err("Server update response exceeded its limit".into());
    }
    channel
        .wait_close()
        .map_err(|_| "Server update outcome is uncertain; inspect the service before retrying")?;
    if channel
        .exit_status()
        .map_err(|_| "Server update status unavailable")?
        != 0
    {
        return Err("Server update check failed. Confirm Ubuntu x86-64, root SSH, installed binaries, and an active Dockyard service".into());
    }
    Ok(output)
}
fn inspect_access(session: &Session, key_id: &str) -> Result<AccessReport, String> {
    if key_id.is_empty() || key_id.len() > 48 || !key_id.bytes().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-') {
        return Err("Invalid enrolled credential".into());
    }
    let script = ACCESS_CHECK.replace('\'', "'\"'\"'");
    let output = command_limited(session, &format!("python3 -c '{}' '{}'", script, key_id), 90000, 32768).map_err(|_| {
        "Could not check updater access. Confirm Python 3 and root SSH are available on the VPS"
            .to_owned()
    })?;
    let raw: RawAccess =
        serde_json::from_str(output.trim()).map_err(|_| "VPS returned an invalid access check")?;
    Ok(access_report(raw))
}
pub fn check_access(app: &AppHandle, identity: &SshIdentity, key_id: &str) -> Result<AccessReport, String> {
    let session = ssh(app, identity, None)?;
    inspect_access(&session, key_id)
}
pub fn prepare_access(app: &AppHandle, identity: &SshIdentity, key_id: &str) -> Result<AccessReport, String> {
    let session = ssh(
        app,
        identity,
        Some("Prepare Dockyard's private updater directory on your VPS"),
    )?;
    if inspect_access(&session, key_id)?.update_directory != "can_prepare" {
        return Err("Updater directory cannot be prepared automatically. Review its owner and path on the VPS".into());
    }
    command(&session, ACCESS_PREPARE, 15000).map_err(|_| {
        "Could not prepare the private updater directory. Review /var/lib/dockyard-desktop-updates on the VPS".to_owned()
    })?;
    let report = inspect_access(&session, key_id)?;
    if report.update_directory != "ready" {
        return Err("Updater directory still needs VPS review after preparation".into());
    }
    Ok(report)
}
fn inspect(session: &Session, m: &Manifest) -> Result<Preview, String> {
    let output = command(session, INSPECT, 15000)?;
    let lines: Vec<&str> = output.lines().collect();
    if lines.len() != 4 || lines[2].trim() != "active" {
        return Err("Dockyard service is not active on this VPS".into());
    }
    let installed: Vec<&str> = lines[..2]
        .iter()
        .map(|line| line.split_whitespace().next().unwrap_or(""))
        .collect();
    if installed.iter().any(|s| !valid_hash(s)) {
        return Err("VPS returned invalid binary checksums".into());
    }
    let build = installed_build(lines[3].trim())?;
    let different = installed[0] != m.dockyard_sha256 || installed[1] != m.dockyardctl_sha256;
    let status = update_status(
        different,
        &m.version,
        build.as_ref().map(|b| b.version.as_str()),
    );
    let script = JOB_CHECK.replace('\'', "'\"'\"'");
    let raw_jobs = command_limited(session, &format!("python3 -c '{}'", script), 15000, 16384)
        .map_err(|_| "Could not inspect Dockyard jobs. Check Python 3 and database access on the VPS before updating".to_owned())?;
    let jobs: UpdateJobs = serde_json::from_str(raw_jobs.trim())
        .map_err(|_| "VPS returned an invalid update job check")?;
    if !jobs.valid() { return Err("VPS returned an invalid update job check".into()); }
    Ok(Preview {
        source_commit: m.source_commit.clone(),
        candidate_version: m.version.clone(),
        installed_version: build.as_ref().map(|b| b.version.clone()),
        installed_commit: build.as_ref().map(|b| b.commit.clone()),
        version_status: status.into(),
        candidate_dockyard: m.dockyard_sha256.clone(),
        candidate_dockyardctl: m.dockyardctl_sha256.clone(),
        installed_dockyard: installed[0].into(),
        installed_dockyardctl: installed[1].into(),
        update_available: status == "update_available"
            || status == "legacy"
            || status == "legacy_bundle",
        jobs,
    })
}
pub fn check(app: &AppHandle, identity: &SshIdentity) -> Result<Preview, String> {
    let (_, _, m) = binaries(app)?;
    let session = ssh(app, identity, None)?;
    inspect(&session, &m)
}
fn upload(session: &Session, path: &str, bytes: &[u8], executable: bool) -> Result<(), String> {
    session.set_timeout(120000);
    let sftp = session
        .sftp()
        .map_err(|_| "Cannot open secure update upload")?;
    let mut file = sftp
        .open_mode(
            Path::new(path),
            OpenFlags::WRITE | OpenFlags::CREATE | OpenFlags::EXCLUSIVE,
            if executable {
                BINARY_UPLOAD_MODE
            } else {
                DATA_UPLOAD_MODE
            },
            OpenType::File,
        )
        .map_err(|_| "Cannot create private update file on VPS")?;
    file.write_all(bytes)
        .map_err(|_| "Update upload was interrupted; no service binaries were replaced")?;
    file.fsync()
        .map_err(|_| "Cannot persist uploaded update file")?;
    Ok(())
}
pub fn apply(
    app: &AppHandle,
    identity: &SshIdentity,
    expected: &Preview,
) -> Result<String, String> {
    let (agent, ctl, m) = binaries(app)?;
    if expected.source_commit != m.source_commit
        || expected.candidate_version != m.version
        || expected.candidate_dockyard != m.dockyard_sha256
        || expected.candidate_dockyardctl != m.dockyardctl_sha256
        || !expected.update_available
        || !expected.jobs.valid()
    {
        return Err("The Mac app build changed. Check the server version again".into());
    }
    if expected.jobs.active_count > 0 {
        return Err("A Dockyard job is queued or running. Wait for it to finish, then check the server again".into());
    }
    let session = ssh(app, identity, None)?;
    let current = inspect(&session, &m)?;
    if current.installed_dockyard != expected.installed_dockyard
        || current.installed_dockyardctl != expected.installed_dockyardctl
        || current.installed_version != expected.installed_version
        || current.installed_commit != expected.installed_commit
        || !current.update_available
    {
        return Err(
            "The VPS binaries changed since your update check. Check again before updating".into(),
        );
    }
    if current.jobs != expected.jobs {
        return Err("Dockyard jobs changed since your update review. Check the server again before updating".into());
    }
    native::authenticate_reason(if current.jobs.recovery_count > 0 {
        "Update Dockyard while preserving its recovery jobs and write lock. This does not repair the failed deployment."
    } else {
        "Update and restart the Dockyard control service on your VPS"
    })?;
    let stage = format!(
        "/var/lib/dockyard-desktop-updates/{}",
        uuid::Uuid::new_v4().simple()
    );
    command(&session, &format!("set -eu; test ! -L /var/lib/dockyard-desktop-updates; install -d -o root -g root -m 0700 /var/lib/dockyard-desktop-updates; install -d -o root -g root -m 0700 {stage}"), 15000)?;
    upload(&session, &format!("{stage}/dockyard"), &agent, true)?;
    upload(&session, &format!("{stage}/dockyardctl"), &ctl, true)?;
    command(&session, &format!("set -eu; for name in dockyard dockyardctl; do path={stage}/$name; test -f \"$path\"; test ! -L \"$path\"; test \"$(stat -c %u \"$path\")\" = 0; chmod 0700 \"$path\"; test -x \"$path\"; done"), 15000)
        .map_err(|_| "Could not make the private updater binaries executable on the VPS".to_owned())?;
    upload(&session, &format!("{stage}/update.py"), SCRIPT, false)?;
    upload(&session, &format!("{stage}/update_jobs.py"), JOB_CHECK.as_bytes(), false)?;
    upload(&session, &format!("{stage}/dockyard.service"), UNIT, false)?;
    let request = serde_json::to_vec(&json!({"candidate":{"dockyard":m.dockyard_sha256,"dockyardctl":m.dockyardctl_sha256},"expected":{"dockyard":expected.installed_dockyard,"dockyardctl":expected.installed_dockyardctl},"jobs_sha256":expected.jobs.sha256})).map_err(|_| "Cannot prepare update request")?;
    upload(&session, &format!("{stage}/request.json"), &request, false)?;
    // Once started, this command must finish even if the UI session expires.
    let response =
        command(&session, &format!("python3 {stage}/update.py"), 180000).map_err(|e| {
            format!(
                "{e}. If the update was interrupted, inspect {stage} on the VPS before retrying"
            )
        })?;
    let value: serde_json::Value = serde_json::from_str(response.trim())
        .map_err(|_| format!("Update result was unclear. Inspect {stage} on the VPS"))?;
    match value.get("status").and_then(|s| s.as_str()) {
        Some("updated") => {
            let now = inspect(&session, &m)?;
            if now.installed_dockyard != m.dockyard_sha256
                || now.installed_dockyardctl != m.dockyardctl_sha256
                || (m.version != "unversioned"
                    && now.installed_version.as_deref() != Some(&m.version))
                || (m.version == "unversioned" && now.installed_version.is_some())
            {
                return Err(format!("The service restarted, but its binaries do not match the expected checksums. Inspect {stage}"));
            }
            Ok(m.version)
        }
        Some("current") => Ok(m.version),
        Some("error") => Err(value
            .get("message")
            .and_then(|s| s.as_str())
            .unwrap_or("Server update failed")
            .to_string()),
        _ => Err(format!(
            "Update result was unclear. Inspect {stage} on the VPS"
        )),
    }
}

#[cfg(test)]
mod tests {
    #[test]
    #[ignore = "network: downloads the latest GitHub release"]
    fn latest_release_downloads_and_verifies() {
        let (agent, ctl, m) = latest_release().expect("release");
        assert!(version_parts(&m.version).is_some());
        assert_eq!(hash(&agent), m.dockyard_sha256);
        assert_eq!(hash(&ctl), m.dockyardctl_sha256);
        println!("latest release {} ({})", m.version, m.source_commit);
    }

    use super::*;

    #[test]
    fn job_review_accepts_recovery_but_rejects_untrusted_metadata() {
        let raw = json!({"sha256":"a".repeat(64),"active_count":0,"recovery_count":1,"active_jobs":[],"recovery_jobs":[{
            "job_id":"job-example","project_id":"tasks","status":"recovery_required","action":"start","phase":"draining","error_code":"CONTAINER_STOP_FAILED"
        }]});
        let jobs: UpdateJobs = serde_json::from_value(raw.clone()).unwrap();
        assert!(jobs.valid());
        let mut wrong = jobs.clone();
        wrong.recovery_count = 0;
        assert!(!wrong.valid());
        wrong = jobs.clone();
        wrong.recovery_jobs[0].status = "running".into();
        assert!(!wrong.valid());
        wrong = jobs;
        wrong.recovery_jobs[0].error_code = "untrusted prose\nsecret".into();
        assert!(!wrong.valid());
        let mut extra = raw;
        extra["recovery_jobs"][0]["input"] = json!({"environment":"private"});
        assert!(serde_json::from_value::<UpdateJobs>(extra).is_err());
    }

    #[test]
    fn packaged_update_manifest_has_valid_version_and_checksums() {
        let m = manifest().expect("release manifest must parse");
        assert!(version_parts(&m.version).is_some() || m.version == "unversioned");
        assert!(valid_hex(&m.source_commit, 40) || m.source_commit == "local");
        assert!(valid_hash(&m.dockyard_sha256));
        assert!(valid_hash(&m.dockyardctl_sha256));
    }
    #[test]
    fn version_order_and_legacy_detection() {
        assert!(version_parts("0.2.0") > version_parts("0.1.9"));
        assert_eq!(version_parts("0.2.0-preview"), None);
        assert!(installed_build("legacy").unwrap().is_none());
        assert_eq!(update_status(true, "0.2.0", Some("0.3.0")), "server_newer");
        assert_eq!(
            update_status(true, "0.2.0", Some("0.2.0")),
            "same_version_different_build"
        );
        assert_eq!(
            update_status(true, "unversioned", Some("0.2.0")),
            "bundle_unversioned"
        );
        assert_eq!(update_status(true, "unversioned", None), "legacy_bundle");
    }
    #[test]
    fn updater_uploads_binaries_executable_and_limits_repairs() {
        assert_eq!(BINARY_UPLOAD_MODE, 0o700);
        assert_eq!(DATA_UPLOAD_MODE, 0o600);
        let dir = PathInfo {
            exists: true,
            kind: "directory".into(),
            uid: 0,
            mode: 0o755,
        };
        let file = PathInfo {
            exists: true,
            kind: "regular".into(),
            uid: 0,
            mode: 0o755,
        };
        let mut raw = RawAccess {
            root: true,
            parent: dir.clone(),
            stage: PathInfo {
                mode: 0o700,
                ..dir.clone()
            },
            bin_dir: dir,
            daemon: file.clone(),
            ctl: file.clone(),
            database: PathInfo {
                mode: 0o600,
                ..file
            },
            service_active: true,
            checks: vec![],
            recent_jobs: vec![],
        };
        assert_eq!(access_report(raw.clone()).update_directory, "ready");
        raw.stage.mode = 0o777;
        assert_eq!(access_report(raw.clone()).update_directory, "can_prepare");
        raw.stage.kind = "other".into();
        assert_eq!(access_report(raw).update_directory, "manual_review");
    }
}

pub fn enable_compose(app: &AppHandle, identity: &SshIdentity, key_id: &str) -> Result<(), String> {
    if key_id.is_empty()
        || key_id.len() > 48
        || !key_id
            .bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
    {
        return Err("Invalid enrolled credential".into());
    }
    let session = ssh(app, identity, Some("Allow this Mac to deploy Docker Compose stacks with full server privileges. Dockyard will restart briefly."))?;
    let script = include_str!("../../updater/compose_access.py").replace('\'', "'\"'\"'");
    let output = command(
        &session,
        &format!("python3 -c '{}' '{}'", script, key_id),
        180000,
    )?;
    let value: serde_json::Value =
        serde_json::from_str(output.trim()).map_err(|_| "Invalid Compose access response")?;
    if value["enabled"] == true {
        return Ok(());
    }
    Err(value["error"]
        .as_str()
        .unwrap_or("Could not enable Compose management")
        .to_owned())
}
