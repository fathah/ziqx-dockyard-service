//! Desktop-owned DNS credentials. Nothing is uploaded to the VPS or returned to JavaScript.
use crate::{native, native_task, session, terminal::Lease, Control};
use reqwest::{Client, Method};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::time::Duration;
use tauri::State;
use zeroize::{Zeroize, ZeroizeOnDrop, Zeroizing};

const API: &str = "https://api.cloudflare.com/client/v4";
const TOKEN_PAGE: &str = "https://dash.cloudflare.com/profile/api-tokens";
const MAX_RESPONSE: usize = 4 * 1024 * 1024;

#[derive(Clone, Serialize, Deserialize)]
pub struct Provider {
    id: String,
    name: String,
    kind: String,
}
#[derive(Serialize, Deserialize, Zeroize, ZeroizeOnDrop)]
struct Credential {
    #[zeroize(skip)]
    provider: Provider,
    token: String,
}
const SERVICE: &str = "com.ziqx.dockyard.domain-providers.v1";
fn load() -> Result<Vec<Credential>, String> {
    match native::credential_load(SERVICE, "providers")? {
        Some(bytes) => serde_json::from_slice(&bytes).map_err(|_| "Saved domain providers could not be read".into()),
        None => Ok(vec![]),
    }
}
fn save(entries: &[Credential]) -> Result<(), String> {
    let bytes = Zeroizing::new(serde_json::to_vec(entries).map_err(|_| "Could not encode domain providers")?);
    native::credential_save(SERVICE, "providers", &bytes)
}
async fn authority(c: &Control) -> Result<Lease, String> {
    let mut inner = c.inner.lock().await;
    let s = session(&mut inner)?;
    Ok(Lease {
        generation: c.generation.clone(),
        expected: s.generation,
    })
}
fn credential(id: &str) -> Result<Credential, String> {
    load()?
        .into_iter()
        .find(|p| p.provider.id == id)
        .ok_or("Connect this provider in Settings first".into())
}
fn client() -> Result<Client, String> {
    Client::builder()
        .https_only(true)
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(30))
        .build()
        .map_err(|_| "Could not create the Cloudflare connection".into())
}
fn id(value: &str) -> Result<(), String> {
    if value.len() == 32 && value.bytes().all(|c| c.is_ascii_hexdigit()) {
        Ok(())
    } else {
        Err("Invalid Cloudflare resource ID".into())
    }
}
fn page(value: u32) -> Result<(), String> {
    if (1..=100_000).contains(&value) {
        Ok(())
    } else {
        Err("Invalid page".into())
    }
}
fn decode(status: u16, bytes: &[u8]) -> Result<Value, String> {
    // Never forward Cloudflare's raw response: it may echo sensitive request contents.
    if status == 401 || status == 403 {
        return Err("Cloudflare denied access. Reconnect with a valid token granting Zone Read and DNS Edit for this domain.".into());
    }
    if status == 429 {
        return Err("Cloudflare is rate limiting requests. Wait a moment, then refresh.".into());
    }
    if status == 404 {
        return Err("This domain or record is no longer available. Refresh the list.".into());
    }
    let value: Value = serde_json::from_slice(bytes).map_err(|_| {
        "Cloudflare returned an unreadable response. Refresh before retrying a change."
    })?;
    if !(200..300).contains(&status) || value["success"] != true {
        let codes = value["errors"]
            .as_array()
            .map(|errors| {
                errors
                    .iter()
                    .filter_map(|e| e["code"].as_u64())
                    .take(5)
                    .map(|n| n.to_string())
                    .collect::<Vec<_>>()
                    .join(", ")
            })
            .unwrap_or_default();
        return Err(format!("Cloudflare rejected the request (HTTP {status}{}). Check the record values, conflicting records and token permissions.", if codes.is_empty() { String::new() } else { format!(", code {codes}") }));
    }
    Ok(value)
}
async fn request(
    http: &Client,
    token: &str,
    method: Method,
    path: &str,
    body: Option<&Value>,
    lease: &Lease,
) -> Result<Value, String> {
    request_url(http, token, method, &format!("{API}{path}"), body, lease).await
}
// URL stays internal; production callers only use the fixed Cloudflare API above.
async fn request_url(
    http: &Client,
    token: &str,
    method: Method,
    url: &str,
    body: Option<&Value>,
    lease: &Lease,
) -> Result<Value, String> {
    lease.check()?;
    let mut authorization = reqwest::header::HeaderValue::from_str(&format!("Bearer {token}"))
        .map_err(|_| "Invalid API token")?;
    authorization.set_sensitive(true);
    let mut req = http
        .request(method, url)
        .header(reqwest::header::AUTHORIZATION, authorization);
    if let Some(body) = body {
        req = req.json(body);
    }
    let mut response = req.send().await.map_err(|_| "Could not reach Cloudflare. If you were saving a change, refresh DNS records before retrying; it may have succeeded.")?;
    let status = response.status().as_u16();
    let mut bytes = Zeroizing::new(Vec::new());
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|_| "Cloudflare response interrupted. Refresh before retrying a change.")?
    {
        if bytes.len() + chunk.len() > MAX_RESPONSE {
            return Err("Cloudflare response is too large".into());
        }
        bytes.extend_from_slice(&chunk);
    }
    lease.check()?;
    decode(status, &bytes)
}
#[derive(Serialize)]
pub struct Listing {
    items: Vec<Value>,
    page: u32,
    total_pages: u32,
    total: u64,
}
fn listing(response: Value, page: u32) -> Result<Listing, String> {
    let items = response["result"]
        .as_array()
        .ok_or("Cloudflare returned an invalid list")?
        .clone();
    Ok(Listing {
        items,
        page,
        total_pages: response["result_info"]["total_pages"]
            .as_u64()
            .unwrap_or(1)
            .clamp(1, 100_000) as u32,
        total: response["result_info"]["total_count"].as_u64().unwrap_or(0),
    })
}
#[tauri::command]
pub async fn provider_list(c: State<'_, Control>) -> Result<Vec<Provider>, String> {
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    lease.check()?;
    let result = load()?.into_iter().map(|x| x.provider.clone()).collect();
    lease.check()?;
    Ok(result)
}
#[tauri::command]
pub async fn provider_token_page(c: State<'_, Control>) -> Result<(), String> {
    authority(&c).await?.check()?;
    let status = std::process::Command::new("/usr/bin/open")
        .arg(TOKEN_PAGE)
        .status()
        .map_err(|_| "Could not open Cloudflare in your browser")?;
    if status.success() {
        Ok(())
    } else {
        Err("Could not open Cloudflare in your browser".into())
    }
}
#[tauri::command]
pub async fn provider_connect(
    app: tauri::AppHandle,
    c: State<'_, Control>,
    name: String,
    replace: Option<String>,
) -> Result<Provider, String> {
    let name = name.trim().to_string();
    if name.is_empty() || name.len() > 80 || name.chars().any(char::is_control) {
        return Err("Enter a provider name up to 80 characters".into());
    }
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    lease.check()?;
    let mut entries = load()?;
    if let Some(ref replace) = replace {
        if !entries.iter().any(|x| &x.provider.id == replace) {
            return Err("Provider not found".into());
        }
    } else if entries.len() >= 20 {
        return Err("You can connect up to 20 providers".into());
    }
    let check = lease.clone();
    let token = native_task(&c, move || {
        check.check()?;
        native::authenticate_reason("Connect a Cloudflare account to Dockyard")?;
        check.check()?;
        native::cloudflare_token(&app)
    })
    .await?;
    let token = Zeroizing::new(token.trim().to_string());
    if token.is_empty()
        || token
            .bytes()
            .any(|c| c.is_ascii_whitespace() || c.is_ascii_control())
    {
        return Err("Paste the API token only".into());
    }
    let http = client()?;
    let verified = request(
        &http,
        &token,
        Method::GET,
        "/user/tokens/verify",
        None,
        &lease,
    )
    .await?;
    if verified["result"]["status"] != "active" {
        return Err("This Cloudflare user token is not active".into());
    }
    // Verify domain discovery separately; token verification alone does not prove permissions.
    request(
        &http,
        &token,
        Method::GET,
        "/zones?per_page=1&page=1",
        None,
        &lease,
    )
    .await?;
    let provider = Provider {
        id: replace.unwrap_or_else(|| uuid::Uuid::new_v4().to_string()),
        name,
        kind: "cloudflare".into(),
    };
    entries.retain(|p| p.provider.id != provider.id);
    entries.push(Credential {
        provider: provider.clone(),
        token: token.to_string(),
    });
    lease.check()?;
    save(&entries)?;
    Ok(provider)
}
#[tauri::command]
pub async fn provider_remove(c: State<'_, Control>, provider: String) -> Result<(), String> {
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    native_task(&c, move || {
        lease.check()?;
        native::authenticate_reason("Remove this Mac’s saved Cloudflare connection")?;
        lease.check()?;
        let mut entries = load()?;
        entries.retain(|p| p.provider.id != provider);
        save(&entries)
    })
    .await
}
#[tauri::command]
pub async fn provider_zones(
    c: State<'_, Control>,
    provider: String,
    page_number: u32,
) -> Result<Listing, String> {
    page(page_number)?;
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    lease.check()?;
    let p = credential(&provider)?;
    listing(
        request(
            &client()?,
            &p.token,
            Method::GET,
            &format!("/zones?per_page=50&page={page_number}&order=name&direction=asc"),
            None,
            &lease,
        )
        .await?,
        page_number,
    )
}
#[tauri::command]
pub async fn provider_records(
    c: State<'_, Control>,
    provider: String,
    zone: String,
    page_number: u32,
) -> Result<Listing, String> {
    id(&zone)?;
    page(page_number)?;
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    lease.check()?;
    let p = credential(&provider)?;
    listing(request(&client()?, &p.token, Method::GET, &format!("/zones/{zone}/dns_records?per_page=100&page={page_number}&order=name&direction=asc"), None, &lease).await?, page_number)
}

