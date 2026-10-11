import { useEffect, useRef, useState } from "react";
import { ShieldCheck } from "lucide-react";
import Button from "./Button";
import * as api from "./api";

export default function DomainHTTPS({
  project,
  hostname,
  revision,
  disabled,
}: {
  project: string;
  hostname: string;
  revision: string;
  disabled: boolean;
}) {
  const [checked, setChecked] = useState<api.DomainHTTPSCheck>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const sequence = useRef(0);
  useEffect(() => {
    sequence.current++;
    setChecked(undefined);
    setError("");
    setBusy(false);
    return () => {
      sequence.current++;
    };
  }, [project, hostname, revision]);
  const messages: Record<string, string> = {
    dns_failed: "DNS could not resolve",
    private_address: "Domain resolves to a private address",
    invalid_certificate: "Certificate verification failed",
    https_failed: "HTTPS connection failed",
    invalid_hostname: "Invalid domain",
  };
  const text =
    error ||
    (checked?.state === "verified"
      ? `HTTPS verified · HTTP ${checked.http_status}`
      : checked
        ? (messages[checked.state] ?? "HTTPS check failed")
        : "HTTPS not checked");
  return (
    <div className="domain-https">
      <span
        role="status"
        className={
          error || (checked && checked.state !== "verified") ? "error" : "muted"
        }
        title={
          checked
            ? `Checked from this Mac at ${new Date(checked.checked_at * 1000).toLocaleTimeString()}. Certificate validity and HTTP response only.`
            : "Check this domain's public certificate and HTTP response from this Mac."
        }
      >
        {text}
      </span>
      <Button
        type="button"
        className="button small"
        disabled={disabled || busy}
        onClick={async () => {
          const current = ++sequence.current;
          setBusy(true);
          setError("");
          setChecked(undefined);
          try {
            const result = await api.checkDomainHTTPS(project, hostname);
            if (sequence.current === current) setChecked(result);
          } catch (e) {
            if (sequence.current === current)
              setError(
                String(e).includes("SESSION_LOCKED")
                  ? "Unlock Dockyard to check HTTPS"
                  : String(e).includes("DOMAIN_NOT_ASSIGNED")
                    ? "Refresh this domain's assignment"
                    : "HTTPS check could not complete. Retry.",
              );
          } finally {
            if (sequence.current === current) setBusy(false);
          }
        }}
      >
        <ShieldCheck size={15} />
        {busy ? "Checking HTTPS…" : "Check HTTPS"}
      </Button>
    </div>
  );
}
