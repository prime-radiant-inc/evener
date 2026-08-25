import type { StoreApi } from "zustand/vanilla";
import type { PrototypeAction } from "./state";
import type { PrototypeStore } from "./store";

export interface NavigationController {
  dispatch(action: PrototypeAction): void;
  dispose(): void;
}

export interface ConceptHistoryState {
  owner: "evener-concepts";
  depth: number;
  key: string;
}

const forwardActions: ReadonlySet<PrototypeAction["type"]> = new Set([
  "openSession",
  "openSearchResult",
  "completeNewSession",
  "openWork",
  "openVoice",
  "openOverlay",
]);

function shouldPush(action: PrototypeAction): boolean {
  return (
    forwardActions.has(action.type) &&
    (action.type !== "completeNewSession" || action.result === "success")
  );
}

let nextHistoryKey = 0;

function historyState(depth: number): ConceptHistoryState {
  nextHistoryKey += 1;
  return {
    owner: "evener-concepts",
    depth,
    key: `concept-history-${nextHistoryKey}`,
  };
}

function isConceptHistoryState(value: unknown): value is ConceptHistoryState {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<ConceptHistoryState>;
  return (
    candidate.owner === "evener-concepts" &&
    Number.isSafeInteger(candidate.depth) &&
    (candidate.depth ?? -1) >= 0 &&
    typeof candidate.key === "string" &&
    candidate.key.length > 0
  );
}

export function createNavigationController(
  store: StoreApi<PrototypeStore>,
  historyTarget: Window,
): NavigationController {
  let ownedDepth = 0;
  let pendingRoutePops = 0;
  let suppressHistory = false;
  let disposed = false;

  const replaceRoot = () => {
    ownedDepth = 0;
    pendingRoutePops = 0;
    historyTarget.history.replaceState(historyState(0), "");
  };

  const reduce = (action: PrototypeAction) => {
    const before = store.getState();
    if (action.type === "reset") before.resetPrototype();
    else before.dispatch(action);
    const after = store.getState();

    if (suppressHistory) return;
    if (
      action.type === "endVoice" &&
      before.route.kind === "voice" &&
      after.route.kind !== "voice"
    ) {
      // The canonical reducer records End by popping Voice immediately. The
      // following browser Back must consume that owned depth without popping
      // the reducer stack a second time.
      pendingRoutePops += 1;
      return;
    }
    if (action.type === "navigateRoot" || action.type === "reset") {
      replaceRoot();
      return;
    }
    if (before !== after && shouldPush(action)) {
      ownedDepth += 1;
      historyTarget.history.pushState(historyState(ownedDepth), "");
    }
  };

  const reduceBack = () => reduce({ type: "goBack" });

  const failClosed = () => {
    suppressHistory = true;
    try {
      let state = store.getState();
      while (state.overlay !== null || state.history.length > 0) {
        const before = state;
        reduceBack();
        state = store.getState();
        if (state === before) break;
      }
    } finally {
      suppressHistory = false;
    }
    replaceRoot();
  };

  const onPopState = (event: PopStateEvent) => {
    if (!isConceptHistoryState(event.state) || event.state.depth > ownedDepth) {
      failClosed();
      return;
    }

    const targetDepth = event.state.depth;
    suppressHistory = true;
    try {
      while (ownedDepth > targetDepth) {
        if (pendingRoutePops > 0) pendingRoutePops -= 1;
        else reduceBack();
        ownedDepth -= 1;
      }
    } finally {
      suppressHistory = false;
    }
  };

  replaceRoot();
  historyTarget.addEventListener("popstate", onPopState);

  return {
    dispatch(action) {
      if (disposed) return;
      if (action.type === "goBack") {
        historyTarget.history.back();
        return;
      }
      reduce(action);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      historyTarget.removeEventListener("popstate", onPopState);
    },
  };
}