#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RecordInput {
    #[serde(rename = "type")]
    kind: String,
    name: String,
    content: String,
    ttl: u32,
    proxied: bool,
    priority: Option<u16>,
    data: Option<Value>,
}
fn record_body(input: &RecordInput, zone: &str) -> Result<Value, String> {
    let name = input.name.trim().trim_end_matches('.').to_ascii_lowercase();
    let zone = zone.trim_end_matches('.').to_ascii_lowercase();
    if name.len() > 253
        || name.is_empty()
        || !name.is_ascii()
        || name
            .bytes()
            .any(|b| !b.is_ascii_alphanumeric() && !b"-._*".contains(&b))
        || !(name == zone || name.ends_with(&format!(".{zone}")))
    {
        return Err(
            "Enter a full DNS name belonging to this domain (use punycode for international names)"
                .into(),
        );
    }
    if input.ttl != 1 && !(60..=86400).contains(&input.ttl) {
        return Err("TTL must be Auto or 60–86400 seconds".into());
    }
    if input.content.len() > 65536 || input.content.contains('\0') {
        return Err("Record content is too large or invalid".into());
    }
    if input.proxied && !matches!(input.kind.as_str(), "A" | "AAAA" | "CNAME") {
        return Err("Only A, AAAA and CNAME records can be proxied".into());
    }
    match input.kind.as_str() {
        "A" => {
            input
                .content
                .parse::<std::net::Ipv4Addr>()
                .map_err(|_| "Enter a valid IPv4 address")?;
        }
        "AAAA" => {
            input
                .content
                .parse::<std::net::Ipv6Addr>()
                .map_err(|_| "Enter a valid IPv6 address")?;
        }
        "CNAME" | "NS" | "MX" => {
            if input.content.is_empty()
                || input.content.len() > 253
                || input.content.chars().any(char::is_whitespace)
            {
                return Err("Enter a valid target hostname".into());
            }
        }
        "TXT" => {}
        "CAA" | "SRV" => {}
        _ => return Err("This record type can be viewed here; edit it in Cloudflare".into()),
    }
    let mut body =
        json!({"type":input.kind,"name":name,"ttl":if input.proxied {1} else {input.ttl}});
    if matches!(input.kind.as_str(), "A" | "AAAA" | "CNAME") {
        body["proxied"] = json!(input.proxied);
    }
    if input.kind == "MX" {
        body["priority"] = json!(input.priority.ok_or("MX priority is required")?);
    }
    if matches!(input.kind.as_str(), "CAA" | "SRV") {
        let d = input
            .data
            .as_ref()
            .and_then(Value::as_object)
            .ok_or("Record details are required")?;
        let integer = |key: &str, max: u64| -> Result<u64, String> {
            d.get(key)
                .and_then(Value::as_u64)
                .filter(|v| *v <= max)
                .ok_or(format!("Invalid {key}"))
        };
        let text = |key: &str| -> Result<String, String> {
            d.get(key)
                .and_then(Value::as_str)
                .filter(|s| !s.is_empty() && s.len() <= 2048 && !s.chars().any(char::is_control))
                .map(str::to_owned)
                .ok_or(format!("Invalid {key}"))
        };
        body["data"] = if input.kind == "CAA" {
            let tag = text("tag")?;
            if !matches!(tag.as_str(), "issue" | "issuewild" | "iodef") {
                return Err("Invalid CAA tag".into());
            }
            json!({"flags":integer("flags",255)?,"tag":tag,"value":text("value")?})
        } else {
            json!({"priority":integer("priority",65535)?,"weight":integer("weight",65535)?,"port":integer("port",65535)?,"target":text("target")?})
        };
    } else {
        body["content"] = json!(input.content);
    }
    Ok(body)
}
#[derive(Deserialize)]
#[serde(tag = "action", rename_all = "snake_case", deny_unknown_fields)]
pub enum Change {
    Create {
        record: RecordInput,
    },
    Update {
        id: String,
        modified_on: String,
        record: RecordInput,
    },
    Delete {
        id: String,
        modified_on: String,
    },
}
#[tauri::command]
pub async fn provider_write(
    c: State<'_, Control>,
    provider: String,
    zone: String,
    change: Change,
) -> Result<(), String> {
    id(&zone)?;
    let lease = authority(&c).await?;
    let _gate = c.provider_gate.lock().await;
    lease.check()?;
    let p = credential(&provider)?;
    let http = client()?;
    let zone_info = request(
        &http,
        &p.token,
        Method::GET,
        &format!("/zones/{zone}"),
        None,
        &lease,
    )
    .await?;
    let zone_name = zone_info["result"]["name"]
        .as_str()
        .ok_or("Cloudflare returned an invalid domain")?;
    let (record_id, expected) = match &change {
        Change::Create { .. } => (None, None),
        Change::Update {
            id, modified_on, ..
        }
        | Change::Delete { id, modified_on } => {
            self::id(id)?;
            (Some(id), Some(modified_on))
        }
    };
    let existing = if let Some(id) = record_id {
        Some(
            request(
                &http,
                &p.token,
                Method::GET,
                &format!("/zones/{zone}/dns_records/{id}"),
                None,
                &lease,
            )
            .await?["result"]
                .clone(),
        )
    } else {
        None
    };
    if let (Some(old), Some(expected)) = (&existing, expected) {
        if expected.is_empty() || old["modified_on"].as_str() != Some(expected.as_str()) {
            return Err(
                "This record changed since you opened it. Refresh and review it again.".into(),
            );
        }
    }
    let (method, body, reason) = match &change {
        Change::Create { record } => (
            Method::POST,
            Some(record_body(record, zone_name)?),
            format!("Create {} DNS record for {}", record.kind, record.name),
        ),
        Change::Update { record, .. } => (
            Method::PATCH,
            Some(record_body(record, zone_name)?),
            format!("Update {} DNS record for {}", record.kind, record.name),
        ),
        Change::Delete { .. } => (
            Method::DELETE,
            None,
            format!(
                "Delete DNS record for {}",
                existing
                    .as_ref()
                    .and_then(|v| v["name"].as_str())
                    .unwrap_or(zone_name)
            ),
        ),
    };
    let check = lease.clone();
    native_task(&c, move || {
        check.check()?;
        native::authenticate_reason(&reason)?;
        check.check()
    })
    .await?;
    // Recheck after the biometric dialog as the user may leave it open.
    if let (Some(id), Some(expected)) = (record_id, expected) {
        let current = request(
            &http,
            &p.token,
            Method::GET,
            &format!("/zones/{zone}/dns_records/{id}"),
            None,
            &lease,
        )
        .await?;
        if current["result"]["modified_on"].as_str() != Some(expected.as_str()) {
            return Err(
                "This record changed during confirmation. Refresh and review it again.".into(),
            );
        }
    }
    let path = format!(
        "/zones/{zone}/dns_records{}",
        record_id.map(|id| format!("/{id}")).unwrap_or_default()
    );
    request(&http, &p.token, method, &path, body.as_ref(), &lease).await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn record(kind: &str, content: &str) -> RecordInput {
        RecordInput {
            kind: kind.into(),
            name: "app.example.com".into(),
            content: content.into(),
            ttl: 1,
            proxied: false,
            priority: None,
            data: None,
        }
    }
    fn test_lease() -> Lease {
        Lease {
            generation: std::sync::Arc::new(std::sync::atomic::AtomicU64::new(1)),
            expected: 1,
        }
    }
    fn fixture(response: String) -> (String, std::thread::JoinHandle<String>) {
        use std::io::{Read, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!(
            "http://{}/zones/test/dns_records?page=2",
            listener.local_addr().unwrap()
        );
        let thread = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut bytes = Vec::new();
            let mut block = [0u8; 1024];
            loop {
                let n = stream.read(&mut block).unwrap();
                if n == 0 {
                    break;
                };
                bytes.extend_from_slice(&block[..n]);
                if bytes.windows(4).any(|x| x == b"\r\n\r\n") {
                    break;
                };
            }
            let _ = stream.write_all(response.as_bytes());
            String::from_utf8(bytes).unwrap()
        });
        (url, thread)
    }
    #[test]
    fn transport_auth_no_redirects_and_revoked_session() {
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        rt.block_on(async {
            let http=Client::builder().no_proxy().redirect(reqwest::redirect::Policy::none()).timeout(Duration::from_secs(5)).build().unwrap();
            let payload=r#"{"success":true,"result":[],"result_info":{"total_pages":2,"total_count":120}}"#;
            let (url,server)=fixture(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",payload.len(),payload));
            let lease=test_lease();
            let result=request_url(&http,"fake-test-token",Method::GET,&url,None,&lease).await.unwrap();
            assert_eq!(result["result_info"]["total_count"],120);
            let wire=server.join().unwrap();
            assert!(wire.to_lowercase().contains("authorization: bearer fake-test-token"));
            assert!(wire.starts_with("GET /zones/test/dns_records?page=2"));
            let (url,server)=fixture("HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1:1/should-never-be-requested\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}".into());
            let error=request_url(&http,"fake-test-token",Method::GET,&url,None,&lease).await.unwrap_err();
            assert!(error.contains("302")); server.join().unwrap();
            lease.generation.store(2,std::sync::atomic::Ordering::SeqCst);
            assert_eq!(request_url(&http,"fake",Method::GET,"http://127.0.0.1:1",None,&lease).await.unwrap_err(),"SESSION_LOCKED");
        });
    }
    #[test]
    fn transport_rejects_oversized_response() {
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        rt.block_on(async {
            let http = Client::builder()
                .no_proxy()
                .timeout(Duration::from_secs(5))
                .build()
                .unwrap();
            let body = "x".repeat(MAX_RESPONSE + 1);
            let (url, server) = fixture(format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            ));
            assert!(
                request_url(&http, "fake", Method::GET, &url, None, &test_lease())
                    .await
                    .unwrap_err()
                    .contains("too large")
            );
            server.join().unwrap();
        });
    }
    #[test]
    fn rejects_path_injection_and_bad_pages() {
        for bad in ["../zones", "a/b", "", "abc?x=y"] {
            assert!(id(bad).is_err());
        }
        assert!(page(0).is_err());
        assert!(id(&"a".repeat(32)).is_ok());
    }
    #[test]
    fn validates_zone_and_ip_and_proxy() {
        let mut r = record("A", "127.0.0.1");
        assert!(record_body(&r, "example.com").is_ok());
        r.name = "example.com.evil.test".into();
        assert!(record_body(&r, "example.com").is_err());
        r.name = "example.com".into();
        r.content = "not-ip".into();
        assert!(record_body(&r, "example.com").is_err());
        let mut t = record("TXT", "v=spf1 -all");
        t.proxied = true;
        assert!(record_body(&t, "example.com").is_err());
    }
    #[test]
    fn preserves_txt_and_serializes_mx_srv_caa() {
        assert_eq!(
            record_body(&record("TXT", "a\"b"), "example.com").unwrap()["content"],
            "a\"b"
        );
        let mut mx = record("MX", "mail.example.com");
        assert!(record_body(&mx, "example.com").is_err());
        mx.priority = Some(10);
        assert_eq!(record_body(&mx, "example.com").unwrap()["priority"], 10);
        let mut srv = record("SRV", "");
        srv.data = Some(json!({"priority":10,"weight":0,"port":443,"target":"app.example.com"}));
        assert_eq!(
            record_body(&srv, "example.com").unwrap()["data"]["port"],
            443
        );
        let mut caa = record("CAA", "");
        caa.data = Some(json!({"flags":0,"tag":"issue","value":"letsencrypt.org"}));
        assert!(record_body(&caa, "example.com").is_ok());
    }
    #[test]
    fn errors_never_expose_response_contents() {
        let secret = br#"{"success":false,"errors":[{"code":1000,"message":"SECRET_TOKEN"}]}"#;
        for status in [200, 400, 401, 403, 404, 429, 500] {
            let error = decode(status, secret).unwrap_err();
            assert!(!error.contains("SECRET_TOKEN"));
        }
    }
    #[test]
    fn provider_metadata_has_no_token() {
        let p = Provider {
            id: "one".into(),
            name: "Work".into(),
            kind: "cloudflare".into(),
        };
        assert_eq!(
            serde_json::to_value(p).unwrap(),
            json!({"id":"one","name":"Work","kind":"cloudflare"})
        );
    }
    #[test]
    fn pagination_preserves_cloudflare_totals() {
        let list = listing(
            json!({"result":[{"id":"one"}],"result_info":{"total_count":120,"total_pages":3}}),
            2,
        )
        .unwrap();
        assert_eq!(list.page, 2);
        assert_eq!(list.total_pages, 3);
        assert_eq!(list.total, 120);
    }
}
