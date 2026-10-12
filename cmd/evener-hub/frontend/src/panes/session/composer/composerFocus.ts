// composerFocus.ts: the pub/sub bridge that lets a caller outside Composer
// (a shell worker's global Mod+I chord, per this file's own commissioning
// note) ask a specific ref's Composer to move keyboard focus into its own
// textarea, without either side depending on the other's internals. Mirrors
// quoteInsert.ts's per-ref vanilla-store pattern EXACTLY (see that file's
// own header comment for the fuller rationale - a request outliving a
// target pane remount, why a Map keyed by ref rather than one shared
// value): this is the same shape, just without a text payload, since a
// focus request carries no data beyond "focus now".
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface ComposerFocusRequest {
  // Monotonic, not a boolean flag: two focus requests fired back to back
  // (e.g. the chord pressed twice while the pane is already focused) must
  // still be two separate events a subscriber can tell apart, not one it
  // already handled - same rationale as QuoteInsertRequest.id.
  id: number;
  // The pane id of the seat the focus is meant for, for the same
  // two-composers-one-ref world as QuoteInsertRequest.target; null keeps
  // the historic any-composer routing.
  target: string | null;
}

interface ComposerFocusState {
  requests: Map<string, ComposerFocusRequest>;
  requestComposerFocus(ref: string, target: string | null): void;
  consumeComposerFocus(ref: string): void;
}

let nextId = 1;

const store = createStore<ComposerFocusState>((set) => ({
  requests: new Map(),
  requestComposerFocus: (ref, target) =>
    set((state) => {
      const requests = new Map(state.requests);
      requests.set(ref, { id: nextId++, target });
      return { requests };
    }),
  consumeComposerFocus: (ref) =>
    set((state) => {
      if (!state.requests.has(ref)) return state;
      const requests = new Map(state.requests);
      requests.delete(ref);
      return { requests };
    }),
}));

/** The caller's write side: asks `ref`'s Composer to focus its textarea. */
export function requestComposerFocus(ref: string, target: string | null = null): void {
  store.getState().requestComposerFocus(ref, target);
}

/** Composer's write side: clears a request only after a mounted textarea has
 * successfully received the corresponding `.focus()` call. Until then the
 * request deliberately survives renders and subscriber remounts. */
export function consumeComposerFocus(ref: string): void {
  store.getState().consumeComposerFocus(ref);
}

/** Composer's read side: the pending request for `ref` that belongs to this
 * seat, if any - same addressing rule as useQuoteInsertRequest. */
export function useComposerFocusRequest(ref: string, paneId?: string): ComposerFocusRequest | undefined {
  return useStore(store, (s) => {
    const request = s.requests.get(ref);
    if (!request) return undefined;
    if (paneId === undefined || request.target === null || request.target === paneId) return request;
    return undefined;
  });
}

/** Test-only: clears every pending request - mirrors quoteInsert.ts's own
 * resetQuoteInsertStoreForTests (see that function's doc comment). */
export function resetComposerFocusStoreForTests(): void {
  store.setState({ requests: new Map() });
}
