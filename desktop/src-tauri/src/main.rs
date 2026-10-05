#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
#![allow(unexpected_cfgs)] // objc 0.2 macros predate rustc check-cfg.
#[cfg(not(target_os = "macos"))]
compile_error!("Dockyard Desktop currently requires macOS Keychain and LocalAuthentication");
mod native;
mod auth_window;
mod credential_cache;
mod protocol;
mod providers;
mod setup;
mod setup_error;
mod terminal;
mod tls;
mod updater;

use protocol::{Enrollment, Mutation, Operation, Read};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc, Mutex as SyncMutex,
    },
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
use tauri::{Emitter, Manager, State};
use tokio::sync::Mutex;
use zeroize::{Zeroize, Zeroizing};

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Saved {
    enrollment: Enrollment,
    pending: Option<Pending>,
    jobs: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    setup: Option<setup::Receipt>,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Pending {
    // Legacy saved writes without this field must be treated as already attempted.
    #[serde(default = "default_attempted")]
    attempted: bool,
    operation: Operation,
    job_id: Option<String>,
    // Unix seconds of the latest send. Missing on older saves (sent long ago).
    #[serde(default)]
    sent_at: Option<u64>,
}
fn default_attempted() -> bool {
    true
}
struct Session {
    generation: u64,
    saved: Saved,
    client: reqwest::Client,
    secret: Zeroizing<Vec<u8>>,
    api_origin: String,
    _tunnel: Option<setup::Tunnel>,
}
const AWAY_TIMEOUT: Duration = Duration::from_secs(5 * 60);

#[derive(Default)]
struct AwayTimer {
    prompt_epoch: u64,
    last_sample: Option<(Instant, SystemTime)>,
    deadline: Option<(Instant, SystemTime)>,
    locked: bool,
}
impl AwayTimer {
    fn sample(
        &mut self,
        active: bool,
        prompt_active: bool,
        epoch: u64,
        now: Instant,
        wall: SystemTime,
    ) -> bool {
        if prompt_active || self.prompt_epoch != epoch {
            *self = Self {
                prompt_epoch: epoch,
                ..Self::default()
            };
        }
        if prompt_active {
            return false;
        }
        self.observe(active, now, wall)
    }
    fn observe(&mut self, focused: bool, now: Instant, wall: SystemTime) -> bool {
        // A long polling gap means the Mac slept or the app was suspended.
        let suspended = self.last_sample.is_some_and(|(mono, real)| {
            now.saturating_duration_since(mono) >= AWAY_TIMEOUT
                || wall
                    .duration_since(real)
                    .is_ok_and(|gap| gap >= AWAY_TIMEOUT)
        });
        self.last_sample = Some((now, wall));
        let expired = !self.locked
            && (suspended
                || self
                    .deadline
                    .is_some_and(|(mono, real)| now >= mono || wall >= real));
        if expired {
            self.deadline = None;
            self.locked = true;
        }
        if focused {
            self.deadline = None;
            self.locked = false;
        } else if self.deadline.is_none() && !self.locked {
            let grace = AWAY_TIMEOUT;
            self.deadline = Some((now + grace, wall + grace));
        }
        expired
    }
}
#[derive(Clone, Serialize)]
struct Profile {
    name: String,
    origin: String,
    server_id: String,
    key_id: String,
    actor_id: String,
    server_certificate_sha256: String,
    server_ip: Option<String>,
    ssh_port: Option<u16>,
    ssh_fingerprint: Option<String>,
}
struct Inner {
    generation: Arc<AtomicU64>,
    session: Option<Session>,
    profile: Option<Profile>,
}
#[derive(Clone)]
struct Control {
    generation: Arc<AtomicU64>,
    inner: Arc<Mutex<Inner>>,
    native_prompt: Arc<AtomicBool>,
    provider_gate: Arc<Mutex<()>>,
    setup: Arc<Mutex<Option<setup::Plan>>>,
    away: Arc<SyncMutex<AwayTimer>>,
    terminal: Arc<SyncMutex<Option<terminal::Handle>>>,
}
impl Control {
    fn new() -> Self {
        let generation = Arc::new(AtomicU64::new(0));
        Self {
            generation: generation.clone(),
            inner: Arc::new(Mutex::new(Inner {
                generation,
                session: None,
                profile: None,
            })),
            native_prompt: Arc::new(AtomicBool::new(false)),
            provider_gate: Arc::new(Mutex::new(())),
            setup: Arc::new(Mutex::new(None)),
            away: Arc::new(SyncMutex::new(AwayTimer::default())),
            terminal: Arc::new(SyncMutex::new(None)),
        }
    }
    async fn clear(&self) {
        native::clear_authentication();
        self.generation.fetch_add(1, Ordering::SeqCst);
        self.terminal.lock().expect("terminal poisoned").take();
        self.inner.lock().await.session = None;
        self.setup.lock().await.take();
    }
}
fn observe_focus(c: &Control, app: &tauri::AppHandle, focused: bool) {
    let expired = {
        let mut timer = c.away.lock().expect("focus timer poisoned");
        let (prompt_active, epoch) = native::prompt_state();
        timer.sample(
            focused,
            prompt_active,
            epoch,
            Instant::now(),
            SystemTime::now(),
        )
    };
    if expired {
        native::clear_authentication();
        // Revoke immediately even when an API call holds the async session mutex.
        let revoked = c.generation.fetch_add(1, Ordering::SeqCst) + 1;
        let _ = app.emit("session-locked", ());
        let c = c.clone();
        tauri::async_runtime::spawn(async move {
            let mut inner = c.inner.lock().await;
            if inner
                .session
                .as_ref()
                .is_some_and(|s| s.generation < revoked)
            {
                inner.session = None;
            }
            drop(inner);
            let mut plan = c.setup.lock().await;
            if c.generation.load(Ordering::SeqCst) == revoked {
                plan.take();
            }
        });
    }
}
fn profile(e: &Enrollment) -> Profile {
    Profile {
        name: e.name.clone(),
        origin: e.origin.clone(),
        server_id: e.server_id.clone(),
        key_id: e.key_id.clone(),
        actor_id: e.actor_id.clone(),
        server_certificate_sha256: e.server_certificate_sha256.clone(),
        server_ip: e.ssh.as_ref().map(|s| s.server_ip.clone()),
        ssh_port: e.ssh.as_ref().map(|s| s.port),
        ssh_fingerprint: e.ssh.as_ref().map(|s| s.host_sha256.clone()),
    }
}
fn session(inner: &mut Inner) -> Result<&mut Session, String> {
    if inner
        .session
        .as_ref()
        .is_some_and(|s| s.generation != inner.generation.load(Ordering::SeqCst))
    {
        inner.session = None;
    }
    inner
        .session
        .as_mut()
        .ok_or_else(|| "SESSION_LOCKED".into())
}
fn persist(saved: &Saved) -> Result<(), String> {
    let bytes =
        Zeroizing::new(serde_json::to_vec(saved).map_err(|_| "Could not serialize secure state")?);
    native::save(&bytes)
}
async fn make_session(saved: Saved, generation: u64) -> Result<Session, String> {
    let (client, secret) = client(&saved.enrollment)?;
    let ssh = saved.enrollment.ssh.clone();
    let tunnel = if let Some(ssh) = ssh {
        Some(
            tauri::async_runtime::spawn_blocking(move || setup::tunnel(&ssh))
                .await
                .map_err(|_| "SSH tunnel task failed")??,
        )
    } else {
        None
    };
    let api_origin = tunnel
        .as_ref()
        .map(|t| t.origin.clone())
        .unwrap_or_else(|| saved.enrollment.origin.clone());
    Ok(Session {
        generation,
        saved,
        client,
        secret,
        api_origin,
        _tunnel: tunnel,
    })
}

#[tauri::command]
async fn setup_inspect(
    app: tauri::AppHandle,
    c: State<'_, Control>,
    request: setup::Request,
) -> Result<setup::Preview, String> {
    request.validate()?;
    c.clear().await;
    let generation = c.generation.load(Ordering::SeqCst);
    let handle = app.clone();
    let plan=native_task(&c,move || {
        native::authenticate()?;
        if native::load_optional()?.is_some() { return Err("A saved enrollment already exists. Unlock it, or resume its installation; setup cannot replace it".into()); }
        setup::inspect(&handle,request)
    }).await?;
    if generation != c.generation.load(Ordering::SeqCst)
        || !app
            .get_webview_window("main")
            .is_some_and(|w| w.is_focused().unwrap_or(false))
    {
        return Err("SESSION_LOCKED".into());
    }
    let preview = plan.preview(&setup::binary(&app, "dockyard")?);
    *c.setup.lock().await = Some(plan);
    Ok(preview)
}

async fn finish_setup(
    app: &tauri::AppHandle,
    c: &Control,
    saved: Saved,
    generation: u64,
) -> Result<Value, String> {
    let mut s = make_session(saved, generation).await?;
    send(&s, &read_op(&Read::Projects {})?).await?;
    s.saved.setup = None;
    persist(&s.saved)?;
    let p = profile(&s.saved.enrollment);
    let mut inner = c.inner.lock().await;
    if generation != c.generation.load(Ordering::SeqCst)
        || !app
            .get_webview_window("main")
            .is_some_and(|w| w.is_focused().unwrap_or(false))
    {
        return Err("SESSION_LOCKED: Setup completed and was saved. Unlock with Touch ID".into());
    }
    inner.profile = Some(p.clone());
    inner.session = Some(s);
    Ok(json!({"unlocked":true,"profile":p,"jobs":[]}))
}

#[tauri::command]
async fn setup_install(
    app: tauri::AppHandle,
    c: State<'_, Control>,
    id: String,
) -> Result<Value, String> {
    let plan = c
        .setup
        .lock()
        .await
        .take()
        .ok_or("Setup inspection expired; connect again")?;
    if plan.id != id || plan.created.elapsed() > Duration::from_secs(600) {
        return Err("Setup review expired; connect again".into());
    }
    let generation = c.generation.load(Ordering::SeqCst);
    let handle = app.clone();
    let saved=native_task(&c,move || {
        native::authenticate()?;
        if native::load_optional()?.is_some() {return Err("A saved enrollment exists; resume it instead".into());}
        let mode="Compose management with full server privileges";
        let review=format!("Install Dockyard on {}:{} (Ubuntu {})\nSSH: {}\n\n{}\nInstall missing Docker/Compose/Caddy dependencies; preserve existing Compose files.\nBack up and validate Caddy before adding its private admin socket and managed import.\nInstall a 24/7 systemd service and restricted SSH connector.\nSave generated Mac credentials in Keychain before making server changes.\nRoot password is never saved.",plan.request.server_ip,plan.request.ssh_port,plan.version,plan.host,mode);
        if native::with_prompt(|| rfd::MessageDialog::new().set_title("Review Ubuntu server installation").set_description(review).set_buttons(rfd::MessageButtons::OkCancel).show())!=rfd::MessageDialogResult::Ok {return Err("Installation cancelled; no server changes made".into());}
        let (enrollment,receipt)=setup::material(&handle,&plan)?;
        let saved=Saved{enrollment,pending:None,jobs:vec![],setup:Some(receipt.clone())};
        persist(&saved)?;
        setup::apply(&handle,&plan.session,&receipt,plan.python)?;
        Ok(saved)
    }).await?;
    finish_setup(&app, &c, saved, generation).await
}

#[tauri::command]
async fn setup_resume(app: tauri::AppHandle, c: State<'_, Control>) -> Result<Value, String> {
    c.clear().await;
    let generation = c.generation.load(Ordering::SeqCst);
    let handle = app.clone();
    let saved = native_task(&c, move || {
        native::authenticate()?;
        let bytes = native::with_prompt(native::load)?;
        let saved: Saved =
            serde_json::from_slice(&bytes).map_err(|_| "Saved enrollment is invalid")?;
        let receipt = saved
            .setup
            .as_ref()
            .ok_or("No unfinished setup is saved. Use Unlock with Touch ID")?;
        let session = setup::resume_connection(&handle, receipt)?;
        setup::apply(&handle, &session, receipt, false)?;
        Ok(saved)
    })
    .await?;
    finish_setup(&app, &c, saved, generation).await
}

#[tauri::command]
async fn setup_cancel(c: State<'_, Control>) -> Result<(), String> {
    c.setup.lock().await.take();
    Ok(())
}
fn client(e: &Enrollment) -> Result<(reqwest::Client, Zeroizing<Vec<u8>>), String> {
    let secret = Zeroizing::new(e.validate()?);
    let tls = tls::configuration(e)?;
    let builder = reqwest::Client::builder()
        .use_preconfigured_tls(tls)
        .https_only(true)
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(8))
        .timeout(Duration::from_secs(30))
        .retry(reqwest::retry::never())
        .user_agent("Dockyard-Desktop/0.1.0");
    Ok((
        builder.build().map_err(|_| "TLS configuration failed")?,
        secret,
    ))
}
async fn send(s: &Session, op: &Operation) -> Result<Value, String> {
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|_| "Check your Mac clock")?
        .as_secs()
        .to_string();
    let signature = protocol::sign(&s.secret, &s.saved.enrollment, op, &timestamp);
    let mut sig =
        reqwest::header::HeaderValue::from_str(&signature).map_err(|_| "Signing failed")?;
    sig.set_sensitive(true);
    let mut request = s
        .client
        .request(
            reqwest::Method::from_bytes(op.method.as_bytes()).map_err(|_| "Invalid method")?,
            format!("{}{}", s.api_origin.trim_end_matches('/'), op.target),
        )
        .header("X-Deploy-Key-ID", &s.saved.enrollment.key_id)
        .header("X-Deploy-Timestamp", timestamp)
        .header("Idempotency-Key", &op.idempotency)
        .header("X-Actor-ID", &s.saved.enrollment.actor_id)
        .header("X-Request-ID", &op.request_id)
        .header("X-Deploy-Scopes", &op.scopes)
        .header("X-Deploy-Signature", sig);
    if !op.body.is_empty() {
        request = request
            .header("Content-Type", "application/json")
            .body(op.body.clone());
    }
    let mut response = request.send().await.map_err(|error| {
        if error.is_connect() {
            "CONNECTION_UNAVAILABLE: could not connect to Dockyard on the VPS. Check that Dockyard is running and the tunnel and certificate are valid.".to_string()
        } else {
            "NETWORK_UNCERTAIN: the connection was interrupted. Reconnect, then retry the saved operation to check its outcome.".to_string()
        }
    })?;
    let status = response.status();
    // The project index includes bounded release history for every project; allow 100+ services without lifting log/audit limits.
    let response_limit = if op.target == "/v1/projects" {
        32 * 1024 * 1024
    } else {
        3 * 1024 * 1024
    };
    let mut bytes = Zeroizing::new(Vec::new());
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|_| "NETWORK_UNCERTAIN: response interrupted")?
    {
        if bytes.len() + chunk.len() > response_limit {
            return Err(format!(
                "Response exceeds the {} MiB limit",
                response_limit / (1024 * 1024)
            ));
        }
        bytes.extend_from_slice(&chunk);
    }
    let data: Value = serde_json::from_slice(&bytes)
        .map_err(|_| "Invalid server response; check the saved operation before another write")?;
    if !status.is_success() {
        let code = data
            .get("code")
            .or_else(|| data.get("error").and_then(|e| e.get("code")))
            .and_then(Value::as_str)
            .unwrap_or("REQUEST_REJECTED");
        // Never include raw responses or request bodies in an error.
        let code: String = code
            .chars()
            .filter(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || *c == '_')
            .take(80)
            .collect();
        // The server may add Docker's explanation (known .env values already
        // redacted). Keep it bounded and printable; append it after the code.
        let detail: String = data
            .get("error")
            .and_then(|e| e.get("detail"))
            .and_then(Value::as_str)
            .unwrap_or("")
            .chars()
            .filter(|c| *c == '\n' || !c.is_control())
            .take(4000)
            .collect();
        if !detail.is_empty() {
            return Err(format!("HTTP_{}: {}\n{}", status.as_u16(), code, detail));
        }
        return Err(format!("HTTP_{}: {}", status.as_u16(), code));
    }
    Ok(data)
}
fn read_op(read: &Read) -> Result<Operation, String> {
    let (target, scopes) = read.target()?;
    Ok(Operation {
        method: "GET".into(),
        target,
        scopes: scopes.into(),
        project: String::new(),
        action: "read".into(),
        body: String::new(),
        idempotency: format!("read-{}", uuid::Uuid::new_v4()),
        request_id: format!("req-{}", uuid::Uuid::new_v4()),
    })
}
struct PromptGuard(Arc<AtomicBool>);
impl Drop for PromptGuard {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}
async fn native_task<T: Send + 'static>(
    c: &Control,
    task: impl FnOnce() -> Result<T, String> + Send + 'static,
) -> Result<T, String> {
    if c.native_prompt.swap(true, Ordering::SeqCst) {
        return Err("A native operation is already running".into());
    }
    let _guard = PromptGuard(c.native_prompt.clone());
    tauri::async_runtime::spawn_blocking(task)
        .await
        .map_err(|_| "Native operation failed")?
}
#[tauri::command]
async fn session_info(c: State<'_, Control>) -> Result<Value, String> {
    let mut inner = c.inner.lock().await;
    let unlocked = session(&mut inner).is_ok();
    let jobs = inner
        .session
        .as_ref()
        .map(|s| s.saved.jobs.clone())
        .unwrap_or_default();
    let profile = if unlocked {
        inner.profile.clone()
    } else {
        None
    };
    let known_enrollment = unlocked || inner.profile.is_some();
    drop(inner);
    let enrolled = if known_enrollment {
        true
    } else {
        native::enrollment_exists()?
    };
    Ok(json!({"unlocked":unlocked,"profile":profile,"jobs":jobs,"enrolled":enrolled}))
}
#[tauri::command]
async fn enroll(c: State<'_, Control>) -> Result<Value, String> {
    c.clear().await;
    let generation = c.generation.load(Ordering::SeqCst);
    let enrollment: Enrollment = native_task(&c, || {
        native::authenticate()?;
        if let Some(existing) = native::load_optional()? {
            let old: Saved = serde_json::from_slice(&existing)
                .map_err(|_| "Existing enrollment is invalid; inspect it before replacing")?;
            if old.pending.is_some() || old.setup.is_some() {
                return Err(
                    "Unlock and resolve the pending operation before replacing enrollment".into(),
                );
            }
        }
        let path = native::with_prompt(|| {
            rfd::FileDialog::new()
                .set_title("Enroll this Mac · select a private Dockyard enrollment")
                .add_filter("Enrollment", &["json"])
                .pick_file()
        })
        .ok_or("Enrollment cancelled")?;
        use std::{
            io::Read,
            os::unix::{
                ffi::OsStrExt,
                fs::{MetadataExt, OpenOptionsExt},
            },
        };
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(&path)
            .map_err(|_| "Cannot open enrollment file")?;
        let meta = file
            .metadata()
            .map_err(|_| "Cannot inspect enrollment file")?;
        if !meta.is_file()
            || meta.uid() != unsafe { libc::geteuid() }
            || meta.mode() & 0o077 != 0
            || meta.len() > 128 * 1024
            || path.as_os_str().as_bytes().contains(&0)
        {
            return Err(
                "Enrollment must be a regular file owned by you, chmod 600, up to 128 KiB".into(),
            );
        }
        let mut bytes = Zeroizing::new(Vec::new());
        file.take(128 * 1024 + 1)
            .read_to_end(&mut bytes)
            .map_err(|_| "Cannot read enrollment")?;
        if bytes.len() > 128 * 1024 {
            return Err("Enrollment is too large".into());
        }
        serde_json::from_slice(&bytes).map_err(|_| "Invalid enrollment format".into())
    })
    .await?;
    let s = make_session(
        Saved {
            enrollment,
            pending: None,
            jobs: vec![],
            setup: None,
        },
        generation,
    )
    .await?;
    // Prove both TLS trust and the certificate-pinned HMAC policy before replacing enrollment.
    send(&s, &read_op(&Read::Projects {})?).await?;
    persist(&s.saved)?;
    let p = profile(&s.saved.enrollment);
    let mut inner = c.inner.lock().await;
    if generation != c.generation.load(Ordering::SeqCst) {
        return Err("SESSION_LOCKED".into());
    }
    inner.profile = Some(p.clone());
    inner.session = Some(s);
    Ok(json!({"unlocked":true,"profile":p,"jobs":[]}))
}
#[tauri::command]
async fn unlock(c: State<'_, Control>) -> Result<Value, String> {
    c.clear().await;
    let generation = c.generation.load(Ordering::SeqCst);
    let saved: Saved = native_task(&c, move || {
        native::authenticate()?;
        let bytes = native::load()?;
        let saved: Saved = serde_json::from_slice(&bytes).map_err(|_| "Saved enrollment is invalid")?;
        if saved.setup.is_none() {
            let _validated_secret = Zeroizing::new(saved.enrollment.validate()?);
        }
        Ok(saved)
    })
    .await?;
    if saved.setup.is_some() {
        return Err(
            "SETUP_INCOMPLETE: Use Resume server setup to finish the saved installation".into(),
        );
    }
    if saved.jobs.len() > 100 || saved.jobs.iter().any(|s| !protocol::token(s)) {
        return Err("Saved job history is invalid".into());
    }
    if let Some(p) = &saved.pending {
        validate_pending(&p.operation)?;
    }
    let s = make_session(saved, generation).await?;
    let p = profile(&s.saved.enrollment);
    let jobs = s.saved.jobs.clone();
    let mut inner = c.inner.lock().await;
    if generation != c.generation.load(Ordering::SeqCst) {
        return Err("SESSION_LOCKED".into());
    }
    inner.profile = Some(p.clone());
    inner.session = Some(s);
    Ok(json!({"unlocked":true,"profile":p,"jobs":jobs}))
}
fn validate_pending(op: &Operation) -> Result<(), String> {
    let body: Value = serde_json::from_str(&op.body).map_err(|_| "Invalid saved operation")?;
    let value = match op.action.as_str() {
        "create" => json!({"action":"create","data":body}),
        "migrate" => json!({"action":"migrate","project":op.project,"source_sha256":body.get("source_sha256")}),
        "blue-green" => json!({"action":"blue_green","project":op.project,"data":body}),
        "service-update" => json!({"action":"service_update","project":op.project,"data":body}),
        "route-setup" => json!({"action":"route_setup","project":op.project,"data":body}),
        "deploy" | "routes" => json!({"action":op.action,"project":op.project,"data":body}),
        "stop" => {
            json!({"action":"stop","project":op.project,"confirmation":body.get("confirmation")})
        }
        "dns" => json!({"action":"dns","project":op.project,"hostname":body.get("hostname")}),
        "rollback" => {
            json!({"action":"rollback","project":op.project,"release_id":body.get("release_id")})
        }
        "start" | "restart" if body == json!({}) => {
            json!({"action":op.action,"project":op.project})
        }
        _ => return Err("Invalid saved operation".into()),
    };
    let rebuilt = serde_json::from_value::<Mutation>(value)
        .map_err(|_| "Invalid saved operation")?
        .plan()?;
    if op.method != rebuilt.method
        || op.target != rebuilt.target
        || op.scopes != rebuilt.scopes
        || op.project != rebuilt.project
        || op.body != rebuilt.body
        || !protocol::token(&op.idempotency)
        || !protocol::token(&op.request_id)
    {
        return Err("Invalid saved operation".into());
    }
    Ok(())
}
#[tauri::command]
async fn lock_session(c: State<'_, Control>) -> Result<(), String> {
    c.clear().await;
    Ok(())
}
#[tauri::command]
async fn forget_device(c: State<'_, Control>) -> Result<(), String> {
    c.clear().await;
    native_task(&c, || {
        native::authenticate()?;
        let bytes = native::with_prompt(native::load)?;
        let saved: Saved =
            serde_json::from_slice(&bytes).map_err(|_| "Saved enrollment is invalid")?;
        if saved.pending.is_some() || saved.setup.is_some() {
            return Err("Resolve the saved operation before removing enrollment".into());
        }
        if let Some(ssh) = &saved.enrollment.ssh {
            native::terminal_delete(&terminal::account(
                &ssh.server_ip,
                ssh.port,
                &ssh.host_sha256,
            ))?;
        }
        native::delete()
    })
    .await?;
    c.inner.lock().await.profile = None;
    Ok(())
}
#[tauri::command]
async fn pending_info(c: State<'_, Control>) -> Result<Value, String> {
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    Ok(s.saved.pending.as_ref().map(|p| json!({"project":p.operation.project,"action":p.operation.action,"request_id":p.operation.request_id,"idempotency":p.operation.idempotency,"job_id":p.job_id})).unwrap_or(Value::Null))
}
#[tauri::command]
async fn read_api(c: State<'_, Control>, read: Read) -> Result<Value, String> {
    let op = read_op(&read)?;
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    let mut data = send(s, &op).await?;
    if matches!(&read, Read::Configuration { .. }) {
        // Compose opens freely; environment secrets need read_env (Touch ID).
        if let Some(Value::String(env)) = data.get_mut("env_file") {
            env.zeroize();
        }
        if let Some(obj) = data.as_object_mut() {
            obj.remove("env_file");
            obj.insert("env_locked".into(), Value::Bool(true));
        }
    }
    if s.generation != c.generation.load(Ordering::SeqCst) {
        return Err("SESSION_LOCKED".into());
    }
    if let Read::Job { job } = read {
        if s.saved
            .pending
            .as_ref()
            .is_some_and(|p| p.job_id.as_ref() == Some(&job))
            && matches!(
                data.get("status").and_then(Value::as_str),
                Some("succeeded" | "failed" | "recovery_required")
            )
        {
            let previous = s.saved.pending.take();
            if let Err(e) = persist(&s.saved) {
                s.saved.pending = previous;
                return Err(e);
            }
        }
    }
    Ok(data)
}
fn definitive_initial_rejection(was_attempted: bool, error: &str) -> bool {
    !was_attempted
        && [
            "HTTP_400:",
            "HTTP_401:",
            "HTTP_403:",
            "HTTP_404:",
            "HTTP_405:",
            "HTTP_409:",
            "HTTP_415:",
            "HTTP_422:",
            "HTTP_429:",
            "CONNECTION_UNAVAILABLE:",
        ]
        .iter()
        .any(|status| error.starts_with(status))
}
async fn execute_pending(s: &mut Session) -> Result<Value, String> {
    let op = s
        .saved
        .pending
        .as_ref()
        .ok_or("No saved operation")?
        .operation
        .clone();
    if let Some(job) = s.saved.pending.as_ref().and_then(|p| p.job_id.clone()) {
        return Ok(json!({"job_id":job,"project_id":op.project,"action":op.action}));
    }
    let was_attempted = s.saved.pending.as_ref().unwrap().attempted;
    let previous_sent = s.saved.pending.as_ref().unwrap().sent_at;
    {
        let pending = s.saved.pending.as_mut().unwrap();
        pending.attempted = true;
        pending.sent_at = Some(unix_now());
    }
    // Record that a request may reach the VPS before sending a single byte.
    if let Err(error) = persist(&s.saved) {
        let pending = s.saved.pending.as_mut().unwrap();
        pending.attempted = was_attempted;
        pending.sent_at = previous_sent;
        return Err(error);
    }
    match send(s, &op).await {
        Ok(data) => {
            let job = data
                .get("job_id")
                .and_then(Value::as_str)
                .filter(|j| protocol::token(j))
                .ok_or("Uncertain write response: retry the saved operation")?
                .to_string();
            s.saved.pending.as_mut().unwrap().job_id = Some(job.clone());
            if !s.saved.jobs.contains(&job) {
                s.saved.jobs.insert(0, job);
                s.saved.jobs.truncate(100);
            }
            persist(&s.saved)?;
            Ok(data)
        }
        Err(e) => {
            // A failed connection means this attempt sent no HTTP request. Only
            // the first attempt can be cleared; a retry may follow an accepted write.
            if definitive_initial_rejection(was_attempted, &e) {
                let previous = s.saved.pending.take();
                if persist(&s.saved).is_err() {
                    s.saved.pending = previous;
                }
            }
            Err(e)
        }
    }
}
#[tauri::command]
async fn preview_blue_green(c: State<'_, Control>, project: String, data: protocol::BlueGreen) -> Result<Value,String> {
 if !protocol::id(&project) || !data.valid() {return Err("Invalid seamless updates configuration".into());}
 let op=Operation {method:"POST".into(),target:format!("/v1/projects/{project}/blue-green-preview"),scopes:"deploy.environment".into(),project,action:"blue-green-preview".into(),body:serde_json::to_string(&data).map_err(|_|"Invalid request")?,idempotency:format!("preview-{}",uuid::Uuid::new_v4()),request_id:format!("req-{}",uuid::Uuid::new_v4())};
 let mut inner=c.inner.lock().await;let s=session(&mut inner)?;
 let result=send(s,&op).await?;
 if s.generation!=c.generation.load(Ordering::SeqCst){return Err("SESSION_LOCKED".into());}
 Ok(result)
}
#[tauri::command]
async fn preview_service_update(c: State<'_, Control>, project: String, data: protocol::ServiceUpdate) -> Result<Value,String> {
    if !protocol::id(&project) || !data.valid() { return Err("Invalid service update".into()); }
    let op=Operation {method:"POST".into(),target:format!("/v1/projects/{project}/service-update-preview"),scopes:data.scopes().into(),project,action:"service-update-preview".into(),body:serde_json::to_string(&data).map_err(|_|"Invalid request")?,idempotency:format!("preview-{}",uuid::Uuid::new_v4()),request_id:format!("req-{}",uuid::Uuid::new_v4())};
    let mut inner=c.inner.lock().await;let s=session(&mut inner)?;
    let result=send(s,&op).await?;
    if s.generation!=c.generation.load(Ordering::SeqCst){return Err("SESSION_LOCKED".into());}
    Ok(result)
}
#[tauri::command]
async fn preview_route_setup(c: State<'_, Control>, project: String, data: protocol::RouteSetup) -> Result<Value,String> {
    if !protocol::id(&project) || !data.valid() { return Err("Invalid route setup".into()); }
    let op=Operation {method:"POST".into(),target:format!("/v1/projects/{project}/route-setup-preview"),scopes:"projects.write sites.write".into(),project,action:"route-setup-preview".into(),body:serde_json::to_string(&data).map_err(|_|"Invalid request")?,idempotency:format!("preview-{}",uuid::Uuid::new_v4()),request_id:format!("req-{}",uuid::Uuid::new_v4())};
    let mut inner=c.inner.lock().await; let s=session(&mut inner)?;
    let result=send(s,&op).await?;
    if s.generation!=c.generation.load(Ordering::SeqCst){return Err("SESSION_LOCKED".into());}
    Ok(result)
}
#[tauri::command]
async fn read_env(c: State<'_, Control>, project: String) -> Result<Value, String> {
    // Touch ID reuses a success from the last two minutes (auth_window).
    let read = Read::Configuration { project };
    let op = read_op(&read)?;
    let generation = {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?.generation
    };
    native_task(&c, || native::authenticate_reason("View this project's environment secrets")).await?;
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    if generation != c.generation.load(Ordering::SeqCst) {
        return Err("SESSION_LOCKED".into());
    }
    let mut data = send(s, &op).await?;
    let result = json!({
        "release_id": data.get("release_id").cloned().unwrap_or(Value::Null),
        "env_file": data.get("env_file").cloned().unwrap_or(Value::String(String::new())),
    });
    if let Some(Value::String(compose)) = data.get_mut("compose_yaml") {
        compose.zeroize();
    }
    Ok(result)
}
#[derive(serde::Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
struct DraftInput {
    compose_yaml: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    env_file: Option<String>,
}
#[tauri::command]
async fn save_draft(c: State<'_, Control>, project: String, mut data: DraftInput) -> Result<Value, String> {
    // Saves the files on the VPS without validating or deploying them.
    if !protocol::id(&project) || data.compose_yaml.len() > 65536 || data.env_file.as_ref().is_some_and(|e| e.len() > 65536 || e.contains('\0')) {
        return Err("Compose and .env must each be at most 64 KiB".into());
    }
    let body = serde_json::to_string(&data).map_err(|_| "Invalid request")?;
    data.compose_yaml.zeroize();
    if let Some(env) = data.env_file.as_mut() {
        env.zeroize();
    }
    let op = Operation { method: "POST".into(), target: format!("/v1/projects/{project}/draft"), scopes: "deploy.environment".into(), project, action: "draft".into(), body, idempotency: format!("draft-{}", uuid::Uuid::new_v4()), request_id: format!("req-{}", uuid::Uuid::new_v4()) };
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    let result = send(s, &op).await?;
    if s.generation != c.generation.load(Ordering::SeqCst) { return Err("SESSION_LOCKED".into()); }
    Ok(result)
}
#[tauri::command]
async fn reconcile_job(c: State<'_, Control>, job: String) -> Result<Value, String> {
    // Same verified recovery as `dockyard -reconcile-job`, run by the daemon.
    // It never starts, stops or relabels containers and is safe to repeat, so
    // it bypasses the saved-operation slot that would otherwise block it.
    if job.len() != 36 || !job.starts_with("job-") || !job[4..].bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        return Err("Invalid job".into());
    }
    let name = {
        let mut inner = c.inner.lock().await;
        let s = session(&mut inner)?;
        format!("{} ({})", s.saved.enrollment.name, s.saved.enrollment.server_id)
    };
    let review = format!("Server: {name}\nJob: {job}\n\nRecover this interrupted operation. Dockyard inspects the live containers and records what is actually running. Containers are not started, stopped or changed.");
    let approved = native_task(&c, move || {
        let approved = native::with_prompt(|| {
            rfd::MessageDialog::new()
                .set_title("Recover interrupted operation")
                .set_description(review)
                .set_level(rfd::MessageLevel::Warning)
                .set_buttons(rfd::MessageButtons::OkCancel)
                .show()
        }) == rfd::MessageDialogResult::Ok;
        if approved {
            native::authenticate_reason("Recover an interrupted operation on your VPS")?;
        }
        Ok(approved)
    })
    .await?;
    if !approved {
        return Err("Operation cancelled".into());
    }
    let op = Operation { method: "POST".into(), target: format!("/v1/jobs/{job}/reconcile"), scopes: "deploy.execute".into(), project: String::new(), action: "reconcile".into(), body: "{}".into(), idempotency: format!("reconcile-{}", uuid::Uuid::new_v4()), request_id: format!("req-{}", uuid::Uuid::new_v4()) };
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    let result = send(s, &op).await?;
    if s.generation != c.generation.load(Ordering::SeqCst) { return Err("SESSION_LOCKED".into()); }
    Ok(result)
}
#[tauri::command]
async fn mutate(c: State<'_, Control>, mutation: Mutation) -> Result<Value, String> {
    let op = mutation.plan()?;
    let name = {
        let mut inner = c.inner.lock().await;
        let s = session(&mut inner)?;
        if s.saved.pending.is_some() {
            return Err("Resolve the unconfirmed operation at the top first".into());
        }
        let mut target = format!(
            "{} ({})\nOrigin: {}",
            s.saved.enrollment.name, s.saved.enrollment.server_id, s.saved.enrollment.origin
        );
        if let Some(ssh) = &s.saved.enrollment.ssh {
            target.push_str(&format!(
                "\nVPS: {}:{}\nSSH: {}",
                ssh.server_ip, ssh.port, ssh.host_sha256
            ));
        }
        target
    };
    let mut review = format!(
        "Server: {name}\nProject: {}\nAction: {}\n",
        op.project, if op.action == "blue-green" { "Seamless updates" } else if op.action == "service-update" { "Service update" } else { &op.action }
    );
    let body: Value = serde_json::from_str(&op.body).map_err(|_| "Invalid request")?;
    if let Some(env) = body.get("environment").and_then(Value::as_str) {
        review.push_str(&format!("Environment: {env}\n"));
    }
    if let Some(domains) = body.get("domains") {
        review.push_str(&format!("Domains: {domains}\n"));
    }
    if let Some(hostname) = body.get("hostname") {
        review.push_str(&format!("Hostname: {hostname}\n"));
    }
    for field in ["port", "secondary_port", "zerodowntime", "release_id"] {
        if let Some(v) = body.get(field) {
            review.push_str(&format!("{field}: {v}\n"));
        }
    }
    review.push_str(&format!(
        "\nPayload SHA-256:\n{}\n\nThis changes services on your VPS.",
        hex::encode(sha2::Sha256::digest(op.body.as_bytes()))
    ));
    if op.action == "blue-green" {review.push_str("\nStart the new version separately, verify health, switch the reviewed domains, then drain and stop the old instance. Original files and the previous project snapshot are retained for recovery.");}
    if op.action == "service-update" {
        review.push_str(&format!("\nService: {}\nUpdate: {}\nOther services and their data volumes remain running.", body.get("service").and_then(Value::as_str).unwrap_or(""), if body.get("mode").and_then(Value::as_str)==Some("seamless") { "Seamless traffic switch after health checks" } else { "Controlled restart of this service" }));
    }
    if op.action == "migrate" { review.push_str("\nAdopt the reviewed existing Compose stack into Dockyard. Containers, ports and Caddy routes stay in place. Future deployments use a single instance."); }
    if op.action == "route-setup" { review.push_str("\nConfigure Caddy for the reviewed existing Compose service and published port. Import only the reviewed plain site routes. Containers, volumes and Compose files stay in place."); }
    let sensitive = op.action != "create";
    let approved = native_task(&c, move || {
        let approved = native::with_prompt(|| {
            rfd::MessageDialog::new()
                .set_title("Confirm Dockyard operation")
                .set_description(review)
                .set_level(rfd::MessageLevel::Warning)
                .set_buttons(rfd::MessageButtons::OkCancel)
                .show()
        }) == rfd::MessageDialogResult::Ok;
        if approved && sensitive {
            native::authenticate_reason("Confirm a service or traffic change on your VPS")?;
        }
        Ok(approved)
    })
    .await?;
    if !approved {
        return Err("Operation cancelled".into());
    }
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    if s.saved.pending.is_some() {
        return Err("Another operation is pending".into());
    }
    s.saved.pending = Some(Pending {
        attempted: false,
        operation: op,
        job_id: None,
        sent_at: None,
    });
    if let Err(e) = persist(&s.saved) {
        s.saved.pending = None;
        return Err(e);
    }
    execute_pending(s).await
}
fn unix_now() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}
// Ask the VPS whether the saved write was ever admitted, without resending it.
// Signed requests expire after 60s, so "never admitted" after that is final.
#[tauri::command]
async fn resolve_pending(c: State<'_, Control>) -> Result<Value, String> {
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    let Some(pending) = s.saved.pending.as_ref() else { return Ok(json!({"outcome":"none"})) };
    let (attempted, sent_at, job_id, key) = (pending.attempted, pending.sent_at, pending.job_id.clone(), pending.operation.idempotency.clone());
    let clear = |s: &mut Session| -> Result<(), String> {
        let previous = s.saved.pending.take();
        persist(&s.saved).map_err(|e| { s.saved.pending = previous; e })
    };
    if job_id.is_none() && !attempted {
        clear(s)?;
        return Ok(json!({"outcome":"not_sent"}));
    }
    let job = match job_id {
        Some(job) => job,
        None => {
            let op = Operation { method: "GET".into(), target: format!("/v1/requests/{key}"), scopes: "deploy.read".into(), project: String::new(), action: "read".into(), body: String::new(), idempotency: format!("read-{}", uuid::Uuid::new_v4()), request_id: format!("req-{}", uuid::Uuid::new_v4()) };
            match send(s, &op).await {
                Ok(data) => data.get("job_id").and_then(Value::as_str).filter(|j| protocol::token(j)).ok_or("Unexpected server response")?.to_string(),
                // Only this code is definitive; an older server's generic 404
                // means the lookup is unsupported, not that nothing happened.
                Err(e) if e.starts_with("HTTP_404:") && !e.contains("REQUEST_NOT_FOUND") => {
                    return Err("Update the VPS service to 0.7.7 to check this operation.".into());
                }
                Err(e) if e.starts_with("HTTP_404:") => {
                    if sent_at.is_some_and(|t| unix_now().saturating_sub(t) < 90) {
                        return Err("Still settling. Check again in a minute.".into());
                    }
                    clear(s)?;
                    return Ok(json!({"outcome":"not_applied"}));
                }
                Err(e) => return Err(e),
            }
        }
    };
    let op = Operation { method: "GET".into(), target: format!("/v1/jobs/{job}"), scopes: "deploy.read".into(), project: String::new(), action: "read".into(), body: String::new(), idempotency: format!("read-{}", uuid::Uuid::new_v4()), request_id: format!("req-{}", uuid::Uuid::new_v4()) };
    let data = send(s, &op).await?;
    let status = data.get("status").and_then(Value::as_str).unwrap_or("").to_string();
    if !s.saved.jobs.contains(&job) {
        s.saved.jobs.insert(0, job.clone());
        s.saved.jobs.truncate(100);
    }
    if matches!(status.as_str(), "succeeded" | "failed" | "recovery_required") {
        clear(s)?;
    } else if let Some(p) = s.saved.pending.as_mut() {
        p.job_id = Some(job.clone());
        persist(&s.saved)?;
    }
    if s.generation != c.generation.load(Ordering::SeqCst) { return Err("SESSION_LOCKED".into()); }
    Ok(json!({"outcome":"applied","job_id":job,"status":status}))
}
#[tauri::command]
async fn retry_pending(c: State<'_, Control>) -> Result<Value, String> {
    let mut inner = c.inner.lock().await;
    execute_pending(session(&mut inner)?).await
}
#[tauri::command]
async fn import_compose(c: State<'_, Control>) -> Result<Option<String>, String> {
    import_project_file(c, false).await
}
#[tauri::command]
async fn import_env(c: State<'_, Control>) -> Result<Option<String>, String> {
    import_project_file(c, true).await
}
async fn import_project_file(
    c: State<'_, Control>,
    dotenv: bool,
) -> Result<Option<String>, String> {
    {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?;
    }
    native_task(&c, move || {
        use std::{io::Read, os::unix::fs::OpenOptionsExt};
        let Some(path) = native::with_prompt(|| {
            let dialog = rfd::FileDialog::new();
            if dotenv {
                dialog
                    .set_title("Import .env (Command–Shift–. shows hidden files)")
                    .pick_file()
            } else {
                dialog
                    .set_title("Import Docker Compose")
                    .add_filter("Compose YAML", &["yaml", "yml"])
                    .pick_file()
            }
        }) else {
            return Ok(None);
        };
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(path)
            .map_err(|_| "Cannot open project file")?;
        if !file
            .metadata()
            .map_err(|_| "Cannot inspect project file")?
            .is_file()
        {
            return Err("Choose a regular text file".into());
        }
        let mut bytes = Zeroizing::new(Vec::new());
        file.take(65537)
            .read_to_end(&mut bytes)
            .map_err(|_| "Cannot read project file")?;
        if bytes.len() > 65536 {
            return Err("Project files must be at most 64 KiB each".into());
        }
        String::from_utf8(bytes.to_vec())
            .map(Some)
            .map_err(|_| "Project files must be UTF-8".into())
    })
    .await
}

