use base64::{engine::general_purpose::STANDARD, Engine};
use hmac::{Hmac, Mac};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use url::Url;
use zeroize::{Zeroize, ZeroizeOnDrop};

#[derive(Clone, Deserialize, Serialize, Zeroize, ZeroizeOnDrop)]
#[serde(deny_unknown_fields)]
pub struct Enrollment {
    pub name: String,
    pub origin: String,
    pub server_id: String,
    pub key_id: String,
    pub actor_id: String,
    pub server_ca_pem: String,
    pub server_certificate_sha256: String,
    pub client_identity_pem: String,
    pub hmac_base64: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ssh: Option<SshIdentity>,
}

#[derive(Clone, Deserialize, Serialize, Zeroize, ZeroizeOnDrop)]
#[serde(deny_unknown_fields)]
pub struct SshIdentity {
    pub server_ip: String,
    pub port: u16,
    pub host_sha256: String,
    pub private_key: String,
}

pub fn id(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 48
        && s.as_bytes()[0].is_ascii_lowercase()
        && s.bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
}
pub fn token(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 96
        && s.as_bytes()[0].is_ascii_alphanumeric()
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}
pub fn environment(s: &str) -> bool {
    matches!(s, "development" | "staging" | "production")
}
impl Enrollment {
    pub fn validate(&self) -> Result<Vec<u8>, String> {
        if let Some(ssh) = &self.ssh {
            let ip: std::net::IpAddr =
                ssh.server_ip.parse().map_err(|_| "Invalid SSH server IP")?;
            if ip.is_unspecified()
                || ip.is_multicast()
                || ssh.port == 0
                || !ssh.host_sha256.starts_with("SHA256:")
                || ssh.host_sha256.len() != 50
                || ssh.private_key.len() > 16384
                || !ssh
                    .private_key
                    .starts_with("-----BEGIN OPENSSH PRIVATE KEY-----")
                || self.origin != "https://127.0.0.1:9123"
            {
                return Err("Invalid pinned SSH enrollment".into());
            }
        }
        let u = Url::parse(&self.origin).map_err(|_| "Invalid server origin")?;
        let private = match u.host() {
            Some(url::Host::Ipv4(ip)) => ip.is_loopback() || ip.is_private(),
            Some(url::Host::Ipv6(ip)) => ip.is_loopback() || (ip.segments()[0] & 0xfe00 == 0xfc00),
            _ => false,
        };
        if u.scheme() != "https"
            || !private
            || !u.username().is_empty()
            || u.password().is_some()
            || u.query().is_some()
            || u.fragment().is_some()
            || u.path() != "/"
            || !self.origin.starts_with("https://")
            || self.origin.contains(['%', '\\', '\r', '\n', ' '])
            || !id(&self.server_id)
            || !id(&self.key_id)
            || !token(&self.actor_id)
            || self.name.is_empty()
            || self.name.len() > 80
            || self.name.chars().any(char::is_control)
            || self.server_certificate_sha256.len() != 64
            || !self
                .server_certificate_sha256
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
            || self.server_ca_pem.len() > 32768
            || self.client_identity_pem.len() > 65536
        {
            return Err("Enrollment requires a private/loopback HTTPS IP, valid identity, and bounded PEM credentials".into());
        }
        let secret = STANDARD
            .decode(self.hmac_base64.trim())
            .map_err(|_| "Invalid HMAC encoding")?;
        if !(32..=128).contains(&secret.len()) {
            return Err("HMAC key must contain 32–128 random bytes".into());
        }
        Ok(secret)
    }
}

