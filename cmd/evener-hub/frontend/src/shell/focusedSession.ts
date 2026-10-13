// The focused-session readers: which session the user is looking at right
// now, derived from the workspace store. Extracted from AppShell.tsx so the
// focus-driven activity surfaces (the sidebar and keyboard routes) can read
// the same truth without importing the whole shell. Pane status bars receive
// their owning ref directly instead.
//
// focusedSessionRef is AppShell's original strict reading: the focused pane
// IS a session pane - or a cascade, whose column is a live conversation seat
// - else null (Mod+I's "no-op when the focused pane isn't a session"
// contract, Mod+J's cycle starting point). The activity surfaces
// read the workspace's OWN currentSessionRef instead of a parallel mechanism:
// the session the workspace is showing (a transcript pane's subject, a job
// log's parent, a doc's session, falling back to the main pane's session when
// the focused pane is about none) is also what the rail marks as the selected
// row - so the rail's selection and the surfaces' scope can never disagree,
// and a pure focus switch re-renders the hook's readers (the workspace store
// subscription does it; nothing reads a stale imperative value).

import { parseZoomParams } from "../panes/zoom/intent";
import { refParam } from "./routing";
import { currentSessionRef, useWorkspaceStore, workspaceStore } from "./workspace";

export function focusedSessionRef(): string | null {
  const state = workspaceStore.getState();
  const pane = state.panes.find((p) => p.id === state.focusedPaneId);
  if (pane?.type === "session") return refParam(pane.params);
  // A focused cascade is showing its leaf conversation, under the pane that
  // hosts its column (Zoom's seats carry viewFallbackId: pane.id) - the same
  // scope the workspace shows for that pane.
  if (pane?.type === "sessionZoom") return parseZoomParams(pane.params)?.ref ?? null;
  return null;
}

// The same strict reading as focusedSessionRef, for callers that address a
// seat-scoped request (a composer focus or quote insert) and must name the
// pane, not just the ref: the embedded cascade column means one ref can
// have composers under more than one pane.
export function focusedSessionPaneId(): string | null {
  const state = workspaceStore.getState();
  const pane = state.panes.find((p) => p.id === state.focusedPaneId);
  if (pane?.type !== "session" && pane?.type !== "sessionZoom") return null;
  return pane.id;
}

export function focusedActivityScopeRef(): string | null {
  return currentSessionRef(workspaceStore.getState());
}

export function useFocusedActivityScopeRef(): string | null {
  return useWorkspaceStore(currentSessionRef);
}

/** Test-only: kept for the reset symmetry the suite files expect; the store
 * has no memory to clear (the scope derives fresh from the workspace). */
export function resetFocusedActivityScopeForTests(): void {}
