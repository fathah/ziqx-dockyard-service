import { Fragment, useMemo, useState } from "react";
import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
  type FilterFn,
  type SortingState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, Copy, RefreshCw, Search } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";
import JobDetails from "./JobDetails";
import * as api from "./api";
import type { Job, Project } from "./types";

type Row = Job & { project: string };
type StatusFilter = "all" | "active" | "failed" | "recovery" | "succeeded";

const statusGroups: Record<StatusFilter, (s: string) => boolean> = {
  all: () => true,
  active: (s) => s === "queued" || s === "running",
  failed: (s) => s === "failed",
  recovery: (s) => s === "recovery_required",
  succeeded: (s) => s === "succeeded",
};
const statusTabs: { id: StatusFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "active", label: "Active" },
  { id: "recovery", label: "Needs recovery" },
  { id: "failed", label: "Failed" },
  { id: "succeeded", label: "Succeeded" },
];

const phases: Record<string, string> = {
  accepted: "Queued",
  validating: "Validating",
  pulling: "Pulling images",
  candidate_intent: "Starting new version",
  readiness: "Health check",
  route_intent: "Switching traffic",
  maintenance_intent: "Maintenance",
  draining: "Draining old version",
  reconciled: "Recovered",
  dns_intent: "Updating DNS",
};
const sentence = (code: string) =>
  code.charAt(0) + code.slice(1).toLowerCase().replaceAll("_", " ");

// One short human line; raw codes stay in the tooltip.
function stage(j: Job): string {
  if (j.status === "succeeded") return j.warning_code ? sentence(j.warning_code) : "Done";
  if (j.error_code === "JOB_INTERRUPTED_RECONCILED") return "Interrupted · recovered";
  if (j.error_code) return sentence(j.error_code);
  return phases[j.phase] ?? sentence(j.phase);
}

function when(s: string): string {
  const d = new Date(s);
  const today = new Date().toDateString() === d.toDateString();
  return d.toLocaleString(undefined, {
    ...(today ? {} : { month: "short", day: "numeric" }),
    hour: "numeric",
    minute: "2-digit",
  });
}

const statusTone = (s: string) =>
  s === "succeeded"
    ? "green"
    : s === "failed" || s === "recovery_required"
      ? "red"
      : "amber";

const textFilter: FilterFn<Row> = (row, _id, value: string) => {
  const q = value.trim().toLowerCase();
  if (!q) return true;
  const j = row.original;
  return [j.job_id, j.project, j.project_id, j.action, j.error_code, j.phase, j.actor_id]
    .filter(Boolean)
    .some((v) => String(v).toLowerCase().includes(q));
};

const column = createColumnHelper<Row>();

