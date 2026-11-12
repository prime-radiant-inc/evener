// One row per user message, across every place an undelivered message can
// live: the local outbox/optimistic/recovery stores, the daemon's input queue,
// and the daemon's pending executions. The transcript item is the only thing
// that retires a row; queue and pending-execution frames update it, never
// remove it. This is the projection the session's pending stack renders.
//
// `reconcilePendingEntries` remains the transport/routing reconciliation
// (ownPendingSend, Retry eligibility, composer routing). This module builds on
// the same identity rule and adds the queue merge, the drain/promote
// suppression, images, and the visible-delivery retirement the stack needs.

import type { ComposerMention } from "../../composerInput";
import { canonicalSkillNames } from "../../composerInput";
import type { ThreadModel } from "../../model";
import { isQueueParked } from "../../submitRouting";
import type { InputItem, PendingMutation } from "../../types.gen";
import { composerVerb } from "./composerMethods";
import {
  commandMarkers,
  compareKnownNumber,
  normalizeText,
  outboxInput,
  type PendingMethod,
  parseImagePlaceholder,
  pendingEntryPreview,
  reconcilePendingEntries,
  recordedInputLineage,
  skillMarkers,
  textualInput,
  transcriptMutationIds,
} from "./pendingEntries";
import type {
  MutationAttachmentRef,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "./records";
import { effectiveWireId } from "./records";

export type PendingMessageState =
  | "waiting"
  | "sending"
  | "queued"
  | "held"
  | "steering"
  | "delivering"
  | "uncertain"
  | "canceled"
  | "refused"
  | "orphaned";

export interface PendingMessageImage {
  src: string;
  name?: string;
}

export interface PendingMessage<A extends MutationAttachmentRef = MutationAttachmentRef> {
  // The wire identity (`clientMutationId`) every owner shares. A legacy queue
  // row with no mutation id is keyed `queue:<entryId>` so it still renders
  // exactly once.
  id: string;
  ref: string;
  method: PendingMethod;
  state: PendingMessageState;
  text: string;
  // The bubble's display composition: `text` plus the image placeholder and
  // selection markers. Actions still use the raw `text`.
  displayText: string;
  // Whether `text` carries the full composed prose. A queue row on an older
  // daemon may only have the truncated preview, which must stay visible but is
  // not safe to restore into the composer.
  textComplete: boolean;
  images: PendingMessageImage[];
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
  mentions?: readonly ComposerMention[];
  createdAt?: number;
  intentSequence?: number;
  fromThisClient: boolean;
  // The live queue position, when this row is backed by a daemon queue entry.
  // Cancel and promote send it with the entry id.
  queueIndex?: number;
  queueEntryId?: string;
  // The queue entries a drain or promote consumes; those queue rows are
  // represented by this row instead of rendering beside it.
  queueEntryIds: string[];
  queueRevision?: number;
  recoveryKind?: MutationRecoveryKind;
  reason?: string;
  // A refused Stop has no user composition, but it is still waiting work the
  // user must dismiss. It renders as a row with only Dismiss.
  interrupt?: boolean;
  // A refused queue cancellation names the control that failed, not a message
  // the user can edit or resend.
  controlFailure?: "cancel";
  // The durable source this row came from, for the action layer. At most one
  // of these describes a given position in the wire family; recovery wins.
  outbox?: MutationOutboxRecord<A>;
  optimistic?: MutationOptimisticRecord<A>;
  recovery?: MutationRecoveryRecord<A>;
}

export interface PendingMessagesInput<A extends MutationAttachmentRef = MutationAttachmentRef> {
  ref: string;
  outbox: readonly MutationOutboxRecord<A>[];
  optimistic: readonly MutationOptimisticRecord<A>[];
  recovery: readonly MutationRecoveryRecord<A>[];
  // Queue entries this client saw consumed by another client's transition but
  // never owned locally. Their content has no other durable copy here, so they
  // stay visible until an authoritative snapshot names their replacement.
  consumedQueueEntries?: readonly ConsumedQueueEntry[];
  model: ThreadModel | undefined;
  // Ids the client has already seen as delivered. A later bounded read that
  // drops the item cannot resurrect the row.
  deliveredHere?: ReadonlySet<string>;
  // The transcript identity set, when the caller already computed it for the
  // same model. Avoids a second full transcript walk per projection.
  transcriptIds?: ReadonlySet<string>;
  // The model's queue and pending mutations are current durable authority, not
  // a saved or partial snapshot.
  mutationStateAuthoritative?: boolean;
  // This model is still the one a successfully published authoritative
  // hydration produced. A queue-only frame clears it until the next snapshot.
  mutationSnapshotCurrent?: boolean;
  submittedHere?: ReadonlyMap<string, number>;
  isOwnMutationRecord: (record: { originClientId?: string }) => boolean;
}

export interface ConsumedQueueEntry {
  entryId: string;
  // The queue command that produced this entry. A recorded item carrying this
  // identity proves the message landed; any other recorded item does not.
  clientMutationId: string;
  text: string;
  // False when the content came from the daemon's truncated preview.
  textComplete: boolean;
  imageCount: number;
  skillNames: readonly string[];
  commandNames: readonly string[];
}

export { transcriptMutationIds };

interface InputContent {
  text: string;
  images: PendingMessageImage[];
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
  mentions: ComposerMention[];
}

// The same input array is read by the base row and again when a queue row
// merges into it. Cache the decoded content so image data URIs are built once.
const NO_INPUT: readonly InputItem[] = [];
const inputContentCache = new WeakMap<readonly InputItem[], InputContent>();

function inputContent(input: readonly InputItem[]): InputContent {
  const cached = inputContentCache.get(input);
  if (cached) return cached;
  const content = textualInput(input);
  const images: PendingMessageImage[] = [];
  const mentions: ComposerMention[] = [];
  for (const item of input) {
    if (item.type === "image" && typeof item.data === "string") {
      images.push({
        src: `data:${item.mediaType ?? "image/png"};base64,${item.data}`,
        ...(item.name ? { name: item.name } : {}),
      });
    }
    for (const mention of item.mentions ?? []) {
      if (mention.kind === "skill" || mention.kind === "command") {
        mentions.push({ kind: mention.kind, name: mention.name, offset: mention.offset });
      }
    }
  }
  const result = {
    ...content,
    images,
    imageCount: images.length,
    mentions,
  };
  inputContentCache.set(input, result);
  return result;
}

// Pending rows render the actual image bytes when they have them, so the
// composer's text-only `[image]` placeholder is only for a queue row whose
// bytes this client does not hold.
function composeDisplayText(
  text: string,
  images: readonly PendingMessageImage[],
  imageCount: number,
  skillNames: readonly string[],
  commandNames: readonly string[],
): string {
  return pendingEntryPreview({
    text,
    imageCount: images.length > 0 ? 0 : imageCount,
    skillNames,
    commandNames,
  });
}

// A preview-only legacy row still carries the daemon's selection arrays. The
// named markers replace a synthetic selection-only preview, while literal user
// text keeps its own brackets.
function legacyQueueDisplayText(text: string, skillNames: readonly string[], commandNames: readonly string[]): string {
  const namedMarkers = [skillMarkers(skillNames), commandMarkers(commandNames)].filter(Boolean).join(" ");
  const genericSelection = namedMarkers !== "" && /^\[\d*\s*(skills?|commands?)\]$/i.test(text.trim());
  return [genericSelection ? "" : text, namedMarkers].filter((part) => part !== "").join(" ");
}

function sameStrings(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

interface LocalFamily<A extends MutationAttachmentRef> {
  outbox?: MutationOutboxRecord<A>;
  optimistic?: MutationOptimisticRecord<A>;
  recovery?: MutationRecoveryRecord<A>;
}

function addLocal<A extends MutationAttachmentRef>(
  families: Map<string, LocalFamily<A>>,
  record: MutationRecord<A>,
): void {
  const id = effectiveWireId(record);
  const family = families.get(id) ?? {};
  if ("recoveryKind" in record) family.recovery = record as MutationRecoveryRecord<A>;
  else if ("state" in record && record.state === "accepted") family.optimistic = record as MutationOptimisticRecord<A>;
  else family.outbox = record as MutationOutboxRecord<A>;
  families.set(id, family);
}

function familyRecord<A extends MutationAttachmentRef>(
  family: LocalFamily<A> | undefined,
): MutationRecord<A> | undefined {
  return family?.recovery ?? family?.outbox ?? family?.optimistic;
}

function familyCreatedAt<A extends MutationAttachmentRef>(family: LocalFamily<A> | undefined): number | undefined {
  return familyRecord(family)?.createdAt;
}

function familyInput<A extends MutationAttachmentRef>(family: LocalFamily<A> | undefined): InputItem[] | undefined {
  const record = familyRecord(family);
  if (!record) return undefined;
  const input = outboxInput(record) ?? [];
  return input.length > 0 ? input : undefined;
}

function withoutEditedRecovery<A extends MutationAttachmentRef>(
  family: LocalFamily<A> | undefined,
): LocalFamily<A> | undefined {
  return family?.recovery && (family.recovery.compositionRevision ?? 0) > 0
    ? { ...family, recovery: undefined }
    : family;
}

function recoveryRowId(recovery: MutationRecoveryRecord): string {
  const wireId = effectiveWireId(recovery);
  const revision = recovery.compositionRevision ?? 0;
  return revision > 0 ? `${wireId}:edited:${recovery.clientMutationId}:${revision}` : wireId;
}

function methodState(method: PendingMethod, claimed: boolean): PendingMessageState {
  if (claimed) return "delivering";
  switch (method) {
    case "send":
      return "sending";
    case "queue":
      return "queued";
    case "steer":
    case "drain":
    case "promote":
      return "steering";
  }
}

function entryState(state: string, method: PendingMethod): PendingMessageState {
  switch (state) {
    case "blockedUnknown":
      return "uncertain";
    case "canceled":
      return "canceled";
    default:
      return methodState(method, false);
  }
}

function durableState<A extends MutationAttachmentRef>(
  family: LocalFamily<A> | undefined,
  pending: PendingMutation | undefined,
  method: PendingMethod,
): PendingMessageState | undefined {
  const recovery = family?.recovery;
  if (recovery) return recovery.recoveryKind === "orphaned" ? "orphaned" : "refused";
  const outbox = family?.outbox;
  if (outbox?.state === "blockedUnknown") return "uncertain";
  if (outbox?.state === "canceled") return "canceled";
  if (pending) {
    const claimed = pending.executionState === "claimed";
    return methodState(method, claimed);
  }
  if (outbox?.state === "submitting") return outbox.attempted === false ? "waiting" : "sending";
  if (family?.optimistic) {
    // The daemon no longer lists the execution and the transcript has not
    // shown the item yet: the message is on its way, not gone.
    return "delivering";
  }
  return undefined;
}

function rowFromBase<A extends MutationAttachmentRef>(
  entry: {
    id: string;
    ref: string;
    method: PendingMethod;
    text: string;
    imageCount: number;
    skillNames: string[];
    commandNames?: string[];
    createdAt?: number;
    state: string;
    fromThisClient: boolean;
    queueEntryId?: string;
    queueRevision?: number;
  },
  families: Map<string, LocalFamily<A>>,
  pendingById: Map<string, PendingMutation>,
): PendingMessage<A> {
  const family = families.get(entry.id);
  const baseFamily = withoutEditedRecovery(family);
  const pending = pendingById.get(entry.id);
  const input = pending?.input && pending.input.length > 0 ? pending.input : familyInput(baseFamily);
  const content = input ? inputContent(input) : undefined;
  const text = content?.text ?? entry.text;
  const images = content?.images ?? [];
  const imageCount = content?.imageCount ?? entry.imageCount;
  const skillNames = content?.skillNames ?? entry.skillNames;
  const commandNames = content?.commandNames ?? entry.commandNames ?? [];
  const familyEntry = familyRecord(baseFamily);
  const method = entry.method;
  const state = durableState(baseFamily, pending, method) ?? entryState(entry.state, method);
  const queueEntryIds = pending?.queueEntryIds ?? (entry.queueEntryId ? [entry.queueEntryId] : []);
  const localQueueEntryIds =
    baseFamily?.optimistic?.queueEntryIds ??
    baseFamily?.outbox?.queueEntryIds ??
    baseFamily?.recovery?.queueEntryIds ??
    [];
  return {
    id: entry.id,
    ref: entry.ref,
    method,
    state,
    text,
    displayText: composeDisplayText(text, images, imageCount, skillNames, commandNames),
    textComplete: true,
    images,
    imageCount,
    skillNames,
    commandNames,
    ...(content && content.mentions.length > 0 ? { mentions: content.mentions } : {}),
    ...(entry.createdAt !== undefined ? { createdAt: entry.createdAt } : {}),
    ...(familyEntry?.intentSequence !== undefined ? { intentSequence: familyEntry.intentSequence } : {}),
    fromThisClient: entry.fromThisClient,
    ...(entry.queueEntryId ? { queueEntryId: entry.queueEntryId } : {}),
    queueEntryIds: [...new Set([...queueEntryIds, ...localQueueEntryIds])],
    ...(entry.queueRevision !== undefined ? { queueRevision: entry.queueRevision } : {}),
    ...(baseFamily?.recovery
      ? {
          recoveryKind: baseFamily.recovery.recoveryKind,
          ...(baseFamily.recovery.recoveryReason !== undefined ? { reason: baseFamily.recovery.recoveryReason } : {}),
          recovery: baseFamily.recovery,
        }
      : {}),
    ...(family?.outbox ? { outbox: family.outbox } : {}),
    ...(family?.optimistic ? { optimistic: family.optimistic } : {}),
  };
}

function mergeQueueContent<A extends MutationAttachmentRef>(
  row: PendingMessage<A>,
  family: LocalFamily<A> | undefined,
  pending: PendingMutation | undefined,
): PendingMessage<A> {
  const input = pending?.input ?? familyInput(withoutEditedRecovery(family));
  if (input && input.length > 0) {
    const content = inputContent(input);
    return {
      ...row,
      text: content.text,
      displayText: composeDisplayText(
        content.text,
        content.images,
        content.imageCount,
        content.skillNames,
        content.commandNames,
      ),
      textComplete: true,
      images: content.images,
      imageCount: content.imageCount,
      skillNames: content.skillNames,
      commandNames: content.commandNames,
      ...(content.mentions.length > 0 ? { mentions: content.mentions } : {}),
    };
  }
  return row;
}

function compareRows(left: PendingMessage, right: PendingMessage): number {
  return (
    compareKnownNumber(left.createdAt, right.createdAt) ||
    compareKnownNumber(left.intentSequence, right.intentSequence) ||
    compareKnownNumber(left.queueIndex, right.queueIndex) ||
    left.id.localeCompare(right.id)
  );
}

function drainFallback<A extends MutationAttachmentRef>(row: PendingMessage<A>): PendingMessage<A> {
  return row.method === "drain" && row.displayText === "" ? { ...row, displayText: "[queued messages]" } : row;
}

function isSteeringRow(row: PendingMessage): boolean {
  return (
    (row.method === "steer" || row.method === "drain" || row.method === "promote") &&
    (row.state === "steering" || row.state === "delivering" || row.state === "sending")
  );
}

// A confirmed queue entry is the durable lineage shared by the original queue
// command and the promotion, drain or delivery that consumes it. Join those
// commands before rendering so the message keeps one row and one identity
// across the transition.
function mergePendingRows<A extends MutationAttachmentRef>(
  existing: PendingMessage<A>,
  row: PendingMessage<A>,
): PendingMessage<A> {
  return {
    ...existing,
    ...(isSteeringRow(row)
      ? {
          method: row.method,
          state: row.state,
          text: row.text,
          displayText: row.displayText,
          textComplete: row.textComplete,
          images: row.images,
          imageCount: row.imageCount,
          skillNames: row.skillNames,
          commandNames: row.commandNames,
          ...(row.mentions !== undefined ? { mentions: row.mentions } : {}),
        }
      : {}),
    ...(row.queueEntryId !== undefined ? { queueEntryId: row.queueEntryId } : {}),
    ...(row.queueIndex !== undefined ? { queueIndex: row.queueIndex } : {}),
    ...(row.queueRevision !== undefined ? { queueRevision: row.queueRevision } : {}),
    queueEntryIds: [...new Set([...existing.queueEntryIds, ...row.queueEntryIds])],
    fromThisClient: existing.fromThisClient || row.fromThisClient,
    ...(row.outbox !== undefined ? { outbox: row.outbox } : {}),
    ...(row.optimistic !== undefined ? { optimistic: row.optimistic } : {}),
    ...(row.recovery !== undefined
      ? {
          recovery: row.recovery,
          recoveryKind: row.recoveryKind,
          ...(row.reason !== undefined ? { reason: row.reason } : {}),
        }
      : {}),
  };
}

function mergeLineageRows<A extends MutationAttachmentRef>(rows: PendingMessage<A>[]): PendingMessage<A>[] {
  const byEntryId = new Map<string, PendingMessage<A>>();
  const out: PendingMessage<A>[] = [];
  for (const row of rows) {
    const intersecting = [
      ...new Set(
        row.queueEntryIds
          .map((id) => byEntryId.get(id))
          .filter((candidate): candidate is PendingMessage<A> => candidate !== undefined),
      ),
    ];
    if (intersecting.length === 0) {
      out.push(row);
      for (const id of row.queueEntryIds) byEntryId.set(id, row);
      continue;
    }
    const [first, ...rest] = intersecting;
    if (!first) continue;
    let merged = first;
    for (const other of rest) merged = mergePendingRows(merged, other);
    merged = mergePendingRows(merged, row);
    for (const other of intersecting) {
      const index = out.indexOf(other);
      if (index >= 0) out.splice(index, 1);
    }
    out.push(merged);
    for (const id of merged.queueEntryIds) byEntryId.set(id, merged);
  }
  return out;
}

// A retained accepted copy whose confirmed queue entry is gone from both the
// queue and the daemon's pending projection has been delivered or discarded by
// another client. Require a known-complete queue/pending snapshot and a recorded
// user item before hiding it, so an older daemon that omits consumed ids still
// clears the source row without ever blinking the message out mid-transition.
function deliveredSourceRow<A extends MutationAttachmentRef>(
  row: PendingMessage<A>,
  queuedEntryIds: ReadonlySet<string>,
  pendingEntryIds: ReadonlySet<string>,
  pendingMutationIds: ReadonlySet<string>,
  queueKnown: boolean,
  pendingKnown: boolean,
  mutationStateAuthoritative: boolean,
  mutationSnapshotCurrent: boolean,
  deliveredAny: boolean,
): boolean {
  if (!deliveredAny || !queueKnown || !pendingKnown || !mutationStateAuthoritative || !mutationSnapshotCurrent)
    return false;
  // Only the original queue command is a source. A promotion or drain owns the
  // queue entry it consumed, so its own accepted copy must stay until the
  // transcript records that mutation.
  if (row.method !== "queue") return false;
  if (pendingMutationIds.has(row.id)) return false;
  if (!row.optimistic || row.outbox || row.recovery) return false;
  if (row.queueEntryIds.length === 0) return false;
  return row.queueEntryIds.every((id) => !queuedEntryIds.has(id) && !pendingEntryIds.has(id));
}

export function projectPendingMessages<A extends MutationAttachmentRef = MutationAttachmentRef>(
  input: PendingMessagesInput<A>,
): PendingMessage<A>[] {
  const { ref, model } = input;
  const transcriptIds = input.transcriptIds ?? transcriptMutationIds(model);
  const isDelivered = (id: string): boolean => transcriptIds.has(id) || input.deliveredHere?.has(id) === true;
  const queuedEntryIds = new Set(model?.queue?.ids ?? []);
  const queueKnown = model?.queue != null && (model.queue.ids !== undefined || (model.queue.depth ?? 0) === 0);
  const pendingEntryIds = new Set<string>();
  const pendingMutationIds = new Set<string>();
  for (const mutation of model?.pendingMutations ?? []) {
    pendingMutationIds.add(mutation.clientMutationId);
    for (const id of mutation.queueEntryIds ?? []) pendingEntryIds.add(id);
  }
  const pendingKnown = model?.pendingMutations !== undefined;
  // Inputs the daemon retired before they reached canonical history: a
  // canceled message, or one whose preparation failed. Nothing will ever
  // record them, so they are positive evidence that a pending row is done.
  const retiredInputs = new Set(
    (model?.conversationProvenance?.retiredInputs ?? []).map((id) =>
      id.startsWith("client:") ? id.slice("client:".length) : id,
    ),
  );
  const recordedLineage = recordedInputLineage(model);
  // A drain, promotion or delivery that consumed an entry records ITS OWN
  // mutation id in the transcript, never the consumed entry's. The daemon does
  // name the lineage: a pending mutation carries the queueEntryIds it owns. So
  // an entry is delivered when a recorded mutation owns it, or when the
  // consumed entry's own id landed (an older daemon that never promoted it).
  // Lineage names the conversation INPUT, which the daemon derives from the
  // client mutation that produced the entry (`client:<clientMutationId>`), so
  // it must be compared against that identity. A queue ENTRY id is a different
  // value and only the pending mutation's queueEntryIds speak in it.
  const recordedOwnerOf = (entry: { entryId: string; clientMutationId: string }): boolean =>
    (model?.pendingMutations ?? []).some(
      (mutation) =>
        (mutation.queueEntryIds ?? []).includes(entry.entryId) && transcriptIds.has(mutation.clientMutationId),
    ) ||
    // A recorded item that inherited this entry's conversation input is the
    // same proof for a promotion or drain whose pending owner already settled.
    recordedLineage.has(`client:${entry.clientMutationId}`);
  const deliveredAny = (input.deliveredHere?.size ?? 0) > 0 || transcriptIds.size > 0;
  const mutationStateAuthoritative = input.mutationStateAuthoritative === true;
  const mutationSnapshotCurrent = input.mutationSnapshotCurrent === true;
  // A consumed row this client never owned stays until a current snapshot
  // proves the replacement landed, or names it again in the queue.
  const carriedConsumed = (input.consumedQueueEntries ?? []).filter(
    (entry) =>
      !queuedEntryIds.has(entry.entryId) &&
      !pendingEntryIds.has(entry.entryId) &&
      !(mutationStateAuthoritative && mutationSnapshotCurrent && retiredInputs.has(entry.clientMutationId)) &&
      !(
        mutationStateAuthoritative &&
        mutationSnapshotCurrent &&
        (transcriptIds.has(entry.clientMutationId) || recordedOwnerOf(entry))
      ),
  );

  const families = new Map<string, LocalFamily<A>>();
  const localOutbox = input.outbox.filter((record) => record.targetRef === ref || record.displayedRef === ref);
  for (const record of localOutbox) addLocal(families, record);
  for (const record of input.optimistic) if (record.targetRef === ref) addLocal(families, record);
  for (const record of input.recovery) {
    if (record.targetRef === ref || record.displayedRef === ref) addLocal(families, record);
  }
  // A Retry mints a fresh storage key that travels under its predecessor's
  // wire id. The projection's row identity, delivered set and family lookups
  // use the wire id; the record keeps its own key for actions.
  const wireByStorageKey = new Map<string, string>();
  for (const [wireId, family] of families) {
    for (const record of [family.recovery, family.outbox, family.optimistic]) {
      if (record) wireByStorageKey.set(record.clientMutationId, wireId);
    }
  }

  const pendingById = new Map<string, PendingMutation>();
  for (const pending of model?.pendingMutations ?? []) pendingById.set(pending.clientMutationId, pending);

  // Build the local/authoritative base entries against a model that cannot
  // reflect any of them: queue and transcript reflection is applied here, so a
  // queue row can absorb its local record instead of the local record
  // disappearing into a surface this projection does not render.
  const reconciliationModel: ThreadModel | undefined = model
    ? {
        ...model,
        queue: model.queue ? { ...model.queue, clientMutationIds: [] } : null,
        turns: [],
      }
    : undefined;
  const base = reconcilePendingEntries(
    ref,
    [
      ...localOutbox.map((record) => (record.targetRef === ref ? record : { ...record, targetRef: ref })),
      ...input.optimistic.filter((record) => record.targetRef === ref),
    ],
    reconciliationModel,
    input.submittedHere ?? new Map(),
    input.isOwnMutationRecord,
  ).filter((entry) => !isDelivered(wireByStorageKey.get(entry.id) ?? entry.id));

  const rows = new Map<string, PendingMessage<A>>();
  for (const entry of base) {
    const wireId = wireByStorageKey.get(entry.id) ?? entry.id;
    rows.set(wireId, rowFromBase({ ...entry, id: wireId }, families, pendingById));
  }

  // Recovery rows that never became a pending entry (refused, orphaned,
  // interrupt refusals) are pending messages too.
  for (const [id, family] of families) {
    const recovery = family.recovery;
    if (!recovery) continue;
    const cancelFailure = recovery.method === "turn/cancelQueued";
    const rowId = cancelFailure ? `${id}:cancel-failure:${recovery.clientMutationId}` : recoveryRowId(recovery);
    if (rows.has(rowId) || (rowId === id && isDelivered(id))) continue;
    const method = composerVerb(recovery.method);
    const interrupt = recovery.method === "turn/interrupt";
    if (!method && !interrupt && !cancelFailure) continue;
    const collected = inputContent(familyInput(family) ?? NO_INPUT);
    rows.set(rowId, {
      id: rowId,
      ref,
      method: method ?? (cancelFailure ? "queue" : "send"),
      state: recovery.recoveryKind === "orphaned" ? "orphaned" : "refused",
      text: collected.text,
      displayText: composeDisplayText(
        collected.text,
        collected.images,
        collected.imageCount,
        collected.skillNames,
        collected.commandNames,
      ),
      textComplete: true,
      images: collected.images,
      imageCount: collected.imageCount,
      skillNames: collected.skillNames,
      commandNames: collected.commandNames,
      ...(recovery.createdAt !== undefined ? { createdAt: recovery.createdAt } : {}),
      ...(recovery.intentSequence !== undefined ? { intentSequence: recovery.intentSequence } : {}),
      fromThisClient: input.isOwnMutationRecord(recovery),
      queueEntryIds: [],
      recoveryKind: recovery.recoveryKind,
      ...(interrupt || cancelFailure ? { interrupt: true } : {}),
      ...(cancelFailure ? { controlFailure: "cancel" } : {}),
      ...(recovery.recoveryReason !== undefined ? { reason: recovery.recoveryReason } : {}),
      recovery,
    });
  }

  // Queue rows: one per daemon entry, merged with the local/pending row that
  // shares its identity or its queue entry id.
  const queue = model?.queue;
  const rowCount = queue?.preview?.length ?? queue?.texts?.length ?? queue?.ids?.length ?? queue?.depth ?? 0;
  const queueRows: PendingMessage<A>[] = [];
  const ownerByEntryId = new Map<string, PendingMessage<A>>();
  for (const row of rows.values()) {
    for (const ownedId of row.queueEntryIds) ownerByEntryId.set(ownedId, row);
    if (row.queueEntryId) ownerByEntryId.set(row.queueEntryId, row);
  }
  for (let index = 0; index < rowCount; index += 1) {
    const entryId = queue?.ids?.[index];
    const mutationId = queue?.clientMutationIds?.[index];
    const fullText = queue?.texts?.[index];
    const text = fullText ?? queue?.preview?.[index] ?? "";
    const preview = queue?.preview?.[index];
    const imageCount = parseImagePlaceholder(preview);
    const skillNames = canonicalSkillNames(queue?.skillNames?.[index] ?? []);
    const commandNames = canonicalSkillNames(queue?.commandNames?.[index] ?? []);
    const queueMentions = queue?.mentions?.[index];
    const local = mutationId ? families.get(mutationId) : undefined;
    const localRecord = familyRecord(local);
    const pending = mutationId ? pendingById.get(mutationId) : undefined;
    const id = mutationId ?? (entryId ? `queue:${entryId}` : `queue:${index}`);
    if (isDelivered(id)) continue;
    const createdAt = (mutationId ? input.submittedHere?.get(mutationId) : undefined) ?? familyCreatedAt(local);
    const row: PendingMessage<A> = {
      id,
      ref,
      method: "queue",
      state: isQueueParked(model?.status.type ?? "", queue?.depth ?? 0) ? "held" : "queued",
      text,
      displayText:
        fullText !== undefined
          ? pendingEntryPreview({
              text,
              imageCount,
              skillNames,
              commandNames,
            })
          : legacyQueueDisplayText(text, skillNames, commandNames),
      textComplete: fullText !== undefined,
      images: [],
      imageCount,
      skillNames,
      commandNames,
      ...(queueMentions
        ? {
            mentions: queueMentions.flatMap((mention): ComposerMention[] =>
              mention.kind === "skill" || mention.kind === "command" ? [{ ...mention, kind: mention.kind }] : [],
            ),
          }
        : {}),
      ...(createdAt !== undefined ? { createdAt } : {}),
      ...(localRecord?.intentSequence !== undefined ? { intentSequence: localRecord.intentSequence } : {}),
      fromThisClient: mutationId
        ? (input.submittedHere?.has(mutationId) ?? false) ||
          (localRecord ? input.isOwnMutationRecord(localRecord) : false)
        : false,
      queueIndex: index,
      ...(entryId ? { queueEntryId: entryId } : {}),
      queueEntryIds: entryId ? [entryId] : [],
      ...(queue?.revision !== undefined ? { queueRevision: queue.revision } : {}),
      ...(localRecord ? { outbox: local?.outbox, optimistic: local?.optimistic } : {}),
    };
    // The queue row is the pending representation of this message. If a
    // local/authoritative row already describes the same identity, update that
    // row instead of adding a second one. A drain or promote owns the queue
    // entries it names, so those queue rows disappear in its favor.
    let owner = entryId ? ownerByEntryId.get(entryId) : undefined;
    if (!owner && mutationId) owner = rows.get(mutationId);
    if (owner) {
      const withQueue = mergeQueueContent(owner, families.get(owner.id), pendingById.get(owner.id));
      const next: PendingMessage<A> = {
        ...withQueue,
        // A queued message's state is the queue's (including the Stop-parked
        // held state). A drain or promote keeps its steering state.
        ...(withQueue.method === "queue" ? { state: row.state } : {}),
        ...(row.queueIndex !== undefined ? { queueIndex: row.queueIndex } : {}),
        ...(row.queueEntryId ? { queueEntryId: row.queueEntryId } : {}),
        ...(row.queueRevision !== undefined ? { queueRevision: row.queueRevision } : {}),
        queueEntryIds: [...new Set([...withQueue.queueEntryIds, ...(row.queueEntryId ? [row.queueEntryId] : [])])],
        displayText: withQueue.displayText !== "" ? withQueue.displayText : row.displayText,
        textComplete: withQueue.textComplete || row.textComplete,
        ...(row.mentions !== undefined ? { mentions: row.mentions } : {}),
      };
      rows.set(owner.id, next);
      if (entryId) ownerByEntryId.set(entryId, next);
    } else {
      queueRows.push(mergeQueueContent(row, local, pending));
    }
  }

  // A legacy queue row carries no clientMutationId and no entry-id link. Merge
  // it only when its complete text matches a still-unmatched local queue row;
  // a preview alone must not be restored or acted on as if it were full
  // content. Pairing in queue order keeps two identical messages as two rows.
  const matchedLocalIds = new Set<string>();
  const normalizedText = new Map<string, string>();
  for (const [id, row] of rows) normalizedText.set(id, normalizeText(row.text));
  for (const row of queueRows) {
    if (!row.id.startsWith("queue:") || !row.textComplete) continue;
    let match: PendingMessage<A> | undefined;
    const rowText = normalizeText(row.text);
    for (const candidate of rows.values()) {
      if (
        !matchedLocalIds.has(candidate.id) &&
        candidate.method === "queue" &&
        !candidate.queueEntryId &&
        candidate.outbox?.attempted !== false &&
        candidate.textComplete &&
        normalizedText.get(candidate.id) === rowText &&
        candidate.imageCount === row.imageCount &&
        sameStrings(candidate.skillNames, row.skillNames) &&
        sameStrings(candidate.commandNames, row.commandNames) &&
        candidate.queueEntryIds.length === 0
      ) {
        match = candidate;
        break;
      }
    }
    if (!match) continue;
    matchedLocalIds.add(match.id);
    row.id = match.id;
    row.text = match.text;
    row.displayText = match.displayText || row.displayText;
    row.textComplete = match.textComplete;
    row.images = match.images;
    row.imageCount = match.imageCount;
    row.skillNames = match.skillNames;
    row.commandNames = match.commandNames;
    if (row.mentions === undefined) row.mentions = match.mentions;
    row.createdAt = match.createdAt;
    row.fromThisClient = match.fromThisClient;
    row.outbox = match.outbox;
    row.optimistic = match.optimistic;
    row.recovery = match.recovery;
    row.recoveryKind = match.recoveryKind;
    rows.delete(match.id);
  }

  const all = mergeLineageRows([...queueRows, ...rows.values()])
    .filter(
      (row) =>
        !deliveredSourceRow(
          row,
          queuedEntryIds,
          pendingEntryIds,
          pendingMutationIds,
          queueKnown,
          pendingKnown,
          mutationStateAuthoritative,
          mutationSnapshotCurrent,
          deliveredAny,
        ),
    )
    .map(drainFallback);

  // A local row that already owns this entry is the message's one
  // representation; carrying it again would show the same text twice.
  const ownedEntryIds = new Set(all.flatMap((row) => row.queueEntryIds));
  const carriedRows: PendingMessage<A>[] = carriedConsumed
    .filter((entry) => !ownedEntryIds.has(entry.entryId))
    .map((entry) => ({
      id: `queue:${entry.entryId}`,
      ref,
      method: "queue",
      state: "delivering",
      text: entry.text,
      // Match the live queue row's own rule: full text composes with markers,
      // a preview-only row falls back to the legacy display so the same
      // message does not change text when another client consumes it.
      displayText: entry.textComplete
        ? pendingEntryPreview({
            text: entry.text,
            imageCount: entry.imageCount,
            skillNames: entry.skillNames,
            commandNames: entry.commandNames,
          })
        : legacyQueueDisplayText(entry.text, entry.skillNames, entry.commandNames),
      textComplete: entry.textComplete,
      images: [],
      imageCount: entry.imageCount,
      skillNames: [...entry.skillNames],
      commandNames: [...entry.commandNames],
      fromThisClient: false,
      queueEntryIds: [entry.entryId],
    }));

  // Carried rows are already deduped against every owned entry id above, so
  // there is nothing left for the lineage merge to join.
  return [...all, ...carriedRows].sort(compareRows);
}
