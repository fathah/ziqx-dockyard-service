import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Activity,
  ArrowDownLeft,
  ArrowRight,
  ArrowUpRight,
  Box,
  Check,
  CheckCheck,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Cloud,
  Code2,
  Database,
  FileCode2,
  Fingerprint,
  FolderGit2,
  Globe2,
  History,
  Layers3,
  LayoutDashboard,
  LockKeyhole,
  Plus,
  Radio,
  RefreshCw,
  Search,
  Server,
  Settings2,
  ShieldCheck,
  Terminal,
  Upload,
  X,
  Zap,
} from "lucide-react";
import { listen } from "@tauri-apps/api/event";
import * as api from "./api";
import SetupWizard from "./SetupWizard";
import { demoInventory, demoJobs, demoProjects } from "./demo";
import type {
  Environment,
  Inventory,
  Job,
  Pending,
  Project,
  Profile,
  Release,
  Service,
  Session,
} from "./types";

type Page =
  "projects" | "deployments" | "domains" | "inventory" | "audit" | "security";
type Modal =
  | { kind: "create" }
  | { kind: "deploy" | "routes" | "stop" | "rollback"; project: Project }
  | null;
type Audit = { event_id: number; event: Record<string, unknown> };
const nav = [
  { id: "projects", label: "Projects", icon: LayoutDashboard },
  { id: "deployments", label: "Deployments", icon: Layers3 },
  { id: "domains", label: "Domains", icon: Globe2 },
  { id: "inventory", label: "VPS inventory", icon: Database },
  { id: "audit", label: "Audit trail", icon: History },
] as const;
const environments: Environment[] = ["production", "staging", "development"];
const labels: Record<Environment, string> = {
  production: "Production",
  staging: "Staging",
  development: "Development",
};
const ago = (s?: string) =>
  s
    ? new Date(s).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "Not observed";
