// Module-singleton toast queue, read via React's useSyncExternalStore.
// A singleton (rather than context) is what lets useToasts() work from any
// component while <Toast/> is mounted once, elsewhere, with no provider
// wiring - the two sides only need to agree on this module. zustand is an
// existing dependency of this project, but nothing under src/ uses it yet
// (src/stores/ does not exist in this worktree) and a toast queue needs
// none of its middleware/selector machinery, so this is a plain
// subscribe/getSnapshot pair instead - the smallest thing that works.

export type ToastKind = "success" | "error" | "warning" | "info";

export interface ToastRecord {
  id: string;
  kind: ToastKind;
  text: string;
}

type Listener = () => void;

let toasts: ToastRecord[] = [];
let nextId = 0;
const listeners = new Set<Listener>();

function emit(): void {
  for (const listener of listeners) listener();
}

export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getToasts(): ToastRecord[] {
  return toasts;
}

export function pushToast(kind: ToastKind, text: string): string {
  const id = `toast-${nextId++}`;
  toasts = [...toasts, { id, kind, text }];
  emit();
  return id;
}

export function dismissToast(id: string): void {
  toasts = toasts.filter((toast) => toast.id !== id);
  emit();
}

/** Test-only: clears the queue and resets id numbering between tests, so
 * one test file's pushes never leak into the next test. Not part of the
 * widget's public surface (not re-exported from ./index). */
export function resetToastStoreForTests(): void {
  toasts = [];
  nextId = 0;
  emit();
}
