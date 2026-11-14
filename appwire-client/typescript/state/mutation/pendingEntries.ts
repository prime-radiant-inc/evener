import { canonicalSkillNames } from "../../composerInput";
import type { ThreadModel } from "../../model";
import type { InputItem, PendingMutation } from "../../types.gen";
import { type ComposerVerb, composerVerb } from "./composerMethods";
import {
  effectiveWireId,
  type MutationOptimisticRecord,
  type MutationOutboxRecord,
  type MutationRecord,
} from "./records";

export type PendingMethod = ComposerVerb;
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
  commandNames?: string[];
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
  // A queue entry a confirmed receipt or pending projection says this
  // command's message owns. A promotion's expected entry is only a
  // precondition and never appears here.
  queueEntryId?: string;
  // The queue revision a drain steers with, likewise: the queue it takes.
  queueRevision?: number;
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

// The inverse of imagePlaceholder for a daemon queue preview. The preview is a
// wire string, so its grammar stays next to the composer that writes it.
export function parseImagePlaceholder(preview: string | undefined): number {
  if (!preview) return 0;
  const normalized = preview.trim();
  if (normalized === imagePlaceholder(1)) return 1;
  const match = /^\[(\d+) images\]$/.exec(normalized);
  return match ? Number(match[1]) : 0;
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
// One definition shared by the projection's durable, daemon and pending rows,
// so every representation of the same submission names a selection
// identically.
export function skillMarkers(names: readonly string[]): string {
  return names.map((name) => `[skill: ${name}]`).join(" ");
}

// The one entry-body composition the pending stack renders. The bubble body
// keeps the entry's own line structure: an indented code block renders
// flattened by neither this nor the delivered form, and the daemon's queue
// preview is a label, not the body. Only the ends trim; interior whitespace
// survives. Contentless input still composes to "" - the matching-key
// contract queueEntryPreviewText carries above.
export function pendingEntryPreview(entry: {
  text: string;
  imageCount: number;
  skillNames: readonly string[];
  commandNames?: readonly string[];
}): string {
  return [
    entry.text.trim() || imagePlaceholder(entry.imageCount),
    skillMarkers(entry.skillNames),
    commandMarkers(entry.commandNames ?? []),
  ]
    .filter((part) => part !== "")
    .join(" ");
}

// One index of the daemon's queue arrays, canonicalized. Both the pending
// projection and the web store's carried-row registry read the wire shape
// through here so the two cannot disagree about text, previews, or selections.
export function queueEntryAt(
  queue: {
    ids?: readonly string[];
    clientMutationIds?: readonly string[];
    texts?: readonly string[];
    preview?: readonly string[];
    skillNames?: readonly (readonly string[])[];
    commandNames?: readonly (readonly string[])[];
  },
  index: number,
): {
  entryId: string | undefined;
  clientMutationId: string | undefined;
  fullText: string | undefined;
  text: string;
  preview: string | undefined;
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
} {
  const fullText = queue.texts?.[index];
  const preview = queue.preview?.[index];
  // A preview is the daemon's compact label, so a literal text message whose
  // own text IS "[image]" must not classify its row as image-bearing: the
  // placeholder grammar speaks only when no full text says otherwise.
  const imageCount = fullText === undefined || fullText.trim() === "" ? parseImagePlaceholder(preview) : 0;
  return {
    entryId: queue.ids?.[index],
    clientMutationId: queue.clientMutationIds?.[index],
    fullText,
    text: fullText ?? preview ?? "",
    preview,
    imageCount,
    skillNames: canonicalSkillNames(queue.skillNames?.[index] ?? []),
    commandNames: canonicalSkillNames(queue.commandNames?.[index] ?? []),
  };
}

// Whether THIS client has a turn/start of its own that the session has not
// reflected yet: deriveSendQueueAvailability's tier 6 input. A fast second
// message is composed while the thread still reads idle; routed to
// turn/start it is refused with Conflict("turn is already active").
//
// Someone else's pending send never counts: a routing decision must not
// follow another tab's or the TUI's send. It is fromThisClient, not
// entry.source, because a hydrate landing mid-send re-describes this client's
// own unsettled send as "authoritative". blockedUnknown counts: the send's
// response was lost, so its turn may already be running. A canceled row does
// not count: Stop wrote its cancellation before dispatch, so no turn can be
// running because of it.
export function ownPendingSend(entries: readonly PendingTurnEntry[] | null | undefined): boolean {
  return ownUnsettledInput(entries, ["send"]);
}

// The provenance/state half of "this client has input the session has not
// settled": another client's entry never counts, and a canceled row is a
// cancellation the client itself wrote before dispatch, so no submission of
// ours can still be in flight because of it.
function isOwnUnsettledEntry(entry: PendingTurnEntry): boolean {
  return entry.fromThisClient && entry.state !== "canceled";
}

// Whether THIS client has any composer input still unresolved, for Retry's
// repeated-press guard. Ordinary routing can put the shared continuation on
// either turn/start or turn/queue, so both count; steering a message is not a
// submitted turn input and neither is a drain or promote. The payload need not
// be a continuation: any unresolved submission of this client's blocks a
// second Retry from acting on a stale failure.
//
// This is not readiness. Null or undefined entries mean the pending read has
// not answered yet, so a false result here is not proof the read completed;
// the caller loads that state from its owner. ownPendingSend above keeps its
// narrower send-only meaning for ordinary composer routing.
export function ownPendingTurnInput(entries: readonly PendingTurnEntry[] | null | undefined): boolean {
  return ownUnsettledInput(entries, ["send", "queue"]);
}

function ownUnsettledInput(
  entries: readonly PendingTurnEntry[] | null | undefined,
  methods: readonly PendingTurnEntry["method"][],
): boolean {
  return (entries ?? []).some((entry) => methods.includes(entry.method) && isOwnUnsettledEntry(entry));
}

export function commandMarkers(names: readonly string[]): string {
  return names.map((name) => `[command: ${name}]`).join(" ");
}

// Known values sort first, ascending; undefined sorts last. Shared by the
// durable-entry sort and the pending projection's final ordering.
export function compareKnownNumber(left: number | undefined, right: number | undefined): number {
  if (left === undefined && right === undefined) return 0;
  if (left === undefined) return 1;
  if (right === undefined) return -1;
  return left - right;
}

interface TextualInput {
  text: string;
  skillNames: string[];
  commandNames: string[];
}

export function textualInput(input: readonly InputItem[] | undefined): TextualInput {
  return {
    text:
      input
        ?.filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
        .map((item) => item.text)
        .join("\n") ?? "",
    skillNames: canonicalSkillNames(
      input
        ?.filter((item): item is InputItem & { name: string } => item.type === "skill" && typeof item.name === "string")
        .map((item) => item.name),
    ),
    commandNames: canonicalSkillNames(
      input
        ?.filter(
          (item): item is InputItem & { name: string } => item.type === "command" && typeof item.name === "string",
        )
        .map((item) => item.name),
    ),
  };
}

function inputPreview(input: InputItem[] | undefined): TextualInput & { imageCount: number } {
  const content = textualInput(input);
  const imageCount = input?.filter((item) => item.type === "image").length ?? 0;
  return { ...content, imageCount };
}

type PendingRecord = MutationOutboxRecord | MutationOptimisticRecord;

export function outboxInput(record: MutationRecord): InputItem[] | undefined {
  const display = record.optimisticDisplay;
  if (display && typeof display === "object" && "input" in display && Array.isArray(display.input)) {
    return display.input as InputItem[];
  }
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : undefined;
}

// A durable record's display parts, through the same extraction every
// projected pending entry uses: the optimisticDisplay.input carries a
// promoted row's composed content (its wire params carry only the queue
// position), so it is preferred over the payload input every other method
// populates. The surfaces that render or copy a durable row directly read
// this, so a durable row and its optimistic twin can never compose the same
// message differently.
export function outboxRecordPreview(record: PendingRecord): {
  text: string;
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
} {
  return inputPreview(outboxInput(record));
}

// The transcript-item half of the reflected-identity rule, shared by the
// reflected set below and the pending projection's retirement check.
// A fresh set every call, deliberately: callers that need to test membership
// against extra identities add to the result (settleConsumedFromModel), so a
// shared instance would corrupt every later read of the same model. The
// pending store memoizes its own copy in transcriptIdsFor when it needs one.
export function transcriptMutationIds(model: ThreadModel | undefined): Set<string> {
  const ids = new Set<string>();
  for (const turn of model?.turns ?? []) {
    for (const item of turn.items) {
      const clientMutationId = (item as typeof item & { clientMutationId?: string }).clientMutationId;
      if (clientMutationId) ids.add(clientMutationId);
    }
  }
  return ids;
}

// The conversation input ids a recorded user item speaks for. A promotion or
// drain records its own client mutation id but inherits the consumed queue
// entry's input ids, so this is what ties a delivered message back to the
// queue entry another client consumed. A daemon that does not send the field
// yields nothing, and callers fall back to identity matching.
export function recordedInputLineage(model: ThreadModel | undefined): Set<string> {
  const ids = new Set<string>();
  for (const turn of model?.turns ?? []) {
    for (const item of turn.items) {
      // Only an item that RENDERS as the user's own message is a replacement
      // the reader can see. A promotion or drain records type "steering" with
      // source "user", and SteeringItem draws that through UserMessageView, so
      // it counts. An agent or system steering item does not: it renders
      // inside the turn it steered, and retiring a carried row on it would drop
      // the only visible copy of the message.
      const rendersAsUserMessage = item.type === "userMessage" || (item.type === "steering" && item.source === "user");
      if (!rendersAsUserMessage) continue;
      for (const id of item.conversationInputIds ?? []) {
        ids.add(id);
      }
    }
  }
  return ids;
}

// The ONE home of the reflected-identity rule - which client mutation ids
// have landed in the live model (queue rows or transcript items). Both the
// pending reconciliation below and the held-steer announcements region
// consume it, so a departure's outcome classification can never diverge
// from reconcile's own settle rule.
export function reflectedMutationIds(model: ThreadModel | undefined): Set<string> {
  const ids = new Set(model?.queue?.clientMutationIds ?? []);
  for (const id of transcriptMutationIds(model)) ids.add(id);
  return ids;
}

function outboxEntry(
  record: PendingRecord,
  isOwnMutationRecord: (record: { originClientId?: string }) => boolean,
): PendingTurnEntry | undefined {
  const method = composerVerb(record.method);
  if (!method) return undefined;
  // A superseded row is settled terminal work: its send lives in the fresh
  // row a mint or reconcile queued (which projects on its own), so the dead
  // predecessor leaves every pending surface - it is not a pending entry in
  // any state.
  if (record.state === "superseded") return undefined;
  const preview = inputPreview(outboxInput(record));
  // A receipt or pending projection names the entry a command owns. An active
  // promotion also names the entry it is replacing so the queue row hides
  // behind it while the command is in flight. A canceled promotion never
  // consumed the entry and must keep its own row.
  const queueEntryId =
    record.queueEntryIds?.[0] ??
    (method === "promote" && record.state !== "canceled" ? record.payload.expectedEntryId : undefined);
  const queueRevision = method === "drain" ? record.payload.expectedQueueRevision : undefined;
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
    ...(typeof queueEntryId === "string" ? { queueEntryId } : {}),
    ...(typeof queueRevision === "number" ? { queueRevision } : {}),
  };
}

function authoritativeEntry(
  ref: string,
  mutation: PendingMutation,
  fromThisClient: boolean,
  submittedHere: ReadonlyMap<string, number>,
  durableCreatedAt: number | undefined,
): PendingTurnEntry | undefined {
  const method = composerVerb(mutation.method);
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
    // Key the durable side by the record's effective wire id - the same key
    // space the daemon's feed names - and let a reflected feed id retire a
    // durable replacement that carries it: a replacement owns a fresh storage
    // key while traveling under its predecessor's wire id, and any other
    // keying projects the one composition twice. The entry's own id stays
    // the storage key; only the projection's identity is the wire id.
    const wireId = effectiveWireId(record);
    if (record.targetRef !== ref || reflected.has(wireId)) continue;
    const entry = outboxEntry(record, isOwnMutationRecord);
    if (entry) entries.set(wireId, entry);
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
  // keeps submission order in-session, an accepted corner. This is
  // the ONE home of the rule: the pending stack renders this order.
  return [...entries.values()].sort((left, right) => compareKnownNumber(left.createdAt, right.createdAt));
}
