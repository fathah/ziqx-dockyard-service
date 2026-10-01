import Button from "./Button";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { invoke } from "@tauri-apps/api/core";
import {
  Cloud,
  Plus,
  RefreshCw,
  Trash2,
  Pencil,
  X,
  ArrowUpRight,
  ChevronLeft,
  ChevronRight,
  Globe2,
  Fingerprint,
} from "lucide-react";
import toast from "react-hot-toast";
import Help from "./Help";
import Select from "./Select";
import "./providers.css";

type Provider = { id: string; name: string; kind: "cloudflare" };
type Zone = {
  id: string;
  name: string;
  status: string;
  account: { id: string; name: string };
  name_servers?: string[];
};
type RecordData = {
  flags?: number;
  tag?: string;
  value?: string;
  priority?: number;
  weight?: number;
  port?: number;
  target?: string;
};
type DNSRecord = {
  id: string;
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied?: boolean;
  proxiable?: boolean;
  priority?: number;
  data?: RecordData;
  modified_on: string;
};
type Listing<T> = {
  items: T[];
  page: number;
  total_pages: number;
  total: number;
};
type Input = {
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied: boolean;
  priority: number | null;
  data: RecordData | null;
};
const supported = ["A", "AAAA", "CNAME", "TXT", "MX", "NS", "CAA", "SRV"];
const explain = (e: unknown) => (e instanceof Error ? e.message : String(e));

