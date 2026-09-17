import { canonicalSkillNames } from "../../composerInput";
import type { ThreadModel } from "../../model";
import type { InputItem, PendingMutation } from "../../types.gen";
import type { MutationOptimisticRecord, MutationOutboxRecord } from "./records";

export type PendingMethod = "send" | "steer" | "queue" | "drain";
export type PendingTurnState = "submitting" | "blockedUnknown" | "accepted" | "claimed";

export interface PendingTurnEntry {
  id: string;
  ref: string;
  method: PendingMethod;
  text: string;
  imageCount: number;
  // Canonical skill selections submitted alongside (or instead of) the text,
  // from the same input the entry is otherwise built from. A skill-only
  // queued submission has no text at all, so these names are its whole
  // user-visible content.
  skillNames: string[];
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

function inputPreview(input: InputItem[] | undefined): { text: string; imageCount: number; skillNames: string[] } {
  const text = input
    ?.filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n");
  const imageCount = input?.filter((item) => item.type === "image").length ?? 0;
  const skillNames = canonicalSkillNames(
    input
      ?.filter((item): item is InputItem & { name: string } => item.type === "skill" && typeof item.name === "string")
      .map((item) => item.name),
  );
  return { text: text ?? "", imageCount, skillNames };
}

type PendingRecord = MutationOutboxRecord | MutationOptimisticRecord;

function outboxInput(record: PendingRecord): InputItem[] | undefined {
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

function outboxEntry(
  record: PendingRecord,
  isOwnMutationRecord: (record: { originClientId?: string }) => boolean,
): PendingTurnEntry | undefined {
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
    // The outbox is shared per origin, so a durable record is this client's
    // own submission only when it is unattributed (predates the owner field)
    // or names this client - another client's in-flight send must not claim
    // tier-6 routing here.
    fromThisClient: isOwnMutationRecord(record),
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

// Pending presentation is identity based. The durable outbox owns transport
// ambiguity; a separate durable optimistic record owns accepted but
// not-yet-reflected input until pendingMutations, queue, or transcript state
// replaces it.
//
// submittedHere is the set of client mutation ids this client itself submitted
// (the host's pending-turns store owns it). The durable records answer that
// for as long as they exist, and they do not outlast the hydrate that reports
// the same id: publishing a read settles every authoritative identity out of
// durable storage (the host's own reconcileIdentities). This set is what
// carries provenance past that settlement.
//
// isOwnMutationRecord is the host's ClientIdentity capability
// (createClientIdentity's own method in records.ts) - required, not
// defaulted: a partial "unattributed only" stand-in here would silently
// diverge from that rule's "or names this client" branch the moment a real
// caller's own submission carries an originClientId.
export function reconcilePendingEntries(
  ref: string,
  outbox: PendingRecord[],
  model: ThreadModel | undefined,
  submittedHere: ReadonlySet<string>,
  isOwnMutationRecord: (record: { originClientId?: string }) => boolean,
): PendingTurnEntry[] {
  const reflected = reflectedMutationIds(model);
  const entries = new Map<string, PendingTurnEntry>();

  for (const record of outbox) {
    if (record.targetRef !== ref || reflected.has(record.clientMutationId)) continue;
    const entry = outboxEntry(record, isOwnMutationRecord);
    if (entry) entries.set(entry.id, entry);
  }

  for (const mutation of model?.pendingMutations ?? []) {
    if (reflected.has(mutation.clientMutationId)) continue;
    // An entry already placed came from a durable record - which may be
    // another client's, since the outbox is shared - so the daemon's
    // projection inherits THAT entry's provenance rather than assuming every
    // durable record is this client's. No durable record means submittedHere
    // is the only provenance carrier for the id.
    const existing = entries.get(mutation.clientMutationId);
    const fromThisClient = existing ? existing.fromThisClient : submittedHere.has(mutation.clientMutationId);
    const entry = authoritativeEntry(ref, mutation, fromThisClient);
    if (entry) entries.set(entry.id, entry);
  }

  return [...entries.values()].sort((left, right) => (left.createdAt ?? 0) - (right.createdAt ?? 0));
}
