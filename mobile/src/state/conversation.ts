// ConversationStore — the active session's Zustand state. Wraps a
// ConversationService (which wraps an AppwireClient) and owns the conversation
// projection, draft, paging cursor, and mutation lifecycle.
//
// Generation safety (CRITICAL):
// - conversationGeneration increments on every open, close, and reset; late
//   frames from an older generation cannot overwrite a newer conversation's
//   state.
// - applyNotification checks threadId/ref against the current conversation and
//   silently drops mismatches.
// - Profile switching calls reset() before opening a new conversation.
// - The store never auto-retries a user mutation. On conflict, it restores the
//   draft (only if no new text was typed) and sets error.
//
// The store holds the MobileConversation projection (never the raw wire
// Thread). Notifications update the projection in place; a re-read via
// thread/read after evener/thread/resync is the authoritative refresh path
// (triggered by the store-owned drain scheduler, not timers).

import { create } from "zustand";
import {
  applyNotification,
  foldWarningParams,
  isActiveItem,
  isStaleCursorError,
  isToolCallItemId,
  isToolResultItemId,
  itemIdentityMatches,
  joinWarningParts,
  markItemTextOmitted,
  mergeOlderItemPageWithFolds,
  mergeTurnHistory,
  mergeTurnHistoryWithFolds,
  notificationTargetsThread,
  sessionControls,
  WireError,
} from "@evener/appwire-client";
import type {
  AnyNotification,
  AskQuestionRef,
  InputItem,
  ItemModel,
  MutationReceipt,
  ThreadItem,
  TurnModel,
} from "@evener/appwire-client";
import type {
  ActivityDetail,
  MobileConversation,
  MobileTimelineItem,
  ActivityMember,
} from "../conversation/project";
import {
  activityIdentity,
  activityState,
  attachmentSourceId,
  attachmentSourceIdentity,
  capItems as sharedCapItems,
  clusterActivities,
  itemAttachments,
  MAX_ITEM_BYTES,
  ownTimelineIdentities,
  projectItemAttachments,
  projectTimeline,
  RETAINED_ITEM_CAP,
  timelineIdentity,
  timelineIdentities,
  TRUNCATION_MARKER,
  truncateItem as sharedTruncateItem,
  truncateText,
} from "../conversation/project";
import type { ActivityView } from "../services/activity";
import type {
  ConversationReadProjection,
  ConversationService,
  LiveConversationService,
} from "../services/conversation";
import type { ActivityIdentity, NotificationOutcome } from "./activity";

function liveRevisionForItem(
  item: MobileTimelineItem,
  revisions: Map<string, number>,
): number {
  let revision = revisions.get(timelineIdentity(item)) ?? 0;
  for (const identity of timelineIdentities(item)) {
    revision = Math.max(revision, revisions.get(identity) ?? 0);
  }
  return revision;
}

function isLiveOwned(
  item: MobileTimelineItem,
  revisions: Map<string, number>,
): boolean {
  return [...timelineIdentities(item)].some((identity) => revisions.has(identity));
}

function projectActivityMembers(members: ActivityMember[]): MobileTimelineItem[] {
  return clusterActivities(
    members.map((member) => ({
      family: member.state === "failed" ? `failed:${member.id}` : member.family,
      item: { kind: "activity", ...member },
    })),
  );
}

function mergeLiveActivityMembers(
  snapshot: Extract<MobileTimelineItem, { kind: "activity" }>,
  currentItems: MobileTimelineItem[],
  revisions: Map<string, number>,
  entryRevision: number,
): { rows: MobileTimelineItem[]; supersededIdentities: string[] } | undefined {
  const liveMembers = new Map<string, ActivityMember>();
  for (const candidate of currentItems) {
    if (candidate.kind !== "activity") continue;
    for (const member of candidate.members ?? [candidate]) {
      const identity = activityIdentity(member);
      if ((revisions.get(identity) ?? 0) > entryRevision) {
        liveMembers.set(identity, member);
      }
    }
  }
  // Report which member identities the live side actually won, not the whole
  // cluster: a member the snapshot still owns is answerable to the reread, and
  // naming it superseded would carry its freeze forward for good.
  const supersededIdentities: string[] = [];
  const members = (snapshot.members ?? [snapshot]).map((member) => {
    const identity = activityIdentity(member);
    const current = liveMembers.get(identity);
    if (current) supersededIdentities.push(identity);
    return current ?? member;
  });
  if (supersededIdentities.length === 0) return undefined;
  // Reuse the lifecycle projector so failed members retain their own rows.
  return { rows: projectActivityMembers(members), supersededIdentities };
}

function itemsAbsentFromSnapshot(
  items: MobileTimelineItem[],
  snapshotIdentities: Set<string>,
  snapshotRows: Set<string>,
): MobileTimelineItem[] {
  return items.flatMap((item) => {
    if (item.kind !== "activity") {
      return snapshotRows.has(timelineIdentity(item)) ? [] : [item];
    }
    const members = item.members ?? [item];
    const omitted = members.filter(
      (member) => !snapshotIdentities.has(member.transcriptKey ?? member.id),
    );
    return omitted.length === members.length ? [item] : projectActivityMembers(omitted);
  });
}

function decorateLifecycleItem(
  item: MobileTimelineItem,
  source: ThreadItem,
): MobileTimelineItem {
  return {
    ...item,
    ...(source.transcriptKey ? { transcriptKey: source.transcriptKey } : {}),
    ...(source.position ? { position: source.position } : {}),
  };
}

// Snapshot/live-tail merging can introduce a companion after later messages.
// Keep attachments beside their source whenever both rows are retained.
function attachToSources(items: MobileTimelineItem[]): MobileTimelineItem[] {
  const sourceItems = items.filter((item) => item.kind !== "attachments");
  const ids = new Set(sourceItems.flatMap((item) => [...timelineIdentities(item)]));
  const companions = new Map<string, MobileTimelineItem>();
  for (const item of items) {
    const sourceId = attachmentSourceIdentity(item);
    if (sourceId !== null && ids.has(sourceId)) companions.set(sourceId, item);
  }
  return items.flatMap((item) => {
    const sourceId = attachmentSourceIdentity(item);
    if (sourceId !== null) return companions.has(sourceId) ? [] : [item];
    const attachments = [...timelineIdentities(item)].flatMap((identity) => {
      const companion = companions.get(identity);
      return companion ? [companion] : [];
    });
    return [item, ...attachments];
  });
}

function activityClusterSegments(
  cluster: Extract<MobileTimelineItem, { kind: "activity" }>,
  updatedIndex: number,
  updatedMember: ActivityMember,
): MobileTimelineItem[] {
  const members = cluster.members ? [...cluster.members] : [];
  members[updatedIndex] = updatedMember;
  return projectActivityMembers(members);
}

// The activity a live notification addresses by wire item id: either a
// top-level row, or one member of a clustered row. Wire ids address members
// directly, so every delta and the lifecycle handler resolve through this —
// searching top-level rows alone leaves a later member unreachable (a reread)
// and a first member stale behind the cluster's own detail.
interface ActivityTarget {
  row: Extract<MobileTimelineItem, { kind: "activity" }>;
  rowIndex: number;
  memberIndex: number | null;
  activity: ActivityMember;
}

function findActivityTargetBy(
  items: MobileTimelineItem[],
  matches: (activity: ActivityMember) => boolean,
): ActivityTarget | null {
  for (const [rowIndex, row] of items.entries()) {
    if (row.kind !== "activity") continue;
    const memberIndex = (row.members ?? []).findIndex(matches);
    const member = row.members?.[memberIndex];
    if (member) return { row, rowIndex, memberIndex, activity: member };
    if (matches(row)) {
      return { row, rowIndex, memberIndex: null, activity: row };
    }
  }
  return null;
}

// Deltas address their target by wire item id — they carry nothing else.
function findActivityTarget(
  items: MobileTimelineItem[],
  itemId: string,
): ActivityTarget | null {
  return findActivityTargetBy(items, (activity) => activity.id === itemId);
}

// Lifecycle events carry a transcriptKey, which outlives a changing wire id;
// this is the form that matches how they replace their target.
function findActivityTargetByIdentity(
  items: MobileTimelineItem[],
  identity: string,
): ActivityTarget | null {
  return findActivityTargetBy(
    items,
    (activity) => activityIdentity(activity) === identity,
  );
}

// Write a new detail onto the addressed activity. A clustered member is
// replaced through the cluster projector, so every other member keeps its own
// identity, output and truncation state, and the cluster's own top-level
// fields (which mirror its first member) stay in step with it.
function replaceActivityTargetDetail(
  items: MobileTimelineItem[],
  target: ActivityTarget,
  detail: ActivityDetail,
): MobileTimelineItem[] {
  const replacement =
    target.memberIndex === null
      ? [{ ...target.row, detail }]
      : activityClusterSegments(target.row, target.memberIndex, {
          ...target.activity,
          detail,
        });
  return items.flatMap((item, index) =>
    index === target.rowIndex ? replacement : [item],
  );
}

export type ConversationStatus =
  | "idle"
  | "opening"
  | "open"
  | "error"
  | "closed";

// Mutation lifecycle state for send/steer/queue/interrupt. The store tracks
// the kind, pending/failed status, the exact draft snapshot at submission,
// the draft revision at submission (so type-then-delete after clear is
// detected as an edit), and the conversation generation that initiated it.
// On failure, the failed state PERSISTS until a subsequent mutation or open
// clears it. The mutationId is a monotonically increasing private counter
// (F4) so out-of-order completion cannot change a newer mutation, error, or
// draft.
export interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  /** @internal — draft revision captured at submit; used to detect post-clear edits. */
  draftRevisionAtSubmit: number;
  generation: number;
  /** @internal — monotonic token for stale-check; not for external consumption. */
  mutationId: number;
}

/** Display-safe local acknowledgement of a completed production mutation. */
export interface AcceptedConversationMutation {
  readonly kind: "send" | "steer" | "queue" | "interrupt";
  readonly receipt: MutationReceipt;
}

export type LoadOlderResult =
  | { readonly status: "loaded"; readonly itemKeys: readonly string[] }
  | { readonly status: "failed" }
  | { readonly status: "ignored" };

interface DrainScheduler {
  request(key: string, effect: () => Promise<void>): Promise<void>;
}

// A structural activity sink accepted by openProjected/rehydrate. Uses the
// approved LiveActivityState contract from state/activity.ts directly via an
// exact structural Pick so argument order cannot drift from the real store.
// The applyLiveNotification return value drives the shared drain scheduler.
export interface LiveActivitySink {
  setLiveView(view: ActivityView, identity: ActivityIdentity): boolean;
  applyLiveNotification(
    n: AnyNotification,
    identity: ActivityIdentity,
  ): NotificationOutcome;
  reset(): void;
}

// I1: Binding snapshot captured at request time. Every request through the
// drain scheduler captures the current bindingEpoch + ref + generation +
// service + sink object identities. The effect verifies ALL fields are still
// current BEFORE any read, suppressing stale work at the boundary — a request
// queued for conversation A can never call serviceA with refB after the store
// switched to B. This is an internal exact tuple; it is never exported.
/** @internal — binding snapshot for scheduler stale-work suppression. */
interface RequestBinding {
  readonly epoch: number;
  readonly ref: string;
  readonly generation: number;
  readonly service: LiveConversationService;
  // C1: sink is null for plain open() (no projected sink), non-null for
  // openProjected. Both paths validate exact service identity.
  readonly sink: LiveActivitySink | null;
}

// Production-owned drain scheduler with per-key completion promises. Each
// request returns a promise that resolves when that key's actual effect
// completes/skips/errors — even while unrelated rereads remain or hang. Same
// logical key coalesces (overwrites effect, merges waiter lists); distinct keys
// are preserved so heterogeneous outcomes both run. One effect at a time;
// recursively drains all pending. Catches effect errors without unhandled
// rejections and remains usable.
//
// SCHEDULER CONTRACT — effects must NOT await a scheduler request:
// Effects passed to scheduler.request(key, effect) are executed by the drain
// loop. An effect MUST NOT call scheduler.request() and await its result,
// because that would create a circular dependency: the drain loop awaits the
// effect, which awaits a new scheduler request, which cannot start until the
// current drain loop reaches idle — a deadlock. The reread effect
// (requestRehydrate) calls storeGet().rehydrate() directly (not through the
// scheduler). handleMutationError only requests a reread (never awaits one).
// This invariant is confirmed by the production call graph:
//   requestRehydrate effect -> rehydrate() -> readProjection() [no scheduler]
//   handleMutationError -> requestRehydrate() [fire-and-forget, action body]
function createDrainScheduler(): DrainScheduler {
  let scheduled = false;
  let busy = false;
  let inFlight: Promise<void> | null = null;
  // Per-key pending queue. Each entry holds the coalesced effect AND a list of
  // waiters that resolve when this key's effect completes/skips/errors.
  interface PendingEntry {
    effect: () => Promise<void>;
    waiters: Array<() => void>;
  }
  const pending: Map<string, PendingEntry> = new Map();
  // Waiters for the currently in-flight effect (not yet in pending Map).
  let currentWaiters: Array<() => void> = [];

  function runOne(effect: () => Promise<void>): Promise<void> {
    return Promise.resolve()
      .then(effect)
      .catch(() => {
        // Effect errors are the effect's responsibility. Swallow to prevent
        // unhandled rejection; remain usable.
      })
      .then(() => {
        // Resolve this key's waiters — the effect completed/skipped/errored.
        const waiters = currentWaiters;
        currentWaiters = [];
        for (const w of waiters) w();
        if (pending.size > 0) {
          // Drain the next pending effect in insertion order. One at a time.
          const entry = pending.entries().next().value;
          if (entry === undefined) {
            inFlight = null;
            busy = false;
            return undefined;
          }
          const nextKey = entry[0] as string;
          const nextEntry = entry[1] as PendingEntry;
          pending.delete(nextKey);
          currentWaiters = nextEntry.waiters;
          inFlight = runOne(nextEntry.effect);
          return inFlight;
        }
        inFlight = null;
        busy = false;
        return undefined;
      });
  }

  function request(key: string, effect: () => Promise<void>): Promise<void> {
    // Per-key completion promise. Resolves when this key's actual effect
    // completes/skips/errors — even while unrelated rereads remain or hang.
    const waiters: Array<() => void> = [];
    const completion = new Promise<void>((resolve) => {
      waiters.push(resolve);
    });

    if (busy) {
      // An effect is in flight — coalesce same key (merge waiters, overwrite
      // effect); distinct keys are preserved for trailing drain.
      const existing = pending.get(key);
      if (existing !== undefined) {
        existing.effect = effect;
        for (const w of waiters) existing.waiters.push(w);
      } else {
        pending.set(key, { effect, waiters });
      }
      return completion;
    }
    if (scheduled) {
      // A microtask is pending but hasn't started — coalesce same key only.
      const existing = pending.get(key);
      if (existing !== undefined) {
        existing.effect = effect;
        for (const w of waiters) existing.waiters.push(w);
      } else {
        pending.set(key, { effect, waiters });
      }
      return completion;
    }
    // First request in a quiescent drain — defer to a microtask so a
    // synchronous burst coalesces into one effect.
    scheduled = true;
    pending.set(key, { effect, waiters });
    inFlight = Promise.resolve().then(() => {
      scheduled = false;
      busy = true;
      const entry = pending.entries().next().value;
      if (entry === undefined) {
        inFlight = null;
        busy = false;
        return;
      }
      const keyToUse = entry[0] as string;
      const entryValue = entry[1] as PendingEntry;
      pending.delete(keyToUse);
      currentWaiters = entryValue.waiters;
      return runOne(entryValue.effect);
    });
    return completion;
  }

  return {
    request,
  };
}

// F4: No legacy createActivitySink adapter — the activity store's own
// LiveActivityState (Coordinate B) implements LiveActivitySink directly.
// Do not fabricate "applied" — use the real applyLiveNotification outcome.

// --- limits and truncation helpers -------------------------------------------
// MAX_ITEM_BYTES/RETAINED_ITEM_CAP/truncateText are the single copy in
// project.ts (imported above), re-exported here so this module's own test
// file and any other reader can name them from this store too. Only
// exceedsByteLimit (the truncation-freeze check) is this store's own —
// project.ts has no equivalent, since it tracks no per-item ownership state
// to freeze.
const textEncoder = new TextEncoder();

// Check if text exceeds the byte limit (for setting truncated flag in projections).
export function exceedsByteLimit(text: string, maxBytes: number): boolean {
  return textEncoder.encode(text).length > maxBytes;
}

export { MAX_ITEM_BYTES, RETAINED_ITEM_CAP, TRUNCATION_MARKER, truncateText };

// Check if an activity detail's text-bearing fields exceed the byte limit —
// description joins arguments/output/error, the four fields
// truncateActivityDetail bounds. The same rule applies to a top-level
// activity detail and to each of a cluster's member details.
function exceedsActivityDetailLimit(detail: ActivityDetail): boolean {
  return (
    (detail.description !== undefined &&
      exceedsByteLimit(detail.description, MAX_ITEM_BYTES)) ||
    (detail.arguments !== undefined &&
      exceedsByteLimit(detail.arguments, MAX_ITEM_BYTES)) ||
    (detail.output !== undefined &&
      exceedsByteLimit(detail.output, MAX_ITEM_BYTES)) ||
    (detail.error !== undefined &&
      exceedsByteLimit(detail.error, MAX_ITEM_BYTES))
  );
}

// A question's own prose, in the fields boundQuestion cuts: header, question,
// why, ifUnanswered, and every option's label and detail.
function exceedsQuestionLimit(question: AskQuestionRef): boolean {
  return (
    exceedsByteLimit(question.header, MAX_ITEM_BYTES) ||
    exceedsByteLimit(question.question, MAX_ITEM_BYTES) ||
    (question.why !== undefined &&
      exceedsByteLimit(question.why, MAX_ITEM_BYTES)) ||
    (question.ifUnanswered !== undefined &&
      exceedsByteLimit(question.ifUnanswered, MAX_ITEM_BYTES)) ||
    question.options.some(
      (option) =>
        exceedsByteLimit(option.label, MAX_ITEM_BYTES) ||
        exceedsByteLimit(option.detail, MAX_ITEM_BYTES),
    )
  );
}

