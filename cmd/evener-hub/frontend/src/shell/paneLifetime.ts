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
    if (!state.panes.includes(pane)) disposeConversationPaneLifetime(pane);
  }
});
