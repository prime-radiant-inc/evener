import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { refParam } from "./routing";
import { type OpenPaneRecord, workspaceStore } from "./workspace";

export interface PaneLifetime {
  readonly paneId: string;
  readonly serial: number;
  readonly sourceRef: string;
  readonly alive: boolean;
  readonly composer: ComposerSourceState | null;
  readonly readViews: Map<string, TranscriptReadView>;
  dispose(): void;
}
let nextSerial = 0;
const lifetimes = new WeakMap<OpenPaneRecord, PaneLifetime>();

export function conversationPaneLifetime(pane: OpenPaneRecord): PaneLifetime {
  const existing = lifetimes.get(pane);
  if (existing) return existing;
  const sourceRef = refParam(pane.params);
  if (sourceRef === null) throw new Error("Conversation pane requires a session ref");
  let alive = true;
  let composer: ComposerSourceState | null = null;
  const lifetime: PaneLifetime = {
    paneId: pane.id,
    serial: ++nextSerial,
    sourceRef,
    readViews: new Map(),
    get alive() {
      return alive;
    },
    get composer() {
      if (alive && pane.type === "session" && composer === null) composer = createComposerSourceState(sourceRef);
      return composer;
    },
    dispose() {
      if (!alive) return;
      alive = false;
      for (const view of lifetime.readViews.values()) view.dispose();
      lifetime.readViews.clear();
      composer?.dispose();
    },
  };
  lifetimes.set(pane, lifetime);
  return lifetime;
}

export function disposeConversationPaneLifetime(pane: OpenPaneRecord): void {
  lifetimes.get(pane)?.dispose();
}

// Observe the committed record list without making workspace import recovery
// stores. A reused ID is a different lifetime, focus is the same lifetime.
workspaceStore.subscribe((state, previous) => {
  if (state.panes === previous.panes) return;
  for (const pane of previous.panes) {
    if (!state.panes.includes(pane)) disposeConversationPaneLifetime(pane);
  }
});