function useLoad<T>(
  command: string,
  args: object,
  enabled: boolean,
  revision = 0,
) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(enabled);
  const key = JSON.stringify(args);
  useEffect(() => {
    let alive = true;
    setData(null);
    setError("");
    setLoading(enabled);
    if (enabled)
      void invoke<T>(command, JSON.parse(key))
        .then((value) => {
          if (alive) setData(value);
        })
        .catch((e) => {
          if (alive) setError(explain(e));
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    return () => {
      alive = false;
    };
  }, [command, key, enabled, revision]);
  return { data, error, loading };
}
function Placeholder({ rows = 4 }: { rows?: number }) {
  return (
    <div role="status" aria-label="Loading Cloudflare data">
      {Array.from({ length: rows }, (_, i) => (
        <div className="provider-skeleton" key={i}>
          <span
            className="skeleton-bar"
            style={{ width: 36, height: 36, borderRadius: 8 }}
          >
            <span className="skeleton-fill" />
          </span>
          <div>
            <span className="skeleton-bar" style={{ width: "45%", height: 16 }}>
              <span className="skeleton-fill" />
            </span>
            <span
              className="skeleton-bar"
              style={{ width: "70%", height: 12, marginTop: 12 }}
            >
              <span className="skeleton-fill" />
            </span>
          </div>
        </div>
      ))}
    </div>
  );
}
function ErrorBox({ error }: { error: string }) {
  return error ? (
    <p className="provider-error" role="alert">
      {error}
    </p>
  ) : null;
}
function Pagination({
  data,
  change,
}: {
  data: { page: number; total_pages: number; total: number };
  change: (n: number) => void;
}) {
  return (
    <div className="dns-pagination">
      <span>{data.total} total</span>
      <Button
        type="button"
        className="button small"
        aria-label="Previous page"
        disabled={data.page <= 1}
        onClick={() => change(data.page - 1)}
      >
        <ChevronLeft size={16} />
      </Button>
      <span>
        {data.page} / {data.total_pages}
      </span>
      <Button
        type="button"
        className="button small"
        aria-label="Next page"
        disabled={data.page >= data.total_pages}
        onClick={() => change(data.page + 1)}
      >
        <ChevronRight size={16} />
      </Button>
    </div>
  );
}
function Dialog({
  title,
  children,
  close,
  busy,
}: {
  title: string;
  children: ReactNode;
  close: () => void;
  busy?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className="provider-dialog"
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) close();
      }}
    >
      <div className="provider-dialog-heading">
        <h2>{title}</h2>
        <Button
          type="button"
          className="icon-button"
          disabled={busy}
          onClick={close}
          aria-label="Close"
        >
          <X size={20} />
        </Button>
      </div>
      {children}
    </dialog>
  );
}
export function DomainProviderSettings({
  preview,
  manage,
}: {
  preview: boolean;
  manage: (id: string) => void;
}) {
  const [revision, setRevision] = useState(0);
  const { data, loading, error } = useLoad<Provider[]>(
    "provider_list",
    {},
    !preview,
    revision,
  );
  const [editing, setEditing] = useState<Provider | "new" | null>(null);
  const [removing, setRemoving] = useState<Provider | null>(null);
  const [busy, setBusy] = useState(false);
  const [removeError, setRemoveError] = useState("");
  return (
    <>
      <div className="page-heading">
        <h1>Settings</h1>
      </div>
      <section className="panel provider-settings">
        <div className="provider-section-heading">
          <h2>
            Domain Providers{" "}
            <Help label="domain providers">
              Connect accounts to manage their domains and DNS. Credentials stay
              in this Mac’s Keychain. DNS edits do not change your Caddy routes.
            </Help>
          </h2>
          <Button
            type="button"
            className="button primary"
            disabled={preview}
            onClick={() => setEditing("new")}
          >
            <Plus size={17} /> Add Cloudflare
          </Button>
        </div>
        {preview && (
          <p className="muted">
            Connect providers in the unlocked desktop app.
          </p>
        )}
        <ErrorBox error={error} />
        {error && (
          <Button
            type="button"
            className="button small"
            onClick={() => setRevision((n) => n + 1)}
          >
            Retry
          </Button>
        )}
        {loading ? (
          <Placeholder rows={2} />
        ) : (
          data?.map((p) => (
            <div className="provider-account-row" key={p.id}>
              <span className="provider-cloud">
                <Cloud size={24} />
              </span>
              <div>
                <strong>{p.name}</strong>
                <p>Cloudflare · Token saved in Keychain</p>
              </div>
              <Button
                type="button"
                className="button small"
                onClick={() => manage(p.id)}
              >
                Manage DNS <ArrowUpRight size={15} />
              </Button>
              <Button
                type="button"
                className="button small"
                onClick={() => setEditing(p)}
              >
                Reconnect
              </Button>
              <Button
                type="button"
                className="icon-button"
                aria-label={`Remove ${p.name}`}
                onClick={() => {
                  setRemoving(p);
                  setRemoveError("");
                }}
              >
                <Trash2 size={17} />
              </Button>
            </div>
          ))
        )}
        {!loading && !error && !data?.length && (
          <div className="provider-empty">
            <Cloud size={32} />
            <h3>Connect your first provider</h3>
            <p>Bring your Cloudflare domains into Dockyard.</p>
          </div>
        )}
        <div className="provider-coming">
          <span>GoDaddy</span>
          <span className="badge">Coming later</span>
        </div>
      </section>
      {editing && (
        <ConnectProvider
          provider={editing === "new" ? null : editing}
          close={() => setEditing(null)}
          done={() => {
            setEditing(null);
            setRevision((n) => n + 1);
          }}
        />
      )}
      {removing && (
        <Dialog
          title={`Disconnect ${removing.name}?`}
          close={() => setRemoving(null)}
          busy={busy}
        >
          <p>
            Remove this connection from this Mac. Your domains and DNS records
            stay in Cloudflare.
          </p>
          <p className="muted">
            You can revoke the token separately in Cloudflare.
          </p>
          <ErrorBox error={removeError} />
          <div className="provider-dialog-actions">
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setRemoving(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await invoke("provider_remove", { provider: removing.id });
                  setRemoving(null);
                  setRevision((n) => n + 1);
                  toast.success("Provider disconnected");
                } catch (e) {
                  setRemoveError(explain(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Fingerprint size={17} />
              {busy ? "Disconnecting…" : "Disconnect"}
            </Button>
          </div>
        </Dialog>
      )}
    </>
  );
}
function ConnectProvider({
  provider,
  close,
  done,
}: {
  provider: Provider | null;
  close: () => void;
  done: () => void;
}) {
  const [name, setName] = useState(provider?.name ?? "Cloudflare");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Dialog
      title={provider ? "Reconnect Cloudflare" : "Connect Cloudflare"}
      close={close}
      busy={busy}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            await invoke("provider_connect", {
              name,
              replace: provider?.id ?? null,
            });
            toast.success("Cloudflare connected");
            done();
          } catch (e) {
            setError(explain(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label className="provider-field">
          Connection name
          <input
            required
            maxLength={80}
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={busy}
          />
        </label>
        <div className="provider-connect-step">
          <span>1</span>
          <div>
            <strong>Create a Cloudflare user API token</strong>
            <p>
              Choose <b>Zone → Zone → Read</b> and <b>Zone → DNS → Edit</b>.
              Select the domains you want Dockyard to access.
            </p>
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() =>
                void invoke("provider_token_page").catch((e) =>
                  setError(explain(e)),
                )
              }
            >
              Open Cloudflare <ArrowUpRight size={16} />
            </Button>
          </div>
        </div>
        <div className="provider-connect-step">
          <span>2</span>
          <div>
            <strong>Connect with your token</strong>
            <p>Touch ID, then paste it into the secure macOS prompt.</p>
          </div>
        </div>
        <ErrorBox error={error} />
        <div className="provider-dialog-actions">
          <Button
            className="button"
            type="button"
            disabled={busy}
            onClick={close}
          >
            Cancel
          </Button>
          <Button type="submit" className="button primary" disabled={busy}>
            <Fingerprint size={17} />
            {busy ? "Connecting…" : "Enter token & connect"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
export function ProviderDomains({
  preview,
  serverIP,
  settings,
  routes,
  initialProvider,
}: {
  preview: boolean;
  serverIP?: string | null;
  settings: () => void;
  routes: ReactNode;
  initialProvider?: string;
}) {
  const [tab, setTab] = useState<"dns" | "routes">("dns");
  return (
    <>
      <div className="dns-tabs">
        <Button
          type="button"
          className={tab === "dns" ? "button primary" : "button"}
          onClick={() => setTab("dns")}
        >
          Provider DNS
        </Button>
        <Button
          type="button"
          className={tab === "routes" ? "button primary" : "button"}
          onClick={() => setTab("routes")}
        >
          Caddy routes
        </Button>
      </div>
      {tab === "routes" ? (
        routes
      ) : (
        <DNSWorkspace
          preview={preview}
          serverIP={serverIP}
          settings={settings}
          initialProvider={initialProvider}
        />
      )}
    </>
  );
}
function DNSWorkspace({
  preview,
  serverIP,
  settings,
  initialProvider,
}: {
  preview: boolean;
  serverIP?: string | null;
  settings: () => void;
  initialProvider?: string;
}) {
  const [revision, setRevision] = useState(0);
  const providers = useLoad<Provider[]>(
    "provider_list",
    {},
    !preview,
    revision,
  );
  const [selected, setSelected] = useState(initialProvider ?? "");
  const current =
    providers.data?.find((p) => p.id === selected) ?? providers.data?.[0];
  const [zone, setZone] = useState<Zone | null>(null);
  const [zonePage, setZonePage] = useState(1);
  const zones = useLoad<Listing<Zone>>(
    "provider_zones",
    { provider: current?.id, pageNumber: zonePage },
    !!current,
    revision,
  );
  const [search, setSearch] = useState("");
  return (
    <>
      <div className="page-heading">
        <h1>
          Domains{" "}
          <Help label="provider DNS">
            Manage DNS zones accessible to your Cloudflare token. Domain
            registration, billing and nameserver changes remain in Cloudflare.
            Configure traffic routing separately in Caddy routes.
          </Help>
        </h1>
        <Button type="button" className="button" onClick={settings}>
          Domain Providers
        </Button>
      </div>
      {preview ? (
        <section className="panel">
          <p>Open the desktop app to connect Cloudflare.</p>
        </section>
      ) : providers.loading ? (
        <section className="panel">
          <Placeholder />
        </section>
      ) : (
        <>
          <ErrorBox error={providers.error} />
          {providers.error && (
            <Button
              type="button"
              className="button"
              onClick={() => setRevision((n) => n + 1)}
            >
              Retry
            </Button>
          )}
          {providers.data && !providers.data.length && (
            <section className="panel provider-empty">
              <Globe2 size={36} />
              <h2>Your domains, in one place</h2>
              <p>Connect Cloudflare to view domains and manage DNS.</p>
              <Button
                type="button"
                className="button primary"
                onClick={settings}
              >
                <Plus size={17} /> Add provider
              </Button>
            </section>
          )}
          {current && (
            <>
              <div className="dns-toolbar">
                <Select
                  label="Provider"
                  containerClassName="provider-field"
                  value={current.id}
                  onValueChange={(value) => {
                    setSelected(value);
                    setZone(null);
                    setZonePage(1);
                    setSearch("");
                  }}
                  options={
                    providers.data?.map((p) => ({
                      value: p.id,
                      label: p.name,
                    })) ?? []
                  }
                />
                <Button
                  type="button"
                  className="button small"
                  disabled={zones.loading}
                  onClick={() => {
                    setZone(null);
                    setRevision((n) => n + 1);
                  }}
                >
                  <RefreshCw size={16} /> Refresh domains
                </Button>
              </div>
              {zone ? (
                <DNSZone
                  key={`${current.id}:${zone.id}`}
                  provider={current}
                  zone={zone}
                  serverIP={serverIP}
                  back={() => setZone(null)}
                />
              ) : (
                <section className="panel">
                  <div className="provider-section-heading">
                    <h2>Domains</h2>
                    <input
                      className="dns-search"
                      aria-label="Filter domains on this page"
                      placeholder="Filter this page…"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                  </div>
                  <ErrorBox error={zones.error} />
                  {zones.error && (
                    <Button
                      type="button"
                      className="button"
                      onClick={() => setRevision((n) => n + 1)}
                    >
                      Retry
                    </Button>
                  )}
                  {zones.loading ? (
                    <Placeholder />
                  ) : (
                    zones.data?.items
                      .filter((z) =>
                        (z.name + z.account?.name)
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((z) => (
                        <Button
                          type="button"
                          className="dns-zone-row"
                          key={z.id}
                          onClick={() => setZone(z)}
                        >
                          <Globe2 size={22} />
                          <span>
                            <strong>{z.name}</strong>
                            <small>{z.account?.name}</small>
                          </span>
                          <span className="badge">{z.status}</span>
                          <ChevronRight size={18} />
                        </Button>
                      ))
                  )}
                  {zones.data?.total === 0 && (
                    <p className="muted">
                      No domains available. Check which zones your token can
                      access.
                    </p>
                  )}
                  {zones.data && (
                    <Pagination data={zones.data} change={setZonePage} />
                  )}
                </section>
              )}
            </>
          )}
        </>
      )}
    </>
  );
}
function DNSZone({
  provider,
  zone,
  serverIP,
  back,
}: {
  provider: Provider;
  zone: Zone;
  serverIP?: string | null;
  back: () => void;
}) {
  const [revision, setRevision] = useState(0);
  const [page, setPage] = useState(1);
  const records = useLoad<Listing<DNSRecord>>(
    "provider_records",
    { provider: provider.id, zone: zone.id, pageNumber: page },
    true,
    revision,
  );
  const [search, setSearch] = useState("");
  const [edit, setEdit] = useState<DNSRecord | "new" | null>(null);
  const [remove, setRemove] = useState<DNSRecord | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <section className="panel">
      <Button type="button" className="text-button" onClick={back}>
        <ChevronLeft size={16} /> All domains
      </Button>
      <div className="provider-section-heading dns-zone-heading">
        <div>
          <h2>{zone.name}</h2>
          <span className="muted">
            {zone.account?.name} · {zone.status}
          </span>
        </div>
        <div className="dns-row-actions">
          <Button
            type="button"
            className="button"
            aria-label="Refresh DNS records"
            disabled={records.loading}
            onClick={() => setRevision((n) => n + 1)}
          >
            <RefreshCw size={17} />
          </Button>
          <Button
            type="button"
            className="button primary"
            onClick={() => setEdit("new")}
          >
            <Plus size={17} /> Add record
          </Button>
        </div>
      </div>
      {zone.status !== "active" && (
        <p className="provider-notice">
          This zone is {zone.status}. Check its setup and nameservers in
          Cloudflare before expecting DNS changes to serve publicly.
        </p>
      )}
      <input
        className="dns-search"
        aria-label="Filter DNS records on this page"
        placeholder="Filter records on this page…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      <ErrorBox error={records.error} />
      {records.loading ? (
        <Placeholder rows={5} />
      ) : (
        records.data && (
          <>
            <div className="dns-table-wrap">
              <table className="dns-table">
                <thead>
                  <tr>
                    <th>Type</th>
                    <th>Name / content</th>
                    <th>Proxy</th>
                    <th>TTL</th>
                    <th>
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {records.data.items
                    .filter((r) =>
                      (r.type + r.name + r.content)
                        .toLowerCase()
                        .includes(search.toLowerCase()),
                    )
                    .map((r) => (
                      <tr key={r.id}>
                        <td>
                          <span className="badge">{r.type}</span>
                        </td>
                        <td>
                          <strong>{r.name}</strong>
                          <div className="dns-content">
                            {r.content || JSON.stringify(r.data)}
                          </div>
                        </td>
                        <td>{r.proxied ? "Proxied" : "DNS only"}</td>
                        <td>{r.ttl === 1 ? "Auto" : `${r.ttl}s`}</td>
                        <td>
                          <div className="dns-row-actions">
                            <Button
                              type="button"
                              className="icon-button"
                              disabled={!supported.includes(r.type)}
                              title={
                                supported.includes(r.type)
                                  ? "Edit record"
                                  : "Edit this record type in Cloudflare"
                              }
                              aria-label={`Edit ${r.type} ${r.name}`}
                              onClick={() => setEdit(r)}
                            >
                              <Pencil size={16} />
                            </Button>
                            <Button
                              type="button"
                              className="icon-button"
                              aria-label={`Delete ${r.type} ${r.name}`}
                              onClick={() => {
                                setRemove(r);
                                setError("");
                              }}
                            >
                              <Trash2 size={16} />
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
            {!records.data.items.length && (
              <p className="provider-empty">No DNS records yet.</p>
            )}
            <Pagination data={records.data} change={setPage} />
          </>
        )
      )}
      {zone.name_servers?.length ? (
        <details className="dns-nameservers">
          <summary>Nameservers</summary>
          {zone.name_servers.map((n) => (
            <p key={n}>{n}</p>
          ))}
        </details>
      ) : null}
      {edit && (
        <RecordEditor
          provider={provider.id}
          zone={zone}
          record={edit === "new" ? null : edit}
          serverIP={serverIP}
          close={() => setEdit(null)}
          done={() => {
            setEdit(null);
            setRevision((n) => n + 1);
          }}
        />
      )}
      {remove && (
        <Dialog
          title="Delete DNS record?"
          close={() => setRemove(null)}
          busy={busy}
        >
          <p>
            <b>
              {remove.type} · {remove.name}
            </b>
          </p>
          <p className="dns-content">{remove.content}</p>
          <p>
            This removes the record from Cloudflare and may interrupt services
            using it.
          </p>
          <ErrorBox error={error} />
          <div className="provider-dialog-actions">
            <Button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setRemove(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await invoke("provider_write", {
                    provider: provider.id,
                    zone: zone.id,
                    change: {
                      action: "delete",
                      id: remove.id,
                      modified_on: remove.modified_on,
                    },
                  });
                  toast.success("DNS record deleted");
                  setRemove(null);
                  setRevision((n) => n + 1);
                } catch (e) {
                  setError(explain(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Fingerprint size={17} />
              {busy ? "Deleting…" : "Delete record"}
            </Button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
function RecordEditor({
  provider,
  zone,
  record,
  serverIP,
  close,
  done,
}: {
  provider: string;
  zone: Zone;
  record: DNSRecord | null;
  serverIP?: string | null;
  close: () => void;
  done: () => void;
}) {
  const [input, setInput] = useState<Input>({
    type: record?.type ?? "A",
    name: record?.name ?? zone.name,
    content: record?.content ?? "",
    ttl: record?.ttl ?? 1,
    proxied: record?.proxied ?? false,
    priority: record?.priority ?? 10,
    data: record?.data ?? null,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [attempted, setAttempted] = useState(false);
  const field = <K extends keyof Input>(key: K, value: Input[K]) =>
    setInput((p) => ({ ...p, [key]: value }));
  const data = (key: keyof RecordData, value: string | number) =>
    setInput((p) => ({ ...p, data: { ...p.data, [key]: value } }));
  const canProxy = ["A", "AAAA", "CNAME"].includes(input.type);
  return (
    <Dialog
      title={record ? "Edit DNS record" : "Add DNS record"}
      close={close}
      busy={busy}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          setAttempted(true);
          try {
            await invoke("provider_write", {
              provider,
              zone: zone.id,
              change: record
                ? {
                    action: "update",
                    id: record.id,
                    modified_on: record.modified_on,
                    record: input,
                  }
                : { action: "create", record: input },
            });
            toast.success(record ? "DNS record updated" : "DNS record created");
            done();
          } catch (e) {
            setError(explain(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="dns-form-grid">
          <Select
            label="Type"
            containerClassName="provider-field"
            value={input.type}
            disabled={!!record || busy}
            onValueChange={(type) => {
              setInput((p) => ({
                ...p,
                type,
                proxied: false,
                content: "",
                data:
                  type === "CAA"
                    ? { flags: 0, tag: "issue", value: "" }
                    : type === "SRV"
                      ? { priority: 0, weight: 0, port: 443, target: "" }
                      : null,
              }));
            }}
            options={supported.map((t) => ({ value: t, label: t }))}
          />
          <Select
            label="TTL"
            containerClassName="provider-field"
            value={String(input.proxied ? 1 : input.ttl)}
            disabled={input.proxied || busy}
            onValueChange={(value) => field("ttl", Number(value))}
            options={[
              ...new Set([1, 60, 120, 300, 600, 1800, 3600, 86400, input.ttl]),
            ].map((n) => ({
              value: String(n),
              label: n === 1 ? "Auto" : `${n} seconds`,
            }))}
          />
        </div>
        <label className="provider-field">
          Full record name
          <input
            required
            maxLength={253}
            value={input.name}
            disabled={busy}
            onChange={(e) => field("name", e.target.value)}
            placeholder={`app.${zone.name}`}
          />
        </label>
        {!["CAA", "SRV"].includes(input.type) && (
          <label className="provider-field">
            {input.type === "A"
              ? "IPv4 address"
              : input.type === "AAAA"
                ? "IPv6 address"
                : input.type === "TXT"
                  ? "Text"
                  : "Target hostname"}
            {input.type === "TXT" ? (
              <textarea
                required
                value={input.content}
                disabled={busy}
                onChange={(e) => field("content", e.target.value)}
              />
            ) : (
              <input
                required
                value={input.content}
                disabled={busy}
                onChange={(e) => field("content", e.target.value)}
              />
            )}
          </label>
        )}
        {serverIP &&
          ((input.type === "A" && !serverIP.includes(":")) ||
            (input.type === "AAAA" && serverIP.includes(":"))) && (
            <Button
              type="button"
              className="text-button"
              disabled={busy}
              onClick={() => field("content", serverIP)}
            >
              Use VPS IP · {serverIP}
            </Button>
          )}
        {input.type === "MX" && (
          <label className="provider-field">
            Priority
            <input
              type="number"
              required
              min={0}
              max={65535}
              disabled={busy}
              value={input.priority ?? 10}
              onChange={(e) => field("priority", Number(e.target.value))}
            />
          </label>
        )}
        {input.type === "CAA" && (
          <>
            <div className="dns-form-grid">
              <label className="provider-field">
                Flags
                <input
                  type="number"
                  required
                  min={0}
                  max={255}
                  disabled={busy}
                  value={input.data?.flags ?? 0}
                  onChange={(e) => data("flags", Number(e.target.value))}
                />
              </label>
              <Select
                label="Tag"
                containerClassName="provider-field"
                value={input.data?.tag ?? "issue"}
                disabled={busy}
                onValueChange={(value) => data("tag", value)}
                options={["issue", "issuewild", "iodef"].map((value) => ({
                  value,
                  label: value,
                }))}
              />
            </div>
            <label className="provider-field">
              Value
              <input
                required
                disabled={busy}
                value={input.data?.value ?? ""}
                onChange={(e) => data("value", e.target.value)}
              />
            </label>
          </>
        )}
        {input.type === "SRV" && (
          <>
            <div className="dns-form-grid">
              {(["priority", "weight", "port"] as const).map((key) => (
                <label className="provider-field" key={key}>
                  {key}
                  <input
                    type="number"
                    required
                    min={0}
                    max={65535}
                    disabled={busy}
                    value={input.data?.[key] ?? 0}
                    onChange={(e) => data(key, Number(e.target.value))}
                  />
                </label>
              ))}
            </div>
            <label className="provider-field">
              Target
              <input
                required
                disabled={busy}
                value={input.data?.target ?? ""}
                onChange={(e) => data("target", e.target.value)}
              />
            </label>
          </>
        )}
        {canProxy && (
          <label className="dns-checkbox">
            <input
              type="checkbox"
              checked={input.proxied}
              disabled={busy}
              onChange={(e) => field("proxied", e.target.checked)}
            />{" "}
            Cloudflare proxy{" "}
            <Help label="Cloudflare proxy">
              Routes web traffic through Cloudflare. DNS only points directly at
              the target. The proxy supports specific protocols and ports; it
              does not create a Caddy route.
            </Help>
          </label>
        )}
        <ErrorBox error={error} />
        {error && attempted && (
          <p className="muted">
            Close and refresh records before retrying if the connection was
            interrupted.
          </p>
        )}
        <div className="provider-dialog-actions">
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={close}
          >
            Cancel
          </Button>
          <Button type="submit" className="button primary" disabled={busy}>
            <Fingerprint size={17} />
            {busy ? "Saving…" : "Save with Touch ID"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