async fn terminal_authority(
    c: &Control,
) -> Result<(protocol::SshIdentity, terminal::Lease), String> {
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    let ssh = s
        .saved
        .enrollment
        .ssh
        .clone()
        .ok_or("Enroll a pinned SSH server before using the terminal")?;
    Ok((
        ssh,
        terminal::Lease {
            generation: c.generation.clone(),
            expected: s.generation,
        },
    ))
}
#[tauri::command]
async fn server_update_check(
    app: tauri::AppHandle,
    c: State<'_, Control>,
) -> Result<updater::Preview, String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    native_task(&c, move || {
        lease.check()?;
        let result = updater::check(&app, &ssh)?;
        lease.check()?;
        Ok(result)
    })
    .await
}
#[tauri::command]
async fn server_update_apply(
    app: tauri::AppHandle,
    c: State<'_, Control>,
    expected: updater::Preview,
) -> Result<String, String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    native_task(&c, move || {
        lease.check()?;
        updater::apply(&app, &ssh, &expected)
    })
    .await
}
#[tauri::command]
async fn server_compose_enable(app: tauri::AppHandle, c: State<'_, Control>) -> Result<(), String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    let key_id = {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?.saved.enrollment.key_id.clone()
    };
    native_task(&c, move || {
        lease.check()?;
        updater::enable_compose(&app, &ssh, &key_id)
    })
    .await
}
#[tauri::command]
async fn server_access_check(
    app: tauri::AppHandle,
    c: State<'_, Control>,
) -> Result<updater::AccessReport, String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    let key_id = {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?.saved.enrollment.key_id.clone()
    };
    native_task(&c, move || {
        lease.check()?;
        let result = updater::check_access(&app, &ssh, &key_id)?;
        lease.check()?;
        Ok(result)
    })
    .await
}
#[tauri::command]
async fn server_access_prepare(
    app: tauri::AppHandle,
    c: State<'_, Control>,
) -> Result<updater::AccessReport, String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    let key_id = {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?.saved.enrollment.key_id.clone()
    };
    native_task(&c, move || {
        lease.check()?;
        updater::prepare_access(&app, &ssh, &key_id)
    })
    .await
}
#[tauri::command]
async fn terminal_connect(
    app: tauri::AppHandle,
    c: State<'_, Control>,
    remember: bool,
) -> Result<terminal::Connected, String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    c.terminal.lock().expect("terminal poisoned").take();
    let check = lease.clone();
    let handle = app.clone();
    let (connection, connected) = native_task(&c, move || {
        native::authenticate_reason(&format!("Open root SSH access to {}:{}. This grants full server control.", ssh.server_ip, ssh.port))?;
        lease.check()?;
        let (session, _) = setup::connect(&ssh.server_ip, ssh.port, Some(&ssh.host_sha256))?;
        let account = terminal::account(&ssh.server_ip, ssh.port, &ssh.host_sha256);
        let password = native::root_credential(&handle, format!("{}:{}", ssh.server_ip, ssh.port), &account, remember)?;
        lease.check()?;
        if session.userauth_password("root", &password).is_err() || !session.authenticated() {
            native::terminal_invalidate(&account);
            return Err("Root SSH login failed. Reconnect to enter the current password.".into());
        }
        lease.check()?;
        if remember { native::terminal_save(&account, password.as_bytes())?; }
        else { native::terminal_remember_for_run(&account, password.as_bytes())?; }
        drop(password);
        terminal::spawn(session, lease, remember)
    }).await?;
    check.check()?;
    if !app
        .get_webview_window("main")
        .is_some_and(|w| w.is_focused().unwrap_or(false))
    {
        return Err("Bring Dockyard to the foreground and reconnect".into());
    }
    *c.terminal.lock().expect("terminal poisoned") = Some(connection);
    Ok(connected)
}
#[tauri::command]
async fn terminal_start(
    c: State<'_, Control>,
    id: String,
    container: Option<String>,
    shell: String,
    cols: u32,
    rows: u32,
) -> Result<(), String> {
    let reply = {
        let manager = c.terminal.lock().expect("terminal poisoned");
        let handle = manager.as_ref().ok_or("Terminal is disconnected")?;
        handle.matches(&id)?;
        handle.start(container, shell, cols, rows)?
    };
    tauri::async_runtime::spawn_blocking(move || {
        reply
            .recv_timeout(Duration::from_secs(12))
            .map_err(|_| "Opening the shell timed out")?
    })
    .await
    .map_err(|_| "Terminal task failed")?
}
#[tauri::command]
fn terminal_input(c: State<'_, Control>, id: String, data: String) -> Result<(), String> {
    let manager = c.terminal.lock().expect("terminal poisoned");
    let handle = manager.as_ref().ok_or("Terminal is disconnected")?;
    handle.matches(&id)?;
    let data = Zeroizing::new(data);
    handle.input(&data)
}
#[tauri::command]
fn terminal_resize(c: State<'_, Control>, id: String, cols: u32, rows: u32) -> Result<(), String> {
    let manager = c.terminal.lock().expect("terminal poisoned");
    let handle = manager.as_ref().ok_or("Terminal is disconnected")?;
    handle.matches(&id)?;
    handle.resize(cols, rows)
}
#[tauri::command]
fn terminal_poll(c: State<'_, Control>, id: String) -> Result<terminal::Output, String> {
    let manager = c.terminal.lock().expect("terminal poisoned");
    let handle = manager.as_ref().ok_or("Terminal is disconnected")?;
    handle.matches(&id)?;
    Ok(handle.poll())
}
#[tauri::command]
fn terminal_close(c: State<'_, Control>, id: String) -> Result<(), String> {
    let mut manager = c.terminal.lock().expect("terminal poisoned");
    // Closing stays available after lock, and a stale close cannot terminate a newer shell.
    if manager.as_ref().is_some_and(|h| h.id == id) {
        manager.take();
    }
    Ok(())
}
#[tauri::command]
async fn terminal_forget_password(c: State<'_, Control>) -> Result<(), String> {
    let (ssh, lease) = terminal_authority(&c).await?;
    native_task(&c, move || {
        native::authenticate_reason(&format!(
            "Remove the saved root SSH password for {} from Keychain",
            ssh.server_ip
        ))?;
        lease.check()?;
        native::terminal_delete(&terminal::account(
            &ssh.server_ip,
            ssh.port,
            &ssh.host_sha256,
        ))
    })
    .await
}

