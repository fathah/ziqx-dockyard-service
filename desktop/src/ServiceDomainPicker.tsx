import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { Cloud, Plus } from "lucide-react";
import Button from "./Button";
import Select from "./Select";
import { ConnectProvider } from "./DomainProviders";

export type ServiceDNSInput = {
  provider: string;
  zone: string;
  name: string;
  proxied: boolean;
};
export type ServiceDNSReview = {
  name: string;
  record_type: string;
  server_ip: string;
  proxied: boolean;
  action: "create" | "update" | "reuse";
  previous?: string;
  review_sha256: string;
};
type Provider = { id: string; name: string; kind: "cloudflare" };
type Zone = { id: string; name: string; status: string };

export default function ServiceDomainPicker({
  service,
  domains,
  setDomains,
  onDNS,
  disabled,
  preview,
}: {
  service: string;
  domains: string;
  setDomains: (value: string) => void;
  onDNS: (value?: ServiceDNSInput) => void;
  disabled: boolean;
  preview: boolean;
}) {
  const [mode, setMode] = useState("existing");
  const [providers, setProviders] = useState<Provider[]>([]);
  const [provider, setProvider] = useState("");
  const [zones, setZones] = useState<Zone[]>([]);
  const [zone, setZone] = useState("");
  const [page, setPage] = useState(1);
  const [pages, setPages] = useState(1);
  const [subdomain, setSubdomain] = useState(service || "app");
  const [subdomainEdited, setSubdomainEdited] = useState(false);
  const [proxied, setProxied] = useState(false);
  const [adding, setAdding] = useState(false);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const current = zones.find((z) => z.id === zone);
  const name = current
    ? (subdomain === "@"
        ? current.name
        : `${subdomain}.${current.name}`
      ).toLowerCase()
    : "";
  useEffect(() => {
    if (!subdomainEdited && service) setSubdomain(service);
  }, [service, subdomainEdited]);
  useEffect(() => {
    if (mode !== "cloudflare" || preview) return;
    let alive = true;
    setLoading(true);
    setError("");
    invoke<Provider[]>("provider_list")
      .then((items) => {
        if (!alive) return;
        setProviders(items);
        setProvider((old) =>
          items.some((p) => p.id === old) ? old : (items[0]?.id ?? ""),
        );
      })
      .catch((e) => {
        if (alive) setError(String(e));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [mode, preview, revision]);
  useEffect(() => {
    setZones([]);
    setZone("");
    if (!provider || mode !== "cloudflare") return;
    let alive = true;
    setLoading(true);
    setError("");
    invoke<{ items: Zone[]; total_pages: number }>("provider_zones", {
      provider,
      pageNumber: page,
    })
      .then((value) => {
        if (!alive) return;
        setZones(value.items);
        setPages(value.total_pages);
        setZone(value.items[0]?.id ?? "");
      })
      .catch((e) => {
        if (alive) setError(String(e));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [provider, page, mode, revision]);
  const dnsKey = JSON.stringify(
    mode === "cloudflare" ? { provider, zone, name, proxied } : null,
  );
  useEffect(() => {
    onDNS(
      mode === "cloudflare" ? { provider, zone, name, proxied } : undefined,
    );
    if (mode === "cloudflare") setDomains(name);
  }, [dnsKey]);
  return (
    <div className="service-domain-picker">
      <div className="segments" role="tablist" aria-label="Domain source">
        <Button
          type="button"
          role="tab"
          aria-selected={mode === "existing"}
          className={mode === "existing" ? "selected" : ""}
          disabled={disabled}
          onClick={() => setMode("existing")}
        >
          Use a domain
        </Button>
        <Button
          type="button"
          role="tab"
          aria-selected={mode === "cloudflare"}
          className={mode === "cloudflare" ? "selected" : ""}
          disabled={disabled || preview}
          onClick={() => setMode("cloudflare")}
        >
          <Cloud size={15} /> Cloudflare subdomain
        </Button>
      </div>
      {mode === "existing" ? (
        <label className="provider-field">
          <span>Domain</span>
          <input
            placeholder="app.example.com"
            value={domains}
            disabled={disabled}
            onChange={(e) => setDomains(e.target.value)}
          />
        </label>
      ) : (
        <>
          <div className="route-fields">
            <Select
              label="Cloudflare account"
              value={provider}
              disabled={disabled || loading}
              options={providers.map((p) => ({ value: p.id, label: p.name }))}
              onValueChange={(value) => {
                setProvider(value);
                setPage(1);
              }}
            />
            <Select
              label="Domain"
              value={zone}
              disabled={disabled || loading}
              options={zones.map((z) => ({ value: z.id, label: z.name }))}
              onValueChange={setZone}
            />
            <label className="provider-field">
              <span>Subdomain</span>
              <input
                placeholder="api (or @ for the root domain)"
                value={subdomain}
                disabled={disabled}
                onChange={(e) => {
                  setSubdomainEdited(true);
                  setSubdomain(e.target.value.trim());
                }}
              />
            </label>
          </div>
          {pages > 1 && (
            <div className="heading-actions">
              <Button
                type="button"
                className="button small"
                disabled={loading || page === 1}
                onClick={() => setPage((n) => n - 1)}
              >
                Previous domains
              </Button>
              <span>
                {page} / {pages}
              </span>
              <Button
                type="button"
                className="button small"
                disabled={loading || page === pages}
                onClick={() => setPage((n) => n + 1)}
              >
                More domains
              </Button>
            </div>
          )}
          <label className="check">
            <input
              type="checkbox"
              checked={proxied}
              disabled={disabled}
              onChange={(e) => setProxied(e.target.checked)}
            />{" "}
            Use Cloudflare proxy
          </label>
          {name && (
            <p className="muted">
              {name} · DNS value fills with this VPS’s enrolled IP during
              review.
            </p>
          )}
          {loading && (
            <p className="muted" role="status">
              Loading Cloudflare domains…
            </p>
          )}
          {error && <p className="route-error">{error}</p>}
          <Button
            type="button"
            className="button small"
            disabled={disabled || loading}
            onClick={() => setAdding(true)}
          >
            <Plus size={15} />
            {providers.length ? "Add account" : "Connect Cloudflare"}
          </Button>
          {error && (
            <Button
              type="button"
              className="button small"
              onClick={() => setRevision((n) => n + 1)}
            >
              Retry
            </Button>
          )}
        </>
      )}
      {adding && (
        <ConnectProvider
          provider={null}
          close={() => setAdding(false)}
          done={() => {
            setAdding(false);
            setRevision((n) => n + 1);
          }}
        />
      )}
    </div>
  );
}
