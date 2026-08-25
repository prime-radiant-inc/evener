import type { StoreApi } from "zustand/vanilla";
import type { PrototypeAction, PrototypeState } from "./state";
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

function isDuplicateDestination(
  state: PrototypeState,
  action: PrototypeAction,
): boolean {
  if (action.type === "openOverlay") return state.overlay === action.overlay;
  if (state.overlay !== null) return false;

  switch (action.type) {
    case "openSession":
      return (
        state.route.kind === "conversation" &&
        state.route.sessionId === action.sessionId &&
        state.route.focusItemId === action.focusItemId
      );
    case "openSearchResult": {
      const result = state.projection.fixture.search.find(
        ({ id }) => id === action.resultId,
      );
      return (
        result !== undefined &&
        state.route.kind === "conversation" &&
        state.route.sessionId === result.sessionId &&
        state.route.focusItemId === (result.itemId ?? undefined)
      );
    }
    case "openWork":
      return (
        state.route.kind === "work" &&
        state.route.sessionId === action.sessionId
      );
    case "openVoice":
      return (
        state.route.kind === "voice" &&
        state.route.sessionId === action.sessionId
      );
    default:
      return false;
  }
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
  let reconcilingRoot = false;
  let disposed = false;
  const deferredActions: PrototypeAction[] = [];

  const replaceRoot = () => {
    ownedDepth = 0;
    pendingRoutePops = 0;
    reconcilingRoot = false;
    historyTarget.history.replaceState(historyState(0), "");
  };

  const reconcileRoot = () => {
    pendingRoutePops = 0;
    if (ownedDepth === 0) {
      replaceRoot();
      return;
    }
    reconcilingRoot = true;
    historyTarget.history.go(-ownedDepth);
  };

  const reduce = (action: PrototypeAction) => {
    const before = store.getState();
    if (isDuplicateDestination(before, action)) return;
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
    if (
      action.type === "navigateRoot" ||
      action.type === "setScenario" ||
      action.type === "reset"
    ) {
      reconcileRoot();
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
    if (reconcilingRoot) {
      if (isConceptHistoryState(event.state) && event.state.depth === 0) {
        replaceRoot();
        const actions = deferredActions.splice(0);
        for (const action of actions) dispatchAction(action);
      } else {
        failClosed();
      }
      return;
    }
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

  const dispatchAction = (action: PrototypeAction) => {
    if (disposed) return;
    if (reconcilingRoot) {
      deferredActions.push(action);
      return;
    }
    if (action.type === "goBack") {
      historyTarget.history.back();
      return;
    }
    reduce(action);
  };

  replaceRoot();
  historyTarget.addEventListener("popstate", onPopState);

  return {
    dispatch: dispatchAction,
    dispose() {
      if (disposed) return;
      disposed = true;
      historyTarget.removeEventListener("popstate", onPopState);
    },
  };
}
