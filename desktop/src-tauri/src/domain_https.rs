use serde::Serialize;
use serde_json::Value;
use std::{
    net::{IpAddr, SocketAddr, ToSocketAddrs},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

#[derive(Serialize)]
pub struct Check {
    pub state: &'static str,
    pub http_status: Option<u16>,
    pub checked_at: u64,
}
fn result(state: &'static str, http_status: Option<u16>) -> Check {
    Check {
        state,
        http_status,
        checked_at: SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs(),
    }
}
pub fn valid_hostname(host: &str) -> bool {
    host.len() <= 253
        && host.contains('.')
        && host.parse::<IpAddr>().is_err()
        && host.split('.').all(|label| {
            !label.is_empty()
                && label.len() <= 63
                && !label.starts_with('-')
                && !label.ends_with('-')
                && label
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
        })
}
pub fn assigned(data: &Value, host: &str) -> bool {
    valid_hostname(host)
        && data
            .get("domains")
            .and_then(Value::as_array)
            .is_some_and(|domains| {
                domains.iter().any(|d| {
                    d.get("hostname").and_then(Value::as_str) == Some(host)
                        && d.get("assigned").and_then(Value::as_bool) == Some(true)
                })
            })
}
fn public_address(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(ip) => {
            let [a, b, c, _] = ip.octets();
            !ip.is_private()
                && !ip.is_loopback()
                && !ip.is_link_local()
                && !ip.is_broadcast()
                && !ip.is_documentation()
                && !ip.is_multicast()
                && a != 0
                && a < 240
                && !(a == 100 && (64..=127).contains(&b))
                && !(a == 192 && b == 0 && c == 0)
                && !(a == 198 && (b == 18 || b == 19))
        }
        IpAddr::V6(ip) => {
            let s = ip.segments();
            // Public unicast only, excluding reserved transition and documentation ranges.
            s[0] & 0xe000 == 0x2000
                && !(s[0] == 0x2001 && (s[1] < 0x200 || s[1] == 0xdb8))
                && s[0] != 0x2002
                && !(s[0] == 0x3fff && s[1] < 0x1000)
        }
    }
}
fn request(client: reqwest::blocking::Client, url: &str) -> Check {
    match client.head(url).send() {
        Ok(response) => result("verified", Some(response.status().as_u16())),
        Err(error) => {
            let mut source: Option<&(dyn std::error::Error + 'static)> = Some(&error);
            let mut certificate = false;
            while let Some(err) = source {
                certificate |= err.to_string().to_ascii_lowercase().contains("certificate");
                source = err.source();
            }
            result(
                if certificate {
                    "invalid_certificate"
                } else {
                    "https_failed"
                },
                None,
            )
        }
    }
}
fn client() -> reqwest::blocking::ClientBuilder {
    reqwest::blocking::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(4))
        .timeout(Duration::from_secs(8))
}
pub fn check(host: &str) -> Check {
    if !valid_hostname(host) {
        return result("invalid_hostname", None);
    }
    let addresses: Vec<SocketAddr> = match (host, 443).to_socket_addrs() {
        Ok(addresses) => addresses.collect(),
        Err(_) => return result("dns_failed", None),
    };
    if addresses.is_empty() {
        return result("dns_failed", None);
    }
    if addresses
        .iter()
        .any(|address| !public_address(address.ip()))
    {
        return result("private_address", None);
    }
    // Pin the checked DNS answers. Never follow redirects or send enrollment credentials.
    match client().resolve_to_addrs(host, &addresses).build() {
        Ok(client) => request(client, &format!("https://{host}/")),
        Err(_) => result("https_failed", None),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        sync::Arc,
    };
    #[test]
    fn only_assigned_canonical_public_hosts_are_allowed() {
        let data = serde_json::json!({"domains":[{"hostname":"api.example.com","assigned":true},{"hostname":"old.example.com","assigned":false}]});
        assert!(assigned(&data, "api.example.com"));
        for host in [
            "other.example.com",
            "old.example.com",
            "API.example.com",
            "127.0.0.1",
            "example.com/path",
            "example.com:443",
            "example.com@localhost",
            "example..com",
        ] {
            assert!(!assigned(&data, host), "{host}");
        }
        for ip in [
            "127.0.0.1",
            "10.0.0.1",
            "172.16.0.1",
            "192.168.0.1",
            "169.254.169.254",
            "100.64.0.1",
            "198.18.0.1",
            "0.0.0.0",
            "224.0.0.1",
            "240.0.0.1",
            "::1",
            "::ffff:127.0.0.1",
            "fe80::1",
            "fc00::1",
            "2001:db8::1",
            "2002:7f00:1::1",
        ] {
            assert!(!public_address(ip.parse().unwrap()), "{ip}");
        }
        for ip in [
            "1.1.1.1",
            "8.8.8.8",
            "2606:4700:4700::1111",
            "2001:4860:4860::8888",
        ] {
            assert!(public_address(ip.parse().unwrap()), "{ip}");
        }
    }
    fn server(
        status: u16,
    ) -> (
        String,
        reqwest::Certificate,
        std::thread::JoinHandle<String>,
    ) {
        let certificate =
            rcgen::generate_simple_self_signed(vec!["api.example.com".into()]).unwrap();
        let root = reqwest::Certificate::from_der(certificate.cert.der()).unwrap();
        let config = rustls::ServerConfig::builder_with_provider(Arc::new(
            rustls::crypto::ring::default_provider(),
        ))
        .with_safe_default_protocol_versions()
        .unwrap()
        .with_no_client_auth()
        .with_single_cert(
            vec![certificate.cert.der().clone()],
            rustls::pki_types::PrivatePkcs8KeyDer::from(certificate.signing_key.serialize_der())
                .into(),
        )
        .unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let thread = std::thread::spawn(move || {
            let (tcp, _) = listener.accept().unwrap();
            tcp.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
            let mut stream = rustls::StreamOwned::new(
                rustls::ServerConnection::new(Arc::new(config)).unwrap(),
                tcp,
            );
            let mut bytes = [0; 2048];
            let count = stream.read(&mut bytes).unwrap_or(0);
            let request = String::from_utf8_lossy(&bytes[..count]).into_owned();
            if count > 0 {
                let _ = stream.write_all(format!("HTTP/1.1 {status} Test\r\nLocation: http://127.0.0.1:1/secret\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes());
                let _ = stream.flush();
            }
            request
        });
        (
            format!("https://api.example.com:{}/", address.port()),
            root,
            thread,
        )
    }
    #[test]
    fn validates_tls_reports_http_errors_and_never_follows_redirects() {
        for status in [200, 404, 502, 302] {
            let (url, root, server) = server(status);
            let address: SocketAddr = format!(
                "127.0.0.1:{}",
                url::Url::parse(&url).unwrap().port().unwrap()
            )
            .parse()
            .unwrap();
            let client = client()
                .resolve("api.example.com", address)
                .add_root_certificate(root)
                .build()
                .unwrap();
            let checked = request(client, &url);
            assert_eq!(checked.state, "verified");
            assert_eq!(checked.http_status, Some(status));
            let sent = server.join().unwrap();
            assert!(sent.starts_with("HEAD / HTTP/1.1"));
            assert!(!sent.to_ascii_lowercase().contains("authorization"));
        }
    }
    #[test]
    fn rejects_untrusted_certificates() {
        let (url, _, server) = server(200);
        let address: SocketAddr = format!(
            "127.0.0.1:{}",
            url::Url::parse(&url).unwrap().port().unwrap()
        )
        .parse()
        .unwrap();
        let checked = request(
            client()
                .resolve("api.example.com", address)
                .build()
                .unwrap(),
            &url,
        );
        assert_eq!(checked.state, "invalid_certificate");
        assert_eq!(checked.http_status, None);
        server.join().unwrap();
    }
}
