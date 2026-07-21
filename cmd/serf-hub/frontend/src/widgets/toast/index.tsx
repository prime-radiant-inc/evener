import { useEffect, useRef, useSyncExternalStore } from "react";
import { requireClass } from "../internal/requireClass";
import { dismissToast, getToasts, pushToast, subscribe, type ToastKind, type ToastRecord } from "./store";
import styles from "./toast.module.css";

export type { ToastKind } from "./store";

const AUTO_DISMISS_MS = 5000;

const CLASS = {
  region: requireClass(styles.region, "toast.module.css", "region"),
  toast: requireClass(styles.toast, "toast.module.css", "toast"),
  text: requireClass(styles.text, "toast.module.css", "text"),
};

const KIND_CLASS: Record<ToastKind, string> = {
  success: requireClass(styles.success, "toast.module.css", "success"),
  error: requireClass(styles.error, "toast.module.css", "error"),
  warning: requireClass(styles.warning, "toast.module.css", "warning"),
  info: requireClass(styles.info, "toast.module.css", "info"),
};

/**
 * `push(kind, text)` enqueues a toast for the `<Toast/>` region (mounted
 * once, e.g. near the app root) to display. Toasts always auto-dismiss -
 * there is no imperative dismiss in this hook's return value.
 */
export function useToasts(): { push: (kind: ToastKind, text: string) => void } {
  return { push: pushToast };
}

/** The aria-live="polite" region that renders the active toast queue. */
export function Toast() {
  const toasts = useSyncExternalStore(subscribe, getToasts);
  return (
    <div className={CLASS.region} role="region" aria-live="polite" aria-label="Notifications">
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} />
      ))}
    </div>
  );
}

function ToastItem({ toast }: { toast: ToastRecord }) {
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  function schedule() {
    timerRef.current = setTimeout(() => dismissToast(toast.id), AUTO_DISMISS_MS);
  }

  useEffect(() => {
    schedule();
    return () => clearTimeout(timerRef.current);
  }, [toast.id]);

  function pause() {
    clearTimeout(timerRef.current);
  }

  function resume() {
    schedule();
  }

  return (
    <div className={`${CLASS.toast} ${KIND_CLASS[toast.kind]}`} onMouseEnter={pause} onMouseLeave={resume}>
      <p className={CLASS.text}>{toast.text}</p>
    </div>
  );
}
