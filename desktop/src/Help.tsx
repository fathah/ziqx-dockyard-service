import Button from "./Button";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { CircleHelp } from "lucide-react";
import "./help.css";

/** Secondary information only. Errors and consequential action warnings stay inline. */
export default function Help({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const bubble = useRef<HTMLSpanElement>(null);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ x: 0, y: 0 });
  const cancelHide = () => {
    if (hideTimer.current) clearTimeout(hideTimer.current);
  };
  const show = () => {
    cancelHide();
    setOpen(true);
  };
  const hide = () => {
    if (document.activeElement === trigger.current) return;
    cancelHide();
    hideTimer.current = setTimeout(() => setOpen(false), 80);
  };
  useLayoutEffect(() => {
    if (!open || !trigger.current || !bubble.current) return;
    const anchor = trigger.current.getBoundingClientRect();
    const rect = bubble.current.getBoundingClientRect();
    const half = rect.width / 2;
    const below = anchor.bottom + 10;
    setPosition({
      x: Math.max(
        half + 12,
        Math.min(innerWidth - half - 12, anchor.left + anchor.width / 2),
      ),
      y:
        below + rect.height <= innerHeight - 12
          ? below
          : Math.max(12, anchor.top - rect.height - 10),
    });
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const escape = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        cancelHide();
        setOpen(false);
      }
    };
    const close = () => setOpen(false);
    const scroll = (e: Event) => {
      if (!bubble.current?.contains(e.target as Node)) close();
    };
    const outside = (e: PointerEvent) => {
      if (
        !trigger.current?.contains(e.target as Node) &&
        !bubble.current?.contains(e.target as Node)
      )
        close();
    };
    document.addEventListener("keydown", escape, true);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("keydown", escape, true);
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);
  useEffect(() => () => cancelHide(), []);
  return (
    <span className="help-inline">
      <Button
        ref={trigger}
        type="button"
        className="help-trigger t-tt-trigger"
        aria-label={`About ${label}`}
        aria-describedby={open ? id : undefined}
        aria-expanded={open}
        onMouseEnter={show}
        onMouseLeave={hide}
        onFocus={show}
        onBlur={hide}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          show();
        }}
      >
        <CircleHelp size={15} aria-hidden="true" />
      </Button>
      <span className="help-description" id={id} aria-hidden={!open}>
        {children}
      </span>
      {createPortal(
        <span
          className="t-tt-wrap help-layer"
          data-open={open}
          style={{ left: position.x, top: position.y }}
        >
          <span
            ref={bubble}
            className="t-tt help-tip"
            role="tooltip"
            aria-hidden={!open}
            onMouseEnter={show}
            onMouseLeave={hide}
          >
            {children}
          </span>
        </span>,
        document.body,
      )}
    </span>
  );
}