export default function JobsTable({
  jobs,
  projects,
  operationLabel,
  projectName,
  preview,
  refresh,
  report,
  lookup,
}: {
  jobs: Job[];
  projects: Project[];
  operationLabel: (action: string) => string;
  projectName: (projects: Project[], id: string) => string;
  preview: boolean;
  refresh: () => Promise<void>;
  report: (e: unknown) => void;
  lookup: (id: string) => Promise<void>;
}) {
  const [status, setStatus] = useState<StatusFilter>("all");
  const [project, setProject] = useState("");
  const [search, setSearch] = useState("");
  const [sorting, setSorting] = useState<SortingState>([{ id: "created_at", desc: true }]);
  const [selected, setSelected] = useState<Row>();

  const rows = useMemo<Row[]>(
    () =>
      jobs
        .map((j) => ({ ...j, project: projectName(projects, j.project_id) }))
        .filter((j) => statusGroups[status](j.status))
        .filter((j) => !project || j.project_id === project),
    [jobs, projects, projectName, status, project],
  );
  const projectOptions = useMemo(
    () =>
      Array.from(new Set(jobs.map((j) => j.project_id))).map((id) => ({
        id,
        name: projectName(projects, id),
      })),
    [jobs, projects, projectName],
  );
  const counts = useMemo(() => {
    const c = {} as Record<StatusFilter, number>;
    for (const t of statusTabs) c[t.id] = jobs.filter((j) => statusGroups[t.id](j.status)).length;
    return c;
  }, [jobs]);

  const columns = useMemo(
    () => [
      column.accessor("status", {
        header: "Status",
        cell: (c) => (
          <span className={`jobs-status ${statusTone(c.getValue())}`}>
            <i aria-hidden="true" />
            {c.getValue().replaceAll("_", " ")}
          </span>
        ),
      }),
      column.accessor("action", {
        header: "Operation",
        cell: (c) => <strong>{operationLabel(c.getValue())}</strong>,
      }),
      column.accessor("project", {
        header: "Project",
        cell: (c) => <span title={c.row.original.project_id}>{c.getValue()}</span>,
      }),
      column.display({
        id: "stage",
        header: "Stage",
        cell: (c) => {
          const j = c.row.original;
          return (
            <span
              className="jobs-stage"
              title={[j.phase, j.error_code, j.warning_code].filter(Boolean).join(" · ")}
            >
              {stage(j)}
            </span>
          );
        },
      }),
      column.accessor("created_at", {
        header: "Started",
        sortingFn: "datetime",
        cell: (c) => (
          <span title={`${new Date(c.getValue()).toLocaleString()} · ${c.row.original.actor_id}`}>
            {when(c.getValue())}
          </span>
        ),
      }),
      column.display({
        id: "id",
        header: "",
        cell: (c) => (
          <button
            type="button"
            className="jobs-copy"
            title={`Copy ${c.row.original.job_id}`}
            aria-label={`Copy job ID ${c.row.original.job_id}`}
            onClick={(e) => {
              e.stopPropagation();
              void navigator.clipboard.writeText(c.row.original.job_id);
              toast.success("Job ID copied");
            }}
          >
            <Copy size={14} />
          </button>
        ),
      }),
    ],
    [operationLabel],
  );

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, globalFilter: search },
    onSortingChange: setSorting,
    onGlobalFilterChange: setSearch,
    globalFilterFn: textFilter,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getRowId: (r) => r.job_id,
  });

  const visible = table.getRowModel().rows;
  return (
    <>
      <div className="jobs-toolbar">
        <div className="jobs-filters" role="tablist" aria-label="Filter by status">
          {statusTabs
            .filter((t) => t.id === "all" || counts[t.id] > 0)
            .map((t) => (
              <button
                key={t.id}
                type="button"
                role="tab"
                aria-selected={status === t.id}
                className={status === t.id ? "selected" : ""}
                onClick={() => setStatus(t.id)}
              >
                {t.label}
                <span>{counts[t.id]}</span>
              </button>
            ))}
        </div>
        <div className="jobs-search">
          {projectOptions.length > 1 && (
            <select
              aria-label="Filter by project"
              value={project}
              onChange={(e) => setProject(e.target.value)}
            >
              <option value="">All projects</option>
              {projectOptions.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          )}
          <label>
            <Search size={14} aria-hidden="true" />
            <input
              aria-label="Search operations or look up a job ID"
              placeholder="Search or paste job ID"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                const id = search.trim();
                if (e.key === "Enter" && /^job-[a-f0-9]{32}$/.test(id) && !jobs.some((j) => j.job_id === id))
                  void lookup(id);
              }}
            />
          </label>
        </div>
      </div>
      <div className="jobs-table-scroll">
        <table className="jobs-table">
          <thead>
            {table.getHeaderGroups().map((g) => (
              <tr key={g.id}>
                {g.headers.map((h) => (
                  <th
                    key={h.id}
                    className={`jobs-col-${h.column.id}`}
                    aria-sort={
                      h.column.getIsSorted() === "asc"
                        ? "ascending"
                        : h.column.getIsSorted() === "desc"
                          ? "descending"
                          : undefined
                    }
                  >
                    {h.column.getCanSort() && h.column.id !== "id" ? (
                      <button type="button" onClick={h.column.getToggleSortingHandler()}>
                        {flexRender(h.column.columnDef.header, h.getContext())}
                        {h.column.getIsSorted() === "asc" && <ArrowUp size={12} />}
                        {h.column.getIsSorted() === "desc" && <ArrowDown size={12} />}
                      </button>
                    ) : (
                      flexRender(h.column.columnDef.header, h.getContext())
                    )}
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody>
            {visible.map((r) => (
              <Fragment key={r.id}>
                <tr
                  className={`jobs-row ${r.original.status === "recovery_required" ? "has-detail" : ""}`}
                  tabIndex={0}
                  aria-label={`Show details for ${operationLabel(r.original.action)} on ${r.original.project}`}
                  onClick={() => setSelected(r.original)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      setSelected(r.original);
                    }
                  }}
                >
                  {r.getVisibleCells().map((c) => (
                    <td key={c.id} className={`jobs-col-${c.column.id}`}>
                      {flexRender(c.column.columnDef.cell, c.getContext())}
                    </td>
                  ))}
                </tr>
                {r.original.status === "recovery_required" && (
                  <tr className="jobs-detail">
                    <td colSpan={r.getVisibleCells().length}>
                      <Recovery job={r.original} preview={preview} refresh={refresh} report={report} />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
        {!visible.length && jobs.length > 0 && (
          <p className="jobs-empty">No operations match these filters.</p>
        )}
      </div>
      {selected && (
        <JobDetails
          key={selected.job_id}
          job={jobs.find((j) => j.job_id === selected.job_id) ?? selected}
          title={`${operationLabel(selected.action)} · ${selected.project}`}
          preview={preview}
          onClose={() => setSelected(undefined)}
        />
      )}
    </>
  );
}

function Recovery({
  job,
  preview,
  refresh,
  report,
}: {
  job: Job;
  preview: boolean;
  refresh: () => Promise<void>;
  report: (e: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [manual, setManual] = useState(false);
  if (!/^job-[a-f0-9]{32}$/.test(job.job_id)) return null;
  return (
    <div className="job-recovery">
      <div className="job-recovery-actions">
        <span>Interrupted. New deployments wait until it's recovered.</span>
        <Button
          type="button"
          className="button small primary"
          disabled={busy || preview}
          title="Checks the live containers and records what's running. Nothing is restarted."
          onClick={async () => {
            setBusy(true);
            try {
              await api.reconcileJob(job.job_id);
              toast.success("Recovered");
              await refresh();
            } catch (e) {
              report(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          <RefreshCw size={14} />
          {busy ? "Recovering…" : "Recover"}
        </Button>
        <button type="button" className="text-button" onClick={() => setManual(!manual)}>
          {manual ? "Hide terminal steps" : "Server older than 0.7.6?"}
        </button>
      </div>
      {manual && (
        <pre>
          <code>{`systemctl stop dockyard\n/usr/local/bin/dockyard -config /etc/dockyard/config.json -reconcile-job ${job.job_id}\nsystemctl start dockyard`}</code>
        </pre>
      )}
    </div>
  );
}
