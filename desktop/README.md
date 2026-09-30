# Dockyard for macOS

A Tauri 2 desktop control room with a Rust API client and a bundled React interface. It manages the existing Go agent; the agent remains the 24/7 VPS service. Closing or locking this Mac app does not interrupt running services.

## Run on this Mac

The latest local Apple Silicon app is in `releases/dockyard-migration-plan/Dockyard.app`, with `releases/Dockyard-migration-plan-macos-arm64.zip` (SHA-256 `ec5457f539693d95a8c4ce824b123a203239cb244aed354321b30da558860a7f`). Its app icon and in-app wordmark use the same Dockyard logo. When an enrolled Mac is locked, the app shows only the Touch ID unlock screen. This build includes a fixed, non-scrolling sidebar, server-shaped loading placeholders, updater access checks, the corrected executable upload, a migration plan review, and the linked Caddy route layout fix. From the repository root:

```sh
open desktop/releases/dockyard-migration-plan/Dockyard.app
```

Quit any previously opened Dockyard instance first so the single-instance guard does not bring an older build to the foreground. This local artifact uses an ad-hoc signature; it is not notarized for distribution.

Package the app with `npm run desktop:build`; a standalone `cargo build --release` does not embed the frontend and produces a blank window when copied into an app bundle.

The local build produces `src-tauri/target/release/bundle/macos/Dockyard.app`:

```sh
cd desktop
npm ci
bash scripts/generate-icon.sh
npm run server:bundle
npm run desktop:build
open src-tauri/target/release/bundle/macos/Dockyard.app
```

Requirements: macOS 12+, Node 22.12+ or 24+, Rust 1.88+, and Xcode Command Line Tools. The default build targets the current Mac architecture; the initial verified build is Apple Silicon. Local builds seal the bundle with an ad-hoc signature and hardened runtime; this is not Developer ID notarization. The app starts locked and disconnected. **Explore the interface** opens clearly labeled, read-only sample data. No production health is inferred from the preview.

For development: `npm run desktop:dev`. For a browser-only visual preview: `npm run build && npm run preview`, then open `http://127.0.0.1:1420/`. A browser preview cannot enroll, read your API, or submit writes. Frontend assets and icons are bundled; no CDN, remote fonts, analytics, or desktop auto-update service is used.

## Built-in terminal

Choose **Terminal** in the sidebar, select **VPS root** or **Container**, then **Connect with Touch ID**. A separate root SSH connection checks the server IP, port and host fingerprint saved during enrollment before sending credentials. Password entry uses a native macOS secure field; passwords never enter the webview, enrollment JSON, SQLite or app logs. This requires an SSH enrollment and root password login already permitted by the server; the app does not change SSH policy.

**Remember root SSH password in this Mac’s Keychain** is on by default; uncheck it for a one-time terminal connection. A password is saved only after successful root SSH authentication. The non-synchronized Keychain entry is separate from API credentials and scoped to the exact IP, port and SSH fingerprint. Each connection requires fresh Touch ID even when a password is remembered. Unchecking the option uses a newly entered password without updating a previously saved entry; **Forget password** removes the saved entry after Touch ID. Removing enrollment also removes its saved root password. Changing the VPS password requires forgetting the old one and reconnecting.

The root terminal opens a full administrative login shell. Container mode lists all running Docker containers, including observed legacy Compose services, and opens a PTY using the full container ID and `docker exec -it … /bin/sh` (or `/bin/bash`, if installed). Docker uses the container’s configured user. The API-only `dockyard-link` account and Go API never gain arbitrary shell execution.

One terminal is open at a time. It stays connected when switching Dockyard pages. **Disconnect**, `exit`, app closure, manual lock, the existing five-minute session expiry, or a minute away from the app closes the connection. Disconnecting does not roll back commands or guarantee that remote background processes have stopped; terminal commands bypass managed deployment reviews and may change any server resource. To preserve deployment tracking, use Dockyard’s Compose deployment controls for normal releases.

Terminal history is held only in memory, with 2,000 lines of scrollback, bounded output/backpressure and 8 KiB input chunks. Remote clipboard (OSC 52), hyperlinks (OSC 8) and window control are disabled. Commands run on the VPS may still appear in its shell history or server audit logs; this app does not disable those logs. This local ad-hoc Mac build does not claim production code-signing or absolute protection against a compromised Mac/root server.

