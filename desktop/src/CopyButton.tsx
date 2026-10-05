import { useState } from "react";
import { Check, Copy } from "lucide-react";
import toast from "react-hot-toast";
import Button from "./Button";

/** Small "Copy" button that puts text on the clipboard in one click. */
export default function CopyButton({
  text,
  label = "Copy",
}: {
  text: string;
  label?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      className="button small"
      disabled={!text}
      title={`Copy ${text.split("\n").length} lines`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast.error("Could not copy to the clipboard");
        }
      }}
    >
      {copied ? <Check size={13} /> : <Copy size={13} />} {copied ? "Copied" : label}
    </Button>
  );
}
