import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";

import { copyText } from "./clipboard";
import { toastStore } from "./toasts";

const COPIED_MS = 1600;

/**
 * Icon button that copies `text`. It confirms in place ("Copied", announced
 * politely) and reports a refused copy as an error toast rather than failing
 * silently.
 */
export function CopyButton(props: { text: string; label?: string }): React.ReactElement {
  const { text, label = "Copy" } = props;
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const onClick = (): void => {
    void copyText(text).then((ok) => {
      if (!ok) {
        toastStore.push("error", "Copy failed — select the text and copy it manually.");
        return;
      }
      setCopied(true);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), COPIED_MS);
    });
  };

  return (
    <button
      type="button"
      className="pf-btn pf-btn-ghost pf-btn-sm pf-btn-icon pf-copy"
      data-copied={copied}
      aria-label={label}
      title={copied ? "Copied" : label}
      onClick={onClick}
    >
      {copied ? <Check size={13} /> : <Copy size={13} />}
      <span className="pf-sr-only" aria-live="polite">
        {copied ? "Copied" : ""}
      </span>
    </button>
  );
}
