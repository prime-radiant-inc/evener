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
    <section className={CLASS.region} aria-live="polite" aria-label="Notifications">
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} />
      ))}
    </section>
  );
}

function ToastItem({ toast }: { toast: ToastRecord }) {
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // True pause/resume, not restart: hovering right before a toast would
  // have dismissed must not grant it a fresh full-length reprieve.
  // remainingMsRef holds what's left of the budget; startedAtRef is when
  // the currently-running timer (if any) was last (re)started, so pause()
  // can compute how much of THIS stretch actually elapsed and subtract it.
  const remainingMsRef = useRef(AUTO_DISMISS_MS);
  const startedAtRef = useRef(0);

  function schedule(durationMs: number) {
    startedAtRef.current = Date.now();
    timerRef.current = setTimeout(() => dismissToast(toast.id), durationMs);
  }

  useEffect(() => {
    remainingMsRef.current = AUTO_DISMISS_MS;
    schedule(AUTO_DISMISS_MS);
    return () => clearTimeout(timerRef.current);
  }, [toast.id]);

  function pause() {
    clearTimeout(timerRef.current);
    const elapsed = Date.now() - startedAtRef.current;
    remainingMsRef.current = Math.max(0, remainingMsRef.current - elapsed);
  }

  function resume() {
    schedule(remainingMsRef.current);
  }

  return (
    // Pointer-only convenience (pause the auto-dismiss clock while a sighted
    // mouse user is reading), not a new interactive affordance needing a
    // role: there's nothing to operate here, and a screen reader user isn't
    // disadvantaged by its absence - the region's aria-live="polite"
    // (Toast above) announces this toast's text once, on insertion,
    // independent of how long the DOM node then sticks around for.
    // biome-ignore lint/a11y/noStaticElementInteractions: pointer-only pause convenience, aria-live already covers AT users, see above
    <div className={`${CLASS.toast} ${KIND_CLASS[toast.kind]}`} onMouseEnter={pause} onMouseLeave={resume}>
      <p className={CLASS.text}>{toast.text}</p>
    </div>
  );
}