## Update the VPS service from the Mac app

Open **Server details → Server software → Check version**. The desktop reads the installed Go binary's embedded version over SSH and compares it with the versioned, verified Ubuntu bundle in this Mac build. It shows exact versions when available and offers **Update server** only for a newer bundled version or a legacy VPS binary that has no version metadata. It blocks automatic replacement when the VPS reports a newer version or a different build of the same version. Older binaries cannot report a version until updated. SHA-256 checks still protect the bundle and detect changes between review and installation. The app requires its saved, pinned SSH host identity and root SSH access. The first successful root login saves the password in this Mac’s non-synchronized Keychain for later Touch ID-gated update checks and terminal sessions. A failed login never saves it. **Forget password** in Server details or Terminal removes it. The password and update command do not pass through the webview.

**Server details → Required server access → Check access** reports whether root SSH, the private updater directory, installed binaries, the SQLite database, and the Dockyard service are ready. If the updater directory is missing or its root-owned permissions need tightening, **Prepare updater access** creates or restricts only `/var/lib/dockyard-desktop-updates` after Touch ID. Unsafe paths and problems with binaries, database, or service are reported for VPS review; the app does not broadly grant system permissions. Update uploads place binaries in that private directory with owner-execute permission before validating and installing them.

The updater uploads only the bundled `dockyard` and `dockyardctl` binaries, a fixed updater script and checksums to a private root-owned stage. It checks the current hashes again, rejects active or recovery-required jobs, validates the candidate against the existing configuration, stops only the Dockyard service, snapshots SQLite and old binaries, replaces the binaries and starts the service. Docker containers and Caddy are not restarted. If the new service fails to stay active, it restores the binaries and database and restarts the old service. No Compose files, Caddy routes, server credentials or project data are edited by the updater. If SSH disconnects mid-update or automatic recovery fails, inspect the private stage path reported by the app before retrying. The first release supports Ubuntu x86-64 installations using `/usr/local/bin/dockyard`, `/usr/local/bin/dockyardctl`, `/var/lib/dockyard/state.db`, and the `dockyard` systemd unit.

`npm run server:bundle` builds both Ubuntu binaries and records their version, source commit and SHA-256 hashes in `resources/ubuntu/manifest.json`. The updater refuses to run if the packaged binaries differ from that manifest. An older local bundle without version metadata is labeled unversioned; it can replace only another unversioned VPS build after an explicit review of its source commit. A versioned VPS build cannot be replaced with an unversioned bundle. Rebuild the Ubuntu bundle with Docker Desktop to show exact target versions. The Mac app does not fetch or run arbitrary binaries from the VPS or GitHub at update time.

Verification: Rust unit tests cover target validation, dimensions, credential scoping and session revocation. The optional loopback SSH fixture verifies streaming, root/container channel selection, resize requests, bounded output and disconnect on lock without executing OS commands. The separate OpenSSH fixture verifies actual PTYs and Docker CLI dispatch against a simulated container command; it does not mount the host Docker socket. Native Touch ID/Keychain prompts and real VPS/container acceptance require an operator check.

```sh
# From desktop; prepare only the loopback test fixture dependency cache.
(cd tests/terminal && GOMODCACHE=/private/tmp/dockyard-terminal-go-mod-cache go mod download)
cargo test --offline --locked --manifest-path src-tauri/Cargo.toml loopback_streaming_resize_container_and_revocation -- --ignored
# With dockyard-ssh-fixture:local built and Docker Desktop running:
cargo test --offline --locked --manifest-path src-tauri/Cargo.toml interactive_pty_container_resize_backpressure_and_lock -- --ignored
```

## First-time Ubuntu setup

Choose **Set up Dockyard on your server** in the Mac app. Select Ubuntu, enter the server IP and SSH port, verify its SSH fingerprint against your provider's console, and enter the root password in the native secure Mac dialog. Review the installation and confirm with Touch ID. The installer generates the certificates and HMAC credentials automatically; no enrollment file is needed. See [the complete setup and recovery guide](FIRST_TIME.md).

Ubuntu 22.04/24.04 x86-64 with working root SSH password access is supported by this installer. The app contains its Ubuntu agent binaries; first-time installation does not depend on a GitHub release. `server:bundle` builds those resources with Docker when building the app from source. It needs Docker Desktop or another functioning local Docker builder.

