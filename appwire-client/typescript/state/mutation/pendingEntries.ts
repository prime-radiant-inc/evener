import { canonicalSkillNames } from "../../composerInput";
import type { ThreadModel } from "../../model";
import type { InputItem, PendingMutation } from "../../types.gen";
import type { MutationOptimisticRecord, MutationOutboxRecord } from "./records";

export type PendingMethod = "send" | "steer" | "queue" | "drain" | "promote";
// "canceled" is the durable Stop cancellation state surfaced as-is: the entry
// stays visible (with its Retry affordance) until the user retries it or the
// thread goes away.
export type PendingTurnState = "submitting" | "blockedUnknown" | "canceled" | "accepted" | "claimed";

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

// The wire-method → PendingMethod mapping that names the entry method family:
// promote is its own method so labels and tests stay honest (spec §3), never
// folded into `steer`.
function pendingMethod(method: string): PendingMethod | undefined {
  if (method === "turn/start") return "send";
  if (method === "turn/steer") return "steer";
  if (method === "turn/queue") return "queue";
  if (method === "turn/drainAsSteer") return "drain";
  if (method === "turn/promoteQueuedAsSteer") return "promote";
  return undefined;
}

// Collapses any whitespace run to a single space and trims both ends.
export function normalizeText(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

// The synthetic label an image-only entry displays (and the string this
// module trusts the daemon's own queue preview to also produce for an
// image-only queued entry - see reconcilePendingEntries below).
export function imagePlaceholder(count: number): string {
  if (count === 1) return "[image]";
  if (count > 1) return `[${count} images]`;
  return "";
}

// queueEntryPreviewText is the one label computation used for BOTH a real
// queue row's fallback text and a pending entry's display/matching text:
// normalized text wins whenever non-blank; only a blank (or whitespace-only)
// text falls back to the image placeholder. The label is a MATCHING KEY
// between a queued daemon row and its optimistic pending row, not just a
// display choice.
export function queueEntryPreviewText(text: string, imageCount: number): string {
  const normalized = normalizeText(text);
  return normalized || imagePlaceholder(imageCount);
}

// skillMarkers renders an entry's canonical skill selections for display and
// copy: the name is the selection's whole user-visible identity - the part of
// an entry that is distinct from its typed text. Without it a skill-only entry
// previews blank and copies as an empty string.
//
// One definition shared by QueueStrip's durable/daemon/pending rows and
// PendingChips' in-flight chips, so every surface of the same submission names
// a selection identically - the split that let a skill-only pending chip
// render an empty body while the queue row named it.
export function skillMarkers(names: readonly string[]): string {
  return names.map((name) => `[skill: ${name}]`).join(" ");
}

// The one entry-body composition every in-flight surface renders - the
// chips for sends, the held-steer ghost stack for steer/drain/promote
// (steering-ghost spec §2): the matching text-plus-markers preview
// PendingChips used to compose inline, extracted so the ghost is not a
// second copy of the chip's composition. Contentless input still composes
// to "" - the matching-key contract queueEntryPreviewText carries above.
export function pendingEntryPreview(entry: {
  text: string;
  imageCount: number;
  skillNames: readonly string[];
}): string {
  return [queueEntryPreviewText(entry.text, entry.imageCount), skillMarkers(entry.skillNames)]
    .filter((part) => part !== "")
    .join(" ");
}

const DEFAULT_MAX_DISPLAY_LENGTH = 140;

// The client-side visual cap layered on top of the daemon's own first-line
// truncation (parity-m5-composer.md §B) - independent of and smaller than
// most real messages, so this mostly matters for a single very long line.
export function truncateForDisplay(text: string, max: number = DEFAULT_MAX_DISPLAY_LENGTH): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max)}…`;
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

// The ONE home of the reflected-identity rule - which client mutation ids
// have landed in the live model (queue rows or transcript items). Both the
// pending reconciliation below and the held-steer announcements region
// consume it, so a departure's outcome classification can never diverge
// from reconcile's own settle rule.
export function reflectedMutationIds(model: ThreadModel | undefined): Set<string> {
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
  submittedHere: ReadonlyMap<string, number>,
  durableCreatedAt: number | undefined,
): PendingTurnEntry | undefined {
  const method = pendingMethod(mutation.method);
  if (!method) return undefined;
  return {
    id: mutation.clientMutationId,
    ref,
    method,
    ...inputPreview(mutation.input),
    // The wire's PendingMutation carries no timestamp. Prefer this client's
    // page-session carrier after settle; while a matching durable record
    // exists, its timestamp is known regardless of which client submitted it.
    createdAt: submittedHere.get(mutation.clientMutationId) ?? durableCreatedAt,
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
// submittedHere is the id -> createdAt map of the mutations this client
// itself submitted (the host's pending-turns store owns it). The durable
// records answer that for as long as they exist, and they do not outlast the
// hydrate that reports the same id: publishing a read settles every
// authoritative identity out of durable storage (the host's own
// reconcileIdentities). This map is what carries provenance past that
// settlement - the `fromThisClient` carrier, widened to also carry
// `createdAt` across the settle.
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
  submittedHere: ReadonlyMap<string, number>,
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
    // projection inherits THAT entry's provenance and timestamp rather than
    // assuming every durable record is this client's. No durable record means
    // submittedHere is the only provenance and timestamp carrier for the id.
    const existing = entries.get(mutation.clientMutationId);
    const fromThisClient = existing ? existing.fromThisClient : submittedHere.has(mutation.clientMutationId);
    const entry = authoritativeEntry(ref, mutation, fromThisClient, submittedHere, existing?.createdAt);
    if (entry) entries.set(entry.id, entry);
  }

  // Known-createdAt first, ascending; entries without one after them (the
  // daemon sorts pendingMutations lexicographically by id, so their
  // relative order is stable within a snapshot only). The stable sort keeps
  // array order for equal createdAt - a same-millisecond double-submit
  // keeps submission order in-session, a corner the spec accepts. This is
  // the ONE home of the rule: queue rows, chips, and the held-steer ghost
  // stack all render this order.
  return [...entries.values()].sort((left, right) => {
    if (left.createdAt === undefined && right.createdAt === undefined) return 0;
    if (left.createdAt === undefined) return 1;
    if (right.createdAt === undefined) return -1;
    return left.createdAt - right.createdAt;
  });
}
