//! A separate root SSH connection. The API-only account never gains a shell.
use base64::{engine::general_purpose::STANDARD, Engine};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use ssh2::{Channel, Session};
use std::{
    io::{Read, Write},
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc, Arc, Mutex,
    },
    time::{Duration, Instant, SystemTime},
};
use zeroize::Zeroizing;

const BUFFER: usize = 64 * 1024;
const INPUT: usize = 8 * 1024;
#[derive(Clone)]
pub struct Lease {
    pub generation: Arc<AtomicU64>,
    pub expected: u64,
    pub expires: Instant,
    pub expires_wall: SystemTime,
}
impl Lease {
    pub fn valid(&self) -> bool {
        self.generation.load(Ordering::SeqCst) == self.expected
            && !crate::deadline_passed(self.expires, self.expires_wall)
    }
    pub fn check(&self) -> Result<(), String> {
        if self.valid() {
            Ok(())
        } else {
            Err("SESSION_LOCKED".into())
        }
    }
}
pub fn account(ip: &str, port: u16, pin: &str) -> String {
    hex::encode(Sha256::digest(format!("root\n{ip}\n{port}\n{pin}")))
}
#[derive(Serialize, Deserialize, Debug)]
pub struct Container {
    pub id: String,
    pub name: String,
    pub image: String,
    pub status: String,
}
fn container_id(id: &str) -> bool {
    id.len() == 64
        && id
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
pub fn size(cols: u32, rows: u32) -> Result<(), String> {
    if (10..=500).contains(&cols) && (5..=240).contains(&rows) {
        Ok(())
    } else {
        Err("Terminal dimensions are outside supported limits".into())
    }
}
fn target(container: Option<&str>, shell: &str) -> Result<Option<String>, String> {
    if !matches!(shell, "/bin/sh" | "/bin/bash") {
        return Err("Choose sh or bash".into());
    }
    match container {
        None => Ok(None),
        Some(id) if container_id(id) => Ok(Some(format!("docker exec -it {id} {shell}"))),
        _ => Err("Select a running container using its full Docker ID".into()),
    }
}
#[derive(Serialize)]
pub struct Connected {
    pub id: String,
    pub containers: Vec<Container>,
    pub container_warning: Option<String>,
    pub password_saved: bool,
}
#[derive(Serialize)]
pub struct Output {
    pub data: String,
    pub closed: bool,
    pub reason: Option<String>,
}
#[derive(Default)]
struct Stream {
    bytes: Zeroizing<Vec<u8>>,
    closed: bool,
    reason: Option<String>,
}
enum Command {
    Start(
        Option<String>,
        String,
        u32,
        u32,
        mpsc::SyncSender<Result<(), String>>,
    ),
    Input(Zeroizing<Vec<u8>>),
    Resize(u32, u32),
}
pub struct Handle {
    pub id: String,
    tx: mpsc::SyncSender<Command>,
    stream: Arc<Mutex<Stream>>,
    alive: Arc<AtomicBool>,
    lease: Lease,
}
impl Drop for Handle {
    fn drop(&mut self) {
        self.alive.store(false, Ordering::SeqCst);
    }
}
impl Handle {
    pub fn matches(&self, id: &str) -> Result<(), String> {
        self.lease.check()?;
        if self.id == id && id.len() == 36 {
            Ok(())
        } else {
            Err("Terminal connection is no longer open".into())
        }
    }
    pub fn start(
        &self,
        container: Option<String>,
        shell: String,
        cols: u32,
        rows: u32,
    ) -> Result<mpsc::Receiver<Result<(), String>>, String> {
        target(container.as_deref(), &shell)?;
        size(cols, rows)?;
        let (tx, rx) = mpsc::sync_channel(1);
        self.tx
            .try_send(Command::Start(container, shell, cols, rows, tx))
            .map_err(|_| "Terminal is busy or disconnected")?;
        Ok(rx)
    }
    pub fn input(&self, data: &str) -> Result<(), String> {
        if data.len() > INPUT * 4 / 3 + 4 {
            return Err("Paste at most 8 KiB at a time".into());
        }
        let bytes = Zeroizing::new(
            STANDARD
                .decode(data)
                .map_err(|_| "Invalid terminal input")?,
        );
        if bytes.len() > INPUT {
            return Err("Paste at most 8 KiB at a time".into());
        }
        self.tx
            .try_send(Command::Input(bytes))
            .map_err(|_| "Terminal input queue is full or disconnected".into())
    }
    pub fn resize(&self, cols: u32, rows: u32) -> Result<(), String> {
        size(cols, rows)?;
        self.tx
            .try_send(Command::Resize(cols, rows))
            .map_err(|_| "Terminal is busy or disconnected".into())
    }
    pub fn poll(&self) -> Output {
        let mut stream = self.stream.lock().expect("terminal stream poisoned");
        let bytes = std::mem::take(&mut stream.bytes);
        Output {
            data: STANDARD.encode(&*bytes),
            closed: stream.closed,
            reason: stream.reason.clone(),
        }
    }
}

fn would_block(e: &std::io::Error) -> bool {
    e.kind() == std::io::ErrorKind::WouldBlock
}
fn containers(session: &Session, lease: &Lease) -> Result<Vec<Container>, String> {
    session.set_timeout(3000);
    let mut channel = session
        .channel_session()
        .map_err(|_| "Could not list Docker containers")?;
    channel
        .exec("docker ps --no-trunc --format '{{json .}}'")
        .map_err(|_| "Could not list Docker containers")?;
    session.set_blocking(false);
    let limit = Instant::now() + Duration::from_secs(10);
    let mut bytes = Zeroizing::new(Vec::new());
    let mut chunk = [0u8; 8192];
    loop {
        lease.check()?;
        if Instant::now() >= limit {
            return Err("Docker container listing timed out".into());
        }
        match channel.read(&mut chunk) {
            Ok(0) if channel.eof() => break,
            Ok(n) => {
                if bytes.len() + n > 128 * 1024 {
                    return Err("Docker container listing is too large".into());
                }
                bytes.extend_from_slice(&chunk[..n]);
            }
            Err(e) if would_block(&e) => {}
            Err(_) => return Err("Could not read Docker container listing".into()),
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    if channel.exit_status().unwrap_or(1) != 0 {
        return Err("Docker could not list running containers".into());
    }
    let text =
        std::str::from_utf8(&bytes).map_err(|_| "Docker returned invalid container information")?;
    let mut out = Vec::new();
    for line in text.lines() {
        let row: serde_json::Value = serde_json::from_str(line)
            .map_err(|_| "Docker returned invalid container information")?;
        let field = |key| {
            row.get(key)
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .chars()
                .filter(|c| !c.is_control())
                .take(256)
                .collect::<String>()
        };
        let id = field("ID");
        if !container_id(&id) || out.len() >= 1000 {
            return Err("Docker returned invalid container information".into());
        }
        out.push(Container {
            id,
            name: field("Names"),
            image: field("Image"),
            status: field("Status"),
        });
    }
    Ok(out)
}
pub fn spawn(
    session: Session,
    lease: Lease,
    password_saved: bool,
) -> Result<(Handle, Connected), String> {
    let catalog = containers(&session, &lease);
    lease.check()?;
    session.set_blocking(false);
    let (tx, rx) = mpsc::sync_channel(16);
    let stream = Arc::new(Mutex::new(Stream::default()));
    let alive = Arc::new(AtomicBool::new(true));
    let id = uuid::Uuid::new_v4().to_string();
    let handle = Handle {
        id: id.clone(),
        tx,
        stream: stream.clone(),
        alive: alive.clone(),
        lease: lease.clone(),
    };
    std::thread::Builder::new()
        .name("dockyard-terminal".into())
        .spawn(move || {
            let reason = run(session, lease, alive, &stream, rx);
            let mut output = stream.lock().expect("terminal stream poisoned");
            output.closed = true;
            output.reason = Some(reason);
        })
        .map_err(|_| "Could not start the SSH terminal worker")?;
    let (containers, warning) = match catalog {
        Ok(c) => (c, None),
        Err(e) => (Vec::new(), Some(e)),
    };
    Ok((
        handle,
        Connected {
            id,
            containers,
            container_warning: warning,
            password_saved,
        },
    ))
}
fn run(
    session: Session,
    lease: Lease,
    alive: Arc<AtomicBool>,
    stream: &Arc<Mutex<Stream>>,
    rx: mpsc::Receiver<Command>,
) -> String {
    let mut channel: Option<Channel> = None;
    let mut input = Zeroizing::new(Vec::new());
    let mut offset = 0;
    let mut chunk = [0u8; 8192];
    let reason = 'worker: loop {
        if !lease.valid() {
            break "Dockyard locked. Unlock with Touch ID to reconnect.";
        }
        if !alive.load(Ordering::SeqCst) {
            break "Disconnected";
        }
        if input.is_empty() {
            match rx.try_recv() {
                Ok(Command::Start(container, shell, cols, rows, reply)) => {
                    let result = (|| {
                        if channel.is_some() {
                            return Err("Disconnect before opening another shell".into());
                        }
                        let command = target(container.as_deref(), &shell)?;
                        session.set_blocking(true);
                        session.set_timeout(3000);
                        let mut opened = session
                            .channel_session()
                            .map_err(|_| "Could not open an SSH shell")?;
                        opened
                            .handle_extended_data(ssh2::ExtendedData::Merge)
                            .map_err(|_| "Could not prepare terminal output")?;
                        opened
                            .request_pty("xterm-256color", None, Some((cols, rows, 0, 0)))
                            .map_err(|_| "Server refused an interactive terminal")?;
                        if let Some(command) = command {
                            opened
                                .exec(&command)
                                .map_err(|_| "Could not open the container shell")?;
                        } else {
                            opened.shell().map_err(|_| "Server refused a root shell")?;
                        }
                        lease.check()?;
                        channel = Some(opened);
                        Ok(())
                    })();
                    session.set_blocking(false);
                    let failed = result.is_err();
                    let _ = reply.send(result);
                    if failed {
                        break "The shell could not be opened. Reconnect to try again.";
                    }
                }
                Ok(Command::Input(data)) => {
                    input = data;
                    offset = 0;
                }
                Ok(Command::Resize(cols, rows)) => {
                    if let Some(ch) = channel.as_mut() {
                        // A resize is an SSH control packet. Bound its blocking wait, then restore streaming.
                        session.set_blocking(true);
                        session.set_timeout(1000);
                        let resized = ch.request_pty_size(cols, rows, None, None);
                        session.set_blocking(false);
                        if resized.is_err() {
                            break "Terminal resize failed. Reconnect to continue.";
                        }
                    }
                }
                Err(mpsc::TryRecvError::Disconnected) => break "Disconnected",
                Err(mpsc::TryRecvError::Empty) => {}
            }
        }
        if let Some(ch) = channel.as_mut() {
            if !input.is_empty() {
                match ch.write(&input[offset..]) {
                    Ok(0) => break "SSH terminal disconnected",
                    Ok(n) => {
                        offset += n;
                        if offset == input.len() {
                            input = Zeroizing::new(Vec::new());
                            offset = 0;
                        }
                    }
                    Err(e) if would_block(&e) => {}
                    Err(_) => break "SSH terminal input failed",
                }
            }
            let room =
                BUFFER.saturating_sub(stream.lock().expect("terminal stream poisoned").bytes.len());
            if room > 0 {
                let count = room.min(chunk.len());
                match ch.read(&mut chunk[..count]) {
                    Ok(n) => {
                        stream
                            .lock()
                            .expect("terminal stream poisoned")
                            .bytes
                            .extend_from_slice(&chunk[..n]);
                        if n == 0 && ch.eof() {
                            break 'worker "Shell exited";
                        }
                    }
                    Err(e) if would_block(&e) => {}
                    Err(_) => break "SSH terminal output failed",
                }
            }
        }
        std::thread::sleep(Duration::from_millis(20));
    };
    if let Some(mut ch) = channel {
        let _ = ch.close();
    }
    let _ = session.disconnect(None, "Dockyard terminal closed", None);
    reason.into()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn commands_reject_injection_and_invalid_dimensions() {
        for id in ["abc", "$(id)", "--privileged", &"a".repeat(65)] {
            assert!(target(Some(id), "/bin/sh").is_err());
        }
        let id = "a".repeat(64);
        assert_eq!(
            target(Some(&id), "/bin/sh").unwrap().unwrap(),
            format!("docker exec -it {id} /bin/sh")
        );
        assert!(target(Some(&id), "/bin/sh; id").is_err());
        assert!(target(None, "/bin/bash").unwrap().is_none());
        assert!(size(80, 24).is_ok());
        assert!(size(u32::MAX, 24).is_err());
        assert!(size(80, 0).is_err());
    }
    #[test]
    fn credentials_are_bound_to_pinned_endpoint_and_lease_is_revoked() {
        assert_ne!(account("127.0.0.1", 22, "a"), account("127.0.0.1", 22, "b"));
        assert_ne!(account("127.0.0.1", 22, "a"), account("127.0.0.1", 23, "a"));
        let generation = Arc::new(AtomicU64::new(7));
        let lease = Lease {
            generation: generation.clone(),
            expected: 7,
            expires: Instant::now() + Duration::from_secs(30),
            expires_wall: SystemTime::now() + Duration::from_secs(30),
        };
        assert!(lease.valid());
        generation.fetch_add(1, Ordering::SeqCst);
        assert!(!lease.valid());
        let expired = Lease {
            expected: 8,
            expires_wall: SystemTime::now() - Duration::from_secs(1),
            ..lease
        };
        assert!(!expired.valid());
    }
}

#[cfg(test)]
mod ssh_tests {
    use super::*;
    use std::process::Command;
    #[test]
    #[ignore = "disposable local OpenSSH/Docker CLI fixture; no VPS or Keychain"]
    fn interactive_pty_container_resize_backpressure_and_lock() {
        struct Fixture(String);
        impl Drop for Fixture {
            fn drop(&mut self) {
                let _ = Command::new("docker")
                    .args(["--context", "desktop-linux", "rm", "-f", &self.0])
                    .output();
            }
        }
        let name = format!("dockyard-terminal-test-{}", uuid::Uuid::new_v4().simple());
        let _guard = Fixture(name.clone());
        let docker = |args: &[&str]| {
            Command::new("docker")
                .args(["--context", "desktop-linux"])
                .args(args)
                .output()
                .unwrap()
        };
        assert!(docker(&[
            "run",
            "--platform",
            "linux/amd64",
            "-d",
            "--name",
            &name,
            "-p",
            "127.0.0.1::22",
            "dockyard-ssh-fixture:local"
        ])
        .status
        .success());
        let port: u16 = String::from_utf8(docker(&["port", &name, "22/tcp"]).stdout)
            .unwrap()
            .trim()
            .rsplit(':')
            .next()
            .unwrap()
            .parse()
            .unwrap();
        let mut ready = None;
        for _ in 0..50 {
            if let Ok(pair) = crate::setup::connect("127.0.0.1", port, None) {
                ready = Some(pair);
                break;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        let (ssh, pin) = ready.expect("SSH fixture available");
        ssh.userauth_password("root", "dockyard-fixture-only")
            .unwrap();
        assert!(crate::setup::connect("127.0.0.1", port, Some("SHA256:wrong")).is_err());
        let container = "a".repeat(64);
        // The fixture emulates Docker's two fixed CLI commands. No host Docker socket is mounted.
        let script = format!(
            r#"#!/bin/sh
if [ "$1" = ps ]; then
    printf '%s\n' '{{"ID":"{container}","Names":"fixture-app","Image":"fixture:local","Status":"Up"}}'
elif [ "$1" = exec ] && [ "$2" = -it ] && [ "$3" = {container} ] && [ "$4" = /bin/sh ] && [ "$#" = 4 ]; then
    exec /bin/sh
else
    exit 1
fi
"#
        );
        let sftp = ssh.sftp().unwrap();
        let mut file = sftp
            .open_mode(
                std::path::Path::new("/usr/local/bin/docker"),
                ssh2::OpenFlags::CREATE | ssh2::OpenFlags::WRITE | ssh2::OpenFlags::TRUNCATE,
                0o755,
                ssh2::OpenType::File,
            )
            .unwrap();
        file.write_all(script.as_bytes()).unwrap();
        file.fsync().unwrap();
        drop(file);
        drop(sftp);
        let generation = Arc::new(AtomicU64::new(1));
        let lease = || Lease {
            generation: generation.clone(),
            expected: generation.load(Ordering::SeqCst),
            expires: Instant::now() + Duration::from_secs(60),
            expires_wall: SystemTime::now() + Duration::from_secs(60),
        };
        let (root, catalog) = spawn(ssh, lease(), false).unwrap();
        assert!(
            catalog.container_warning.is_none(),
            "{:?}",
            catalog.container_warning
        );
        assert_eq!(catalog.containers[0].id, container);
        root.start(None, "/bin/sh".into(), 80, 24)
            .unwrap()
            .recv_timeout(Duration::from_secs(12))
            .unwrap()
            .unwrap();
        root.input(&STANDARD.encode(b"stty -echo; printf '\\nROOT_OK\\n'; stty size\r"))
            .unwrap();
        let root_output = read_until(&root, "24 80");
        assert!(root_output.contains("\r\nROOT_OK\r\n"));
        root.resize(120, 40).unwrap();
        root.input(&STANDARD.encode(b"stty size\r")).unwrap();
        read_until(&root, "40 120");
        assert!(root.input(&STANDARD.encode(vec![b'x'; INPUT + 1])).is_err());
        root.input(&STANDARD.encode(b"python3 -c 'import sys;sys.stdout.write(\"x\"*2000000)'\r"))
            .unwrap();
        std::thread::sleep(Duration::from_millis(250));
        assert!(root.stream.lock().unwrap().bytes.len() <= BUFFER);
        generation.fetch_add(1, Ordering::SeqCst);
        let limit = Instant::now() + Duration::from_secs(3);
        while !root.poll().closed && Instant::now() < limit {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(
            root.poll().closed,
            "lock must stop streaming even with backpressure"
        );
        assert_eq!(root.matches(&root.id).unwrap_err(), "SESSION_LOCKED");
        drop(root);
        let (ssh, _) = crate::setup::connect("127.0.0.1", port, Some(&pin)).unwrap();
        ssh.userauth_password("root", "dockyard-fixture-only")
            .unwrap();
        let (inside, _) = spawn(ssh, lease(), false).unwrap();
        inside
            .start(Some(container), "/bin/sh".into(), 80, 24)
            .unwrap()
            .recv_timeout(Duration::from_secs(12))
            .unwrap()
            .unwrap();
        inside
            .input(&STANDARD.encode(b"stty -echo; printf '\\nCONTAINER_OK\\n'\r"))
            .unwrap();
        assert!(read_until(&inside, "\r\nCONTAINER_OK\r\n").contains("CONTAINER_OK"));
        inside.input(&STANDARD.encode(b"exit\r")).unwrap();
        let limit = Instant::now() + Duration::from_secs(3);
        while !inside.poll().closed && Instant::now() < limit {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(inside.poll().closed);
    }
    #[test]
    #[ignore = "starts a disposable loopback SSH protocol fixture; no OS commands, Docker or Keychain"]
    fn loopback_streaming_resize_container_and_revocation() {
        use std::io::{BufRead, BufReader};
        use std::process::Stdio;
        struct Fixture(std::process::Child, std::path::PathBuf);
        impl Drop for Fixture {
            fn drop(&mut self) {
                let _ = self.0.kill();
                let _ = self.0.wait();
                let _ = std::fs::remove_file(&self.1);
            }
        }
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../tests/terminal");
        let binary = std::env::temp_dir().join(format!(
            "dockyard-terminal-fixture-{}",
            uuid::Uuid::new_v4()
        ));
        let built = Command::new("go")
            .args(["build", "-o"])
            .arg(&binary)
            .arg(".")
            .current_dir(path)
            .env("GOCACHE", "/private/tmp/dockyard-go-cache")
            .env("GOMODCACHE", "/private/tmp/dockyard-terminal-go-mod-cache")
            .env("GOPROXY", "off")
            .env("GOSUMDB", "off")
            .output()
            .unwrap();
        assert!(
            built.status.success(),
            "fixture build: {}",
            String::from_utf8_lossy(&built.stderr)
        );
        let mut fixture = Fixture(
            Command::new(&binary)
                .stdout(Stdio::piped())
                .spawn()
                .unwrap(),
            binary,
        );
        let mut address = String::new();
        BufReader::new(fixture.0.stdout.take().unwrap())
            .read_line(&mut address)
            .unwrap();
        let port: u16 = address.trim().rsplit(':').next().unwrap().parse().unwrap();
        let (ssh, pin) = crate::setup::connect("127.0.0.1", port, None).unwrap();
        assert!(crate::setup::connect("127.0.0.1", port, Some("SHA256:wrong")).is_err());
        ssh.userauth_password("root", "fixture-only").unwrap();
        let generation = Arc::new(AtomicU64::new(1));
        let lease = || Lease {
            generation: generation.clone(),
            expected: generation.load(Ordering::SeqCst),
            expires: Instant::now() + Duration::from_secs(60),
            expires_wall: SystemTime::now() + Duration::from_secs(60),
        };
        let (root, catalog) = spawn(ssh, lease(), false).unwrap();
        assert!(
            catalog.container_warning.is_none(),
            "{:?}",
            catalog.container_warning
        );
        assert_eq!(catalog.containers.len(), 1);
        root.start(None, "/bin/sh".into(), 80, 24)
            .unwrap()
            .recv_timeout(Duration::from_secs(12))
            .unwrap()
            .unwrap();
        root.input(&STANDARD.encode(b"marker\rsize\r")).unwrap();
        let output = read_until(&root, "24 80");
        assert!(output.contains("ROOT_OK"));
        root.resize(120, 40).unwrap();
        root.input(&STANDARD.encode(b"size\r")).unwrap();
        read_until(&root, "40 120");
        assert!(root.input(&STANDARD.encode(vec![b'x'; INPUT + 1])).is_err());
        root.input(&STANDARD.encode(b"flood\r")).unwrap();
        std::thread::sleep(Duration::from_millis(250));
        assert!(root.stream.lock().unwrap().bytes.len() <= BUFFER);
        generation.fetch_add(1, Ordering::SeqCst);
        let limit = Instant::now() + Duration::from_secs(3);
        while !root.poll().closed && Instant::now() < limit {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(root.poll().closed);
        assert_eq!(root.matches(&root.id).unwrap_err(), "SESSION_LOCKED");
        drop(root);
        let (ssh, _) = crate::setup::connect("127.0.0.1", port, Some(&pin)).unwrap();
        ssh.userauth_password("root", "fixture-only").unwrap();
        let (inside, _) = spawn(ssh, lease(), false).unwrap();
        inside
            .start(Some("a".repeat(64)), "/bin/sh".into(), 80, 24)
            .unwrap()
            .recv_timeout(Duration::from_secs(12))
            .unwrap()
            .unwrap();
        inside.input(&STANDARD.encode(b"marker\r")).unwrap();
        read_until(&inside, "CONTAINER_OK");
        inside.input(&STANDARD.encode(b"exit\r")).unwrap();
        let limit = Instant::now() + Duration::from_secs(3);
        while !inside.poll().closed && Instant::now() < limit {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(inside.poll().closed);
    }
    fn read_until(handle: &Handle, marker: &str) -> String {
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut text = String::new();
        while Instant::now() < deadline {
            let output = handle.poll();
            text.push_str(&String::from_utf8_lossy(
                &STANDARD.decode(output.data).unwrap(),
            ));
            if text.contains(marker) {
                return text;
            }
            assert!(
                !output.closed,
                "terminal closed: {:?}, {text}",
                output.reason
            );
            std::thread::sleep(Duration::from_millis(20));
        }
        panic!("terminal marker {marker:?} missing: {text}");
    }
}
