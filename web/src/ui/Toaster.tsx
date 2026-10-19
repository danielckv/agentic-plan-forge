import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from "lucide-react";

import { toastStore, useToasts } from "./toasts";
import type { Toast, ToastKind } from "./toasts";

/**
 * Top-right toast stack. The container is the single polite live region
 * (`role="region"` + `aria-live`), so a screen reader hears every notice once;
 * individual toasts carry no `role` of their own, which would nest a second
 * live region and double-announce. Every toast has a dismiss button, and the
 * pointer or keyboard focus on a toast holds its auto-dismiss timer.
 */

const KIND_ICON: Record<ToastKind, React.ComponentType<{ size?: number | string }>> = {
  info: Info,
  success: CircleCheck,
  warning: TriangleAlert,
  error: CircleAlert,
};

const KIND_LABEL: Record<ToastKind, string> = {
  info: "Notice",
  success: "Done",
  warning: "Warning",
  error: "Error",
};

export function Toaster(): React.ReactElement {
  const toasts = useToasts();
  return (
    <div className="pf-toaster" role="region" aria-label="Notifications" aria-live="polite">
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </div>
  );
}

function ToastItem({ toast }: { toast: Toast }): React.ReactElement {
  const Icon = KIND_ICON[toast.kind];
  return (
    <div
      className="pf-toast"
      data-kind={toast.kind}
      data-leaving={toast.leaving}
      onMouseEnter={() => toastStore.pause(toast.id)}
      onMouseLeave={() => toastStore.resume(toast.id)}
      onFocus={() => toastStore.pause(toast.id)}
      onBlur={() => toastStore.resume(toast.id)}
    >
      <span className="pf-toast-icon">
        <Icon size={15} />
      </span>
      <p className="pf-toast-body">
        <span className="pf-sr-only">{KIND_LABEL[toast.kind]}: </span>
        {toast.message}
      </p>
      {toast.count > 1 && (
        <span className="pf-toast-count" aria-label={`${String(toast.count)} times`}>
          ×{toast.count}
        </span>
      )}
      <button
        type="button"
        className="pf-toast-close"
        aria-label="Dismiss notification"
        onClick={() => toastStore.dismiss(toast.id)}
      >
        <X size={14} />
      </button>
    </div>
  );
}
