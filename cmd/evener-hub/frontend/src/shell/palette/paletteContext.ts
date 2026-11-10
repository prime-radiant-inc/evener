// The palette's runtime context: the new-shell successor to search.js's
// buildCtx (search.js:568-579), which read #conversation's data-session-id /
// data-state and the URL path. Here the "current session" is the focused
// workspace pane, and everything a command needs to know about it comes from
// that session's ThreadModel in the threads store, not a DOM attribute:
// which commands exist (scope), which of them the hub will carry out
// (capabilities), and whether a turn is in flight to act on.

import type { ThreadModel } from "@evener/appwire-client";
import { threadsStore } from "../../stores/threads";
import { presentedPanes, workspaceStore } from "../workspace";

export type OnPage = "home" | "session" | "settings" | "spawn" | "other";

// The shell's one needs-you owner as a palette command can see it. The
// CommandPalette supplies this from useNeedsYou(); calling next() closes the
// palette and defers to the controller exactly as Mod+J and the palette's
// needs-you rows do, so a command can never grow a second row-selection,
// paging, validity or cancellation rule.
export interface PaletteNeedsYouAction {
  next(): void;
}

export interface PaletteContext {
  // The focused session's ref, or null when no session pane is focused. A
  // session's commands (and the in-session search) key off this.
  sessionRef: string | null;
  // The pane id of the seat a session-scoped composer request must name: the
  // focused session pane itself, or the session pane for a focused panel's
  // ref. One ref can hold composers under more than one pane (an embedded
  // cascade column), so insert and focus requests address the seat. Null
  // when no session pane pins the destination.
  sessionPaneId: string | null;
  onPage: OnPage;
}

function refFromParams(params: unknown): string | null {
  if (typeof params !== "object" || params === null) return null;
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" && ref.length > 0 ? ref : null;
}

function onPageForType(type: string): OnPage {
  switch (type) {
    case "session":
    // A cascade column is a live conversation seat now: the context it
    // provides is the leaf conversation under the pane hosting its column,
    // the same scope focusedSessionRef and the workspace show for it.
    case "sessionZoom":
    case "sessionTasks":
    case "sessionDetails":
      return "session";
    case "spawn":
      return "spawn";
    case "settings":
      return "settings";
    case "welcome":
      return "home";
    default:
      // transcript (job logs only) and doc panes carry
      // no interactive session context, so they read as "other" - session
      // commands gate on sessionRef, which stays null for them.
      return "other";
  }
}

export function buildPaletteContext(): PaletteContext {
  const ws = workspaceStore.getState();
  const panes = presentedPanes(ws.panes);
  const focused = panes.find((p) => p.id === ws.focusedPaneId);
  if (!focused) return { sessionRef: null, sessionPaneId: null, onPage: "other" };
  const onPage = onPageForType(focused.type);
  const sessionRef = onPage === "session" ? refFromParams(focused.params) : null;
  const sessionPaneId =
    focused.type === "session" || focused.type === "sessionZoom"
      ? focused.id
      : sessionRef !== null
        ? (panes.find((p) => p.type === "session" && refFromParams(p.params) === sessionRef)?.id ?? null)
        : null;
  return {
    onPage,
    sessionRef,
    sessionPaneId,
  };
}

// focusedModel reads the tracked ThreadModel for a ref fresh at call time
// (not a snapshot captured when the palette opened), so a command that runs
// after the turn state changed sees the current state - matching the legacy's
// live activeTurnId()/isThreadBusy() DOM reads.
export function focusedModel(sessionRef: string | null): ThreadModel | undefined {
  return sessionRef ? threadsStore.getState().threads.get(sessionRef) : undefined;
}
