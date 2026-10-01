use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
static PROMPT_DEPTH: AtomicUsize = AtomicUsize::new(0);
static PROMPT_EPOCH: AtomicU64 = AtomicU64::new(0);
struct PromptScope;
impl PromptScope {
    fn new() -> Self {
        PROMPT_DEPTH.fetch_add(1, Ordering::SeqCst);
        Self
    }
}
impl Drop for PromptScope {
    fn drop(&mut self) {
        PROMPT_EPOCH.fetch_add(1, Ordering::SeqCst);
        PROMPT_DEPTH.fetch_sub(1, Ordering::SeqCst);
    }
}
pub fn prompt_state() -> (bool, u64) {
    (
        PROMPT_DEPTH.load(Ordering::SeqCst) > 0,
        PROMPT_EPOCH.load(Ordering::SeqCst),
    )
}
pub fn with_prompt<T>(task: impl FnOnce() -> T) -> T {
    let _scope = PromptScope::new();
    task()
}

use zeroize::Zeroizing;
const SERVICE: &str = "com.ziqx.dockyard.enrollment.v1";
const ACCOUNT: &str = "primary";

static CREDENTIALS: std::sync::OnceLock<std::sync::Mutex<crate::credential_cache::CredentialCache>> = std::sync::OnceLock::new();
fn cache() -> &'static std::sync::Mutex<crate::credential_cache::CredentialCache> {
    CREDENTIALS.get_or_init(Default::default)
}
fn options(service: &str, account: &str) -> security_framework::passwords::PasswordOptions {
    let mut options = security_framework::passwords::PasswordOptions::new_generic_password(service, account);
    options.set_access_synchronized(Some(false));
    options
}
pub fn credential_load(service: &str, account: &str) -> Result<Option<Zeroizing<Vec<u8>>>, String> {
    cache().lock().map_err(|_| "Credential cache unavailable")?.read(service, account, || {
        with_prompt(|| match security_framework::passwords::generic_password(options(service, account)) {
            Ok(bytes) => Ok(Some(Zeroizing::new(bytes))),
            Err(e) if e.code() == -25300 => Ok(None),
            Err(_) => Err("Keychain access was denied. Unlock Dockyard again to retry".into()),
        })
    })
}
pub fn credential_save(service: &str, account: &str, data: &[u8]) -> Result<(), String> {
    let mut values = cache().lock().map_err(|_| "Credential cache unavailable")?;
    with_prompt(|| security_framework::passwords::set_generic_password_options(data, options(service, account)))
        .map_err(|_| "macOS Keychain could not save the credential")?;
    values.set(service, account, Some(Zeroizing::new(data.to_vec())));
    Ok(())
}
fn credential_delete(service: &str, account: &str) -> Result<(), String> {
    let mut values = cache().lock().map_err(|_| "Credential cache unavailable")?;
    match with_prompt(|| security_framework::passwords::delete_generic_password_options(options(service, account))) {
        Ok(()) => (),
        Err(e) if e.code() == -25300 => (),
        Err(_) => return Err("Keychain could not remove the credential".into()),
    }
    values.set(service, account, None);
    Ok(())
}
pub fn clear_credential_cache() {
    clear_authentication();
    // Never block the main-thread exit on a Keychain dialog running on a worker.
    // Process exit releases all remaining memory if a read is still in flight.
    if let Ok(mut values) = cache().try_lock() { values.clear(); }
}
// Check only metadata before Touch ID; never retrieve secret bytes on the lock screen.
pub fn enrollment_exists() -> Result<bool, String> {
    use security_framework::item::{ItemClass, ItemSearchOptions};
    match ItemSearchOptions::new().class(ItemClass::generic_password()).service(SERVICE).account(ACCOUNT).load_attributes(true).load_data(false).search() {
        Ok(items) => Ok(!items.is_empty()),
        Err(e) if e.code() == -25300 => Ok(false),
        Err(_) => Err("Cannot check saved enrollment".into()),
    }
}
pub fn load() -> Result<Zeroizing<Vec<u8>>, String> {
    load_optional()?.ok_or("No saved enrollment".into())
}
pub fn load_optional() -> Result<Option<Zeroizing<Vec<u8>>>, String> {
    credential_load(SERVICE, ACCOUNT)
}
pub fn save(data: &[u8]) -> Result<(), String> { credential_save(SERVICE, ACCOUNT, data) }
pub fn delete() -> Result<(), String> { credential_delete(SERVICE, ACCOUNT) }

// macOS owns this prompt. JavaScript never receives biometric data or an unlock token.
#[link(name = "LocalAuthentication", kind = "framework")]
extern "C" {}
#[link(name = "Foundation", kind = "framework")]
extern "C" {}
// LocalAuthentication constants from Apple's SDK. Never use policy 2 (password/Watch fallback).
const TOUCH_ID_POLICY: isize = 1; // LAPolicyDeviceOwnerAuthenticationWithBiometrics
const TOUCH_ID_TYPE: isize = 1; // LABiometryTypeTouchID

