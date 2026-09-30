import { useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { Terminal as XTerminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import {
  Fingerprint,
  Terminal,
  Unplug,
  ShieldCheck,
  Trash2,
  Box,
} from "lucide-react";
import "@xterm/xterm/css/xterm.css";
import Help from "./Help";

type Container = { id: string; name: string; image: string; status: string };
type Connection = {
  id: string;
  containers: Container[];
  container_warning: string | null;
  password_saved: boolean;
};
type Output = { data: string; closed: boolean; reason: string | null };
const encode = (data: Uint8Array) =>
  btoa(Array.from(data, (b) => String.fromCharCode(b)).join(""));
const decode = (data: string) =>
  Uint8Array.from(atob(data), (c) => c.charCodeAt(0));

export default function TerminalPage({
  visible,
  native,
  server,
  report,
}: {
  visible: boolean;
  native: boolean;
  server?: string | null;
  report: (e: unknown) => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<XTerminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const id = useRef<string | null>(null);
  const started = useRef(false);
  const epoch = useRef(0);
  const inputQueue = useRef(Promise.resolve());
  const queued = useRef(0);
  const reportRef = useRef(report);
  reportRef.current = report;
  const [connection, setConnection] = useState<Connection | null>(null);
  const [mode, setMode] = useState("root");
  const [container, setContainer] = useState("");
  const [shell, setShell] = useState("/bin/sh");
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [active, setActive] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const fail = (e: unknown) => {
    const text = String(e);
    setError(text);
    if (text.includes("SESSION_LOCKED")) reportRef.current(e);
  };
  const disconnect = async () => {
    epoch.current++;
    const old = id.current;
    id.current = null;
    started.current = false;
    if (term.current) term.current.options.disableStdin = true;
    setActive(false);
    setConnection(null);
    setMessage("Disconnected");
    if (old) await invoke("terminal_close", { id: old }).catch(() => {});
  };
  useEffect(() => {
    const terminal = new XTerminal({
      cursorBlink: true,
      disableStdin: true,
      scrollback: 2000,
      fontSize: 14,
      fontFamily: "Menlo, Monaco, monospace",
      convertEol: false,
      theme: {
        background: "#102d25",
        foreground: "#edf5e8",
        cursor: "#cfdfae",
        selectionBackground: "#47684a",
      },
      allowProposedApi: false,
      windowOptions: {},
      linkHandler: { activate: () => {} },
    });
    // Remote programs cannot read/write the Mac clipboard or create clickable links.
    terminal.parser.registerOscHandler(52, () => true);
    terminal.parser.registerOscHandler(8, () => true);
    const addon = new FitAddon();
    terminal.loadAddon(addon);
    term.current = terminal;
    fit.current = addon;
    terminal.open(host.current!);
    const send = (bytes: Uint8Array) => {
      const current = id.current;
      if (!current || !started.current) return;
      if (bytes.length > 8192 || queued.current >= 16) {
        setError("Terminal input is busy. Paste at most 8 KiB at a time.");
        return;
      }
      queued.current++;
      inputQueue.current = inputQueue.current
        .then(async () => {
          if (id.current !== current || !started.current) return;
          await invoke("terminal_input", { id: current, data: encode(bytes) });
        })
        .catch(fail)
        .finally(() => {
          queued.current--;
        });
    };
    const data = terminal.onData((text) =>
      send(new TextEncoder().encode(text)),
    );
    const binary = terminal.onBinary((text) =>
      send(Uint8Array.from(text, (c) => c.charCodeAt(0) & 255)),
    );
    const resized = terminal.onResize(({ cols, rows }) => {
      const current = id.current;
      if (current && started.current)
        void invoke("terminal_resize", { id: current, cols, rows }).catch(fail);
    });
    let timer: ReturnType<typeof setTimeout>;
    const observer = new ResizeObserver(() => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (host.current?.offsetWidth) addon.fit();
      }, 150);
    });
    observer.observe(host.current!);
    let mounted = true;
    let pollTimer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const current = id.current;
      if (current && started.current) {
        try {
          const output = await invoke<Output>("terminal_poll", { id: current });
          if (!mounted || id.current !== current) return;
          if (output.data)
            await new Promise<void>((resolve) =>
              terminal.write(decode(output.data), resolve),
            );
          if (output.closed) {
            started.current = false;
            terminal.options.disableStdin = true;
            setActive(false);
            setMessage(output.reason || "Shell exited");
            id.current = null;
            setConnection(null);
            void invoke("terminal_close", { id: current }).catch(() => {});
          }
        } catch (e) {
          if (mounted && id.current === current) {
            fail(e);
            void disconnect();
          }
        }
      }
      if (mounted) pollTimer = setTimeout(() => void poll(), 50);
    };
    void poll();
    return () => {
      mounted = false;
      epoch.current++;
      clearTimeout(timer);
      clearTimeout(pollTimer);
      observer.disconnect();
      data.dispose();
      binary.dispose();
      resized.dispose();
      const old = id.current;
      id.current = null;
      started.current = false;
      if (old) void invoke("terminal_close", { id: old }).catch(() => {});
      terminal.dispose();
      term.current = null;
      fit.current = null;
    };
    // A terminal remains mounted when navigating between Dockyard pages.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    if (visible)
      requestAnimationFrame(() => {
        fit.current?.fit();
        if (active) term.current?.focus();
      });
  }, [visible, active]);
  const start = async (current: Connection) => {
    if (mode === "container" && !container) return;
    fit.current?.fit();
    const terminal = term.current!;
    await invoke("terminal_start", {
      id: current.id,
      container: mode === "container" ? container : null,
      shell,
      cols: Math.max(10, Math.min(500, terminal.cols)),
      rows: Math.max(5, Math.min(240, terminal.rows)),
    });
    if (id.current !== current.id) return;
    started.current = true;
    terminal.options.disableStdin = false;
    setActive(true);
    setMessage(
      mode === "root" ? "Root shell · full server access" : "Container shell",
    );
    terminal.focus();
  };
  const connect = async () => {
    const turn = ++epoch.current;
    setBusy(true);
    setError("");
    setMessage("");
    term.current?.reset();
    try {
      const result = await invoke<Connection>("terminal_connect", { remember });
      if (epoch.current !== turn) {
        await invoke("terminal_close", { id: result.id });
        return;
      }
      id.current = result.id;
      setConnection(result);
      setContainer("");
      if (mode === "root") await start(result);
      else
        setMessage(
          "Connected. Choose a running container, then open its shell.",
        );
    } catch (e) {
      if (epoch.current === turn) {
        fail(e);
        const old = id.current;
        id.current = null;
        setConnection(null);
        if (old) void invoke("terminal_close", { id: old });
      }
    } finally {
      if (epoch.current === turn) setBusy(false);
    }
  };
  return (
    <section
      className="terminal-page"
      style={{ display: visible ? undefined : "none" }}
    >
      <div className="page-heading">
        <div>
          <h1>
            Terminal
            <Help label="terminal access">
              Connect to the enrolled VPS over pinned SSH. Each connection
              requires Touch ID. The shell stays open when switching pages and
              closes when Dockyard locks. Disconnecting does not stop commands
              already running remotely.
            </Help>
          </h1>
        </div>
        <ShieldCheck size={28} />
      </div>
      <div className="terminal-access card">
        <div className="terminal-controls">
          <div
            className="terminal-targets"
            role="group"
            aria-label="Terminal target"
          >
            <button
              className={`terminal-target ${mode === "root" ? "selected" : ""}`}
              disabled={busy || !!connection}
              onClick={() => setMode("root")}
              title="Full server access"
            >
              <Terminal size={18} />
              <strong>VPS root</strong>
            </button>
            <button
              className={`terminal-target ${mode === "container" ? "selected" : ""}`}
              disabled={busy || !!connection}
              onClick={() => setMode("container")}
              title="Configured container user"
            >
              <Box size={18} />
              <strong>Container</strong>
            </button>
          </div>
          <div className="terminal-toolbar">
            <label className="terminal-remember">
              <input
                type="checkbox"
                checked={remember}
                disabled={busy || !!connection}
                onChange={(e) => setRemember(e.target.checked)}
              />
              Save password for future app launches
              <Help label="remembering the SSH password">
                By default, root access is remembered only until Dockyard closes.
                Enable this to also save the password in this Mac’s Keychain.
                Touch ID still protects root access. Use Forget password to
                remove an existing saved password.
              </Help>
            </label>
            {connection ? (
              <button
                className="button secondary"
                disabled={busy}
                onClick={() => void disconnect()}
              >
                <Unplug size={16} />
                Disconnect
              </button>
            ) : (
              <button
                className="button"
                disabled={busy || !native || !server}
                onClick={() => void connect()}
              >
                <Fingerprint size={17} />
                {busy ? "Connecting…" : "Connect with Touch ID"}
              </button>
            )}
          </div>
        </div>
        <div className="terminal-access-meta">
          <span>
            {server || "No server connected"}
            <Help label="SSH security">
              The SSH fingerprint is checked before sending credentials.
              Password entry uses a native macOS secure field and stays outside
              the webview. Container mode also authenticates as root over SSH,
              then starts the shell as the container’s configured user.
            </Help>
          </span>
          <button
            className="terminal-forget"
            disabled={!native || busy || !server}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                await invoke("terminal_forget_password");
                setMessage("Saved root SSH password removed from Keychain.");
              } catch (e) {
                fail(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            <Trash2 size={14} />
            Forget password
          </button>
        </div>
        {connection && mode === "container" && !active && (
          <div className="terminal-container-picker">
            <label>
              Running container
              <select
                value={container}
                onChange={(e) => setContainer(e.target.value)}
                disabled={busy}
              >
                <option value="">Choose a container…</option>
                {connection.containers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} · {c.image}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Shell
              <select
                value={shell}
                onChange={(e) => setShell(e.target.value)}
                disabled={busy}
              >
                <option value="/bin/sh">sh</option>
                <option value="/bin/bash">bash (if installed)</option>
              </select>
            </label>
            <button
              className="button"
              disabled={!container || busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await start(connection);
                } catch (e) {
                  fail(e);
                  await disconnect();
                } finally {
                  setBusy(false);
                }
              }}
            >
              Open container shell
            </button>
          </div>
        )}
        {connection &&
          mode === "container" &&
          !connection.containers.length &&
          !connection.container_warning && (
            <p className="terminal-security-note">
              No containers are running. Disconnect and choose VPS root to
              inspect the server.
            </p>
          )}
        {connection?.container_warning && (
          <div className="alert error">
            {connection.container_warning}. Root shell is still available;
            disconnect to change the target.
          </div>
        )}
        {error && (
          <div className="alert error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="terminal-window">
        <div className="terminal-window-bar">
          <span className={`terminal-dot ${active ? "online" : ""}`} />
          <span>
            {active
              ? mode === "root"
                ? `root@${server}`
                : connection?.containers.find((c) => c.id === container)?.name
              : "Terminal"}
          </span>
          <span>{active ? "Connected" : "Disconnected"}</span>
        </div>
        <div ref={host} className="terminal-screen" />
      </div>
      <p className="terminal-status" role="status">
        {message ||
          (native ? "Disconnected" : "Preview · use the Mac app to connect.")}
      </p>
    </section>
  );
}
