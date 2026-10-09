import Button from "./Button";
import ServerDetailsTabs from "./ServerDetailsTabs";
import ServerDiagnostics from "./ServerDiagnostics";
import ServerUpdater from "./ServerUpdater";
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
  ChevronLeft,
  CircleAlert,
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
  LoaderCircle,
  LockKeyhole,
  Plus,
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
import toast from "react-hot-toast";
import * as api from "./api";
import SetupWizard from "./SetupWizard";
import CodeEditor from "./CodeEditor";
import { composeForEditor, lintCompose } from "./composeLint";
import ProjectConfiguration, {
  type HealthcheckRequest,
} from "./ProjectConfiguration";
import ProjectHeader, { ProjectSummary } from "./ProjectHeader";
import ServiceUpdate from "./ServiceUpdate";
import JobsTable from "./JobsTable";
import CopyButton from "./CopyButton";
import RouteSetup from "./RouteSetup";
import TerminalPage from "./TerminalPage";
import { ProviderDomains } from "./DomainProviders";
import Help from "./Help";
import Accordion from "./Accordion";
import Select from "./Select";
import { flavorIdentity, projectIdentity } from "./projectNaming";
import { demoInventory, demoJobs, demoProjects } from "./demo";
import dockyardIcon from "../src-tauri/icons/icon.png";
import type {
  Environment,
  Inventory,
  MigrationAssessment,
  Job,
  Pending,
  Project,
  Profile,
  Release,
  Service,
  Session,
} from "./types";

type Page =
  | "projects"
  | "deployments"
  | "domains"
  | "inventory"
  | "audit"
  | "security"
  | "terminal";
type Modal =
  | { kind: "create"; appID?: string }
  | { kind: "deploy" | "routes" | "stop" | "rollback"; project: Project }
  | null;
type Audit = { event_id: number; event: Record<string, unknown> };
const nav = [
  { id: "projects", label: "Projects", icon: LayoutDashboard },
  { id: "deployments", label: "Deployments", icon: Layers3 },
  { id: "domains", label: "Domains", icon: Globe2 },
  { id: "inventory", label: "VPS inventory", icon: Database },
  { id: "audit", label: "Audit trail", icon: History },
  { id: "terminal", label: "Terminal", icon: Terminal },
] as const;
const environments: Environment[] = ["production", "staging", "development"];
const labels: Record<Environment, string> = {
  production: "Production",
  staging: "Staging",
  development: "Development",
};
const instanceLabel = (slot: string) =>
  ({
    blue: "Instance 1",
    green: "Instance 2",
    active: "Live instance",
    inactive: "Standby instance",
  })[slot] ?? slot;
const operationLabel = (action: string) =>
  action === "blue-green" || action === "blue_green"
    ? "Seamless updates"
    : action === "service-update" || action === "service_update"
      ? "Service update"
      : action === "route-setup" || action === "route_setup"
        ? "Configure route"
        : action.charAt(0).toUpperCase() + action.slice(1).replaceAll("_", " ");
const activityVerb = (action: string) =>
  ({
    start: "Starting",
    restart: "Restarting",
    stop: "Stopping",
    deploy: "Deploying",
    rollback: "Rolling back",
    route_setup: "Connecting domain for",
    "route-setup": "Connecting domain for",
    service_update: "Updating",
    "service-update": "Updating",
  })[action] ?? `${operationLabel(action)} ·`;
