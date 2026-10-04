import { useId, useRef, useState, type ReactNode } from "react";
import { Activity, Download, Server, ShieldCheck } from "lucide-react";
import Button from "./Button";

const tabs = [
  { id: "connection", label: "Connection", icon: Server },
  { id: "access", label: "Access & health", icon: Activity },
  { id: "security", label: "Security", icon: ShieldCheck },
  { id: "updates", label: "Updates", icon: Download },
] as const;
type Tab = (typeof tabs)[number]["id"];

export default function ServerDetailsTabs(content: Record<Tab, ReactNode>) {
  const id = useId();
  const [selected, setSelected] = useState<Tab>("connection");
  const buttons = useRef<(HTMLButtonElement | null)[]>([]);
  return (
    <div className="server-details-tabs">
      <div className="tabs" role="tablist" aria-label="Server details">
        {tabs.map((tab, index) => (
          <Button
            key={tab.id}
            ref={(element) => {
              buttons.current[index] = element;
            }}
            type="button"
            role="tab"
            id={`${id}-${tab.id}-tab`}
            aria-controls={`${id}-${tab.id}-panel`}
            aria-selected={selected === tab.id}
            tabIndex={selected === tab.id ? 0 : -1}
            className={selected === tab.id ? "selected" : ""}
            onClick={() => setSelected(tab.id)}
            onKeyDown={(event) => {
              let next = index;
              if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
              else if (event.key === "ArrowLeft")
                next = (index + tabs.length - 1) % tabs.length;
              else if (event.key === "Home") next = 0;
              else if (event.key === "End") next = tabs.length - 1;
              else return;
              event.preventDefault();
              setSelected(tabs[next].id);
              buttons.current[next]?.focus();
            }}
          >
            <tab.icon size={17} aria-hidden="true" />
            {tab.label}
          </Button>
        ))}
      </div>
      {tabs.map((tab) => (
        <div
          key={tab.id}
          role="tabpanel"
          id={`${id}-${tab.id}-panel`}
          aria-labelledby={`${id}-${tab.id}-tab`}
          hidden={selected !== tab.id}
          tabIndex={0}
        >
          {content[tab.id]}
        </div>
      ))}
    </div>
  );
}
