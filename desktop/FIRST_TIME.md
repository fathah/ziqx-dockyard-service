# First-time Ubuntu setup from your Mac

Open the native Mac app and choose **Set up Dockyard on your server**. The first screen offers large **Resume setup** and **Start over** choices. Resume continues the saved installation directly. Start over resets the questions and keeps any saved server installation available; it does not remove server files or replace saved credentials. Choose **Start over** for the Ubuntu and connection questions. Choose Ubuntu, enter its IP, SSH port (normally 22), and a connection name. Touch ID authorizes setup. Compare the displayed SHA-256 SSH fingerprint with a trusted provider console or an already trusted SSH connection before accepting it. On the server console, `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256` shows the Ed25519 fingerprint; check the matching host-key type if your server negotiates a different key. An unverified first fingerprint is trust on first use, not independent server authentication.

During setup, the root password is entered into a native `NSSecureTextField`; it never reaches the React interface, command line, saved files, or Keychain. Root SSH password access must already be allowed by your server. Dockyard does not enable root password login or bypass your SSH policy. A working Touch ID sensor and enrolled fingerprint are required.

The app inspects the server read-only, then shows the installation plan. The installer currently supports Ubuntu 22.04 and 24.04, x86-64, with systemd/OpenSSH and standard protected filesystem paths. It refuses an existing Dockyard config. Other Ubuntu versions, architectures, and sudo-only login are not available in this wizard yet.

The app ships the agent/operator binaries built from this workspace. No GitHub token, release URL, or certificate purchase is needed for this initial local build. Agent and staged payload hashes are recorded in the installation receipt. The app checks modern SSH algorithms, stores the accepted SSH host fingerprint, and blocks a changed fingerprint before sending credentials.