The server IP/SSH port/fingerprint and restricted connector key are saved in Keychain; the setup root password is never saved or returned to JavaScript. Terminal access has a separate, optional Keychain password. The API remains on the VPS's loopback address, reached through a pinned SSH tunnel automatically when the app is unlocked. Caddy is backed up, adapted and compared before a reload; existing Docker/Compose files are preserved. Installation receipts and generated credentials are saved before writes so the same installation can be resumed after an uncertain disconnect.

Without an imported, reviewed deployment policy, setup grants inventory/log reads only. The advanced policy option accepts private root-policy JSON for approved templates/domains/resource limits; it does not import credential paths or Cloudflare secrets. DNS still requires an explicit VPS Cloudflare configuration and zone-scoped token; the saved IP does not grant DNS permissions. Existing projects remain production observations until explicitly migrated.

## Import an already configured private VPS

The desktop uses the existing certificate-pinned mTLS + HMAC API protocol. It does not introduce an alternate public API or a bearer-token bypass.

1. Provision a **dedicated Mac client certificate** signed by the configured control CA, its private key, and a dedicated random HMAC key. Keep the CA signing key offline. Keep the client key on this Mac; provide only the client certificate fingerprint and HMAC material to the VPS root policy. Give the client certificate the clientAuth extended key usage. See [server installation](../docs/INSTALL.md).
2. In `/etc/dockyard/config.json`, configure a key such as `desktop-01`, with its matching `certificate_sha256`, a root-owned HMAC `secret_file`, desired scopes, and permitted project IDs. To administer inventory and audit, use `projects: ["*"]` plus `deploy.read`. All management features require the ten scopes listed in the example policy. Do not send the root config or Cloudflare token to the Mac.
3. For desktop-exclusive enrollment, remove other client keys/certificate pins and stop sharing the desktop credentials with `dockyardctl` or automation. Use a dedicated control CA with no other issued clients, and limit network ingress to this Mac's private VPN identity. Check/restart the daemon after the policy change. Removing a local enrollment does **not** revoke its server key.
4. Keep API access on a private IP or SSH tunnel. For example, with the server certificate containing the `127.0.0.1` IP SAN:

   ```sh
   ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
     -L 127.0.0.1:9123:127.0.0.1:9123 your-user@your-vps
   ```

   Enrollment origin: `https://127.0.0.1:9123`. The Rust client accepts private/loopback IP literals, not public hosts. Server SAN verification is mandatory. A tunnel transports end-to-end mTLS; no TLS-intercepting Caddy proxy is used for the control API.

5. On this Mac, package the provisioned identity using private local files:

   ```sh
   chmod 600 /secure/mac-client.key /secure/desktop-01.key
   python3 desktop/scripts/enroll.py \
     --name "Production VPS" --origin https://127.0.0.1:9123 \
     --server-id vps-01 --key-id desktop-01 --actor-id mac-owner \
     --server-ca /secure/server-ca.crt --server-cert /secure/server.crt \
     --client-cert /secure/mac-client.crt \
     --client-key /secure/mac-client.key --hmac-file /secure/desktop-01.key \
     --out /secure/mac.enrollment.json
   ```

   The output directory must belong to you and not be writable by other users. The helper refuses existing output files, validates certificate/key agreement, and creates mode 600. It prints only the public client certificate fingerprint, for the VPS policy. It never generates a shared CA or exports server credentials.

6. Choose **Import existing enrollment** and authenticate with Touch ID. A configured fingerprint and a working Touch ID sensor are required; Dockyard does not offer a password or Apple Watch fallback. Select the enrollment using the native file chooser. Rust verifies a signed `/v1/projects` request before saving the enrollment to the non-synchronized macOS login Keychain. Credentials are never returned to the webview. Remove the transfer bundle and unneeded credential copies after successful enrollment; keep a protected offline recovery procedure. File deletion on SSDs is not guaranteed secure erasure.

## Managing services