#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum Read {
    Projects {},
    Inventory {},
    Configuration { project: String },
    Migration {
        project: String,
    },
    Audit {
        after: u64,
    },
    Port {},
    Project {
        project: String,
        view: String,
    },
    Logs {
        project: String,
        service: String,
        slot: String,
        tail: u16,
        since: String,
    },
    Job {
        job: String,
    },
}
impl Read {
    pub fn target(&self) -> Result<(String, &'static str), String> {
        let bad = || "Invalid read operation".to_string();
        Ok(match self {
            Self::Projects {} => ("/v1/projects".into(), "deploy.read"),
            Self::Configuration { project } if id(project) => (format!("/v1/projects/{project}/configuration"), "deploy.environment"),
            Self::Inventory {} => ("/v1/inventory".into(), "deploy.read"),
            Self::Migration { project } if id(project) && project.starts_with("existing-") =>
                (format!("/v1/inventory/{project}/migration"), "deploy.read"),
            Self::Audit { after } => (format!("/v1/audit?after={after}"), "deploy.read"),
            Self::Port {} => ("/v1/ports/next".into(), "projects.write"),
            Self::Project { project, view } if id(project) && matches!(view.as_str(), "status" | "releases" | "services" | "domains") =>
                (format!("/v1/projects/{project}/{view}"), "deploy.read"),
            Self::Logs { project, service, slot, tail, since } if id(project) && service_name(service)
                && matches!(slot.as_str(), "active" | "inactive" | "blue" | "green") && (1..=2000).contains(tail)
                && matches!(since.as_str(), "5m" | "30m" | "1h" | "24h") =>
                (format!("/v1/projects/{project}/logs?service={service}&slot={slot}&tail={tail}&since={since}"), "deploy.logs"),
            Self::Job { job } if token(job) => (format!("/v1/jobs/{job}"), "deploy.read"),
            _ => return Err(bad()),
        })
    }
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Create {
    pub id: String,
    pub app_id: String,
    pub environment: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub template_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub route_service: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub route_port: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub readiness_path: Option<String>,
    pub domains: Vec<String>,
    pub zerodowntime: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub port: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub secondary_port: Option<u16>,
}
#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Deploy {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_release_id: Option<String>,
    pub environment: String,
    pub compose_yaml: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub variables: Option<std::collections::BTreeMap<String, String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub env_file: Option<String>,
}
#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Routes {
    pub domains: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub port: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub secondary_port: Option<u16>,
}
#[derive(Deserialize)]
#[serde(tag = "action", rename_all = "snake_case", deny_unknown_fields)]
pub enum Mutation {
    Migrate { project: String, source_sha256: String },
    Create {
        data: Create,
    },
    Deploy {
        project: String,
        data: Deploy,
    },
    Routes {
        project: String,
        data: Routes,
    },
    Dns {
        project: String,
        hostname: String,
    },
    Start {
        project: String,
    },
    Restart {
        project: String,
    },
    Stop {
        project: String,
        confirmation: String,
    },
    Rollback {
        project: String,
        release_id: Option<String>,
    },
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Operation {
    pub method: String,
    pub target: String,
    pub scopes: String,
    pub project: String,
    pub action: String,
    pub body: String,
    pub idempotency: String,
    pub request_id: String,
}
impl Drop for Operation {
    fn drop(&mut self) {
        self.body.zeroize();
    }
}
fn domains_ok(d: &[String]) -> bool {
    !d.is_empty()
        && d.len() <= 10
        && d.iter().all(|s| {
            s.len() <= 253
                && s.contains('.')
                && s.split('.').all(|l| {
                    !l.is_empty()
                        && l.len() <= 63
                        && !l.starts_with('-')
                        && !l.ends_with('-')
                        && l.bytes()
                            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
                })
        })
}
fn service_name(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 128
        && s.as_bytes()[0].is_ascii_alphanumeric()
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b'-'))
}
impl Mutation {
    pub fn plan(self) -> Result<Operation, String> {
        let (method, project, action, scopes, body) = match self {
            Self::Migrate { project, source_sha256 } => {
                if !project.starts_with("existing-") || source_sha256.len() != 64 || !source_sha256.bytes().all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c)) {
                    return Err("Invalid migration review".into());
                }
                ("POST", project, "migrate", "deploy.environment projects.write", serde_json::to_string(&serde_json::json!({"source_sha256":source_sha256})))
            }
            Self::Create { data } => {
                if !id(&data.id)
                    || !id(&data.app_id)
                    || (!data.template_id.is_empty() && !id(&data.template_id))
                    || !environment(&data.environment)
                    || (!(data.domains.is_empty() && data.template_id.is_empty())
                        && !domains_ok(&data.domains))
                    || data
                        .route_service
                        .as_ref()
                        .is_some_and(|s| !service_name(s))
                    || data.route_port == Some(0)
                    || (data.environment != "production"
                        && (data.zerodowntime || data.secondary_port.is_some()))
                {
                    return Err("Invalid project or environment configuration".into());
                }
                (
                    "POST",
                    data.id.clone(),
                    "create",
                    "projects.write",
                    serde_json::to_string(&data),
                )
            }
            Self::Deploy { project, mut data } => {
                if !environment(&data.environment)
                    || data.expected_release_id.as_ref().is_some_and(|r| !r.is_empty() && !token(r))
                    || data.compose_yaml.is_empty()
                    || data.compose_yaml.len() > 65536
                    || data
                        .env_file
                        .as_ref()
                        .is_some_and(|s| s.len() > 65536 || s.contains('\0'))
                    || data.variables.as_ref().is_some_and(|v| {
                        v.len() > 100 || v.iter().any(|(k, val)| k.len() > 64 || val.len() > 8192)
                    })
                {
                    data.compose_yaml.zeroize();
                    return Err(
                        "Select an environment and submit a Compose file up to 64 KiB".into(),
                    );
                }
                let body = serde_json::to_string(&data);
                data.compose_yaml.zeroize();
                if let Some(env) = data.env_file.as_mut() {
                    env.zeroize();
                }
                if let Some(v) = data.variables.as_mut() {
                    v.values_mut().for_each(Zeroize::zeroize);
                }
                (
                    "POST",
                    project,
                    "deploy",
                    "deploy.environment deploy.execute",
                    body,
                )
            }
            Self::Routes { project, data } => {
                if !data.domains.is_empty() && !domains_ok(&data.domains) {
                    return Err("Invalid domains".into());
                }
                (
                    "PUT",
                    project,
                    "routes",
                    "sites.write",
                    serde_json::to_string(&data),
                )
            }
            Self::Dns { project, hostname } => {
                if !domains_ok(std::slice::from_ref(&hostname)) {
                    return Err("Invalid hostname".into());
                }
                (
                    "POST",
                    project,
                    "dns",
                    "dns.write",
                    serde_json::to_string(&serde_json::json!({"hostname": hostname})),
                )
            }
            Self::Start { project } => (
                "POST",
                project,
                "start",
                "deploy.lifecycle",
                Ok("{}".into()),
            ),
            Self::Restart { project } => (
                "POST",
                project,
                "restart",
                "deploy.execute",
                Ok("{}".into()),
            ),
            Self::Stop {
                project,
                confirmation,
            } => {
                if confirmation != project {
                    return Err("Type the exact project ID to stop services".into());
                }
                (
                    "POST",
                    project,
                    "stop",
                    "deploy.stop",
                    serde_json::to_string(&serde_json::json!({"confirmation": confirmation})),
                )
            }
            Self::Rollback {
                project,
                release_id,
            } => {
                if release_id.as_ref().is_some_and(|r| !token(r)) {
                    return Err("Invalid release ID".into());
                }
                let data = release_id
                    .map(|r| serde_json::json!({"release_id":r}))
                    .unwrap_or(serde_json::json!({}));
                (
                    "POST",
                    project,
                    "rollback",
                    "deploy.rollback",
                    serde_json::to_string(&data),
                )
            }
        };
        if !id(&project) {
            return Err("Invalid project ID".into());
        }
        let mut body = body.map_err(|_| "Invalid request")?;
        if body.len() > 128 * 1024 {
            body.zeroize();
            return Err("Request too large".into());
        }
        Ok(Operation {
            method: method.into(),
            target: if action == "migrate" {
                format!("/v1/inventory/{project}/migrate")
            } else if action == "create" {
                "/v1/projects".into()
            } else {
                format!("/v1/projects/{project}/{action}")
            },
            scopes: scopes.into(),
            project,
            action: action.into(),
            body,
            idempotency: format!("desktop-{}", uuid::Uuid::new_v4()),
            request_id: format!("req-{}", uuid::Uuid::new_v4()),
        })
    }
}
pub fn sign(secret: &[u8], e: &Enrollment, op: &Operation, timestamp: &str) -> String {
    let hash = hex::encode(Sha256::digest(op.body.as_bytes()));
    let input = [
        "deploy-agent-v1",
        &e.server_id,
        &e.key_id,
        timestamp,
        &op.idempotency,
        &e.actor_id,
        &op.request_id,
        &op.scopes,
        &op.method,
        &op.target,
        &hash,
    ]
    .join("\n");
    let mut mac = Hmac::<Sha256>::new_from_slice(secret).expect("HMAC accepts any key length");
    mac.update(input.as_bytes());
    hex::encode(mac.finalize().into_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn enrollment(origin: &str) -> Enrollment {
        Enrollment {
            name: "VPS".into(),
            origin: origin.into(),
            server_id: "vps-01".into(),
            key_id: "desktop-01".into(),
            actor_id: "owner".into(),
            server_ca_pem: String::new(),
            server_certificate_sha256: "a".repeat(64),
            client_identity_pem: String::new(),
            hmac_base64: STANDARD.encode([42u8; 32]),
            ssh: None,
        }
    }
    #[test]
    fn migration_is_bound_to_exact_review_and_scopes() {
        let op = Mutation::Migrate { project: "existing-abc".into(), source_sha256: "a".repeat(64) }.plan().unwrap();
        assert_eq!(op.target, "/v1/inventory/existing-abc/migrate");
        assert_eq!(op.scopes, "deploy.environment projects.write");
        assert!(Mutation::Migrate { project: "existing-abc".into(), source_sha256: "x".repeat(64) }.plan().is_err());
        assert!(Mutation::Migrate { project: "../other".into(), source_sha256: "a".repeat(64) }.plan().is_err());
    }
    #[test]
    fn origins_cannot_leak_credentials() {
        for u in [
            "http://127.0.0.1:9123",
            "https://example.com",
            "https://8.8.8.8",
            "https://user@127.0.0.1",
            "https://127.0.0.1/v1",
            "https://127.0.0.1?x=y",
            "https://127.0.0.1#x",
            "https://127.0.0.1/%2e%2e",
        ] {
            assert!(enrollment(u).validate().is_err(), "{u}");
        }
        for u in [
            "https://127.0.0.1:9123",
            "https://10.1.2.3:9123",
            "https://[::1]:9123",
            "https://[fd00::1]:9123",
        ] {
            assert!(enrollment(u).validate().is_ok());
        }
    }
    #[test]
    fn no_generic_proxy_or_path_injection() {
        assert!(
            serde_json::from_str::<Read>(r#"{"kind":"projects","path":"https://evil.com"}"#)
                .is_err()
        );
        assert!(Read::Project {
            project: "../secret".into(),
            view: "status".into()
        }
        .target()
        .is_err());
        assert!(Read::Project {
            project: "demo".into(),
            view: "config".into()
        }
        .target()
        .is_err());
        assert!(Read::Migration {
            project: "../secret".into()
        }
        .target()
        .is_err());
        assert_eq!(
            Read::Migration {
                project: "existing-safe".into()
            }
            .target()
            .unwrap()
            .0,
            "/v1/inventory/existing-safe/migration"
        );
        assert!(Read::Logs {
            project: "demo".into(),
            service: "app&tail=9999".into(),
            slot: "active".into(),
            tail: 200,
            since: "30m".into()
        }
        .target()
        .is_err());
    }
    #[test]
    fn configuration_requires_secret_scope_and_preserves_edit_revision() {
        let read = Read::Configuration { project: "demo".into() };
        assert_eq!(read.target().unwrap(), ("/v1/projects/demo/configuration".into(), "deploy.environment"));
        assert!(Read::Configuration { project: "../demo".into() }.target().is_err());
        let deploy: Mutation = serde_json::from_value(serde_json::json!({"action":"deploy","project":"demo","data":{"environment":"production","compose_yaml":"services: {}", "env_file":"", "expected_release_id":"rel-old"}})).unwrap();
        let op = deploy.plan().unwrap();
        let body: serde_json::Value = serde_json::from_str(&op.body).unwrap();
        assert_eq!(body["expected_release_id"], "rel-old");
        assert_eq!(body["env_file"], "");
    }

    #[test]
    fn full_compose_inputs_keep_dotenv_and_need_no_template() {
        let create: Mutation = serde_json::from_value(serde_json::json!({"action":"create","data":{"id":"demo","app_id":"demo","environment":"production","domains":[],"zerodowntime":false}})).unwrap();
        let plan = create.plan().unwrap();
        assert!(!plan.body.contains("template_id"));
        let deploy: Mutation = serde_json::from_value(serde_json::json!({"action":"deploy","project":"demo","data":{"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:alpine\n","env_file":"PASSWORD='a$HOME'\n"}})).unwrap();
        let plan = deploy.plan().unwrap();
        let body: serde_json::Value = serde_json::from_str(&plan.body).unwrap();
        assert_eq!(body["env_file"], "PASSWORD='a$HOME'\n");
        assert!(plan.scopes.contains("deploy.environment"));
        assert!(Read::Logs {
            project: "demo".into(),
            service: "web_API.v2".into(),
            slot: "active".into(),
            tail: 200,
            since: "30m".into()
        }
        .target()
        .is_ok());
    }
    #[test]
    fn staging_cannot_be_blue_green() {
        let data = Create {
            id: "demo".into(),
            app_id: "demo".into(),
            environment: "staging".into(),
            template_id: "web".into(),
            route_service: None,
            route_port: None,
            readiness_path: None,
            domains: vec!["app.example.com".into()],
            zerodowntime: true,
            port: None,
            secondary_port: None,
        };
        assert!(Mutation::Create { data }.plan().is_err());
        assert!(Mutation::Stop {
            project: "demo".into(),
            confirmation: "other".into()
        }
        .plan()
        .is_err());
    }
    #[test]
    fn signature_matches_go_fixture() {
        let e = enrollment("https://127.0.0.1:9123");
        let op = Operation {
            method: "POST".into(),
            target: "/v1/projects/demo/restart".into(),
            scopes: "deploy.execute".into(),
            project: "demo".into(),
            action: "restart".into(),
            body: "{}".into(),
            idempotency: "op-01".into(),
            request_id: "req-01".into(),
        };
        assert_eq!(
            sign(&[42u8; 32], &e, &op, "1700000000"),
            "5681f2392d91fe8b3eb330325236b1eb277cc348dc845509a02eaf33d82e12a0"
        );
    }
}
