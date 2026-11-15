import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { requireClass } from "../internal/requireClass";
import { usePresence } from "../internal/usePresence";
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

// One frozen object for the whole app, not a fresh literal per render: push is
// a module function with no per-caller state, and callers legitimately put the
// hook's result in a dependency array. A new object each render silently
// re-runs every such effect on every render - in Composer's case an effect
// that writes the active recovery draft to IndexedDB, which republishes the
// projection, which re-renders, which writes again.
const TOASTS: { push: (kind: ToastKind, text: string, reason?: string) => void } = Object.freeze({ push: pushToast });

/**
 * `push(kind, text, reason?)` enqueues a toast for the `<Toast/>` region
 * (mounted once, e.g. near the app root) to display. The optional reason names
 * which outcome the toast reports (rendered as `data-reason`). Toasts always
 * auto-dismiss - there is no imperative dismiss in this hook's return value.
 */
export function useToasts(): { push: (kind: ToastKind, text: string, reason?: string) => void } {
  return TOASTS;
}

interface ShownToast {
  record: ToastRecord;
  leaving: boolean;
}

// The rendered list: every queued toast in queue order, plus each toast that
// has just left the queue, held where it was until its exit has played.
function reconcile(shown: readonly ShownToast[], queued: readonly ToastRecord[]): ShownToast[] {
  const live = new Map(queued.map((record) => [record.id, record]));
  const next: ShownToast[] = shown.map(({ record }) => {
    const current = live.get(record.id);
    return current ? { record: current, leaving: false } : { record, leaving: true };
  });
  const held = new Set(shown.map(({ record }) => record.id));
  for (const record of queued) if (!held.has(record.id)) next.push({ record, leaving: false });
  return next;
}

/** The aria-live="polite" region that renders the active toast queue. A
 * dismissed toast floats out (the Float recipe) before it leaves the DOM; its
 * dismissal timing is unchanged. */
export function Toast() {
  const toasts = useSyncExternalStore(subscribe, getToasts);
  const [queued, setQueued] = useState(toasts);
  const [shown, setShown] = useState<ShownToast[]>(() => reconcile([], toasts));
  if (queued !== toasts) {
    setQueued(toasts);
    setShown((current) => reconcile(current, toasts));
  }
  const forget = useCallback((id: string) => {
    setShown((current) => current.filter((entry) => !(entry.leaving && entry.record.id === id)));
  }, []);
  return (
    <section className={CLASS.region} aria-live="polite" aria-label="Notifications">
      {shown.map(({ record, leaving }) => (
        <ToastItem key={record.id} toast={record} leaving={leaving} onExited={forget} />
      ))}
    </section>
  );
}

function ToastItem({
  toast,
  leaving,
  onExited,
}: {
  toast: ToastRecord;
  leaving: boolean;
  onExited: (id: string) => void;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const { mounted, phase } = usePresence(!leaving, rootRef, { onExited: () => onExited(toast.id) });
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // True pause/resume, not restart: hovering right before a toast would
  // have dismissed must not grant it a fresh full-length reprieve.
  // remainingMsRef holds what's left of the budget; startedAtRef is when
  // the currently-running timer (if any) was last (re)started, so pause()
  // can compute how much of THIS stretch actually elapsed and subtract it.
  const remainingMsRef = useRef(AUTO_DISMISS_MS);
  const startedAtRef = useRef(0);

  // useCallback so the mount effect below can depend on it honestly: its
  // only real dependency is toast.id, which - list is keyed by toast.id
  // (Toast above) - never actually changes within one ToastItem instance's
  // lifetime anyway (a different id remounts a fresh instance instead).
  const schedule = useCallback(
    (durationMs: number) => {
      startedAtRef.current = Date.now();
      timerRef.current = setTimeout(() => dismissToast(toast.id), durationMs);
    },
    [toast.id],
  );

  useEffect(() => {
    remainingMsRef.current = AUTO_DISMISS_MS;
    schedule(AUTO_DISMISS_MS);
    return () => clearTimeout(timerRef.current);
  }, [schedule]);

  function pause() {
    clearTimeout(timerRef.current);
    const elapsed = Date.now() - startedAtRef.current;
    remainingMsRef.current = Math.max(0, remainingMsRef.current - elapsed);
  }

  function resume() {
    schedule(remainingMsRef.current);
  }

  if (!mounted) return null;
  return (
    // Pointer-only convenience (pause the auto-dismiss clock while a sighted
    // mouse user is reading), not a new interactive affordance needing a
    // role: there's nothing to operate here, and a screen reader user isn't
    // disadvantaged by its absence - the region's aria-live="polite"
    // (Toast above) announces this toast's text once, on insertion,
    // independent of how long the DOM node then sticks around for.
    // biome-ignore lint/a11y/noStaticElementInteractions: pointer-only pause convenience, aria-live already covers AT users, see above
    <div
      ref={rootRef}
      data-motion={phase}
      inert={phase === "closing"}
      className={`${CLASS.toast} ${KIND_CLASS[toast.kind]}`}
      data-kind={toast.kind}
      data-reason={toast.reason}
      onMouseEnter={pause}
      onMouseLeave={resume}
    >
      <p className={CLASS.text}>{toast.text}</p>
    </div>
  );
}