fn authentication_error(code: isize) -> &'static str {
    match code {
        -6 | -12 | -13 => "Touch ID is unavailable. Use a Mac with an enabled, connected Touch ID sensor.",
        -7 => "Set up a fingerprint in System Settings → Touch ID & Password, then try again.",
        -8 => "Touch ID is locked by macOS. Unlock your Mac with its login password to restore Touch ID, then try again. Dockyard remains locked.",
        -1 => "Touch ID did not recognize your fingerprint. Try again.",
        _ => "Touch ID authentication was cancelled or denied. Dockyard remains locked.",
    }
}

pub fn authenticate() -> Result<(), String> {
    authenticate_reason("Unlock Dockyard to manage your VPS")
}
static AUTHENTICATION: std::sync::OnceLock<std::sync::Mutex<crate::auth_window::AuthWindow>> = std::sync::OnceLock::new();
fn authentication() -> &'static std::sync::Mutex<crate::auth_window::AuthWindow> {
    AUTHENTICATION.get_or_init(Default::default)
}
pub fn clear_authentication() {
    authentication().lock().unwrap_or_else(|e| e.into_inner()).clear();
}
pub fn authenticate_reason(reason_text: &str) -> Result<(), String> {
    use std::time::{Instant, SystemTime};
    let epoch = authentication().lock().map_err(|_| "Authentication unavailable")?
        .begin(Instant::now(), SystemTime::now());
    let Some(epoch) = epoch else { return Ok(()); };
    let result = authenticate_fresh(reason_text);
    let current = authentication().lock().map_err(|_| "Authentication unavailable")?
        .complete(epoch, result.is_ok(), Instant::now(), SystemTime::now());
    if !current { return Err("SESSION_LOCKED".into()); }
    result
}
fn authenticate_fresh(reason_text: &str) -> Result<(), String> {
    let _prompt = PromptScope::new();
    use objc::runtime::{Object, BOOL, YES};
    use objc::{class, msg_send, sel, sel_impl};
    let (tx, rx) = std::sync::mpsc::channel();
    unsafe {
        let context: *mut Object = msg_send![class!(LAContext), new];
        if context.is_null() {
            return Err("macOS could not start Touch ID authentication".into());
        }
        let mut error: *mut Object = std::ptr::null_mut();
        let available: BOOL =
            msg_send![context, canEvaluatePolicy:TOUCH_ID_POLICY error:&mut error];
        let biometry: isize = msg_send![context, biometryType];
        if available != YES || biometry != TOUCH_ID_TYPE {
            let code: isize = if error.is_null() {
                -6
            } else {
                msg_send![error, code]
            };
            let _: () = msg_send![context, invalidate];
            let _: () = msg_send![context, release];
            return Err(authentication_error(code).into());
        }
        // Expired/cleared app approval requires a fresh finger touch, never Mac-unlock reuse.
        let empty: *mut Object = msg_send![class!(NSString), string];
        let _: () = msg_send![context, setLocalizedFallbackTitle:empty];
        let _: () = msg_send![context, setTouchIDAuthenticationAllowableReuseDuration:0.0f64];
        let reason: *mut Object = msg_send![class!(NSString), alloc];
        let text =
            std::ffi::CString::new(reason_text).expect("validated native authentication reason");
        let reason: *mut Object = msg_send![reason, initWithUTF8String:text.as_ptr()];
        let reply = block::ConcreteBlock::new(move |success: BOOL, error: *mut Object| {
            let result = if success == YES {
                Ok(())
            } else {
                let code: isize = if error.is_null() {
                    0
                } else {
                    msg_send![error, code]
                };
                Err(authentication_error(code).to_owned())
            };
            let _ = tx.send(result);
        })
        .copy();
        let _: () =
            msg_send![context, evaluatePolicy:TOUCH_ID_POLICY localizedReason:reason reply:&*reply];
        let result = rx.recv_timeout(std::time::Duration::from_secs(120));
        let _: () = msg_send![context, invalidate];
        let _: () = msg_send![context, release];
        let _: () = msg_send![reason, release];
        result.unwrap_or_else(|_| {
            Err("Touch ID authentication timed out. Dockyard remains locked.".into())
        })
    }
}

pub fn terminal_password(
    app: &tauri::AppHandle,
    server: String,
    remember: bool,
) -> Result<Zeroizing<String>, String> {
    password_prompt(
        app,
        server,
        if remember {
            "Saved in this Mac’s Keychain only after successful SSH authentication. This opens full administrative access."
        } else {
            "Kept in memory until Dockyard closes; not saved to disk. This opens full administrative access."
        },
    )
}

#[allow(deprecated)]
fn password_prompt(
    app: &tauri::AppHandle,
    server: String,
    purpose: &'static str,
) -> Result<Zeroizing<String>, String> {
    secure_prompt(app, "Connect to your Ubuntu server", format!("Root SSH password for {server}. {purpose} The SSH fingerprint has already been reviewed."), "Root password", "Connect")
}

