import { type WorkspaceStoreState, workspaceStore } from "../shell/workspace";

export interface PanelStoreEvictor {
  refs: () => Iterable<string>;
  evict: (ref: string) => void;
}

const evictors = new Set<PanelStoreEvictor>();
let evictionScheduled = false;
let evictionGeneration = 0;

function paneRef(pane: { type: string; params: unknown }): string | undefined {
  if (
    pane.type !== "session" &&
    pane.type !== "sessionTasks" &&
    pane.type !== "sessionActivity" &&
    pane.type !== "sessionDetails"
  ) {
    return undefined;
  }
  const params = pane.params;
  if (!params || typeof params !== "object" || Array.isArray(params)) return undefined;
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" && ref !== "" ? ref : undefined;
}

export function openPanelRefs(state: Pick<WorkspaceStoreState, "panes">): Set<string> {
  const refs = new Set<string>();
  for (const pane of state.panes) {
    const ref = paneRef(pane);
    if (ref) refs.add(ref);
  }
  return refs;
}

function evictUnreferencedEntries(): void {
  evictionScheduled = false;
  const openRefs = openPanelRefs(workspaceStore.getState());
  for (const evictor of evictors) {
    for (const ref of evictor.refs()) {
      if (!openRefs.has(ref)) evictor.evict(ref);
    }
  }
}

export function schedulePanelStoreEviction(): void {
  if (evictionScheduled) return;
  evictionScheduled = true;
  const generation = evictionGeneration;
  queueMicrotask(() => {
    if (generation !== evictionGeneration) {
      evictionScheduled = false;
      return;
    }
    evictUnreferencedEntries();
  });
}

export function registerPanelStoreEvictor(evictor: PanelStoreEvictor): () => void {
  evictors.add(evictor);
  schedulePanelStoreEviction();
  return () => evictors.delete(evictor);
}

// The subscription is deliberately owned here rather than by each individual
// panel store. A workspace restore is several synchronous sets; one shared
// microtask observes only its settled pane list and therefore cannot evict a
// ref that disappears between intermediate restore states.
const unsubscribeWorkspace = workspaceStore.subscribe(() => {
  schedulePanelStoreEviction();
});

export function resetPanelStoreEvictionForTests(): void {
  evictionGeneration += 1;
  evictionScheduled = false;
  // Keep the module-level workspace subscription and registered production
  // stores alive. Tests reset their entries independently, while this reset
  // only invalidates a microtask queued by the preceding test.
  void unsubscribeWorkspace;
}
