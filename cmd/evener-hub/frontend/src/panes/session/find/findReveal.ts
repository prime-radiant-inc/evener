// What Find is landed on in each transcript view, for a renderer that folds
// its text away: the call's row shows an excerpt of the stretch holding the
// words instead of the words themselves. One landing per view (its disclosure
// scope), so two views of one conversation do not disturb each other; a
// landing clears when Find moves on or closes.
import { useSyncExternalStore } from "react";

interface Landing {
  readonly itemId: string;
  readonly needle: string;
}

let landings = new Map<string, Landing>();
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

/** `needle` is the shared find needle (findNeedle); a null item clears the
 * view's landing. */
export function revealForFind(scope: string, itemId: string | null, needle: string): void {
  const current = landings.get(scope);
  if (itemId === null ? current === undefined : current?.itemId === itemId && current.needle === needle) return;
  const next = new Map(landings);
  if (itemId === null) next.delete(scope);
  else next.set(scope, { itemId, needle });
  landings = next;
  emit();
}

/** The needle Find is landed on in `itemId`, in the transcript view this row
 * belongs to, or "" when it is not the landing. */
export function useFindReveal(scope: string, itemId: string): string {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => {
      const landing = landings.get(scope);
      return landing?.itemId === itemId ? landing.needle : "";
    },
  );
}

export function resetFindRevealForTests(): void {
  landings = new Map();
  emit();
}