- Projects: search/filter and page through application environments; create an app/environment project with its approved template and domains. Port allocation is automatic; **Check next available port** is advisory, not a reservation.
- Deploy Compose: explicitly choose development, staging or production every time. The target environment must already exist for the same app. Import/edit multi-service YAML and optionally supply an app environment snapshot as JSON. The VPS validates the strict Compose subset and starts all services with Compose. See [Compose policy](../docs/COMPOSE.md).
- Production supports stateless blue-green stacks. Dev/staging always use one slot. Persistent volumes require a single-instance production project. New projects accept ordinary Compose and `.env` without templates or required digests. Enable Compose management in Server details after updating an older VPS. This action requires root SSH and Touch ID.
- Inspect live status separately from recorded state, browse release/service metadata, read bounded container logs by service/slot, and start/restart/stop/rollback services. Stop requires typing the exact project ID. Public TLS remains explicitly unverified by this API.
- Domains: edit managed Caddy assignments and optional ports, then create Cloudflare DNS for an assigned hostname in the project detail. The VPS selects the permitted zone and origin. Manual Caddy sites appear in read-only inventory.
- Inventory: existing directories remain production observations from SQLite and are not silently adopted or restarted. The VPS refreshes them every five minutes; the app's refresh reads the current database snapshot. **Check migration** opens a read-only plan when the VPS runs a service version with the migration preflight endpoint. It separates the traffic move Dockyard must perform from policy and source decisions requiring review. The actual takeover job is not implemented yet. See [legacy migration design](../docs/LEGACY_MIGRATION.md).
- Deployments: tracks the newest ten jobs started by this Mac, retains up to 100 job IDs in Keychain, and supports job lookup by ID. This API has no global jobs-list endpoint. Audit reads the server's paginated event log.

All writes have a native review dialog, separate from the webview. The desktop permits one outstanding write at a time. Before sending, Rust saves the exact JSON bytes, stable operation/request IDs, and an attempted marker in Keychain. A definitive rejection of the first attempt can clear the operation; any rejection after an uncertain attempt retains it, since authentication or policy changes do not prove the earlier request was never accepted. Uncertain outcomes retain them across restarts; **Retry safely** refreshes the timestamp/signature while reusing those bytes and IDs. Successful job acceptance retains the operation until its terminal result is read, then removes the payload from saved state. A saved operation prevents replacing or removing enrollment. A single-instance guard prevents concurrent desktop processes from overwriting the saved state. Never re-submit the same change with new IDs after a network error. If a recorded operation cannot be resolved because of key rotation or state recovery, inspect its IDs and the VPS before removing local state.

Server `recovery_required` blocks API writes until local reconciliation; the API has no arbitrary host shell or recovery bypass. The separate root terminal has full VPS access after Touch ID. Backups, legacy adoption, other root-policy edits, database migrations and destructive cleanup remain explicit VPS operator work.

## Security boundaries and production signing

TLS 1.3 is mandatory, using only the enrolled server CA and an exact SHA-256 pin of the server leaf certificate, checked during the handshake before API payloads are sent. Standard chain, expiry, SAN and handshake signature verification remain mandatory. Proxy inheritance, redirects, automatic request retries, cookies and TLS bypasses are disabled. HTTP responses are bounded to 3 MiB (32 MiB for the project index, which includes release history for all projects) and requests to the existing API limits. Enrolled signing material and session clients stay in Rust; credential buffers and retained write bodies use best-effort zeroization. Compose/environment editor values necessarily exist in UI memory while being edited; there is no localStorage, plaintext payload cache or credential export command.

Only the bundled `main` window has explicit command capabilities. Release CSP denies webview network connections other than Tauri IPC. Remote navigation/popups are rejected, devtools are disabled, and there are no HTTP/shell/filesystem/opener plugins. Rust validates operation types, IDs and parameters; the VPS remains the final authorization and Compose validation boundary. Sessions last five minutes (checked against both monotonic and wall-clock deadlines to cover Mac sleep), lock after one continuous minute away from the app (returning sooner cancels that timer), and require a fresh Touch ID scan to unlock. If macOS locks out Touch ID, unlock the Mac with its login password to restore Touch ID and then retry; Dockyard itself remains locked. It checks Touch ID availability before requesting authentication and does not reuse a recent Mac unlock. Locks discard the session and clear visible server data/editors. An already accepted VPS job continues running.

**An API can authenticate this Mac's enrolled credentials; it cannot prove that only a particular UI/binary sent a request.** A copied client private key + HMAC secret can impersonate the app. The initial enrollment is exportable software key material, not a Secure Enclave non-exportable identity or remote attestation. macOS Keychain/native prompts improve local protection but do not protect a compromised Mac or an operator who explicitly allows another program access. A compromised enrolled server could supply malicious content to the UI; React renders content as text and native dialogs protect writes, but no application can claim absolute security.