// Whether ANY field truncateItem bounds on this row exceeds the limit in its
// original content — the exact rule for which rows the store records as
// truncated. #1737 moved the bounds over every row kind (a pasted user
// message, a daemon notice, a failure's title and detail, question prose,
// and an activity's description and label joined assistant markdown and the
// activity arguments/output/error), but the ownership checks below kept
// reading only those last two, so the other kinds arrived cut with no id in
// the set and no affordance. An attachments row's display name is bounded
// too, but that bound predates #1737 and its ownership stays as it was.
function rowExceedsDisplayBound(item: MobileTimelineItem): boolean {
  switch (item.kind) {
    case "assistant":
      return exceedsByteLimit(item.markdown, MAX_ITEM_BYTES);
    case "activity":
      return (
        exceedsByteLimit(item.label, MAX_ITEM_BYTES) ||
        exceedsActivityDetailLimit(item.detail)
      );
    case "user":
    case "notice":
      return exceedsByteLimit(item.text, MAX_ITEM_BYTES);
    case "failure":
      return (
        exceedsByteLimit(item.title, MAX_ITEM_BYTES) ||
        exceedsByteLimit(item.detail, MAX_ITEM_BYTES)
      );
    case "question":
      return item.questions.some(exceedsQuestionLimit);
    default:
      return false;
  }
}

// A clustered member is bounded on its label and its detail's fields exactly
// like the top-level row (truncateItem's member map), so ownership reads the
// same pair.
function exceedsActivityMemberBound(member: ActivityMember): boolean {
  return (
    exceedsByteLimit(member.label, MAX_ITEM_BYTES) ||
    exceedsActivityDetailLimit(member.detail)
  );
}

// F12: Per-item truncation ownership. Instead of checking if the text ends
// with the marker (which would freeze if genuine content ends with "…
// truncated"), the store tracks which item IDs have been truncated in a
// private set. This allows genuine marker suffixes in content without
// freezing delta appends.

// Apply truncation to an item's text-bearing fields. Delegates to project.ts's
// shared truncateItem so every row kind it bounds (user, assistant, notice,
// failure, question, activity) is bounded here too — this store used to
// truncate only "assistant" and "activity", so a pasted user message, a
// daemon notice, a tool failure's stack, and a question's own text were never
// bounded by the live path at all. The bound callback is a plain
// truncateText call, not the caching one project.ts's own callers use — this
// store already tracks per-item truncation ownership itself (the comment
// above), so a second cache here would just be dead weight.
export function truncateItem(item: MobileTimelineItem): MobileTimelineItem {
  return sharedTruncateItem(item, (text) => truncateText(text, MAX_ITEM_BYTES));
}

// Enforce the 500-item retained cap. Delegates to project.ts's shared
// capItems, which also drops a leading attachment whose source item did not
// survive the cut (this store's own cap used to just slice, leaving orphaned
// attachments behind).
function capItems(items: MobileTimelineItem[]): MobileTimelineItem[] {
  return sharedCapItems(items);
}

export interface ConversationState {
  readonly ref: string | null;
  readonly profileId: string | null;
  readonly connectionGeneration: number;
  readonly conversationGeneration: number;

  readonly conversation: MobileConversation | null;
  readonly olderCursor: string | null;
  readonly hasEarlierItems: boolean;
  readonly hasLaterItems: boolean;
  readonly loadingOlder: boolean;
  readonly status: ConversationStatus;
  readonly error: string | null;

  readonly draft: string;
  readonly pendingSend: string | null;
  readonly pendingMutation?: ConversationMutationState | null;
  readonly lastAcceptedMutation?: AcceptedConversationMutation | null;

  // The non-projected compatibility surface, kept for screen test mocks; no
  // production screen calls it (they open through openProjected /
  // resumeProjected). It binds no activity sink, so it does not self-recover:
  // a refused mutation surfaces its error and issues no reread.
  open(service: ConversationService, ref: string): Promise<void>;
  loadOlder(service: ConversationService): Promise<LoadOlderResult>;
  setDraft(text: string): void;
  send(service: ConversationService, input: InputItem[]): Promise<void>;
  steer(
    service: ConversationService,
    input: InputItem[],
    expectedQueueRevision?: number,
  ): Promise<void>;
  queue(service: ConversationService, input: InputItem[]): Promise<void>;
  interrupt(service: ConversationService): Promise<void>;
  close(): void;
  applyNotification(n: AnyNotification): void;
  reset(): void;
}

// Required live state interface (F3): the production store always implements
// these live-only methods. They are NOT optional-fallback to old open().
// Base ConversationState is preserved for screen test mocks that only need
// the basic open/send/steer/queue/interrupt/close surface.
// F2: setCoalescer is removed from the public interface — openProjected
// creates and binds the coalescer internally.
export interface LiveConversationState extends ConversationState {
  openProjected(
    service: LiveConversationService,
    activitySink: LiveActivitySink,
    ref: string,
    replacement?: ConversationReadProjection,
  ): Promise<void>;
  suspendProjected(): void;
  resumeProjected(
    service: LiveConversationService,
    activitySink: LiveActivitySink,
    ref: string,
  ): Promise<void>;
  rehydrate(
    service: LiveConversationService,
    activitySink: LiveActivitySink,
  ): Promise<void>;
  // Plan3 host projection: returns a fresh immutable snapshot of the private
  // truncatedItemIds set. The caller (live-concept projector) uses this to
  // mark items truncated:true/false without suffix inference. The returned
  // Set is a copy — mutating it cannot affect the store's internal ownership.
  getTruncatedItemIds(): ReadonlySet<string>;
  // Task3: store-owned external error publication seam. The dispatcher passes
  // a generic sanitized message plus the exact ref and conversationGeneration
  // it captured when deciding to publish. The store writes the error ONLY when
  // the current ref and conversationGeneration exactly match the expected
  // values; any mismatch (profile/thread switch, reset/open transition) makes
  // zero set calls, leaves the full state object reference unchanged, and
  // fires zero subscriber notifications. The write goes through the store's
  // wrapped set so errorOwnerRev advances, which lets a subsequent rehydrate
  // preserve the newer external error instead of clearing it. No
  // service/display/raw IDs are accepted beyond the ref already privately held
  // by the store; the dispatcher is responsible for passing only generic
  // sanitized messages.
  publishExternalError(
    message: string,
    expectedRef: string | null,
    expectedGeneration: number,
  ): void;
}

// Check if an error is a WireError carrying the actionUnavailable
// evenerErrorInfo. F11: uses actual WireError identity (instanceof), not a
// structural property check, to match the canonical error discrimination
// pattern used by isHubLaunchError.
function isActionUnavailableError(err: unknown): boolean {
  return (
    err instanceof WireError && err.evenerErrorInfo === "actionUnavailable"
  );
}

// A mutation's precondition, re-evaluated at the mutation boundary rather than
// trusted from the render that offered the control: the session's controls
// (the SDK's sessionControls over the wire's status, the harness's
// capabilities and the queue depth). A status that flipped between the render
// and the submit refuses the mutation here, with the control's own reason.
// Runs in the store so the fake service (which gates on nothing) still
// respects what the hub would refuse.
function requireControl(
  conv: MobileConversation,
  control: "stop" | "steer" | "drain" | "queue" | "send",
  action: string,
): void {
  const controls = sessionControls(
    conv.status.type,
    conv.capabilities,
    conv.queue?.depth ?? 0,
  );
  if (!controls[control]) {
    throw new Error(
      controls.reason[control] ??
        `Action "${action}" is not available for this thread`,
    );
  }
}

// The incremental path has no Turn object, so a sparse item's containing
// turn status is derived from the store's active turn: that is the only turn
// that can still be inProgress, and an item naming a different turn is not in
// it. Items that carry their own status never consult this.
function containingTurnStatus(
  conv: MobileConversation,
  item: ThreadItem,
): string | undefined {
  if (conv.activeTurnId === undefined) return undefined;
  if (item.turnId !== undefined && item.turnId !== conv.activeTurnId) {
    return undefined;
  }
  return "inProgress";
}

// The reducer's own item/started and item/completed folds (applyNotification,
// via mergeItemImages) already resolved this item's images against whatever
// the model held before it — a raw wire item carrying no images field means
// "unchanged", never "removed" (the same rule imagesToItemImagesForSession
// documents). Finds that folded ItemModel in conv (already updated by
// applyThreadNotification before this call) using the package's own
// identity rule (itemIdentityMatches: transcriptKey when both sides carry
// one, else id) rather than a local copy of it.
function findFoldedItem(
  conv: MobileConversation,
  item: ThreadItem,
): ItemModel | undefined {
  for (const turn of conv.turns) {
    const found = turn.items.find((candidate) => itemIdentityMatches(candidate, item));
    if (found) return found;
  }
  return undefined;
}

// Project a wire ThreadItem into a mobile timeline item for insertion from
// item/started and item/completed notifications. This reuses the same field
// mapping as the full projection but handles a single item in isolation.
// F6: ask_user items (commandExecution with toolName "ask_user") are NOT
// projected as generic activity — they return null to signal a reread, since
// the canonical projector needs the full turn context (pendingAsks set) to
// project them as question items. F7: precise subtype checks — wrong subtype
// or missing context returns null to schedule a reread.
// Task 2A-Ops-5: when askPending is true, a userMessage item also returns
// null to trigger an authoritative reread — the canonical projector must
// settle the pending question state (remove question rows, clear askPending)
// based on the full turn context after a user-message answer lifecycle.
function projectSingleItem(
  item: ThreadItem,
  askPending: boolean,
  turnStatus: string | undefined,
): MobileTimelineItem | null {
  if (item.type === "userMessage") {
    // Task 2A-Ops-5: if there's a pending ask_user, a user message is the
    // answer lifecycle — trigger an authoritative reread to settle the
    // pending question state according to canonical projection.
    if (askPending) return null;
    return { kind: "user", id: item.id, text: item.text ?? "", ...(item.transcriptEntryIndex !== undefined ? { transcriptEntryIndex: item.transcriptEntryIndex } : {}) };
  }
  if (item.type === "agentMessage") {
    return {
      kind: "assistant",
      id: item.id,
      markdown: `${item.text ?? ""}${item.delta ?? ""}`,
      streaming: isActiveItem(item, turnStatus),
    };
  }
  // F6: ask_user is a commandExecution with toolName "ask_user". The canonical
  // projector handles ask_user with full turn context (pendingAsks, question
  // parsing). We cannot replicate that from a single item notification, so
  // return null to schedule an authoritative reread — never project as generic
  // activity.
  if (item.type === "commandExecution") {
    if (item.toolName === "ask_user") {
      return null; // F6: schedule reread for ask_user
    }
    return {
      kind: "activity",
      id: item.id,
      label: item.toolName ?? item.description?.trim() ?? "Tool",
      family: "tool",
      state: activityState(item, turnStatus),
      detail: {
        arguments: item.argumentsJson,
        description: item.description,
        output: item.output,
        error: item.error,
        exitCode: item.exitCode,
        durationMs: item.durationMs,
        callId: item.callId,
      },
    };
  }
  if (item.type === "reasoning") {
    return {
      kind: "activity",
      id: item.id,
      label: "Reasoning",
      family: "reasoning",
      state: isActiveItem(item, turnStatus) ? "running" : "completed",
      detail: { output: item.text },
    };
  }
  // Unknown item types — return null to signal an unsupported transition
  // that should trigger a coalesced rehydrate.
  return null;
}

