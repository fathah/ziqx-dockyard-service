import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  Check,
  Fingerprint,
  KeyRound,
  LoaderCircle,
  Server,
  ShieldCheck,
  Terminal,
  X,
} from "lucide-react";
import { listen } from "@tauri-apps/api/event";
import * as api from "./api";
import type { Session, SetupPreview } from "./types";

const stages = [
  ["verify", "Verify installation files"],
  ["dependencies", "Prepare Docker, Compose and Caddy"],
  ["credentials", "Install private server credentials"],
  ["caddy", "Validate and connect Caddy"],
  ["ssh", "Create a restricted SSH connection"],
  ["service", "Start Dockyard and sync production inventory"],
  ["complete", "Verify your Mac’s secure API access"],
] as const;

export default function SetupWizard({
  close,
  ready,
}: {
  close: () => void;
  ready: (s: Session) => void;
}) {
  const [step, setStep] = useState(0);
  const [name, setName] = useState("Production VPS");
  const [ip, setIP] = useState("");
  const [port, setPort] = useState("22");
  const [policy, setPolicy] = useState(false);
  const [plan, setPlan] = useState<SetupPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [phase, setPhase] = useState("");
  const [attempted, setAttempted] = useState(false);
  const dialog = useRef<HTMLElement>(null);
  const busyRef = useRef(false);
  busyRef.current = busy;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    dialog.current?.querySelector<HTMLElement>("button, input")?.focus();
    const keyboard = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busyRef.current) {
        e.preventDefault();
        void api.cancelSetup().catch(() => {});
        close();
      }
      if (e.key !== "Tab") return;
      const elements = Array.from(
        dialog.current?.querySelectorAll<HTMLElement>(
          'button:not(:disabled), input:not(:disabled), [tabindex="0"]',
        ) ?? [],
      );
      const first = elements[0],
        last = elements.at(-1);
      if (!dialog.current?.contains(document.activeElement)) {
        e.preventDefault();
        (e.shiftKey ? last : first)?.focus();
        return;
      }
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last?.focus();
      }
      if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", keyboard);
    return () => {
      document.removeEventListener("keydown", keyboard);
      if (previous?.isConnected) previous.focus();
    };
  }, [close]);
  useEffect(() => {
    if (!api.native) return;
    let cleanup: (() => void) | undefined;
    let alive = true;
    void listen<{ phase: string }>("setup-progress", ({ payload }) => {
      if (alive && stages.some(([id]) => id === payload.phase))
        setPhase(payload.phase);
    }).then((fn) => {
      if (alive) cleanup = fn;
      else fn();
    });
    return () => {
      alive = false;
      cleanup?.();
    };
  }, []);
  async function inspect() {
    setBusy(true);
    setError("");
    try {
      const plan = await api.inspectServer({
        os: "ubuntu",
        server_ip: ip.trim(),
        ssh_port: Number(port),
        name: name.trim(),
        import_policy: policy,
      });
      setPlan(plan);
      setStep(2);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  async function install(resume = false) {
    setBusy(true);
    setError("");
    setAttempted(true);
    setPhase("verify");
    setStep(3);
    try {
      ready(
        resume ? await api.resumeSetup() : await api.installServer(plan!.id),
      );
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  function dismiss() {
    if (busy) return;
    void api.cancelSetup().catch(() => {});
    close();
  }
  const current = stages.findIndex(([id]) => id === phase);
  const installedLocked = error.includes("Setup completed and was saved");
  return (
    <div className="modal-backdrop">
      <section
        className="modal setup-modal"
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby="setup-title"
      >
        <div className="modal-heading">
          <div>
            <div className="eyebrow">FIRST-TIME SERVER SETUP</div>
            <h2 id="setup-title">Set up Dockyard on your server</h2>
          </div>
          <button
            className="icon-button"
            aria-label="Close server setup"
            disabled={busy}
            onClick={dismiss}
          >
            <X size={20} />
          </button>
        </div>
        <div className="wizard-steps">
          {["Operating system", "Connection", "Review", "Install"].map(
            (label, i) => (
              <span
                key={label}
                className={step === i ? "current" : step > i ? "done" : ""}
              >
                <i>{step > i ? <Check size={12} /> : i + 1}</i>
                {label}
              </span>
            ),
          )}
        </div>
        <div className="wizard-body setup-body">
          {!api.native && (
            <p className="setup-info">
              This is a read-only browser preview. Server setup runs in the
              native Mac app.
            </p>
          )}
          {error && (
            <div className="setup-error" role="alert">
              {error}
            </div>
          )}
          {step === 0 && (
            <>
              <h3>Choose your server’s operating system</h3>
              <p>
                Dockyard runs continuously on the VPS. This Mac app connects
                whenever you need to manage it.
              </p>
              <button className="setup-os selected" onClick={() => setStep(1)}>
                <div className="ubuntu-mark">◎</div>
                <div>
                  <strong>Ubuntu</strong>
                  <span>22.04 / 24.04 LTS · x86-64</span>
                </div>
                <Check size={19} />
              </button>
              <p className="muted">
                Other operating systems and ARM servers are not available in
                this installer yet.
              </p>
              <div className="setup-info">
                <ShieldCheck size={18} />
                <span>
                  Enrollment certificates are generated automatically and saved
                  in Keychain. You do not need to buy or select a certificate.
                </span>
              </div>
            </>
          )}
          {step === 1 && (
            <>
              <h3>Connect to your Ubuntu VPS</h3>
              <p>
                Verify the SSH fingerprint, then enter the root password in the
                native Mac prompt.
              </p>
              <label className="field-label">
                Connection name
                <input
                  value={name}
                  maxLength={80}
                  onChange={(e) => setName(e.target.value)}
                  disabled={busy}
                />
              </label>
              <div className="form-row">
                <label className="field-label">
                  Server IP
                  <input
                    autoComplete="off"
                    value={ip}
                    placeholder="203.0.113.10"
                    onChange={(e) => setIP(e.target.value)}
                    disabled={busy}
                  />
                </label>
                <label className="field-label">
                  SSH port
                  <input
                    type="number"
                    min={1}
                    max={65535}
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                    disabled={busy}
                  />
                </label>
              </div>
              <div className="setup-password">
                <KeyRound size={18} />
                <div>
                  <strong>Root password</strong>
                  <span>
                    Asked securely by macOS after SSH verification. Used for
                    setup only.
                  </span>
                </div>
              </div>
              <label className="setup-checkbox">
                <input
                  type="checkbox"
                  checked={policy}
                  onChange={(e) => setPolicy(e.target.checked)}
                  disabled={busy}
                />
                <span>
                  Import an approved deployment policy{" "}
                  <small>
                    Optional private JSON file for allowed images, domains and
                    service templates.
                  </small>
                </span>
              </label>
              <p className="muted">
                Without a deployment policy, setup enables inventory and log
                access. Deployment permissions stay disabled until an operator
                configures the root policy. Existing projects are synced as
                production observations.
              </p>
              <div className="setup-info">
                <Fingerprint size={18} />
                <span>
                  Your server IP and SSH fingerprint are saved for future
                  connections. Your root password is never saved.
                </span>
              </div>
            </>
          )}
          {step === 2 && plan && (
            <>
              <h3>Review the installation</h3>
              <dl className="setup-details">
                <dt>Server</dt>
                <dd>
                  {plan.server_ip}:{plan.ssh_port}
                </dd>
                <dt>Operating system</dt>
                <dd>Ubuntu {plan.ubuntu} · x86-64</dd>
                <dt>SSH fingerprint</dt>
                <dd className="mono">{plan.host_sha256}</dd>
                <dt>Docker / Caddy</dt>
                <dd>
                  {plan.docker_installed
                    ? "Keep existing Docker"
                    : "Install Docker + Compose"}{" "}
                  ·{" "}
                  {plan.caddy_installed
                    ? "Keep existing Caddy"
                    : "Install Caddy"}
                </dd>
                <dt>Access</dt>
                <dd>
                  {plan.inventory_only
                    ? "Inventory and logs"
                    : "Your approved deployment policy"}
                </dd>
                <dt>Agent checksum</dt>
                <dd className="mono">{plan.binary_sha256}</dd>
              </dl>
              <div className="setup-info">
                <Server size={19} />
                <span>
                  A 24/7 systemd service will manage the VPS. Existing Compose
                  files stay in place. Caddy is backed up and its routes must
                  match before a reload is allowed.
                </span>
              </div>
              <p className="muted">
                The API stays on 127.0.0.1:9123. Future connections use a
                restricted SSH key in Keychain, protected by Touch ID. The
                native confirmation will show this installation plan again.
              </p>
            </>
          )}
          {step === 3 && (
            <>
              <h3>
                {busy
                  ? "Setting up your server"
                  : installedLocked
                    ? "Dockyard is installed"
                    : "Resume the saved installation"}
              </h3>
              <p>
                {busy
                  ? "Keep Dockyard open. Dependency installation can take several minutes."
                  : installedLocked
                    ? "Close this window and unlock with Touch ID to connect to your saved server."
                    : "The installation may have made changes. Resume the same setup to keep its saved credentials and checksums."}
              </p>
              <div className="setup-progress" aria-live="polite">
                {stages.map(([id, label], i) => (
                  <div
                    key={id}
                    className={
                      i < current ? "done" : i === current ? "current" : ""
                    }
                  >
                    {i < current ? (
                      <Check size={17} />
                    ) : i === current && busy ? (
                      <LoaderCircle size={17} className="spin" />
                    ) : (
                      <span className="stage-dot" />
                    )}
                    <span>{label}</span>
                  </div>
                ))}
              </div>
              <p className="muted">
                An interrupted setup remains saved in Keychain. You can resume
                it after reopening the app; no root password is retained.
              </p>
            </>
          )}
        </div>
        <div className="modal-footer">
          <button
            className="button"
            disabled={busy}
            onClick={
              step === 1 || step === 2
                ? () => {
                    void api.cancelSetup().catch(() => {});
                    setPlan(null);
                    setStep(step - 1);
                  }
                : dismiss
            }
          >
            {step === 1 || step === 2 ? "Back" : "Close"}
          </button>
          {step === 0 && (
            <>
              <button
                className="text-button"
                disabled={!api.native || busy}
                onClick={() => void install(true)}
              >
                Resume server setup
              </button>
              <button className="button primary" onClick={() => setStep(1)}>
                Continue <ArrowRight size={15} />
              </button>
            </>
          )}
          {step === 1 && (
            <button
              className="button primary"
              disabled={
                !api.native ||
                busy ||
                !ip.trim() ||
                !name.trim() ||
                !Number.isInteger(Number(port)) ||
                Number(port) < 1 ||
                Number(port) > 65535
              }
              onClick={() => void inspect()}
            >
              {busy ? (
                <>
                  <LoaderCircle size={16} className="spin" /> Waiting for macOS…
                </>
              ) : (
                <>
                  Connect and inspect <ArrowRight size={15} />
                </>
              )}
            </button>
          )}
          {step === 2 && (
            <button
              className="button primary"
              disabled={busy}
              onClick={() => void install()}
            >
              <Terminal size={16} /> Install Dockyard
            </button>
          )}
          {step === 3 && attempted && !busy && !installedLocked && (
            <button
              className="button primary"
              onClick={() => void install(true)}
            >
              Resume server setup <ArrowRight size={15} />
            </button>
          )}
        </div>
      </section>
    </div>
  );
}