// Human project name for job lists; falls back to the internal ID.
const projectName = (projects: Project[], id: string) => {
  const p = projects.find((x) => x.id === id);
  if (!p) return id;
  return p.environment === "production"
    ? p.app_id
    : `${p.app_id} · ${p.environment}`;
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
      {children && <p>{children}</p>}
    </div>
  );
}
function Skeleton({
  width,
  height = 14,
  radius = 5,
}: {
  width: string | number;
  height?: number;
  radius?: number;
}) {
  return (
    <span
      className="t-skel skeleton-bar"
      style={{ width, height, borderRadius: radius }}
      aria-hidden="true"
    >
      <span className="t-skel-skeleton is-pulsing">
        <span className="skeleton-fill" />
      </span>
    </span>
  );
}
function SkeletonStats({ count = 3 }: { count?: number }) {
  return (
    <div
      className={`stats-grid ${count === 3 ? "three" : ""}`}
      aria-label="Loading summary"
      role="status"
    >
      {Array.from({ length: count }, (_, index) => (
        <div className="stat skeleton-stat" key={index}>
          <div>
            <Skeleton width="48%" height={11} />
            <Skeleton width={18} height={18} radius={6} />
          </div>
          <Skeleton width={62} height={38} radius={7} />
        </div>
      ))}
    </div>
  );
}
function SkeletonProjectTable() {
  return (
    <div className="project-table" role="status" aria-label="Loading projects">
      <div className="table-head">
        <span>Project</span>
        <span>State</span>
        <span>Deployment</span>
        <span>Route</span>
        <span />
      </div>
      {Array.from({ length: 6 }, (_, index) => (
        <div className="project-row skeleton-project-row" key={index}>
          <span className="project-name">
            <Skeleton width={34} height={36} radius={8} />
            <span className="skeleton-lines">
              <Skeleton width={index % 2 ? 150 : 116} height={16} />
              <Skeleton width={108} height={11} />
            </span>
          </span>
          <Skeleton width={78} height={25} />
          <span className="skeleton-lines">
            <Skeleton width="75%" height={14} />
            <Skeleton width="60%" height={11} />
          </span>
          <span className="skeleton-lines">
            <Skeleton width="78%" height={14} />
            <Skeleton width="50%" height={11} />
          </span>
          <Skeleton width={10} height={16} />
        </div>
      ))}
    </div>
  );
}
function SkeletonRows({
  count = 3,
  label = "Loading server data",
}: {
  count?: number;
  label?: string;
}) {
  return (
    <div role="status" aria-label={label}>
      {Array.from({ length: count }, (_, index) => (
        <div className="inventory-row skeleton-list-row" key={index}>
          <Skeleton width={42} height={42} radius={9} />
          <div className="skeleton-lines">
            <Skeleton width={index % 2 ? 185 : 138} height={17} />
            <Skeleton width={index % 2 ? 120 : 155} height={12} />
          </div>
          <Skeleton width={82} height={24} />
        </div>
      ))}
    </div>
  );
}
function Brand() {
  return (
    <>
      <img className="brand-mark" src={dockyardIcon} alt="" />
      <span>
        dockyard<span className="brand-dot">.</span>
      </span>
    </>
  );
}
function LockedScreen({
  loading,
  retry,
  busy,
  error,
  unlock,
}: {
  loading: boolean;
  retry: boolean;
  busy: boolean;
  error: string;
  unlock: () => void;
}) {
  return (
    <main className="lock-screen">
      <div className="lock-card">
        <div className="lock-brand">
          <Brand />
        </div>
        <div className="lock-symbol">
          <LockKeyhole size={28} />
        </div>
        <h1>Locked</h1>
        {error && (
          <p className="lock-error" role="alert">
            {error}
          </p>
        )}
        <Button
          type="button"
          className="button primary lock-unlock"
          disabled={loading || busy}
          onClick={unlock}
          autoFocus={!loading}
        >
          <Fingerprint size={19} />
          {loading
            ? "Checking this Mac…"
            : busy
              ? "Waiting for macOS…"
              : retry
                ? "Retry"
                : "Unlock with Touch ID"}
        </Button>
      </div>
    </main>
  );
}
function App() {
  const [preview, setPreview] = useState(!api.native);
  const [session, setSession] = useState<Session>({
    unlocked: false,
    profile: null,
    jobs: [],
  });
  const [sessionReady, setSessionReady] = useState(!api.native);
  const [sessionLoadFailed, setSessionLoadFailed] = useState(false);
  const [hasEnrollment, setHasEnrollment] = useState(false);
  const [page, setPage] = useState<Page>("projects");
  const [projects, setProjects] = useState<Project[]>([]);
  const [inventory, setInventory] = useState<Inventory | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [pending, setPending] = useState<Pending>(null);
  const [audit, setAudit] = useState<Audit[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [projectSection, setProjectSection] = useState("overview");
  const [modal, setModal] = useState<Modal>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [jobsReady, setJobsReady] = useState(false);
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
    setSession((s) => ({ ...s, unlocked: false, profile: null, jobs: [] }));
    setPreview(false);
    setProjects([]);
    setInventory(null);
    setJobs([]);
    setAudit([]);
    setPending(null);
    setModal(null);
    setSelected(null);
    setUpdated(undefined);
    setLoading(false);
    setJobsReady(false);
    setSetupOpen(false);
    setError("");
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
        // Saved writes are local state; keep them current even while the VPS
        // service is stopped and the project read cannot connect.
        const pending = await api.pendingInfo();
        if (epoch.current !== version) return;
        setPending(pending);
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
        if (epoch.current === version) {
          setLoading(false);
          setJobsReady(true);
        }
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
          setHasEnrollment(Boolean(s.enrolled || s.unlocked));
          setSessionLoadFailed(false);
          setSessionReady(true);
          unlocked.current = s.unlocked;
          if (s.unlocked) void refresh();
        }
      })
      .catch((e) => {
        if (!disposed) {
          setSessionLoadFailed(true);
          setSessionReady(true);
          report(e);
        }
      });
    listen("session-locked", () => {
      loseSession();
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
      setHasEnrollment(true);
      setSessionLoadFailed(false);
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
    let result: { job_id: string };
    try {
      result = await api.mutate(mutation);
    } finally {
      const saved = await api.pendingInfo().catch(() => undefined);
      if (version === epoch.current && saved !== undefined) setPending(saved);
    }
    if (version !== epoch.current) return;
    toast.success(`Operation accepted · ${result.job_id}`);
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
  // Follow a running saved operation; reading its job clears it when done.
  useEffect(() => {
    const job = pending?.job_id;
    if (!job || preview || !api.native) return;
    const timer = setInterval(async () => {
      try {
        await api.read<Job>({ kind: "job", job });
        const saved = await api.pendingInfo();
        if (!saved) {
          clearInterval(timer);
          await refresh();
        }
      } catch {
        // Keep the bar; the next tick or a manual refresh retries.
      }
    }, 3000);
    return () => clearInterval(timer);
  }, [pending?.job_id, preview]);
  async function doResolve() {
    const version = epoch.current;
    try {
      const result = await api.resolvePending();
      if (result.outcome === "not_applied" || result.outcome === "not_sent")
        toast.success("It was never applied. Nothing changed.");
      else if (result.outcome === "applied")
        toast.success(`Found it · ${result.status?.replaceAll("_", " ")}`);
      await refresh();
    } catch (e) {
      report(e);
    } finally {
      const saved = await api.pendingInfo().catch(() => undefined);
      if (version === epoch.current && saved !== undefined) setPending(saved);
    }
  }
  async function doRetry() {
    const version = epoch.current;
    try {
      await api.retry();
      await refresh();
    } catch (e) {
      report(e);
    } finally {
      const saved = await api.pendingInfo().catch(() => undefined);
      if (version === epoch.current && saved !== undefined) setPending(saved);
    }
  }
  async function retrySessionInfo() {
    setAuthBusy(true);
    setError("");
    try {
      const s = await api.sessionInfo();
      setSession(s);
      setHasEnrollment(Boolean(s.enrolled || s.unlocked));
      setSessionLoadFailed(false);
      unlocked.current = s.unlocked;
      if (s.unlocked) await refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setAuthBusy(false);
    }
  }
  const shownProjects = preview ? demoProjects : projects;
  const shownInventory = preview ? demoInventory : inventory;
  const shownJobs = preview ? demoJobs : jobs;
  const project = shownProjects.find((p) => p.id === selected);
  const observedProject = selected?.startsWith("observed:")
    ? shownInventory?.projects.find(
        (p) => !p.managed && p.id === selected.slice("observed:".length),
      )
    : undefined;
  const projectCount =
    shownProjects.length +
    (shownInventory?.projects.filter((p) => !p.managed).length ?? 0);
  const busy = authBusy || loading;
  const initialWorkspaceLoading = !preview && loading && !updated;
  if (
    api.native &&
    (!sessionReady || sessionLoadFailed || (hasEnrollment && !session.unlocked))
  ) {
    return (
      <LockedScreen
        loading={!sessionReady}
        retry={sessionLoadFailed}
        busy={authBusy}
        error={sessionReady ? error : ""}
        unlock={() => {
          if (sessionLoadFailed) void retrySessionInfo();
          else void authenticate("unlock");
        }}
      />
    );
  }
  return (
    <div
      className={
        page === "terminal" && canUse
          ? "app-shell terminal-layout"
          : "app-shell"
      }
    >
      <aside className="sidebar">
        <div className="brand">
          <Brand />
        </div>
        <div className="nav-label">Workspace</div>
        <nav>
          {nav.map((n) => (
            <Button
              type="button"
              key={n.id}
              className={page === n.id ? "nav-item active" : "nav-item"}
              onClick={() => {
                setPage(n.id);
                setSelected(null);
                setError("");
              }}
            >
              <n.icon size={16} />
              <span>{n.label}</span>
              {n.id === "projects" && canUse && !initialWorkspaceLoading && (
                <small>{projectCount}</small>
              )}
            </Button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div
            className={
              page === "security" ? "server-footer active" : "server-footer"
            }
          >
            <Button
              type="button"
              className="server-summary"
              onClick={() => {
                setPage("security");
                setSelected(null);
                setError("");
              }}
              aria-label="Open server details"
            >
              <span className="server-symbol">
                <Server size={18} />
              </span>
              <span className="server-summary-copy">
                <strong>
                  {preview
                    ? "Preview workspace"
                    : (session.profile?.name ?? "Your VPS")}
                </strong>
                <span className="server-status">
                  <i
                    className={
                      session.unlocked && !preview ? "green-dot" : "gray-dot"
                    }
                  />
                  {preview
                    ? "Sample data"
                    : session.unlocked
                      ? "Connected"
                      : "Not connected"}
                </span>
              </span>
            </Button>
            <Button
              type="button"
              className="sidebar-lock"
              aria-label="Lock session"
              title="Lock session"
              onClick={() => {
                loseSession();
                setPreview(false);
                if (api.native) void api.lock();
              }}
            >
              <LockKeyhole size={16} />
            </Button>
          </div>
        </div>
      </aside>
      <main className="main">
        {preview && (
          <div className="preview-banner">
            <FileCode2 size={15} />
            <span>
              <strong>Preview</strong> · Sample data
            </span>
            <Button
              type="button"
              onClick={() => {
                setPreview(false);
                setSelected(null);
                setPage("security");
              }}
            >
              Connect your VPS <ArrowRight size={14} />
            </Button>
          </div>
        )}
        {error && (
          <div className="alert error" role="alert">
            {error}
            <Button
              type="button"
              aria-label="Dismiss error"
              onClick={() => setError("")}
            >
              <X size={15} />
            </Button>
          </div>
        )}
        {pending && !preview && canUse && (
          <div
            className={`activity-bar ${pending.job_id ? "running" : "unconfirmed"}`}
            role="status"
          >
            {pending.job_id ? (
              <LoaderCircle size={14} className="spin" aria-hidden="true" />
            ) : (
              <CircleAlert size={14} aria-hidden="true" />
            )}
            <span>
              {pending.job_id
                ? `${activityVerb(pending.action)} ${projectName(projects, pending.project)}…`
                : `${operationLabel(pending.action)} · ${projectName(projects, pending.project)} wasn't confirmed`}
            </span>
            {pending.job_id ? (
              <button
                type="button"
                className="activity-link"
                onClick={() => {
                  setPage("deployments");
                  setSelected(null);
                }}
              >
                Show
              </button>
            ) : (
              <>
                <button
                  type="button"
                  className="activity-link"
                  disabled={busy}
                  title="Send the same request again"
                  onClick={() => void doRetry()}
                >
                  Send again
                </button>
                <Button
                  type="button"
                  className="button small"
                  disabled={busy}
                  onClick={() => void doResolve()}
                >
                  Check
                </Button>
              </>
            )}
          </div>
        )}
        <div
          className={
            page === "terminal" && canUse
              ? "content content-terminal"
              : "content"
          }
        >
          {canUse && (
            <TerminalPage
              visible={page === "terminal"}
              native={api.native && !preview}
              server={session.profile?.server_ip}
              report={report}
            />
          )}
          {page === "security" ? (
            <Security
              profile={session.profile}
              unlocked={session.unlocked && !preview}
              native={api.native}
              busy={authBusy}
              authenticate={authenticate}
              setup={() => setSetupOpen(true)}
              explore={() => setPreview(true)}
              openOperations={() => {
                setSelected(null);
                setPage("deployments");
              }}
              forget={async () => {
                loseSession();
                setPreview(false);
                setAuthBusy(true);
                try {
                  await api.forget();
                  loseSession();
                  setSession({ unlocked: false, profile: null, jobs: [] });
                  setHasEnrollment(false);
                  toast.success(
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
          ) : page === "terminal" ? null : project ? (
            <ProjectDetail
              key={project.id + String(preview)}
              project={project}
              initialTab={projectSection}
              preview={preview}
              jobs={shownJobs}
              refreshWorkspace={refresh}
              openDeployments={() => {
                setSelected(null);
                setPage("deployments");
              }}
              open={setModal}
              action={simple}
              execute={perform}
              report={report}
              openServerDetails={() => {
                setSelected(null);
                setPage("security");
              }}
            />
          ) : observedProject ? (
            <ObservedProjectDetail
              project={observedProject}
              inventory={shownInventory}
              preview={preview}
              perform={perform}
              back={() => setSelected(null)}
              openServerDetails={() => {
                setSelected(null);
                setPage("security");
              }}
            />
          ) : page === "projects" ? (
            <Projects
              projects={shownProjects}
              inventory={shownInventory}
              updated={updated}
              loading={loading}
              initialLoading={initialWorkspaceLoading}
              select={(id) => {
                setProjectSection("overview");
                setSelected(id);
              }}
              selectObserved={(id) => setSelected(`observed:${id}`)}
              create={() => setModal({ kind: "create" })}
              refresh={() => {
                if (!preview) void refresh();
                else toast("This preview uses sample data.");
              }}
            />
          ) : page === "deployments" ? (
            <Jobs
              jobs={shownJobs}
              projects={projects}
              preview={preview}
              loading={!preview && !jobsReady}
              refresh={refresh}
              report={report}
            />
          ) : page === "domains" ? (
            <ProviderDomains
              preview={preview}
              serverIP={session.profile?.server_ip}
              routes={
                <Domains
                  projects={shownProjects}
                  inventory={shownInventory}
                  loading={initialWorkspaceLoading}
                  select={(id) => {
                    setProjectSection("domains");
                    setPage("projects");
                    setSelected(id);
                  }}
                  open={setModal}
                />
              }
            />
          ) : page === "inventory" ? (
            <InventoryView
              inventory={shownInventory}
              loading={loading}
              initialLoading={initialWorkspaceLoading}
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
        {page !== "terminal" && (
          <footer className="footer">
            <span>
              <span className="tiny-mark">D</span>DOCKYARD{" "}
              <span className="footer-dot">/</span> Service manager.
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
        )}
      </main>
      {modal && canUse && (
        <OperationModal
          modal={modal}
          projects={shownProjects}
          observedIDs={(shownInventory?.projects ?? [])
            .filter((p) => !p.managed)
            .map((p) => p.id)}
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
            setHasEnrollment(true);
            setPreview(false);
            setSetupOpen(false);
            setPage("projects");
            toast.success(
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
      <h1>
        Connect your VPS{" "}
        <Help label="server connection">
          Set up Dockyard on an Ubuntu VPS, or import credentials for a server
          already configured. A saved connection unlocks with Touch ID.
        </Help>
      </h1>
      <div className="connect-actions">
        <Button
          type="button"
          className="button primary"
          disabled={busy}
          onClick={setup}
        >
          <Server size={17} />
          Set up Dockyard on your server
        </Button>
      </div>

      <div className="connect-actions">
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={() => authenticate("enroll")}
        >
          <ShieldCheck size={17} />
          {busy ? "Waiting for macOS…" : "Import existing enrollment"}
        </Button>
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={() => authenticate("unlock")}
        >
          <Fingerprint size={16} />
          Unlock with Touch ID
        </Button>
      </div>
      <Button type="button" className="text-button" onClick={explore}>
        Explore the interface <ArrowRight size={14} />
      </Button>
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
  inventory,
  updated,
  loading,
  initialLoading,
  select,
  selectObserved,
  create,
  refresh,
}: {
  projects: Project[];
  inventory: Inventory | null;
  updated?: string;
  loading: boolean;
  initialLoading: boolean;
  select: (s: string) => void;
  selectObserved: (id: string) => void;
  create: () => void;
  refresh: () => void;
}) {
  const [filter, setFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const observed = (inventory?.projects ?? []).filter((p) => !p.managed);
  const entries = [
    ...projects.map((project) => ({ kind: "managed" as const, project })),
    ...observed.map((project) => ({ kind: "observed" as const, project })),
  ];
  const filtered = entries.filter(({ kind, project }) => {
    const domains =
      kind === "managed"
        ? project.domains
        : (inventory?.sites ?? [])
            .filter((site) => site.project_ids.includes(project.id))
            .map((site) => site.host_matcher);
    const name = kind === "managed" ? project.app_id : project.name;
    return (
      (filter === "all" || project.environment === filter) &&
      `${name} ${project.id} ${domains.join(" ")}`
        .toLowerCase()
        .includes(query.toLowerCase())
    );
  });
  const maxPage = Math.max(0, Math.ceil(filtered.length / 20) - 1);
  const slice = filtered.slice(
    Math.min(page, maxPage) * 20,
    Math.min(page, maxPage) * 20 + 20,
  );
  if (initialLoading) {
    return (
      <>
        <div className="page-heading">
          <h1>Projects</h1>
          <Button type="button" className="button primary" onClick={create}>
            <Plus size={17} /> New project
          </Button>
        </div>
        <SkeletonStats />
        <div className="section-heading">
          <h2>Projects</h2>
          <RefreshCw size={16} className="spin" />
        </div>
        <div className="filters skeleton-filters" aria-hidden="true">
          <Skeleton width={390} height={37} radius={6} />
          <Skeleton width={195} height={36} radius={6} />
        </div>
        <SkeletonProjectTable />
      </>
    );
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>
            Projects{" "}
            <Help label="project management">
              Dockyard deployments and existing Compose stacks from /docker
              appear here. Existing stacks are read-only until migrated.
            </Help>
          </h1>
        </div>
        <Button type="button" className="button primary" onClick={create}>
          <Plus size={17} />
          New project
        </Button>
      </div>
      <div className="stats-grid three">
        <Stat
          label="Projects"
          value={entries.length}
          icon={Box}
          note="Dockyard deployments and existing Compose stacks"
        />
        <Stat
          label="Managed"
          value={projects.length}
          icon={Layers3}
          note="Projects Dockyard can deploy and control"
        />
        <Stat
          label="Existing Compose"
          value={observed.length}
          icon={FolderGit2}
          note="Read-only stacks found in /docker"
        />
      </div>
      <div className="workspace-grid">
        <div className="project-area">
          <div className="section-heading">
            <h2>
              Projects <span>{entries.length}</span>
            </h2>
            <Button
              type="button"
              className="icon-button"
              aria-label="Refresh workspace"
              title="Refresh workspace"
              disabled={loading}
              onClick={refresh}
            >
              <RefreshCw size={16} className={loading ? "spin" : ""} />
            </Button>
          </div>
          <div className="filters">
            <div className="segments">
              {["all", ...environments].map((e) => (
                <Button
                  type="button"
                  key={e}
                  className={filter === e ? "selected" : ""}
                  onClick={() => {
                    setFilter(e);
                    setPage(0);
                  }}
                >
                  {e === "all" ? "All environments" : labels[e as Environment]}
                </Button>
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
              <span>Project</span>
              <span>State</span>
              <span>Deployment</span>
              <span>Route</span>
              <span />
            </div>
            {slice.map((entry) => {
              if (entry.kind === "observed") {
                const p = entry.project;
                const sites = (inventory?.sites ?? []).filter((site) =>
                  site.project_ids.includes(p.id),
                );
                const ports = p.services.flatMap((s) => s.published_ports);
                return (
                  <Button
                    type="button"
                    className="project-row"
                    key={`observed:${p.id}`}
                    onClick={() => selectObserved(p.id)}
                  >
                    <span className="project-name">
                      <span className="app-icon sand">{initials(p.name)}</span>
                      <span>
                        <strong>{p.name}</strong>
                        <span className="env-label">
                          <i className="env-dot production" />
                          Production · existing
                        </span>
                      </span>
                    </span>
                    <span>
                      <Tag tone={p.present ? "neutral" : "red"}>
                        {p.present ? "Observed" : "Missing folder"}
                      </Tag>
                    </span>
                    <span className="deployment-label">
                      <FolderGit2 size={14} />
                      <span>
                        Existing Compose
                        <small>{p.services.length} services · read-only</small>
                      </span>
                    </span>
                    <span className="route-label">
                      <span>{sites[0]?.host_matcher ?? "No linked route"}</span>
                      <small>
                        {ports.length
                          ? `:${ports[0]} observed port`
                          : "No observed port"}
                        {sites.length > 1 ? ` · +${sites.length - 1} more` : ""}
                      </small>
                    </span>
                    <ChevronRight size={16} />
                  </Button>
                );
              }
              const p = entry.project;
              return (
                <Button
                  type="button"
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
                      {p.zerodowntime ? "Seamless updates" : "Single instance"}
                      <small>
                        {p.active_slot ? "Live instance" : "Not deployed"}
                      </small>
                    </span>
                  </span>
                  <span className="route-label">
                    <span>
                      {p.domains[0] ?? p.external_domains?.[0] ?? "No domain"}
                    </span>
                    <small>
                      :{p.active_slot === "green" ? p.green_port : p.blue_port}
                      {p.domains.length > 1
                        ? ` · +${p.domains.length - 1} more`
                        : ""}
                    </small>
                  </span>
                  <ChevronRight size={16} />
                </Button>
              );
            })}
            {slice.length === 0 && (
              <Empty
                title={
                  entries.length
                    ? "No matching projects"
                    : inventory === null
                      ? "Inventory unavailable"
                      : "No projects found"
                }
              >
                {entries.length === 0 && inventory === null
                  ? "Refresh to read existing Compose stacks."
                  : entries.length === 0
                    ? "Create a project or check VPS inventory."
                    : undefined}
              </Empty>
            )}
          </div>
          <div className="table-footer">
            <span>
              {filtered.length} {filtered.length === 1 ? "project" : "projects"}
              {updated ? ` · Read ${ago(updated)}` : ""}
            </span>
            <div>
              <Button
                type="button"
                aria-label="Previous page"
                disabled={page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                <ChevronLeft size={15} />
              </Button>
              <span>
                {Math.min(page, maxPage) + 1} / {maxPage + 1}
              </span>
              <Button
                type="button"
                aria-label="Next page"
                disabled={page >= maxPage}
                onClick={() => setPage((p) => p + 1)}
              >
                <ChevronRight size={15} />
              </Button>
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
        <span>
          {label}
          <Help label={label.toLowerCase()}>{note}</Help>
        </span>
        <Icon size={17} />
      </div>
      <strong>{value}</strong>
    </div>
  );
}
export function ProjectDetail({
  project: p,
  initialTab = "overview",
  preview,
  open,
  action,
  execute,
  report,
  openServerDetails,
  jobs = [],
  refreshWorkspace,
  openDeployments,
}: {
  project: Project;
  preview: boolean;
  open: (m: Modal) => void;
  action: (a: "start" | "restart", p: Project) => Promise<void>;
  execute: (m: unknown) => Promise<void>;
  report: (e: unknown) => void;
  openServerDetails: () => void;
  initialTab?: string;
  jobs?: Job[];
  refreshWorkspace?: () => Promise<void>;
  openDeployments?: () => void;
}) {
  const [tab, setTab] = useState(initialTab);
  const [configDirty, setConfigDirty] = useState(false);
  const [healthcheckRequest, setHealthcheckRequest] =
    useState<HealthcheckRequest>();
  const [services, setServices] = useState<Service[]>([]);
  const [updatingService, setUpdatingService] = useState<Service>();
  const [servicesLoading, setServicesLoading] = useState(!preview);
  const [servicesError, setServicesError] = useState(false);
  const [servicesRefresh, setServicesRefresh] = useState(0);
  const [dnsLoading, setDnsLoading] = useState(!preview);
  const [logsLoading, setLogsLoading] = useState(false);
  const [status, setStatus] = useState<{
    health: string;
    route: string;
    busy: boolean;
    public_tls_state: string;
  }>();
  const [logs, setLogs] = useState("");
  const [truncated, setTruncated] = useState(false);
  const [service, setService] = useState(
    p.mode === "compose" ? (p.route_service ?? "") : "app",
  );
  const [slot, setSlot] = useState("active");
  const [since, setSince] = useState("30m");
  const [busy, setBusy] = useState(false);
  const [starting, setStarting] = useState(false);
  const [configuringRoute, setConfiguringRoute] = useState(false);
  const [dns, setDNS] = useState<
    { hostname: string; assigned: boolean; dns_record_id?: string }[]
  >([]);
  const serviceRevision = JSON.stringify(p.service_instances ?? {});
  const operationRevision = JSON.stringify(
    jobs
      .filter((job) => job.project_id === p.id)
      .map((job) => [job.job_id, job.status, job.finished_at]),
  );
  const recoveryJob = jobs.find((job) => job.status === "recovery_required");
  const projectOperation = jobs.find(
    (job) =>
      job.project_id === p.id && ["queued", "running"].includes(job.status),
  );
  const startBlocked =
    preview ||
    starting ||
    p.state !== "stopped" ||
    !p.active_slot ||
    !p.slots[p.active_slot] ||
    Boolean(recoveryJob || projectOperation || status?.busy);
  async function startProject() {
    if (startBlocked) return;
    setStarting(true);
    try {
      await action("start", p);
    } catch (error) {
      report(error);
    } finally {
      setStarting(false);
    }
  }
  const needsServerUpdate =
    p.mode === "compose" &&
    services.some(
      (s) =>
        s.slot === p.active_slot &&
        s.updatable === undefined &&
        s.seamless === undefined &&
        !s.update_reason &&
        !s.container_ports,
    );
  function updateBlockedReason(s: Service) {
    if (servicesLoading) return "Checking service availability…";
    if (servicesError) return "Could not check services. Refresh to try again.";
    if (recoveryJob)
      return "An interrupted operation needs recovery. Open Deployments.";
    if (p.state !== "running")
      return "Start this project before updating services.";
    if (starting || projectOperation || status?.busy)
      return "Another operation is running. Refresh when it finishes.";
    if (!s.updatable)
      return (
        s.update_reason ||
        (s.updatable === false
          ? "Individual updates are unavailable for this service."
          : "Update Dockyard on the server to enable this action.")
      );
    return "";
  }
  useEffect(() => {
    let cancelled = false;
    if (preview) {
      setServicesLoading(false);
      setDnsLoading(false);
      setServices([
        {
          name: "app",
          depends_on: [],
          updatable: true,
          seamless: p.environment === "production" && !p.zerodowntime,
          container_ports: [3000],
          update_reason: "This flavor uses a controlled restart.",
          slot: p.active_slot ?? "blue",
          image: p.releases[0].image,
          template_id: p.template_id,
          environment_revision: "env-preview",
        },
        {
          name: "worker",
          depends_on: ["app"],
          updatable: true,
          seamless: false,
          update_reason: "Background workers use a controlled restart.",
          slot: p.active_slot ?? "blue",
          image: p.releases[0].image,
          template_id: "worker-node",
          environment_revision: "env-preview",
        },
      ]);
      setDNS(p.domains.map((hostname) => ({ hostname, assigned: true })));
      return;
    }
    setServicesLoading(true);
    setServicesError(false);
    setStatus(undefined);
    setDnsLoading(true);
    setServices([]);
    setDNS([]);
    void Promise.all([
      api.read<{ services: Service[] }>({
        kind: "project",
        project: p.id,
        view: "services",
      }),
      api.read<NonNullable<typeof status>>({
        kind: "project",
        project: p.id,
        view: "status",
      }),
    ])
      .then(([d, projectStatus]) => {
        if (!cancelled) {
          setStatus(projectStatus);
          const items = d.services ?? [];
          setServices(items);
          setService((current) =>
            items.some((item) => item.name === current)
              ? current
              : (items[0]?.name ?? ""),
          );
        }
      })
      .catch((e) => {
        if (!cancelled) {
          setServicesError(true);
          report(e);
        }
      })
      .finally(() => {
        if (!cancelled) setServicesLoading(false);
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
      })
      .finally(() => {
        if (!cancelled) setDnsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [
    p.id,
    p.releases.at(-1)?.id,
    p.domains.join(),
    p.state,
    p.active_slot,
    p.service_updates,
    serviceRevision,
    operationRevision,
    servicesRefresh,
    preview,
    report,
  ]);
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
    setLogsLoading(true);
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
      setLogsLoading(false);
    }
  }
  return (
    <>
      <ProjectHeader
        project={p}
        onAddFlavor={() => open({ kind: "create", appID: p.app_id })}
        onDeploy={() =>
          p.mode === "compose"
            ? setTab("configuration")
            : open({ kind: "deploy", project: p })
        }
      />
      <nav className="tabs project-tabs" aria-label="Project sections">
        {[
          { id: "overview", label: "Overview", icon: LayoutDashboard },
          { id: "services", label: "Services", icon: Box },
          ...(p.mode === "compose"
            ? [{ id: "configuration", label: "Configuration", icon: FileCode2 }]
            : []),
          { id: "releases", label: "Releases", icon: History },
          { id: "logs", label: "Logs", icon: Terminal },
          { id: "domains", label: "Domains", icon: Globe2 },
        ].map(({ id, label, icon: Icon }) => (
          <Button
            key={id}
            type="button"
            aria-pressed={tab === id}
            className={tab === id ? "selected" : ""}
            onClick={() => {
              if (
                id !== tab &&
                configDirty &&
                !window.confirm("Discard your unsaved configuration edits?")
              )
                return;
              setTab(id);
            }}
          >
            <Icon size={19} aria-hidden="true" />
            <span>{label}</span>
          </Button>
        ))}
      </nav>
      {tab === "configuration" && (
        <ProjectConfiguration
          project={p}
          preview={preview}
          execute={execute}
          onDirtyChange={setConfigDirty}
          healthcheck={healthcheckRequest}
          onHealthcheckHandled={() => setHealthcheckRequest(undefined)}
        />
      )}
      {tab === "overview" && (
        <>
          <ProjectSummary
            project={p}
            onConfigure={() => setTab("services")}
            onDomains={() => setTab("domains")}
          />
          {p.adoption && (
            <p className="alert pending">
              {p.published_route
                ? "Managed in place · configure domains through Dockyard. Containers and their published ports stay in place."
                : p.service_updates
                  ? "Managed in place · individual app updates use Dockyard's website routes. Database services remain in this project."
                  : "Managed in place · Caddy routes remain in the existing Caddyfile. Update individual services from Services."}
            </p>
          )}
          <div className="detail-grid">
            <section className="panel">
              <div className="section-heading">
                <h2>App instances</h2>
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
                      <span className="eyebrow">{instanceLabel(s)}</span>
                      <Tag tone={p.active_slot === s ? "green" : "neutral"}>
                        {p.active_slot === s ? "Live" : "Standby"}
                      </Tag>
                    </div>
                    <Server size={28} />
                    <h3>
                      127.0.0.1:
                      {s === p.active_slot &&
                      p.route_service &&
                      p.service_instances?.[p.route_service]?.port
                        ? p.service_instances[p.route_service].port
                        : s === "blue"
                          ? p.blue_port
                          : p.green_port}
                    </h3>
                    <p>
                      {s === p.active_slot &&
                      p.route_service &&
                      p.service_instances?.[p.route_service]
                        ? p.service_instances[p.route_service].release.id
                        : (p.slots[s]?.id ?? "No release recorded")}
                    </p>
                    <small className="mono">
                      {p.slots[s]?.image.split("@")[1]?.slice(0, 22) ??
                        "Awaiting Compose deployment"}
                      …
                    </small>
                  </div>
                ))}
              </div>
              <p className="muted">
                Instances show recorded metadata. Check live status to verify
                container health and the Caddy route.
              </p>
            </section>
            <section className="panel">
              <div className="section-heading">
                <h2>Live verification</h2>
                <Button
                  type="button"
                  className="button small"
                  disabled={busy}
                  onClick={() => void check()}
                >
                  <RefreshCw size={14} />
                  Check now
                </Button>
              </div>
              <details className="connection-details">
                <summary>Connection details</summary>
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
              </details>
              <hr />
              <h3>Service controls</h3>
              <div className="controls">
                <Button
                  type="button"
                  className="button"
                  disabled={startBlocked}
                  onClick={() => void startProject()}
                >
                  <Zap size={14} />
                  {starting ? "Starting…" : "Start"}
                </Button>
                <Button
                  type="button"
                  className="button"
                  onClick={() => action("restart", p)}
                >
                  <RefreshCw size={14} />
                  Restart
                </Button>
                <Button
                  type="button"
                  className="button danger"
                  onClick={() => open({ kind: "stop", project: p })}
                >
                  Stop services
                </Button>
              </div>
              <p className="muted">
                Stopping preserves Compose files, releases and data volumes.
              </p>
            </section>
          </div>
        </>
      )}
      {tab === "services" && (
        <section className="panel services-panel">
          <div className="services-header">
            <div>
              <h2>Services</h2>
              <p>
                Update one service at a time. Its dependencies keep running.
              </p>
            </div>
            <div className="service-heading-actions">
              {!servicesLoading && !servicesError && (
                <Tag>{services.length} recorded services</Tag>
              )}
              <Button
                type="button"
                className="button small"
                disabled={servicesLoading}
                onClick={() => {
                  setServicesRefresh((n) => n + 1);
                  if (!preview) void refreshWorkspace?.();
                }}
              >
                <RefreshCw size={14} />{" "}
                {servicesLoading ? "Refreshing…" : "Refresh"}
              </Button>
            </div>
          </div>
          {p.mode === "compose" && p.state === "stopped" && !recoveryJob && (
            <div className="service-update-notice" role="status">
              <div>
                <strong>Project stopped</strong>
                <p>
                  Service updates become available once this project is running.
                  Start its last recorded release, then return here after the
                  startup operation succeeds.
                </p>
                {(projectOperation || status?.busy) && (
                  <p>
                    Another operation is in progress. Wait for it to finish.
                  </p>
                )}
              </div>
              <Button
                type="button"
                className="button primary"
                disabled={startBlocked}
                onClick={() => void startProject()}
              >
                <Zap size={16} /> {starting ? "Starting…" : "Start project"}
              </Button>
            </div>
          )}
          {!servicesLoading && needsServerUpdate && (
            <div className="service-update-notice" role="status">
              <div>
                <strong>Server update required</strong>
                <p>
                  This server hasn’t reported support for individual updates. In
                  Server details → Updates, check and update Dockyard to version
                  0.7.0 or later. Then refresh Services.
                </p>
              </div>
              <Button
                type="button"
                className="button small"
                onClick={openServerDetails}
              >
                <Server size={14} /> Server details
              </Button>
            </div>
          )}
          {recoveryJob && (
            <div className="service-update-notice" role="alert">
              <div>
                <strong>Recovery required</strong>
                <p>
                  An interrupted server operation blocks updates. Resolve it in
                  Deployments, then refresh Services.
                </p>
              </div>
              {openDeployments && (
                <Button
                  type="button"
                  className="button small"
                  onClick={openDeployments}
                >
                  <Layers3 size={14} /> View recovery
                </Button>
              )}
            </div>
          )}
          {servicesError && (
            <div className="service-update-notice" role="alert">
              Could not load service availability. Refresh to try again.
            </div>
          )}
          {servicesLoading && (
            <SkeletonRows count={3} label="Loading services" />
          )}
          {!!services.length && (
            <div className="services-table-scroll">
              <table className="services-table" aria-label="Project services">
                <colgroup>
                  <col className="services-column-name" />
                  <col className="services-column-dependencies" />
                  <col />
                  <col className="services-column-action" />
                </colgroup>
                <thead>
                  <tr>
                    <th scope="col">Service</th>
                    <th scope="col">Depends on</th>
                    <th scope="col">Status</th>
                    <th scope="col">
                      <span className="services-sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {services.map((s) => {
                    const active = s.slot === p.active_slot;
                    const blocked =
                      p.mode === "compose" && active
                        ? updateBlockedReason(s)
                        : "";
                    const showReason =
                      blocked &&
                      !(needsServerUpdate && !s.update_reason && !s.updatable);
                    const recordedState = active
                      ? p.state.replaceAll("_", " ")
                      : "Recorded";
                    return (
                      <tr key={s.slot + s.name}>
                        <th scope="row">
                          <div className="services-identity">
                            <span className="services-icon">
                              <Box size={20} aria-hidden="true" />
                            </span>
                            <div>
                              <strong>{s.name}</strong>
                              <code title={s.image}>
                                {s.image || "Built from Compose"}
                              </code>
                            </div>
                          </div>
                        </th>
                        <td className="services-dependencies">
                          {s.depends_on == null ? (
                            <span title="Dependency metadata is available with Dockyard 0.7.1 or later on the server.">
                              Not recorded
                            </span>
                          ) : s.depends_on.length ? (
                            s.depends_on.map((dependency) => (
                              <code key={dependency}>{dependency}</code>
                            ))
                          ) : (
                            <span aria-label="No dependencies">–</span>
                          )}
                        </td>
                        <td>
                          <div
                            className="services-status"
                            title="Recorded project state. Check Overview to verify live container health."
                          >
                            <i
                              data-state={active ? p.state : "recorded"}
                              aria-hidden="true"
                            />
                            <span className="services-state-label">
                              {recordedState}
                            </span>
                            <span>· {instanceLabel(s.slot)}</span>
                          </div>
                          <p
                            className={`services-status-note ${showReason ? "blocked" : ""}`}
                          >
                            {showReason ? blocked : "Image recorded at deploy"}
                          </p>
                        </td>
                        <td className="services-action">
                          {p.mode === "compose" && active && (
                            <Button
                              type="button"
                              className="button"
                              variant="outline"
                              disabled={Boolean(blocked)}
                              title={
                                blocked ||
                                `Update only ${s.name} · ${s.seamless ? "Seamless updates" : "Controlled restart"}`
                              }
                              aria-label={`Pull & update ${s.name}`}
                              onClick={() => setUpdatingService(s)}
                            >
                              <RefreshCw size={16} /> Pull & update
                            </Button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {!services.length && !servicesLoading && !servicesError && (
            <Empty title="No services recorded">
              Deploy your first validated Compose stack.
            </Empty>
          )}
          {updatingService && (
            <ServiceUpdate
              key={p.id + updatingService.name}
              project={p}
              service={
                services.find(
                  (s) =>
                    s.name === updatingService.name &&
                    s.slot === updatingService.slot,
                ) ?? updatingService
              }
              preview={preview}
              execute={execute}
              onClose={() => setUpdatingService(undefined)}
              onAddHealthcheck={(port, path) => {
                setHealthcheckRequest({
                  service: updatingService.name,
                  image: updatingService.image ?? "",
                  port,
                  path,
                });
                setUpdatingService(undefined);
                setTab("configuration");
              }}
            />
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
              <Button
                type="button"
                className="button small"
                onClick={() =>
                  open({ kind: "rollback", project: { ...p, releases: [r] } })
                }
              >
                Rollback <ArrowDownLeft size={14} />
              </Button>
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
            <div className="heading-actions">
              <Tag>Bounded · 200 lines</Tag>
              <CopyButton text={logs} />
            </div>
          </div>
          <div className="log-filters">
            <Select
              label="Service"
              value={service}
              onValueChange={setService}
              options={Array.from(
                new Set(["app", ...services.map((s) => s.name)]),
              ).map((s) => ({ value: s, label: s }))}
            />
            <Select
              label="Instance"
              value={slot}
              onValueChange={setSlot}
              options={[
                "active",
                ...(p.zerodowntime ? ["inactive", "blue", "green"] : ["blue"]),
              ].map((s) => ({ value: s, label: instanceLabel(s) }))}
            />
            <Select
              label="Since"
              value={since}
              onValueChange={setSince}
              options={["5m", "30m", "1h", "24h"].map((s) => ({
                value: s,
                label: s,
              }))}
            />
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => void loadLogs()}
            >
              <RefreshCw size={14} />
              Read logs
            </Button>
          </div>
          {logsLoading ? (
            <div
              className="terminal skeleton-terminal"
              role="status"
              aria-label="Loading container logs"
            >
              {["72%", "53%", "84%", "45%", "64%"].map((width, index) => (
                <Skeleton key={index} width={width} height={12} />
              ))}
            </div>
          ) : (
            <pre className="terminal">
              {logs ||
                "Select a service and read its logs. No terminal or shell access is exposed."}
            </pre>
          )}
          {truncated && (
            <p className="warning">Output was truncated by the server.</p>
          )}
          <div className="help-row">
            Log privacy{" "}
            <Help label="log privacy">
              The server redacts retained environment values. Avoid logging
              secrets in your applications.
            </Help>
          </div>
        </section>
      )}
      {tab === "domains" && (
        <section className="panel">
          <div className="section-heading">
            <h2>Domains</h2>
            {!(p.adoption && !p.domains.length) && (
              <Button
                type="button"
                className="button small"
                onClick={() => open({ kind: "routes", project: p })}
              >
                <Settings2 size={14} /> Edit routes
              </Button>
            )}
          </div>
          {dnsLoading && (
            <SkeletonRows count={3} label="Loading project domains" />
          )}
          {p.adoption && !p.domains.length && (
            <RouteSetup
              project={p}
              services={services}
              loading={servicesLoading}
              unavailable={servicesError}
              blocked={Boolean(recoveryJob || projectOperation || status?.busy)}
              preview={preview}
              execute={execute}
              onClose={() => setConfiguringRoute(false)}
              openDeployments={openDeployments}
              refresh={() => setServicesRefresh((n) => n + 1)}
            />
          )}
          {(p.external_domains ?? []).map((hostname) => (
            <div className="domain-row" key={hostname}>
              <Globe2 size={20} />
              <div>
                <strong>{hostname}</strong>
                <p>Current Caddyfile site · kept as is</p>
              </div>
            </div>
          ))}
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
              <Button
                type="button"
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
              </Button>
            </div>
          ))}
        </section>
      )}
    </>
  );
}
export function Jobs({
  jobs,
  projects,
  preview,
  loading,
  refresh,
  report,
}: {
  jobs: Job[];
  projects: Project[];
  preview: boolean;
  loading: boolean;
  refresh: () => Promise<void>;
  report: (e: unknown) => void;
}) {
  const [found, setFound] = useState<Job | null>(null);
  const [lookingUp, setLookingUp] = useState(false);
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>
            Deployments{" "}
            <Help label="tracked deployments">
              The latest operations tracked by this Mac. Paste a job ID in the
              search box to look one up.
            </Help>
          </h1>
        </div>
        <Button
          type="button"
          className="button"
          onClick={() => {
            if (!preview) void refresh();
          }}
        >
          <RefreshCw size={15} />
          Refresh
        </Button>
      </div>
      <section className="panel">
        {loading && <SkeletonRows count={3} label="Loading operations" />}
        {lookingUp && <SkeletonRows count={1} label="Looking up operation" />}
        {!loading && (jobs.length > 0 || found) && (
          <JobsTable
            jobs={[
              ...(found ? [found] : []),
              ...jobs.filter((j) => j.job_id !== found?.job_id),
            ]}
            projects={projects}
            operationLabel={operationLabel}
            projectName={projectName}
            preview={preview}
            refresh={refresh}
            report={report}
            lookup={async (id) => {
              if (preview) return;
              setLookingUp(true);
              try {
                setFound(await api.read<Job>({ kind: "job", job: id }));
              } catch (e) {
                report(e);
              } finally {
                setLookingUp(false);
              }
            }}
          />
        )}
        {!jobs.length && !found && !loading && !lookingUp && (
          <Empty icon={Layers3} title="No deployments yet">
            Deploy a Compose stack to get started.
          </Empty>
        )}
      </section>
    </>
  );
}

function Domains({
  projects,
  inventory,
  loading,
  select,
  open,
}: {
  projects: Project[];
  inventory: Inventory | null;
  loading: boolean;
  select: (s: string) => void;
  open: (m: Modal) => void;
}) {
  if (loading) {
    return (
      <>
        <section className="panel">
          <h2>Managed routes</h2>
          <SkeletonRows label="Loading managed routes" />
        </section>
        <section className="panel">
          <h2>Observed Caddy sites</h2>
          <SkeletonRows label="Loading Caddy sites" />
        </section>
      </>
    );
  }
  return (
    <>
      <section className="panel">
        <h2>Managed routes</h2>
        {projects.flatMap((p) =>
          p.domains.map((d) => (
            <div className="domain-row" key={d}>
              <Globe2 size={20} />
              <div>
                <Button
                  type="button"
                  className="text-button"
                  onClick={() => select(p.id)}
                >
                  {d} <ArrowUpRight size={13} />
                </Button>
                <p>
                  {p.id} · 127.0.0.1:
                  {p.active_slot === "green" ? p.green_port : p.blue_port}
                </p>
              </div>
              <Tag>{labels[p.environment]}</Tag>
              <Button
                type="button"
                className="button small"
                onClick={() => open({ kind: "routes", project: p })}
              >
                Edit route
              </Button>
            </div>
          )),
        )}
        {!projects.length && <Empty title="No managed domains" />}
        {projects
          .filter((p) => p.adoption && !p.domains.length)
          .map((p) => (
            <div className="domain-row" key={p.id}>
              <Globe2 size={20} />
              <div>
                <strong>{p.external_domains?.join(", ") || p.app_id}</strong>
                <p>{p.app_id} · No Dockyard-managed route</p>
              </div>
              <Button
                type="button"
                className="button small"
                onClick={() => select(p.id)}
              >
                Configure route
              </Button>
            </div>
          ))}
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
function ObservedProjectDetail({
  project,
  inventory,
  preview,
  perform,
  back,
  openServerDetails,
}: {
  project: Inventory["projects"][number];
  inventory: Inventory | null;
  preview: boolean;
  perform: (mutation: unknown) => Promise<void>;
  back: () => void;
  openServerDetails: () => void;
}) {
  const [migrationOpen, setMigrationOpen] = useState(false);
  const sites = (inventory?.sites ?? []).filter((site) =>
    site.project_ids.includes(project.id),
  );
  return (
    <>
      <div className="page-heading">
        <div>
          <Button type="button" className="text-button" onClick={back}>
            <ChevronLeft size={15} /> Projects
          </Button>
          <h1>{project.name}</h1>
          <p>/docker/{project.name}</p>
        </div>
        <div className="heading-actions">
          <Tag>Existing Compose · read-only</Tag>
          <Button
            type="button"
            className="button primary"
            onClick={() => setMigrationOpen(true)}
          >
            Migrate Now <ArrowRight size={16} />
          </Button>
        </div>
      </div>
      <div className="alert pending">
        Imported from VPS · read-only until migrated.
      </div>
      <div className="detail-meta">
        <Tag tone={project.present ? "green" : "red"}>
          {project.present ? "Folder present" : "Folder missing"}
        </Tag>
        <span>Production</span>
        <span>Observed {ago(inventory?.projects_observed_at)}</span>
      </div>
      <section className="panel">
        <div className="section-heading">
          <h2>Compose services</h2>
          <span className="count">{project.services.length}</span>
        </div>
        <p className="muted">
          {project.compose_files.join(" · ") || "Compose filename unavailable"}
        </p>
        {project.services.map((service) => (
          <div className="inventory-row" key={service.name}>
            <span className="job-icon">
              <Box size={17} />
            </span>
            <div>
              <strong>{service.name}</strong>
              {service.image && <p>{service.image}</p>}
            </div>
            <span className="mono">
              {service.published_ports.length
                ? service.published_ports.map((port) => `:${port}`).join(" · ")
                : "No published port"}
            </span>
          </div>
        ))}
        {!project.services.length && (
          <Empty title="Service metadata unavailable" />
        )}
      </section>
      <section className="panel">
        <div className="section-heading">
          <h2>Linked Caddy routes</h2>
        </div>
        {sites.map((site) => (
          <div className="inventory-row" key={site.host_matcher}>
            <Globe2 size={17} />
            <div>
              <strong>{site.host_matcher}</strong>
              <p>{site.upstreams.join(" · ")}</p>
            </div>
          </div>
        ))}
        {!sites.length && <p className="muted">No linked route found.</p>}
      </section>
      {project.warnings.length > 0 && (
        <div className="alert pending">
          Some metadata could not be read: {project.warnings.join(", ")}.
        </div>
      )}
      {migrationOpen && (
        <MigrationReview
          project={project}
          preview={preview}
          perform={perform}
          close={() => setMigrationOpen(false)}
          openServerDetails={() => {
            setMigrationOpen(false);
            openServerDetails();
          }}
        />
      )}
    </>
  );
}
const migrationLabels: Record<string, string> = {
  INVENTORY_FRESH: "Refresh VPS inventory and resolve scan warnings.",
  SOURCE_PRESENT: "Restore the project folder before migrating.",
  SOURCE_METADATA_COMPLETE: "Resolve Compose metadata warnings.",
  SINGLE_COMPOSE_FILE:
    "Multiple Compose files or overrides need a reviewed merge.",
  PROJECT_ID_SUPPORTED: "Project name must fit Dockyard's naming rules.",
  NO_MANUAL_CADDY_CUTOVER:
    "A live Caddy route needs a reviewed traffic switch.",
  NO_EXISTING_PUBLISHED_PORTS:
    "A live published port needs a replacement port and traffic switch.",
  APPROVED_TEMPLATES_CONFIGURED:
    "This VPS needs approved deployment templates before it can manage this stack.",
  SOURCE_FILE_TRUSTED:
    "The Compose file and its parent folders need safe root ownership.",
  SOURCE_FILE_READABLE: "The Compose file could not be read safely.",
  APP_TEMPLATE_UNAMBIGUOUS:
    "The app image must match exactly one approved template and use a digest.",
  COMPOSE_POLICY_COMPATIBLE:
    "Convert unsupported Compose settings to Dockyard's secure subset.",
  STATELESS_STACK: "Persistent volumes need a separate data migration plan.",
};
function migrationCheckError(error: string) {
  if (error.includes("HTTP_404: NOT_FOUND")) {
    return "The Dockyard service running on this VPS does not have the migration-check endpoint. Update the VPS service to enable this check. No project files or containers were changed.";
  }
  if (error.includes("HTTP_404: PROJECT_NOT_FOUND")) {
    return "This project is no longer in the VPS inventory. Refresh the inventory and try again.";
  }
  if (error.includes("SESSION_LOCKED")) {
    return "Your session locked. Unlock with Touch ID, then try again.";
  }
  return "The migration check could not reach or read the VPS service. Try again after checking the connection.";
}
function MigrationReview({
  project,
  preview,
  close,
  openServerDetails,
  perform,
}: {
  project: Inventory["projects"][number];
  preview: boolean;
  close: () => void;
  openServerDetails: () => void;
  perform: (mutation: unknown) => Promise<void>;
}) {
  const [assessment, setAssessment] = useState<MigrationAssessment | null>(
    null,
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (preview) return;
    let active = true;
    setAssessment(null);
    setError("");
    api
      .read<MigrationAssessment>({ kind: "migration", project: project.id })
      .then((value) => {
        if (active) setAssessment(value);
      })
      .catch((cause) => {
        if (active) setError(String(cause));
      });
    return () => {
      active = false;
    };
  }, [preview, project.id, revision]);
  const messages: Record<string, string> = {
    PROJECT_DIRECTORY_EXISTS:
      "The snapshot folder already exists. Check that folder on the VPS before retrying; Dockyard will not overwrite it.",
    APP_ENVIRONMENT_EXISTS:
      "This application's production environment is already managed. Open that project instead.",
    PROJECT_EXISTS:
      "This project is already managed. Close this dialog and refresh Projects.",
    RECOVERY_REQUIRED:
      "A previous server operation needs recovery. Review its details in Deployments before migrating.",
    MIGRATION_UNAVAILABLE:
      "Update the VPS service from Server details to enable adoption.",
    MIGRATION_CONTAINERS_NOT_FOUND:
      "No Compose containers were found for this folder. Start the existing stack, then check again.",
    MIGRATION_COMPOSE_IDENTITY_UNKNOWN:
      "The containers do not identify one Compose project and folder. Review their Compose labels before adoption.",
    MIGRATION_SERVICE_MISMATCH:
      "The Compose file and existing services differ. Reconcile the original stack, then check again.",
    MIGRATION_SOURCE_UNSAFE:
      "A Compose file is linked, outside the project folder, or too large. Use regular files inside the project folder.",
    MIGRATION_SOURCE_UNREADABLE:
      "Dockyard could not read the existing Compose files. Check their location and access on the VPS.",
    COMPOSE_VALIDATION_FAILED:
      "Docker could not validate the existing Compose configuration. Check its YAML and required environment values.",
    DOCKER_UNAVAILABLE:
      "Docker could not be reached. Check the Docker service in Server details.",
    PROJECT_ID_SUPPORTED:
      "The project folder name must use lowercase letters, numbers and hyphens, starting with a letter.",
  };
  const blocked =
    assessment?.checks.filter((c) => c.status === "blocked") ?? [];
  const oldServer = !!assessment && assessment.strategy !== "adopt_in_place";
  return (
    <div className="modal-backdrop">
      <section
        className="modal wide migration-modal operation-modal"
        role="dialog"
        aria-modal="true"
        aria-label={`Migrate ${project.name}`}
      >
        <div className="modal-heading">
          <div>
            <span className="eyebrow">Existing project</span>
            <h2>Manage {project.name} with Dockyard</h2>
          </div>
          <Button
            type="button"
            className="icon-button"
            disabled={busy}
            aria-label="Close"
            onClick={close}
          >
            <X size={19} />
          </Button>
        </div>
        <div className="wizard-body">
          <p>
            Keep the current containers, volumes, ports and Caddy routes.
            Dockyard will save a configuration snapshot and manage this Compose
            project in place.
          </p>
          {preview && (
            <div className="alert pending">
              Connect to your VPS to check this project.
            </div>
          )}
          {error && (
            <div className="alert error" role="alert">
              {error.includes("MIGRATION_SOURCE_CHANGED")
                ? "The stack changed since this review. Check again before migrating."
                : error.includes("SCOPE_REQUIRED") ||
                    error.includes("COMPOSE_ACCESS_REQUIRED")
                  ? "Enable Compose management in Server details, then try again."
                  : (messages[error.split(": ").at(-1) ?? ""] ??
                    migrationCheckError(error))}
            </div>
          )}
          {!preview && !error && !assessment && (
            <SkeletonRows count={3} label="Checking existing stack" />
          )}
          {oldServer && (
            <div className="alert pending">
              Update the VPS service to version 0.4.0 or later to migrate
              existing projects.
            </div>
          )}
          {!oldServer && assessment && (
            <>
              {assessment.execution_available ? (
                <div className="migration-result">
                  <strong>
                    Ready to migrate · {assessment.service_count} services
                  </strong>
                  <Check size={20} />
                </div>
              ) : (
                blocked.map((check) => (
                  <div className="alert pending" key={check.code}>
                    {messages[check.code] ??
                      migrationLabels[check.code] ??
                      "The stack could not be verified. Check the server connection and try again."}
                  </div>
                ))
              )}
              <p>
                After migration: deploy Compose, start, stop, restart and view
                logs. Existing Caddy routes stay in their current file.
                Redeploys use a single instance and may briefly interrupt
                service.
              </p>
            </>
          )}
        </div>
        <div className="modal-footer">
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={close}
          >
            Close
          </Button>
          {assessment?.execution_available &&
          assessment.source_sha256 &&
          !oldServer ? (
            <Button
              type="button"
              className="button primary"
              disabled={busy || preview || !!error}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await perform({
                    action: "migrate",
                    project: project.id,
                    source_sha256: assessment.source_sha256,
                  });
                  close();
                } catch (e) {
                  setError(String(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Fingerprint size={18} />
              {busy ? "Migrating…" : "Migrate with Touch ID"}
            </Button>
          ) : null}
          {(oldServer ||
            error.includes("HTTP_404") ||
            error.includes("SCOPE_REQUIRED") ||
            error.includes("COMPOSE_ACCESS_REQUIRED")) && (
            <Button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={openServerDetails}
            >
              Server details <ArrowRight size={15} />
            </Button>
          )}
          {!preview && (error || (!oldServer && blocked.length > 0)) && (
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setRevision((n) => n + 1)}
            >
              Check again <RefreshCw size={15} />
            </Button>
          )}
        </div>
      </section>
    </div>
  );
}
function InventoryView({
  inventory: i,
  loading,
  initialLoading,
  refresh,
}: {
  inventory: Inventory | null;
  loading: boolean;
  initialLoading: boolean;
  refresh: () => void;
}) {
  if (initialLoading) {
    return (
      <>
        <div className="page-heading">
          <h1>VPS inventory</h1>
        </div>
        <SkeletonStats />
        <section className="panel">
          <h2>Observed projects</h2>
          <SkeletonRows count={5} label="Loading VPS inventory" />
        </section>
      </>
    );
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>
            VPS inventory{" "}
            <Help label="VPS inventory">
              Existing /docker projects and Caddy sites are synced as production
              inventory. Import preserves their files and containers. Migration
              is required before Dockyard can deploy or control them. Secrets
              are excluded.
            </Help>
          </h1>
        </div>
        <Button
          type="button"
          className="button"
          disabled={loading}
          onClick={refresh}
        >
          <RefreshCw size={15} />
          Read inventory
        </Button>
      </div>
      <div className="stats-grid three">
        <Stat
          label="Observed projects"
          value={i?.projects.length ?? 0}
          icon={FolderGit2}
          note={`Last observed ${ago(i?.projects_observed_at)}`}
        />
        <Stat
          label="Caddy sites"
          value={i?.sites.length ?? 0}
          icon={Globe2}
          note={`Last observed ${ago(i?.caddy_observed_at)}`}
        />
        <Stat
          label="Reserved ports"
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
            Refresh after connecting your server.
          </Empty>
        )}
      </section>
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
          <h1>
            Audit trail{" "}
            <Help label="audit trail">
              Server audit events include the actor and request ID. Viewing them
              requires permission to read all projects.
            </Help>
          </h1>
        </div>
        <Button
          type="button"
          className="button"
          disabled={preview || busy}
          onClick={() => void load(true)}
        >
          <RefreshCw size={15} />
          Read from start
        </Button>
      </div>
      <section className="panel">
        {events.map((e) => {
          const field = (key: string, fallback = "") =>
            typeof e.event[key] === "string"
              ? (e.event[key] as string)
              : fallback;
          return (
            <div className="audit-event" key={e.event_id}>
              <Tag>#{e.event_id}</Tag>
              <div className="audit-summary">
                <strong>
                  {field("Action", "Event")} · {field("ProjectID", "Server")}
                </strong>
                <p>
                  {field("ActorID", "Unknown actor")} · {ago(field("Time"))}
                </p>
                {field("Code") && <p className="warning">{field("Code")}</p>}
                <details className="connection-details">
                  <summary>Event details</summary>
                  <pre>{JSON.stringify(e.event, null, 2)}</pre>
                </details>
              </div>
              <Tag tone={field("Status") === "succeeded" ? "green" : "neutral"}>
                {field("Status", "Recorded")}
              </Tag>
            </div>
          );
        })}
        {busy && events.length === 0 && (
          <SkeletonRows count={5} label="Loading audit trail" />
        )}
        {events.length === 0 && !busy && (
          <Empty
            icon={History}
            title={
              preview
                ? "Audit events appear after connecting"
                : "No audit events loaded"
            }
          />
        )}
        {events.length > 0 && more && (
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => void load()}
          >
            Load next 100
          </Button>
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
  openOperations,
}: {
  profile: Profile | null;
  unlocked: boolean;
  native: boolean;
  busy: boolean;
  authenticate: (m: "enroll" | "unlock") => void;
  setup: () => void;
  explore: () => void;
  forget: () => void;
  openOperations: () => void;
}) {
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>
            Server details{" "}
            <Help label="connection security">
              Access uses this Mac’s dedicated credentials. Revoke other client
              keys on the VPS to limit access. A compromised Mac or copied
              credentials can impersonate a client. Production builds should be
              signed and notarized.
            </Help>
          </h1>
        </div>
        <Tag tone={unlocked ? "green" : "neutral"}>
          <LockKeyhole size={14} />
          {unlocked ? "Unlocked" : "Locked"}
        </Tag>
      </div>
      <ServerDetailsTabs
        key={
          p
            ? `${p.server_id}:${p.key_id}:${p.server_certificate_sha256}`
            : "unenrolled"
        }
        connection={
          <section className="panel enrollment-panel">
            <div className="server-card-header">
              <div className="security-symbol">
                <ShieldCheck size={28} />
              </div>
              <div>
                <h2>
                  {p?.name ?? "Connect your VPS"}
                  <Help label="enrollment">
                    Set up a new Ubuntu VPS, or import an enrollment for an
                    existing Dockyard server. Credentials are stored in macOS
                    Keychain. Removing local enrollment does not revoke its key
                    on the VPS.
                  </Help>
                </h2>
                {p?.server_ip && (
                  <p className="mono server-address">
                    {p.server_ip}:{p.ssh_port}
                  </p>
                )}
              </div>
            </div>
            {p && (
              <dl className="facts">
                <dt>API origin</dt>
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
                    <dt>SSH fingerprint</dt>
                    <dd className="mono">{p.ssh_fingerprint}</dd>
                  </>
                )}
              </dl>
            )}
            <div className="controls">
              {!p && (
                <Button
                  type="button"
                  className="button primary"
                  disabled={busy}
                  onClick={setup}
                >
                  <Server size={15} />
                  Set up your server
                </Button>
              )}
              <Button
                type="button"
                className={p ? "button" : "button primary"}
                disabled={!native || busy}
                onClick={() => authenticate("enroll")}
              >
                <Plus size={15} />
                Import existing enrollment
              </Button>
              {!unlocked && (
                <Button
                  type="button"
                  className="button"
                  disabled={!native || busy}
                  onClick={() => authenticate("unlock")}
                >
                  <Fingerprint size={15} />
                  Unlock with Touch ID
                </Button>
              )}
            </div>
            {!native && (
              <p className="muted">Preview · use the Mac app to connect.</p>
            )}
            <div className="enrollment-secondary-actions">
              {p && native && (
                <Button
                  type="button"
                  className="text-button danger-text"
                  disabled={busy}
                  onClick={forget}
                >
                  Remove local enrollment
                </Button>
              )}
              {!p && (
                <Button type="button" className="text-button" onClick={explore}>
                  Explore sample workspace <ArrowRight size={14} />
                </Button>
              )}
            </div>
          </section>
        }
        security={
          <section className="panel security-list">
            <h2>Layered access controls</h2>
            {[
              {
                icon: ShieldCheck,
                title: "Client certificate",
                text: "The server checks the client CA and certificate fingerprint. Rust verifies the enrolled CA, SAN, expiry and exact server certificate pin.",
              },
              {
                icon: Code2,
                title: "Signed requests",
                text: "HMAC includes the server, actor, scope, exact route and body hash. The server enforces scopes and project permissions.",
              },
              {
                icon: LockKeyhole,
                title: "Touch ID & Keychain",
                text: "Touch ID unlocks the app. It stays unlocked while active and locks after five minutes away. Sensitive changes require a fresh scan.",
              },
              {
                icon: History,
                title: "Safe retries",
                text: "Exact payloads and retry IDs are saved in Keychain before submission. A pending write blocks another write.",
              },
              {
                icon: Server,
                title: "Private connection",
                text: "API access uses a private connection and restricted SSH tunnel. Terminal access uses a separate root SSH connection. Touch ID is requested when your last Dockyard verification is more than two minutes old.",
              },
            ].map((s) => (
              <div className="security-item" key={s.title}>
                <s.icon size={20} />
                <div>
                  <h3>
                    {s.title}
                    <Help label={s.title}>{s.text}</Help>
                  </h3>
                </div>
                <Check size={15} />
              </div>
            ))}
          </section>
        }
        access={
          p?.server_ip && native && unlocked ? (
            <ServerAccess />
          ) : (
            <section className="panel">
              <h2>Required server access</h2>
              <p className="muted">
                {!native
                  ? "Use the Mac app to check server access and deployment health."
                  : !p?.server_ip
                    ? "Connect an enrolled VPS to check server access and deployment health."
                    : "Unlock with Touch ID from the Connection tab to run server checks."}
              </p>
            </section>
          )
        }
        updates={
          p?.server_ip && native && unlocked ? (
            <ServerUpdater openOperations={openOperations} />
          ) : (
            <section className="panel">
              <h2>Server updates</h2>
              <p className="muted">
                {!native
                  ? "Use the Mac app to check for server updates."
                  : !p?.server_ip
                    ? "Connect an enrolled VPS to check for updates."
                    : "Unlock with Touch ID from the Connection tab to check for updates."}
              </p>
            </section>
          )
        }
      />
    </>
  );
}
function ServerAccess() {
  const [report, setReport] = useState<api.ServerAccessReport | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [checkedAt, setCheckedAt] = useState<Date | null>(null);
  async function check() {
    setBusy(true);
    setError("");
    setReport(null);
    try {
      setReport(await api.checkServerAccess());
      setCheckedAt(new Date());
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  async function prepare() {
    setBusy(true);
    setError("");
    try {
      const next = await api.prepareServerAccess();
      setReport(next);
      setCheckedAt(new Date());
      if (next.update_directory === "ready")
        toast.success("Updater access is ready.");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  const rows = report
    ? ([
        [
          "Root SSH",
          report.root_ssh,
          "Full server access for this Mac's pinned SSH connection.",
        ],
        [
          "Updater folder",
          report.update_directory === "ready",
          "A private, root-owned directory holds temporary update files.",
        ],
        [
          "Installed binaries",
          report.installed_binaries,
          "Dockyard binaries must be root-owned and executable.",
        ],
        [
          "SQLite database",
          report.database,
          "The updater needs access to back up the Dockyard database.",
        ],
        [
          "Dockyard service",
          report.service_active,
          "The service must be running before an update.",
        ],
      ] as const)
    : [];
  return (
    <section className="panel server-access">
      <div className="server-updater-heading">
        <div>
          <h2>Required server access</h2>
          <p className="muted">
            Check permissions, routes, and deployment readiness before updating
            services.
          </p>
        </div>
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={() => void check()}
        >
          <RefreshCw size={16} />
          {busy ? "Checking…" : "Check access"}
        </Button>
      </div>
      {busy && (
        <p className="muted" role="status">
          Checking the server over its pinned SSH connection… This may take up
          to a minute.
        </p>
      )}
      {!busy && report && checkedAt && (
        <p className="muted server-access-checked">
          Checked at {checkedAt.toLocaleTimeString()} · Run again after changing
          server settings.
        </p>
      )}
      <div className="server-access-row">
        <span>
          Compose management{" "}
          <Help label="Compose management">
            Allows this Mac to deploy Compose stacks with full server
            privileges. Update the server before enabling access. Dockyard
            restarts briefly.
          </Help>
        </span>
        {report?.checks?.find((check) => check.id === "compose_access")
          ?.status === "ready" ? (
          <Tag tone="green">Enabled</Tag>
        ) : (
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                await api.enableComposeManagement();
                setReport(await api.checkServerAccess());
                setCheckedAt(new Date());
                toast.success("Compose management enabled.");
              } catch (e) {
                setError(String(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Enable with Touch ID
          </Button>
        )}
      </div>
      {report && (
        <>
          <ServerDiagnostics report={report} />
          <h3 className="server-access-updater-heading">Updater access</h3>
          <div className="server-access-grid">
            {rows.map(([label, allowed, detail]) => (
              <div className="server-access-row" key={label}>
                <span>
                  {label} <Help label={label}>{detail}</Help>
                </span>
                <Tag tone={allowed ? "green" : "neutral"}>
                  {allowed
                    ? "Ready"
                    : label === "Updater folder" &&
                        report.update_directory === "can_prepare"
                      ? "Needs setup"
                      : "Needs review"}
                </Tag>
              </div>
            ))}
          </div>
          {report.update_directory === "can_prepare" && (
            <Button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={() => void prepare()}
            >
              Prepare updater access with Touch ID
            </Button>
          )}
          {report.update_directory === "manual_review" && (
            <p className="muted">
              Review ownership and permissions for
              /var/lib/dockyard-desktop-updates on the VPS.
            </p>
          )}
          {(!report.installed_binaries ||
            !report.database ||
            !report.service_active) && (
            <p className="muted">
              Review the flagged VPS items before updating. Dockyard will not
              change binaries, database permissions, or services from this
              check.
            </p>
          )}
        </>
      )}
      {error && (
        <div className="alert error" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
function composeError(error: unknown): string {
  const message = String(error);
  const explanations: Record<string, string> = {
    ADOPTED_ROUTES_PRESERVED:
      "This project keeps its existing Caddy routes. Update those routes in the server Caddyfile.",
    SCOPE_REQUIRED:
      "This Mac does not have permission for this project. Enable Compose management in Server details to allow access.",
    COMPOSE_ACCESS_REQUIRED:
      "Enable Compose management in Server details first.",
    COMPOSE_VALIDATION_FAILED:
      "Docker Compose could not validate this stack. Check YAML, required .env values, and referenced files on the VPS.",
    COMPOSE_INVALID:
      "Docker Compose could not validate the resolved configuration. Check the Compose file and referenced files on the VPS.",
    COMPOSE_ROUTE_SERVICE_MISSING:
      "The web service chosen for Caddy is missing from this Compose file.",
    COMPOSE_ROUTE_NOT_CONFIGURED:
      "Choose which Compose service receives this domain's traffic.",
    COMPOSE_ROUTE_INVALID:
      "Choose a web service and its container port when assigning domains.",
    COMPOSE_BLUE_GREEN_INCOMPATIBLE:
      "This stack cannot run two independent copies. Use a single instance for shared storage, host ports or fixed container names.",
    COMPOSE_BLUE_GREEN_HEALTHCHECK_REQUIRED:
      "Add a healthcheck to every service for seamless updates deployment, or use a single instance.",
    COMPOSE_NO_ACTIVE_SERVICES:
      "No active services were found. Check your Compose profiles and .env settings.",
  };
  return (
    Object.entries(explanations).find(([code]) =>
      message.includes(code),
    )?.[1] ?? message
  );
}
function OperationModal({
  modal: m,
  projects,
  observedIDs,
  preview,
  close,
  perform,
  report,
}: {
  modal: NonNullable<Modal>;
  projects: Project[];
  observedIDs: string[];
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
  const [existingApp, setExistingApp] = useState(
    m.kind === "create" ? (m.appID ?? "") : "",
  );
  const [projectName, setProjectName] = useState("");
  const [nameConflicts, setNameConflicts] = useState<
    Pick<Project, "id" | "app_id" | "environment">[]
  >([]);
  const identity = existingApp
    ? flavorIdentity(
        existingApp,
        env,
        [...projects, ...nameConflicts],
        observedIDs,
      )
    : projectIdentity(
        projectName,
        env,
        [...projects, ...nameConflicts],
        observedIDs,
      );
  const [routeService, setRouteService] = useState("web");
  const [routePort, setRoutePort] = useState("80");
  // A Compose project without a web service: connecting a domain picks the
  // service and redeploys, because the port binding is part of the release.
  const connecting =
    m.kind === "routes" &&
    p?.mode === "compose" &&
    !p.route_service &&
    !p.published_route &&
    !(p.adoption && !p.domains.length);
  const [routeServices, setRouteServices] = useState<Service[]>([]);
  useEffect(() => {
    if (!connecting || preview || !p) return;
    void api
      .read<{ services: Service[] }>({
        kind: "project",
        project: p.id,
        view: "services",
      })
      .then((d) => {
        const items = (d.services ?? []).filter(
          (s, i, all) => all.findIndex((x) => x.name === s.name) === i,
        );
        setRouteServices(items);
        const web =
          items.find((s) => s.container_ports?.length) ?? items[0];
        if (web) {
          setRouteService(web.name);
          setRoutePort(String(web.container_ports?.[0] ?? ""));
        }
      })
      .catch(() => setRouteServices([]));
  }, [connecting, preview, p?.id]);
  const [domains, setDomains] = useState(p?.domains.join("\n") ?? "");
  const [blue, setBlue] = useState("");
  const [green, setGreen] = useState("");
  const zero = false;
  const [compose, setCompose] = useState("");
  const [variables, setVariables] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [release, setRelease] = useState(p?.releases.at(-1)?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [accessRequired, setAccessRequired] = useState(false);
  const [suggestion, setSuggestion] = useState<number>();
  const name =
    m.kind === "create"
      ? existingApp
        ? "Add project flavor"
        : "New project"
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
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (preview) {
      setError("Preview is read-only. Connect your VPS to submit operations.");
      return;
    }
    if (
      m.kind === "deploy" &&
      (!compose.trim() ||
        lintCompose(compose).some((p) => p.severity === "error"))
    ) {
      setError(
        "Enter Compose configuration and fix the highlighted syntax errors.",
      );
      return;
    }
    setBusy(true);
    setError("");
    try {
      let mutation: unknown;
      if (m.kind === "create") {
        if (!identity)
          throw new Error("Enter a project name and choose an environment.");
        mutation = {
          action: "create",
          data: {
            id: identity.id,
            app_id: identity.app_id,
            environment: env,
            ...(domains.trim()
              ? { route_service: routeService, route_port: Number(routePort) }
              : {}),
            domains: domains
              .split(/[\n,]+/)
              .map((s) => s.trim())
              .filter(Boolean),
            zerodowntime: env === "production" && zero && !!domains.trim(),
            ...(blue && domains.trim() ? { port: Number(blue) } : {}),
            ...(env === "production" && zero && domains.trim() && green
              ? { secondary_port: Number(green) }
              : {}),
          },
        };
      } else if (m.kind === "deploy") {
        if (!target)
          throw new Error(
            "Create this application environment before deploying.",
          );
        let vars: unknown;
        if (target.mode !== "compose" && variables.trim()) {
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
            ...(target.mode === "compose"
              ? variables
                ? { env_file: variables }
                : {}
              : vars !== undefined
                ? { variables: vars }
                : {}),
          },
        };
      } else if (connecting) {
        const list = domains
          .split(/[\n,]+/)
          .map((s) => s.trim())
          .filter(Boolean);
        if (!list.length) throw new Error("Enter a domain to connect.");
        const config = await api.read<{
          release_id: string;
          compose_yaml: string;
        }>({ kind: "configuration", project: p!.id });
        if (!config.compose_yaml.trim())
          throw new Error(
            "Add this project's Compose file in Configuration first.",
          );
        const env = await api.readEnv(p!.id);
        mutation = {
          action: "deploy",
          project: p!.id,
          data: {
            environment: p!.environment,
            compose_yaml: config.compose_yaml,
            env_file: env.env_file,
            expected_release_id: config.release_id,
            domains: list,
            route_service: routeService,
            route_port: Number(routePort),
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
            ...(blue && domains.trim() ? { port: Number(blue) } : {}),
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
      if (
        m.kind === "create" &&
        /SCOPE_REQUIRED|COMPOSE_ACCESS_REQUIRED/.test(String(e))
      ) {
        setAccessRequired(true);
        setError(
          "This Mac is not allowed to create projects on this server yet.",
        );
      } else if (
        m.kind === "create" &&
        identity &&
        env &&
        /APP_ENVIRONMENT_EXISTS|PROJECT_EXISTS|PROJECT_DIRECTORY_EXISTS/.test(
          String(e),
        )
      ) {
        setNameConflicts((items) => [
          ...items,
          { ...identity, environment: env },
        ]);
        setError(
          existingApp
            ? "This flavor or folder was just created on the server. Refresh projects before trying again."
            : "That name was just taken on the server. A new available name is ready below; review and submit again.",
        );
      } else setError(composeError(e));
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    function key(e: KeyboardEvent) {
      if (e.key === "Escape" && !e.defaultPrevented && !busy) close();
    }
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [busy, close]);
  return (
    <div className="modal-backdrop">
      <section
        className={`modal operation-modal ${m.kind === "deploy" ? "wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
      >
        <div className="modal-heading">
          <div>
            <div className="eyebrow">
              {m.kind === "create"
                ? "Dockyard"
                : p?.app_id}
            </div>
            <h2 id="modal-title">{name}</h2>
          </div>
          <Button
            type="button"
            className="icon-button"
            aria-label="Close dialog"
            disabled={busy}
            onClick={close}
          >
            <X size={20} />
          </Button>
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
            {m.kind === "create" && (
              <Select
                label="Project"
                value={existingApp}
                onValueChange={(value) => {
                  setExistingApp(value);
                  setEnv("");
                }}
                options={[
                  { value: "", label: "Create a new project" },
                  ...[...new Set(projects.map((p) => p.app_id))]
                    .sort()
                    .map((app) => ({
                      value: app,
                      label: `${app} · add a flavor`,
                    })),
                ]}
              />
            )}
            <h3>
              Flavor / environment{" "}
              <Help label="environments">
                Each app has separate development, staging and production
                environments. Seamless updates deployment is available only for
                stateless production stacks. Development and staging use one
                instance.
              </Help>
            </h3>
            <div className="environment-options">
              {environments.map((e) => (
                <Button
                  type="button"
                  key={e}
                  className={env === e ? "chosen" : ""}
                  disabled={
                    m.kind === "create" &&
                    !!existingApp &&
                    projects.some(
                      (p) => p.app_id === existingApp && p.environment === e,
                    )
                  }
                  onClick={() => choose(e)}
                >
                  <span className={`env-option-icon ${e}`}>
                    <Layers3 size={21} />
                  </span>
                  <span>
                    <strong>{labels[e]}</strong>
                    <small>
                      {m.kind === "create" &&
                      existingApp &&
                      projects.some(
                        (p) => p.app_id === existingApp && p.environment === e,
                      )
                        ? "Already created"
                        : e === "production"
                          ? "Seamless updates available"
                          : "Single instance"}
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
                </Button>
              ))}
            </div>
            {m.kind === "create" && (
              <p className="muted">
                Each flavor has its own services, .env, Compose file, ports and
                Caddy domains. Supply the files for this flavor after creating
                it.
              </p>
            )}
            <div className="modal-footer">
              <span />
              <Button
                type="button"
                className="button primary"
                disabled={!env || (m.kind === "deploy" && !target)}
                onClick={() => setStep(1)}
              >
                Continue <ArrowRight size={15} />
              </Button>
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
              {accessRequired && m.kind === "create" && (
                <div className="project-access-recovery">
                  <Button
                    type="button"
                    className="button primary"
                    disabled={busy || preview}
                    onClick={async () => {
                      setBusy(true);
                      try {
                        await api.enableComposeManagement();
                        setAccessRequired(false);
                        setError("");
                        toast.success(
                          "Access enabled. You can submit this project now.",
                        );
                      } catch (e) {
                        setError(String(e));
                      } finally {
                        setBusy(false);
                      }
                    }}
                  >
                    <Fingerprint size={18} /> Enable access with Touch ID
                  </Button>
                  <Help label="enable project access">
                    Grants this Mac full Compose management on the server using
                    root SSH. Dockyard restarts briefly; running containers stay
                    up.
                  </Help>
                </div>
              )}
              {m.kind === "create" && !existingApp && (
                <>
                  <label htmlFor="project-name">
                    Project name
                    <input
                      id="project-name"
                      required
                      maxLength={120}
                      placeholder="e.g. Payments"
                      autoComplete="off"
                      value={projectName}
                      onChange={(e) => setProjectName(e.target.value)}
                      aria-describedby={
                        identity ? "project-name-preview" : undefined
                      }
                    />
                    {identity && (
                      <small
                        id="project-name-preview"
                        className="project-name-preview"
                        aria-live="polite"
                      >
                        {identity.adjusted
                          ? "Name already in use. Will create "
                          : "Will create "}
                        <strong>{identity.id}</strong>
                      </small>
                    )}
                  </label>
                </>
              )}
              {m.kind === "create" && existingApp && (
                <p>
                  Project <strong>{existingApp}</strong> · {env && labels[env]}
                </p>
              )}
              {m.kind === "create" && identity && (
                <p className="muted">
                  Files: <code>/docker/{identity.id}/compose.yml</code> and{" "}
                  <code>.env</code>
                </p>
              )}
              {(m.kind === "create" || m.kind === "routes") && (
                <>
                  <label htmlFor="project-domains">
                    <span className="field-title">
                      Domains
                      <Help label="project domains">
                        Optional: enter one domain per line to route traffic
                        through Caddy. Without domains, Compose controls
                        published ports.
                      </Help>
                    </span>
                    <textarea
                      id="project-domains"
                      required={p?.mode !== "compose" && m.kind === "routes"}
                      rows={3}
                      placeholder="app.example.com"
                      value={domains}
                      onChange={(e) => setDomains(e.target.value)}
                    />
                  </label>
                  {domains.trim() && (m.kind === "create" || connecting) && (
                    <div className="form-grid">
                      {connecting && routeServices.length > 0 ? (
                        <Select
                          label="Web service"
                          value={routeService}
                          options={routeServices.map((s) => ({
                            value: s.name,
                            label: s.name,
                          }))}
                          onValueChange={(name) => {
                            setRouteService(name);
                            const port = routeServices.find(
                              (s) => s.name === name,
                            )?.container_ports?.[0];
                            if (port) setRoutePort(String(port));
                          }}
                        />
                      ) : (
                        <label>
                          Web service
                          <input
                            required
                            value={routeService}
                            onChange={(e) => setRouteService(e.target.value)}
                            placeholder="web"
                          />
                        </label>
                      )}
                      <label>
                        Container port
                        <input
                          required
                          type="number"
                          min={1}
                          max={65535}
                          value={routePort}
                          onChange={(e) => setRoutePort(e.target.value)}
                        />
                      </label>
                    </div>
                  )}
                  {connecting && domains.trim() && (
                    <p className="configuration-note">
                      Connecting a domain redeploys {p?.app_id} so the web
                      service&apos;s port is published for Caddy. A host port
                      is chosen automatically.
                    </p>
                  )}
                  {domains.trim() && !connecting && (
                    <>
                      {" "}
                      <div className="form-grid">
                        <label>
                          Primary port <small>(optional)</small>
                          <input
                            type="number"
                            disabled={
                              m.kind === "routes" &&
                              (!!p?.published_route ||
                                (!!p?.route_service &&
                                  !!p.service_instances?.[p.route_service]
                                    ?.port))
                            }
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
                                p
                                  ? String(p.green_port)
                                  : "Allocate automatically"
                              }
                              value={green}
                              onChange={(e) => setGreen(e.target.value)}
                            />
                          </label>
                        )}
                      </div>
                      <Button
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
                      </Button>
                      {suggestion && (
                        <p className="muted">
                          Available port: {suggestion}
                          <Help label="port availability">
                            This is a suggestion. The agent reserves the port
                            when accepting the job.
                          </Help>
                        </p>
                      )}
                    </>
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
                    <span className="field-title">
                      <label htmlFor="compose">Docker Compose YAML</label>
                      <Help label="Compose requirements">
                        {target?.mode === "compose"
                          ? "Docker Compose validates your stack. Referenced build contexts and files must already exist on the VPS. Seamless updates requires isolated services with health checks."
                          : "This older project uses its existing server template policy."}
                      </Help>
                    </span>
                    <Button
                      type="button"
                      className="button small"
                      disabled={preview || busy}
                      onClick={async () => {
                        try {
                          const yaml = await api.importCompose();
                          if (yaml) setCompose(composeForEditor(yaml));
                        } catch (e) {
                          setError(String(e));
                        }
                      }}
                    >
                      <Upload size={14} />
                      Import file
                    </Button>
                  </div>
                  <CodeEditor
                    value={compose}
                    onChange={setCompose}
                    readOnly={preview || busy}
                  />
                  <div className="editor-meta">
                    <span>
                      {new TextEncoder()
                        .encode(compose)
                        .length.toLocaleString()}{" "}
                      / 65,536 bytes
                    </span>
                  </div>
                  <label htmlFor="app-variables">
                    <span className="field-title">
                      {target?.mode === "compose"
                        ? ".env"
                        : "Environment variables (JSON)"}
                      <Help label="environment variables">
                        {target?.mode === "compose"
                          ? "Paste .env contents, or leave blank to reuse saved values. For a new project, blank means no values. Enter # empty to replace the saved file with an empty environment."
                          : "Leave blank to reuse the current snapshot. Enter {} for an empty environment."}
                      </Help>
                    </span>
                    {target?.mode === "compose" ? (
                      <CodeEditor
                        kind="env"
                        value={variables}
                        onChange={setVariables}
                        readOnly={preview || busy}
                      />
                    ) : (
                      <textarea
                        id="app-variables"
                        className="code-editor short"
                        rows={3}
                        spellCheck={false}
                        placeholder={
                          target?.mode === "compose"
                            ? "APP_URL=https://app.example.com"
                            : '{"APP_URL": "https://app.example.com"}'
                        }
                        value={variables}
                        onChange={(e) => setVariables(e.target.value)}
                      />
                    )}
                  </label>
                  {target?.mode === "compose" && (
                    <Button
                      type="button"
                      className="button small"
                      disabled={preview || busy}
                      onClick={async () => {
                        try {
                          const value = await api.importEnv();
                          if (value !== null)
                            setVariables(value || "# empty\n");
                        } catch (e) {
                          setError(String(e));
                        }
                      }}
                    >
                      <Upload size={14} /> Import .env
                    </Button>
                  )}
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
                  <Select
                    label="Release"
                    value={release}
                    onValueChange={setRelease}
                    options={
                      p?.releases.map((r) => ({
                        value: r.id,
                        label: `${r.id} · ${ago(r.created_at)}`,
                      })) ?? []
                    }
                  />
                </>
              )}
            </div>
            <div className="modal-footer">
              {m.kind === "create" || m.kind === "deploy" ? (
                <Button
                  className="text-button"
                  type="button"
                  disabled={busy}
                  onClick={() => setStep(0)}
                >
                  <ChevronLeft size={14} />
                  Environment
                </Button>
              ) : (
                <span className="muted">Touch ID required</span>
              )}
              <Button
                className={`button ${m.kind === "stop" ? "danger" : "primary"}`}
                disabled={
                  busy ||
                  preview ||
                  (m.kind === "create" && !identity) ||
                  (m.kind === "stop" && confirmation !== p?.id) ||
                  (m.kind === "deploy" &&
                    (new TextEncoder().encode(compose).length > 65536 ||
                      new TextEncoder().encode(variables).length > 65536))
                }
                type="submit"
              >
                {preview
                  ? "Preview · read-only"
                  : busy
                    ? "Submitting…"
                    : m.kind === "stop"
                      ? "Review stop"
                      : connecting
                        ? "Connect & deploy"
                        : "Review & submit"}
                <ArrowRight size={15} />
              </Button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
export default App;
