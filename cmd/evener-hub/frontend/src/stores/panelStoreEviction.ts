import type { SessionActivityContext } from "@evener/appwire-client";
import { deriveCascadePath, parseZoomParams } from "../panes/zoom/intent";
import { type OpenPaneRecord, type WorkspaceStoreState, workspaceStore } from "../shell/workspace";
import { connectionStore } from "./connection";
import { sessionActivitySnapshot } from "./sessionActivity";

export interface PanelStoreEvictor {
  refs: () => Iterable<string>;
  // Which open panes keep this store's per-ref state alive. Defaults to every
  // ref-bearing pane type (session, companion, and a cascade's scopes); a
  // store whose state exists only inside the conversation surface (top-notes
  // state lives in the session pane or a cascade column) narrows this so
  // companion panes for the same ref cannot keep dead state alive.
  keepAliveRefs?: (panes: OpenPaneRecord[]) => Set<string>;
  evict: (ref: string) => void;
}

const evictors = new Set<PanelStoreEvictor>();
let evictionScheduled = false;
let evictionGeneration = 0;

function paramsRef(params: unknown): string | undefined {
  if (!params || typeof params !== "object" || Array.isArray(params)) return undefined;
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" && ref !== "" ? ref : undefined;
}

function paneRef(pane: { type: string; params: unknown }): string | undefined {
  if (pane.type !== "session" && pane.type !== "sessionTasks" && pane.type !== "sessionDetails") {
    return undefined;
  }
  return paramsRef(pane.params);
}

// The authoritative ancestry Zoom renders comes from the leaf's session
// activity context (see Zoom.tsx and panes/zoom/actions.ts's own contextFor).
// Reading the same snapshot here keeps the retention set in step with the
// rendered path: a readable parent the recorded edges do not carry - e.g. one
// restored only from authority after popping above the original source - stays
// referenced instead of having its column's panel state evicted.
function cascadeActivityContext(ref: string): SessionActivityContext | null {
  const client = connectionStore.getState().client;
  return client ? (sessionActivitySnapshot(client, ref, "session")?.context ?? null) : null;
}

// A sessionZoom pane renders a retained cascade path of embedded conversations:
// the leaf and its ancestors each stay readable in a column of the same pane,
// so EVERY scope ref in the path keeps its panel state alive - the pane's own
// params.ref names only the leaf. parseZoomParams rejects a malformed record;
// fall back to the raw leaf ref so a damaged pane still names its conversation.
function zoomScopeRefs(params: unknown): Iterable<string> {
  const parsed = parseZoomParams(params);
  if (!parsed) {
    const ref = paramsRef(params);
    return ref ? [ref] : [];
  }
  // Pass the same authoritative context Zoom renders from, not null, so scopes
  // the edges omit but authority names are still retained.
  return deriveCascadePath(parsed, cascadeActivityContext(parsed.ref)).scopes.map((scope) => scope.requestedRef);
}

function paneRefs(pane: { type: string; params: unknown }): Iterable<string> {
  if (pane.type === "sessionZoom") return zoomScopeRefs(pane.params);
  const ref = paneRef(pane);
  return ref ? [ref] : [];
}

export function openPanelRefs(state: Pick<WorkspaceStoreState, "panes">): Set<string> {
  const refs = new Set<string>();
  for (const pane of state.panes) {
    for (const ref of paneRefs(pane)) refs.add(ref);
  }
  return refs;
}

// The refs whose conversation surface - the notes bar and the session panels
// mounted by ConversationBody - is on screen: a session pane, or any retained
// scope of a sessionZoom cascade. Companion panes (sessionTasks,
// sessionDetails) carry the ref but mount no conversation surface, so a store
// that lives only inside the conversation narrows its keep-alive set to this.
export function openConversationRefs(panes: Iterable<{ type: string; params: unknown }>): Set<string> {
  const refs = new Set<string>();
  for (const pane of panes) {
    if (pane.type !== "session" && pane.type !== "sessionZoom") continue;
    for (const ref of paneRefs(pane)) refs.add(ref);
  }
  return refs;
}

function evictUnreferencedEntries(): void {
  evictionScheduled = false;
  const panes = workspaceStore.getState().panes;
  for (const evictor of evictors) {
    const openRefs = evictor.keepAliveRefs ? evictor.keepAliveRefs(panes) : openPanelRefs({ panes });
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
