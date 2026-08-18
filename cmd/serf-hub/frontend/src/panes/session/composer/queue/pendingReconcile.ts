import type { ThreadModel } from "../../../../protocol/model";
import type { InputItem, PendingMutation } from "../../../../protocol/types.gen";
import type { MutationOptimisticRecord, MutationOutboxRecord } from "../../../../stores/mutationOutbox";

export type PendingMethod = "send" | "steer" | "queue" | "drain";
export type PendingTurnState = "submitting" | "blockedUnknown" | "accepted" | "claimed";

export interface PendingTurnEntry {
  id: string;
  ref: string;
  method: PendingMethod;
  text: string;
  imageCount: number;
  createdAt?: number;
  state: PendingTurnState;
  source: "outbox" | "optimistic" | "authoritative";
  // Whether THIS client submitted the mutation. Separate from `source`, which
  // names the projection currently DESCRIBING the row and flips to
  // "authoritative" the moment a hydrate reports the same clientMutationId:
  // one submission, two describers. Send/queue routing asks whose submission it
  // is (deriveSendQueueAvailability's tier 6 counts strictly this client's own
  // sends, never the daemon's session-wide projection of every client's), and
  // that answer cannot change when a hydrate lands.
  fromThisClient: boolean;
}

function pendingMethod(method: string): PendingMethod | undefined {
  if (method === "turn/start") return "send";
  if (method === "turn/steer") return "steer";
  if (method === "turn/queue") return "queue";
  if (method === "turn/drainAsSteer") return "drain";
  return undefined;
}

function inputPreview(input: InputItem[] | undefined): { text: string; imageCount: number } {
  const text = input
    ?.filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n");
  const imageCount = input?.filter((item) => item.type === "image").length ?? 0;
  return { text: text ?? "", imageCount };
}

type BrowserPendingRecord = MutationOutboxRecord | MutationOptimisticRecord;

function outboxInput(record: BrowserPendingRecord): InputItem[] | undefined {
  const display = record.optimisticDisplay;
  if (display && typeof display === "object" && "input" in display && Array.isArray(display.input)) {
    return display.input as InputItem[];
  }
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : undefined;
}

function reflectedMutationIds(model: ThreadModel | undefined): Set<string> {
  const ids = new Set(model?.queue?.clientMutationIds ?? []);
  for (const turn of model?.turns ?? []) {
    for (const item of turn.items) {
      const clientMutationId = (item as typeof item & { clientMutationId?: string }).clientMutationId;
      if (clientMutationId) ids.add(clientMutationId);
    }
  }
  return ids;
}

function outboxEntry(record: BrowserPendingRecord): PendingTurnEntry | undefined {
  const method = pendingMethod(record.method);
  if (!method) return undefined;
  const preview = inputPreview(outboxInput(record));
  return {
    id: record.clientMutationId,
    ref: record.targetRef,
    method,
    ...preview,
    createdAt: record.createdAt,
    state: record.state,
    source: record.state === "accepted" ? "optimistic" : "outbox",
    // A durable browser record IS this client's own submission.
    fromThisClient: true,
  };
}

function authoritativeEntry(
  ref: string,
  mutation: PendingMutation,
  fromThisClient: boolean,
): PendingTurnEntry | undefined {
  const method = pendingMethod(mutation.method);
  if (!method) return undefined;
  return {
    id: mutation.clientMutationId,
    ref,
    method,
    ...inputPreview(mutation.input),
    state: mutation.executionState === "claimed" ? "claimed" : "accepted",
    source: "authoritative",
    fromThisClient,
  };
}

// Pending presentation is identity based. The durable browser outbox owns
// transport ambiguity; a separate durable optimistic record owns accepted but
// not-yet-reflected input until pendingMutations, queue, or transcript state
// replaces it.
//
// submittedHere is the set of client mutation ids this client itself submitted
// (pendingTurnsStore owns it). The durable records answer that for as long as
// they exist, and they do not outlast the hydrate that reports the same id:
// publishing a read settles every authoritative identity out of durable storage
// (threads.ts's reconcileIdentities). This set is what carries provenance past
// that settlement.
export function reconcilePendingEntries(
  ref: string,
  outbox: BrowserPendingRecord[],
  model: ThreadModel | undefined,
  submittedHere: ReadonlySet<string>,
): PendingTurnEntry[] {
  const reflected = reflectedMutationIds(model);
  const entries = new Map<string, PendingTurnEntry>();

  for (const record of outbox) {
    if (record.targetRef !== ref || reflected.has(record.clientMutationId)) continue;
    const entry = outboxEntry(record);
    if (entry) entries.set(entry.id, entry);
  }

  for (const mutation of model?.pendingMutations ?? []) {
    if (reflected.has(mutation.clientMutationId)) continue;
    // Every entry placed so far came from a durable record of this client's, so
    // an id already present is one this client submitted - whatever the daemon's
    // projection is about to say about the same submission.
    const fromThisClient = entries.has(mutation.clientMutationId) || submittedHere.has(mutation.clientMutationId);
    const entry = authoritativeEntry(ref, mutation, fromThisClient);
    if (entry) entries.set(entry.id, entry);
  }

  return [...entries.values()].sort((left, right) => (left.createdAt ?? 0) - (right.createdAt ?? 0));
}
