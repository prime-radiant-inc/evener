// quoteInsert.ts: the pub/sub bridge that lets SelectionQuote (mounted at
// the transcript root, a sibling of Composer under Session.tsx) hand a
// quoted markdown block to that ref's Composer instance without either side
// depending on the other's internals - SelectionQuote never imports
// Composer, and Composer never imports SelectionQuote. Mirrors the
// vanilla-store-keyed-by-ref pattern the rest of this pane's stores use
// (askDock/askDockStore.ts's own header comment), which is also what lets a
// request outlive a target pane remount if one ever raced with dockview
// unmounting/remounting the session tree on a tab switch (Session.tsx's own
// header comment) - not a case this feature exercises today (the bar only
// ever renders while its own pane is mounted and focused), but the same
// shape as every other cross-component seam in this pane, not a bespoke
// one-off.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface QuoteInsertRequest {
  // Monotonic, not derived from `text`: two requests for the same ref can
  // carry identical text (quoting the same line twice in a row), and a
  // subscriber must still see that as two separate events, not one it
  // already handled.
  id: number;
  text: string;
}

interface QuoteInsertState {
  requests: Map<string, QuoteInsertRequest>;
  requestQuoteInsert(ref: string, text: string): void;
  consumeQuoteInsert(ref: string): void;
}

let nextId = 1;

const store = createStore<QuoteInsertState>((set) => ({
  requests: new Map(),
  requestQuoteInsert: (ref, text) =>
    set((state) => {
      const requests = new Map(state.requests);
      requests.set(ref, { id: nextId++, text });
      return { requests };
    }),
  consumeQuoteInsert: (ref) =>
    set((state) => {
      if (!state.requests.has(ref)) return state;
      const requests = new Map(state.requests);
      requests.delete(ref);
      return { requests };
    }),
}));

/** SelectionQuote's write side: records a quote block waiting for `ref`'s Composer. */
export function requestQuoteInsert(ref: string, text: string): void {
  store.getState().requestQuoteInsert(ref, text);
}

/** Composer's write side: clears a request once it has been merged into the draft. */
export function consumeQuoteInsert(ref: string): void {
  store.getState().consumeQuoteInsert(ref);
}

/** Composer's read side: the pending request for `ref`, if any. */
export function useQuoteInsertRequest(ref: string): QuoteInsertRequest | undefined {
  return useStore(store, (s) => s.requests.get(ref));
}

/** Test-only: clears every pending request, mirroring this pane's other
 * module-singleton stores' own resetXForTests (askDockStore.ts,
 * threads.ts) - without it, an unconsumed request from one test (nothing
 * ever mounts a Composer for that ref to consume it) would still be
 * sitting here for a later test that reuses the same ref. */
export function resetQuoteInsertStoreForTests(): void {
  store.setState({ requests: new Map() });
}
