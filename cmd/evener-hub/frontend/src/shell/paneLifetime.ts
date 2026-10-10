import { disposeDocumentReaderWork } from "../panes/doc/documentReadState";
import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { parseZoomParams } from "../panes/zoom/intent";
import { refParam } from "./routing";
import { type OpenPaneRecord, onPaneRetype, workspaceStore } from "./workspace";

export interface PaneLifetime {
  readonly paneId: string;
  readonly serial: number;
  readonly sourceRef: string;
  readonly sourceType: "session" | "transcript";
  readonly alive: boolean;
  readonly composer: ComposerSourceState | null;
  readonly readViews: Map<string, TranscriptReadView>;
  readonly resolvedSessions: Map<string, string>;
  dispose(): void;
}
let nextSerial = 0;
const lifetimes = new WeakMap<OpenPaneRecord, PaneLifetime>();

export function conversationPaneLifetime(pane: OpenPaneRecord): PaneLifetime {
  const existing = lifetimes.get(pane);
  if (existing) return existing;
  const source = pane.type === "sessionZoom" ? parseZoomParams(pane.params)?.source : null;
  const sourceRef = refParam(source?.params ?? pane.params);
  if (sourceRef === null) throw new Error("Conversation pane requires a session ref");
  const sourceType = source?.type ?? (pane.type === "session" ? "session" : "transcript");
  let alive = true;
  let composer: ComposerSourceState | null = null;
  const lifetime: PaneLifetime = {
    paneId: pane.id,
    serial: ++nextSerial,
    sourceRef,
    sourceType,
    readViews: new Map(),
    resolvedSessions: new Map(),
    get alive() {
      return alive;
    },
    get composer() {
      if (alive && sourceType === "session" && composer === null) composer = createComposerSourceState(sourceRef);
      return composer;
    },
    dispose() {
      if (!alive) return;
      alive = false;
      for (const view of lifetime.readViews.values()) view.dispose();
      lifetime.readViews.clear();
      lifetime.resolvedSessions.clear();
      composer?.dispose();
    },
  };
  lifetimes.set(pane, lifetime);
  return lifetime;
}

export function disposeConversationPaneLifetime(pane: OpenPaneRecord): void {
  lifetimes.get(pane)?.dispose();
}

// The destination resolver for a saved browser note (Task 4). It reads the
// EXISTING WeakMap and the current workspace record - never creating a lifetime
// and never authorizing a closed or replaced source. A mismatched pane, a
// disposed lifetime, a non-session source or a different displayed session all
// resolve to null. The durable document key's namespace check belongs to the
// annotation port that calls this, not here.
export function retainedComposerSourceFor(
  paneId: string,
  sourceId: string,
  sessionRef: string,
): ComposerSourceState | null {
  const pane = workspaceStore.getState().panes.find((record) => record.id === paneId);
  const lifetime = pane ? lifetimes.get(pane) : undefined;
  if (!lifetime?.alive || lifetime.sourceType !== "session" || lifetime.sourceRef !== sessionRef) return null;
  const source = lifetime.composer;
  return source?.alive && source.sourceId === sourceId ? source : null;
}

onPaneRetype((previous, replacement) => {
  const lifetime = lifetimes.get(previous);
  if (!lifetime) return;
  lifetimes.delete(previous);
  lifetimes.set(replacement, lifetime);
});

// Observe the committed record list without making workspace import recovery
// stores. A reused ID is a different lifetime, focus is the same lifetime.
workspaceStore.subscribe((state, previous) => {
  if (state.panes === previous.panes) return;
  for (const pane of previous.panes) {
    if (state.panes.includes(pane)) continue;
    disposeConversationPaneLifetime(pane);
    // A committed document-viewer removal also retires that viewer's retained
    // annotation work; a conversation-only WeakMap entry is simply absent.
    if (pane.type === "doc") disposeDocumentReaderWork(pane.id);
  }
});