The local artifact has a sealed ad-hoc signature, **not Developer ID notarization**. `desktop:build` applies `tauri.local.conf.json` with the ad-hoc identity. For Developer ID distribution, use `npm run desktop:release` without this local override and configure signing/notarization credentials in your build environment. For production, build with an Apple Developer ID identity using Tauri's documented [macOS signing](https://v2.tauri.app/distribute/sign/macos/) and notarization flow, keep a stable signing identity for Keychain ACLs, and verify the artifact on a clean Mac. Do not disable Gatekeeper, broaden Keychain access to all applications, or add updater/remote-content permissions as a workaround. The app's bundle config enables hardened runtime, but a local ad-hoc build is not equivalent to trusted Developer ID distribution. Do not revoke other legitimate clients until the enrolled desktop has been verified on staging.

## Validation

```sh
cd desktop
npm run build
cargo test --locked --manifest-path src-tauri/Cargo.toml
cargo clippy --locked --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
cargo test --locked --manifest-path src-tauri/Cargo.toml -- --ignored
```

The ignored test requires Go and permission to listen on loopback. It starts a disposable TLS 1.3/mTLS agent, proves valid Rust/Go signatures, rejects wrong HMAC/client pin/server CA/server pin, proves that matching pins cannot bypass SAN/expiry/TLS-version checks, verifies no redirects and bounded responses, and simulates an accepted write with a lost response followed by an idempotent retry. It also verifies a 100-project index with 200 releases each. It never touches Keychain, your VPS, Docker or Caddy. It is not a pentest or a live deployment acceptance test.

Before production enrollment, verify native authentication, Keychain denial behavior, idle/focus locking, certificate/key rotation, interrupted-write recovery, server recovery, and live Docker/Caddy/Cloudflare deployment on a disposable VPS. No real production credentials were used during local implementation.

Server certificate rotation requires a new authenticated enrollment containing the new trusted leaf fingerprint. Resolve pending operations first; never bypass pin/CA validation to restore access. Rotating the client certificate or HMAC key also requires updating the matching VPS policy. Keep the stable server fingerprint key and request records for retry recovery.

### Cloudflare domain providers

Open **Settings → Domain Providers → Add Cloudflare**. Create a **user API token** in Cloudflare with **Zone / Zone / Read** and **Zone / DNS / Edit**, scoped to the domains you want to manage. The app opens Cloudflare’s token page; after creating the token, return to Dockyard and choose **Enter token & connect**. Touch ID and the secure native macOS prompt keep the token out of the webview. Browser OAuth requires a registered Dockyard public PKCE client and is not configured in this build.

**Domains → Provider DNS** lists accessible Cloudflare zones by connection/account. View records, create/edit A, AAAA, CNAME, TXT, MX, NS, CAA and SRV records, delete records with confirmation, change TTL/proxy settings, and use the saved VPS IP. Other record types are visible and must be edited in Cloudflare. Lists are paginated; filters apply to the current page. DNS changes require Touch ID. Cloudflare remains responsible for domain registration, billing and zone onboarding; Caddy routing stays in the **Caddy routes** tab.

Connections are desktop-wide and saved in a separate, non-iCloud-synchronized Keychain item. Reconnect replaces the local token. Disconnect removes the local connection without deleting remote DNS; revoke the token in Cloudflare if necessary. These tokens are never sent to the VPS and do not configure the daemon’s separate optional Cloudflare integration. No server update is needed for this desktop feature. GoDaddy is listed as coming later.

Native requests use a fixed HTTPS Cloudflare endpoint, no redirects or environment proxies, bounded responses and timeouts, and an unlocked-session lease. Record edits/deletes re-read `modified_on` before applying a change to detect stale forms (Cloudflare does not provide an atomic compare-and-swap through this flow). Writes are never retried automatically; after a connection failure, refresh to determine whether the change succeeded.

Tests: `cargo test --locked --manifest-path src-tauri/Cargo.toml` includes local-only Cloudflare HTTP fixtures (requires permission to bind loopback). For a credential-free visual fixture, run Vite and visit `/tests/providers-preview.html`. This test page uses mocked IPC and is not included in the packaged app.
