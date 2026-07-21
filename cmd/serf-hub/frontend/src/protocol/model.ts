// UI-ready thread/turn/item model. reducer.ts folds wire shapes (Thread,
// Turn, ThreadItem, AnyNotification, ...) from ./types.gen.ts into this
// shape; components should only ever read this model, never the wire types
// directly.

import type { QueueState, SandboxEscalationRequested, ThreadStatus } from "./types.gen";

export interface ItemModel {
  id: string;
  turnId: string;
  type: string; // wire ThreadItem.type verbatim
  text: string; // settled text
  pendingText?: string[]; // in-flight delta chunks (join on complete)
  toolName?: string;
  callId?: string;
  argumentsJSON?: string;
  output?: string;
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
}