export function createConversationStore() {
  let conversationGen = 0;
  let mutationIdCounter = 0;
  // Draft revision: a monotonically increasing counter incremented on every
  // setDraft call. When a mutation clears the draft on submit, it captures the
  // current revision. On failure, the snapshot is restored ONLY if the revision
  // has not changed since the clear — type-then-delete (which produces "" but
  // increments the revision) counts as an edit and prevents restore.
  let draftRevision = 0;
  // loadOlder operation token — incremented on each loadOlder call so a stale
  // loadOlder (from an older operation in the same generation) cannot overwrite
  // a newer loadOlder's loadingOlder, error, or items.
  let loadOlderToken = 0;
  // R1: Monotonic mutation-owner revision — increments on every
  // pendingMutation transition (set, clear, even ABA same value). Rehydrate
  // captures this at entry; if it changed during the await, the rehydrate
  // is stale with respect to the mutation owner and must not publish its
  // projection. Instead, one bounded trailing reread is scheduled.
  let mutationOwnerRev = 0;
  // R1: Monotonic error-owner revision — increments on every error
  // transition (set, clear, even ABA same value). Rehydrate captures this at
  // entry; if it changed during the await, the rehydrate is stale with
  // respect to the error owner and must not clear or overwrite error.
  let errorOwnerRev = 0;
  // The reread's snapshot is authoritative over every thread-level write that
  // preceded its response. AppWire orders a thread/read response at the
  // snapshot cut and delivers frames in producer order on the one socket
  // (docs/superpowers/plans/2026-07-28-appwire-retry-safe-mutations-and-atomic-
  // rejoin.md:19, 372-373), so a frame this store applied while the read was
  // in flight is already folded into the snapshot that arrives, and a frame
  // that arrives after the response lands on top of it through the normal
  // path. Nothing awaits between the response and the commit (the service's
  // readProjection and rehydrate below each await the read alone), so there
  // is no window to buffer for; the web's applyHydrationResponseCut drops its
  // buffer at the same point for the same reason. Every item frame folds into
  // this model too (dual-write, see applyNotification); liveOwnedRevs below
  // survives only for the display rows until c-2b projects them from here.
  //
  // The package reducer over the conversation. Every reducer case spreads the
  // model it was given, so the display rows (and anything else native keeps
  // on the conversation) survive untouched.
  function applyThreadNotification(
    conversation: MobileConversation,
    n: AnyNotification,
  ): MobileConversation {
    return applyNotification(conversation, n, Date.now());
  }
  // I3: Page-owned item IDs — tracks which item IDs were loaded by loadOlder
  // (page-owned history). On rehydrate page-race merge, only these items are
  // prepended as older history; current-only non-page items (live notifications
  // that arrived during the await) are appended as the live tail, never moved
  // to the oldest position where they'd be discarded by the 500-cap. Cleared
  // on every conversation transition (open/close/reset/openProjected).
  const pageOwnedIds = new Set<string>();
  // Track page-owned turn IDs separately from pageOwnedIds above. Turns are
  // never EVICTED the way display items are (a page's items can be entirely
  // deduped away or trimmed by the item cap while its turns — the only source
  // of a usage total when there is no thread-level cumulative usage — still
  // belong in conversation.turns), so whether to preserve older turns on a
  // rehydrate must not depend on whether any of that page's ROWS survived.
  //
  // #1919 follow-up (retained-turn bound): turns are no longer exempt from
  // retention bounds — the old "never capped" exemption retained every
  // page turn's FULL item payloads for the conversation's lifetime, so
  // memory and per-refresh merge/sum cost grew with the whole loaded
  // transcript. A retained turn now keeps its full payloads only inside the
  // keep-window: while any of its items intersects the retained display
  // rows (the 500-row cap's final set — the same boundary pruneEvictedIds
  // settles item ownership against). Outside that window, boundRetainedTurns
  // trims the turn to compact identity + usage: every loaded turn's id and
  // usage must survive for sessionTokens' turn-summed fallback to keep
  // covering what was actually loaded, so ONLY the display-fallback
  // payloads (text, output, images) are dropped. pageOwnedTurnIds is pruned
  // with the same bound by that pass — it holds only the page turns still
  // inside the window. A page turn whose payloads were trimmed moves to
  // pageOwnedCompactTurnIds below: the compact identity+usage survivors
  // still gate rehydrate preservation, because their usage is accounting
  // data, not display data. Both sets clear together on every conversation
  // transition, same as pageOwnedIds.
  const pageOwnedTurnIds = new Set<string>();
  const pageOwnedCompactTurnIds = new Set<string>();
  // #1919 follow-up, review rounds 1-2 (fragment identity): the package's
  // merges match fragments by ITEM identity when turn ids differ
  // (turnsMatch/itemIdentityMatches — transcriptKey when both sides carry
  // one, else id) and coalesce matching groups transitively, so a trimmed
  // turn must not lose the identities of the items it shed. A compact
  // survivor that can no longer match would sit beside a later turn that
  // re-issued its content under another id, and both would count their
  // usage. Each trimmed turn therefore remembers identity-only skeletons of
  // its shed items here, and every page/rehydrate merge injects those
  // skeletons back into the package's own merge whenever an incoming item
  // collides with one — the package then folds exactly as the unbounded
  // main would have, and the skeletons are stripped from the stored result
  // so no payload returns. Entries union across re-trims (a partial
  // restoration never forgets the rest) and go dormant while the turn again
  // carries real items.
  const compactedTurnItems = new Map<string, ItemModel[]>();
  // Residual 2 / Fix round 1: Per-item live ownership with monotonic revision.
  // liveOwnedRevs maps item ID → the liveOwnerRev value at the time of the
  // last accepted live notification for that item. liveOwnerRev is a global
  // monotonically increasing counter incremented on every accepted lifecycle
  // insertion, replacement, delta, reset, and warning. Rejected/missing/wrong/
  // frozen notifications do NOT increment or mark.
  // Rehydrate captures entryLiveRev at entry. At commit:
  // - If the reread contains an ID whose liveOwnedRevs revision > entryLiveRev,
  //   the current (live-updated) version is newer than the reread's snapshot;
  //   preserve the current version in the authoritative position and keep
  //   ownership (do NOT delete from liveOwnedRevs).
  // - If the reread contains an ID whose revision ≤ entryLiveRev (or not
  //   live-owned), accept the reread's authoritative version and clear that
  //   ID's ownership.
  // - If the reread omits an ID that is genuinely live-owned, append the
  //   current item as live tail.
  // - Page IDs still prepend; unowned old history drops.
  const liveOwnedRevs = new Map<string, number>();
  let liveOwnerRev = 0;
  // Mark an item as live-owned with the current global revision. Called from
  // every accepted lifecycle insertion/replacement, delta, reset, warning.
  function markLiveOwned(id: string): void {
    liveOwnerRev += 1;
    liveOwnedRevs.set(id, liveOwnerRev);
  }
  // C1+I1: Deferred trailing-reread request. When a rehydrate detects the
  // mutation owner changed during its await, it stores a deferred trailing
  // request with the EXACT binding snapshot captured at schedule time (not
  // recaptured inside the effect). The request is drained exactly once after
  // the mutation settles (pendingMutation cleared or set to "failed"). If the
  // binding changed (switch to B), the stored binding is stale and the request
  // is dropped. Additional mutation revisions create at most one later need.
  interface TrailingReread {
    binding: RequestBinding;
    mutationRev: number;
  }
  let trailingReread: TrailingReread | null = null;

  // C1+I1: Store a deferred trailing reread with the exact binding captured
  // at schedule time. Does NOT schedule through the scheduler yet — the
  // request is drained when the mutation settles. If a request is already
  // pending, it is overwritten (at most one later need per mutation revision).
  function scheduleTrailingReread(): void {
    const binding = captureBinding();
    if (binding === null) return;
    trailingReread = { binding, mutationRev: mutationOwnerRev };
    // The mutation may have already settled (e.g., the send completed before
    // the rehydrate was released). Try to drain immediately — if the mutation
    // is still pending, drainTrailingReread will defer.
    drainTrailingReread();
  }

  // C1+I1: Drain the deferred trailing reread after a mutation settles. The
  // mutation must be terminal (pendingMutation is null or "failed"). The
  // stored binding is validated — if stale (switch to B), the request is
  // dropped. Exactly one reread is scheduled through the store-owned scheduler.
  // The effect validates the exact captured binding (NOT recapturing current),
  // AND rechecks the captured/current monotonic mutation revision and true
  // terminal status INSIDE the scheduler effect immediately before any read.
  // If a newer mutation is pending after enqueue, do zero read; atomically
  // restore one binding-owned deferred request for that mutation revision and
  // let its settle hook drain exactly once. Keep separate queued/deferred
  // identity so no duplicate effects/third reread.
  function drainTrailingReread(): void {
    if (trailingReread === null) return;
    // Only drain if the mutation has settled (no pending mutation, or a
    // failed terminal mutation). A still-pending mutation keeps the request
    // deferred.
    const currentMutation = storeGet?.().pendingMutation;
    if (
      currentMutation !== null &&
      currentMutation !== undefined &&
      currentMutation.status === "pending"
    ) {
      return;
    }
    const { binding } = trailingReread;
    // Capture the mutation revision at enqueue time — the effect will compare
    // this against the current revision to detect a newer pending mutation
    // that started after enqueue.
    const enqueueMutationRev = mutationOwnerRev;
    trailingReread = null;
    // C1: Validate the EXACT captured binding — if stale (switch to B),
    // drop the request. Never recapture the current binding here.
    if (!isBindingCurrent(binding)) return;
    // Schedule one trailing reread through the store-owned scheduler. The
    // effect validates the exact captured binding again (double-check after
    // the scheduler microtask), rechecks mutation revision and terminal status,
    // and calls rehydrate with the captured service and sink — never the
    // closure's. If a newer mutation is pending, the effect re-defers instead
    // of reading.
    scheduler.request(binding.ref, async () => {
      // C1: Re-validate the exact captured binding after the microtask.
      if (!isBindingCurrent(binding)) return;
      // Residual 1: Recheck mutation revision and true terminal status INSIDE
      // the effect immediately before any read. If the mutation revision
      // advanced since enqueue AND a mutation is currently pending, a newer
      // mutation started after enqueue — do zero read. Atomically restore one
      // binding-owned deferred request for the newer mutation revision and let
      // its settle hook drain exactly once.
      if (mutationOwnerRev !== enqueueMutationRev) {
        const mut = storeGet?.().pendingMutation;
        if (mut !== null && mut !== undefined && mut.status === "pending") {
          // Newer mutation is pending — re-defer for this revision. The
          // binding stays the same (already validated). The settle hook
          // (called when M2 settles) will drainTrailingReread exactly once.
          trailingReread = { binding, mutationRev: mutationOwnerRev };
          return; // zero read
        }
      }
      // captureBinding only returns non-null when boundSink is non-null, so
      // binding.sink is always non-null here.
      if (binding.sink === null) return;
      await storeGet?.().rehydrate(binding.service, binding.sink);
    });
  }

  // The store owns ONE drain scheduler for its entire lifetime. Lifecycle,
  // activity rehydrate, structural notification gaps, and mutation capability
  // recovery all request through it rather than owning timers/coalescers.
  const scheduler = createDrainScheduler();
  let activitySink: LiveActivitySink | null = null;
  // The service+sink bound by the last openProjected/rehydrate, used by
  // requestRehydrate so applyNotification can request through the scheduler
  // without holding its own service reference.
  let boundService: LiveConversationService | null = null;
  let boundSink: LiveActivitySink | null = null;
  let suspendedService: LiveConversationService | null = null;
  let acceptedRehydrate: { generation: number; sink: LiveActivitySink } | null =
    null;
  let liveNoticeSerial = 0;
  // I1: Binding epoch — incremented on every openProjected/open/close/reset so
  // a request queued for an older binding (serviceA+refA) can never run after
  // the store switched to a newer binding (serviceB+refB). Every request
  // captures epoch+ref+generation+service+sink; the effect verifies all still
  // current before any read.
  let bindingEpoch = 0;
  // Late-bound store getter — assigned inside create() so requestRehydrate
  // (called from applyNotification) can access get().rehydrate.
  let storeGet: (() => LiveConversationState) | null = null;

  // I1: Capture the current binding snapshot at request time. The effect
  // verifies ALL fields (epoch+ref+generation+service+sink object identities)
  // are still current BEFORE any read — stale work is suppressed at the
  // boundary. Object identity comparison means a rebind with different service
  // or sink objects (even if same ref) produces a different binding and the
  // old effect is suppressed.
  function captureBinding(): RequestBinding | null {
    if (boundService === null || boundSink === null) return null;
    const state = storeGet?.();
    if (state === undefined || state.ref === null || state.status !== "open")
      return null;
    return {
      epoch: bindingEpoch,
      ref: state.ref,
      generation: state.conversationGeneration,
      service: boundService,
      sink: boundSink,
    };
  }

  // C1: Capture a service-specific operation binding for loadOlder and
  // mutations. The supplied service MUST equal boundService (wrong service
  // after B bound => zero request/state). sink is boundSink (null for plain
  // open, non-null for openProjected). epoch/ref/gen are captured from
  // current state. Returns null if no service is bound or ref is null.
  function captureOperationBinding(
    service: ConversationService,
  ): RequestBinding | null {
    if (boundService === null) return null;
    // C1: The supplied service must be the exact bound service object.
    // A wrong service (serviceA called after B is bound) is rejected at
    // the boundary before any request.
    if ((service as LiveConversationService) !== boundService) return null;
    const state = storeGet?.();
    if (state === undefined || state.ref === null || state.status !== "open")
      return null;
    return {
      epoch: bindingEpoch,
      ref: state.ref,
      generation: state.conversationGeneration,
      service: boundService,
      sink: boundSink,
    };
  }

  // Verify a captured binding is still current. This applies to scheduled
  // rereads, paging, and mutations. If the epoch, ref,
  // generation, or service/sink object identity changed, suppress the stale
  // request before it can publish into the new binding.
  function isBindingCurrent(binding: RequestBinding): boolean {
    const state = storeGet?.();
    if (state === undefined) return false;
    return (
      binding.epoch === bindingEpoch &&
      binding.ref === state.ref &&
      binding.generation === state.conversationGeneration &&
      boundService === binding.service &&
      boundSink === binding.sink
    );
  }

  // Request one authoritative reread through the store-owned drain scheduler.
  // I1: captures the binding at request time; the effect verifies it is still
  // current before calling rehydrate with the expected service+sink.
  function requestRehydrate(ref: string): void {
    const binding = captureBinding();
    const service = boundService;
    const sink = boundSink;
    if (binding === null || service === null || sink === null) return;
    scheduler.request(ref, async () => {
      // I1: Suppress stale work BEFORE any read — if the binding changed,
      // do not call rehydrate (serviceA must never be called with refB).
      if (!isBindingCurrent(binding)) return;
      await storeGet?.().rehydrate(service, sink);
    });
  }

  // Returns the current draft revision — passed to handleMutationError so it
  // can detect post-clear edits (type-then-delete) without exposing the
  // revision in public state.
  function getDraftRevision(): number {
    return draftRevision;
  }
  // I1: Returns the current error-owner revision — passed to
  // handleMutationError so it can compare against the captured
  // entryErrorRev without exposing the revision in public state.
  function getErrorOwnerRev(): number {
    return errorOwnerRev;
  }
  // F9: Rehydrate operation token — incremented on each rehydrate call so
  // a stale rehydrate (from an older operation) cannot overwrite a newer
  // rehydrate's state within the same generation.
  let rehydrateToken = 0;
  // F12: Per-item truncation ownership — tracks which item IDs have been
  // truncated to their byte limit. Once an item is truncated, later deltas
  // cannot append (the marker appears exactly once). This is tracked by
  // item ID, not by checking the text suffix, so genuine content that
  // happens to end with "… truncated" does not freeze delta appends.
  const truncatedItemIds = new Set<string>();

  // Truncate items and record which item IDs were truncated (F12).
  // Called from open/openProjected/rehydrate to seed the truncation set.
  // Task 2A-Truncation: truncateAndRecord is PURE truncation — it no longer
  // mutates truncatedItemIds. Authoritative paths call reconcileTruncationFrom
  // (on the pre-truncation capped items) to rebuild the truncation set exactly:
  // oversized originals are frozen, short originals unfreeze, omitted/capped
  // IDs are removed, and already-frozen superseded live versions (truncated by
  // a prior live delta) stay frozen. Family is required carried data on
  // item.family (set by the projector from the wire type); it is never
  // label-derived and not recorded here.
  function truncateAndRecord(
    items: MobileTimelineItem[],
  ): MobileTimelineItem[] {
    // Task 2A-Family: family is read directly from item.family (required,
    // set by the projector from the wire type). No label inference, no
    // side-channel family map.
    return items.map((item) => truncateItem(item));
  }

  // Task 2A-Truncation: Exact reconciliation of truncation ownership from the
  // FINAL retained/merged items (pre-truncation content). Replaces add-only
  // frozen tracking on every authoritative install path
  // (open/openProjected/rehydrate/loadOlder). Rebuilds truncatedItemIds and
  // (no family map to rebuild — family is read from item.family directly):
  //   - An item whose original content exceeds the byte limit → frozen.
  //   - An item whose original content is short → unfrozen, even if it was
  //     frozen before (authoritative short version unfreezes).
  //   - An ID omitted/capped from the final set → removed (no stale freeze).
  //   - An already-frozen item that remains in the final set (a current item
  //     whose text was already truncated by a prior path — its text is ≤
  //     MAX_ITEM_BYTES so exceedsByteLimit is false) stays frozen via
  //     priorFrozenIds. This is critical for loadOlder: current items are
  //     already truncated; without priorFrozenIds the reconciliation would
  //     unfreeze them. Intersected with the final IDs so capped/removed
  //     ownership drops.
  //   - A superseded item (a live version that replaced the reread's version
  //     during the rehydrate await — already truncated by a prior live delta,
  //     so its text is ≤ MAX_ITEM_BYTES and exceedsByteLimit is false) stays
  //     frozen via supersededFrozenIds. Only superseded IDs that are STILL in
  //     truncatedItemIds at call time are passed — a short lifecycle/delta/reset
  //     that removed the freeze stays unfrozen.
  // `items` are the FINAL retained items BEFORE truncateItem runs (so
  // exceedsByteLimit sees the original oversized content). `priorFrozenIds`
  // is the set of IDs frozen before this call (captured by the caller before
  // any live update); only IDs still in the final set are preserved.
  // `supersededFrozenIds` is the set of superseded IDs that are still frozen
  // (intersected with truncatedItemIds by the caller); only IDs in the final
  // set are preserved.
  function reconcileTruncationFrom(
    items: MobileTimelineItem[],
    priorFrozenIds: Set<string> = new Set(),
    supersededFrozenIds: Set<string> = new Set(),
  ): void {
    const retainedIds = new Set(items.flatMap((item) => [...timelineIdentities(item)]));
    // An identity that was already frozen before this call (or by a
    // superseded live version) keeps its freeze while it remains in the final
    // set — its content is already truncated, so the byte check alone would
    // unfreeze it. Members go through the same check as top-level rows:
    // otherwise an already-truncated member lost its freeze on the next
    // loadOlder/rehydrate and admitted deltas against truncated content.
    const staysFrozen = (identity: string): boolean =>
      (priorFrozenIds.has(identity) || supersededFrozenIds.has(identity)) &&
      retainedIds.has(identity);
    truncatedItemIds.clear();
    for (const item of items) {
      if (rowExceedsDisplayBound(item) || staysFrozen(timelineIdentity(item))) {
        truncatedItemIds.add(timelineIdentity(item));
      }
      // A clustered member's own oversized label or detail freezes under the
      // member's own identity, independent of the top-level freeze above —
      // native expands members directly, so each is bounded and guarded on
      // its own.
      if (item.kind === "activity" && item.members) {
        for (const member of item.members) {
          const identity = activityIdentity(member);
          if (exceedsActivityMemberBound(member) || staysFrozen(identity)) {
            truncatedItemIds.add(identity);
          }
        }
      }
    }
  }

  // Task 2A-Items: truncate a single item and record its truncation state.
  // Returns a non-undefined MobileTimelineItem (the input is known non-null).
  // Used by item/started and item/completed for authoritative replacement —
  // the caller removes any stale freeze entry first so the new content can
  // accept future deltas; this re-freezes if the replacement is oversized.
  // Family is required carried data on item.family (set by the projector from
  // the wire type); it is never label-derived and not recorded here.
  function truncateAndRecordSingle(
    item: MobileTimelineItem,
  ): MobileTimelineItem {
    if (rowExceedsDisplayBound(item)) {
      truncatedItemIds.add(timelineIdentity(item));
    }
    return truncateItem(item);
  }

  // Task 2A-Truncation residual fix round 2: Prune ownership maps for evicted
  // IDs after an incremental append+cap path (item/started, item/completed,
  // warning). When capItems trims the oldest items, any frozen/page/live
  // entries for those evicted IDs are stale and must be removed so a later
  // re-introduction (page load or lifecycle) independently judges the new
  // content instead of inheriting a stale freeze.
  function pruneEvictedIds(items: MobileTimelineItem[]): void {
    // Member-inclusive: truncatedItemIds can now hold a clustered member's
    // own identity (transcriptKey ?? id), not just a top-level one — a
    // top-level-only retained set would prune a still-present member's
    // freeze right after reconcileTruncationFrom sets it. pageOwnedIds and
    // liveOwnedRevs only ever hold identities from this same union, so the
    // richer set is a safe superset for them too.
    const retainedIds = new Set(items.flatMap((item) => [...timelineIdentities(item)]));
    for (const id of [...truncatedItemIds]) {
      if (!retainedIds.has(id)) truncatedItemIds.delete(id);
    }
    for (const id of [...pageOwnedIds]) {
      if (!retainedIds.has(id)) pageOwnedIds.delete(id);
    }
    for (const id of [...liveOwnedRevs.keys()]) {
      if (!retainedIds.has(id)) liveOwnedRevs.delete(id);
    }
  }

  // #1919 follow-up: bound retained page-turn data. The keep-window is the
  // retained display set itself — the final capped rows at the publish site
  // (loadOlder's pageMerged, rehydrate's rehydrateCapped). A turn whose items
  // intersect it keeps full payloads: those are exactly the turns a fresh
  // reread's window can fragment-merge against, so trimming them would
  // change mergeTurnHistory's fresh-wins/older-supplies behavior. A turn
  // outside the window can no longer display anything or supply anything the
  // window needs, so only its identity + usage metadata survive. The pass
  // also settles pageOwnedTurnIds with the same bound: a page turn leaving
  // the window moves to pageOwnedCompactTurnIds, which preserveTurnHistory
  // reads together with pageOwnedTurnIds so the compact survivors still cross
  // rehydrates (accounting completeness).
  function boundRetainedTurns(
    turns: TurnModel[],
    retainedItems: MobileTimelineItem[],
  ): TurnModel[] {
    const retainedIdentities = new Set(
      retainedItems.flatMap((item) => [...timelineIdentities(item)]),
    );
    let trimmed = false;
    const bounded = turns.map((turn) => {
      if (turn.items.length === 0) return turn;
      // Item identity is the package's own rule (itemIdentityMatches:
      // transcriptKey when both sides carry one, else id) approximated from
      // above: a retained row keeps the turn that could supply it alive
      // whether it matches by transcript key or by bare id — the same rule
      // mergeTurnHistory matches fragments by, clustered members included.
      // Testing the bare id too only ever over-keeps (a row that shares an
      // id but conflicts on transcript key is not in the retained set under
      // its id), and over-keeping is the safe direction for merge parity.
      const inWindow = turn.items.some(
        (item) =>
          retainedIdentities.has(item.transcriptKey ?? item.id) ||
          retainedIdentities.has(item.id),
      );
      if (inWindow) return turn;
      trimmed = true;
      if (pageOwnedTurnIds.delete(turn.id)) {
        pageOwnedCompactTurnIds.add(turn.id);
      }
      // Remember identity-only skeletons of the shed items (unioned with
      // whatever the turn shed earlier — a partial restoration must not
      // forget the rest) so a later re-issue under a different turn id can
      // still fold through the package's own merge.
      const shed = compactItemSkeletons(turn.items);
      const remembered = compactedTurnItems.get(turn.id);
      if (remembered === undefined) {
        compactedTurnItems.set(turn.id, shed);
      } else {
        // Dedupe by composite identity: a restore-and-trim cycle of the
        // same items must not grow the remembered set (review round 3).
        const byKey = new Map(remembered.map((skeleton) => [skeletonKey(skeleton), skeleton]));
        for (const skeleton of shed) byKey.set(skeletonKey(skeleton), skeleton);
        compactedTurnItems.set(turn.id, [...byKey.values()]);
      }
      return { ...turn, items: [] };
    });
    return trimmed ? bounded : turns;
  }

  // The identity-only shape of a shed item: identity, ordering and
  // fold-classification fields only. Every output/image field stays shed —
  // this is the payload bound, not a payload cache. text carries the wire's
  // own settled-empty representation ("", exactly what wireItemToModel gives
  // a wire item whose text field was omitted) AND the reducer's omitted-text
  // marker, so a skeleton is exactly as text-less as a sparse wire fragment:
  // a skeleton selected as mergePageItem's textSource contributes the same
  // empty settle the sparse wire reissue itself would have hydrated to —
  // never an undefined that leaks into streaming prefixes or reasoningText's
  // item.text.length — and a later page that brings the item's real text
  // still wins it, instead of the empty settle reading as authoritative
  // (review rounds 4-5).
  function compactItemSkeletons(items: ItemModel[]): ItemModel[] {
    return items.map(
      (item) =>
        markItemTextOmitted({
          id: item.id,
          turnId: item.turnId,
          type: item.type,
          text: "",
          ...(item.transcriptKey !== undefined ? { transcriptKey: item.transcriptKey } : {}),
          ...(item.position !== undefined ? { position: item.position } : {}),
          ...(item.callId !== undefined ? { callId: item.callId } : {}),
        }),
    );
  }

  // The dedupe key of a remembered skeleton: composite identity, so two
  // skeletons that share an id but differ on transcript key (or vice versa)
  // stay distinct entries while a re-shed of the same item replaces its own
  // entry instead of growing the set.
  function skeletonKey(skeleton: ItemModel): string {
    return `${skeleton.id}\u0000${skeleton.transcriptKey ?? ""}`;
  }

  // Conservative collision scan: which compact turns remember an identity
  // the incoming side carries? The package's itemIdentityMatches rule
  // (transcriptKey when both sides carry one, else id) is approximated from
  // above by testing both fields — a false collision only injects skeletons
  // the package then fails to match and the strip removes, so the common
  // no-collision case costs one lookup per remembered identity and never
  // over-folds.
  function compactedTurnsCollidingWith(identities: Set<string>): Set<string> {
    const colliding = new Set<string>();
    if (compactedTurnItems.size === 0) return colliding;
    for (const [turnId, skeletons] of compactedTurnItems) {
      for (const skeleton of skeletons) {
        if (
          identities.has(skeleton.transcriptKey ?? skeleton.id) ||
          identities.has(skeleton.id)
        ) {
          colliding.add(turnId);
          break;
        }
      }
    }
    return colliding;
  }

  // Inject the colliding turns' remembered skeletons into their side of the
  // merge, reporting the injected items so the result can be stripped. A
  // turn whose payloads came back (a same-id page fragment, an earlier fold)
  // injects its remembered identities even where a real item already
  // represents one of them — under ONE alias. A partial restoration must
  // not forget the rest, and a re-issue under a remembered alias the
  // restored item does not carry can only fold through the alias skeleton
  // (round 10); the restored payload itself stays authoritative, and a
  // skeleton nothing matches comes back out through the strip.
  function injectCompactedSkeletons(
    turns: TurnModel[],
    colliding: Set<string>,
  ): { turns: TurnModel[]; injected: ItemModel[] } {
    if (colliding.size === 0) return { turns, injected: [] };
    const injected: ItemModel[] = [];
    let changed = false;
    const replacement = turns.map((turn) => {
      if (!colliding.has(turn.id)) return turn;
      const skeletons = compactedTurnItems.get(turn.id);
      if (skeletons === undefined) return turn;
      // Review round 10: every remembered skeleton injects, including ones
      // the turn already carries a real item for under ONE alias. "Already
      // represented" was true only under that alias: a restored item
      // supersedes its skeleton's keyed identity, but a later reissue under
      // the remembered BARE id matches neither the restored item nor the
      // turn id, and skipping the alias let that reissue survive as its own
      // turn and double-count usage. The alias skeleton is what folds it.
      // The restored payload stays authoritative through the merge itself —
      // a skeleton carries the reducer's omitted-text marker, so a fold
      // with a real item keeps the real item's provided text (round 5) —
      // and a skeleton nothing matches comes back out through the strip.
      if (skeletons.length === 0) return turn;
      changed = true;
      injected.push(...skeletons);
      return { ...turn, items: [...turn.items, ...skeletons] };
    });
    return changed ? { turns: replacement, injected } : { turns, injected: [] };
  }

  // The structured index behind the strip pass's "was this identity real
  // anywhere" test — exact under the package's own matching rule, not an
  // unqualified string set (review round 7): a keyed item is real through
  // its own transcript key or a KEYLESS source's bare id; a keyless item is
  // real through any source's id. A keyed source sharing the item's bare id
  // under a CONFLICTING key is not a match — exactly itemIdentityMatches.
  type RealIdentityIndex = {
    keyedTranscriptKeys: Set<string>;
    bareIdsOfKeylessSources: Set<string>;
    allIds: Set<string>;
  };
  function realIdentityIndexOf(
    sources: Iterable<{ id: string; transcriptKey?: string }>,
  ): RealIdentityIndex {
    const keyedTranscriptKeys = new Set<string>();
    const bareIdsOfKeylessSources = new Set<string>();
    const allIds = new Set<string>();
    for (const source of sources) {
      if (source.transcriptKey !== undefined) keyedTranscriptKeys.add(source.transcriptKey);
      else bareIdsOfKeylessSources.add(source.id);
      allIds.add(source.id);
    }
    return { keyedTranscriptKeys, bareIdsOfKeylessSources, allIds };
  }
  function itemIsRealSomewhere(
    item: { id: string; transcriptKey?: string },
    index: RealIdentityIndex,
  ): boolean {
    return item.transcriptKey !== undefined
      ? index.keyedTranscriptKeys.has(item.transcriptKey) ||
          index.bareIdsOfKeylessSources.has(item.id)
      : index.allIds.has(item.id);
  }

  // Remove injected skeleton items the package kept without folding them
  // into real content. Two passes, because folded results are new objects:
  // (1) Reference identity removes skeletons the package left untouched.
  // (2) An identity pass removes results the merge built out of skeletons
  //     ALONE — two remembered aliases of the same item (an item restored
  //     under a different id with the same transcript key, then compacted
  //     again, leaves both) match each other in mergePageItems and fold into
  //     an unrestored placeholder that no real side contributed to. Such an
  //     item claims transcript coverage a later rehydrate would read as
  //     retained evidence. An item survives the pass only when some real
  //     source — a pre-injection item, a page item, a fresh item — matches
  //     it by the package's own rule.
  //
  // Review round 8, tool-result folds: one class of failing item is content,
  // not memory. The package's callId fold removes a real tool RESULT from
  // the merged items and carries its fields onto the matching CALL item —
  // which can be an injected call SKELETON, leaving the enriched host the
  // only item holding the result's content under an identity no real source
  // carries. Stripping it deleted both representations of the result. A
  // host that received a real result's fields therefore survives — unless a
  // real call with the same callId SURVIVES IN THE MERGED OUTPUT, because
  // surviving calls keep their own identity, the fold enriches them with the
  // same fields, and keeping the host beside one would duplicate content the
  // surviving call already carries.
  //
  // Review round 10: the disqualifying check reads the merged OUTPUT, never
  // the merge inputs. A real call among the inputs can be CONSUMED by an
  // identity fold through remembered aliases before the tool fold rewrites
  // the surviving call — then no real call with that callId is left in the
  // output, the rewritten host is the sole carrier of the result's content,
  // and an input-based check would reject it and delete the content all over
  // again.
  //
  // Review round 9, alias chains: a real page item can fold THROUGH
  // remembered skeletons and settle on an identity no real source carries
  // (a compact turn remembering two id aliases of one item, then a page
  // supplying the item keyless under the first alias's bare id — the merge
  // folds the real text through both skeletons and lands on the second
  // alias's identity). The page item itself is consumed by the fold, so the
  // final identity is the only handle the content has — deleting it lost
  // the restored text outright. The merge's own item membership says which
  // inputs folded into an item: it survives whenever one of them is a real
  // source, by reference. Membership runs the OTHER way too — a fold whose
  // inputs are all remembered skeletons (the round 6-7 placeholders) still
  // has no real source in it and still strips.
  function descendsFromRealSource(
    item: ItemModel,
    itemFoldSources: ((item: ItemModel) => readonly ItemModel[]) | undefined,
    realSourceRefs: ReadonlySet<unknown>,
  ): boolean {
    if (itemFoldSources === undefined) return false;
    return itemFoldSources(item).some((source) => realSourceRefs.has(source));
  }
  function hostsRealToolResultFold(
    item: { id: string; callId?: string },
    realResultCallIds: ReadonlySet<string>,
    realOutputCallCallIds: ReadonlySet<string>,
  ): boolean {
    return (
      item.callId !== undefined &&
      isToolCallItemId(item.id) &&
      realResultCallIds.has(item.callId) &&
      !realOutputCallCallIds.has(item.callId)
    );
  }
  function stripInjectedSkeletons(
    turns: TurnModel[],
    injected: ItemModel[],
    realSources: ReadonlyArray<{ id: string; transcriptKey?: string; callId?: string }>,
    itemFoldSources?: (item: ItemModel) => readonly ItemModel[],
  ): TurnModel[] {
    if (injected.length === 0) return turns;
    const injectedRefs = new Set(injected);
    const realSourceRefs: ReadonlySet<unknown> = new Set(realSources);
    const realIdentities = realIdentityIndexOf(realSources);
    const realResultCallIds = new Set<string>();
    for (const source of realSources) {
      if (source.callId === undefined) continue;
      if (isToolResultItemId(source.id)) realResultCallIds.add(source.callId);
    }
    const realOutputCallCallIds = new Set<string>();
    for (const turn of turns) {
      for (const item of turn.items) {
        if (
          item.callId !== undefined &&
          isToolCallItemId(item.id) &&
          itemIsRealSomewhere(item, realIdentities)
        ) {
          realOutputCallCallIds.add(item.callId);
        }
      }
    }
    let changed = false;
    const stripped = turns.map((turn) => {
      const kept = turn.items.filter(
        (item) =>
          !injectedRefs.has(item) &&
          (itemIsRealSomewhere(item, realIdentities) ||
            hostsRealToolResultFold(item, realResultCallIds, realOutputCallCallIds) ||
            descendsFromRealSource(item, itemFoldSources, realSourceRefs)),
      );
      if (kept.length === turn.items.length) return turn;
      changed = true;
      return { ...turn, items: kept };
    });
    return changed ? stripped : turns;
  }

  // After a merge, a compact turn may have folded away entirely — its
  // skeletons matched a fresh fragment under a different id at rehydrate,
  // or at loadOlder a page fragment bridged it into another retained turn,
  // and the group's last fragment won the id. Its remembered identities and
  // its page ownership move to the surviving turn that carries its content,
  // so a future re-issue still folds and the preservation gate still sees
  // the page history.
  function transferFoldedCompactedEntries(
    after: TurnModel[],
    folds?: ReadonlyMap<string, readonly string[]>,
  ): void {
    if (compactedTurnItems.size === 0) return;
    const afterIds = new Set(after.map((turn) => turn.id));
    for (const [turnId, skeletons] of [...compactedTurnItems]) {
      if (afterIds.has(turnId)) continue;
      // Review round 8: when the merge's own fragment membership is at hand,
      // IT names the carrier — not final item identities. A remembered
      // keyless identity restored under its bare id carrying a transcript
      // key can coalesce with a second fragment sharing that key, and the
      // merged item's final identity then matches NEITHER skeleton:
      // identity matching finds no carrier, and deleting the entry forgot
      // the turn's OTHER remembered identities too — a later reissue of one
      // of those survived beside the carrier and double-counted usage. A
      // fold whose output turn the merge itself dropped has no carrier left
      // (a degenerate fold); the memory can no longer reach a stored turn
      // either way.
      let foldSurvivor: string | undefined;
      let foldNamesCarrier = false;
      if (folds !== undefined) {
        for (const [outputId, olderTurnIds] of folds) {
          if (!olderTurnIds.includes(turnId)) continue;
          foldNamesCarrier = true;
          if (afterIds.has(outputId)) foldSurvivor = outputId;
          break;
        }
      }
      // Review round 5: the owner is the first surviving turn that carries
      // an item matching a remembered skeleton by the package's own rule
      // (itemIdentityMatches: transcriptKey when both sides carry one, else
      // id) — never a bare-id key match alone. A remembered keyed item whose
      // id collides with an unrelated keyed item's id must not hand its
      // memory (and the vanished turn's page ownership) to that unrelated
      // turn, where a later injection would coalesce it with a fragment it
      // never shared an identity with and drop a separate usage stamp. The
      // id-only fallback still works through the rule itself: an id-only
      // skeleton matches a keyed item sharing its id, and a keyed skeleton
      // matches its own key.
      let survivor: string | undefined;
      if (foldSurvivor !== undefined) {
        survivor = foldSurvivor;
      } else if (!foldNamesCarrier) {
        for (const turn of after) {
          if (
            turn.items.some((item) =>
              skeletons.some((skeleton) => itemIdentityMatches(item, skeleton)),
            )
          ) {
            survivor = turn.id;
            break;
          }
        }
      }
      if (survivor === undefined) {
        // The content truly has no carrier left (a degenerate fold); the
        // memory can no longer reach a stored turn either way — drop it.
        compactedTurnItems.delete(turnId);
        continue;
      }
      compactedTurnItems.set(survivor, [
        ...(compactedTurnItems.get(survivor) ?? []),
        ...skeletons,
      ]);
      compactedTurnItems.delete(turnId);
      if (pageOwnedCompactTurnIds.delete(turnId)) {
        pageOwnedTurnIds.add(survivor);
      }
    }
  }

  return create<LiveConversationState>((rawSet, get) => {
    // R1: Wrap set so any write to pendingMutation or error increments the
    // corresponding monotonic revision counter — even ABA (same value). This
    // is the single chokepoint for ownership transitions; all set() calls
    // inside the store go through this wrapper.
    const set = (partial: Partial<ConversationState>) => {
      if ("pendingMutation" in partial) mutationOwnerRev += 1;
      if ("error" in partial) errorOwnerRev += 1;
      rawSet(partial);
    };
    storeGet = get;
    return {
      ref: null,
      profileId: null,
      connectionGeneration: 0,
      conversationGeneration: 0,

      conversation: null,
      olderCursor: null,
      hasEarlierItems: false,
      hasLaterItems: false,
      loadingOlder: false,
      status: "idle",
      error: null,

      draft: "",
      pendingSend: null,
      pendingMutation: null,
      lastAcceptedMutation: null,

      async open(service, ref) {
        suspendedService = null;
        // Increment conversation generation so late frames from a previous
        // conversation are rejected.
        const gen = ++conversationGen;
        // I1: plain open CANNOT retain projected bindings — clear them and
        // increment the binding epoch so any queued projected rehydrate/cap
        // refresh is suppressed at the boundary.
        bindingEpoch += 1;
        boundService = null;
        boundSink = null;
        // Fix round 1 I2: invalidate page ownership on conversation transition.
        loadOlderToken += 1;
        // C1+I1: clear any stale deferred trailing-reread request.
        trailingReread = null;
        pageOwnedIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        liveOwnedRevs.clear();
        truncatedItemIds.clear();
        set({
          status: "opening",
          ref,
          error: null,
          conversation: null,
          olderCursor: null,
          hasEarlierItems: false,
          hasLaterItems: false,
          loadingOlder: false,
          draft: "",
          pendingSend: null,
          pendingMutation: null,
          lastAcceptedMutation: null,
          conversationGeneration: gen,
        });
        try {
          const conv = await service.open(ref);
          // Reject if a newer conversation generation was opened during the await.
          if (gen !== conversationGen) return;
          // Task 2A-Truncation: cap the original items, reconcile truncation
          // ownership exactly from the FINAL retained (capped) pre-truncation
          // items (authoritative short versions unfreeze; omitted/capped IDs
          // are removed), then truncate the text.
          const openCapped = capItems(conv.items);
          reconcileTruncationFrom(openCapped);
          const openItems = truncateAndRecord(openCapped);
          set({
            conversation: {
              ...conv,
              items: openItems,
            },
            status: "open",
            olderCursor: null,
          });
          // C1: Track the plain-open service as boundService with a null
          // sink so loadOlder/mutations validate exact service
          // identity via captureOperationBinding.
          boundService = service as LiveConversationService;
          // Subscribe to notifications for this thread.
          service.subscribeNotifications((n) => {
            if (gen !== conversationGen) return;
            get().applyNotification(n);
          });
        } catch (err) {
          if (gen !== conversationGen) return;
          set({
            status: "error",
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },

      async openProjected(service, sink, ref, replacement) {
        suspendedService = null;
        const gen = ++conversationGen;
        // I1: increment the binding epoch and bind service+sink so queued
        // requests from an older binding are suppressed at the boundary.
        bindingEpoch += 1;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        // Fix round 1 I2: invalidate page ownership on conversation transition.
        loadOlderToken += 1;
        // C1+I1: clear any stale deferred trailing-reread request.
        trailingReread = null;
        pageOwnedIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        liveOwnedRevs.clear();
        truncatedItemIds.clear();
        // Reset thread-scoped state (draft, pending mutation) — presentation state
        // now lives outside the store (in live-ui-store).
        // F4: reset the activity sink on thread change.
        sink.reset();
        set({
          status: "opening",
          ref,
          error: null,
          conversation: null,
          olderCursor: null,
          loadingOlder: false,
          draft: "",
          pendingSend: null,
          pendingMutation: null,
          lastAcceptedMutation: null,
          conversationGeneration: gen,
        });
        let active = true;
        let unsubscribe: (() => void) | null = null;
        let liveHandler: ((notification: AnyNotification) => void) | null =
          null;
        try {
          // Subscribe before the read so no frame after its response is missed;
          // a frame before the response is already in the snapshot (the
          // response-cut note by applyThreadNotification), so no handler yet.
          unsubscribe = service.subscribeNotifications((notification) => {
            if (!active || gen !== conversationGen) return;
            liveHandler?.(notification);
          });
          const {
            conversation,
            activity,
            olderCursor,
            hasEarlierItems,
            hasLaterItems,
          } = replacement ?? (await service.readProjection(ref));
          if (gen !== conversationGen) return;
          const identity: ActivityIdentity = {
            threadId: conversation.threadId,
            ref,
            generation: gen,
          };
          // Strict activity sink: call setLiveView BEFORE committing the paired
          // conversation projection. If it returns false (stale/invalid identity),
          // do not commit the conversation projection — the two stores stay
          // atomically consistent under the same exact identity tuple.
          const accepted = sink.setLiveView(activity, identity);
          if (!accepted) return;
          // Task 2A-Truncation: cap the original items, reconcile truncation
          // ownership exactly from the FINAL retained (capped) pre-truncation
          // items, then truncate the text.
          const openProjCapped = capItems(conversation.items);
          reconcileTruncationFrom(openProjCapped);
          const openProjItems = truncateAndRecord(openProjCapped);
          set({
            conversation: {
              ...conversation,
              items: openProjItems,
            },
            status: "open",
            olderCursor,
            hasEarlierItems: hasEarlierItems ?? false,
            hasLaterItems: hasLaterItems ?? false,
          });
          liveHandler = (n) => {
            if (gen !== conversationGen) return;
            // Route notifications to BOTH stores — conversation and activity.
            // Propagate every returned outcome; rehydrate requests the one shared
            // drain scheduler. Notification-first, identity-second.
            const outcome = sink.applyLiveNotification(n, identity);
            if (outcome === "rehydrate") {
              requestRehydrate(ref);
            }
            // Also process the conversation store's own notification handler.
            // Only call the conversation's applyNotification if the activity
            // sink did not say "ignored" (ignored means the activity store
            // rejected it on identity grounds — but conversation still needs
            // its own processing for conversation-specific notifications).
            get().applyNotification(n);
          };
        } catch (err) {
          if (gen !== conversationGen) return;
          set({
            status: "error",
            error: err instanceof Error ? err.message : String(err),
          });
        } finally {
          if (!liveHandler) {
            active = false;
            // The service's cleanup targets its current subscription. Never
            // let an old open unsubscribe a newer conversation's listener.
            if (gen === conversationGen) unsubscribe?.();
          }
        }
      },

      suspendProjected() {
        const state = get();
        if (state.ref === null) return;
        suspendedService = boundService;
        conversationGen += 1;
        bindingEpoch += 1;
        loadOlderToken += 1;
        trailingReread = null;
        boundService = null;
        boundSink = null;
        set({
          status: "opening",
          error: null,
          pendingMutation: null,
          pendingSend: null,
          loadingOlder: false,
          conversationGeneration: conversationGen,
        });
      },

      async resumeProjected(service, sink, ref) {
        const state = get();
        if (
          state.ref !== ref ||
          state.conversation === null ||
          suspendedService !== service
        ) {
          await get().openProjected(service, sink, ref);
          return;
        }
        const gen = ++conversationGen;
        bindingEpoch += 1;
        loadOlderToken += 1;
        trailingReread = null;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        set({
          status: "opening",
          error: null,
          pendingMutation: null,
          pendingSend: null,
          loadingOlder: false,
          conversationGeneration: gen,
        });

        let active = true;
        // As in openProjected: no handler until the snapshot has committed (a
        // frame before the response is already in it — the response-cut note
        // by applyThreadNotification).
        let liveHandler: ((notification: AnyNotification) => void) | null =
          null;
        const unsubscribe = service.subscribeNotifications((notification) => {
          if (!active || gen !== conversationGen) return;
          liveHandler?.(notification);
        });
        try {
          await get().rehydrate(service, sink);
          if (!active || gen !== conversationGen) return;
          const current = get();
          if (
            current.error !== null ||
            acceptedRehydrate === null ||
            acceptedRehydrate.generation !== gen ||
            acceptedRehydrate.sink !== sink
          ) {
            boundService = null;
            boundSink = null;
            suspendedService = service;
            set({
              status: "error",
              error:
                current.error ?? "Could not refresh the session. Try again.",
            });
            return;
          }
          suspendedService = null;
          set({ status: "open" });
          liveHandler = (notification) => {
            const conversation = get().conversation;
            if (conversation) {
              const outcome = sink.applyLiveNotification(notification, {
                threadId: conversation.threadId,
                ref,
                generation: gen,
              });
              if (outcome === "rehydrate") requestRehydrate(ref);
            }
            get().applyNotification(notification);
          };
        } finally {
          if (gen !== conversationGen || get().status === "error") {
            active = false;
            if (gen === conversationGen) unsubscribe();
          }
        }
      },

      async rehydrate(service, sink) {
        // Rehydrate uses readProjection to refresh the conversation without
        // calling destructive open(). Preserves draft.
        // F3: rehydrate triggers the same shared reread.
        // F9: Operation token prevents stale rehydrate from overwriting
        // newer state within the same generation.
        // I1: rehydrate accepts the expected binding (ref+gen from entry state)
        // rather than resnapshotting current state mid-flight. It can never
        // call serviceA with refB — the scheduler effect verifies the binding
        // epoch before calling this, and rehydrate itself re-checks.
        // I1: Any rehydrate(service, sink) with different objects than the
        // current binding increments bindingEpoch BEFORE assignment, so queued
        // effects captured with the old service/sink are suppressed.
        // R1: capture monotonic mutation-owner revision and error-owner revision
        // (not mutationId/null or error string) at entry. After the await, if
        // either changed, the rehydrate is stale with respect to that owner.
        // If mutation owner changed, do not publish predating projection;
        // arrange one bounded trailing authoritative reread after the mutation
        // settles via the scheduler (no loop, no reentrant await).
        // R2: if page owner changed, safely merge/preserve newer page-owned
        // items/cursor while committing the reread conversation+activity.
        const state = get();
        if (state.ref === null) return;
        acceptedRehydrate = null;
        const ref = state.ref;
        const gen = state.conversationGeneration;
        // I1: If the service or sink objects differ from the current binding,
        // increment bindingEpoch BEFORE assignment so queued effects captured
        // with the old service/sink are suppressed.
        // I5: The rebind transition itself settles any old pending/loading
        // ownership so A's late completion makes ZERO state changes — no
        // permanent UI state stuck. This is the new binding transition, not
        // A's completion. A pending mutation or loadingOlder from the old
        // binding is invalidated by incrementing its operation tokens.
        if (service !== boundService || sink !== boundSink) {
          bindingEpoch += 1;
          // I5: Invalidate old page/loading ownership so a held loadOlder
          // completion from A cannot write loadingOlder/items/cursor.
          loadOlderToken += 1;
          // I5: Settle any held pending mutation from the old binding so a
          // held mutation completion from A cannot write pending/error/draft.
          // Only clear if the current pending mutation belongs to the old
          // binding (it always does — rehydrate is same-generation, and a
          // new binding means the old one's mutation is stale).
          const heldMutation = get().pendingMutation;
          if (
            heldMutation !== null &&
            heldMutation !== undefined &&
            heldMutation.status === "pending"
          ) {
            // Settle the old pending mutation — it belongs to A's binding.
            // Do NOT write error (a newer error owner may own it). Just
            // clear the pending state so it's not permanently stuck.
            set({ pendingMutation: null, pendingSend: null });
          }
          // I5: Settle held loadingOlder from the old binding.
          if (get().loadingOlder) {
            set({ loadingOlder: false });
          }
        }
        // I1: Capture the binding epoch at entry — if it changed during the
        // await (open/close/reset/openProjected/openProjected/rehydrate), this
        // rehydrate is stale.
        const entryEpoch = bindingEpoch;
        const token = ++rehydrateToken;
        // R1: Capture monotonic ownership revisions at entry (not string/id).
        const entryLoadOlderToken = loadOlderToken;
        const entryMutationRev = mutationOwnerRev;
        const entryErrorRev = errorOwnerRev;
          // D18 B3 round 8: the store's own paging cursor at entry. A racing
          // loadOlder that succeeds with no retained rows still advances this
          // value; a failed one moves it not at all — the difference the
          // cursor merge below reads to keep a successful page's advancement
          // without letting a failed page pin a stale pre-race cursor.
          const entryOlderCursor = get().olderCursor;
        // Fix round 1: Capture live-owner revision at entry. If an item's
        // liveOwnedRevs revision advanced past this after entry, the live
        // notification updated the item after the rehydrate's readProjection
        // snapshot — the current version is newer and must be preserved.
        const entryLiveRev = liveOwnerRev;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        try {
          const {
            conversation,
            activity,
            olderCursor,
            hasEarlierItems,
            hasLaterItems,
          } = await service.readProjection(ref);
          // I1: Suppress stale work — if the binding epoch changed, the store
          // switched to a different service/sink/ref. Do not commit.
          if (entryEpoch !== bindingEpoch) return;
          // Guard: a newer generation may have opened during the await.
          if (get().conversationGeneration !== gen) {
            return;
          }
          // F9: Stale rehydrate — a newer rehydrate started in the same gen.
          if (token !== rehydrateToken) {
            return;
          }
          const currentSnapshot = get();
          const mutationOwnerChanged = entryMutationRev !== mutationOwnerRev;
          // R1: If mutation owner changed, do not publish predating projection.
          // Arrange one bounded trailing authoritative reread via the scheduler
          // (no loop, no reentrant await). The trailing reread publishes the
          // fresh projection once the mutation has settled.
          if (mutationOwnerChanged) {
            // C1+I1: Store a deferred trailing reread with the exact binding
            // captured at schedule time. Do NOT schedule through the scheduler
            // yet — the request is drained when the mutation settles. If a
            // request is already pending, it is overwritten (at most one per
            // mutation revision).
            if (trailingReread === null) {
              scheduleTrailingReread();
            }
            // Preserve current draft only — do not publish projection.
            set({ draft: currentSnapshot.draft });
            return;
          }
          // R2: If the same conversation instance has page-owned history,
          // preserve that history/cursor while committing the reread
          // conversation+activity. A replaced instance must start clean.
          // Merge page items (from current conversation) into the reread's
          // conversation items, deduping by source item identity, and keep
          // the page's newer cursor.
          // Fix round 1: Per-item live ownership with monotonic revision. For
          // each item in the reread, if its liveOwnedRevs revision advanced
          // past entryLiveRev, the live notification updated it after the
          // reread's snapshot — preserve the current version in the
          // authoritative position. Otherwise accept the reread's version.
          // Items omitted from the reread that are genuinely live-owned are
          // appended as the live tail. Page-owned items prepend. Unowned old
          // history drops.
          const rereadIds = new Set(conversation.items.map((i) => i.id));
          const rereadKeys = new Set(conversation.items.map(timelineIdentity));
          const rereadIdentities = new Set(
            conversation.items.flatMap((item) => [...timelineIdentities(item)]),
          );
          const currentConvForMerge = currentSnapshot.conversation;
          const sameInstance =
            currentConvForMerge !== null && currentConvForMerge.instanceId === conversation.instanceId;
          const replacesInstance = currentConvForMerge !== null && !sameInstance;
          const preservePageHistory =
            sameInstance && pageOwnedIds.size > 0;
          // Turn-history and wire-cursor merging gate on turn ownership, not
          // item ownership — a page whose items
          // were entirely deduped or evicted still owns turns that must not
          // be dropped, since they may be the only usage data a session
          // without a thread-level cumulative total has. That stays true
          // after the retained-turn bound: a trimmed turn is still owned
          // page history — its id has only moved to the compact set.
          const preserveTurnHistory =
            sameInstance &&
            (pageOwnedTurnIds.size > 0 || pageOwnedCompactTurnIds.size > 0);
          // Superseded: reread contains ID but current live revision > entry.
          // Preserve the current (live-updated) version in the reread position.
          const supersededIds = new Set<string>();
          const supersededVersions = new Map<string, MobileTimelineItem[]>();
          if (currentConvForMerge !== null) {
            for (const item of conversation.items) {
              const identity = timelineIdentity(item);
              if (item.kind === "activity") {
                const replacement = mergeLiveActivityMembers(item, currentConvForMerge.items, liveOwnedRevs, entryLiveRev);
                if (replacement) {
                  // Supersession is per member, like truncation and freezing.
                  // Only the members the live side won are named: a cluster's
                  // top-level identity IS its first member's, so adding it
                  // whenever any member was won would freeze that first member
                  // against an authoritative short reread. An unclustered item
                  // is its own member, so it names itself here.
                  for (const member of replacement.supersededIdentities) {
                    supersededIds.add(member);
                  }
                  supersededVersions.set(identity, replacement.rows);
                }
                continue;
              }
              const current = currentConvForMerge.items.find(
                (candidate) => candidate.kind === item.kind && timelineIdentity(candidate) === identity,
              );
              if (current && liveRevisionForItem(current, liveOwnedRevs) > entryLiveRev) {
                supersededIds.add(identity);
                supersededVersions.set(identity, [current]);
              }
            }
          }
          // Replace superseded items in the reread with the current version.
          let mergedItems = conversation.items.flatMap((item) => {
            // A newer whole-item notification can remove its attachment row.
            // Use the retained source's revision so a stale snapshot cannot
            // resurrect it, without keeping tombstones for evicted rows.
            const sourceId = attachmentSourceIdentity(item);
            if (sourceId !== null) {
              const sourceRev = liveRevisionForItem(item, liveOwnedRevs);
              if (
                sourceRev !== undefined &&
                sourceRev > entryLiveRev &&
                !currentConvForMerge?.items.some(
                  (current) => current.id === item.id,
                )
              ) {
                return [];
              }
            }
            return supersededVersions.get(timelineIdentity(item)) ?? [item];
          });
          const omittedItems = currentConvForMerge === null ? [] : itemsAbsentFromSnapshot(
            currentConvForMerge.items, rereadIdentities, rereadKeys,
          );
          let mergedCursor = olderCursor;
          if (preservePageHistory) {
            if (currentConvForMerge !== null) {
              // 1. Prepend only current-only pageOwned history (items in
              //    pageOwnedIds that are not in the reread projection).
              // 2. Commit the authoritative reread projection (with superseded
              //    replacements applied).
              // 3. Append only current-only liveOwned tail (items in
              //    liveOwnedRevs that are not in the reread projection).
              // 4. Drop current-only items owned by NEITHER (not pageOwned,
              //    not liveOwned, not in reread) as omitted old history.
              const pageOnlyItems = omittedItems.filter(
                (i) =>
                  pageOwnedIds.has(timelineIdentity(i)),
              );
              const liveTailItems = omittedItems.filter(
                (i) =>
                  !pageOwnedIds.has(timelineIdentity(i)) &&
                  isLiveOwned(i, liveOwnedRevs),
              );
              // Page history first (oldest), then reread items, then live tail.
              // Items owned by neither are dropped (omitted old history).
              mergedItems = [
                ...pageOnlyItems,
                ...mergedItems,
                ...liveTailItems,
              ];
            }
            // Keep the page's newer cursor (the reread's cursor reflects the
            // full readProjection, which may not include page-loaded items).
            mergedCursor = currentSnapshot.olderCursor;
          } else if (currentConvForMerge !== null) {
            // No page race, but still append live-owned items omitted from the
            // reread (live notifications that arrived during the await).
            const liveTailItems = omittedItems.filter(
              (i) =>
                !pageOwnedIds.has(timelineIdentity(i)) &&
                isLiveOwned(i, liveOwnedRevs),
            );
            if (liveTailItems.length > 0) {
              mergedItems = [...mergedItems, ...liveTailItems];
            }
          }
          // D18 B3 round 8: a racing loadOlder that retained no display rows
          // (every row deduped, or cap-evicted) still advanced the store's
          // own paging cursor, and the fresh reread's window cursor knows
          // nothing about pages this client already consumed — keep the
          // advancement, or the next loadOlder re-requests that page (or
          // resurrects paging at a cursor the cap or exhausted history had
          // honestly stopped). A FAILED racing page also bumps the page
          // token but moves the cursor not at all, so the entry comparison
          // — not the token, and not row ownership — is what separates the
          // two: the fresh read's own signal still wins unless the store's
          // own cursor actually moved during the await.
          const pageCursorAdvanced =
            sameInstance && currentSnapshot.olderCursor !== entryOlderCursor;
          if (pageCursorAdvanced) mergedCursor = currentSnapshot.olderCursor;
          // Accept a snapshot's removal of a companion when it also contains
          // the source, unless a live event changed that group during the read.
          mergedItems = mergedItems.filter((item) => {
            const sourceId = attachmentSourceIdentity(item);
            return (
              sourceId === null ||
              !rereadIdentities.has(sourceId) ||
              rereadIds.has(item.id) ||
              liveRevisionForItem(item, liveOwnedRevs) > entryLiveRev
            );
          });
          mergedItems = attachToSources(mergedItems);
          const identity: ActivityIdentity = {
            threadId: conversation.threadId,
            ref,
            generation: gen,
          };
          // Strict activity sink: call setLiveView BEFORE committing the paired
          // conversation projection. If it returns false, do not commit.
          const accepted = sink.setLiveView(activity, identity);
          if (!accepted) return;
          acceptedRehydrate = { generation: gen, sink };
          // R1: Success preserves any newer error owner. Only clear error if
          // the error-owner revision hasn't changed AND no failed mutation
          // owns the error. A failed mutation's error persists until a
          // subsequent mutation or open clears it.
          const currentState = get();
          const errorUnchanged = entryErrorRev === errorOwnerRev;
          const mutationOwnsError =
            currentState.pendingMutation?.status === "failed";
          // Task 2A-Truncation: cap the merged items, reconcile truncation
          // ownership exactly from the FINAL retained (capped) pre-truncation
          // items. mergedItems already contains superseded live/page
          // replacements (newer versions preserved based on final actual
          // content). I2: do NOT pass all superseded live IDs as frozen —
          // only superseded IDs that are STILL in truncatedItemIds after the
          // accepted live update. A short lifecycle/delta/reset that removed
          // the freeze stays unfrozen; a superseded item still frozen (live
          // delta made it oversized) stays frozen. I1: priorFrozenIds preserves
          // freeze for already-frozen current-only items (live tail / page
          // items not in the reread — already truncated, text ≤ limit). Reread
          // IDs are authoritative: short content unfreezes, oversized freezes.
          // The reread set is member-inclusive: a clustered member's freeze is
          // as answerable to an authoritative short version as a top-level
          // row's, and a top-level-only set would carry every member freeze
          // forward for good.
          const rehydratePriorFrozen = new Set<string>();
          for (const id of truncatedItemIds) {
            if (!rereadIdentities.has(id)) rehydratePriorFrozen.add(id);
          }
          const supersededFrozen = new Set<string>();
          for (const id of supersededIds) {
            if (truncatedItemIds.has(id)) supersededFrozen.add(id);
          }
          const rehydrateCapped = capItems(mergedItems);
          reconcileTruncationFrom(
            rehydrateCapped,
            rehydratePriorFrozen,
            supersededFrozen,
          );
          const committedItems = truncateAndRecord(rehydrateCapped);
          // The reread's own turns cover only its itemLimit-bounded window,
          // so a turn loaded via an earlier
          // loadOlder (outside that window) is absent from it. Preserve those
          // turns — deduped by id, older first — under preserveTurnHistory
          // (turn ownership, not item ownership: a page whose rows were all
          // deduped/evicted still owns its turns), or a session with no
          // cumulative usage loses everything loadOlder added the moment the
          // next rehydrate runs.
          //
          // conversation.olderCursor is the same wire-truth value, carried
          // the same way: currentConvForMerge.olderCursor is itself the wire
          // cursor loadOlder/a prior rehydrate already established, never the
          // store's own capped pagination cursor (currentSnapshot.olderCursor
          // — a UI-only concern, set below via mergedCursor). Reading that
          // capped value here would flip a partial sum's scope to "session".
          let mergedTurns = conversation.turns;
          let wireOlderCursor = conversation.olderCursor;
          if (preserveTurnHistory && currentConvForMerge !== null) {
            // The public merge folds accumulated page turns into the fresh
            // read with fresh-defined fields winning and older fragments
            // supplying omitted fields/items. Coverage is separate from the
            // value merge: local observations and bare warnings stay visible
            // without making a fresh cursor look partial.
            // Review rounds 1-2: inject identity skeletons for compact
            // turns whose remembered identities the fresh read re-issues,
            // so the package's own turnsMatch/coalescing does the folding
            // exactly as the unbounded main would have.
            const freshIdentities = new Set<string>();
            for (const turn of conversation.turns) {
              for (const item of turn.items) {
                freshIdentities.add(item.transcriptKey ?? item.id);
                freshIdentities.add(item.id);
              }
            }
            const injectedFresh = injectCompactedSkeletons(
              currentConvForMerge.turns,
              compactedTurnsCollidingWith(freshIdentities),
            );
            const history = mergeTurnHistoryWithFolds(injectedFresh.turns, conversation.turns);
            mergedTurns = history.turns;
            // Review round 3: the wire-cursor gate must read only RETAINED
            // transcript evidence. Injected skeletons fold fragments, but
            // they are memory, not content — an unmatched skeleton must not
            // claim older coverage or transcript overlap and let discarded
            // history override the fresh wire cursor. When skeletons were
            // injected, take the gate from the same merge WITHOUT them: the
            // compact turns then contribute exactly what the retained
            // state still holds (nothing, or a restored turn's real items).
            // Review round 7: turns that are compact-ONLY (no items, only
            // remembered skeletons) are excluded from that merge's inputs
            // entirely. mergeTurnHistory counts an unmatched empty turn as
            // older coverage (usage metadata is claimed even when items
            // cannot be), so a compact turn that the real, injected merge
            // folded into a fresh carrier under a different id would still
            // claim coverage here — and with an unchanged real turn
            // supplying transcriptOverlap, the stale cursor would override a
            // fresh read that actually covers everything (sessionTokens then
            // reports "loaded" for a complete read). A compact turn holds no
            // transcript content by construction; its usage survives through
            // the actual merge, not through the cursor gate.
            // Review round 8: the exclusion holds regardless of whether a
            // collision actually injected skeletons. Without injection the
            // unmatched compact turn itself still entered the merge and
            // claimed olderCoverage, and an unchanged real turn supplied
            // transcriptOverlap — the stale cursor overrode a fresh read
            // that actually covered everything. So the gate's older input
            // always drops compact-only turns; only when that excludes
            // nothing AND no skeletons were injected is the gate the merge
            // already computed.
            const coverageOlderTurns = currentConvForMerge.turns.filter(
              (turn) => !(turn.items.length === 0 && compactedTurnItems.has(turn.id)),
            );
            const coverage =
              injectedFresh.injected.length > 0 ||
              coverageOlderTurns.length !== currentConvForMerge.turns.length
                ? mergeTurnHistory(coverageOlderTurns, conversation.turns)
                : history;
            if (coverage.olderCoverage && coverage.transcriptOverlap) {
              wireOlderCursor = currentConvForMerge.olderCursor;
            }
            // Strip the injected skeletons the merge did not fold away —
            // including skeleton-on-skeleton alias folds no real side
            // contributed to — move folded compact turns' memory and page
            // ownership to their surviving carriers, then bound the payloads.
            // The real sources are every item the retained side held before
            // the injection plus every item the fresh read carries: an item
            // matching none of them by the package's rule can only be
            // remembered memory, never content.
            const rehydrateRealSources = [
              ...currentConvForMerge.turns.flatMap((turn) => turn.items),
              ...conversation.turns.flatMap((turn) => turn.items),
            ];
            mergedTurns = stripInjectedSkeletons(
              mergedTurns,
              injectedFresh.injected,
              rehydrateRealSources,
              history.itemFoldSources,
            );
            transferFoldedCompactedEntries(mergedTurns, history.olderTurnFolds);
            // #1919 follow-up: bound the merged result AFTER the merge, so
            // turns inside the keep-window keep everything the older
            // fragments supplied, and only out-of-window payloads trim. The
            // final retained rows (rehydrateCapped) are the window; the pass
            // settles page turn ownership with the same bound.
            mergedTurns = boundRetainedTurns(mergedTurns, rehydrateCapped);
          }
          if (replacesInstance) {
            pageOwnedIds.clear();
            pageOwnedTurnIds.clear();
            pageOwnedCompactTurnIds.clear();
            compactedTurnItems.clear();
          }
          // The snapshot's thread-level fields are authoritative (see the
          // response-cut note by applyThreadNotification); the rows are the
          // live/page merge above.
          const committedConversation: MobileConversation = {
            ...conversation,
            items: committedItems,
            turns: mergedTurns,
            olderCursor: wireOlderCursor,
          };
          // Fix round 1: Reconcile liveOwnedRevs — for items in the
          // authoritative reread projection that are NOT superseded (revision
          // ≤ entryLiveRev or not live-owned), accept the reread and clear
          // that ID's ownership. Superseded items (revision > entryLiveRev)
          // keep their ownership — the live version is newer and may need
          // to survive a future page merge. Live-owned items NOT in the reread
          // stay in the map (still live-only / live tail).
          for (const item of conversation.items) {
            const rev = liveRevisionForItem(item, liveOwnedRevs);
            if (rev === undefined || rev <= entryLiveRev) {
              for (const identity of timelineIdentities(item)) liveOwnedRevs.delete(identity);
            }
          }
          pruneEvictedIds(committedItems);
          const commitBase = {
            conversation: committedConversation,
            olderCursor: mergedCursor,
            hasEarlierItems: hasEarlierItems ?? currentSnapshot.hasEarlierItems,
            hasLaterItems: hasLaterItems ?? currentSnapshot.hasLaterItems,
            draft: currentState.draft,
          };
          if (
            errorUnchanged &&
            !mutationOwnsError &&
            currentState.error !== null
          ) {
            // I1: Only include error: null when the current error is actually
            // non-null. Clearing an already-null error would spuriously
            // increment errorOwnerRev (ABA-null), blocking a pending
            // mutation from writing its error after a rehydrate clear-to-null.
            set({ ...commitBase, error: null });
          } else {
            // A mutation or page operation owns the error, or error is already
            // null — preserve it (do not spuriously increment errorOwnerRev).
            set(commitBase);
          }
        } catch (err) {
          // R1: failure may set error only if error/page/mutation owners all
          // remain unchanged. Check binding epoch, generation, token, page
          // owner, mutation-owner revision, and error-owner revision.
          const currentSnapshot = get();
          if (
            entryEpoch === bindingEpoch &&
            currentSnapshot.conversationGeneration === gen &&
            token === rehydrateToken &&
            entryLoadOlderToken === loadOlderToken &&
            entryMutationRev === mutationOwnerRev &&
            entryErrorRev === errorOwnerRev
          ) {
            set({
              error: err instanceof Error ? err.message : String(err),
            });
          }
        }
      },

      async loadOlder(service) {
        const state = get();
        if (state.loadingOlder || state.conversation === null) {
          return { status: "ignored" };
        }
        // F8: Never request with a null/empty cursor — no more older pages.
        if (state.olderCursor === null) return { status: "ignored" };
        const cursor = state.olderCursor;
        const gen = state.conversationGeneration;
        // C1: Capture a service-specific operation binding. If the supplied
        // service is wrong (A after B bound), binding is null — zero request.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return { status: "ignored" };
        // Task 2A-Ops-2: Operation token for loadOlder — a stale success/failure
        // from an older operation must make no state change at all after a
        // newer conversation or newer page operation owns those fields.
        const olderToken = ++loadOlderToken;
        // I1: Capture the error-owner revision BEFORE the call. A current page
        // failure always settles its own loadingOlder, but writes error only if
        // its captured error owner is unchanged; a newer mutation/page error
        // survives.
        const entryErrorRev = errorOwnerRev;
        set({ loadingOlder: true });
        try {
          const result = await service.loadOlder(cursor);
          // C1: Recheck the exact operation binding after the await. If the
          // binding changed (rebind to B), A's completion makes ZERO state
          // changes — no items/cursor/loading.
          if (!isBindingCurrent(opBinding)) return { status: "ignored" };
          // Fix round 1 I2: generation/identity-stale — perform ZERO set calls
          // (including loadingOlder). The newer conversation owns all fields.
          if (get().conversationGeneration !== gen) {
            return { status: "ignored" };
          }
          // Task 2A-Ops-2: Stale loadOlder — a newer page operation owns the
          // loadingOlder/error fields. Make no state change at all.
          if (olderToken !== loadOlderToken) return { status: "ignored" };
          const currentConv = get().conversation;
          if (currentConv !== null) {
            // F10: Dedupe by source item identity — items from older pages
            // that already exist in the current conversation (same id) are
            // dropped, keeping the newer (live tail) version.
            // Task 2A-Items: also dedupe within the incoming page by updating
            // the seen set during traversal, preserving order and first
            // occurrence semantics.
            const existingIds = new Set(
              currentConv.items.flatMap((item) => [...timelineIdentities(item)]),
            );
            const currentIds = new Set(existingIds);
            const deduped: MobileTimelineItem[] = [];
            for (const item of result.items) {
              // A row is a duplicate when ANY identity it carries is already
              // present — an incoming cluster can reintroduce a member under
              // a brand-new top-level id.
              const duplicate = [...ownTimelineIdentities(item)].some((id) =>
                existingIds.has(id),
              );
              if (duplicate) continue;
              const sourceId = attachmentSourceIdentity(item);
              if (sourceId !== null && currentIds.has(sourceId)) continue;
              // I3: Defense-in-depth — filter question rows at the state merge
              // boundary too, not only in the service's projectOlderTurns. A
              // pending ask cannot legitimately be older than newer continuation
              // turns; page-local projection otherwise resurrects settled calls.
              if (item.kind === "question") continue;
              for (const id of timelineIdentities(item)) existingIds.add(id);
              deduped.push(item);
            }
            // I3: Record page-owned item IDs — these are items loaded from
            // older pages. They are tracked so the rehydrate page-race merge
            // can distinguish page-owned history from live notifications.
            for (const item of deduped) {
              pageOwnedIds.add(timelineIdentity(item));
            }
            // Prepend older (deduped) items, then trim from the oldest (front)
            // so the newest live tail is retained (finding 8).
            // Task 2A-Truncation: cap the pre-truncation merged items, reconcile
            // truncation ownership exactly from the FINAL retained (capped)
            // items. I1: capture prior frozen IDs BEFORE reconciliation so
            // already-frozen current items (already truncated, text ≤ limit,
            // exceedsByteLimit false) stay frozen — intersect with current
            // item IDs AND final IDs so capped/removed ownership drops.
            // Incoming raw page items matching a stale frozen ID that is NOT
            // in currentConv are independently judged from their raw content
            // (exceedsByteLimit), NOT carried over as frozen.
            const priorFrozen = new Set<string>();
            for (const id of truncatedItemIds) {
              if (currentIds.has(id)) priorFrozen.add(id);
            }
            const mergedInput = [...deduped, ...currentConv.items];
            const pageMerged = capItems(mergedInput);
            reconcileTruncationFrom(pageMerged, priorFrozen);
            const merged = truncateAndRecord(pageMerged);
            // Prune ownership maps for evicted IDs (IDs not in the final merged
            // set). This prevents stale freeze/page/live entries from
            // affecting future page loads or re-introductions.
            pruneEvictedIds(merged);
            // F8: When the merge trimmed rows — the pre-cap merged set
            // overflowed the retained cap, so capItems discarded the overflow
            // from the oldest end — disable further paging honestly: set
            // cursor to null so we don't repeatedly load rows that will be
            // discarded. The overflow, never the final row count, is the
            // signal: capItems also drops a leading orphaned attachment whose
            // source fell off the cut, so a trimmed merge can end below
            // RETAINED_ITEM_CAP (a 501-row merge that drops one orphan ends at
            // 499) while it discarded its whole page, and a merge that ends at
            // exactly the cap may have discarded nothing at all. Paging
            // therefore ends exactly when the cap discarded rows, and
            // hasEarlierItems must say so — a cursor of null with the flag
            // still true offers a load that early-returns "ignored".
            const atCap = mergedInput.length > RETAINED_ITEM_CAP;
            const nextCursor = atCap ? null : (result.nextCursor ?? null);
            // D18 B3 round 3/6: conversation.turns/olderCursor (the
            // ThreadModel fields sessionTokens reads) must stay in sync with
            // items/the store's own olderCursor, or a session with no
            // cumulative usage keeps summing only the first page after older
            // turns load. thread/turns/list is itself item-paginated, so an
            // older page can carry a fragment of a turn already in the
            // window; folding through the package's own mergeOlderItemPage
            // (turnsMatch/mergePageTurn) reconciles that by identity instead
            // of an id-only filter, which would drop the fragment or
            // double-count it under a different id.
            // Record page ownership by turn ID separately from pageOwnedIds
            // (item IDs) below — a turn survives here even
            // when every one of its display rows is deduped away or evicted.
            for (const turn of result.turnsPage?.data ?? []) pageOwnedTurnIds.add(turn.id);
            // Review rounds 1-2: inject identity skeletons for compact
            // turns whose remembered identities the page re-issues, so the
            // package's own turnsMatch/coalescing does the folding exactly
            // as the unbounded main would have. The retained copy stays the
            // newer merge input, so its usage still wins the fold.
            const pageIdentities = new Set<string>();
            for (const turn of result.turnsPage?.data ?? []) {
              for (const item of turn.items ?? []) {
                pageIdentities.add(item.transcriptKey ?? item.id);
                pageIdentities.add(item.id);
              }
            }
            const injectedPage = injectCompactedSkeletons(
              currentConv.turns,
              compactedTurnsCollidingWith(pageIdentities),
            );
            const mergeConv: MobileConversation =
              injectedPage.injected.length > 0
                ? { ...currentConv, turns: injectedPage.turns }
                : currentConv;
            const pageMerge = result.turnsPage
              ? mergeOlderItemPageWithFolds(mergeConv, result.turnsPage)
              : null;
            const mergedTurns = pageMerge ? pageMerge.model.turns : currentConv.turns;
            // The real sources for the strip pass: every item the retained
            // side held before the injection plus every item the page
            // carries — the page ones read off the merge's own hydrated
            // inputs, which is what the item membership refers to. An item
            // matching none of them by the package's rule can only be
            // remembered memory — a skeleton the merge left behind or a
            // fold of skeletons alone (review rounds 6-7) — never content,
            // unless the merge's item membership says a real source folded
            // into it (round 9). Skeletons carry the reducer's omitted-text
            // semantics, so a fold with a page item keeps the page's text
            // natively; there is nothing to repair post-merge.
            const pageRealSources = [
              ...currentConv.turns.flatMap((turn) => turn.items),
              ...(pageMerge?.olderTurns ?? []).flatMap((turn) => turn.items),
            ];
            const strippedPageTurns = stripInjectedSkeletons(
              mergedTurns,
              injectedPage.injected,
              pageRealSources,
              pageMerge?.folds.itemFoldSources,
            );
            // The compact turns are the merge's NEWER (retained) side here;
            // the retained-side folds name the carrier a bridged compact
            // turn's content landed in.
            transferFoldedCompactedEntries(strippedPageTurns, pageMerge?.folds.newerTurnFolds);
            // #1919 follow-up: bound the retained turn payloads against the
            // final retained rows (pageMerged), after the merge — the pass
            // prunes pageOwnedTurnIds with the same bound, moving a page turn
            // whose payloads left the keep-window to the compact set.
            const boundedTurns = boundRetainedTurns(strippedPageTurns, pageMerged);
            set({
              // conversation.olderCursor is the wire truth (result.nextCursor),
              // never the capped nextCursor above.
              // atCap only stops the STORE's own paging honestly (F8); it says
              // nothing about whether the daemon actually has more history, so
              // sessionTokens must not read it as "this is the whole session".
              conversation: { ...currentConv, items: merged, turns: boundedTurns, olderCursor: result.nextCursor },
              olderCursor: nextCursor,
              hasEarlierItems: atCap
                ? false
                : (result.hasEarlierItems ?? get().hasEarlierItems),
              hasLaterItems: result.hasLaterItems ?? get().hasLaterItems,
              loadingOlder: false,
            });
            const retainedIds = new Set(merged.map(timelineIdentity));
            return {
              status: "loaded",
              itemKeys: deduped
                .map(timelineIdentity)
                .filter((id) => retainedIds.has(id)),
            };
          }
          return { status: "ignored" };
        } catch (err) {
          // A stale v4 item cursor invalidates the visible transcript
          // incarnation. Rehydrate before surfacing the failure so the next
          // user retry starts from the refreshed bounded state and cursor.
          if (
            isStaleCursorError(err) &&
            opBinding.sink !== null &&
            isBindingCurrent(opBinding)
          ) {
            // Rehydrate only the operation's still-current binding. A stale
            // page from service A must not use service B's sink after a
            // rebind, even if both conversations share a ref.
            await get().rehydrate(
              opBinding.service,
              opBinding.sink,
            );
          }
          // C1: Recheck the exact operation binding after the await. If the
          // binding changed (rebind to B), A's completion makes ZERO state
          // changes — no loadingOlder/error.
          if (!isBindingCurrent(opBinding)) return { status: "ignored" };
          // I1: A current page failure always settles its own loadingOlder,
          // but writes error only if its captured error owner is unchanged;
          // a newer mutation/page error/clear/ABA survives.
          if (
            get().conversationGeneration === gen &&
            olderToken === loadOlderToken
          ) {
            // I1: always settle loadingOlder (this page owns it), but only
            // write error if the error-owner revision hasn't changed. A
            // newer clear-to-null or ABA-null owns error — revision equality
            // ONLY, no || currentError === null shortcut.
            if (entryErrorRev === errorOwnerRev) {
              set({
                loadingOlder: false,
                error: err instanceof Error ? err.message : String(err),
              });
            } else {
              // A newer error owner published (even to null) — preserve it,
              // only settle loadingOlder.
              set({ loadingOlder: false });
            }
            return { status: "failed" };
          }
          return { status: "ignored" };
        }
      },

      setDraft(text) {
        draftRevision += 1;
        set({ draft: text });
      },

      async send(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireControl(state.conversation, "send", "send");
        // C1: Capture a service-specific operation binding. If the supplied
        // service is wrong (A after B bound), zero request/state change.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return;
        const draftText = state.draft;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const revisionAtSubmit = draftRevision;
        const mutation: ConversationMutationState = {
          kind: "send",
          status: "pending",
          draftSnapshot: draftText,
          draftRevisionAtSubmit: revisionAtSubmit,
          generation: gen,
          mutationId,
        };
        // Clearing the draft via set() (not setDraft) does NOT increment
        // draftRevision — so any subsequent setDraft call (including
        // type-then-delete) increments the revision and is detected as an edit.
        // Fix round 1 I3: atomically clear prior error with new pending mutation.
        set({
          draft: "",
          pendingSend: "pending",
          pendingMutation: mutation,
          lastAcceptedMutation: null,
          error: null,
        });
        // I1: Capture the error-owner revision AFTER installing the pending
        // mutation + error-clear (the set wrapper incremented errorOwnerRev
        // for the error: null transition). A newer error owner that writes
        // during the await will increment errorOwnerRev past this captured
        // value, so this mutation's success/failure cannot clear or overwrite
        // it.
        const entryErrorRev = errorOwnerRev;
        try {
          const receipt = await service.send(input);
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          // F4: Check mutationId — out-of-order completion cannot clear a
          // newer mutation's state.
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: Clear pending fields unconditionally (this mutation owns
            // them), but clear error only if the error-owner revision is
            // unchanged — revision equality ONLY, no || error === null
            // shortcut. A newer clear-to-null or ABA-null owns error.
            if (entryErrorRev === errorOwnerRev) {
              set({
                pendingSend: null,
                pendingMutation: null,
                lastAcceptedMutation: { kind: "send", receipt },
                error: null,
              });
            } else {
              set({
                pendingSend: null,
                pendingMutation: null,
                lastAcceptedMutation: { kind: "send", receipt },
              });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          handleMutationError(
            err,
            state.ref,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestRehydrate,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async steer(service, input, expectedQueueRevision) {
        const state = get();
        if (state.conversation === null) return;
        requireControl(state.conversation, "steer", "steer");
        // C1: Capture a service-specific operation binding.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return;
        const draftText = state.draft;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const revisionAtSubmit = draftRevision;
        const mutation: ConversationMutationState = {
          kind: "steer",
          status: "pending",
          draftSnapshot: draftText,
          draftRevisionAtSubmit: revisionAtSubmit,
          generation: gen,
          mutationId,
        };
        // Steer/queue clear the draft on submit like send.
        // F10: any new mutation clears legacy pendingSend.
        // Fix round 1 I3: atomically clear prior error with new pending mutation.
        set({
          draft: "",
          pendingSend: null,
          pendingMutation: mutation,
          lastAcceptedMutation: null,
          error: null,
        });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          const receipt = await service.steer(input, expectedQueueRevision);
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({
                pendingMutation: null,
                lastAcceptedMutation: { kind: "steer", receipt },
                error: null,
              });
            } else {
              set({
                pendingMutation: null,
                lastAcceptedMutation: { kind: "steer", receipt },
              });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          handleMutationError(
            err,
            state.ref,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestRehydrate,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async queue(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireControl(state.conversation, "queue", "queue");
        // C1: Capture a service-specific operation binding.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return;
        const draftText = state.draft;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const revisionAtSubmit = draftRevision;
        const mutation: ConversationMutationState = {
          kind: "queue",
          status: "pending",
          draftSnapshot: draftText,
          draftRevisionAtSubmit: revisionAtSubmit,
          generation: gen,
          mutationId,
        };
        // F10: any new mutation clears legacy pendingSend.
        // Fix round 1 I3: atomically clear prior error with new pending mutation.
        set({
          draft: "",
          pendingSend: null,
          pendingMutation: mutation,
          lastAcceptedMutation: null,
          error: null,
        });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          const receipt = await service.queue(input);
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({
                pendingMutation: null,
                lastAcceptedMutation: { kind: "queue", receipt },
                error: null,
              });
            } else {
              set({
                pendingMutation: null,
                lastAcceptedMutation: { kind: "queue", receipt },
              });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          handleMutationError(
            err,
            state.ref,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestRehydrate,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async interrupt(service) {
        const state = get();
        if (state.conversation === null) return;
        requireControl(state.conversation, "stop", "interrupt");
        // C1: Capture a service-specific operation binding.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const revisionAtSubmit = draftRevision;
        const mutation: ConversationMutationState = {
          kind: "interrupt",
          status: "pending",
          // Interrupt does NOT snapshot the draft — it should remain as-is.
          draftSnapshot: null,
          draftRevisionAtSubmit: revisionAtSubmit,
          generation: gen,
          mutationId,
        };
        // Interrupt does NOT clear the draft.
        // F10: any new mutation clears legacy pendingSend.
        // Fix round 1 I3: atomically clear prior error with new pending mutation.
        set({
          pendingSend: null,
          pendingMutation: mutation,
          lastAcceptedMutation: null,
          error: null,
        });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          const receipt = await service.interrupt();
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({
                pendingMutation: null,
                lastAcceptedMutation: {
                  kind: "interrupt",
                  receipt,
                },
                error: null,
              });
            } else {
              set({
                pendingMutation: null,
                lastAcceptedMutation: {
                  kind: "interrupt",
                  receipt,
                },
              });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          handleMutationError(
            err,
            state.ref,
            mutationId,
            mutation,
            null,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestRehydrate,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      close() {
        suspendedService = null;
        // Increment generation so late frames from the closed conversation
        // cannot repopulate the store.
        ++conversationGen;
        // I1: increment the binding epoch and clear bindings so queued
        // requests from the closed conversation are suppressed.
        bindingEpoch += 1;
        // Fix round 1 I2: invalidate page ownership on conversation transition.
        loadOlderToken += 1;
        // C1+I1: clear any stale deferred trailing-reread request.
        trailingReread = null;
        pageOwnedIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        liveOwnedRevs.clear();
        truncatedItemIds.clear();
        // F4: reset the activity sink on close.
        if (activitySink !== null) {
          activitySink.reset();
          activitySink = null;
        }
        boundService = null;
        boundSink = null;
        set({
          status: "closed",
          conversation: null,
          ref: null,
          draft: "",
          pendingSend: null,
          pendingMutation: null,
          lastAcceptedMutation: null,
          olderCursor: null,
          loadingOlder: false,
          conversationGeneration: conversationGen,
        });
      },

      applyNotification(n) {
        const state = get();
        if (state.conversation === null) return;

        // The package reducer's routing: a frame names its thread by ref
        // when it carries one, else by threadId; a frame naming neither is
        // not about this thread. Silently drop everything else.
        if (!notificationTargetsThread(n, state.conversation)) return;

        // Dual-write until c-2b. Every frame about this thread folds into the
        // package reducer's model first — turns, pendingText and output, the
        // failure count, modelRetry — and the cases below then update today's
        // display rows from the wire frame, spreading that updated model. c-2b
        // flips the rows to projectConversation(model) and deletes the row
        // appliers: the same intermediate the web store had before its
        // projector. A row applier that bails (a missing target, a frozen row,
        // a reread request) still publishes the model half.
        const conv = applyThreadNotification(state.conversation, n);
        const publishModel = () => {
          if (conv !== state.conversation) set({ conversation: conv });
        };
        switch (n.method) {
          case "item/started":
          case "item/completed": {
            const params = n.params as { item: ThreadItem };
            const projectedRaw = projectSingleItem(
              params.item,
              conv.askPending,
              containingTurnStatus(conv, params.item),
            );
            const projected =
              projectedRaw === null
                ? null
                : decorateLifecycleItem(projectedRaw, params.item);
            // A sparse completion carries no text, so the accumulated output
            // must come from the row the event settles — which is a clustered
            // member whenever this item runs beside its neighbours. Resolve it
            // the way the replacement below resolves it: by canonical identity
            // first, so a member whose wire id changed while its transcriptKey
            // held keeps its output, then by wire id for a row that has no
            // transcriptKey to be found under.
            const eventIdentity = params.item.transcriptKey ?? params.item.id;
            const existing = (
              findActivityTargetByIdentity(conv.items, eventIdentity) ??
              findActivityTarget(conv.items, params.item.id)
            )?.activity;
            const preservesReasoningOutput =
              projected?.kind === "activity" &&
              projected.family === "reasoning" &&
              existing?.family === "reasoning" &&
              params.item.text === undefined;
            const projectedWithReasoning = preservesReasoningOutput
              ? {
                  ...projected,
                  detail: {
                    ...projected.detail,
                    output: existing.detail.output,
                  },
                }
              : projected;
            if (projectedWithReasoning !== null) {
              // Lifecycle events replace the whole source item, including any
              // companion attachment row — but an empty or absent input-image
              // list is unchanged, never a removal (mergeItemImages,
              // imagesToItemImagesForSession; closes #1656), so the
              // replacement below reads attachments from the reducer-folded
              // item (findFoldedItem/itemAttachments), which already carries
              // forward whatever images the fold kept.
              if (!preservesReasoningOutput) {
                truncatedItemIds.delete(timelineIdentity(projectedWithReasoning));
              }
              const replacement: MobileTimelineItem[] = [
                truncateAndRecordSingle(projectedWithReasoning),
              ];
              const attachmentId = `${params.item.id}:attachments`;
              const foldedItem = findFoldedItem(conv, params.item);
              const attachments = foldedItem
                ? itemAttachments(foldedItem)
                : projectItemAttachments(params.item);
              markLiveOwned(timelineIdentity(projectedWithReasoning));
              if (attachments) {
                // The companion row is built from wire images, not projected
                // here, so it takes the same per-item bound the authoritative
                // install paths apply (truncateItem) — src passes through.
                replacement.push(
                  truncateItem({
                    kind: "attachments",
                    id: attachmentId,
                    items: attachments,
                    ...(params.item.transcriptKey
                      ? { sourceTranscriptKey: params.item.transcriptKey }
                      : projectedWithReasoning.kind === "activity"
                        ? { sourceTranscriptKey: params.item.id }
                        : {}),
                  }),
                );
                markLiveOwned(attachmentId);
              }
              const items: MobileTimelineItem[] = [];
              let replaced = false;
              const consumedAttachments = new Set<string>();
              for (const item of conv.items) {
                if (consumedAttachments.has(item.id)) continue;
                if (
                  item.kind === "activity" &&
                  item.members &&
                  projectedWithReasoning.kind === "activity"
                ) {
                  const memberIndex = item.members.findIndex(
                    (member) =>
                      (member.transcriptKey ?? member.id) === eventIdentity,
                  );
                  if (memberIndex >= 0) {
                    const nextMember: ActivityMember = {
                      id: projectedWithReasoning.id,
                      label: projectedWithReasoning.label,
                      family: projectedWithReasoning.family,
                      state: projectedWithReasoning.state,
                      detail: projectedWithReasoning.detail,
                      ...(projectedWithReasoning.transcriptKey
                        ? { transcriptKey: projectedWithReasoning.transcriptKey }
                        : {}),
                      ...(projectedWithReasoning.position
                        ? { position: projectedWithReasoning.position }
                        : {}),
                    };
                    const segments = activityClusterSegments(item, memberIndex, nextMember);
                    // Rebuild companions beside their source segment. A
                    // later member update must not move its image ahead of
                    // earlier members' images or retain an obsolete image.
                    const attachments = new Map<string, MobileTimelineItem>();
                    const identities = new Set(
                      item.members.map((member) => member.transcriptKey ?? member.id),
                    );
                    for (const candidate of conv.items) {
                      const source = attachmentSourceIdentity(candidate);
                      if (source && identities.has(source)) {
                        consumedAttachments.add(candidate.id);
                        attachments.set(source, candidate);
                      }
                    }
                    attachments.delete(eventIdentity);
                    if (replacement[1]) attachments.set(eventIdentity, replacement[1]);
                    for (const segment of segments) {
                      items.push(segment);
                      if (segment.kind !== "activity") continue;
                      for (const member of segment.members ?? [segment]) {
                        const companion = attachments.get(member.transcriptKey ?? member.id);
                        if (companion) items.push(companion);
                      }
                    }

                    replaced = true;
                    continue;
                  }
                }
                const itemIdentity = timelineIdentity(item);
                const attachmentIdentity = attachmentSourceIdentity(item);
                if (
                  itemIdentity === eventIdentity ||
                  attachmentIdentity === eventIdentity ||
                  item.id === attachmentId
                ) {
                  if (!replaced) items.push(...replacement);
                  replaced = true;
                } else {
                  items.push(item);
                }
              }
              if (!replaced) items.push(...replacement);
              const cappedItems = capItems(items);
              pruneEvictedIds(cappedItems);
              // #1919 follow-up: live growth evicts rows too — bound the
              // retained turn payloads whenever a live path caps display
              // rows, not only at page/rehydrate publishes.
              set({
                conversation: {
                  ...conv,
                  items: cappedItems,
                  turns: boundRetainedTurns(conv.turns, cappedItems),
                },
              });
            } else {
              publishModel();
              // Unsupported transitions require the canonical projection.
              if (state.ref !== null) requestRehydrate(state.ref);
            }
            break;
          }

          case "item/agentMessage/delta": {
            const params = n.params as { itemId: string; delta: string };
            const existing = conv.items.find(
              (i) => i.id === params.itemId && i.kind === "assistant",
            );
            if (existing) {
              // F12: Per-item truncation ownership — once an item is
              // truncated, later deltas cannot append. Tracked by item ID,
              // not by text suffix, so genuine content ending with the
              // marker doesn't freeze.
              if (truncatedItemIds.has(timelineIdentity(existing))) {
                publishModel();
                break;
              }
              const combined =
                (existing.kind === "assistant" ? existing.markdown : "") +
                params.delta;
              const truncated = truncateText(combined, MAX_ITEM_BYTES);
              if (truncated !== combined) {
                truncatedItemIds.add(timelineIdentity(existing));
              }
              // Fix round 1: Mark as live-owned — accepted delta update.
              markLiveOwned(timelineIdentity(existing));
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((item) =>
                    item.kind === "assistant" && item.id === params.itemId
                      ? { ...item, markdown: truncated }
                      : item,
                  ),
                },
              });
            } else {
              // Delta targeting missing item — trigger resync.
              publishModel();
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
            }
            break;
          }

          case "item/agentMessage/reset": {
            const params = n.params as { itemId: string };
            const existing = conv.items.find(
              (i) => i.id === params.itemId && i.kind === "assistant",
            );
            if (existing) {
              // Task 2A-Truncation: explicitly unfreeze the ID before the empty
              // reset so a later delta applies. The reset clears the markdown
              // to "" (short content), so the item must no longer be frozen.
              truncatedItemIds.delete(timelineIdentity(existing));
              // Fix round 1: Mark as live-owned — accepted reset update.
              markLiveOwned(timelineIdentity(existing));
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((item) =>
                    item.kind === "assistant" && item.id === params.itemId
                      ? { ...item, markdown: "" }
                      : item,
                  ),
                },
              });
            } else {
              publishModel();
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
            }
            break;
          }

          case "item/reasoning/summaryTextDelta": {
            const params = n.params as { itemId: string; delta: string };
            const target = findActivityTarget(conv.items, params.itemId);
            // Task 2A-Family: exact delta family from required item.family
            // (never label inference). Reasoning delta mutates only family=
            // reasoning. Missing target, wrong family, or unknown family =>
            // no mutation/live revision/freeze change, request authoritative
            // reread.
            if (target === null || target.activity.family !== "reasoning") {
              publishModel();
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            const identity = activityIdentity(target.activity);
            // F12: Per-item truncation ownership — frozen guard.
            if (truncatedItemIds.has(identity)) {
              publishModel();
              break;
            }
            const combined =
              (target.activity.detail.output ?? "") + params.delta;
            const truncated = truncateText(combined, MAX_ITEM_BYTES);
            if (truncated !== combined) {
              truncatedItemIds.add(identity);
            }
            // Mark live revision only on accepted exact update.
            markLiveOwned(identity);
            set({
              conversation: {
                ...conv,
                items: replaceActivityTargetDetail(conv.items, target, {
                  ...target.activity.detail,
                  output: truncated,
                }),
              },
            });
            break;
          }

          case "item/toolOutput/delta": {
            const params = n.params as {
              itemId: string;
              callId: string;
              delta: string;
            };
            const target = findActivityTarget(conv.items, params.itemId);
            // Task 2A-Family: exact delta family from required item.family
            // (never label inference). Tool-output delta mutates only family=
            // tool AND requires stored detail.callId and incoming params.callId
            // both present strings and exactly equal. Missing target, missing
            // either callId, mismatch, unknown family, or wrong family => no
            // mutation/live revision/freeze change, request authoritative
            // reread.
            if (target === null) {
              publishModel();
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            if (target.activity.family !== "tool") {
              // Wrong family or unknown family — not a tool item.
              publishModel();
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            {
              const itemCallId = target.activity.detail.callId;
              if (
                typeof itemCallId !== "string" ||
                typeof params.callId !== "string" ||
                itemCallId !== params.callId
              ) {
                // Missing either callId, or mismatch — no mutation.
                publishModel();
                if (state.ref !== null) {
                  requestRehydrate(state.ref);
                }
                break;
              }
            }
            const identity = activityIdentity(target.activity);
            // F12: Per-item truncation ownership — frozen guard.
            if (truncatedItemIds.has(identity)) {
              publishModel();
              break;
            }
            const combined =
              (target.activity.detail.output ?? "") + params.delta;
            const truncated = truncateText(combined, MAX_ITEM_BYTES);
            if (truncated !== combined) {
              truncatedItemIds.add(identity);
            }
            // Mark live revision only on accepted exact update.
            markLiveOwned(identity);
            set({
              conversation: {
                ...conv,
                items: replaceActivityTargetDetail(conv.items, target, {
                  ...target.activity.detail,
                  output: truncated,
                }),
              },
            });
            break;
          }

          case "warning": {
            // The reducer's own "warning" fold (applyThreadNotification,
            // above) computes this from n.params too — foldWarningParams is
            // a pure function of params alone, so calling it here again
            // gives the exact value the reducer stored on the model when
            // there was an active turn to store it on, without reading that
            // value back off the model. When there's no active turn the
            // reducer drops the frame (nowhere wire-true to put it), so
            // this row is the only place it folds through either way.
            const folded = foldWarningParams(n.params);
            const title = folded.title ?? "Warning";
            // Compose every non-blank part rather than picking one with ||:
            // a warning carrying both a message and a hint shows both, the
            // same as the web and TUI renderers. title stays its own field
            // here, matching the canonical projector's row
            // (project.ts's warningItem), which also carries title as its own
            // field; both join message+hint into this same detail string.
            const detail = joinWarningParts([folded.text, folded.hint]);
            // Share the canonical row's identity. The reducer's warning fold
            // (applyThreadNotification, above) has already appended this
            // frame's warning item to the active turn as `conv` was built, so
            // the item it just created is that turn's last warning item, and
            // projectConversation's warningItem (project.ts) keys the
            // canonical row on that same item.id. Distinct ids would leave a
            // reread seeing this live-owned row as an omitted tail beside the
            // canonical row — the warning shown twice, and an identity remount
            // for good measure. (A frame the reducer dropped — no active turn —
            // has no model item and no canonical row either, so it takes the
            // synthetic serial id below.)
            const activeTurn = conv.turns.find(
              (turn) => turn.id === conv.activeTurnId,
            );
            const modelWarning = activeTurn?.items
              .filter((item) => item.type === "warning")
              .at(-1);
            // The model's warning ids are per-turn counts
            // (`item_warning_live_<turn>_<count>`), and warnings are not
            // transcript-persisted: a reread drops the model's warning items
            // while this live-owned row stays, so the next warning in the same
            // turn is handed the count-0 id again. Adopting it a second time
            // would put two rows under one id (and one identity) — so only
            // take the model id while no retained row already holds it, and
            // fall back to the unique serial otherwise. The serial is unique;
            // embedding the title (as an earlier round did) merely bloated
            // this id, and the model id never carries it either.
            const canonicalId = modelWarning?.id;
            const id =
              canonicalId !== undefined &&
              !liveOwnedRevs.has(canonicalId) &&
              !conv.items.some(
                (row) => timelineIdentity(row) === canonicalId,
              )
                ? canonicalId
                : `warning:${++liveNoticeSerial}`;
            const failureItem: MobileTimelineItem = {
              kind: "failure",
              id,
              title,
              detail,
            };
            // Residual 2: Mark as live-owned — created by an actual live
            // notification.
            markLiveOwned(id);
            const warningCappedItems = capItems([...conv.items, failureItem]);
            // Task 2A-Truncation residual fix round 2: prune evicted IDs
            // from ownership maps after incremental append+cap.
            pruneEvictedIds(warningCappedItems);
            // #1919 follow-up: a warning append can cap-evict rows — bound
            // the retained turn payloads here too.
            set({
              conversation: {
                ...conv,
                items: warningCappedItems,
                turns: boundRetainedTurns(conv.turns, warningCappedItems),
              },
            });
            break;
          }

          // evener/thread/resync triggers a coalesced rehydrate via the store-owned
          // drain scheduler. The store does not re-read on its own.
          case "evener/thread/resync": {
            publishModel();
            if (state.ref !== null) {
              requestRehydrate(state.ref);
            }
            break;
          }

          default: {
            // Everything without a row applier above is the reducer's alone.
            const askPendingMoved =
              conv.askPending !== state.conversation.askPending;
            if (askPendingMoved && boundSink === null) {
              // A compatibility open() binds no activity sink, so the
              // rehydrate the sink-bound path takes below is a no-op here —
              // yet the sheet still moves (pendingQuestions reads the model
              // through liveAsksFor). Reconcile only the asks whose
              // rendering moved, against the folded model's canonical
              // projection (F6 — question rows come only from the canonical
              // projection). A settled ask may live in a tool cluster and a
              // raised one may have been a cluster member, so each moved
              // ask re-renders the contiguous row window around it —
              // clusters split and merge exactly as projectTimeline builds
              // them — while every row outside those windows passes through
              // verbatim. Live-owned rows that exist only in items (a
              // no-active-turn warning the reducer never folds into the
              // model) survive, where a whole-array reprojection would
              // silently drop them, and a verbatim row's truncation freeze
              // is carried through (carriedFrozen) so a later delta cannot
              // append after its marker, while the canonical replacements
              // are judged fresh. The result still goes through the same
              // treatment open() installs (cap → reconcile truncation
              // ownership → truncate) plus the ownership prune a live
              // merge does. Bounded to the askPending move: a frame that
              // does not move askPending does not touch items at all — no
              // storm, and no read required.
              const canonical = projectTimeline(conv);
              const canonicalCallIds = new Set<string>();
              for (const crow of canonical) {
                if (crow.kind !== "question") continue;
                const callId = crow.questions[0]?.callId;
                if (callId !== undefined) canonicalCallIds.add(callId);
              }
              // The asks whose rendering moved: stale question rows that
              // resolved (or left the model), and canonical question rows
              // with no still-live row of their own.
              const movedAskIds = new Set<string>();
              const keptCallIds = new Set<string>();
              for (const row of conv.items) {
                if (row.kind !== "question") continue;
                const callId = row.questions[0]?.callId;
                if (callId !== undefined && canonicalCallIds.has(callId)) {
                  keptCallIds.add(callId);
                } else {
                  movedAskIds.add(row.id);
                }
              }
              for (const crow of canonical) {
                if (crow.kind !== "question") continue;
                const callId = crow.questions[0]?.callId;
                if (callId === undefined || !keptCallIds.has(callId)) {
                  movedAskIds.add(crow.id);
                }
              }
              if (movedAskIds.size > 0) {
                // Every form under which a row's model item can appear:
                // its wire id, its timeline identity (the transcript key
                // when the wire item carried one), each clustered
                // member's id and identity, and an attachment row's
                // source. Region and segment matching work on these
                // aliases because the two sides disagree on the form — a
                // live-built row carries the wire transcript key while a
                // canonical row may know the item only under its id, and
                // a stale question row may sit under the key its settled
                // replacement will never use.
                const rowAliases = (row: MobileTimelineItem): Set<string> => {
                  const aliases = new Set<string>([
                    row.id,
                    timelineIdentity(row),
                  ]);
                  if (row.kind === "activity" && row.members) {
                    for (const member of row.members) {
                      aliases.add(member.id);
                      aliases.add(activityIdentity(member));
                    }
                  }
                  const source = attachmentSourceIdentity(row);
                  if (source !== null) aliases.add(source);
                  return aliases;
                };
                // The identities of one moved ask's neighborhood: seeded
                // with the ask item and closed over the rows' aliases and
                // cluster membership on both sides, so the window covers
                // exactly the rows the move can re-cluster. A live-owned
                // warning row shares no alias with the neighborhood and
                // stays outside.
                const regionOf = (seed: string): Set<string> => {
                  const region = new Set([seed]);
                  const absorb = (
                    rows: MobileTimelineItem[],
                  ): boolean => {
                    let grew = false;
                    for (const row of rows) {
                      let hit = false;
                      for (const id of rowAliases(row)) {
                        if (region.has(id)) {
                          hit = true;
                          break;
                        }
                      }
                      if (!hit) continue;
                      for (const id of rowAliases(row)) {
                        const before = region.size;
                        region.add(id);
                        if (region.size !== before) grew = true;
                      }
                    }
                    return grew;
                  };
                  let grew = true;
                  while (grew) {
                    const grewItems = absorb(conv.items);
                    const grewCanonical = absorb(canonical);
                    grew = grewItems || grewCanonical;
                  }
                  return region;
                };
                type RegionWindow = {
                  start: number;
                  end: number;
                  region: Set<string>;
                };
                const windows: RegionWindow[] = [];
                const insertions: Array<{
                  rows: MobileTimelineItem[];
                  after: number;
                }> = [];
                for (const askId of movedAskIds) {
                  const region = regionOf(askId);
                  const inRegion = (row: MobileTimelineItem): boolean => {
                    for (const id of rowAliases(row)) {
                      if (region.has(id)) return true;
                    }
                    return false;
                  };
                  const itemIdx: number[] = [];
                  conv.items.forEach((row, idx) => {
                    if (inRegion(row)) itemIdx.push(idx);
                  });
                  if (itemIdx.length === 0) {
                    // The ask has no row at all (a newly pending ask the
                    // row appliers cannot build — F6): insert its
                    // canonical rows positionally below.
                    const rows: MobileTimelineItem[] = [];
                    let after = -1;
                    canonical.forEach((crow, idx) => {
                      if (inRegion(crow)) {
                        rows.push(crow);
                        after = idx;
                      }
                    });
                    if (rows.length > 0) insertions.push({ rows, after });
                    continue;
                  }
                  windows.push({
                    start: Math.min(...itemIdx),
                    end: Math.max(...itemIdx),
                    region,
                  });
                }
                windows.sort((a, b) => a.start - b.start);
                const mergedWindows: RegionWindow[] = [];
                for (const w of windows) {
                  const last = mergedWindows[mergedWindows.length - 1];
                  if (last !== undefined && w.start <= last.end + 1) {
                    for (const id of w.region) last.region.add(id);
                    last.end = Math.max(last.end, w.end);
                  } else {
                    mergedWindows.push({
                      start: w.start,
                      end: w.end,
                      region: new Set(w.region),
                    });
                  }
                }
                const rebuilt: MobileTimelineItem[] = [];
                const carriedFrozen = new Set<string>();
                // Verbatim rows — outside windows, live rows inside them —
                // carry their (and their members') existing truncation
                // freeze through the reconciliation; canonical
                // replacements are judged fresh from their raw content.
                const carryFreeze = (row: MobileTimelineItem): void => {
                  for (const id of ownTimelineIdentities(row)) {
                    if (truncatedItemIds.has(id)) carriedFrozen.add(id);
                  }
                };
                let windowIdx = 0;
                for (let idx = 0; idx < conv.items.length; idx++) {
                  const w = mergedWindows[windowIdx];
                  if (w !== undefined && w.start === idx) {
                    // Walk the window in display order: rows the model
                    // still backs (a region alias, or a canonical
                    // counterpart) group into segments, and a live-only
                    // row — one the canonical projection cannot re-render,
                    // like a no-active-turn warning the reducer never
                    // folds (#2037) — is preserved verbatim and splits the
                    // window: the canonical replacement re-clusters each
                    // side instead of merging across it.
                    type Piece =
                      | { kind: "segment"; identities: Set<string> }
                      | { kind: "live"; row: MobileTimelineItem };
                    const pieces: Piece[] = [];
                    for (let wi = w.start; wi <= w.end; wi++) {
                      const row = conv.items[wi];
                      const inWindowRegion = [...rowAliases(row)].some(
                        (id) => w.region.has(id),
                      );
                      const hasCounterpart = canonical.some(
                        (crow) =>
                          timelineIdentity(crow) === timelineIdentity(row),
                      );
                      if (!inWindowRegion && !hasCounterpart) {
                        pieces.push({ kind: "live", row });
                        continue;
                      }
                      let segment:
                        | Extract<Piece, { kind: "segment" }>
                        | undefined =
                        pieces.length > 0 &&
                        pieces[pieces.length - 1].kind === "segment"
                          ? (pieces[
                              pieces.length - 1
                            ] as Extract<Piece, { kind: "segment" }>)
                          : undefined;
                      if (segment === undefined) {
                        segment = {
                          kind: "segment",
                          identities: new Set<string>(),
                        };
                        pieces.push(segment);
                      }
                      for (const id of rowAliases(row)) {
                        segment.identities.add(id);
                      }
                    }
                    const segments = pieces.filter(
                      (p): p is Extract<Piece, { kind: "segment" }> =>
                        p.kind === "segment",
                    );
                    for (const piece of pieces) {
                      if (piece.kind === "live") {
                        carryFreeze(piece.row);
                        rebuilt.push(piece.row);
                        continue;
                      }
                      for (const crow of canonical) {
                        const crowAliases = rowAliases(crow);
                        if (
                          ![...crowAliases].some((id) =>
                            piece.identities.has(id),
                          )
                        ) {
                          continue;
                        }
                        if (crow.kind === "activity" && crow.members) {
                          const spans = segments.some(
                            (s) =>
                              s !== piece &&
                              [...crowAliases].some((id) =>
                                s.identities.has(id),
                              ),
                          );
                          if (spans) {
                            // A cluster spanning a live boundary splits:
                            // this segment re-clusters the members it
                            // owns.
                            const mine = crow.members.filter(
                              (m) =>
                                piece.identities.has(m.id) ||
                                piece.identities.has(activityIdentity(m)),
                            );
                            rebuilt.push(
                              ...clusterActivities(
                                mine.map((m) => ({
                                  family: m.family,
                                  item: {
                                    kind: "activity" as const,
                                    id: m.id,
                                    label: m.label,
                                    family: m.family,
                                    state: m.state,
                                    detail: m.detail,
                                    ...(m.transcriptKey
                                      ? { transcriptKey: m.transcriptKey }
                                      : {}),
                                    ...(m.position
                                      ? { position: m.position }
                                      : {}),
                                  },
                                })),
                              ),
                            );
                          } else {
                            rebuilt.push(crow);
                          }
                        } else {
                          // Emit once — in the segment that owns the
                          // row's own identity, else the first one it
                          // intersects.
                          const ownId = timelineIdentity(crow);
                          const owner = segments.find((s) =>
                            s.identities.has(ownId),
                          );
                          const firstMatch = segments.find((s) =>
                            [...crowAliases].some((id) =>
                              s.identities.has(id),
                            ),
                          );
                          if ((owner ?? firstMatch ?? piece) === piece) {
                            rebuilt.push(crow);
                          }
                        }
                      }
                    }
                    idx = w.end;
                    windowIdx++;
                    continue;
                  }
                  carryFreeze(conv.items[idx]);
                  rebuilt.push(conv.items[idx]);
                }
                insertions.sort((a, b) => a.after - b.after);
                for (const ins of insertions) {
                  const laterIdentities = new Set<string>();
                  for (let c = ins.after + 1; c < canonical.length; c++) {
                    for (const id of rowAliases(canonical[c])) {
                      laterIdentities.add(id);
                    }
                  }
                  let insertAt = -1;
                  for (let k = 0; k < rebuilt.length; k++) {
                    const row = rebuilt[k];
                    let hit = laterIdentities.has(row.id);
                    if (!hit) {
                      for (const id of rowAliases(row)) {
                        if (laterIdentities.has(id)) {
                          hit = true;
                          break;
                        }
                      }
                    }
                    if (hit) {
                      insertAt = k;
                      break;
                    }
                  }
                  if (insertAt === -1) rebuilt.push(...ins.rows);
                  else rebuilt.splice(insertAt, 0, ...ins.rows);
                }
                const reprojectedCapped = capItems(rebuilt);
                reconcileTruncationFrom(reprojectedCapped, carriedFrozen);
                const reprojected = truncateAndRecord(reprojectedCapped);
                pruneEvictedIds(reprojected);
                // #1919 follow-up: a reproject that caps can evict rows —
                // bound the retained turn payloads here too.
                set({
                  conversation: {
                    ...conv,
                    items: reprojected,
                    turns: boundRetainedTurns(conv.turns, reprojectedCapped),
                  },
                });
                break;
              }
              // askPending moved but no ask row moved either way (no
              // question rows in items or in the canonical projection):
              // fall through to the model-only publish — items untouched.
            }
            publishModel();
            // An item/* transition the cases above do not handle needs the
            // canonical projection; only those resync, not every unknown
            // family, to avoid reread storms from unrelated notifications.
            // An askPending change needs it for the same reason and one
            // more: question rows come only from the canonical projection
            // (F6 — ask_user is never single-item projected), while the
            // sheet reads the model directly (questionAnswers.ts's
            // pendingQuestions, through liveAsksFor), so a model-only flip
            // would otherwise leave the sheet and the timeline disagreeing —
            // a stale question row beside an empty sheet, or a pending ask
            // with no row. askPending rides every thread/status/changed
            // frame but only moves when an ask raises or resolves, so
            // resyncing on the change cannot storm.
            if (
              state.ref !== null &&
              (n.method.startsWith("item/") || askPendingMoved)
            ) {
              requestRehydrate(state.ref);
            }
            break;
          }
        }
      },

      reset() {
        suspendedService = null;
        // Invalidate the current conversation generation so late frames are
        // rejected, then return to idle.
        ++conversationGen;
        // I1: increment the binding epoch and clear bindings so queued
        // requests from the prior conversation are suppressed.
        bindingEpoch += 1;
        // Fix round 1 I2: invalidate page ownership on conversation transition.
        loadOlderToken += 1;
        // C1+I1: clear any stale deferred trailing-reread request.
        trailingReread = null;
        pageOwnedIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        liveOwnedRevs.clear();
        truncatedItemIds.clear();
        // F4: reset the activity sink on thread change.
        if (activitySink !== null) {
          activitySink.reset();
          activitySink = null;
        }
        boundService = null;
        boundSink = null;
        set({
          ref: null,
          profileId: null,
          conversation: null,
          olderCursor: null,
          loadingOlder: false,
          status: "idle",
          error: null,
          draft: "",
          pendingSend: null,
          pendingMutation: null,
          lastAcceptedMutation: null,
          conversationGeneration: conversationGen,
        });
      },

      // Plan3 host projection: fresh snapshot of the private truncatedItemIds.
      // Returns a new Set so the caller cannot mutate internal ownership.
      getTruncatedItemIds() {
        return new Set(truncatedItemIds);
      },

      // Task3: store-owned external error publication seam. Writes the generic
      // sanitized message as the conversation error ONLY when the current ref
      // and conversationGeneration exactly match the expected values the
      // dispatcher captured. Any mismatch — a profile/thread switch, a reset,
      // or an open transition — makes zero set calls: the full state object
      // reference is unchanged and no subscriber fires. The write goes through
      // the wrapped set so errorOwnerRev advances; a subsequent rehydrate that
      // captured the older revision must then preserve this newer external
      // error rather than clearing it. expectedRef null matches a null current
      // ref (e.g. an error published against the idle store) but never matches
      // an active conversation's non-null ref.
      publishExternalError(message, expectedRef, expectedGeneration) {
        const state = get();
        if (
          state.ref !== expectedRef ||
          state.conversationGeneration !== expectedGeneration
        ) {
          return;
        }
        set({ error: message });
      },
    };
  });
}

// Shared mutation error handler. On failure, the failed mutation state
// PERSISTS (not cleared to null). The draft is only restored if the user has
// not edited since the mutation cleared the draft — detected via
// draftRevision, so type-then-delete (which produces "" but increments the
// revision) counts as an edit and prevents restore. F4/F10: uses mutationId
// (not generation alone) so out-of-order failure cannot overwrite a newer
// mutation's error.
function handleMutationError(
  err: unknown,
  ref: string | null,
  mutationId: number,
  mutation: ConversationMutationState,
  draftSnapshot: string | null,
  revisionAtSubmit: number,
  entryErrorRev: number,
  getDraftRevision: () => number,
  getErrorOwnerRev: () => number,
  set: (partial: Partial<ConversationState>) => void,
  get: () => ConversationState,
  requestRehydrate: (ref: string) => void,
): void {
  // F10: Check active mutation first. If a newer mutation has already
  // started, this error is stale — bail out.
  if (get().pendingMutation?.mutationId !== mutationId) return;

  // The hub refused because its capabilities moved on without a status frame
  // this client saw. The web's rule (decision 2): surface the typed error and
  // let the model converge through the reducer — one coalesced reread of the
  // authoritative snapshot — rather than a bespoke capability read and write.
  if (isActionUnavailableError(err) && ref !== null) requestRehydrate(ref);

  // F10: Re-check mutationId — out-of-order failure cannot change a newer
  // mutation, error, or draft.
  if (get().pendingMutation?.mutationId === mutationId) {
    // The failed mutation state PERSISTS — do NOT clear pendingMutation.
    // Task 2A-Ops-4: Draft revision prevents type-delete restore — only
    // restore if the draft revision has NOT changed since the mutation
    // cleared the draft. Type-then-delete produces "" but increments the
    // revision, so it counts as an edit and prevents restore.
    // I1: A current mutation failure may install failed pending/draft
    // restoration, but must NOT overwrite a newer error owner. Only write
    // error if the captured error-owner revision is unchanged — revision
    // equality ONLY, no || currentError === null shortcut. Revision
    // equality preserves ANY newer error owner including a clear-to-null
    // (which increments errorOwnerRev but writes null) and an ABA same
    // text/null re-set. A stale rehydrate that tries to CLEAR a newer
    // mutation's error is handled in the rehydrate success path via
    // entryErrorRev comparison. Here, the mutation failure owns the error
    // write only when no newer owner (null or non-null) advanced the
    // revision during the await.
    const shouldRestore =
      draftSnapshot !== null && getDraftRevision() === revisionAtSubmit;
    const newError = err instanceof Error ? err.message : String(err);
    if (entryErrorRev === getErrorOwnerRev()) {
      set({
        pendingMutation: { ...mutation, status: "failed" },
        pendingSend: null,
        ...(shouldRestore ? { draft: draftSnapshot } : {}),
        error: newError,
      });
    } else {
      // A newer error owner published (non-null error, null clear, or ABA
      // same text/null re-set) — preserve it. Only install the failed
      // pending/draft restoration.
      set({
        pendingMutation: { ...mutation, status: "failed" },
        pendingSend: null,
        ...(shouldRestore ? { draft: draftSnapshot } : {}),
      });
    }
  }
}
