// The palette's runtime context: the new-shell successor to search.js's
// buildCtx (search.js:568-579), which read #conversation's data-session-id /
// data-state and the URL path. Here the "current session" is the focused
// workspace pane, and its live/ended state comes from that session's
// ThreadModel in the threads store, not a DOM attribute. Scope gating and the
// per-command idle guards derive entirely from these.

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
      return "session";
    case "spawn":
      return "spawn";
    case "settings":
      return "settings";
    case "welcome":
      return "home";
    default:
      // transcript (a standalone read-only history view) and doc panes carry
      // no interactive session context, so they read as "other" - session/
      // ended-ok commands gate on sessionRef, which stays null for them.
      return "other";
  }
}

export function buildPaletteContext(): PaletteContext {
  const ws = workspaceStore.getState();
  const focused = ws.panes.find((p) => p.id === ws.focusedPaneId);
  if (!focused) return { sessionRef: null, onPage: "other" };
  return {
    onPage: onPageForType(focused.type),
    sessionRef: focused.type === "session" ? refFromParams(focused.params) : null,
  };
}

// focusedModel reads the tracked ThreadModel for a ref fresh at call time
// (not a snapshot captured when the palette opened), so a command that runs
// after the turn state changed sees the current state - matching the legacy's
// live activeTurnId()/isThreadBusy() DOM reads.
export function focusedModel(sessionRef: string | null): ThreadModel | undefined {
  return sessionRef ? threadsStore.getState().threads.get(sessionRef) : undefined;
}

// isSessionEnded / isSessionBusy / hasActiveTurn are the palette's three
// model-derived predicates. isSessionEnded gates the "session" (live-only)
// scope; isSessionBusy is the /model "turn in progress" guard (mirrors
// panes/session/composer/submitRouting.isTurnActive - the canonical
// interrupt/steer/model-switch busy predicate: status active AND a turn id
// has actually landed); hasActiveTurn is the interrupt/steer/queue/drain "no
// active turn" guard (the legacy's plain `!activeTurnId()` check, which is
// deliberately weaker than isSessionBusy - see submitRouting.ts's own note).
export function isSessionEnded(model: ThreadModel): boolean {
  return model.status.type === "ended" || model.status.type === "closed";
}

export function isSessionBusy(model: ThreadModel): boolean {
  return model.status.type === "active" && !!model.activeTurnId;
}

export function hasActiveTurn(model: ThreadModel): boolean {
  return !!model.activeTurnId;
}
