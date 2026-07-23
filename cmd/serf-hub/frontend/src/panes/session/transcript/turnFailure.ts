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
const RECONNECT_RE =
  /rendezvous|daemon spawn|process exited before rendezvous|resume timed out|local daemon unavailable|source not found|session unavailable/i;

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
