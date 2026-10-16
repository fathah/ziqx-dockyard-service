import { useEffect, useState } from "react";
import { Copy, Network, RefreshCw } from "lucide-react";
import toast from "react-hot-toast";
import * as api from "./api";
import Button from "./Button";
import Help from "./Help";

function portError(error: unknown): string {
  const message = String(error);
  if (message.includes("PORT_POOL_EXHAUSTED"))
    return "No available ports in the configured range.";
  if (/HTTP_400|HTTP_404/.test(message))
    return "Update Dockyard on the VPS to check three ports.";
  if (/HTTP_403|SCOPE_REQUIRED/.test(message))
    return "This connection needs permission to check ports.";
  return "Could not check VPS ports. Refresh to try again.";
}

export default function AvailablePorts({ preview }: { preview: boolean }) {
  const [ports, setPorts] = useState<number[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let cancelled = false;
    setPorts([]);
    setError("");
    setLoading(true);
    if (preview) {
      setPorts([3016, 3017, 3018]);
      setLoading(false);
      return;
    }
    void api
      .read<{ ports: number[]; reserved: boolean }>({ kind: "port", count: 3 })
      .then((data) => {
        if (cancelled) return;
        if (
          !Array.isArray(data.ports) ||
          data.ports.length < 1 ||
          data.ports.length > 3 ||
          data.reserved !== false ||
          new Set(data.ports).size !== data.ports.length ||
          data.ports.some(
            (port) => !Number.isInteger(port) || port < 1 || port > 65535,
          )
        )
          throw new Error("INVALID_PORT_RESPONSE");
        setPorts(data.ports);
      })
      .catch((error) => {
        if (!cancelled) setError(portError(error));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [preview, refresh]);

  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      toast.success("Copied");
    } catch {
      toast.error("Could not copy to the clipboard");
    }
  }

  return (
    <section className="available-ports" aria-label="Next available ports">
      <div className="available-ports-head">
        <Network size={14} aria-hidden="true" />
        <strong>Next 3 available ports</strong>
        <Help label="available ports">
          Checks the VPS port range and skips ports already reserved or in use.
          Suggestions are not reserved; availability can change before
          deployment.
        </Help>
        <Button
          type="button"
          className="icon-button"
          aria-label="Refresh available ports"
          title="Refresh available ports"
          disabled={loading}
          onClick={() => setRefresh((value) => value + 1)}
        >
          <RefreshCw size={14} />
        </Button>
      </div>
      <div
        className="available-ports-values"
        aria-live="polite"
        aria-busy={loading}
      >
        {loading ? (
          <span className="available-ports-message">Checking VPS ports…</span>
        ) : error ? (
          <span className="available-ports-error" role="alert">
            {error}
          </span>
        ) : (
          <>
            {ports.map((port) => (
              <Button
                key={port}
                type="button"
                className="button small port-suggestion"
                title={`Copy port ${port}`}
                aria-label={`Copy port ${port}`}
                onClick={() => void copy(String(port))}
              >
                <code>{port}</code>
                <Copy size={12} />
              </Button>
            ))}
            <span className="available-ports-note">
              {preview
                ? "Sample ports"
                : ports.length < 3
                  ? `Only ${ports.length} available · not reserved`
                  : "Not reserved"}
            </span>
          </>
        )}
      </div>
    </section>
  );
}