const initials = (s: string) =>
  s
    .split("-")
    .map((p) => p[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
function Tag({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: string;
}) {
  return <span className={`tag ${tone}`}>{children}</span>;
}
function Empty({
  icon: Icon = Box,
  title,
  children,
}: {
  icon?: typeof Box;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty">
      <Icon size={30} />
      <h3>{title}</h3>
      <p>{children}</p>
    </div>
  );
}
function Brand() {
  return (
    <>
      <div className="brand-mark">
        <Box size={25} strokeWidth={1.7} />
        <span />
      </div>
      <span>
        dockyard<span className="brand-dot">.</span>
      </span>
    </>
  );
}
function App() {
  const [preview, setPreview] = useState(!api.native);
  const [session, setSession] = useState<Session>({
    unlocked: false,
    profile: null,
    jobs: [],
  });
  const [page, setPage] = useState<Page>("projects");
  const [projects, setProjects] = useState<Project[]>([]);
  const [inventory, setInventory] = useState<Inventory | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [pending, setPending] = useState<Pending>(null);
  const [audit, setAudit] = useState<Audit[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [modal, setModal] = useState<Modal>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(false);
  const [authBusy, setAuthBusy] = useState(false);
  const [setupOpen, setSetupOpen] = useState(false);
  const closeSetup = useCallback(() => setSetupOpen(false), []);
  const [updated, setUpdated] = useState<string>();
  const epoch = useRef(0);
  const unlocked = useRef(false);
  const refreshing = useRef<number | null>(null);
  const canUse = preview || session.unlocked;
  const loseSession = useCallback(() => {
    epoch.current++;
    unlocked.current = false;
    setSession((s) => ({ ...s, unlocked: false, jobs: [] }));
    setProjects([]);
    setInventory(null);
    setJobs([]);
    setAudit([]);
    setPending(null);
    setModal(null);
    setSelected(null);
    setUpdated(undefined);
    setLoading(false);
    setSetupOpen(false);
  }, []);
  const report = useCallback(
    (e: unknown) => {
      const message = String(e);
      if (message.includes("SESSION_LOCKED")) {
        loseSession();
        setError("Your session is locked. Authenticate to continue.");
      } else setError(message);
    },
    [loseSession],
  );
  const refresh = useCallback(
    async (ids?: string[]) => {
      if (!unlocked.current) return;
      const version = epoch.current;
      if (refreshing.current === version) return;
      refreshing.current = version;
      setLoading(true);
      setError("");
      try {
        const data = await api.read<{ projects: Project[] }>({
          kind: "projects",
        });
        if (epoch.current !== version) return;
        setProjects(data.projects ?? []);
        const inv = await api
          .read<Inventory>({ kind: "inventory" })
          .catch((e) => {
            if (epoch.current === version) report(e);
            return null;
          });
        if (epoch.current !== version) return;
        setInventory(inv);
        setUpdated(new Date().toISOString());
        const pending = await api.pendingInfo();
        if (epoch.current !== version) return;
        setPending(pending);
        const info = await api.sessionInfo();
        if (epoch.current !== version) return;
        if (!info.unlocked) {
          loseSession();
          return;
        }
        setSession(info);
        const tracked = Array.from(
          new Set([
            ...(pending?.job_id ? [pending.job_id] : []),
            ...(ids ?? info.jobs),
          ]),
        ).slice(0, 10);
        const fetched: Job[] = [];
        for (const job of tracked) {
          try {
            fetched.push(await api.read<Job>({ kind: "job", job }));
          } catch (e) {
            if (epoch.current === version) report(e);
          }
        }
        if (epoch.current !== version) return;
        setJobs(fetched);
        const remaining = await api.pendingInfo();
        if (epoch.current === version) setPending(remaining);
      } catch (e) {
        if (epoch.current === version) report(e);
      } finally {
        if (refreshing.current === version) refreshing.current = null;
        if (epoch.current === version) setLoading(false);
      }
    },
    [report, loseSession],
  );
  useEffect(() => {
    if (!api.native) return;
    let disposed = false;
    let off: (() => void) | undefined;
    api
      .sessionInfo()
      .then((s) => {
        if (!disposed) {
          setSession(s);
          unlocked.current = s.unlocked;
          if (s.unlocked) void refresh();
        }
      })
      .catch(report);
    listen("session-locked", () => {
      loseSession();
      setNotice("Session locked. VPS services continue running.");
    })
      .then((u) => {
        if (disposed) u();
        else off = u;
      })
      .catch(report);
    return () => {
      disposed = true;
      off?.();
    };
  }, [loseSession, refresh, report]);
  useEffect(() => {
    if (preview || !session.unlocked) return;
    const timer = setInterval(() => void refresh(), 30000);
    return () => clearInterval(timer);
  }, [preview, session.unlocked, refresh]);
  useEffect(() => {
    if (!notice) return;
    const t = setTimeout(() => setNotice(""), 5000);
    return () => clearTimeout(t);
  }, [notice]);
  async function authenticate(method: "enroll" | "unlock") {
    if (!api.native) {
      setError(
        "Install the native macOS app to enroll a server. This browser preview has no API access.",
      );
      return;
    }
    loseSession();
    setPreview(false);
    setAuthBusy(true);
    setError("");
    try {
      const s = await api[method]();
      epoch.current++;
      unlocked.current = true;
      setSession(s);
      setPreview(false);
      setPage("projects");
      await refresh(s.jobs);
    } catch (e) {
      report(e);
    } finally {
      setAuthBusy(false);
    }
  }
  async function perform(mutation: unknown) {
    if (preview)
      throw new Error("Preview is read-only. Enroll this Mac to manage a VPS.");
    const version = epoch.current;
    const result = await api.mutate(mutation);
    if (version !== epoch.current) return;
    setNotice(`Operation accepted · ${result.job_id}`);
    setModal(null);
    await refresh([result.job_id, ...session.jobs]);
    setPage("deployments");
    setSelected(null);
  }
  async function simple(action: "start" | "restart", project: Project) {
    try {
      await perform({ action, project: project.id });
    } catch (e) {
      report(e);
    }
  }
  async function doRetry() {
    try {
      await api.retry();
      await refresh();
    } catch (e) {
      report(e);
    }
  }
  const shownProjects = preview ? demoProjects : projects;
  const shownInventory = preview ? demoInventory : inventory;
  const shownJobs = preview ? demoJobs : jobs;
  const project = shownProjects.find((p) => p.id === selected);
  const busy = authBusy || loading;
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <Brand />
        </div>
        <div className="workspace-label">CONTROL ROOM</div>
        <button
          className="server-select"
          onClick={() => {
            setPage("security");
            setSelected(null);
          }}
        >
          <div className="server-symbol">
            <Server size={18} />
          </div>
          <div>
            <strong>
              {preview
                ? "Preview workspace"
                : (session.profile?.name ?? "Your VPS")}
            </strong>
            <span>
              <i
                className={
                  session.unlocked && !preview ? "green-dot" : "gray-dot"
                }
              />
              {preview
                ? "Sample data"
                : session.unlocked
                  ? "Private connection"
                  : "Not connected"}
            </span>
          </div>
          <ChevronDown size={14} />
        </button>
        <div className="nav-label">WORKSPACE</div>
        <nav>
          {nav.map((n) => (
            <button
              key={n.id}
              className={page === n.id ? "nav-item active" : "nav-item"}
              onClick={() => {
                setPage(n.id);
                setSelected(null);
                setError("");
              }}
            >
              <n.icon size={18} />
              <span>{n.label}</span>
              {n.id === "projects" && canUse && (
                <small>{shownProjects.length}</small>
              )}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="private-note">
            <ShieldCheck size={19} />
            <div>
              <strong>Private by design</strong>
              <p>
                mTLS + signed requests
                <br />
                Credentials stay in Rust.
              </p>
            </div>
          </div>
          <button
            className={page === "security" ? "nav-item active" : "nav-item"}
            onClick={() => {
              setPage("security");
              setSelected(null);
            }}
          >
            <Settings2 size={18} />
            <span>Connection & security</span>
          </button>
          <div className="operator">
            <span className="avatar">
              {initials(session.profile?.actor_id ?? "mac owner")}
            </span>
            <div>
              <strong>{session.profile?.actor_id ?? "Mac operator"}</strong>
              <span>
                {session.unlocked
                  ? "Authenticated session"
                  : "Device authentication"}
              </span>
            </div>
            <button
              aria-label="Lock session"
              title="Lock session"
              onClick={() => {
                loseSession();
                setPreview(false);
                if (api.native) void api.lock();
              }}
            >
              <LockKeyhole size={16} />
            </button>
          </div>
        </div>
      </aside>
      <main className="main">
        <header className="topbar">
          <div className="breadcrumb">
            Workspace <ChevronRight size={13} />
            <strong>
              {project
                ? project.app_id
                : page === "projects"
                  ? "Projects"
                  : (nav.find((n) => n.id === page)?.label ?? "Security")}
            </strong>
            {project && (
              <>
                <ChevronRight size={13} />
                {labels[project.environment]}
              </>
            )}
          </div>
          <div className="topbar-right">
            <span className="connection">
              <i
                className={
                  session.unlocked && !preview ? "green-dot" : "gray-dot"
                }
              />
              {preview
                ? "Preview · disconnected"
                : session.unlocked
                  ? "Session unlocked"
                  : "Session locked"}
            </span>
            <span className="topbar-divider" />
            <span className="mac-label">macOS</span>
            <ShieldCheck size={16} />
          </div>
        </header>
        {preview && (
          <div className="preview-banner">
            <FileCode2 size={15} />
            <span>
              <strong>Design preview.</strong> All projects and activity below
              are sample data. No server is connected.
            </span>
            <button
              onClick={() => {
                setPreview(false);
                setSelected(null);
                setPage("security");
              }}
            >
              Connect your VPS <ArrowRight size={14} />
            </button>
          </div>
        )}
        {error && (
          <div className="alert error" role="alert">
            {error}
            <button aria-label="Dismiss error" onClick={() => setError("")}>
              <X size={15} />
            </button>
          </div>
        )}
        {notice && (
          <div className="toast" role="status">
            <CheckCheck size={17} />
            {notice}
          </div>
        )}
        {pending && !preview && canUse && (
          <div className="alert pending">
            <History size={17} />
            <span>
              <strong>
                Saved operation: {pending.action} · {pending.project}
              </strong>
              <br />
              {pending.job_id
                ? `Tracking ${pending.job_id}. Further writes wait for its result.`
                : "The outcome is uncertain. Retry uses the same request IDs and exact payload."}
            </span>
            <button
              className="button small"
              disabled={busy}
              onClick={() => void doRetry()}
            >
              {pending.job_id ? "Check result" : "Retry safely"}
            </button>
          </div>
        )}
        <div className="content">
          {page === "security" ? (
            <Security
              profile={session.profile}
              unlocked={session.unlocked && !preview}
              native={api.native}
              busy={authBusy}
              authenticate={authenticate}
              setup={() => setSetupOpen(true)}
              explore={() => setPreview(true)}
              forget={async () => {
                loseSession();
                setPreview(false);
                setAuthBusy(true);
                try {
                  await api.forget();
                  loseSession();
                  setSession({ unlocked: false, profile: null, jobs: [] });
                  setNotice(
                    "Local enrollment removed. Revoke its key on the VPS to remove server access.",
                  );
                } catch (e) {
                  report(e);
                } finally {
                  setAuthBusy(false);
                }
              }}
            />
          ) : !canUse ? (
            <Connect
              busy={authBusy}
              authenticate={authenticate}
              setup={() => setSetupOpen(true)}
              explore={() => setPreview(true)}
            />
          ) : project ? (
            <ProjectDetail
              key={project.id + String(preview)}
              project={project}
              preview={preview}
              open={setModal}
              action={simple}
              execute={perform}
              report={report}
            />
          ) : page === "projects" ? (
            <Projects
              projects={shownProjects}
              jobs={shownJobs}
              updated={updated}
              loading={loading}
              select={setSelected}
              create={() => setModal({ kind: "create" })}
              refresh={() => {
                if (!preview) void refresh();
                else setNotice("This preview uses sample data.");
              }}
            />
          ) : page === "deployments" ? (
            <Jobs
              jobs={shownJobs}
              preview={preview}
              refresh={refresh}
              report={report}
            />
          ) : page === "domains" ? (
            <Domains
              projects={shownProjects}
              inventory={shownInventory}
              select={(id) => {
                setPage("projects");
                setSelected(id);
              }}
              open={setModal}
            />
          ) : page === "inventory" ? (
            <InventoryView
              inventory={shownInventory}
              loading={loading}
              refresh={() => {
                if (!preview) void refresh();
              }}
            />
          ) : (
            <AuditView
              preview={preview}
              events={audit}
              setEvents={setAudit}
              report={report}
            />
          )}
        </div>
        <footer className="footer">
          <span>
            <span className="tiny-mark">D</span>DOCKYARD{" "}
            <span className="footer-dot">/</span> A quieter way to run your
            services.
          </span>
          <span>
            {preview
              ? "Read-only preview"
              : updated
                ? `Last read ${ago(updated)}`
                : "Awaiting private connection"}
            <span className="footer-dot">·</span>v0.1.0
          </span>
        </footer>
      </main>
      {modal && canUse && (
        <OperationModal
          modal={modal}
          projects={shownProjects}
          preview={preview}
          close={() => setModal(null)}
          perform={perform}
          report={report}
        />
      )}
      {setupOpen && (
        <SetupWizard
          close={closeSetup}
          ready={(s) => {
            epoch.current++;
            unlocked.current = true;
            setSession(s);
            setPreview(false);
            setSetupOpen(false);
            setPage("projects");
            setNotice(
              "Dockyard is running on your VPS. Your server IP and connection are saved in Keychain.",
            );
            void refresh(s.jobs);
          }}
        />
      )}
    </div>
  );
}
function Connect({
  busy,
  authenticate,
  setup,
  explore,
}: {
  busy: boolean;
  authenticate: (m: "enroll" | "unlock") => void;
  setup: () => void;
  explore: () => void;
}) {
  return (
    <div className="connect">
      <div className="connect-visual">
        <div className="orbit orbit-one" />
        <div className="orbit orbit-two" />
        <div className="connect-icon">
          <Box size={44} />
        </div>
        <span className="orbit-node node-one">
          <Server size={21} />
        </span>
        <span className="orbit-node node-two">
          <ShieldCheck size={21} />
        </span>
        <span className="orbit-node node-three">
          <Layers3 size={21} />
        </span>
      </div>
      <div className="eyebrow">YOUR PRIVATE CONTROL ROOM</div>
      <h1>
        Everything running.
        <br />
        <span>Everything in reach.</span>
      </h1>
      <p>
        One secure place for your VPS, Compose stacks,
        <br />
        deployments and domains. Built for your Mac.
      </p>
      <div className="connect-actions">
        <button className="button primary" disabled={busy} onClick={setup}>
          <Server size={17} />
          Set up Dockyard on your server
        </button>
      </div>
      <p className="connect-enrollment-hint">
        First time? Set up your Ubuntu VPS here. Credentials are generated
        automatically.
      </p>
      <div className="connect-actions">
        <button
          className="button"
          disabled={busy}
          onClick={() => authenticate("enroll")}
        >
          <ShieldCheck size={17} />
          {busy ? "Waiting for macOS…" : "Import existing enrollment"}
        </button>
        <button
          className="button"
          disabled={busy}
          onClick={() => authenticate("unlock")}
        >
          <Fingerprint size={16} />
          Unlock with Touch ID
        </button>
      </div>
      <button className="text-button" onClick={explore}>
        Explore the interface <ArrowRight size={14} />
      </button>
      <div className="trust-row">
        <span>
          <Check size={14} />
          Private HTTPS only
        </span>
        <span>
          <Check size={14} />
          macOS Keychain
        </span>
        <span>
          <Check size={14} />
          Touch ID required
        </span>
      </div>
    </div>
  );
}
function Projects({
  projects,
  jobs,
  updated,
  loading,
  select,
  create,
  refresh,
}: {
  projects: Project[];
  jobs: Job[];
  updated?: string;
  loading: boolean;
  select: (s: string) => void;
  create: () => void;
  refresh: () => void;
}) {
  const [filter, setFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const filtered = projects.filter(
    (p) =>
      (filter === "all" || p.environment === filter) &&
      `${p.app_id} ${p.id} ${p.domains.join(" ")}`
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  const maxPage = Math.max(0, Math.ceil(filtered.length / 20) - 1);
  const slice = filtered.slice(
    Math.min(page, maxPage) * 20,
    Math.min(page, maxPage) * 20 + 20,
  );
  const running = projects.filter((p) => p.state === "running").length;
  const activeJobs = jobs.filter((j) =>
    ["queued", "running", "recovery_required"].includes(j.status),
  ).length;
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">WORKSPACE OVERVIEW</div>
          <h1>
            Your services. <span>One control room.</span>
          </h1>
          <p>Deploy confidently. Keep every environment in view.</p>
        </div>
        <button className="button primary" onClick={create}>
          <Plus size={17} />
          New project
        </button>
      </div>
      <div className="stats-grid">
        <Stat
          label="MANAGED PROJECTS"
          value={projects.length}
          icon={Box}
          note={`${new Set(projects.map((p) => p.app_id)).size} applications across environments`}
        />
        <Stat
          label="RUNNING STATE"
          value={running}
          icon={Radio}
          note="Recorded state · inspect for live health"
        />
        <Stat
          label="BLUE–GREEN PROJECTS"
          value={projects.filter((p) => p.zerodowntime).length}
          icon={Layers3}
          note="Production with two deployment slots"
        />
        <Stat
          label="ASSIGNED DOMAINS"
          value={projects.reduce((n, p) => n + p.domains.length, 0)}
          icon={Globe2}
          note="Managed Caddy route assignments"
        />
      </div>
      <div className="workspace-grid">
        <div className="project-area">
          <div className="section-heading">
            <h2>
              Projects <span>{projects.length}</span>
            </h2>
            <button
              className="icon-button"
              aria-label="Refresh workspace"
              title="Refresh workspace"
              disabled={loading}
              onClick={refresh}
            >
              <RefreshCw size={16} className={loading ? "spin" : ""} />
            </button>
          </div>
          <div className="filters">
            <div className="segments">
              {["all", ...environments].map((e) => (
                <button
                  key={e}
                  className={filter === e ? "selected" : ""}
                  onClick={() => {
                    setFilter(e);
                    setPage(0);
                  }}
                >
                  {e === "all" ? "All environments" : labels[e as Environment]}
                </button>
              ))}
            </div>
            <label className="search">
              <Search size={15} />
              <input
                aria-label="Search projects"
                placeholder="Find a project…"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setPage(0);
                }}
              />
              <kbd>⌕</kbd>
            </label>
          </div>
          <div className="project-table">
            <div className="table-head">
              <span>PROJECT / ENVIRONMENT</span>
              <span>STATE</span>
              <span>DEPLOYMENT</span>
              <span>ROUTE</span>
              <span />
            </div>
            {slice.map((p) => (
              <button
                className="project-row"
                key={p.id}
                onClick={() => select(p.id)}
              >
                <span className="project-name">
                  <span
                    className={`app-icon ${p.app_id.charCodeAt(0) % 3 === 0 ? "lavender" : p.app_id.charCodeAt(0) % 3 === 1 ? "sand" : "mint"}`}
                  >
                    {initials(p.app_id)}
                  </span>
                  <span>
                    <strong>{p.app_id}</strong>
                    <span className="env-label">
                      <i className={`env-dot ${p.environment}`} />
                      {labels[p.environment]}
                    </span>
                  </span>
                </span>
                <span>
                  <Tag
                    tone={
                      p.state === "running"
                        ? "green"
                        : p.state === "stopped"
                          ? "neutral"
                          : "amber"
                    }
                  >
                    <i
                      className={
                        p.state === "running" ? "green-dot" : "gray-dot"
                      }
                    />
                    {p.state.replaceAll("_", " ")}
                  </Tag>
                </span>
                <span className="deployment-label">
                  {p.zerodowntime ? <Layers3 size={14} /> : <Box size={14} />}
                  <span>
                    {p.zerodowntime ? "Blue–green" : "Single instance"}
                    <small>
                      {p.active_slot
                        ? `${p.active_slot} slot`
                        : "No active slot"}
                    </small>
                  </span>
                </span>
                <span className="route-label">
                  <span>{p.domains[0] ?? "No domain"}</span>
                  <small>
                    :{p.active_slot === "green" ? p.green_port : p.blue_port}
                    {p.domains.length > 1
                      ? ` · +${p.domains.length - 1} more`
                      : ""}
                  </small>
                </span>
                <ChevronRight size={16} />
              </button>
            ))}
            {slice.length === 0 && (
              <Empty
                title={
                  projects.length
                    ? "No matching projects"
                    : "No managed projects yet"
                }
              >
                Create a project, select its environment, then deploy a Compose
                stack.
              </Empty>
            )}
          </div>
          <div className="table-footer">
            <span>
              {filtered.length} {filtered.length === 1 ? "project" : "projects"}
              {updated ? ` · Read ${ago(updated)}` : ""}
            </span>
            <div>
              <button
                aria-label="Previous page"
                disabled={page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                <ChevronLeft size={15} />
              </button>
              <span>
                {Math.min(page, maxPage) + 1} / {maxPage + 1}
              </span>
              <button
                aria-label="Next page"
                disabled={page >= maxPage}
                onClick={() => setPage((p) => p + 1)}
              >
                <ChevronRight size={15} />
              </button>
            </div>
          </div>
        </div>
        <div className="right-rail">
          <div className="rollout-card">
            <div className="eyebrow">
              <Layers3 size={14} />
              BUILT FOR CONTINUITY
            </div>
            <h2>
              A smooth switch.
              <br />A steady service.
            </h2>
            <p>
              Production deploys into the alternate slot. Traffic moves after
              readiness checks pass.
            </p>
            <div className="rollout-diagram">
              <div>
                <i className="blue-dot" />
                <span>BLUE</span>
                <small>Serving</small>
              </div>
              <ArrowRight size={19} />
              <div>
                <i className="green-dot" />
                <span>GREEN</span>
                <small>Candidate</small>
              </div>
            </div>
            <div className="rollout-foot">
              <ShieldCheck size={14} />
              Stateless production stacks
            </div>
          </div>
          <div className="rail-activity">
            <div className="section-heading">
              <h3>Recent operations</h3>
              <span className="count">{activeJobs} active</span>
            </div>
            {jobs.slice(0, 3).map((j) => (
              <div className="activity-item" key={j.job_id}>
                <span
                  className={`activity-icon ${j.status === "succeeded" ? "success" : ""}`}
                >
                  {j.status === "succeeded" ? (
                    <Check size={15} />
                  ) : (
                    <Activity size={15} />
                  )}
                </span>
                <div>
                  <strong>
                    {j.action[0].toUpperCase() + j.action.slice(1)} ·{" "}
                    {j.project_id}
                  </strong>
                  <p>
                    {j.status.replaceAll("_", " ")} · {ago(j.created_at)}
                  </p>
                </div>
              </div>
            ))}
            {jobs.length === 0 && (
              <p className="muted">
                Operations started from this Mac will appear here.
              </p>
            )}
          </div>
          <div className="rail-note">
            <FileCode2 size={18} />
            <div>
              <strong>Compose, from end to end.</strong>
              <p>
                Multi-service stacks. Server-side validation. One controlled
                deployment.
              </p>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
function Stat({
  label,
  value,
  icon: Icon,
  note,
}: {
  label: string;
  value: number;
  icon: typeof Box;
  note: string;
}) {
  return (
    <div className="stat">
      <div>
        <span>{label}</span>
        <Icon size={17} />
      </div>
      <strong>{String(value).padStart(2, "0")}</strong>
      <p>{note}</p>
    </div>
  );
}
function ProjectDetail({
  project: p,
  preview,
  open,
  action,
  execute,
  report,
}: {
  project: Project;
  preview: boolean;
  open: (m: Modal) => void;
  action: (a: "start" | "restart", p: Project) => void;
  execute: (m: unknown) => Promise<void>;
  report: (e: unknown) => void;
}) {
  const [tab, setTab] = useState("overview");
  const [services, setServices] = useState<Service[]>([]);
  const [status, setStatus] = useState<{
    health: string;
    route: string;
    busy: boolean;
    public_tls_state: string;
  }>();
  const [logs, setLogs] = useState("");
  const [truncated, setTruncated] = useState(false);
  const [service, setService] = useState("app");
  const [slot, setSlot] = useState("active");
  const [since, setSince] = useState("30m");
  const [busy, setBusy] = useState(false);
  const [dns, setDNS] = useState<
    { hostname: string; assigned: boolean; dns_record_id?: string }[]
  >([]);
  useEffect(() => {
    let cancelled = false;
    if (preview) {
      setServices([
        {
          name: "app",
          slot: p.active_slot ?? "blue",
          image: p.releases[0].image,
          template_id: p.template_id,
          environment_revision: "env-preview",
        },
        {
          name: "worker",
          slot: p.active_slot ?? "blue",
          image: p.releases[0].image,
          template_id: "worker-node",
          environment_revision: "env-preview",
        },
      ]);
      setDNS(p.domains.map((hostname) => ({ hostname, assigned: true })));
      return;
    }
    void api
      .read<{ services: Service[] }>({
        kind: "project",
        project: p.id,
        view: "services",
      })
      .then((d) => {
        if (!cancelled) setServices(d.services ?? []);
      })
      .catch((e) => {
        if (!cancelled) report(e);
      });
    void api
      .read<
        | { hostname: string; assigned: boolean; dns_record_id?: string }[]
        | {
            domains: {
              hostname: string;
              assigned: boolean;
              dns_record_id?: string;
            }[];
          }
      >({ kind: "project", project: p.id, view: "domains" })
      .then((d) => {
        if (!cancelled) setDNS(Array.isArray(d) ? d : (d.domains ?? []));
      })
      .catch((e) => {
        if (!cancelled) report(e);
      });
    return () => {
      cancelled = true;
    };
  }, [p.id, p.releases.at(-1)?.id, p.domains.join(), preview, report]);
  async function check() {
    setBusy(true);
    try {
      if (preview) {
        setStatus({
          health: "preview",
          route: "preview",
          busy: false,
          public_tls_state: "unverified",
        });
        return;
      }
      setStatus(
        await api.read({ kind: "project", project: p.id, view: "status" }),
      );
    } catch (e) {
      report(e);
    } finally {
      setBusy(false);
    }
  }
  async function loadLogs() {
    setBusy(true);
    try {
      if (preview) {
        setLogs(
          "[Sample logs · no server connected]\napp  | Listening on 0.0.0.0:3000\napp  | Readiness check passed\napp  | GET /health/ready 200\n",
        );
        return;
      }
      const data = await api.read<{ logs: string; truncated: boolean }>({
        kind: "logs",
        project: p.id,
        service,
        slot,
        tail: 200,
        since,
      });
      setLogs(data.logs);
      setTruncated(data.truncated);
    } catch (e) {
      report(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">{p.id}</div>
          <h1>
            {p.app_id}{" "}
            <Tag tone={p.environment === "production" ? "green" : "neutral"}>
              {labels[p.environment]}
            </Tag>
          </h1>
          <p>
            {p.domains.join(" · ")} <span className="mono">/{p.id}</span>
          </p>
        </div>
        <button
          className="button primary"
          onClick={() => open({ kind: "deploy", project: p })}
        >
          <ArrowUpRight size={17} />
          Deploy Compose
        </button>
      </div>
      <div className="detail-meta">
        <Tag tone={p.state === "running" ? "green" : "neutral"}>{p.state}</Tag>
        <span>
          <Layers3 size={15} />
          {p.zerodowntime ? "Production blue–green" : "Single Compose instance"}
        </span>
        <span>
          <FileCode2 size={15} />
          {p.template_id}
        </span>
        <span>
          <Globe2 size={15} />
          {p.domains.length} domains
        </span>
      </div>
      <div className="tabs">
        {["overview", "services", "releases", "logs", "domains"].map((t) => (
          <button
            key={t}
            className={tab === t ? "selected" : ""}
            onClick={() => setTab(t)}
          >
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>
      {tab === "overview" && (
        <div className="detail-grid">
          <section className="panel">
            <div className="section-heading">
              <h2>Deployment slots</h2>
              <Tag>
                {p.zerodowntime
                  ? "Traffic switch after readiness"
                  : "Brief maintenance on deploy"}
              </Tag>
            </div>
            <div className="slot-grid">
              {(p.zerodowntime ? ["blue", "green"] : ["blue"]).map((s) => (
                <div
                  key={s}
                  className={`slot-card ${s} ${p.active_slot === s ? "serving" : ""}`}
                >
                  <div>
                    <span className="eyebrow">{s.toUpperCase()} SLOT</span>
                    <Tag tone={p.active_slot === s ? "green" : "neutral"}>
                      {p.active_slot === s ? "Active" : "Alternate"}
                    </Tag>
                  </div>
                  <Server size={28} />
                  <h3>127.0.0.1:{s === "blue" ? p.blue_port : p.green_port}</h3>
                  <p>{p.slots[s]?.id ?? "No release recorded"}</p>
                  <small className="mono">
                    {p.slots[s]?.image.split("@")[1]?.slice(0, 22) ??
                      "Awaiting Compose deployment"}
                    …
                  </small>
                </div>
              ))}
            </div>
            <p className="muted">
              Slots show recorded metadata. Check live status to verify
              container health and the Caddy route.
            </p>
          </section>
          <section className="panel">
            <div className="section-heading">
              <h2>Live verification</h2>
              <button
                className="button small"
                disabled={busy}
                onClick={() => void check()}
              >
                <RefreshCw size={14} />
                Check now
              </button>
            </div>
            <dl className="facts">
              <dt>Container health</dt>
              <dd>{status?.health ?? "Not checked"}</dd>
              <dt>Caddy route</dt>
              <dd>{status?.route ?? "Not checked"}</dd>
              <dt>Job in progress</dt>
              <dd>{status ? String(status.busy) : "Not checked"}</dd>
              <dt>Public HTTPS</dt>
              <dd>{status?.public_tls_state ?? "Unverified"}</dd>
            </dl>
            <hr />
            <h3>Service controls</h3>
            <div className="controls">
              <button className="button" onClick={() => action("start", p)}>
                <Zap size={14} />
                Start
              </button>
              <button className="button" onClick={() => action("restart", p)}>
                <RefreshCw size={14} />
                Restart
              </button>
              <button
                className="button danger"
                onClick={() => open({ kind: "stop", project: p })}
              >
                Stop services
              </button>
            </div>
            <p className="muted">
              Stopping preserves Compose files, releases and data volumes.
            </p>
          </section>
        </div>
      )}
      {tab === "services" && (
        <section className="panel">
          <div className="section-heading">
            <h2>Multi-service stack</h2>
            <Tag>{services.length} recorded services</Tag>
          </div>
          {services.map((s) => (
            <div className="service-row" key={s.slot + s.name}>
              <Box size={22} />
              <div>
                <strong>{s.name}</strong>
                <p className="mono">{s.image}</p>
              </div>
              <Tag>{s.slot}</Tag>
              <Tag>{s.template_id}</Tag>
            </div>
          ))}
          {!services.length && (
            <Empty title="No services recorded">
              Deploy your first validated Compose stack.
            </Empty>
          )}
        </section>
      )}
      {tab === "releases" && (
        <section className="panel">
          <div className="section-heading">
            <h2>Release history</h2>
            <span className="muted">
              Rollback restores containers; database data is preserved.
            </span>
          </div>
          {[...p.releases].reverse().map((r, i) => (
            <div className="release-row" key={r.id}>
              <span className="timeline-dot">
                <Check size={14} />
              </span>
              <div>
                <strong>{r.id}</strong>
                <p>{ago(r.created_at)}</p>
                <code>{r.image}</code>
              </div>
              <Tag>{r.compose_revision ? "Compose" : "Legacy"}</Tag>
              <button
                className="button small"
                onClick={() =>
                  open({ kind: "rollback", project: { ...p, releases: [r] } })
                }
              >
                Rollback <ArrowDownLeft size={14} />
              </button>
            </div>
          ))}
          {!p.releases.length && (
            <Empty title="No releases yet">
              Deploy a Compose stack to create a release.
            </Empty>
          )}
        </section>
      )}
      {tab === "logs" && (
        <section className="panel logs-panel">
          <div className="section-heading">
            <h2>
              <Terminal size={18} />
              Container logs
            </h2>
            <Tag>Bounded · 200 lines</Tag>
          </div>
          <div className="log-filters">
            <label>
              Service
              <select
                value={service}
                onChange={(e) => setService(e.target.value)}
              >
                {Array.from(
                  new Set(["app", ...services.map((s) => s.name)]),
                ).map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <label>
              Slot
              <select value={slot} onChange={(e) => setSlot(e.target.value)}>
                {[
                  "active",
                  ...(p.zerodowntime
                    ? ["inactive", "blue", "green"]
                    : ["blue"]),
                ].map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <label>
              Since
              <select value={since} onChange={(e) => setSince(e.target.value)}>
                {["5m", "30m", "1h", "24h"].map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <button
              className="button"
              disabled={busy}
              onClick={() => void loadLogs()}
            >
              <RefreshCw size={14} />
              Read logs
            </button>
          </div>
          <pre className="terminal">
            {logs ||
              "Select a service and read its logs. No terminal or shell access is exposed."}
          </pre>
          {truncated && (
            <p className="warning">Output was truncated by the server.</p>
          )}
          <p className="muted">
            The server redacts retained environment values. Avoid logging
            secrets in your applications.
          </p>
        </section>
      )}
      {tab === "domains" && (
        <section className="panel">
          <div className="section-heading">
            <h2>Domains & Caddy</h2>
            <button
              className="button small"
              onClick={() => open({ kind: "routes", project: p })}
            >
              Edit routes <Settings2 size={14} />
            </button>
          </div>
          {dns.map((d) => (
            <div className="domain-row" key={d.hostname}>
              <Globe2 size={20} />
              <div>
                <strong>{d.hostname}</strong>
                <p>
                  {d.dns_record_id
                    ? "Cloudflare record tracked"
                    : "DNS record not tracked"}{" "}
                  · Public TLS unverified
                </p>
              </div>
              <Tag tone="green">{d.assigned ? "Assigned" : "Unassigned"}</Tag>
              <button
                className="button small"
                disabled={Boolean(d.dns_record_id)}
                onClick={() => {
                  if (preview) {
                    report("Preview is read-only.");
                    return;
                  }
                  void execute({
                    action: "dns",
                    project: p.id,
                    hostname: d.hostname,
                  }).catch(report);
                }}
              >
                <Cloud size={14} />
                Create DNS
              </button>
            </div>
          ))}
          <p className="muted">
            Managed Caddy snippets are updated by the agent. Manual Caddyfile
            sites remain in VPS inventory.
          </p>
        </section>
      )}
    </>
  );
}
function Jobs({
  jobs,
  preview,
  refresh,
  report,
}: {
  jobs: Job[];
  preview: boolean;
  refresh: () => Promise<void>;
  report: (e: unknown) => void;
}) {
  const [lookup, setLookup] = useState("");
  const [found, setFound] = useState<Job | null>(null);
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">OPERATIONS</div>
          <h1>
            Every deployment, <span>in view.</span>
          </h1>
          <p>
            The latest ten jobs tracked by this Mac. Look up any other job by
            its ID.
          </p>
        </div>
        <button
          className="button"
          onClick={() => {
            if (!preview) void refresh();
          }}
        >
          <RefreshCw size={15} />
          Refresh
        </button>
      </div>
      <form
        className="job-search"
        onSubmit={async (e) => {
          e.preventDefault();
          if (preview) return;
          try {
            setFound(await api.read<Job>({ kind: "job", job: lookup }));
          } catch (e) {
            report(e);
          }
        }}
      >
        <Search size={16} />
        <input
          aria-label="Job ID"
          placeholder="Look up job ID…"
          value={lookup}
          onChange={(e) => setLookup(e.target.value)}
        />
        <button className="button small" disabled={preview}>
          Look up
        </button>
      </form>
      <section className="panel">
        {[
          ...(found ? [found] : []),
          ...jobs.filter((j) => j.job_id !== found?.job_id),
        ].map((j) => (
          <div className="job-row" key={j.job_id}>
            <span className={`job-icon ${j.status}`}>
              <Layers3 size={20} />
            </span>
            <div>
              <strong>
                {j.action[0].toUpperCase() + j.action.slice(1)}{" "}
                <span className="muted">/ {j.project_id}</span>
              </strong>
              <p className="mono">{j.job_id}</p>
              <p>
                {j.phase}
                {j.error_code ? ` · ${j.error_code}` : ""}
                {j.warning_code ? ` · ${j.warning_code}` : ""}
              </p>
            </div>
            <div>
              <Tag
                tone={
                  j.status === "succeeded"
                    ? "green"
                    : j.status === "failed" || j.status === "recovery_required"
                      ? "red"
                      : "amber"
                }
              >
                {j.status.replaceAll("_", " ")}
              </Tag>
              <p>{ago(j.created_at)}</p>
              <p className="muted">{j.actor_id}</p>
            </div>
          </div>
        ))}
        {!jobs.length && !found && (
          <Empty icon={Layers3} title="No tracked operations">
            New operations are saved in Keychain and remain available after
            restarting this Mac app.
          </Empty>
        )}
      </section>
      <div className="note">
        <ShieldCheck size={18} />
        <p>
          Recovery-required jobs block further writes on the VPS. Inspect and
          reconcile locally using the documented server recovery procedure.
        </p>
      </div>
    </>
  );
}
function Domains({
  projects,
  inventory,
  select,
  open,
}: {
  projects: Project[];
  inventory: Inventory | null;
  select: (s: string) => void;
  open: (m: Modal) => void;
}) {
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">NETWORKING</div>
          <h1>
            A home for <span>every service.</span>
          </h1>
          <p>
            Managed domain assignments, upstream ports and existing Caddy sites.
          </p>
        </div>
        <Tag>
          <Globe2 size={14} />
          {projects.reduce((n, p) => n + p.domains.length, 0)} managed domains
        </Tag>
      </div>
      <section className="panel">
        <h2>Managed routes</h2>
        {projects.flatMap((p) =>
          p.domains.map((d) => (
            <div className="domain-row" key={d}>
              <Globe2 size={20} />
              <div>
                <button className="text-button" onClick={() => select(p.id)}>
                  {d} <ArrowUpRight size={13} />
                </button>
                <p>
                  {p.id} · 127.0.0.1:
                  {p.active_slot === "green" ? p.green_port : p.blue_port}
                </p>
              </div>
              <Tag>{labels[p.environment]}</Tag>
              <button
                className="button small"
                onClick={() => open({ kind: "routes", project: p })}
              >
                Edit route
              </button>
            </div>
          )),
        )}
        {!projects.length && <Empty title="No managed domains" />}
      </section>
      <section className="panel">
        <h2>Observed Caddy sites</h2>
        <p className="muted">
          SQLite inventory · last observed {ago(inventory?.caddy_observed_at)} ·
          read-only
        </p>
        {inventory?.sites.map((s, i) => (
          <div className="domain-row" key={i}>
            <FileCode2 size={20} />
            <div>
              <strong>{s.host_matcher}</strong>
              <p className="mono">
                {s.upstreams.join(", ") || "No reverse-proxy upstream"}
              </p>
            </div>
            <Tag>Observed</Tag>
          </div>
        ))}
        {!inventory?.sites.length && <Empty title="No observed sites" />}
      </section>
    </>
  );
}
function InventoryView({
  inventory: i,
  loading,
  refresh,
}: {
  inventory: Inventory | null;
  loading: boolean;
  refresh: () => void;
}) {
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">VPS INVENTORY</div>
          <h1>
            Already running? <span>Already visible.</span>
          </h1>
          <p>
            Existing /docker projects and Caddy sites, synced to SQLite as
            production inventory.
          </p>
        </div>
        <button className="button" disabled={loading} onClick={refresh}>
          <RefreshCw size={15} />
          Read inventory
        </button>
      </div>
      <div className="stats-grid three">
        <Stat
          label="OBSERVED PROJECTS"
          value={i?.projects.length ?? 0}
          icon={FolderGit2}
          note={`Last observed ${ago(i?.projects_observed_at)}`}
        />
        <Stat
          label="CADDY SITES"
          value={i?.sites.length ?? 0}
          icon={Globe2}
          note={`Last observed ${ago(i?.caddy_observed_at)}`}
        />
        <Stat
          label="RESERVED PORTS"
          value={i?.reserved_ports.length ?? 0}
          icon={Server}
          note={`Docker observed ${ago(i?.docker_observed_at)}`}
        />
      </div>
      {i?.warnings.map((w) => (
        <div className="alert pending" key={w}>
          {w}
        </div>
      ))}
      <section className="panel">
        {i?.projects.map((p) => (
          <div className="inventory-row" key={p.id}>
            <span className="app-icon sand">
              <FolderGit2 size={23} />
            </span>
            <div>
              <strong>/docker/{p.name}</strong>
              <p>
                {p.compose_files.join(" · ")} · {p.services.length} services
              </p>
              <div className="chips">
                {p.services.map((s) => (
                  <Tag key={s.name}>
                    {s.name}
                    {s.published_ports.length
                      ? ` :${s.published_ports.join(", :")}`
                      : ""}
                  </Tag>
                ))}
              </div>
              {p.warnings.map((w) => (
                <p key={w} className="warning">
                  {w}
                </p>
              ))}
            </div>
            <div>
              <Tag tone={p.present ? "green" : "red"}>
                {p.present ? "Present" : "Missing directory"}
              </Tag>
              <p>{p.environment}</p>
              <Tag>{p.managed ? "Managed" : "Observed only"}</Tag>
            </div>
          </div>
        ))}
        {!i?.projects.length && (
          <Empty icon={Database} title="No inventory available">
            The agent refreshes existing directories and sites every five
            minutes. Reading this view uses SQLite.
          </Empty>
        )}
      </section>
      <div className="note">
        <LockKeyhole size={18} />
        <p>
          Observed projects are preserved as-is. They require explicit migration
          before deployment or lifecycle controls become available. Credentials
          and .env values are excluded from inventory.
        </p>
      </div>
    </>
  );
}
function AuditView({
  preview,
  events,
  setEvents,
  report,
}: {
  preview: boolean;
  events: Audit[];
  setEvents: (s: Audit[]) => void;
  report: (e: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [more, setMore] = useState(true);
  async function load(reset = false) {
    if (preview) return;
    setBusy(true);
    try {
      const after = reset ? 0 : (events.at(-1)?.event_id ?? 0);
      const d = await api.read<{ events: Audit[] }>({ kind: "audit", after });
      setEvents(reset ? d.events : [...events, ...d.events]);
      setMore(d.events.length === 100);
    } catch (e) {
      report(e);
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    if (!events.length) void load(true);
  }, []);
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">ACCOUNTABILITY</div>
          <h1>
            A clear record <span>of every action.</span>
          </h1>
          <p>
            Server audit events with attributable actors and request IDs.
            All-project read scope required.
          </p>
        </div>
        <button
          className="button"
          disabled={preview || busy}
          onClick={() => void load(true)}
        >
          <RefreshCw size={15} />
          Read from start
        </button>
      </div>
      <section className="panel">
        {events.map((e) => (
          <div className="audit-event" key={e.event_id}>
            <Tag>#{e.event_id}</Tag>
            <pre>{JSON.stringify(e.event, null, 2)}</pre>
          </div>
        ))}
        {events.length === 0 && (
          <Empty
            icon={History}
            title={
              preview
                ? "Audit events appear after connecting"
                : "No audit events loaded"
            }
          >
            The desktop keeps no local plaintext audit cache.
          </Empty>
        )}
        {events.length > 0 && more && (
          <button
            className="button"
            disabled={busy}
            onClick={() => void load()}
          >
            Load next 100
          </button>
        )}
      </section>
    </>
  );
}
function Security({
  profile: p,
  unlocked,
  native,
  busy,
  authenticate,
  setup,
  explore,
  forget,
}: {
  profile: Profile | null;
  unlocked: boolean;
  native: boolean;
  busy: boolean;
  authenticate: (m: "enroll" | "unlock") => void;
  setup: () => void;
  explore: () => void;
  forget: () => void;
}) {
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">CONNECTION & SECURITY</div>
          <h1>
            Your Mac. <span>Your control plane.</span>
          </h1>
          <p>A dedicated client identity for your private VPS API.</p>
        </div>
        <Tag tone={unlocked ? "green" : "neutral"}>
          <LockKeyhole size={14} />
          {unlocked ? "Unlocked" : "Locked"}
        </Tag>
      </div>
      <div className="security-grid">
        <section className="panel enrollment-panel">
          <div className="security-symbol">
            <ShieldCheck size={32} />
          </div>
          <h2>{p?.name ?? "Connect your VPS"}</h2>
          <p>
            Set up a new Ubuntu VPS here, or import an enrollment from an
            already configured server. Credentials go straight to Keychain.
          </p>
          {p && (
            <dl className="facts">
              <dt>Origin</dt>
              <dd className="mono">{p.origin}</dd>
              <dt>Server ID</dt>
              <dd>{p.server_id}</dd>
              <dt>Credential ID</dt>
              <dd>{p.key_id}</dd>
              <dt>Actor</dt>
              <dd>{p.actor_id}</dd>
              <dt>Server certificate pin</dt>
              <dd className="mono">{p.server_certificate_sha256}</dd>
              {p.server_ip && (
                <>
                  <dt>Saved server IP</dt>
                  <dd className="mono">{p.server_ip}</dd>
                  <dt>SSH port</dt>
                  <dd>{p.ssh_port}</dd>
                  <dt>SSH fingerprint</dt>
                  <dd className="mono">{p.ssh_fingerprint}</dd>
                </>
              )}
            </dl>
          )}
          <div className="controls">
            {!p && (
              <button
                className="button primary"
                disabled={busy}
                onClick={setup}
              >
                <Server size={15} />
                Set up your server
              </button>
            )}
            <button
              className="button primary"
              disabled={!native || busy}
              onClick={() => authenticate("enroll")}
            >
              <Plus size={15} />
              Import existing enrollment
            </button>
            <button
              className="button"
              disabled={!native || busy}
              onClick={() => authenticate("unlock")}
            >
              <Fingerprint size={15} />
              Unlock with Touch ID
            </button>
          </div>
          {p && (
            <button
              className="text-button danger-text"
              disabled={busy}
              onClick={forget}
            >
              Remove local enrollment
            </button>
          )}
          <p className="muted">
            {native
              ? "Touch ID is required to enroll and unlock. Set up a fingerprint in System Settings → Touch ID & Password."
              : "This browser is a read-only design preview. Enrollment requires the native macOS app."}
          </p>
          <button className="text-button" onClick={explore}>
            Explore sample workspace <ArrowRight size={14} />
          </button>
        </section>
        <section className="panel security-list">
          <h2>Layered access controls</h2>
          {[
            {
              icon: ShieldCheck,
              title: "TLS 1.3 + dedicated client certificate",
              text: "The server checks the client CA and certificate fingerprint. Rust verifies the enrolled CA, SAN, expiry and exact server certificate pin.",
            },
            {
              icon: Code2,
              title: "Every API request is signed",
              text: "HMAC includes the server, actor, scope, exact route and body hash. The server enforces scopes and project permissions.",
            },
            {
              icon: LockKeyhole,
              title: "Credentials stay outside the webview",
              text: "Keychain storage, native authentication, five-minute sessions, and auto-lock after one minute away from the app.",
            },
            {
              icon: History,
              title: "Safe recovery after uncertain writes",
              text: "Exact payloads and retry IDs are saved in Keychain before submission. A pending write blocks another write.",
            },
            {
              icon: Server,
              title: "Private network, narrow commands",
              text: "Private IP or SSH tunnel only. No webview network, host shell, arbitrary file access, or generic API proxy.",
            },
          ].map((s) => (
            <div className="security-item" key={s.title}>
              <s.icon size={20} />
              <div>
                <h3>{s.title}</h3>
                <p>{s.text}</p>
              </div>
              <Check size={15} />
            </div>
          ))}
        </section>
      </div>
      <div className="note">
        <ShieldCheck size={19} />
        <p>
          Restrict the VPS to this Mac’s dedicated certificate and key, and
          revoke other client keys. A server authenticates credentials, not the
          app’s pixels: copied credentials or a compromised Mac can impersonate
          a client. Use a signed, notarized build for production.
        </p>
      </div>
    </>
  );
}
function OperationModal({
  modal: m,
  projects,
  preview,
  close,
  perform,
  report,
}: {
  modal: NonNullable<Modal>;
  projects: Project[];
  preview: boolean;
  close: () => void;
  perform: (m: unknown) => Promise<void>;
  report: (e: unknown) => void;
}) {
  const p = m.kind === "create" ? undefined : m.project;
  const [step, setStep] = useState(
    m.kind === "create" || m.kind === "deploy" ? 0 : 1,
  );
  const [env, setEnv] = useState<Environment | "">("");
  const [projectID, setProjectID] = useState(p?.id ?? "");
  const [appID, setAppID] = useState("");
  const [template, setTemplate] = useState("");
  const [domains, setDomains] = useState(p?.domains.join("\n") ?? "");
  const [blue, setBlue] = useState("");
  const [green, setGreen] = useState("");
  const [zero, setZero] = useState(true);
  const [compose, setCompose] = useState("");
  const [variables, setVariables] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [release, setRelease] = useState(p?.releases.at(-1)?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [suggestion, setSuggestion] = useState<number>();
  const name =
    m.kind === "create"
      ? "New project"
      : m.kind === "deploy"
        ? "Deploy Compose"
        : m.kind === "routes"
          ? "Edit Caddy routes"
          : m.kind === "stop"
            ? "Stop services"
            : "Rollback release";
  const target =
    m.kind === "deploy"
      ? projects.find((x) => x.app_id === p?.app_id && x.environment === env)
      : p;
  function choose(e: Environment) {
    setEnv(e);
    if (m.kind === "create") setProjectID(appID ? `${appID}-${e}` : "");
    if (m.kind === "deploy") {
      const t = projects.find(
        (x) => x.app_id === p?.app_id && x.environment === e,
      );
      setProjectID(t?.id ?? "");
    }
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (preview) {
      setError("Preview is read-only. Connect your VPS to submit operations.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      let mutation: unknown;
      if (m.kind === "create")
        mutation = {
          action: "create",
          data: {
            id: projectID,
            app_id: appID,
            environment: env,
            template_id: template,
            domains: domains
              .split(/[\n,]+/)
              .map((s) => s.trim())
              .filter(Boolean),
            zerodowntime: env === "production" && zero,
            ...(blue ? { port: Number(blue) } : {}),
            ...(env === "production" && zero && green
              ? { secondary_port: Number(green) }
              : {}),
          },
        };
      else if (m.kind === "deploy") {
        if (!target)
          throw new Error(
            "Create this application environment before deploying.",
          );
        let vars: unknown;
        if (variables.trim()) {
          vars = JSON.parse(variables);
          if (
            !vars ||
            Array.isArray(vars) ||
            typeof vars !== "object" ||
            Object.values(vars).some((v) => typeof v !== "string")
          )
            throw new Error(
              "Environment variables must be a JSON object of string values.",
            );
        }
        mutation = {
          action: "deploy",
          project: target.id,
          data: {
            environment: env,
            compose_yaml: compose,
            ...(vars !== undefined ? { variables: vars } : {}),
          },
        };
      } else if (m.kind === "routes")
        mutation = {
          action: "routes",
          project: p!.id,
          data: {
            domains: domains
              .split(/[\n,]+/)
              .map((s) => s.trim())
              .filter(Boolean),
            ...(blue ? { port: Number(blue) } : {}),
            ...(green ? { secondary_port: Number(green) } : {}),
          },
        };
      else if (m.kind === "stop")
        mutation = { action: "stop", project: p!.id, confirmation };
      else
        mutation = {
          action: "rollback",
          project: p!.id,
          release_id: release || null,
        };
      await perform(mutation);
      setVariables("");
      setCompose("");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    function key(e: KeyboardEvent) {
      if (e.key === "Escape" && !busy) close();
    }
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [busy, close]);
  return (
    <div className="modal-backdrop">
      <section
        className={`modal ${m.kind === "deploy" ? "wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
      >
        <div className="modal-heading">
          <div>
            <div className="eyebrow">
              {m.kind === "create"
                ? "PROVISION A WORKSPACE"
                : p?.app_id.toUpperCase()}
            </div>
            <h2 id="modal-title">{name}</h2>
          </div>
          <button
            className="icon-button"
            aria-label="Close dialog"
            disabled={busy}
            onClick={close}
          >
            <X size={20} />
          </button>
        </div>
        {(m.kind === "create" || m.kind === "deploy") && (
          <div className="wizard-steps">
            <span className={step === 0 ? "current" : "done"}>
              <i>{step > 0 ? <Check size={12} /> : 1}</i>Environment
            </span>
            <span className={step === 1 ? "current" : ""}>
              <i>2</i>
              {m.kind === "create" ? "Configuration" : "Compose & review"}
            </span>
          </div>
        )}
        {step === 0 ? (
          <div className="wizard-body">
            <h3>Where are we deploying?</h3>
            <p>
              One app, three isolated environments. Production can protect
              serving traffic with blue–green slots.
            </p>
            <div className="environment-options">
              {environments.map((e) => (
                <button
                  key={e}
                  className={env === e ? "chosen" : ""}
                  onClick={() => choose(e)}
                >
                  <span className={`env-option-icon ${e}`}>
                    <Layers3 size={21} />
                  </span>
                  <span>
                    <strong>{labels[e]}</strong>
                    <small>
                      {e === "production"
                        ? "Blue–green available · stateless stacks"
                        : e === "staging"
                          ? "Single instance · pre-release validation"
                          : "Single instance · development builds"}
                    </small>
                    {m.kind === "deploy" && (
                      <small>
                        {projects.find(
                          (x) => x.app_id === p?.app_id && x.environment === e,
                        )?.id ?? "Create this environment first"}
                      </small>
                    )}
                  </span>
                  <span className="radio-choice">{env === e && <i />}</span>
                </button>
              ))}
            </div>
            <div className="modal-footer">
              <span className="muted">Environment selection is required.</span>
              <button
                className="button primary"
                disabled={!env || (m.kind === "deploy" && !target)}
                onClick={() => setStep(1)}
              >
                Continue <ArrowRight size={15} />
              </button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit}>
            <div className="wizard-body">
              {error && (
                <div className="alert error" role="alert">
                  {error}
                </div>
              )}
              {m.kind === "create" && (
                <>
                  <div className="form-grid">
                    <label>
                      Application ID
                      <input
                        required
                        pattern="[a-z][a-z0-9-]{0,47}"
                        placeholder="payments"
                        value={appID}
                        onChange={(e) => {
                          setAppID(e.target.value);
                          setProjectID(`${e.target.value}-${env}`);
                        }}
                      />
                    </label>
                    <label>
                      Project ID
                      <input
                        required
                        pattern="[a-z][a-z0-9-]{0,47}"
                        placeholder="payments-production"
                        value={projectID}
                        onChange={(e) => setProjectID(e.target.value)}
                      />
                    </label>
                  </div>
                  <label>
                    Approved server template
                    <input
                      required
                      placeholder="web-node"
                      value={template}
                      onChange={(e) => setTemplate(e.target.value)}
                    />
                    <small>
                      Use a template configured by the VPS administrator.
                    </small>
                  </label>
                </>
              )}
              {(m.kind === "create" || m.kind === "routes") && (
                <>
                  <label>
                    Domains
                    <textarea
                      required
                      rows={3}
                      placeholder="app.example.com"
                      value={domains}
                      onChange={(e) => setDomains(e.target.value)}
                    />
                    <small>
                      One domain per line. All must be permitted by the server
                      policy.
                    </small>
                  </label>
                  {env === "production" && m.kind === "create" && (
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={zero}
                        onChange={(e) => setZero(e.target.checked)}
                      />
                      <span>
                        <strong>Use production blue–green deployment</strong>
                        <small>
                          Two stateless Compose stacks. Persistent volumes
                          require a single instance.
                        </small>
                      </span>
                    </label>
                  )}
                  <div className="form-grid">
                    <label>
                      Primary port <small>(optional)</small>
                      <input
                        type="number"
                        min={1024}
                        max={65535}
                        placeholder={
                          p ? String(p.blue_port) : "Allocate automatically"
                        }
                        value={blue}
                        onChange={(e) => setBlue(e.target.value)}
                      />
                    </label>
                    {((m.kind === "routes" && p?.zerodowntime) ||
                      (m.kind === "create" &&
                        env === "production" &&
                        zero)) && (
                      <label>
                        Secondary port <small>(optional)</small>
                        <input
                          type="number"
                          min={1024}
                          max={65535}
                          placeholder={
                            p ? String(p.green_port) : "Allocate automatically"
                          }
                          value={green}
                          onChange={(e) => setGreen(e.target.value)}
                        />
                      </label>
                    )}
                  </div>
                  <button
                    type="button"
                    className="text-button"
                    disabled={preview || busy}
                    onClick={async () => {
                      try {
                        const d = await api.read<{ port: number }>({
                          kind: "port",
                        });
                        setSuggestion(d.port);
                      } catch (e) {
                        setError(String(e));
                      }
                    }}
                  >
                    <Search size={14} />
                    Check next available port
                  </button>
                  {suggestion && (
                    <p className="muted">
                      Next available: {suggestion}. Advisory only; the agent
                      reserves ports when accepting the job.
                    </p>
                  )}
                </>
              )}
              {m.kind === "deploy" && (
                <>
                  <div className="deploy-summary">
                    <div>
                      <Tag tone={env === "production" ? "green" : "neutral"}>
                        {env && labels[env]}
                      </Tag>
                      <strong>{target?.id}</strong>
                    </div>
                    <span>
                      {target?.zerodowntime
                        ? "Alternate slot → health checks → traffic switch"
                        : "Single instance · maintenance expected"}
                    </span>
                  </div>
                  <div className="section-heading">
                    <label htmlFor="compose">Docker Compose YAML</label>
                    <button
                      type="button"
                      className="button small"
                      disabled={preview || busy}
                      onClick={async () => {
                        try {
                          const yaml = await api.importCompose();
                          if (yaml) setCompose(yaml);
                        } catch (e) {
                          setError(String(e));
                        }
                      }}
                    >
                      <Upload size={14} />
                      Import file
                    </button>
                  </div>
                  <textarea
                    id="compose"
                    className="code-editor"
                    required
                    spellCheck={false}
                    rows={12}
                    placeholder={
                      "services:\n  app:\n    image: ghcr.io/your-org/your-app@sha256:<approved-digest>\n  worker:\n    x-dockyard-template: worker-node\n    image: ghcr.io/your-org/worker@sha256:<approved-digest>\n"
                    }
                    value={compose}
                    onChange={(e) => setCompose(e.target.value)}
                  />
                  <div className="editor-meta">
                    <span>
                      {new TextEncoder()
                        .encode(compose)
                        .length.toLocaleString()}{" "}
                      / 65,536 bytes
                    </span>
                    <span>1–8 services · app required · digest images</span>
                  </div>
                  <label>
                    App environment snapshot <small>(optional JSON)</small>
                    <textarea
                      className="code-editor short"
                      rows={3}
                      spellCheck={false}
                      placeholder={'{"APP_URL": "https://app.example.com"}'}
                      value={variables}
                      onChange={(e) => setVariables(e.target.value)}
                    />
                    <small>
                      Leave blank to use the Compose environment or reuse the
                      current snapshot. For a first deployment with no
                      variables, enter {`{}`}. Do not also set
                      services.app.environment.
                    </small>
                  </label>
                  <div className="note">
                    <ShieldCheck size={17} />
                    <p>
                      The VPS validates the Compose subset and every service
                      against its root policy. No build contexts, privileged
                      containers, or host mounts. Review YAML before submitting.
                    </p>
                  </div>
                </>
              )}
              {m.kind === "stop" && (
                <>
                  <div className="note warning-note">
                    <Activity size={20} />
                    <p>
                      Stops all services in <strong>{p?.id}</strong>. Serving
                      traffic will be interrupted. Compose files and volumes are
                      retained.
                    </p>
                  </div>
                  <label>
                    Type the project ID to confirm
                    <input
                      required
                      autoComplete="off"
                      placeholder={p?.id}
                      value={confirmation}
                      onChange={(e) => setConfirmation(e.target.value)}
                    />
                  </label>
                </>
              )}
              {m.kind === "rollback" && (
                <>
                  <p>
                    Restore a recorded Compose release for{" "}
                    <strong>{p?.id}</strong>. Persistent data is retained;
                    database changes are not reversed.
                  </p>
                  <label>
                    Release
                    <select
                      value={release}
                      onChange={(e) => setRelease(e.target.value)}
                    >
                      {p?.releases.map((r) => (
                        <option value={r.id} key={r.id}>
                          {r.id} · {ago(r.created_at)}
                        </option>
                      ))}
                    </select>
                  </label>
                </>
              )}
            </div>
            <div className="modal-footer">
              {m.kind === "create" || m.kind === "deploy" ? (
                <button
                  className="text-button"
                  type="button"
                  disabled={busy}
                  onClick={() => setStep(0)}
                >
                  <ChevronLeft size={14} />
                  Environment
                </button>
              ) : (
                <span className="muted">A native confirmation follows.</span>
              )}
              <button
                className={`button ${m.kind === "stop" ? "danger" : "primary"}`}
                disabled={
                  busy ||
                  preview ||
                  (m.kind === "stop" && confirmation !== p?.id) ||
                  (m.kind === "deploy" &&
                    new TextEncoder().encode(compose).length > 65536)
                }
                type="submit"
              >
                {preview
                  ? "Preview · read-only"
                  : busy
                    ? "Submitting…"
                    : m.kind === "stop"
                      ? "Review stop"
                      : "Review & submit"}
                <ArrowRight size={15} />
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
export default App;