The Ubuntu release repository is [fathah/ziqx-dockyard-service](https://github.com/fathah/ziqx-dockyard-service/releases). Pushing the `release` branch publishes Ubuntu binaries through the existing GitHub workflow. This installer uses the binaries bundled with the Mac app; it does not silently download or install a newer release.

## Installation and connection

After native review and another Touch ID scan, Rust generates a private server trust root, a separate dedicated Mac client trust root, the server/client leaf certificates, independent HMAC/retry secrets, and an Ed25519 connector key. The client key stays in Keychain. Root-owned server credentials/config files are uploaded with mode 600. The temporary recovery receipt in Keychain includes the installation files needed to resume, including the generated server key; it is removed after pinned mTLS/signed API verification succeeds. No credentials are returned to JavaScript.

The installer runs fixed operations rather than accepting shell commands from the UI:

- Install missing Docker Engine/Compose from [Docker's signed Ubuntu repository](https://docs.docker.com/engine/install/ubuntu/) and missing Caddy from [Caddy's official stable repository](https://caddyserver.com/docs/install). Existing Docker/Caddy installations are retained. Existing Compose below 2.30 or conflicting runtimes require an operator upgrade; the wizard does not remove/reinstall them.
- Preserve `/docker` and existing Compose/container files. Add root-controlled Dockyard paths, SQLite state, and private Docker config.
- Back up the Caddyfile; prepare its private admin socket and managed import. Adapt old/new configs and compare every non-admin JSON field. Validate before atomic disk replacement and reload. Custom/disabled admin settings or changed routes require manual preparation. A failed/unknown reload preserves the backup and receipt; it does not stop containers or guess a rollback.
- Create `dockyard-link`, which has no login shell or host privileges. Its root-owned public key and effective OpenSSH policy permit only local forwarding to `127.0.0.1:9123`; no shell, agent/X11 forwarding, TTY, or remote forwarding. Custom SSH policies that prevent these restrictions cause setup to stop.
- Install `/usr/local/bin/dockyard` and `dockyardctl`, validate the policy, populate safe production inventory, and enable the restarting `dockyard` systemd service. SQLite imports existing services as observations, not managed lifecycle targets.
- Verify the pinned server certificate, client mTLS identity and signed `/v1/projects` response through the restricted connector before showing a connected workspace.

The API remains loopback-only. Rust automatically opens a private local SSH tunnel when you unlock the app; locking ends the tunnel. A copied connector key alone grants neither shell/root access nor authenticated API access. As documented in [security boundaries](README.md#security-boundaries-and-production-signing), all copied Mac credentials or a compromised Mac can still impersonate the enrolled client.

The saved server IP, SSH port/fingerprint and connector key live in Keychain. Public connection fields appear in **Connection & security**. `/etc/dockyard/server.json` also records the IP and installation identity on the VPS for later configuration. Cloudflare DNS remains separately authorized by root policy and a zone-scoped token; it is not configured merely by knowing an IP.

## Deployment policy

Default setup enables full Compose management for this enrolled Mac. Compose access is root-equivalent and remains behind mTLS, signed requests and Touch ID. Templates are not required; optional advanced settings can retain legacy policies and customize limits. Existing projects are imported as observations, without taking over their running containers.

The advanced mode grants the dedicated Mac key all ten management scopes, covering all project IDs. Review that administrative permission in the native installation dialog. The server's `-check` command validates the resulting policy and credentials before enabling the daemon. Template edits after managed projects exist must follow the existing pin/migration boundary in [server installation](../docs/INSTALL.md).

## Interrupted setup

Before any server write, the app saves the exact generated identities and receipt in Keychain. On a disconnect, keep that state and choose **Resume server setup**. Authenticate with Touch ID, then re-enter the root password. Rust verifies the stored SSH host fingerprint, checks staged file hashes, repairs uncommitted partial uploads, and resumes the same installation. It never generates a second identity or overwrites a different existing installation.

The private server staging directory is `/var/lib/dockyard-desktop-setup/<installation-id>`. It keeps `Caddyfile.before` and, after installer failure, `error.txt`. Review those locally on the VPS if setup stops. Do not paste staged files or enrollment material into chat. Resume with the same app build if its bundled binary hashes changed; the app refuses to replace a pending installation's artifacts silently. Preserve the receipt and inspect the server before manually abandoning an interrupted installation.

Once complete, the VPS service runs independently of the Mac. Check it with `sudo systemctl status dockyard` and `sudo journalctl -u dockyard`. The installer does not configure backups, certificate renewal, automatic binary upgrades, existing-project lifecycle adoption, or broad Docker cleanup. Generated leaf certificates expire after 90 days; plan an explicit certificate/key rotation before expiry. Wizard CA signing keys are discarded after issuing the initial certificates; a rotation needs newly provisioned trust material and a matching enrollment/server policy update.

## Understanding setup errors

The desktop shows a plain-language reason, the affected server path when known, and the next step. Permission issues include a copyable command to run in the VPS terminal; the desktop never runs these corrective commands automatically. For example, `/etc/caddy` owned by another user shows `sudo chown root /etc/caddy`. This changes only that folder’s owner, retaining its group and contents. No recursive ownership changes are suggested for project or volume data.

Read-only inspection checks existing installation folders and every parent directory before the installation plan. Missing folders that setup will create are allowed. The same checks run again on installation and resume, so interrupted setups from an older app also get actionable ownership/permission advice. The progress list marks the failed step. Fix the issue, then choose **Connect and inspect** before installation or **Resume installation** for a saved setup.

Known installer failures explain dependency, Docker Compose, Caddy, SSH, file-integrity and service issues. Unknown failures identify the current step and direct the operator to the private staged `error.txt`. Remote command output and unknown exception text are withheld because they may include private configuration or credentials. Existing receipts keep their original installer, identities and checksums; upgrading the desktop does not rewrite them.

## Local checks and acceptance

Frontend/Rust checks, generated-identity/recovery tests, Caddy preparation tests, and disposable local TLS/SSH transport checks do not replace live VPS acceptance. Before production use, verify native password/Touch ID prompts and run the [Linux staging procedure](../docs/STAGING.md), including real systemd, Docker, Caddy reload, SSH restrictions, interrupted installation recovery and Cloudflare traffic. The local app is ad-hoc signed; Developer ID notarization remains required for distribution.
