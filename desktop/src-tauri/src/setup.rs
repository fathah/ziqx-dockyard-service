use crate::{
    native,
    protocol::{Enrollment, SshIdentity},
};
use base64::{
    engine::general_purpose::{STANDARD, STANDARD_NO_PAD},
    Engine,
};
use rcgen::{
    BasicConstraints, CertificateParams, DnType, ExtendedKeyUsagePurpose, IsCa, Issuer, KeyPair,
    KeyUsagePurpose,
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use ssh2::{OpenFlags, OpenType, Session as SshSession};
use std::{
    collections::BTreeMap,
    io::{Read, Write},
    net::{IpAddr, SocketAddr, TcpListener, TcpStream},
    path::Path,
    sync::{
        atomic::{AtomicBool, AtomicUsize, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};
use tauri::{Emitter, Manager};
use zeroize::{Zeroize, Zeroizing};

const PROBE: &str = r#"set -eu
. /etc/os-release
printf '%s\n' "$ID" "$VERSION_ID" "$(uname -m)" "$(id -u)"
if test -e /etc/dockyard/config.json; then echo configured; else echo fresh; fi
if command -v docker >/dev/null; then echo docker; else echo no-docker; fi
if command -v caddy >/dev/null; then echo caddy; else echo no-caddy; fi
if command -v python3 >/dev/null; then echo python; else echo no-python; fi
"#;
pub const PHASES: &[&str] = &[
    "preflight",
    "verify",
    "dependencies",
    "credentials",
    "caddy",
    "ssh",
    "service",
    "complete",
];

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub os: String,
    pub server_ip: String,
    pub ssh_port: u16,
    pub name: String,
    pub import_policy: bool,
}
impl Request {
    pub fn validate(&self) -> Result<IpAddr, String> {
        let ip: IpAddr = self
            .server_ip
            .parse()
            .map_err(|_| "Enter a server IP, not a hostname or URL")?;
        if self.os != "ubuntu"
            || ip.is_unspecified()
            || ip.is_multicast()
            || self.ssh_port == 0
            || self.name.is_empty()
            || self.name.len() > 80
            || self.name.chars().any(char::is_control)
        {
            return Err(
                "Ubuntu, a valid server IP/SSH port, and a name up to 80 bytes are required".into(),
            );
        }
        Ok(ip)
    }
}
#[derive(Serialize)]
pub struct Preview {
    pub id: String,
    pub server_ip: String,
    pub ssh_port: u16,
    pub host_sha256: String,
    pub ubuntu: String,
    pub docker_installed: bool,
    pub caddy_installed: bool,
    pub inventory_only: bool,
    pub binary_sha256: String,
}
pub struct Plan {
    pub session: SshSession,
    pub request: Request,
    pub host: String,
    pub version: String,
    pub docker: bool,
    pub caddy: bool,
    pub python: bool,
    pub policy: Option<Value>,
    pub id: String,
    pub created: Instant,
}
impl Plan {
    pub fn preview(&self, binary: &[u8]) -> Preview {
        Preview {
            id: self.id.clone(),
            server_ip: self.request.server_ip.clone(),
            ssh_port: self.request.ssh_port,
            host_sha256: self.host.clone(),
            ubuntu: self.version.clone(),
            docker_installed: self.docker,
            caddy_installed: self.caddy,
            inventory_only: false,
            binary_sha256: hex::encode(Sha256::digest(binary)),
        }
    }
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct File {
    pub name: String,
    pub hash: String,
    pub data: Option<String>,
}
impl Drop for File {
    fn drop(&mut self) {
        if let Some(data) = &mut self.data {
            data.zeroize();
        }
    }
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Receipt {
    pub id: String,
    pub server_ip: String,
    pub ssh_port: u16,
    pub host_sha256: String,
    pub files: Vec<File>,
}
impl Receipt {
    pub fn validate(&self) -> Result<(), String> {
        let allowed = [
            "server.crt",
            "server.key",
            "control-ca.crt",
            "desktop.key",
            "fingerprint.key",
            "config.json",
            "server.json",
            "ssh.pub",
            "dockyard.service",
            "install.py",
            "dockyard",
            "dockyardctl",
            "manifest.json",
        ];
        if self.id.len() != 32
            || !self
                .id
                .bytes()
                .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
            || self.files.len() != allowed.len()
        {
            return Err("Invalid saved setup receipt".into());
        }
        let mut names = std::collections::BTreeSet::new();
        for f in &self.files {
            if !allowed.contains(&f.name.as_str()) || !names.insert(&f.name) || f.hash.len() != 64 {
                return Err("Invalid saved setup file".into());
            }
            if let Some(data) = &f.data {
                if data.len() > 512 * 1024 {
                    return Err("Saved setup file is too large".into());
                }
                let bytes = Zeroizing::new(
                    STANDARD
                        .decode(data)
                        .map_err(|_| "Invalid saved setup payload")?,
                );
                if hex::encode(Sha256::digest(&bytes)) != f.hash {
                    return Err("Saved setup checksum mismatch".into());
                }
            } else if !matches!(f.name.as_str(), "dockyard" | "dockyardctl") {
                return Err("Missing saved setup material".into());
            }
        }
        Ok(())
    }
    fn stage(&self) -> String {
        format!("/var/lib/dockyard-desktop-setup/{}", self.id)
    }
}

pub(crate) fn connect(
    ip: &str,
    port: u16,
    expected: Option<&str>,
) -> Result<(SshSession, String), String> {
    let ip: IpAddr = ip.parse().map_err(|_| "Invalid SSH IP")?;
    if ip.is_unspecified() || ip.is_multicast() || port == 0 {
        return Err("Invalid SSH endpoint".into());
    }
    let tcp = TcpStream::connect_timeout(&SocketAddr::new(ip, port), Duration::from_secs(12))
        .map_err(|_| "Cannot reach the server over SSH. Check its IP, SSH port, and firewall")?;
    let mut session = SshSession::new().map_err(|_| "Cannot initialize SSH")?;
    session.set_tcp_stream(tcp);
    session.set_timeout(15000);
    for (kind, algorithms) in [
        (ssh2::MethodType::Kex, "curve25519-sha256,curve25519-sha256@libssh.org,ecdh-sha2-nistp256"),
        (ssh2::MethodType::HostKey, "ssh-ed25519,ecdsa-sha2-nistp256,rsa-sha2-512,rsa-sha2-256"),
        (ssh2::MethodType::CryptCs, "aes256-gcm@openssh.com,aes128-gcm@openssh.com,aes256-ctr,aes128-ctr"),
        (ssh2::MethodType::CryptSc, "aes256-gcm@openssh.com,aes128-gcm@openssh.com,aes256-ctr,aes128-ctr"),
        (ssh2::MethodType::MacCs, "hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,hmac-sha2-512,hmac-sha2-256"),
        (ssh2::MethodType::MacSc, "hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,hmac-sha2-512,hmac-sha2-256"),
        (ssh2::MethodType::CompCs, "none"), (ssh2::MethodType::CompSc, "none"),
    ] { session.method_pref(kind, algorithms).map_err(|_| "Modern SSH algorithms unavailable")?; }
    session.handshake().map_err(|_| "SSH handshake failed")?;
    let (key, _) = session
        .host_key()
        .ok_or("SSH server did not present a host key")?;
    let pin = format!("SHA256:{}", STANDARD_NO_PAD.encode(Sha256::digest(key)));
    if expected.is_some_and(|expected| expected != pin) {
        return Err("SSH fingerprint changed. Connection blocked before sending credentials; verify the server with your provider".into());
    }
    Ok((session, pin))
}
fn root_connect(
    app: &tauri::AppHandle,
    ip: &str,
    port: u16,
    expected: Option<&str>,
) -> Result<(SshSession, String), String> {
    let (session, pin) = connect(ip, port, expected)?;
    if expected.is_none()
        && native::with_prompt(|| {
            rfd::MessageDialog::new().set_title("Verify your server's SSH identity")
        .set_description(format!("Server: {ip}:{port}\nSSH fingerprint: {pin}\n\nCompare this with your VPS provider or a trusted SSH connection. Accept only if it matches. No password has been sent."))
        .set_buttons(rfd::MessageButtons::OkCancel).show()
        }) != rfd::MessageDialogResult::Ok
    {
        return Err("SSH identity review cancelled".into());
    }
    let account = crate::terminal::account(ip, port, &pin);
    let password = native::root_credential(app, format!("{ip}:{port}"), &account, false)?;
    if session.userauth_password("root", &password).is_err() || !session.authenticated() {
        native::terminal_invalidate(&account);
        return Err("Root SSH authentication failed. Try again to enter the current password.".into());
    }
    native::terminal_remember_for_run(&account, password.as_bytes())?;
    Ok((session, pin))
}
fn exec(session: &SshSession, command: &str) -> Result<String, String> {
    let mut channel = session
        .channel_session()
        .map_err(|_| "Cannot open SSH command channel")?;
    channel.exec(command).map_err(|_| "SSH command failed")?;
    let mut output = String::new();
    std::io::Read::by_ref(&mut channel)
        .take(32769)
        .read_to_string(&mut output)
        .map_err(|_| "SSH response failed")?;
    if output.len() > 32768 {
        return Err("SSH response exceeded its limit".into());
    }
    channel
        .wait_close()
        .map_err(|_| "SSH command outcome is uncertain")?;
    if channel
        .exit_status()
        .map_err(|_| "SSH command status unavailable")?
        != 0
    {
        return Err("Server inspection failed; check root SSH and Ubuntu tools".into());
    }
    Ok(output)
}
// Read metadata only. Missing install directories are allowed; all existing ancestors
// must still be protected. Never inspect or change project/volume ownership.
fn preflight(session: &SshSession, require_caddyfile: bool) -> Result<(), String> {
    let sftp = session
        .sftp()
        .map_err(|_| "Cannot check server folders over SSH. Reconnect and try again")?;
    for directory in crate::setup_error::DIRECTORIES {
        for path in Path::new(directory).ancestors() {
            match sftp.lstat(path) {
                Ok(stat) => crate::setup_error::check_stat(path.to_str().unwrap(), &stat, true)?,
                Err(error) if error.code() == ssh2::ErrorCode::SFTP(2) => {},
                Err(_) => return Err(format!("Cannot check {}. Ask your server administrator to check SSH file access, then try again", path.display())),
            }
        }
    }
    let path = "/etc/caddy/Caddyfile";
    match sftp.lstat(Path::new(path)) {
        Ok(stat) => {
            crate::setup_error::check_stat(path, &stat, false)?;
            if stat.size.is_none_or(|size| size > 1024 * 1024) {
                return Err(crate::setup_error::path_error("file_large", path, ""));
            }
        },
        Err(error) if error.code() == ssh2::ErrorCode::SFTP(2) && !require_caddyfile => {},
        Err(error) if error.code() == ssh2::ErrorCode::SFTP(2) => return Err(crate::setup_error::path_error("file_missing", path, "")),
        Err(_) => return Err("Cannot check Caddy’s configuration file. Ask your server administrator to check SSH file access, then try again".into()),
    }
    Ok(())
}

pub fn inspect(app: &tauri::AppHandle, request: Request) -> Result<Plan, String> {
    request.validate()?;
    let (session, host) = root_connect(app, &request.server_ip, request.ssh_port, None)?;
    let output = exec(&session, PROBE)?;
    let lines: Vec<_> = output.lines().collect();
    if lines.len() != 8
        || lines[0] != "ubuntu"
        || !matches!(lines[1], "22.04" | "24.04")
        || lines[2] != "x86_64"
        || lines[3] != "0"
    {
        return Err(
            "Setup currently supports Ubuntu 22.04/24.04 on x86-64 with root SSH access".into(),
        );
    }
    if lines[4] != "fresh" {
        return Err("Dockyard is already configured. Import an enrollment instead; setup will not overwrite it".into());
    }
    preflight(&session, lines[6] == "caddy")?;
    let policy = if request.import_policy {
        let path = native::with_prompt(|| {
            rfd::FileDialog::new()
                .set_title("Select your reviewed root deployment policy")
                .add_filter("Root policy", &["json"])
                .pick_file()
        })
        .ok_or("Policy selection cancelled")?;
        use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(|_| "Cannot read root policy")?;
        let info = file.metadata().map_err(|_| "Cannot inspect root policy")?;
        if !info.is_file()
            || info.uid() != unsafe { libc::getuid() }
            || info.mode() & 0o077 != 0
            || info.len() > 262144
        {
            return Err(
                "Policy must be your private mode-600 regular JSON file, up to 256 KiB".into(),
            );
        }
        let mut bytes = Zeroizing::new(Vec::new());
        file.take(262145)
            .read_to_end(&mut bytes)
            .map_err(|_| "Cannot read policy")?;
        let mut policy: Value =
            serde_json::from_slice(&bytes).map_err(|_| "Invalid root policy JSON")?;
        let object = policy
            .as_object_mut()
            .ok_or("Root policy must be a JSON object")?;
        // Only policy portions are accepted. Installation and credential paths remain fixed.
        object.retain(|k, _| {
            matches!(
                k.as_str(),
                "allowed_domains"
                    | "reserved_domains"
                    | "templates"
                    | "port_min"
                    | "port_max"
                    | "reserved_ports"
                    | "max_projects"
                    | "max_queued_jobs"
                    | "max_stack_memory_mb"
                    | "max_stack_cpus"
            )
        });

        Some(policy)
    } else {
        None
    };
    Ok(Plan {
        session,
        request,
        host,
        version: lines[1].into(),
        docker: lines[5] == "docker",
        caddy: lines[6] == "caddy",
        python: lines[7] == "python",
        policy,
        id: uuid::Uuid::new_v4().simple().to_string(),
        created: Instant::now(),
    })
}

pub fn binary(app: &tauri::AppHandle, name: &str) -> Result<Vec<u8>, String> {
    if !matches!(name, "dockyard" | "dockyardctl") {
        return Err("Invalid installer binary".into());
    }
    let path = app
        .path()
        .resolve(
            format!("resources/ubuntu/{name}"),
            tauri::path::BaseDirectory::Resource,
        )
        .map_err(|_| "Installer resource path unavailable")?;
    let bytes=std::fs::read(path).map_err(|_| "This app build is missing its bundled Ubuntu binaries. Rebuild with desktop/scripts/package-server.sh")?;
    if bytes.len() > 32 * 1024 * 1024
        || bytes.len() < 20
        || &bytes[..4] != b"\x7fELF"
        || bytes[4] != 2
        || bytes[5] != 1
        || bytes[18..20] != [62, 0]
    {
        return Err("Installer binary must be a bounded Linux x86-64 ELF".into());
    }
    Ok(bytes)
}
fn cert_params(name: &str, ca: bool, server: bool) -> Result<CertificateParams, String> {
    let mut p = CertificateParams::new(if server {
        vec!["127.0.0.1".into()]
    } else {
        vec![]
    })
    .map_err(|_| "Certificate parameters failed")?;
    p.distinguished_name.push(DnType::CommonName, name);
    p.not_before = time::OffsetDateTime::now_utc() - time::Duration::minutes(5);
    p.not_after =
        time::OffsetDateTime::now_utc() + time::Duration::days(if ca { 3650 } else { 90 });
    p.key_usages = vec![KeyUsagePurpose::DigitalSignature];
    if ca {
        p.is_ca = IsCa::Ca(BasicConstraints::Constrained(0));
        p.key_usages = vec![KeyUsagePurpose::KeyCertSign, KeyUsagePurpose::CrlSign];
    } else {
        p.extended_key_usages = vec![if server {
            ExtendedKeyUsagePurpose::ServerAuth
        } else {
            ExtendedKeyUsagePurpose::ClientAuth
        }];
    }
    Ok(p)
}
fn random_secret() -> String {
    let mut b = [0; 32];
    ssh_key::rand_core::RngCore::fill_bytes(&mut ssh_key::rand_core::OsRng, &mut b);
    let s = STANDARD.encode(b);
    b.zeroize();
    s
}

pub fn material(app: &tauri::AppHandle, plan: &Plan) -> Result<(Enrollment, Receipt), String> {
    material_with_binaries(plan, binary(app, "dockyard")?, binary(app, "dockyardctl")?)
}
fn material_with_binaries(
    plan: &Plan,
    agent: Vec<u8>,
    ctl: Vec<u8>,
) -> Result<(Enrollment, Receipt), String> {
    if plan.created.elapsed() > Duration::from_secs(600) {
        return Err("Setup review expired. Reconnect and inspect again".into());
    }
    let server_ca_key = KeyPair::generate().map_err(|_| "CA generation failed")?;
    let server_ca_params = cert_params("Dockyard private server CA", true, false)?;
    let server_ca = server_ca_params
        .self_signed(&server_ca_key)
        .map_err(|_| "CA signing failed")?;
    let client_ca_key = KeyPair::generate().map_err(|_| "Client CA generation failed")?;
    let client_ca_params = cert_params("Dockyard dedicated Mac CA", true, false)?;
    let client_ca = client_ca_params
        .self_signed(&client_ca_key)
        .map_err(|_| "Client CA signing failed")?;
    let server_key = KeyPair::generate().map_err(|_| "Server key generation failed")?;
    let server = cert_params("Dockyard VPS", false, true)?
        .signed_by(
            &server_key,
            &Issuer::from_params(&server_ca_params, &server_ca_key),
        )
        .map_err(|_| "Server signing failed")?;
    let client_key = KeyPair::generate().map_err(|_| "Mac key generation failed")?;
    let client = cert_params("Dockyard Mac", false, false)?
        .signed_by(
            &client_key,
            &Issuer::from_params(&client_ca_params, &client_ca_key),
        )
        .map_err(|_| "Mac signing failed")?;
    let ssh_key =
        ssh_key::PrivateKey::random(&mut ssh_key::rand_core::OsRng, ssh_key::Algorithm::Ed25519)
            .map_err(|_| "SSH key generation failed")?;
    let public = ssh_key
        .public_key()
        .to_openssh()
        .map_err(|_| "SSH public key encoding failed")?;
    let private = ssh_key
        .to_openssh(ssh_key::LineEnding::LF)
        .map_err(|_| "SSH private key encoding failed")?;
    let enrollment = Enrollment {
        name: plan.request.name.clone(),
        origin: "https://127.0.0.1:9123".into(),
        server_id: "vps-01".into(),
        key_id: "desktop-01".into(),
        actor_id: "mac-owner".into(),
        server_ca_pem: server_ca.pem(),
        server_certificate_sha256: hex::encode(Sha256::digest(server.der())),
        client_identity_pem: format!("{}{}", client.pem(), client_key.serialize_pem()),
        hmac_base64: random_secret(),
        ssh: Some(SshIdentity {
            server_ip: plan.request.server_ip.clone(),
            port: plan.request.ssh_port,
            host_sha256: plan.host.clone(),
            private_key: private.to_string(),
        }),
    };
    enrollment.validate()?;
    let mut config: Value =
        serde_json::from_str(include_str!("../../../deploy/config.example.json"))
            .map_err(|_| "Bundled policy invalid")?;
    config.as_object_mut().unwrap().remove("cloudflare");
    if let Some(policy) = &plan.policy {
        for (k, v) in policy.as_object().unwrap() {
            config[k] = v.clone();
        }
    } else {
        config["allowed_domains"] = json!([]);
        config["reserved_domains"] = json!([]);
        config["templates"] = json!({});
    }
    config["keys"] = json!([{"id":"desktop-01","secret_file":"/etc/dockyard/desktop-01.key","certificate_sha256":hex::encode(Sha256::digest(client.der())),"scopes": vec!["compose.admin","projects.write","sites.write","dns.write","deploy.read","deploy.logs","deploy.execute","deploy.environment","deploy.rollback","deploy.lifecycle","deploy.stop"],"projects":["*"]}]);
    let mut files:BTreeMap<String,Vec<u8>>=BTreeMap::from([
        ("server.crt".into(),server.pem().into_bytes()),("server.key".into(),server_key.serialize_pem().into_bytes()),
        ("control-ca.crt".into(),client_ca.pem().into_bytes()),("desktop.key".into(),format!("{}\n",enrollment.hmac_base64).into_bytes()),
        ("fingerprint.key".into(),format!("{}\n",random_secret()).into_bytes()),("config.json".into(),serde_json::to_vec_pretty(&config).map_err(|_| "Policy encoding failed")?),
        ("server.json".into(),serde_json::to_vec(&json!({"server_ip":plan.request.server_ip,"ssh_port":plan.request.ssh_port,"host_sha256":plan.host,"installation_id":plan.id})).unwrap()),
        ("ssh.pub".into(),public.into_bytes()),("dockyard.service".into(),include_bytes!("../../../deploy/dockyard.service").to_vec()),
        ("install.py".into(),include_bytes!("../../installer/install.py").to_vec()),("dockyard".into(),agent),("dockyardctl".into(),ctl)
    ]);
    let hashes: BTreeMap<_, _> = files
        .iter()
        .map(|(k, v)| (k.clone(), hex::encode(Sha256::digest(v))))
        .collect();
    files.insert(
        "manifest.json".into(),
        serde_json::to_vec(&json!({"version":1,"id":plan.id,"files":hashes})).unwrap(),
    );
    let saved = files
        .iter()
        .map(|(name, bytes)| File {
            name: name.clone(),
            hash: hex::encode(Sha256::digest(bytes)),
            data: if matches!(name.as_str(), "dockyard" | "dockyardctl") {
                None
            } else {
                Some(STANDARD.encode(bytes))
            },
        })
        .collect();
    for bytes in files.values_mut() {
        bytes.zeroize();
    }
    let receipt = Receipt {
        id: plan.id.clone(),
        server_ip: plan.request.server_ip.clone(),
        ssh_port: plan.request.ssh_port,
        host_sha256: plan.host.clone(),
        files: saved,
    };
    receipt.validate()?;
    Ok((enrollment, receipt))
}

pub fn resume_connection(app: &tauri::AppHandle, receipt: &Receipt) -> Result<SshSession, String> {
    receipt.validate()?;
    Ok(root_connect(
        app,
        &receipt.server_ip,
        receipt.ssh_port,
        Some(&receipt.host_sha256),
    )?
    .0)
}
pub fn apply(
    app: &tauri::AppHandle,
    session: &SshSession,
    receipt: &Receipt,
    python: bool,
) -> Result<(), String> {
    receipt.validate()?;
    let _ = app.emit("setup-progress", json!({"phase":"preflight"}));
    preflight(session, false)?;
    let stage = receipt.stage();
    let sftp = session
        .sftp()
        .map_err(|_| "Cannot open the secure upload channel")?;
    for path in ["/var/lib/dockyard-desktop-setup", stage.as_str()] {
        match sftp.lstat(Path::new(path)) {
            Ok(stat) => crate::setup_error::check_stat(path, &stat, true)?,
            Err(error) if error.code() == ssh2::ErrorCode::SFTP(2) => {}
            Err(_) => return Err("Cannot inspect the server setup directory".into()),
        }
    }
    exec(session,&format!("test ! -L /var/lib/dockyard-desktop-setup && install -d -o root -g root -m 0700 /var/lib/dockyard-desktop-setup && test ! -L {stage} && install -d -o root -g root -m 0700 {stage}"))?;
    let sftp = session
        .sftp()
        .map_err(|_| "Cannot open the secure upload channel")?;
    let _ = app.emit("setup-progress", json!({"phase":"verify"}));
    for f in receipt
        .files
        .iter()
        .filter(|f| f.name != "manifest.json")
        .chain(receipt.files.iter().filter(|f| f.name == "manifest.json"))
    {
        let data = Zeroizing::new(if let Some(data) = &f.data {
            STANDARD
                .decode(data)
                .map_err(|_| "Invalid saved setup material")?
        } else {
            binary(app, &f.name)?
        });
        if hex::encode(Sha256::digest(&data)) != f.hash {
            return Err("Installer resources changed since this setup began. Resume with the original app build".into());
        }
        let path = format!("{stage}/{}", f.name);
        if let Ok(stat) = sftp.lstat(Path::new(&path)) {
            crate::setup_error::check_stat(&path, &stat, false)?;
            if stat.perm.is_none_or(|p| p & 0o077 != 0) {
                return Err(crate::setup_error::path_error(
                    "path_private",
                    &path,
                    "verify",
                ));
            }
            let mut existing = Zeroizing::new(Vec::new());
            sftp.open(Path::new(&path))
                .map_err(|_| "Cannot verify setup file")?
                .take((data.len() + 1) as u64)
                .read_to_end(&mut existing)
                .map_err(|_| "Cannot verify setup upload")?;
            if *existing == *data {
                continue;
            }
            // An interrupted upload can only be repaired before the manifest commits the kit.
            if sftp
                .stat(Path::new(&format!("{stage}/manifest.json")))
                .is_ok()
            {
                return Err(
                    "Staged setup differs from its saved receipt; inspect it on the server".into(),
                );
            }
            sftp.unlink(Path::new(&path))
                .map_err(|_| "Cannot repair incomplete setup upload")?;
        }
        let mut file = sftp
            .open_mode(
                Path::new(&path),
                OpenFlags::WRITE | OpenFlags::CREATE | OpenFlags::EXCLUSIVE,
                0o600,
                OpenType::File,
            )
            .map_err(|_| "Cannot create private setup file")?;
        file.write_all(&data)
            .map_err(|_| "Setup upload interrupted. Resume this setup; do not create another")?;
        file.fsync().map_err(|_| "Cannot persist setup upload")?;
    }
    if !python {
        session.set_timeout(600000);
        exec(session,"if ! command -v python3 >/dev/null; then DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3 >/dev/null 2>&1; fi")?;
        session.set_timeout(15000);
    }
    let mut channel = session
        .channel_session()
        .map_err(|_| "Cannot open installer channel")?;
    session.set_timeout(1000);
    channel
        .exec(&format!("python3 {stage}/install.py"))
        .map_err(|_| "Cannot start the server installer")?;
    channel
        .handle_extended_data(ssh2::ExtendedData::Merge)
        .map_err(|_| "Cannot read installer progress")?;
    let deadline = Instant::now() + Duration::from_secs(2400);
    let mut pending = Vec::new();
    let mut total = 0usize;
    let mut complete = false;
    let mut phase = "verify".to_string();
    let mut failure = None;
    loop {
        let mut buf = [0; 2048];
        match channel.read(&mut buf) {
            Ok(0)=>break,
            Ok(n)=>{
                total+=n; if total>1024*1024 { return Err("Installer output exceeded its limit; resume after inspecting the server".into()); }
                pending.extend_from_slice(&buf[..n]);
                while let Some(end)=pending.iter().position(|b| *b==b'\n') {
                    let line=String::from_utf8_lossy(&pending[..end]).trim().to_string(); pending.drain(..=end);
                    if let Some(stage)=line.strip_prefix("DOCKYARD_STAGE:") {
                        if PHASES.contains(&stage) { phase=stage.into(); let _=app.emit("setup-progress",json!({"phase":stage})); complete|=stage=="complete"; }
                    } else if let Some(error) = crate::setup_error::installer_line(&line, &phase) {
                        failure = Some(error);
                    }
                }
                if pending.len()>4096 { return Err("Installer progress line too large".into()); }
            },
            Err(e) if matches!(e.kind(),std::io::ErrorKind::TimedOut|std::io::ErrorKind::WouldBlock)=>{},
            Err(_)=>return Err("Setup connection was interrupted. Its credentials and receipt are saved; resume the same setup".into())
        }
        if Instant::now() > deadline {
            return Err("Setup timed out; resume the saved setup after checking the server".into());
        }
    }
    session.set_timeout(15000);
    channel
        .wait_close()
        .map_err(|_| "Setup outcome is uncertain; resume the saved setup")?;
    if !complete
        || channel
            .exit_status()
            .map_err(|_| "Setup status unavailable")?
            != 0
    {
        // Old saved receipts still run their original script. Diagnose its generic
        // path error from metadata without changing that script or saved credentials.
        preflight(session, false)?;
        return Err(failure.unwrap_or_else(|| crate::setup_error::fallback(&phase)));
    }
    Ok(())
}

pub struct Tunnel {
    stop: Arc<AtomicBool>,
    pub origin: String,
}
impl Drop for Tunnel {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
    }
}
fn connector(ssh: &SshIdentity) -> Result<SshSession, String> {
    let (session, _) = connect(&ssh.server_ip, ssh.port, Some(&ssh.host_sha256))?;
    session
        .userauth_pubkey_memory("dockyard-link", None, &ssh.private_key, None)
        .map_err(|_| {
            "Restricted SSH key was rejected. Check the connector user and SSH policy on the VPS"
        })?;
    Ok(session)
}
pub fn tunnel(ssh: &SshIdentity) -> Result<Tunnel, String> {
    // Prove the pinned host and connector authentication before exposing a loopback listener.
    connector(ssh)?;
    let listener =
        TcpListener::bind("127.0.0.1:0").map_err(|_| "Cannot open the private API tunnel")?;
    let origin = format!(
        "https://{}",
        listener
            .local_addr()
            .map_err(|_| "Tunnel address unavailable")?
    );
    listener
        .set_nonblocking(true)
        .map_err(|_| "Cannot configure the private tunnel")?;
    let stop = Arc::new(AtomicBool::new(false));
    let count = Arc::new(AtomicUsize::new(0));
    let stopping = stop.clone();
    let ssh = Arc::new(ssh.clone());
    std::thread::spawn(move || {
        while !stopping.load(Ordering::SeqCst) {
            match listener.accept() {
                Ok((tcp, _)) if count.load(Ordering::SeqCst) < 4 => {
                    count.fetch_add(1, Ordering::SeqCst);
                    let count = count.clone();
                    let stop = stopping.clone();
                    let ssh = ssh.clone();
                    std::thread::spawn(move || {
                        let _ = forward(tcp, &ssh, &stop);
                        count.fetch_sub(1, Ordering::SeqCst);
                    });
                }
                _ => std::thread::sleep(Duration::from_millis(20)),
            }
        }
    });
    Ok(Tunnel { stop, origin })
}
fn forward(mut tcp: TcpStream, ssh: &SshIdentity, stop: &AtomicBool) -> Result<(), String> {
    let session = connector(ssh)?;
    let mut channel = session
        .channel_direct_tcpip("127.0.0.1", 9123, None)
        .map_err(|_| "SSH forwarding refused")?;
    tcp.set_nonblocking(true)
        .map_err(|_| "Tunnel socket failed")?;
    session.set_blocking(false);
    let mut to_remote = Vec::new();
    let mut to_local = Vec::new();
    let mut local_eof = false;
    let mut remote_eof = false;
    let mut eof_sent = false;
    let mut active = Instant::now();
    let born = Instant::now();
    while !stop.load(Ordering::SeqCst)
        && active.elapsed() < Duration::from_secs(50)
        && born.elapsed() < Duration::from_secs(300)
    {
        let mut moved = false;
        let mut buf = [0; 16384];
        if !local_eof && to_remote.len() < 65536 {
            match tcp.read(&mut buf) {
                Ok(0) => local_eof = true,
                Ok(n) => {
                    to_remote.extend_from_slice(&buf[..n]);
                    moved = true
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                Err(_) => return Ok(()),
            }
        }
        if !to_remote.is_empty() {
            match channel.write(&to_remote) {
                Ok(n) if n > 0 => {
                    to_remote.drain(..n);
                    moved = true
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                _ => {}
            }
        }
        if local_eof && to_remote.is_empty() && !eof_sent && channel.send_eof().is_ok() {
            eof_sent = true;
        }
        if !remote_eof && to_local.len() < 65536 {
            match channel.read(&mut buf) {
                Ok(0) => remote_eof = true,
                Ok(n) => {
                    to_local.extend_from_slice(&buf[..n]);
                    moved = true
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                Err(_) => return Ok(()),
            }
        }
        if !to_local.is_empty() {
            match tcp.write(&to_local) {
                Ok(n) if n > 0 => {
                    to_local.drain(..n);
                    moved = true
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                _ => return Ok(()),
            }
        }
        if remote_eof && to_local.is_empty() {
            let _ = tcp.shutdown(std::net::Shutdown::Write);
            if local_eof {
                break;
            }
        }
        if moved {
            active = Instant::now();
        } else {
            std::thread::sleep(Duration::from_millis(5));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn test_plan() -> Plan {
        Plan {
            session: SshSession::new().unwrap(),
            request: Request {
                os: "ubuntu".into(),
                server_ip: "203.0.113.7".into(),
                ssh_port: 22,
                name: "Test VPS".into(),
                import_policy: false,
            },
            host: format!("SHA256:{}", STANDARD_NO_PAD.encode([0; 32])),
            version: "22.04".into(),
            docker: false,
            caddy: false,
            python: true,
            policy: None,
            id: uuid::Uuid::new_v4().simple().to_string(),
            created: Instant::now(),
        }
    }
    #[test]
    fn generated_kit_matches_saved_enrollment_and_survives_restart() {
        let (e, r) = material_with_binaries(&test_plan(), vec![1], vec![2]).unwrap();
        e.validate().unwrap();
        crate::tls::configuration(&e).unwrap();
        let bytes = Zeroizing::new(serde_json::to_vec(&r).unwrap());
        let restored: Receipt = serde_json::from_slice(&bytes).unwrap();
        restored.validate().unwrap();
        let file = |name: &str| {
            STANDARD
                .decode(
                    restored
                        .files
                        .iter()
                        .find(|f| f.name == name)
                        .unwrap()
                        .data
                        .as_ref()
                        .unwrap(),
                )
                .unwrap()
        };
        assert_eq!(
            file("desktop.key"),
            format!("{}\n", e.hmac_base64).as_bytes()
        );
        assert_ne!(file("fingerprint.key"), file("desktop.key"));
        let config: Value = serde_json::from_slice(&file("config.json")).unwrap();
        assert_eq!(
            config["keys"][0]["scopes"],
            json!([
                "compose.admin",
                "projects.write",
                "sites.write",
                "dns.write",
                "deploy.read",
                "deploy.logs",
                "deploy.execute",
                "deploy.environment",
                "deploy.rollback",
                "deploy.lifecycle",
                "deploy.stop"
            ])
        );
        assert!(config.get("cloudflare").is_none());
        let metadata: Value = serde_json::from_slice(&file("server.json")).unwrap();
        assert_eq!(metadata["server_ip"], e.ssh.as_ref().unwrap().server_ip);
        let mut tampered = restored.clone();
        tampered.files[0].name = "../outside".into();
        assert!(tampered.validate().is_err());
        let mut tampered = restored.clone();
        tampered
            .files
            .iter_mut()
            .find(|f| f.name == "server.key")
            .unwrap()
            .data = Some(STANDARD.encode("different"));
        assert!(tampered.validate().is_err());
    }
    #[test]
    fn setup_inputs_cannot_be_shell_or_network_destinations() {
        let mut r = Request {
            os: "ubuntu".into(),
            server_ip: "203.0.113.7".into(),
            ssh_port: 22,
            name: "My VPS".into(),
            import_policy: false,
        };
        assert!(r.validate().is_ok());
        for ip in [
            "example.com",
            "127.0.0.1; curl evil",
            "0.0.0.0",
            "224.0.0.1",
            "https://203.0.113.7",
        ] {
            r.server_ip = ip.into();
            assert!(r.validate().is_err());
        }
    }
    #[test]
    fn setup_receipts_reject_arbitrary_paths() {
        let r = Receipt {
            id: "../root".into(),
            server_ip: "127.0.0.1".into(),
            ssh_port: 22,
            host_sha256: "x".into(),
            files: vec![],
        };
        assert!(r.validate().is_err());
    }
    #[test]
    #[ignore = "starts a disposable local Docker/OpenSSH fixture; never uses a VPS or Keychain"]
    fn pinned_ssh_connector_and_bounded_tunnel() {
        use std::process::Command;
        struct Container(String);
        impl Drop for Container {
            fn drop(&mut self) {
                let _ = Command::new("docker").args(["rm", "-f", &self.0]).output();
            }
        }
        let name = format!("dockyard-ssh-test-{}", uuid::Uuid::new_v4().simple());
        let guard = Container(name.clone());
        let started = Command::new("docker")
            .args([
                "run",
                "--platform",
                "linux/amd64",
                "-d",
                "--name",
                &name,
                "-p",
                "127.0.0.1::22",
                "dockyard-ssh-fixture:local",
            ])
            .output()
            .unwrap();
        assert!(started.status.success(), "SSH fixture must be built first");
        let port = Command::new("docker")
            .args(["port", &name, "22/tcp"])
            .output()
            .unwrap();
        let port: u16 = String::from_utf8(port.stdout)
            .unwrap()
            .trim()
            .rsplit(':')
            .next()
            .unwrap()
            .parse()
            .unwrap();
        let mut root = None;
        for _ in 0..50 {
            if let Ok(connection) = connect("127.0.0.1", port, None) {
                root = Some(connection);
                break;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        let (session, pin) = root.expect("local fixture SSH must start");
        assert!(connect("127.0.0.1", port, Some("SHA256:wrong")).is_err());
        session
            .userauth_password("root", "dockyard-fixture-only")
            .unwrap();
        // Missing future install directories are allowed, but existing ancestors are checked.
        preflight(&session, false).unwrap();
        // Validate the actual wizard credentials/root policy with the bundled Go agent.
        // Docker is a protected placeholder here: -check never invokes containers.
        exec(&session, "python3 -c 'from pathlib import Path; import os; [Path(p).mkdir(parents=True,exist_ok=True) for p in [\"/etc/dockyard/tls\",\"/etc/dockyard/docker\",\"/var/lib/dockyard\",\"/docker\",\"/etc/caddy/dockyard\"]]; Path(\"/usr/bin/docker\").write_text(\"fixture-check-only\"); Path(\"/etc/caddy/Caddyfile\").write_text(\"# fixture\\n\"); os.chmod(\"/usr/bin/docker\",0o755)' ").unwrap();
        preflight(&session, true).unwrap();
        exec(&session, "chown 1234 /etc/caddy").unwrap();
        let error = preflight(&session, true).unwrap_err();
        assert!(error.contains("sudo chown root /etc/caddy"));
        assert_eq!(
            session
                .sftp()
                .unwrap()
                .lstat(Path::new("/etc/caddy"))
                .unwrap()
                .uid,
            Some(1234),
            "inspection must not change ownership"
        );
        exec(&session, "chown root /etc/caddy").unwrap();
        preflight(&session, true).unwrap();
        let resource_root = Path::new(env!("CARGO_MANIFEST_DIR")).join("resources/ubuntu");
        let agent =
            std::fs::read(resource_root.join("dockyard")).expect("bundle server binaries first");
        let ctl = std::fs::read(resource_root.join("dockyardctl")).unwrap();
        let (_, receipt) = material_with_binaries(&test_plan(), agent.clone(), ctl).unwrap();
        let sftp = session.sftp().unwrap();
        for (source, destination) in [
            ("server.crt", "/etc/dockyard/tls/server.crt"),
            ("server.key", "/etc/dockyard/tls/server.key"),
            ("control-ca.crt", "/etc/dockyard/tls/control-ca.crt"),
            ("desktop.key", "/etc/dockyard/desktop-01.key"),
            ("fingerprint.key", "/etc/dockyard/fingerprint.key"),
            ("config.json", "/etc/dockyard/config.json"),
        ] {
            let file = receipt.files.iter().find(|f| f.name == source).unwrap();
            let data = Zeroizing::new(STANDARD.decode(file.data.as_ref().unwrap()).unwrap());
            let mut remote = sftp
                .open_mode(
                    Path::new(destination),
                    OpenFlags::WRITE | OpenFlags::CREATE | OpenFlags::EXCLUSIVE,
                    0o600,
                    OpenType::File,
                )
                .unwrap();
            remote.write_all(&data).unwrap();
            remote.fsync().unwrap();
        }
        let mut remote = sftp
            .open_mode(
                Path::new("/usr/local/bin/dockyard"),
                OpenFlags::WRITE | OpenFlags::CREATE | OpenFlags::EXCLUSIVE,
                0o755,
                OpenType::File,
            )
            .unwrap();
        remote.write_all(&agent).unwrap();
        remote.fsync().unwrap();
        drop(remote);
        let mut check = session.channel_session().unwrap();
        check
            .exec("/usr/local/bin/dockyard -config /etc/dockyard/config.json -check 2>&1")
            .unwrap();
        let mut check_output = String::new();
        check.read_to_string(&mut check_output).unwrap();
        check.wait_close().unwrap();
        assert_eq!(
            check.exit_status().unwrap(),
            0,
            "generated fixture policy: {check_output}"
        );
        drop(sftp);
        let key = ssh_key::PrivateKey::random(
            &mut ssh_key::rand_core::OsRng,
            ssh_key::Algorithm::Ed25519,
        )
        .unwrap();
        let public = key.public_key().to_openssh().unwrap();
        let mut ch = session.channel_session().unwrap();
        ch.exec("python3 -c 'import os,sys; p=\"/var/lib/dockyard-link/.ssh/authorized_keys\"; data=sys.stdin.read(); f=open(p,\"w\"); f.write(\"restrict,port-forwarding,permitopen=\\\"127.0.0.1:9123\\\" \"+data+\"\\n\"); f.close(); os.chmod(p,0o644)'").unwrap();
        ch.write_all(public.as_bytes()).unwrap();
        ch.send_eof().unwrap();
        let mut upload_output = String::new();
        ch.read_to_string(&mut upload_output).unwrap();
        ch.wait_close().unwrap();
        assert_eq!(ch.exit_status().unwrap(), 0);
        let ssh = SshIdentity {
            server_ip: "127.0.0.1".into(),
            port,
            host_sha256: pin,
            private_key: key.to_openssh(ssh_key::LineEnding::LF).unwrap().to_string(),
        };
        let connected = connector(&ssh).unwrap();
        assert!(
            connected
                .channel_direct_tcpip("127.0.0.1", 22, None)
                .is_err(),
            "connector cannot reach other ports"
        );
        let mut shell = connected.channel_session().unwrap();
        shell.exec("id").unwrap();
        let mut output = String::new();
        shell.read_to_string(&mut output).unwrap();
        shell.wait_close().unwrap();
        assert_ne!(shell.exit_status().unwrap(), 0);
        assert!(!output.contains("uid="));
        let t = tunnel(&ssh).unwrap();
        for _ in 0..2 {
            let mut tcp = TcpStream::connect(t.origin.strip_prefix("https://").unwrap()).unwrap();
            tcp.set_read_timeout(Some(Duration::from_secs(20))).unwrap();
            tcp.write_all(b"GET /fixture HTTP/1.1\r\nHost: localhost\r\n\r\n")
                .unwrap();
            tcp.shutdown(std::net::Shutdown::Write).unwrap();
            let mut response = Vec::new();
            tcp.read_to_end(&mut response).unwrap();
            assert!(response.starts_with(b"HTTP/1.1 200 OK"));
            assert!(response.len() > 2 * 1024 * 1024);
            assert!(response.ends_with(b"dockyard-fixture-"));
        }
        let address = t.origin.clone();
        drop(t);
        std::thread::sleep(Duration::from_millis(100));
        assert!(
            TcpStream::connect(address.strip_prefix("https://").unwrap()).is_err(),
            "locking removes the loopback listener"
        );
        drop(guard);
    }
}