pub fn cloudflare_token(app: &tauri::AppHandle) -> Result<Zeroizing<String>, String> {
    secure_prompt(app, "Connect Cloudflare", "Paste a user API token with Zone Read and DNS Edit permissions for the domains you want to manage. Saved only in this Mac’s Keychain.".into(), "Cloudflare API token", "Connect")
}

#[allow(deprecated)]
fn secure_prompt(
    app: &tauri::AppHandle,
    title: &'static str,
    detail: String,
    placeholder: &'static str,
    action: &'static str,
) -> Result<Zeroizing<String>, String> {
    let _prompt = PromptScope::new();
    let (tx, rx) = std::sync::mpsc::channel();
    app.run_on_main_thread(move || {
        use cocoa::foundation::{NSPoint, NSRect, NSSize};
        use objc::runtime::Object;
        use objc::{class, msg_send, sel, sel_impl};
        unsafe {
            let pool: *mut Object = msg_send![class!(NSAutoreleasePool), new];
            let ns = |text: &str| -> *mut Object {
                let text = std::ffi::CString::new(text).expect("fixed native prompt text");
                msg_send![class!(NSString), stringWithUTF8String:text.as_ptr()]
            };
            let alert: *mut Object = msg_send![class!(NSAlert), new];
            let _: () = msg_send![alert, setMessageText:ns(title)];
            let _: () = msg_send![alert, setInformativeText:ns(&detail)];
            let _: *mut Object = msg_send![alert, addButtonWithTitle:ns(action)];
            let _: *mut Object = msg_send![alert, addButtonWithTitle:ns("Cancel")];
            let field: *mut Object = msg_send![class!(NSSecureTextField), alloc];
            let field: *mut Object = msg_send![field, initWithFrame:NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(360.0, 28.0))];
            let _: () = msg_send![field, setPlaceholderString:ns(placeholder)];
            let _: () = msg_send![alert, setAccessoryView:field];
            let window: *mut Object = msg_send![alert, window];
            let _: bool = msg_send![window, makeFirstResponder:field];
            let response: isize = msg_send![alert, runModal];
            let _: () = msg_send![window, endEditingFor:std::ptr::null_mut::<Object>()];
            let result = if response == 1000 {
                let value: *mut Object = msg_send![field, stringValue];
                let ptr: *const std::ffi::c_char = msg_send![value, UTF8String];
                let password = Zeroizing::new(std::ffi::CStr::from_ptr(ptr).to_string_lossy().into_owned());
                if password.is_empty() || password.len() > 1024 { Err("Enter a value up to 1024 bytes".into()) } else { Ok(password) }
            } else { Err("Connection cancelled".into()) };
            let _: () = msg_send![field, setStringValue:ns("")];
            let _: () = msg_send![field, release];
            let _: () = msg_send![alert, release];
            let _: () = msg_send![pool, drain];
            let _ = tx.send(result);
        }
    }).map_err(|_| "Could not open the native password prompt")?;
    rx.recv().map_err(|_| "Native password prompt failed")?
}

// Separate from enrollment: scoped to the pinned endpoint, never synchronized to iCloud.
const ROOT_SERVICE: &str = "com.ziqx.dockyard.root-ssh.v1";
pub fn terminal_load(account: &str) -> Result<Option<Zeroizing<Vec<u8>>>, String> {
    credential_load(ROOT_SERVICE, account)
}
pub fn terminal_save(account: &str, password: &[u8]) -> Result<(), String> {
    credential_save(ROOT_SERVICE, account, password)
}
pub fn terminal_remember_for_run(account: &str, password: &[u8]) -> Result<(), String> {
    cache().lock().map_err(|_| "Credential cache unavailable")?.set(ROOT_SERVICE, account, Some(Zeroizing::new(password.to_vec())));
    Ok(())
}
pub fn terminal_invalidate(account: &str) {
    if let Ok(mut values) = cache().lock() { values.set(ROOT_SERVICE, account, None); }
}
pub fn terminal_delete(account: &str) -> Result<(), String> { credential_delete(ROOT_SERVICE, account) }
pub fn root_credential(app: &tauri::AppHandle, server: String, account: &str, remember: bool) -> Result<Zeroizing<String>, String> {
    if let Some(bytes) = terminal_load(account)? {
        if bytes.is_empty() || bytes.len() > 1024 { return Err("Saved root password is invalid; forget it in Terminal".into()); }
        return Ok(Zeroizing::new(std::str::from_utf8(&bytes).map_err(|_| "Saved root password is invalid")?.to_owned()));
    }
    terminal_password(app, server, remember)
}

/// Main-thread AppKit activation, independent of which Dockyard control has focus.
pub fn app_is_active() -> bool {
    use objc::runtime::{Object, BOOL, YES};
    use objc::{class, msg_send, sel, sel_impl};
    unsafe {
        let app: *mut Object = msg_send![class!(NSApplication), sharedApplication];
        let active: BOOL = msg_send![app, isActive];
        active == YES
    }
}
