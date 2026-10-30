import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import type { JobLogReader } from "../panes/transcript/jobLogReader";
import { parseZoomParams } from "../panes/zoom/intent";
import { createSecureUUID } from "../stores/secureUUID";
import { refParam } from "./routing";
import { type OpenPaneRecord, onPaneRetype, workspaceStore } from "./workspace";

export interface PaneLifetime {
  readonly paneId: string;
  readonly serial: number;
  readonly sourceRef: string;
  readonly sourceType: "session" | "transcript";
  readonly compositionOwnerId: string;
  readonly alive: boolean;
  readonly composer: ComposerSourceState | null;
  readonly composers: Map<string, ComposerSourceState>;
  readonly readViews: Map<string, TranscriptReadView>;
  readonly jobOutputReads: Map<string, JobLogReader>;
  readonly resolvedSessions: Map<string, string>;
  /** A store-driven resume of the source conversation moves retained source
   * ownership (the Return target, the source read view's role, and the
   * source composer's ref) to the resumed identity. */
  adoptResumedSource(resumedRef: string): void;
  dispose(): void;
}
let nextSerial = 0;
const lifetimes = new WeakMap<OpenPaneRecord, PaneLifetime>();

export function conversationPaneLifetime(pane: OpenPaneRecord): PaneLifetime {
  const existing = lifetimes.get(pane);
  if (existing) return existing;
  const source = pane.type === "sessionZoom" ? parseZoomParams(pane.params)?.source : null;
  const initialSourceRef = refParam(source?.params ?? pane.params);
  if (initialSourceRef === null) throw new Error("Conversation pane requires a session ref");
  let sourceRef = initialSourceRef;
  const sourceType = source?.type ?? (pane.type === "session" ? "session" : "transcript");
  let alive = true;
  const lifetime: PaneLifetime = {
    paneId: pane.id,
    serial: ++nextSerial,
    compositionOwnerId: createSecureUUID(),
    get sourceRef() {
      return sourceRef;
    },
    sourceType,
    composers: new Map(),
    readViews: new Map(),
    jobOutputReads: new Map(),
    resolvedSessions: new Map(),
    get alive() {
      return alive;
    },
    get composer() {
      if (!alive || sourceType !== "session") return null;
      let state = lifetime.composers.get(sourceRef);
      if (state === undefined) {
        state = createComposerSourceState(sourceRef, lifetime.compositionOwnerId);
        lifetime.composers.set(sourceRef, state);
      }
      return state;
    },
    adoptResumedSource(resumedRef: string): void {
      if (sourceRef === resumedRef) return;
      // The retained state binds draft persistence, recovery and
      // submission to the ref it was created with: rekeying the entry
      // would keep writing under the superseded identity. Retire the old
      // state; a fresh one for the resumed identity loads the draft the
      // resume carried, and any state already created for the resumed ref
      // keeps its newer work.
      lifetime.composers.get(sourceRef)?.dispose();
      lifetime.composers.delete(sourceRef);
      sourceRef = resumedRef;
    },
    dispose() {
      if (!alive) return;
      alive = false;
      for (const reader of lifetime.jobOutputReads.values()) reader.dispose();
      lifetime.jobOutputReads.clear();
      for (const view of lifetime.readViews.values()) view.dispose();
      lifetime.readViews.clear();
      lifetime.resolvedSessions.clear();
      for (const state of lifetime.composers.values()) state.dispose();
      lifetime.composers.clear();
    },
  };
  lifetimes.set(pane, lifetime);
  return lifetime;
}

/** The lifetime this pane already owns, without creating one: callers that only
 * read identity (a pending navigation's fence) must not mint lifetimes. */
export function existingConversationPaneLifetime(pane: OpenPaneRecord): PaneLifetime | null {
  return lifetimes.get(pane) ?? null;
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
