import Button from "./Button";
import { useId, useState, type ReactNode } from "react";
import { ChevronDown, type LucideIcon } from "lucide-react";
import "./Accordion.css";

export default function Accordion({
  title,
  icon: Icon,
  children,
  defaultOpen = false,
  className = "",
}: {
  title: ReactNode;
  icon?: LucideIcon;
  children: ReactNode;
  defaultOpen?: boolean;
  className?: string;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const id = useId();
  return (
    <div className={`t-acc accordion ${className}`} data-open={open}>
      <Button
        type="button"
        className="t-acc-head accordion-trigger"
        id={`${id}-trigger`}
        aria-expanded={open}
        aria-controls={`${id}-panel`}
        onClick={() => setOpen((value) => !value)}
      >
        {Icon && (
          <Icon className="accordion-icon" size={18} aria-hidden="true" />
        )}
        <span className="accordion-title">{title}</span>
        <span className="t-acc-chevron" aria-hidden="true">
          <ChevronDown size={18} />
        </span>
      </Button>
      <div
        className="t-acc-panel"
        id={`${id}-panel`}
        role="region"
        aria-labelledby={`${id}-trigger`}
        aria-hidden={!open}
        inert={!open}
      >
        <div className="t-acc-panel-inner">
          <div className="accordion-content">{children}</div>
        </div>
      </div>
    </div>
  );
}
