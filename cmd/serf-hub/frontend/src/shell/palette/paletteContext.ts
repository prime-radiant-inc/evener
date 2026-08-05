// The palette's runtime context: the new-shell successor to search.js's
// buildCtx (search.js:568-579), which read #conversation's data-session-id /
// data-state and the URL path. Here the "current session" is the focused
// workspace pane, and everything a command needs to know about it comes from
// that session's ThreadModel in the threads store, not a DOM attribute:
// which commands exist (scope), which of them the hub will carry out
// (capabilities), and whether a turn is in flight to act on.

import type { ThreadModel } from "../../protocol/model";
import { threadsStore } from "../../stores/threads";
import { workspaceStore } from "../workspace";

export type OnPage = "home" | "session" | "settings" | "spawn" | "other";

export interface PaletteContext {
  // The focused session's ref, or null when no session pane is focused. A
  // session's commands (and the in-session search) key off this.
  sessionRef: string | null;
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
    case "sessionTasks":
    case "sessionActivity":
    case "sessionDetails":
      return "session";
    case "spawn":
      return "spawn";
    case "settings":
      return "settings";
    case "welcome":
      return "home";
    default:
      // transcript (a standalone read-only history view) and doc panes carry
      // no interactive session context, so they read as "other" - session
      // commands gate on sessionRef, which stays null for them.
      return "other";
  }
}

export function buildPaletteContext(): PaletteContext {
  const ws = workspaceStore.getState();
  const focused = ws.panes.find((p) => p.id === ws.focusedPaneId);
  if (!focused) return { sessionRef: null, onPage: "other" };
  const sessionRef =
    focused.type === "session" ||
    focused.type === "sessionTasks" ||
    focused.type === "sessionActivity" ||
    focused.type === "sessionDetails"
      ? refFromParams(focused.params)
      : null;
  return {
    onPage: onPageForType(focused.type),
    sessionRef,
  };
}

// focusedModel reads the tracked ThreadModel for a ref fresh at call time
// (not a snapshot captured when the palette opened), so a command that runs
// after the turn state changed sees the current state - matching the legacy's
// live activeTurnId()/isThreadBusy() DOM reads.
export function focusedModel(sessionRef: string | null): ThreadModel | undefined {
  return sessionRef ? threadsStore.getState().threads.get(sessionRef) : undefined;
}

// hasActiveTurn is the palette's ONE model-derived predicate, and it answers
// exactly one question: is there a turn to act on right now. It belongs to
// /interrupt, /steer, /queue and /drain, which are meaningless without one
// (the legacy's plain `!activeTurnId()` check).
//
// It is deliberately the only one left. A "session is busy" and a "session has
// ended" predicate used to live here too, gating /model and the whole
// session scope respectively - both session-scoped decisions made from
// turn-scoped information (kata cjzc). Whether the NEXT turn can be
// configured is the hub's answer, not this module's: it advertises a
// per-action capability for every thread, cold ones included, and resumes
// behind the call. commands.ts reads those flags instead.
export function hasActiveTurn(model: ThreadModel): boolean {
  return !!model.activeTurnId;
}
