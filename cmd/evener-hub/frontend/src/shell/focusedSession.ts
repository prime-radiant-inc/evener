// The focused-session readers: which session the user is looking at right
// now, derived from the workspace store. Extracted from AppShell.tsx so the
// activity surfaces (status bar, sidebar) can read the same truth without
// importing the whole shell.
//
// focusedSessionRef is AppShell's original strict reading: the focused pane
// IS a session pane, else null (Mod+I's "no-op when the focused pane isn't a
// session" contract, Mod+J's cycle starting point). focusedActivityScopeRef
// is the wider reading the activity surfaces want: a focused transcript pane
// of a SESSION (a drilled subagent) scopes the surfaces to it, and a pane of
// neither kind (settings, docs, welcome) keeps the LAST session scope - the
// bar and sidebar describe what you were reading, not nothing, while you
// check a setting.

import { refParam } from "./routing";
import { workspaceStore, type WorkspaceStoreState } from "./workspace";

export function focusedSessionRef(): string | null {
  const state = workspaceStore.getState();
  const pane = state.panes.find((p) => p.id === state.focusedPaneId);
  if (pane?.type !== "session") return null;
  return refParam(pane.params);
}

function currentActivityScopeRef(state: WorkspaceStoreState): string | null {
  const pane = state.panes.find((p) => p.id === state.focusedPaneId);
  const params = pane?.params as { ref?: unknown; parentRef?: unknown } | undefined;
  const ref = typeof params?.ref === "string" ? params.ref : null;
  const parentRef = typeof params?.parentRef === "string" ? params.parentRef : null;
  if (pane?.type === "session") return ref;
  if (pane?.type === "transcript" && ref !== null) {
    // A job log keeps its parent session's scope; a session's transcript
    // (a drilled subagent) scopes the surfaces to itself.
    return ref.startsWith("job:") ? parentRef : ref;
  }
  return null;
}

// The sticky memory: updated by subscription, not on read, so the last
// session scope survives any focus excursion no matter when (or whether)
// anyone is mid-render of a reader. Null until a session has been focused.
let lastScopeRef: string | null = null;

workspaceStore.subscribe((state) => {
  const current = currentActivityScopeRef(state);
  if (current !== null) lastScopeRef = current;
});

export function focusedActivityScopeRef(): string | null {
  return lastScopeRef;
}

/** Test-only: drop the sticky last-scope memory between tests. App code never
 * calls this. */
export function resetFocusedActivityScopeForTests(): void {
  lastScopeRef = null;
}
