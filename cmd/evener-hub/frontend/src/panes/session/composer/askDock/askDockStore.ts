// The app's one ask-dock store: the package's framework-free factory, wired
// here at module scope to threadsStore and its plain send() path, and bound to
// zustand's useStore for the reactive read. Wiring at module load rather than
// in a component effect is what lets a session's FIRST hydrate (which can
// complete before any AskDock ever mounts) populate batches immediately, and it
// mirrors threads.ts's own connectionStore.subscribe wiring. A dock's
// in-progress answers and in-flight-send state live here, not in component
// state, so they survive a dockview pane remount. The store's rules - batch
// reconciliation, answer drafts, the visible tab, the greeting, the send - are
// the package's (appwire-client/typescript/askDock.ts).
import { type AskDockState, createAskDockStore } from "@evener/appwire-client";
import { useStore } from "zustand";
import { threadsStore } from "../../../../stores/threads";

export type { AskAnswerState, AskDockRefState, AskDockState, SendBatchOutcome } from "@evener/appwire-client";
export { nextUnansweredKey } from "@evener/appwire-client";

export const askDockStore = createAskDockStore();
askDockStore.wire(threadsStore, (ref, text) => threadsStore.getState().send(ref, text));

export function useAskDockStore(): AskDockState;
export function useAskDockStore<T>(selector: (state: AskDockState) => T): T;
export function useAskDockStore<T>(selector?: (state: AskDockState) => T): T | AskDockState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(askDockStore, selector) : useStore(askDockStore);
}

// useAskDockPending is the seam a composer-surface owner (Composer.tsx,
// Session.tsx) reads to decide whether to hide/inert the plain composer for
// `ref`. Defined here, next to the store it selects from, so the predicate
// exists exactly once: askDockPending.ts (the composer's lean chunk seam)
// and askDock/index.ts both re-export it rather than each carrying their
// own verbatim copy.
export function useAskDockPending(ref: string): boolean {
  return useAskDockStore((s) => (s.byRef.get(ref)?.batches.length ?? 0) > 0);
}

// resetAskDockStoreForTests resets this module's singleton state between
// tests - same rationale as threads.ts's resetThreadsStoreForTests (one
// Map shared by the whole app). No production code should call this.
export function resetAskDockStoreForTests(): void {
  askDockStore.setState(askDockStore.getInitialState());
}
