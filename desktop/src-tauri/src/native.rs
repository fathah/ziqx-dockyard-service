use zeroize::Zeroizing;
const SERVICE: &str = "com.ziqx.dockyard.enrollment.v1";
const ACCOUNT: &str = "primary";

fn options() -> security_framework::passwords::PasswordOptions {
    let mut options =
        security_framework::passwords::PasswordOptions::new_generic_password(SERVICE, ACCOUNT);
    options.set_access_synchronized(Some(false));
    options
}
pub fn load() -> Result<Zeroizing<Vec<u8>>, String> {
    security_framework::passwords::generic_password(options())
        .map(Zeroizing::new)
        .map_err(|_| "No saved enrollment, or Keychain access was denied".into())
}
pub fn load_optional() -> Result<Option<Zeroizing<Vec<u8>>>, String> {
    match security_framework::passwords::generic_password(options()) {
        Ok(bytes) => Ok(Some(Zeroizing::new(bytes))),
        Err(error) if error.code() == -25300 => Ok(None), // errSecItemNotFound only
        Err(_) => Err("Keychain access was denied; existing enrollment cannot be replaced".into()),
    }
}
pub fn save(data: &[u8]) -> Result<(), String> {
    security_framework::passwords::set_generic_password_options(data, options())
        .map_err(|_| "macOS Keychain could not save the enrollment".into())
}
pub fn delete() -> Result<(), String> {
    security_framework::passwords::delete_generic_password_options(options())
        .map_err(|_| "macOS Keychain could not remove the enrollment".into())
}

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
pub fn authenticate_reason(reason_text: &str) -> Result<(), String> {
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
        // Hide the fallback button and require a fresh finger touch for every unlock.
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

/// Only Rust receives the password. AppKit owns the secure field on the main thread.
#[allow(deprecated)] // cocoa geometry encodes the existing objc 0.2 bridge's AppKit ABI.
pub fn root_password(app: &tauri::AppHandle, server: String) -> Result<Zeroizing<String>, String> {
    password_prompt(app, server, "Used for setup only; never saved.")
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
            "Used for this terminal connection only; never saved. This opens full administrative access."
        },
    )
}

#[allow(deprecated)]
fn password_prompt(
    app: &tauri::AppHandle,
    server: String,
    purpose: &'static str,
) -> Result<Zeroizing<String>, String> {
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
            let _: () = msg_send![alert, setMessageText:ns("Connect to your Ubuntu server")];
            let _: () = msg_send![alert, setInformativeText:ns(&format!("Root SSH password for {server}. {purpose} The SSH fingerprint has already been reviewed."))];
            let _: *mut Object = msg_send![alert, addButtonWithTitle:ns("Connect")];
            let _: *mut Object = msg_send![alert, addButtonWithTitle:ns("Cancel")];
            let field: *mut Object = msg_send![class!(NSSecureTextField), alloc];
            let field: *mut Object = msg_send![field, initWithFrame:NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(360.0, 28.0))];
            let _: () = msg_send![field, setPlaceholderString:ns("Root password")];
            let _: () = msg_send![alert, setAccessoryView:field];
            let window: *mut Object = msg_send![alert, window];
            let _: bool = msg_send![window, makeFirstResponder:field];
            let response: isize = msg_send![alert, runModal];
            let _: () = msg_send![window, endEditingFor:std::ptr::null_mut::<Object>()];
            let result = if response == 1000 {
                let value: *mut Object = msg_send![field, stringValue];
                let ptr: *const std::ffi::c_char = msg_send![value, UTF8String];
                let password = Zeroizing::new(std::ffi::CStr::from_ptr(ptr).to_string_lossy().into_owned());
                if password.is_empty() || password.len() > 1024 { Err("Enter a root password up to 1024 bytes".into()) } else { Ok(password) }
            } else { Err("SSH connection cancelled".into()) };
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
fn terminal_options(account: &str) -> security_framework::passwords::PasswordOptions {
    let mut options = security_framework::passwords::PasswordOptions::new_generic_password(
        "com.ziqx.dockyard.root-ssh.v1",
        account,
    );
    options.set_access_synchronized(Some(false));
    options
}
pub fn terminal_load(account: &str) -> Result<Option<Zeroizing<Vec<u8>>>, String> {
    match security_framework::passwords::generic_password(terminal_options(account)) {
        Ok(bytes) => Ok(Some(Zeroizing::new(bytes))),
        Err(error) if error.code() == -25300 => Ok(None),
        Err(_) => Err("Keychain denied access to the saved SSH password".into()),
    }
}
pub fn terminal_save(account: &str, password: &[u8]) -> Result<(), String> {
    security_framework::passwords::set_generic_password_options(password, terminal_options(account))
        .map_err(|_| "SSH connected, but Keychain could not save the password".into())
}
pub fn terminal_delete(account: &str) -> Result<(), String> {
    match security_framework::passwords::delete_generic_password_options(terminal_options(account))
    {
        Ok(()) => Ok(()),
        Err(error) if error.code() == -25300 => Ok(()),
        Err(_) => Err("Keychain could not remove the saved SSH password".into()),
    }
}
