// The bridge that lets a caller outside the conversation (the session menu,
// the palette) ask a conversation to open Find, optionally on a query and on
// one entry. Same per-ref request shape as composer/composerFocus.ts: the
// request waits in the store until the addressed conversation consumes it, so
// it survives the pane mounting after the ask.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface FindRequest {
  /** Monotonic, so two asks in a row are two events. */
  id: number;
  /** The pane the ask is for; null reaches any seat showing the ref. */
  target: string | null;
  /** Words to look for; omitted keeps whatever the bar already holds. */
  query?: string;
  /** The source item to land on, when the caller already picked a match. */
  itemId?: string;
}

interface FindRequestState {
  requests: Map<string, FindRequest>;
}

let nextId = 1;

const store = createStore<FindRequestState>(() => ({ requests: new Map() }));

export function requestFindInSession(ref: string, options: Partial<Omit<FindRequest, "id">> = {}): void {
  store.setState((state) => {
    const requests = new Map(state.requests);
    requests.set(ref, { ...options, id: nextId++, target: options.target ?? null });
    return { requests };
  });
}

export function consumeFindRequest(ref: string): void {
  store.setState((state) => {
    if (!state.requests.has(ref)) return state;
    const requests = new Map(state.requests);
    requests.delete(ref);
    return { requests };
  });
}

export function useFindRequest(ref: string, paneId: string): FindRequest | undefined {
  return useStore(store, (state) => {
    const request = state.requests.get(ref);
    if (!request) return undefined;
    return request.target === null || request.target === paneId ? request : undefined;
  });
}

export function resetFindRequestsForTests(): void {
  store.setState({ requests: new Map() });
}