use sha2::Digest;
fn main() {
    let control = Control::new();
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.set_focus();
            }
        }))
        .manage(control.clone())
        .invoke_handler(tauri::generate_handler![
            session_info,
            enroll,
            unlock,
            lock_session,
            forget_device,
            read_api,
            preview_blue_green,
            preview_service_update,
            preview_route_setup,
            mutate,
            reconcile_job,
            resolve_pending,
            read_env,
            save_draft,
            retry_pending,
            pending_info,
            import_compose,
            import_env,
            setup_inspect,
            setup_install,
            setup_resume,
            setup_cancel,
            terminal_connect,
            terminal_start,
            terminal_input,
            terminal_resize,
            terminal_poll,
            terminal_close,
            terminal_forget_password,
            server_update_check,
            server_update_apply,
            server_access_check,
            server_access_prepare,
            server_compose_enable,
            providers::provider_list,
            providers::provider_connect,
            providers::provider_remove,
            providers::provider_token_page,
            providers::provider_zones,
            providers::provider_records,
            providers::provider_write
        ])
        .setup(move |app| {
            tauri::WebviewWindowBuilder::from_config(app, &app.config().app.windows[0])?
                .on_navigation(|u| {
                    (u.scheme() == "tauri" && u.host_str() == Some("localhost"))
                        || (cfg!(debug_assertions)
                            && u.scheme() == "http"
                            && u.host_str() == Some("127.0.0.1")
                            && u.port() == Some(1420))
                })
                .on_new_window(|_, _| tauri::webview::NewWindowResponse::Deny)
                .build()?;
            let handle = app.handle().clone();
            let c = control.clone();
            let focus_handle = handle.clone();
            let focus_control = c.clone();
            // Keep the away timer independent of slow API calls holding the session mutex.
            tauri::async_runtime::spawn(async move {
                loop {
                    tokio::time::sleep(Duration::from_millis(250)).await;
                    let handle = focus_handle.clone();
                    let control = focus_control.clone();
                    let _ = focus_handle.run_on_main_thread(move || {
                        let visible = handle.get_webview_window("main").is_some_and(|w| {
                            w.is_visible().unwrap_or(false) && !w.is_minimized().unwrap_or(true)
                        });
                        observe_focus(&control, &handle, visible && native::app_is_active());
                    });
                }
            });
            tauri::async_runtime::spawn(async move {
                loop {
                    tokio::time::sleep(Duration::from_secs(2)).await;
                    {
                        let mut plan = c.setup.lock().await;
                        if plan
                            .as_ref()
                            .is_some_and(|p| p.created.elapsed() > Duration::from_secs(600))
                        {
                            plan.take();
                        }
                    }
                }
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            if matches!(event, tauri::WindowEvent::Destroyed) {
                native::clear_credential_cache();
                window.app_handle().exit(0);
            }
            // Sample application activation on the main thread. Webview/window focus
            // changes inside Dockyard must not start the away timer.
        })
        .build(tauri::generate_context!())
        .expect("Unable to build Dockyard")
        .run(|_, event| {
            if matches!(event, tauri::RunEvent::Exit) { native::clear_credential_cache(); }
        });
}

#[cfg(test)]
mod transport_tests {
    use super::*;
    use std::io::BufRead;
    struct Fixture(std::process::Child);
    impl Drop for Fixture {
        fn drop(&mut self) {
            self.0.stdin.take();
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }
    fn test_session(e: Enrollment) -> Session {
        let (client, secret) = client(&e).unwrap();
        Session {
            generation: 0,
            api_origin: e.origin.clone(),
            _tunnel: None,
            saved: Saved {
                enrollment: e,
                pending: None,
                jobs: vec![],
                setup: None,
            },
            client,
            secret,
        }
    }
    #[test]
    #[ignore = "starts a disposable loopback TLS server; run with --ignored"]
    fn mtls_signed_requests_and_uncertain_retry() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
        let child = std::process::Command::new("go")
            .args(["run", "./desktop/tests/agent"])
            .current_dir(root)
            .env("GOPROXY", "off")
            .env("GOCACHE", "/private/tmp/dockyard-gocache")
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::null())
            .spawn()
            .unwrap();
        let mut fixture = Fixture(child);
        let mut line = Zeroizing::new(String::new());
        std::io::BufReader::new(fixture.0.stdout.take().unwrap())
            .read_line(&mut line)
            .unwrap();
        let value: Value = serde_json::from_str(&line).expect("local fixture failed to start");
        let e: Enrollment = serde_json::from_value(value["enrollment"].clone()).unwrap();
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        runtime.block_on(async {
            let s = test_session(e.clone());
            let projects = read_op(&Read::Projects {}).unwrap();
            assert!(send(&s, &projects).await.unwrap()["projects"].is_array());
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let closed_port = listener.local_addr().unwrap().port();
            drop(listener);
            let mut offline = e.clone();
            offline.origin = format!("https://127.0.0.1:{closed_port}");
            let offline_error = send(&test_session(offline), &projects).await.unwrap_err();
            assert!(offline_error.starts_with("CONNECTION_UNAVAILABLE:"));
            assert!(definitive_initial_rejection(false, &offline_error));
            assert!(!definitive_initial_rejection(true, &offline_error));
            let mut scale = read_op(&Read::Projects {}).unwrap();
            scale.idempotency = "read-scale".into();
            let index = send(&s, &scale).await.unwrap();
            assert_eq!(index["projects"].as_array().unwrap().len(), 100);
            assert_eq!(
                index["projects"][0]["releases"].as_array().unwrap().len(),
                200
            );
            let mut bad_hmac = e.clone();
            bad_hmac.hmac_base64 =
                base64::Engine::encode(&base64::engine::general_purpose::STANDARD, [43u8; 32]);
            assert!(send(&test_session(bad_hmac), &projects)
                .await
                .unwrap_err()
                .starts_with("HTTP_401:"));
            let mut bad_client = e.clone();
            bad_client.client_identity_pem = value["wrong_client"].as_str().unwrap().into();
            assert!(send(&test_session(bad_client), &projects)
                .await
                .unwrap_err()
                .starts_with("HTTP_401:"));
            let mut bad_pin = e.clone();
            bad_pin.server_certificate_sha256 = "0".repeat(64);
            assert!(send(&test_session(bad_pin), &projects)
                .await
                .unwrap_err()
                .starts_with("CONNECTION_UNAVAILABLE:"));
            let mut bad_ca = e.clone();
            bad_ca.server_ca_pem = value["wrong_ca"].as_str().unwrap().into();
            assert!(send(&test_session(bad_ca), &projects)
                .await
                .unwrap_err()
                .starts_with("CONNECTION_UNAVAILABLE:"));
            for variant in ["wrong_name", "expired", "tls12"] {
                let mut changed = e.clone();
                changed.origin = value["extra"][format!("{variant}_origin")]
                    .as_str()
                    .unwrap()
                    .into();
                changed.server_certificate_sha256 = value["extra"][format!("{variant}_pin")]
                    .as_str()
                    .unwrap()
                    .into();
                assert!(
                    send(&test_session(changed), &projects)
                        .await
                        .unwrap_err()
                        .starts_with("CONNECTION_UNAVAILABLE:"),
                    "{variant} bypassed standard TLS verification"
                );
            }
            let op = Mutation::Restart {
                project: "demo".into(),
            }
            .plan()
            .unwrap();
            assert!(send(&s, &op).await.unwrap_err().starts_with("HTTP_500:"));
            let retry = send(&s, &op).await.unwrap();
            assert_eq!(retry["admissions"], 1);
            assert_eq!(retry["requests"], 2);
            let mut redirect = read_op(&Read::Projects {}).unwrap();
            redirect.target = "/v1/redirect".into();
            assert!(send(&s, &redirect)
                .await
                .unwrap_err()
                .starts_with("HTTP_302:"));
            redirect.target = "/v1/large".into();
            assert!(send(&s, &redirect).await.unwrap_err().contains("3 MiB"));
            redirect.target = "/v1/check".into();
            assert_eq!(send(&s, &redirect).await.unwrap()["leaked"], false);
        });
    }
    #[test]
    fn uncertain_writes_survive_rejection_and_legacy_saved_state() {
        for status in [
            "HTTP_400:",
            "HTTP_401:",
            "HTTP_403:",
            "HTTP_409:",
            "HTTP_429:",
            "CONNECTION_UNAVAILABLE:",
        ] {
            assert!(definitive_initial_rejection(false, status));
            assert!(!definitive_initial_rejection(true, status));
        }
        assert!(!definitive_initial_rejection(false, "HTTP_500:"));
        assert!(!definitive_initial_rejection(false, "NETWORK_UNCERTAIN:"));
        let op = Mutation::Restart {
            project: "demo".into(),
        }
        .plan()
        .unwrap();
        let legacy: Pending =
            serde_json::from_value(json!({"operation":op,"job_id":null})).unwrap();
        assert!(legacy.attempted);
    }
    #[test]
    fn active_app_stays_unlocked_for_a_full_workday() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        for second in 0..86400 {
            let elapsed = Duration::from_secs(second);
            assert!(!timer.observe(true, now + elapsed, wall + elapsed));
            assert!(timer.deadline.is_none());
        }
    }
    #[test]
    fn native_dialogs_are_activity_but_background_work_is_not() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        assert!(!timer.sample(true, false, 0, now, wall));
        // A biometric/file dialog may temporarily own focus for a long time.
        assert!(!timer.sample(
            false,
            true,
            0,
            now + Duration::from_secs(20),
            wall + Duration::from_secs(20)
        ));
        assert!(!timer.sample(
            true,
            false,
            1,
            now + Duration::from_secs(30),
            wall + Duration::from_secs(30)
        ));
        // Its closing epoch is consumed once, not on each subsequent sample.
        assert!(!timer.sample(
            false,
            false,
            1,
            now + Duration::from_secs(31),
            wall + Duration::from_secs(31)
        ));
        for sec in 32..331 {
            assert!(!timer.sample(
                false,
                false,
                1,
                now + Duration::from_secs(sec),
                wall + Duration::from_secs(sec)
            ));
        }
        assert!(timer.sample(
            false,
            false,
            1,
            now + Duration::from_secs(331),
            wall + Duration::from_secs(331)
        ));
    }
    #[test]
    fn sleeping_while_frontmost_still_revokes_session_on_return() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        assert!(!timer.observe(true, now, wall));
        assert!(timer.observe(
            true,
            now + Duration::from_secs(301),
            wall + Duration::from_secs(301)
        ));
    }
    #[test]
    fn returning_within_five_minutes_preserves_session_and_restarts_next_absence() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        assert!(!timer.observe(false, now, wall));
        let later = Duration::from_secs(299);
        assert!(!timer.observe(true, now + later, wall + later));
        assert!(timer.deadline.is_none());
        let next = Duration::from_secs(299);
        assert!(!timer.observe(false, now + next, wall + next));
        assert!(!timer.observe(false, now + next + later, wall + next + later));
        assert!(timer.observe(
            false,
            now + next + Duration::from_secs(300),
            wall + next + Duration::from_secs(300)
        ));
    }
    #[test]
    fn repeated_background_checks_do_not_extend_grace_and_late_return_locks_once() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        timer.observe(false, now, wall);
        for seconds in 1..300 {
            let d = Duration::from_secs(seconds);
            assert!(!timer.observe(false, now + d, wall + d));
        }
        let d = Duration::from_secs(301);
        assert!(timer.observe(true, now + d, wall + d));
        assert!(!timer.observe(true, now + d, wall + d));
        let mut timer = AwayTimer::default();
        timer.observe(false, now, wall);
        assert!(timer.observe(false, now + d, wall + d));
        assert!(!timer.observe(false, now + d + d, wall + d + d));
    }
    #[test]
    fn background_lock_covers_sleep_and_backward_clock_changes() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let elapsed = Duration::from_secs(301);
        let mut timer = AwayTimer::default();
        timer.observe(false, now, wall);
        assert!(timer.observe(true, now + Duration::from_secs(1), wall + elapsed));
        let mut timer = AwayTimer::default();
        timer.observe(false, now, wall);
        assert!(timer.observe(true, now + elapsed, wall - elapsed));
    }
    #[test]
    fn restored_pending_operations_cannot_change_authority_or_body() {
        let op = Mutation::Restart {
            project: "demo".into(),
        }
        .plan()
        .unwrap();
        assert!(validate_pending(&op).is_ok());
        let mut bad = op.clone();
        bad.target = "/v1/projects/other/stop".into();
        assert!(validate_pending(&bad).is_err());
        let mut bad = op.clone();
        bad.scopes = "deploy.stop".into();
        assert!(validate_pending(&bad).is_err());
        let mut bad = op.clone();
        bad.body = "{\"command\":\"rm\"}".into();
        assert!(validate_pending(&bad).is_err());
        let mut bad = op.clone();
        bad.idempotency = "op\nInjected".into();
        assert!(validate_pending(&bad).is_err());
    }
}
