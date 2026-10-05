import { useState } from "react";
import { Copy, KeyRound, RefreshCw } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";

const sizes = [8, 16, 32] as const;

// Same output as `openssl rand -hex N`: N random bytes as 2N hex characters.
function randomHex(bytes: number): string {
  const buffer = new Uint8Array(bytes);
  crypto.getRandomValues(buffer);
  return Array.from(buffer, (b) => b.toString(16).padStart(2, "0")).join("");
}

export default function SecretGenerator() {
  const [size, setSize] = useState<(typeof sizes)[number]>(16);
  const [value, setValue] = useState(() => randomHex(16));
  return (
    <section className="secret-generator" aria-label="Generate a secret">
      <div className="secret-generator-head">
        <KeyRound size={14} aria-hidden="true" />
        <strong>Generate secret</strong>
        <span>openssl rand -hex {size}</span>
        <div className="segments" role="tablist" aria-label="Secret length">
          {sizes.map((n) => (
            <Button
              key={n}
              type="button"
              role="tab"
              aria-selected={size === n}
              className={size === n ? "selected" : ""}
              onClick={() => {
                setSize(n);
                setValue(randomHex(n));
              }}
            >
              {n}
            </Button>
          ))}
        </div>
      </div>
      <div className="secret-generator-value">
        <code title={`${size} bytes · ${size * 2} hex characters`}>
          {value}
        </code>
        <Button
          type="button"
          className="icon-button"
          aria-label="Generate another"
          title="Generate another"
          onClick={() => setValue(randomHex(size))}
        >
          <RefreshCw size={14} />
        </Button>
        <Button
          type="button"
          className="button small"
          onClick={() => {
            void navigator.clipboard.writeText(value);
            toast.success("Copied");
          }}
        >
          <Copy size={13} /> Copy
        </Button>
      </div>
    </section>
  );
}
