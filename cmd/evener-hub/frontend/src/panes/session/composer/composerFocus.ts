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
}

interface ComposerFocusState {
  requests: Map<string, ComposerFocusRequest>;
  requestComposerFocus(ref: string): void;
  consumeComposerFocus(ref: string): void;
}

let nextId = 1;

const store = createStore<ComposerFocusState>((set) => ({
  requests: new Map(),
  requestComposerFocus: (ref) =>
    set((state) => {
      const requests = new Map(state.requests);
      requests.set(ref, { id: nextId++ });
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
export function requestComposerFocus(ref: string): void {
  store.getState().requestComposerFocus(ref);
}

/** Composer's write side: clears a request once it has been acted on. */
export function consumeComposerFocus(ref: string): void {
  store.getState().consumeComposerFocus(ref);
}

/** Composer's read side: the pending request for `ref`, if any. */
export function useComposerFocusRequest(ref: string): ComposerFocusRequest | undefined {
  return useStore(store, (s) => s.requests.get(ref));
}

/** Test-only: clears every pending request - mirrors quoteInsert.ts's own
 * resetQuoteInsertStoreForTests (see that function's doc comment). */
export function resetComposerFocusStoreForTests(): void {
  store.setState({ requests: new Map() });
}
