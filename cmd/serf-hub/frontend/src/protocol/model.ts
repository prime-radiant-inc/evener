// UI-ready thread/turn/item model. reducer.ts folds wire shapes (Thread,
// Turn, ThreadItem, AnyNotification, ...) from ./types.gen.ts into this
// shape; components should only ever read this model, never the wire types
// directly.

import type {
  GoalState,
  QueueState,
  SandboxEscalationRequested,
  SerfUsage,
  ThreadCapabilities,
  ThreadStatus,
} from "./types.gen";

export interface ItemModel {
  id: string;
  turnId: string;
  type: string; // wire ThreadItem.type verbatim
  text: string; // settled text
  pendingText?: string[]; // in-flight delta chunks (join on complete)
  toolName?: string;
  callId?: string;
  argumentsJSON?: string;
  // Tool-call purpose — the wire ThreadItem.description, surfaced for the
  // subagent Activity feed. Dropped historically by wireItemToModel; now carried.
  description?: string;
  // The wire ThreadItem.eventKind: a stable typed discriminator naming what a
  // systemMessage item is ("system_prompt", "compaction", "skill_activated",
  // …; appwire.ThreadItemEventKind* on the Go side). The transcript renderer
  // classifies scaffold/system items off this typed field instead of guessing
  // from the item's char count (kata ckgw). Empty/undefined for non-system
  // items and for a system item projected by a daemon predating a given kind.
  eventKind?: string;
  output?: string;
  // Tool-result error text (wire ThreadItem.error): populated instead of
  // output when a tool call failed or was denied. The wire projects item
  // status "completed" even for an errored call (internal/appprojector and
  // internal/apptranscript both hardcode it - a Go follow-up), so error
  // PRESENCE, not status, is what distinguishes a failed/denied call (e.g. a
  // denied ask_user) from a clean completion. Carried on both the live
  // item/completed and the snapshot/reload paths (both fold through
  // reducer's wireItemToModel).
  error?: string;
  // A shell tool call's process exit code, promoted onto the settled item as a
  // typed wire field (ThreadItem.exitCode, wire-honesty spec Part A). undefined
  // for a still-running/backgrounded call, for any non-shell tool, and for an
  // old daemon that doesn't populate it (the shell descriptor then falls back
  // to parsing the output footer text). A real 0 is a clean exit, distinct from
  // undefined — never conflate the two.
  exitCode?: number;
  images?: string[];
  outputImages?: string[];
  status?: string;
  source?: string;
  reasoningSummaries?: string[][]; // per summaryIndex chunk lists
  startedAt?: string;
  completedAt?: string;
  // Client-observed arrival times stamped by the reducer from its `now`
  // parameter (never a clock read) — NOT wire truth: the wire never carries
  // reasoning timestamps at all (reasoning ThreadItems get no StartedAt/
  // CompletedAt on either the live projector or the historical reader; see
  // reducer.ts's appendReasoningDelta comment for the file:line receipts).
  // Consumers should prefer the wire startedAt/completedAt pair above when
  // present and fall back to these only when it is absent. Hydrated/
  // historical items never carry these — only a reducer that has actually
  // observed the item live stamps them.
  observedStartedAt?: string;
  observedCompletedAt?: string;
  // Populated only by the reducer's `case "warning"` fold (see reducer.ts);
  // undefined for every other item.
  warning?: { source?: string; title?: string; hint?: string };
}

export interface TurnModel {
  id: string;
  status: string;
  items: ItemModel[];
  startedAt?: string;
  completedAt?: string;
  durationMs?: number;
  usage?: unknown;
  cost?: unknown;
  error?: unknown;
}

export interface ThreadModel {
  ref: string;
  threadId: string;
  name: string;
  status: ThreadStatus;
  modelProvider: string;
  model: string;
  reasoningEffort?: string;
  askPending: boolean;
  // Surface-on-entry snapshot of blocked sandbox-exemption approval cards
  // (M7) — appwire/types.go's ThreadSerf.PendingEscalations doc comment: "a
  // HUMAN-CLIENT field only ... never part of the model's transcript or any
  // model-visible projection." THREAD-level, never a turn item; always an
  // array (hydrateThread defaults an absent wire value to []).
  pendingEscalations: SandboxEscalationRequested[];
  turns: TurnModel[];
  activeTurnId?: string;
  queue: QueueState | null;
  tasks: { total: number; done: number } | null;
  olderCursor?: string;
  lastFrameAt: number; // liveness input
  // Wave 5 T1: the following are all sourced from thread.serf
  // (appwire/types.go's SerfThread, lines 223-274) and are SNAPSHOT-ONLY -
  // hydrateThread populates them, and applyNotification updates them ONLY
  // where a real notification actually carries the field (see each field's
  // own note below); everything else here is stale until the next
  // thread/read (e.g. a reconnect re-hydrate) and there is no live-push
  // wire-candidate yet.
  capabilities: ThreadCapabilities;
  // Goal is null when no /goal objective is set (wire: SerfThread.Goal
  // *GoalState, omitempty). No live push exists (goal/set's response
  // carries only {started}, and appwire/protocol.go's Notifications catalog
  // has no goal-changed entry) - a future wave's wire-candidate.
  goal: GoalState | null;
  contextUsed: number;
  contextWindow: number;
  contextPressure: number;
  // Usage is null (not a zero-valued object) when the daemon has no token
  // data at all (old daemon, or a Codex-bridged thread) - SerfThread.Usage's
  // own doc comment: "nil is how a fresh/old-daemon/codex thread signals 'no
  // token data' rather than rendering ↑0 ↓0." No live push.
  usage: SerfUsage | null;
  // Cost is the session-level estimated dollar total (wire: SerfThread.Cost) -
  // the "~$X.XX" string EstimateCost derives SERVER-SIDE from the cumulative
  // usage at the thread's model price (the pricing table never crosses the
  // wire), the session-scope sibling of TurnModel.cost. undefined/null (never
  // "") when the daemon omits it: no token data, or an uncataloged model - an
  // honest "unknown" the status row renders as NO cost chip, never a
  // misleading "~$0.00". Snapshot-only like usage/workMillis: hydrateThread
  // sets it and the reducer's ...model spread preserves it; no live push, so
  // it refreshes on the next thread/read (e.g. a reconnect re-hydrate).
  cost?: string | null;
  workMillis: number;
  // activeTurnStartedAt is undefined when no turn is active (an ISO string,
  // like every other timestamp on this model, converted from the wire's
  // epoch-ms SerfThread.ActiveTurnStartedAt). No live push.
  activeTurnStartedAt?: string;
  // reasoningEffortLevels/supportsReasoning DO get a live update, but only
  // via thread/model/changed (a model switch describes the new model's full
  // profile - see reducer.ts's own case) - never independently pushed.
  reasoningEffortLevels: string[];
  supportsReasoning: boolean;
  // Location facts from the wire Thread snapshot: cwd is always present;
  // gitBranch/projectPath only when known. Consumed by the session chrome's
  // location cluster and doc-pane cwd-relativization. Snapshot-only - a
  // reconnect re-hydrates them; nothing pushes them live.
  cwd: string;
  gitBranch?: string;
  projectPath?: string;
}
