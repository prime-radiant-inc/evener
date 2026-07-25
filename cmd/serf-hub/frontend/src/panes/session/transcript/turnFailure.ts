// Turn-failure taxonomy + recovery classification, derived from the wire's
// TurnError (appwire/types.go's TurnError -> protocol/types.gen.ts). The legacy
// diagnostic classified by `source` (provider vs hub) and matched a set of
// reconnect-class message substrings (renderer.js:4443-4472's
// buildDiagnosticActions / isReconnectRetryDiagnostic) to decide between a
// "Retry turn" and a "Reconnect & retry" action; this reproduces that decision
// purely from the TurnError the reducer already maps onto TurnModel.error
// (reducer.ts:216, wireToTurnScalars).
import type { TurnError } from "../../../protocol/types.gen";

// The reconnect-class message substrings the legacy hub-recovery button keyed
// off (renderer.js:4463-4471) - a daemon/session that went away, where the
// honest recovery is to re-issue the turn and let the hub's auto-resume layer
// relaunch a fresh daemon.
//
// This is the same vocabulary Go classifies by, in
// agent/diagnostic/diagnostic.go's HubFailureKeywords, which stamps
// source:"hub" and the "Hub error" title/hint onto these same messages. The two
// copies had drifted in both directions - this side was missing appwire,
// websocket and "stream failed"; Go was missing "local daemon unavailable" and
// "session unavailable", so the hub's own dial failures came back titled "Serf
// error" and hinted at the Serf session log while this file, correctly, put a
// "Reconnect & retry" button under them. They are held equal now by
// TestHubFailureKeywordsMatchWebClient (cmd/serf-hub), which parses the array
// below; keep it a plain list of lowercase literals so that test can read it.
const RECONNECT_KEYWORDS = [
  "rendezvous",
  "daemon spawn",
  "resume timed out",
  "process exited before rendezvous",
  "appwire",
  "websocket",
  "stream failed",
  "source not found",
  "local daemon unavailable",
  "session unavailable",
];
const RECONNECT_RE = new RegExp(RECONNECT_KEYWORDS.join("|"), "i");

export interface TurnFailureInfo {
  message: string;
  hint?: string;
  badge: string; // the taxonomy label shown on the danger chip
  connection: boolean; // a reconnect-class failure (daemon/session gone)
  recoveryLabel: string; // "Retry" | "Reconnect & retry"
}

// asTurnError narrows the model's `unknown` turn.error (model.ts deliberately
// keeps it wire-type-free) to a TurnError by its one required field, so a turn
// carrying a real error object renders the end-cap and anything else is ignored.
export function asTurnError(error: unknown): TurnError | undefined {
  if (error !== null && typeof error === "object" && typeof (error as { message?: unknown }).message === "string") {
    return error as TurnError;
  }
  return undefined;
}

export function classifyTurnError(error: TurnError): TurnFailureInfo {
  const message = error.message.trim() || "Session error";
  const haystack = `${error.message} ${error.title ?? ""}`.toLowerCase();
  const connection = error.source === "hub" || RECONNECT_RE.test(haystack);

  let badge: string;
  if (error.cause?.kind === "provider") {
    // A provider (LLM adapter) HTTP failure - the one structured cause the wire
    // carries today (DiagnosticCause). Surface its status when present, e.g.
    // "provider 429", so the taxonomy is legible at a glance.
    badge = error.cause.status ? `provider ${error.cause.status}` : "provider";
  } else if (connection) {
    badge = "connection";
  } else if (error.source) {
    badge = error.source;
  } else {
    badge = "error";
  }

  return {
    message,
    hint: error.hint || undefined,
    badge,
    connection,
    recoveryLabel: connection ? "Reconnect & retry" : "Retry",
  };
}
