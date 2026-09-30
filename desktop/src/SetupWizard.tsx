import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  AlertCircle,
  Check,
  Copy,
  KeyRound,
  LoaderCircle,
  RotateCcw,
  Server,
  Terminal,
  X,
} from "lucide-react";
import { listen } from "@tauri-apps/api/event";
import * as api from "./api";
import type { Session, SetupPreview } from "./types";
import { setupAdvice } from "./setupError";
import Help from "./Help";

const stages = [
  ["preflight", "Check server folders and permissions"],
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
  const [step, setStep] = useState(-1);
  const [name, setName] = useState("Production VPS");
  const [ip, setIP] = useState("");
  const [port, setPort] = useState("22");
  const [policy, setPolicy] = useState(false);
  const [plan, setPlan] = useState<SetupPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copyStatus, setCopyStatus] = useState("");
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
    setCopyStatus("");
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
    setCopyStatus("");
    setAttempted(true);
    setPhase("preflight");
    setStep(3);
    try {
      ready(
        resume ? await api.resumeSetup() : await api.installServer(plan!.id),
      );
    } catch (e) {
      setError(String(e));
      const advice = setupAdvice(String(e));
      if (advice?.phase && stages.some(([id]) => id === advice.phase))
        setPhase(advice.phase);
    } finally {
      setBusy(false);
    }
  }
  function dismiss() {
    if (busy) return;
    void api.cancelSetup().catch(() => {});
    close();
  }
  function chooseSetup() {
    if (busy) return;
    void api.cancelSetup().catch(() => {});
    setPlan(null);
    setError("");
    setCopyStatus("");
    setStep(-1);
  }
  function startOver() {
    if (busy) return;
    void api.cancelSetup().catch(() => {});
    setPlan(null);
    setName("Production VPS");
    setIP("");
    setPort("22");
    setPolicy(false);
    setError("");
    setCopyStatus("");
    setAttempted(false);
    setPhase("");
    setStep(0);
  }
  const current = stages.findIndex(([id]) => id === phase);
  const installedLocked = error.includes("Setup completed and was saved");
  const advice = setupAdvice(error);
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
            <div className="eyebrow">SERVER SETUP</div>
            <h2 id="setup-title">Set up your server</h2>
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
        {step >= 0 && (
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
        )}
        <div className="wizard-body setup-body">
          {!api.native && (
            <p className="setup-info">Preview · use the Mac app to install.</p>
          )}
          {error && (
            <div className="setup-error" role="alert">
              {advice ? (
                <>
                  <strong>{advice.title}</strong>
                  <p>{advice.message}</p>
                  <p>{advice.action}</p>
                  {advice.command && (
                    <div className="setup-command">
                      <span>Run in your VPS terminal</span>
                      <code tabIndex={0}>{advice.command}</code>
                      <button
                        className="button"
                        type="button"
                        onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(
                              advice.command!,
                            );
                            setCopyStatus("Command copied");
                          } catch {
                            setCopyStatus(
                              "Select the command above and copy it manually",
                            );
                          }
                        }}
                      >
                        <Copy size={14} /> Copy command
                      </button>
                      {copyStatus && <span role="status">{copyStatus}</span>}
                    </div>
                  )}
                  <p>
                    {step === 3
                      ? "After fixing the issue, click Resume installation below. Your existing setup credentials will be reused."
                      : "After fixing the issue, click Connect and inspect again."}
                  </p>
                </>
              ) : error.startsWith("DOCKYARD_SETUP_ERROR:") ? (
                "Setup needs attention. Close this window and retry the saved installation."
              ) : (
                error
              )}
            </div>
          )}
          {step === -1 && (
            <>
              <h3>Continue setup</h3>
              <div className="setup-options">
                <div className="setup-option">
                  <button
                    className="setup-choice recommended"
                    disabled={!api.native || busy}
                    onClick={() => void install(true)}
                  >
                    <span className="setup-choice-icon">
                      <RotateCcw size={24} />
                    </span>
                    <span className="setup-choice-copy">
                      <strong>Resume setup</strong>
                    </span>
                    <ArrowRight size={20} />
                  </button>
                  <Help label="resume setup">
                    Continue the saved installation using the same server,
                    credentials and checksums.
                  </Help>
                </div>
                <div className="setup-option">
                  <button
                    className="setup-choice"
                    disabled={busy}
                    onClick={startOver}
                  >
                    <span className="setup-choice-icon">
                      <Server size={24} />
                    </span>
                    <span className="setup-choice-copy">
                      <strong>Start over</strong>
                    </span>
                    <ArrowRight size={20} />
                  </button>
                  <Help label="start over">
                    Choose Ubuntu and enter server details again. This resets
                    the questions; your saved installation is preserved and can
                    still be resumed.
                  </Help>
                </div>
              </div>
            </>
          )}
          {step === 0 && (
            <>
              <h3>
                Operating system
                <Help label="supported operating systems">
                  The installer supports Ubuntu 22.04 and 24.04 on x86-64
                  servers. ARM and other operating systems are not supported
                  yet. Dockyard runs continuously on the VPS.
                </Help>
              </h3>
              <button className="setup-os selected" onClick={() => setStep(1)}>
                <div className="ubuntu-mark">◎</div>
                <div>
                  <strong>Ubuntu</strong>
                  <span>22.04 / 24.04 LTS · x86-64</span>
                </div>
                <Check size={19} />
              </button>
              <div className="help-row">
                Automatic enrollment
                <Help label="automatic enrollment">
                  Certificates are generated automatically and saved in
                  Keychain. You do not need to buy or select a certificate.
                </Help>
              </div>
            </>
          )}
          {step === 1 && (
            <>
              <h3>
                Server connection
                <Help label="server verification">
                  Compare the SSH fingerprint with your VPS provider before
                  connecting. macOS asks for the root password after
                  verification. Your IP and fingerprint are saved; the setup
                  password is not.
                </Help>
              </h3>
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
                  <strong>
                    Root password
                    <Help label="setup root password">
                      Entered in a native macOS secure field after SSH
                      verification. Used for setup only; never saved. Terminal
                      password storage is a separate option.
                    </Help>
                  </strong>
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
                  Import advanced settings
                  <Help label="deployment policy">
                    Optional JSON configuration for server limits and legacy policies. New projects use Compose and .env.
                  </Help>
                </span>
              </label>
              <span className="tag">Compose management</span>
            </>
          )}
          {step === 2 && plan && (
            <>
              <h3>
                Review installation
                <Help label="installation changes">
                  Dockyard installs a 24/7 systemd service. Existing Compose
                  files are preserved. Caddy is backed up, and its routes must
                  match before reloading. The API stays private, using a
                  restricted SSH connection protected by Touch ID.
                </Help>
              </h3>
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
                    : "Compose management · full server privileges"}
                </dd>
                <dt>Agent checksum</dt>
                <dd className="mono">{plan.binary_sha256}</dd>
              </dl>
            </>
          )}
          {step === 3 && (
            <>
              <h3>
                {busy
                  ? "Setting up your server"
                  : installedLocked
                    ? "Dockyard is installed"
                    : "Resume installation"}
                <Help label="installation recovery">
                  The receipt and credentials are saved in Keychain. If
                  interrupted, resume the same setup after reopening the app. No
                  setup root password is retained.
                </Help>
              </h3>
              <p>
                {busy
                  ? "Keep Dockyard open during installation."
                  : installedLocked
                    ? "Close this window and unlock with Touch ID to connect to your saved server."
                    : "Resume to continue this installation."}
              </p>
              <div className="setup-progress" aria-live="polite">
                {stages.map(([id, label], i) => (
                  <div
                    key={id}
                    className={
                      i === current && error && !installedLocked
                        ? "failed"
                        : i < current
                          ? "done"
                          : i === current
                            ? "current"
                            : ""
                    }
                  >
                    {i === current && error && !installedLocked ? (
                      <AlertCircle size={17} aria-label="Needs attention" />
                    ) : i < current ? (
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
            </>
          )}
        </div>
        <div className="modal-footer">
          <button
            className="button"
            disabled={busy}
            onClick={
              step === 0 || (step === 3 && !busy && !installedLocked)
                ? chooseSetup
                : step === 1 || step === 2
                  ? () => {
                      void api.cancelSetup().catch(() => {});
                      setPlan(null);
                      setStep(step - 1);
                    }
                  : dismiss
            }
          >
            {step === 0 || (step === 3 && !busy && !installedLocked)
              ? "Back to setup options"
              : step === 1 || step === 2
                ? "Back"
                : "Close"}
          </button>
          {step === 0 && (
            <button className="button primary" onClick={() => setStep(1)}>
              Continue <ArrowRight size={15} />
            </button>
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
