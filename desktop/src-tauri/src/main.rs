#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
#![allow(unexpected_cfgs)] // objc 0.2 macros predate rustc check-cfg.
#[cfg(not(target_os = "macos"))]
compile_error!("Dockyard Desktop currently requires macOS Keychain and LocalAuthentication");
mod native;
mod protocol;
mod setup;
mod tls;

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
use zeroize::Zeroizing;

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
}
fn default_attempted() -> bool {
    true
}
struct Session {
    generation: u64,
    saved: Saved,
    client: reqwest::Client,
    secret: Zeroizing<Vec<u8>>,
    expires: Instant,
    expires_wall: SystemTime,
    api_origin: String,
    _tunnel: Option<setup::Tunnel>,
}
fn deadline_passed(monotonic: Instant, wall: SystemTime) -> bool {
    Instant::now() >= monotonic || SystemTime::now() >= wall
}
#[derive(Default)]
struct AwayTimer {
    deadline: Option<(Instant, SystemTime)>,
    locked: bool,
}
impl AwayTimer {
    fn observe(&mut self, focused: bool, now: Instant, wall: SystemTime) -> bool {
        let expired = self
            .deadline
            .is_some_and(|(mono, real)| now >= mono || wall >= real);
        if expired {
            self.deadline = None;
            self.locked = true;
        }
        if focused {
            self.deadline = None;
            self.locked = false;
        } else if self.deadline.is_none() && !self.locked {
            let grace = Duration::from_secs(60);
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
    setup: Arc<Mutex<Option<setup::Plan>>>,
    away: Arc<SyncMutex<AwayTimer>>,
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
            setup: Arc::new(Mutex::new(None)),
            away: Arc::new(SyncMutex::new(AwayTimer::default())),
        }
    }
    async fn clear(&self) {
        self.generation.fetch_add(1, Ordering::SeqCst);
        self.inner.lock().await.session = None;
        self.setup.lock().await.take();
    }
}
fn observe_focus(c: &Control, app: &tauri::AppHandle, focused: bool) {
    if c.native_prompt.load(Ordering::SeqCst) {
        return;
    }
    let expired = {
        let mut timer = c.away.lock().expect("focus timer poisoned");
        if c.native_prompt.load(Ordering::SeqCst) {
            return;
        }
        timer.observe(focused, Instant::now(), SystemTime::now())
    };
    if expired {
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
    if inner.session.as_ref().is_some_and(|s| {
        deadline_passed(s.expires, s.expires_wall)
            || s.generation != inner.generation.load(Ordering::SeqCst)
    }) {
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
        expires: Instant::now() + Duration::from_secs(300),
        expires_wall: SystemTime::now() + Duration::from_secs(300),
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
        let mode=if plan.policy.is_some() {"Your approved deployment policy"} else {"Inventory access only; deployments require an approved root policy"};
        let review=format!("Install Dockyard on {}:{} (Ubuntu {})\nSSH: {}\n\n{}\nInstall missing Docker/Compose/Caddy dependencies; preserve existing Compose files.\nBack up and validate Caddy before adding its private admin socket and managed import.\nInstall a 24/7 systemd service and restricted SSH connector.\nSave generated Mac credentials in Keychain before making server changes.\nRoot password is never saved.",plan.request.server_ip,plan.request.ssh_port,plan.version,plan.host,mode);
        if rfd::MessageDialog::new().set_title("Review Ubuntu server installation").set_description(review).set_buttons(rfd::MessageButtons::OkCancel).show()!=rfd::MessageDialogResult::Ok {return Err("Installation cancelled; no server changes made".into());}
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
        let bytes = native::load()?;
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
    let mut response = request.send().await.map_err(|_| "NETWORK_UNCERTAIN: check the tunnel, certificate and server; retry the saved operation for writes")?;
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
        // Never include raw responses or request bodies in an error (may contain secrets).
        let code: String = code
            .chars()
            .filter(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || *c == '_')
            .take(80)
            .collect();
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
        return Err("A native prompt is already open".into());
    }
    let _guard = PromptGuard(c.native_prompt.clone());
    // Dockyard's own Touch ID, review and file dialogs count as using the app.
    *c.away.lock().expect("focus timer poisoned") = AwayTimer::default();
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
    Ok(json!({"unlocked":unlocked,"profile":inner.profile,"jobs":jobs}))
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
        let path = rfd::FileDialog::new()
            .set_title("Enroll this Mac · select a private Dockyard enrollment")
            .add_filter("Enrollment", &["json"])
            .pick_file()
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
    let saved: Saved = native_task(&c, || {
        native::authenticate()?;
        let bytes = native::load()?;
        serde_json::from_slice(&bytes).map_err(|_| "Saved enrollment is invalid".into())
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
        let bytes = native::load()?;
        let saved: Saved =
            serde_json::from_slice(&bytes).map_err(|_| "Saved enrollment is invalid")?;
        if saved.pending.is_some() || saved.setup.is_some() {
            return Err("Resolve the saved operation before removing enrollment".into());
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
    let data = send(s, &op).await?;
    if deadline_passed(s.expires, s.expires_wall)
        || s.generation != c.generation.load(Ordering::SeqCst)
    {
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
    if !was_attempted {
        s.saved.pending.as_mut().unwrap().attempted = true;
        // Record that a request may reach the VPS before sending a single byte.
        if let Err(error) = persist(&s.saved) {
            s.saved.pending.as_mut().unwrap().attempted = false;
            return Err(error);
        }
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
            // A rejection on a retry says nothing about an earlier possibly accepted attempt.
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
async fn mutate(c: State<'_, Control>, mutation: Mutation) -> Result<Value, String> {
    let op = mutation.plan()?;
    let name = {
        let mut inner = c.inner.lock().await;
        let s = session(&mut inner)?;
        if s.saved.pending.is_some() {
            return Err("Finish or retry the saved operation before another write".into());
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
        op.project, op.action
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
    let approved = native_task(&c, move || {
        Ok(rfd::MessageDialog::new()
            .set_title("Confirm Dockyard operation")
            .set_description(review)
            .set_level(rfd::MessageLevel::Warning)
            .set_buttons(rfd::MessageButtons::OkCancel)
            .show()
            == rfd::MessageDialogResult::Ok)
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
    });
    if let Err(e) = persist(&s.saved) {
        s.saved.pending = None;
        return Err(e);
    }
    execute_pending(s).await
}
#[tauri::command]
async fn retry_pending(c: State<'_, Control>) -> Result<Value, String> {
    let mut inner = c.inner.lock().await;
    execute_pending(session(&mut inner)?).await
}
#[tauri::command]
async fn import_compose(c: State<'_, Control>) -> Result<Option<String>, String> {
    {
        let mut inner = c.inner.lock().await;
        session(&mut inner)?;
    }
    native_task(&c, || {
        use std::{io::Read, os::unix::fs::OpenOptionsExt};
        let Some(path) = rfd::FileDialog::new()
            .set_title("Import Docker Compose")
            .add_filter("Compose YAML", &["yaml", "yml"])
            .pick_file()
        else {
            return Ok(None);
        };
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(|_| "Cannot open Compose file")?;
        if !file
            .metadata()
            .map_err(|_| "Cannot inspect Compose file")?
            .is_file()
        {
            return Err("Choose a regular YAML file".into());
        }
        let mut bytes = Zeroizing::new(Vec::new());
        file.take(65537)
            .read_to_end(&mut bytes)
            .map_err(|_| "Cannot read Compose file")?;
        if bytes.len() > 65536 {
            return Err("Compose must be at most 64 KiB".into());
        }
        String::from_utf8(bytes.to_vec())
            .map(Some)
            .map_err(|_| "Compose must be UTF-8".into())
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
            mutate,
            retry_pending,
            pending_info,
            import_compose,
            setup_inspect,
            setup_install,
            setup_resume,
            setup_cancel
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
                    tokio::time::sleep(Duration::from_secs(1)).await;
                    if let Some(window) = focus_handle.get_webview_window("main") {
                        if let Ok(focused) = window.is_focused() {
                            observe_focus(&focus_control, &focus_handle, focused);
                        }
                    }
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
                    let expired = {
                        let mut inner = c.inner.lock().await;
                        if inner
                            .session
                            .as_ref()
                            .is_some_and(|s| deadline_passed(s.expires, s.expires_wall))
                        {
                            inner.session = None;
                            true
                        } else {
                            false
                        }
                    };
                    if expired {
                        let _ = handle.emit("session-locked", ());
                    }
                }
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            if matches!(event, tauri::WindowEvent::Destroyed) {
                window.app_handle().exit(0);
            }
            if let tauri::WindowEvent::Focused(focused) = event {
                let c = window.state::<Control>().inner().clone();
                observe_focus(&c, window.app_handle(), *focused);
            }
        })
        .run(tauri::generate_context!())
        .expect("Unable to run Dockyard");
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
            expires: Instant::now() + Duration::from_secs(300),
            expires_wall: SystemTime::now() + Duration::from_secs(300),
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
                .starts_with("NETWORK_UNCERTAIN:"));
            let mut bad_ca = e.clone();
            bad_ca.server_ca_pem = value["wrong_ca"].as_str().unwrap().into();
            assert!(send(&test_session(bad_ca), &projects)
                .await
                .unwrap_err()
                .starts_with("NETWORK_UNCERTAIN:"));
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
                        .starts_with("NETWORK_UNCERTAIN:"),
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
    fn session_deadline_survives_sleep_or_wall_clock_changes() {
        let duration = Duration::from_secs(60);
        assert!(deadline_passed(
            Instant::now() + duration,
            SystemTime::now() - duration
        ));
        assert!(deadline_passed(
            Instant::now() - duration,
            SystemTime::now() + duration
        ));
        assert!(!deadline_passed(
            Instant::now() + duration,
            SystemTime::now() + duration
        ));
    }
    #[test]
    fn returning_within_a_minute_preserves_session_and_restarts_next_absence() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        assert!(!timer.observe(false, now, wall));
        let later = Duration::from_secs(59);
        assert!(!timer.observe(true, now + later, wall + later));
        assert!(timer.deadline.is_none());
        let next = Duration::from_secs(90);
        assert!(!timer.observe(false, now + next, wall + next));
        assert!(!timer.observe(false, now + next + later, wall + next + later));
        assert!(timer.observe(
            false,
            now + next + Duration::from_secs(60),
            wall + next + Duration::from_secs(60)
        ));
    }
    #[test]
    fn repeated_background_checks_do_not_extend_grace_and_late_return_locks_once() {
        let (now, wall) = (Instant::now(), SystemTime::now());
        let mut timer = AwayTimer::default();
        timer.observe(false, now, wall);
        for seconds in 1..60 {
            let d = Duration::from_secs(seconds);
            assert!(!timer.observe(false, now + d, wall + d));
        }
        let d = Duration::from_secs(65);
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
        let elapsed = Duration::from_secs(61);
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
