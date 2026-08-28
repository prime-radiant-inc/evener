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
import { WireError } from "../../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  AnyNotification,
  InputItem,
  ThreadCapabilities,
  ThreadItem,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  ActivityState,
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
  MobileUsage,
} from "../conversation/model";
import type { ActivityView } from "../services/activity";
import type {
  ConversationService,
  LiveConversationService,
} from "../services/conversation";
import type { ActivityIdentity, NotificationOutcome } from "./activity";

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
  // I2: Narrow strict sink for a capabilities-only refresh. Updates only the
  // capabilities for the exact current identity and an open view; returns
  // false on stale/missing/wrong identity. The conversation cap-refresh writer
  // calls this independently of the notification stream, publishing refreshed
  // caps to the bound activity sink BEFORE committing conversation caps.
  setLiveCapabilities(
    capabilities: ThreadCapabilities,
    identity: ActivityIdentity,
  ): boolean;
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
// scheduler). The cap-refresh effect (requestCapabilityRefresh) calls
// liveService.refreshCapabilities() directly (not through the scheduler).
// handleMutationError awaits scheduler.request() OUTSIDE any effect — it is
// called from the send/steer/queue/interrupt action body, not from within a
// scheduler effect. This invariant is confirmed by the production call graph:
//   requestRehydrate effect -> rehydrate() -> readProjection() [no scheduler]
//   requestCapabilityRefresh effect -> refreshCapabilities() [no scheduler]
//   handleMutationError -> scheduler.request() [awaited in action body, not effect]
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

// --- limits and truncation helpers (centralized) ----------------------------

export const MAX_ITEM_BYTES = 64 * 1024; // 64 KiB in UTF-8 bytes
export const TRUNCATION_MARKER = "… truncated";
export const RETAINED_ITEM_CAP = 500;

// Truncate a string to maxBytes in UTF-8 + marker, ending with "… truncated"
// exactly once. Iterates Unicode scalar values (not UTF-16 code units) so
// no surrogate pairs are split and no U+FFFD replacement chars are produced.
const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

export function truncateText(text: string, maxBytes: number): string {
  const encoded = textEncoder.encode(text);
  if (encoded.length <= maxBytes) return text;
  const targetBytes = maxBytes - markerBytes.length;
  // Iterate code points (for...of iterates Unicode scalar values) to find
  // the longest prefix whose UTF-8 encoding fits within targetBytes. This
  // avoids splitting surrogate pairs and never produces U+FFFD.
  let byteLen = 0;
  let cutIdx = 0;
  for (const cp of text) {
    const cpBytes = textEncoder.encode(cp).length;
    if (byteLen + cpBytes > targetBytes) break;
    byteLen += cpBytes;
    cutIdx += cp.length;
  }
  // Trim code points until the result + marker fits within maxBytes.
  // (May need to trim if a multibyte code point straddles the boundary.)
  let truncated = text.slice(0, cutIdx);
  let truncatedBytes = textEncoder.encode(truncated);
  while (
    truncatedBytes.length + markerBytes.length > maxBytes &&
    truncated.length > 0
  ) {
    // Remove one code point (may be 2 UTF-16 units for surrogate pairs).
    const codePoints = [...truncated];
    codePoints.pop();
    truncated = codePoints.join("");
    truncatedBytes = textEncoder.encode(truncated);
  }
  return truncated + TRUNCATION_MARKER;
}

// Check if text exceeds the byte limit (for setting truncated flag in projections).
export function exceedsByteLimit(text: string, maxBytes: number): boolean {
  return textEncoder.encode(text).length > maxBytes;
}

// F12: Per-item truncation ownership. Instead of checking if the text ends
// with the marker (which would freeze if genuine content ends with "…
// truncated"), the store tracks which item IDs have been truncated in a
// private set. This allows genuine marker suffixes in content without
// freezing delta appends.

// Apply truncation to an item's text-bearing fields (arguments, output, error,
// markdown). Returns a new item with truncated fields.
function truncateItem(item: MobileTimelineItem): MobileTimelineItem {
  switch (item.kind) {
    case "assistant":
      return { ...item, markdown: truncateText(item.markdown, MAX_ITEM_BYTES) };
    case "activity":
      return {
        ...item,
        detail: {
          ...item.detail,
          arguments: item.detail.arguments
            ? truncateText(item.detail.arguments, MAX_ITEM_BYTES)
            : item.detail.arguments,
          output: item.detail.output
            ? truncateText(item.detail.output, MAX_ITEM_BYTES)
            : item.detail.output,
          error: item.detail.error
            ? truncateText(item.detail.error, MAX_ITEM_BYTES)
            : item.detail.error,
        },
      };
    default:
      return item;
  }
}

// Enforce the 500-item retained cap. Always retains the NEWEST items (end
// of array) so the live tail is preserved for interactive scrolling.
function capItems(items: MobileTimelineItem[]): MobileTimelineItem[] {
  if (items.length <= RETAINED_ITEM_CAP) return items;
  return items.slice(items.length - RETAINED_ITEM_CAP);
}

export interface ConversationState {
  readonly ref: string | null;
  readonly profileId: string | null;
  readonly connectionGeneration: number;
  readonly conversationGeneration: number;

  readonly conversation: MobileConversation | null;
  readonly olderCursor: string | null;
  readonly loadingOlder: boolean;
  readonly status: ConversationStatus;
  readonly error: string | null;

  readonly draft: string;
  readonly pendingSend: string | null;
  readonly pendingMutation?: ConversationMutationState | null;

  open(service: ConversationService, ref: string): Promise<void>;
  loadOlder(service: ConversationService): Promise<void>;
  setDraft(text: string): void;
  send(service: ConversationService, input: InputItem[]): Promise<void>;
  steer(service: ConversationService, input: InputItem[]): Promise<void>;
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

// Extract threadId/ref from a notification's params, returning null if the
// notification has neither (some global notifications don't).
function notificationRef(
  n: AnyNotification,
): { threadId?: string; ref?: string } | null {
  const params = n.params as Record<string, unknown> | undefined;
  if (params === undefined || params === null) return null;
  const threadId =
    typeof params.threadId === "string" ? params.threadId : undefined;
  const ref = typeof params.ref === "string" ? params.ref : undefined;
  if (threadId === undefined && ref === undefined) return null;
  return { threadId, ref };
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

// Check a capability on the current conversation and throw if false. This
// mirrors the service's requireCap but runs in the store so the fake
// service (which has no capability gating) still respects capabilities.
function requireCap(
  conv: MobileConversation,
  cap: keyof MobileCapabilities,
  action: string,
): void {
  if (!conv.capabilities[cap]) {
    throw new Error(`Action "${action}" is not available for this thread`);
  }
}

function commandActivityState(item: ThreadItem): ActivityState {
  if (item.error !== undefined && item.error !== "") return "failed";
  if (item.status === "inProgress") return "running";
  return "completed";
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
): MobileTimelineItem | null {
  if (item.type === "userMessage") {
    // Task 2A-Ops-5: if there's a pending ask_user, a user message is the
    // answer lifecycle — trigger an authoritative reread to settle the
    // pending question state according to canonical projection.
    if (askPending) return null;
    return { kind: "user", id: item.id, text: item.text ?? "" };
  }
  if (item.type === "agentMessage") {
    return {
      kind: "assistant",
      id: item.id,
      markdown: `${item.text ?? ""}${item.delta ?? ""}`,
      streaming: item.status === "inProgress",
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
      state: commandActivityState(item),
      detail: {
        arguments: item.argumentsJson,
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
      state: item.status === "inProgress" ? "running" : "completed",
      detail: { output: item.text },
    };
  }
  // Unknown item types — return null to signal an unsupported transition
  // that should trigger a coalesced rehydrate.
  return null;
}

// Known notification methods that we handle explicitly. The default branch
// only resyncs for unsupported item/* transitions, not for all unknown
// notifications, to avoid reread storms from unrelated notification families.
const ITEM_NOTIFICATION_METHODS = new Set([
  "item/started",
  "item/completed",
  "item/agentMessage/delta",
  "item/agentMessage/reset",
  "item/reasoning/summaryTextDelta",
  "item/toolOutput/delta",
]);

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
  // I2: Monotonic capability-owner revision — increments on every capability
  // publication/transition (thread/status/changed, cap refresh, rehydrate
  // commit). Rehydrate captures this at entry; if it changed during the
  // await, a newer capability owner published caps and the rehydrate must
  // preserve the current caps instead of overwriting with its stale
  // projection. If unchanged, the rehydrate commits authoritative projected
  // capabilities.
  let capabilityOwnerRev = 0;
  // I3: Page-owned item IDs — tracks which item IDs were loaded by loadOlder
  // (page-owned history). On rehydrate page-race merge, only these items are
  // prepended as older history; current-only non-page items (live notifications
  // that arrived during the await) are appended as the live tail, never moved
  // to the oldest position where they'd be discarded by the 500-cap. Cleared
  // on every conversation transition (open/close/reset/openProjected).
  const pageOwnedIds = new Set<string>();
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
    if (state === undefined || state.ref === null) return null;
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
    if (state === undefined || state.ref === null) return null;
    return {
      epoch: bindingEpoch,
      ref: state.ref,
      generation: state.conversationGeneration,
      service: boundService,
      sink: boundSink,
    };
  }

  // Verify a captured binding is still current. This applies to scheduled
  // rereads, capability refreshes, paging, and mutations. If the epoch, ref,
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

  // I2: Request a mutation capability refresh through the store-owned drain
  // scheduler — no direct await bypass. This serializes/coalesces with rereads.
  // I1: captures the exact binding tuple (epoch/service/sink/ref/gen) at
  // request time. For openProjected, captureBinding() provides the full tuple.
  // For plain open(), boundService/boundSink are null, so we capture a
  // service-specific tuple from the passed service argument. The effect
  // validates isBindingCurrent BEFORE the refresh, AFTER the refresh await,
  // and immediately before publication. A rebind to serviceB/sinkB with same
  // gen/mutation during the await suppresses A's capabilities.
  // I2: The capability key includes mutation identity (cap:ref:mutationId)
  // so distinct mutations do not coalesce with each other or with rereads.
  // handleMutationError schedules the cap refresh through the scheduler AND
  // awaits its per-key completion before surfacing error — an unrelated
  // replenishing reread cannot delay the error.
  function requestCapabilityRefresh(
    service: ConversationService,
    ref: string,
    gen: number,
    mutationId: number,
  ): Promise<void> {
    // C1: Capture the binding from the SUPPLIED service, not the current
    // boundService. captureOperationBinding validates that the supplied
    // service === boundService and captures epoch/ref/gen/sink. A wrong
    // service (A after B bound) returns null and the effect is suppressed.
    const binding = captureOperationBinding(service);
    if (binding === null) return Promise.resolve();
    const capKey = `cap:${ref}:${mutationId}`;
    // I2: scheduler.request returns a per-key completion promise — resolves
    // when this key's effect completes/skips/errors, even while unrelated
    // rereads remain or hang. handleMutationError awaits this exact promise.
    return scheduler.request(capKey, async () => {
      const g = storeGet;
      if (g === null) return;
      // I1: Suppress stale work BEFORE any read — validate the full binding
      // tuple (epoch/service/sink/ref/gen). A rebind with different objects
      // suppresses this effect.
      if (!isBindingCurrent(binding)) return;
      const liveService = service as LiveConversationService;
      if (typeof liveService.refreshCapabilities !== "function") return;
      // I2: Check active mutation before the refresh — stale recovery bail.
      if (g().pendingMutation?.mutationId !== mutationId) return;
      let refreshed: ThreadCapabilities | null;
      try {
        refreshed = await liveService.refreshCapabilities(ref);
      } catch {
        // If refresh fails, the mutation error handler surfaces the error.
        return;
      }
      // I1: Validate the full binding tuple AGAIN after the await — a rebind
      // to serviceB/sinkB during the refresh must suppress A's capabilities.
      if (!isBindingCurrent(binding)) return;
      // I2: Check active mutation AFTER the refresh too.
      if (g().pendingMutation?.mutationId !== mutationId) return;
      // I1: Validate generation immediately before publication.
      if (g().conversationGeneration === gen && refreshed !== null) {
        const currentConv = g().conversation;
        if (currentConv !== null) {
          // D (I2 completion seam): publish refreshed caps to the bound
          // activity sink under exact current identity BEFORE conversation
          // capabilities. A false/stale sink suppresses conversation cap
          // publication — the two stores stay atomically consistent. The
          // plain-open path (no sink) still updates conversation caps.
          const identity: ActivityIdentity = {
            threadId: currentConv.id,
            ref,
            generation: gen,
          };
          const sinkAccepted =
            boundSink !== null
              ? boundSink.setLiveCapabilities(refreshed, identity)
              : true;
          if (!sinkAccepted) return;
          // I2: increment capability-owner revision for this publication.
          capabilityOwnerRev += 1;
          storeSet?.({
            conversation: {
              ...currentConv,
              capabilities: { ...refreshed },
            },
          });
        }
      }
    });
  }

  // Late-bound store setter — assigned inside create() so capability refresh
  // effects can call set() outside the create() callback scope.
  let storeSet: ((partial: Partial<ConversationState>) => void) | null = null;
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
    const retainedIds = new Set(items.map((i) => i.id));
    truncatedItemIds.clear();
    for (const item of items) {
      let needsTruncation = false;
      if (item.kind === "assistant") {
        needsTruncation = exceedsByteLimit(item.markdown, MAX_ITEM_BYTES);
      } else if (item.kind === "activity") {
        needsTruncation =
          (item.detail.arguments !== undefined &&
            exceedsByteLimit(item.detail.arguments, MAX_ITEM_BYTES)) ||
          (item.detail.output !== undefined &&
            exceedsByteLimit(item.detail.output, MAX_ITEM_BYTES)) ||
          (item.detail.error !== undefined &&
            exceedsByteLimit(item.detail.error, MAX_ITEM_BYTES));
      }
      // Freeze if: original content is oversized, OR the item was already
      // frozen and remains in the final set (priorFrozenIds), OR the item
      // is a superseded live version still frozen (supersededFrozenIds).
      if (
        needsTruncation ||
        (priorFrozenIds.has(item.id) && retainedIds.has(item.id)) ||
        (supersededFrozenIds.has(item.id) && retainedIds.has(item.id))
      ) {
        truncatedItemIds.add(item.id);
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
    let needsTruncation = false;
    if (item.kind === "assistant") {
      needsTruncation = exceedsByteLimit(item.markdown, MAX_ITEM_BYTES);
    } else if (item.kind === "activity") {
      needsTruncation =
        (item.detail.arguments !== undefined &&
          exceedsByteLimit(item.detail.arguments, MAX_ITEM_BYTES)) ||
        (item.detail.output !== undefined &&
          exceedsByteLimit(item.detail.output, MAX_ITEM_BYTES)) ||
        (item.detail.error !== undefined &&
          exceedsByteLimit(item.detail.error, MAX_ITEM_BYTES));
    }
    if (needsTruncation) {
      truncatedItemIds.add(item.id);
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
    const retainedIds = new Set(items.map((i) => i.id));
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
    storeSet = set;
    return {
      ref: null,
      profileId: null,
      connectionGeneration: 0,
      conversationGeneration: 0,

      conversation: null,
      olderCursor: null,
      loadingOlder: false,
      status: "idle",
      error: null,

      draft: "",
      pendingSend: null,
      pendingMutation: null,

      async open(service, ref) {
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
        liveOwnedRevs.clear();
        truncatedItemIds.clear();
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
          // sink so loadOlder/mutations/cap-refresh validate exact service
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

      async openProjected(service, sink, ref) {
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
          conversationGeneration: gen,
        });
        try {
          const { conversation, activity, olderCursor } =
            await service.readProjection(ref);
          if (gen !== conversationGen) return;
          const identity: ActivityIdentity = {
            threadId: conversation.id,
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
          });
          service.subscribeNotifications((n) => {
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
          });
        } catch (err) {
          if (gen !== conversationGen) return;
          set({
            status: "error",
            error: err instanceof Error ? err.message : String(err),
          });
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
        // I2: Capture capability-owner revision at entry. If it changed during
        // the await, a newer capability owner published caps and the rehydrate
        // must preserve the current caps.
        const entryCapRev = capabilityOwnerRev;
        // Fix round 1: Capture live-owner revision at entry. If an item's
        // liveOwnedRevs revision advanced past this after entry, the live
        // notification updated the item after the rehydrate's readProjection
        // snapshot — the current version is newer and must be preserved.
        const entryLiveRev = liveOwnerRev;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        try {
          const { conversation, activity, olderCursor } =
            await service.readProjection(ref);
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
          const pageOwnerChanged = entryLoadOlderToken !== loadOlderToken;
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
          // R2: If page owner changed, safely merge: preserve newer page-owned
          // items/cursor while committing the reread conversation+activity.
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
          const currentConvForMerge = currentSnapshot.conversation;
          // Superseded: reread contains ID but current live revision > entry.
          // Preserve the current (live-updated) version in the reread position.
          const supersededIds = new Set<string>();
          const supersededVersions = new Map<string, MobileTimelineItem>();
          if (currentConvForMerge !== null) {
            for (const item of conversation.items) {
              const rev = liveOwnedRevs.get(item.id);
              if (rev !== undefined && rev > entryLiveRev) {
                const current = currentConvForMerge.items.find(
                  (i) => i.id === item.id,
                );
                if (current !== undefined) {
                  supersededIds.add(item.id);
                  supersededVersions.set(item.id, current);
                }
              }
            }
          }
          // Replace superseded items in the reread with the current version.
          let mergedItems = conversation.items.map((item) =>
            supersededIds.has(item.id)
              ? (supersededVersions.get(item.id) as MobileTimelineItem)
              : item,
          );
          let mergedCursor = olderCursor;
          if (pageOwnerChanged) {
            if (currentConvForMerge !== null) {
              // 1. Prepend only current-only pageOwned history (items in
              //    pageOwnedIds that are not in the reread projection).
              // 2. Commit the authoritative reread projection (with superseded
              //    replacements applied).
              // 3. Append only current-only liveOwned tail (items in
              //    liveOwnedRevs that are not in the reread projection).
              // 4. Drop current-only items owned by NEITHER (not pageOwned,
              //    not liveOwned, not in reread) as omitted old history.
              const pageOnlyItems = currentConvForMerge.items.filter(
                (i) => !rereadIds.has(i.id) && pageOwnedIds.has(i.id),
              );
              const liveTailItems = currentConvForMerge.items.filter(
                (i) =>
                  !rereadIds.has(i.id) &&
                  !pageOwnedIds.has(i.id) &&
                  liveOwnedRevs.has(i.id),
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
            const liveTailItems = currentConvForMerge.items.filter(
              (i) =>
                !rereadIds.has(i.id) &&
                !pageOwnedIds.has(i.id) &&
                liveOwnedRevs.has(i.id),
            );
            if (liveTailItems.length > 0) {
              mergedItems = [...mergedItems, ...liveTailItems];
            }
          }
          const identity: ActivityIdentity = {
            threadId: conversation.id,
            ref,
            generation: gen,
          };
          // Strict activity sink: call setLiveView BEFORE committing the paired
          // conversation projection. If it returns false, do not commit.
          const accepted = sink.setLiveView(activity, identity);
          if (!accepted) return;
          // R1: Success preserves any newer error owner. Only clear error if
          // the error-owner revision hasn't changed AND no failed mutation
          // owns the error. A failed mutation's error persists until a
          // subsequent mutation or open clears it.
          const currentState = get();
          const errorUnchanged = entryErrorRev === errorOwnerRev;
          const mutationOwnsError =
            currentState.pendingMutation?.status === "failed";
          // I2: If the capability-owner revision hasn't changed during the
          // await, commit the authoritative projected capabilities from the
          // rehydrate. If it advanced (a newer cap refresh or notification
          // published caps), preserve the current caps.
          const capOwnerChanged = entryCapRev !== capabilityOwnerRev;
          const currentConv = currentState.conversation;
          const preservedCaps = capOwnerChanged
            ? currentConv?.capabilities
            : undefined;
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
          const rehydratePriorFrozen = new Set<string>();
          for (const id of truncatedItemIds) {
            if (!rereadIds.has(id)) rehydratePriorFrozen.add(id);
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
          const committedConversation = {
            ...conversation,
            items: committedItems,
            ...(preservedCaps ? { capabilities: preservedCaps } : {}),
          };
          // Fix round 1: Reconcile liveOwnedRevs — for items in the
          // authoritative reread projection that are NOT superseded (revision
          // ≤ entryLiveRev or not live-owned), accept the reread and clear
          // that ID's ownership. Superseded items (revision > entryLiveRev)
          // keep their ownership — the live version is newer and may need
          // to survive a future page merge. Live-owned items NOT in the reread
          // stay in the map (still live-only / live tail).
          for (const item of conversation.items) {
            const rev = liveOwnedRevs.get(item.id);
            if (rev === undefined || rev <= entryLiveRev) {
              liveOwnedRevs.delete(item.id);
            }
          }
          // I2: If we're committing the projected capabilities (cap owner
          // unchanged), increment the capability-owner revision.
          if (!capOwnerChanged) {
            capabilityOwnerRev += 1;
          }
          const commitBase = {
            conversation: committedConversation,
            olderCursor: mergedCursor,
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
        if (state.loadingOlder || state.conversation === null) return;
        // F8: Never request with a null/empty cursor — no more older pages.
        if (state.olderCursor === null) return;
        const cursor = state.olderCursor;
        const gen = state.conversationGeneration;
        // C1: Capture a service-specific operation binding. If the supplied
        // service is wrong (A after B bound), binding is null — zero request.
        const opBinding = captureOperationBinding(service);
        if (opBinding === null) return;
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
          if (!isBindingCurrent(opBinding)) return;
          // Fix round 1 I2: generation/identity-stale — perform ZERO set calls
          // (including loadingOlder). The newer conversation owns all fields.
          if (get().conversationGeneration !== gen) return;
          // Task 2A-Ops-2: Stale loadOlder — a newer page operation owns the
          // loadingOlder/error fields. Make no state change at all.
          if (olderToken !== loadOlderToken) return;
          const currentConv = get().conversation;
          if (currentConv !== null) {
            // F10: Dedupe by source item identity — items from older pages
            // that already exist in the current conversation (same id) are
            // dropped, keeping the newer (live tail) version.
            // Task 2A-Items: also dedupe within the incoming page by updating
            // the seen set during traversal, preserving order and first
            // occurrence semantics.
            const existingIds = new Set(currentConv.items.map((i) => i.id));
            const deduped: MobileTimelineItem[] = [];
            for (const item of result.items) {
              if (existingIds.has(item.id)) continue;
              // I3: Defense-in-depth — filter question rows at the state merge
              // boundary too, not only in the service's projectOlderTurns. A
              // pending ask cannot legitimately be older than newer continuation
              // turns; page-local projection otherwise resurrects settled calls.
              if (item.kind === "question") continue;
              existingIds.add(item.id);
              deduped.push(item);
            }
            // I3: Record page-owned item IDs — these are items loaded from
            // older pages. They are tracked so the rehydrate page-race merge
            // can distinguish page-owned history from live notifications.
            for (const item of deduped) {
              pageOwnedIds.add(item.id);
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
            const currentIds = new Set(currentConv.items.map((i) => i.id));
            const priorFrozen = new Set<string>();
            for (const id of truncatedItemIds) {
              if (currentIds.has(id)) priorFrozen.add(id);
            }
            const pageMerged = capItems([...deduped, ...currentConv.items]);
            reconcileTruncationFrom(pageMerged, priorFrozen);
            const merged = truncateAndRecord(pageMerged);
            // Prune ownership maps for evicted IDs (IDs not in the final merged
            // set). This prevents stale freeze/page/live entries from
            // affecting future page loads or re-introductions.
            pruneEvictedIds(merged);
            // F8: If we're at the cap and the merge trimmed older items,
            // disable further paging honestly — set cursor to null so
            // we don't repeatedly load rows that will be discarded.
            const atCap = merged.length >= RETAINED_ITEM_CAP;
            const nextCursor = atCap ? null : (result.nextCursor ?? null);
            set({
              conversation: { ...currentConv, items: merged },
              olderCursor: nextCursor,
              loadingOlder: false,
            });
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await. If the
          // binding changed (rebind to B), A's completion makes ZERO state
          // changes — no loadingOlder/error.
          if (!isBindingCurrent(opBinding)) return;
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
          }
        }
      },

      setDraft(text) {
        draftRevision += 1;
        set({ draft: text });
      },

      async send(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "send", "send");
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
          await service.send(input);
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
              set({ pendingSend: null, pendingMutation: null, error: null });
            } else {
              set({ pendingSend: null, pendingMutation: null });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestCapabilityRefresh,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async steer(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "steer", "steer");
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
          error: null,
        });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          await service.steer(input);
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({ pendingMutation: null, error: null });
            } else {
              set({ pendingMutation: null });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestCapabilityRefresh,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async queue(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "queue", "queue");
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
          error: null,
        });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          await service.queue(input);
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({ pendingMutation: null, error: null });
            } else {
              set({ pendingMutation: null });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestCapabilityRefresh,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      async interrupt(service) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "interrupt", "interrupt");
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
        set({ pendingSend: null, pendingMutation: mutation, error: null });
        // I1: Capture error-owner revision AFTER installing pending+error-clear.
        const entryErrorRev = errorOwnerRev;
        try {
          await service.interrupt();
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          if (get().pendingMutation?.mutationId === mutationId) {
            // I1: clear error only if error-owner revision is unchanged —
            // revision equality ONLY.
            if (entryErrorRev === errorOwnerRev) {
              set({ pendingMutation: null, error: null });
            } else {
              set({ pendingMutation: null });
            }
            // I1: mutation settled (success) — drain deferred trailing reread.
            drainTrailingReread();
          }
        } catch (err) {
          // C1: Recheck the exact operation binding after the await.
          if (!isBindingCurrent(opBinding)) return;
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            null,
            revisionAtSubmit,
            entryErrorRev,
            getDraftRevision,
            getErrorOwnerRev,
            set,
            get,
            requestCapabilityRefresh,
          );
          // I1: mutation settled (failed terminal) — drain deferred trailing
          // reread after handleMutationError sets the failed state.
          drainTrailingReread();
        }
      },

      close() {
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
          olderCursor: null,
          loadingOlder: false,
          conversationGeneration: conversationGen,
        });
      },

      applyNotification(n) {
        const state = get();
        if (state.conversation === null) return;

        // Check threadId/ref against the current conversation and silently drop
        // mismatches.
        const nref = notificationRef(n);
        if (nref !== null) {
          const currentId = state.conversation.id;
          const currentRef = state.ref;
          const idMatch =
            nref.threadId === undefined || nref.threadId === currentId;
          const refMatch = nref.ref === undefined || nref.ref === currentRef;
          if (!idMatch || !refMatch) return;
        }

        const conv = state.conversation;
        switch (n.method) {
          case "thread/status/changed": {
            const params = n.params as {
              status: { type: string };
              capabilities?: ThreadCapabilities;
            };
            // I2: increment capability-owner revision if capabilities are
            // being published.
            if (params.capabilities !== undefined) {
              capabilityOwnerRev += 1;
            }
            set({
              conversation: {
                ...conv,
                status: params.status.type,
                capabilities: params.capabilities
                  ? { ...params.capabilities }
                  : conv.capabilities,
              },
            });
            break;
          }

          case "thread/queueChanged": {
            const params = n.params as {
              queue: { depth?: number; preview?: string[]; texts?: string[] };
            };
            set({
              conversation: {
                ...conv,
                queue: {
                  depth: params.queue.depth ?? 0,
                  preview: params.queue.preview ?? params.queue.texts ?? [],
                },
              },
            });
            break;
          }

          case "evener/thread/name/changed": {
            const params = n.params as { name: string };
            set({
              conversation: { ...conv, name: params.name },
            });
            break;
          }

          case "thread/model/changed": {
            const params = n.params as {
              modelProvider: string;
              reasoningEffortLevels?: string[];
              supportsReasoning?: boolean;
            };
            set({
              conversation: {
                ...conv,
                modelProvider: params.modelProvider,
                reasoningEffortLevels: params.reasoningEffortLevels,
                supportsReasoning: params.supportsReasoning,
              },
            });
            break;
          }

          case "thread/reasoning-effort/changed": {
            const params = n.params as { reasoningEffort?: string };
            set({
              conversation: {
                ...conv,
                reasoningEffort: params.reasoningEffort,
              },
            });
            break;
          }

          case "turn/started": {
            set({
              conversation: { ...conv, status: "running" },
            });
            break;
          }

          case "turn/completed": {
            const params = n.params as {
              turn: { usage?: MobileUsage; status: string };
            };
            set({
              conversation: {
                ...conv,
                status: conv.status === "running" ? "ready" : conv.status,
                usage: params.turn.usage
                  ? { ...conv.usage, ...params.turn.usage }
                  : conv.usage,
              },
            });
            break;
          }

          case "item/started": {
            const params = n.params as { item: ThreadItem };
            const projected = projectSingleItem(params.item, conv.askPending);
            if (projected !== null) {
              // Task 2A-Items: authoritative replacement — remove any stale
              // freeze entry so the new content can accept future deltas.
              truncatedItemIds.delete(params.item.id);
              const truncated = truncateAndRecordSingle(projected);
              const existingIdx = conv.items.findIndex(
                (i) => i.id === params.item.id,
              );
              if (existingIdx >= 0) {
                // Fix round 1: Mark as live-owned — accepted replacement.
                markLiveOwned(params.item.id);
                set({
                  conversation: {
                    ...conv,
                    items: conv.items.map((i, idx) =>
                      idx === existingIdx ? truncated : i,
                    ),
                  },
                });
              } else {
                // Residual 2: Mark as live-owned — inserted by an actual
                // accepted item lifecycle notification.
                markLiveOwned(params.item.id);
                const cappedItems = capItems([...conv.items, truncated]);
                // Task 2A-Truncation residual fix round 2: prune evicted IDs
                // from ownership maps after incremental append+cap.
                pruneEvictedIds(cappedItems);
                set({
                  conversation: {
                    ...conv,
                    items: cappedItems,
                  },
                });
              }
            } else {
              // Unsupported item transition — coalesce to one rehydrate.
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
            }
            break;
          }

          case "item/completed": {
            const params = n.params as { item: ThreadItem };
            const projected = projectSingleItem(params.item, conv.askPending);
            if (projected !== null) {
              // Task 2A-Items: authoritative replacement — remove any stale
              // freeze entry so the new content can accept future deltas.
              truncatedItemIds.delete(params.item.id);
              const truncated = truncateAndRecordSingle(projected);
              const existingIdx = conv.items.findIndex(
                (i) => i.id === params.item.id,
              );
              if (existingIdx >= 0) {
                // Replace existing item.
                // Fix round 1: Mark as live-owned — accepted replacement.
                markLiveOwned(params.item.id);
                set({
                  conversation: {
                    ...conv,
                    items: conv.items.map((i, idx) =>
                      idx === existingIdx ? truncated : i,
                    ),
                  },
                });
              } else {
                // UPSERT: insert the authoritative completed item even if the
                // start notification was missed.
                // Residual 2: Mark as live-owned — inserted by an actual
                // accepted item lifecycle notification.
                markLiveOwned(params.item.id);
                const cappedItems = capItems([...conv.items, truncated]);
                // Task 2A-Truncation residual fix round 2: prune evicted IDs
                // from ownership maps after incremental append+cap.
                pruneEvictedIds(cappedItems);
                set({
                  conversation: {
                    ...conv,
                    items: cappedItems,
                  },
                });
              }
            } else {
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
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
              if (truncatedItemIds.has(params.itemId)) {
                break;
              }
              const combined =
                (existing.kind === "assistant" ? existing.markdown : "") +
                params.delta;
              const truncated = truncateText(combined, MAX_ITEM_BYTES);
              if (truncated !== combined) {
                truncatedItemIds.add(params.itemId);
              }
              // Fix round 1: Mark as live-owned — accepted delta update.
              markLiveOwned(params.itemId);
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
              truncatedItemIds.delete(params.itemId);
              // Fix round 1: Mark as live-owned — accepted reset update.
              markLiveOwned(params.itemId);
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
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
            }
            break;
          }

          case "item/reasoning/summaryTextDelta": {
            const params = n.params as { itemId: string; delta: string };
            const existing = conv.items.find(
              (i) => i.id === params.itemId && i.kind === "activity",
            );
            // Task 2A-Family: exact delta family from required item.family
            // (never label inference). Reasoning delta mutates only family=
            // reasoning. Missing target, wrong family, or unknown family =>
            // no mutation/live revision/freeze change, request authoritative
            // reread.
            if (
              existing?.kind !== "activity" ||
              existing.family !== "reasoning"
            ) {
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            // F12: Per-item truncation ownership — frozen guard.
            if (truncatedItemIds.has(params.itemId)) {
              break;
            }
            const combined = (existing.detail.output ?? "") + params.delta;
            const truncated = truncateText(combined, MAX_ITEM_BYTES);
            if (truncated !== combined) {
              truncatedItemIds.add(params.itemId);
            }
            // Mark live revision only on accepted exact update.
            markLiveOwned(params.itemId);
            set({
              conversation: {
                ...conv,
                items: conv.items.map((item) =>
                  item.kind === "activity" && item.id === params.itemId
                    ? {
                        ...item,
                        detail: { ...item.detail, output: truncated },
                      }
                    : item,
                ),
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
            const existing = conv.items.find(
              (i) => i.id === params.itemId && i.kind === "activity",
            );
            // Task 2A-Family: exact delta family from required item.family
            // (never label inference). Tool-output delta mutates only family=
            // tool AND requires stored detail.callId and incoming params.callId
            // both present strings and exactly equal. Missing target, missing
            // either callId, mismatch, unknown family, or wrong family => no
            // mutation/live revision/freeze change, request authoritative
            // reread.
            if (existing?.kind !== "activity") {
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            if (existing.family !== "tool") {
              // Wrong family or unknown family — not a tool item.
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
              break;
            }
            {
              const itemCallId = existing.detail.callId;
              if (
                typeof itemCallId !== "string" ||
                typeof params.callId !== "string" ||
                itemCallId !== params.callId
              ) {
                // Missing either callId, or mismatch — no mutation.
                if (state.ref !== null) {
                  requestRehydrate(state.ref);
                }
                break;
              }
            }
            // F12: Per-item truncation ownership — frozen guard.
            if (truncatedItemIds.has(params.itemId)) {
              break;
            }
            const combined = (existing.detail.output ?? "") + params.delta;
            const truncated = truncateText(combined, MAX_ITEM_BYTES);
            if (truncated !== combined) {
              truncatedItemIds.add(params.itemId);
            }
            // Mark live revision only on accepted exact update.
            markLiveOwned(params.itemId);
            set({
              conversation: {
                ...conv,
                items: conv.items.map((item) =>
                  item.kind === "activity" && item.id === params.itemId
                    ? {
                        ...item,
                        detail: { ...item.detail, output: truncated },
                      }
                    : item,
                ),
              },
            });
            break;
          }

          case "warning": {
            const params = n.params as { message?: string; title?: string };
            const id = `warning:${params.title ?? params.message ?? Date.now()}`;
            const failureItem: MobileTimelineItem = {
              kind: "failure",
              id,
              title: params.title ?? "Warning",
              detail: params.message ?? "",
            };
            // Residual 2: Mark as live-owned — created by an actual live
            // notification.
            markLiveOwned(id);
            const warningCappedItems = capItems([...conv.items, failureItem]);
            // Task 2A-Truncation residual fix round 2: prune evicted IDs
            // from ownership maps after incremental append+cap.
            pruneEvictedIds(warningCappedItems);
            set({
              conversation: {
                ...conv,
                items: warningCappedItems,
              },
            });
            break;
          }

          // evener/thread/resync triggers a coalesced rehydrate via the store-owned
          // drain scheduler. The store does not re-read on its own.
          case "evener/thread/resync": {
            if (state.ref !== null) {
              requestRehydrate(state.ref);
            }
            break;
          }

          // Default: only resync for unsupported item/* transitions, not for
          // all unknown notifications — to avoid reread storms from unrelated
          // notification families.
          default: {
            if (
              typeof n.method === "string" &&
              n.method.startsWith("item/") &&
              !ITEM_NOTIFICATION_METHODS.has(n.method)
            ) {
              if (state.ref !== null) {
                requestRehydrate(state.ref);
              }
            }
            break;
          }
        }
      },

      reset() {
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

// Shared mutation error handler: on actionUnavailable, requests the
// non-subscribing refreshCapabilities (never open()) through the store-owned
// drain scheduler (I2 — no direct await bypass) to publish refreshed caps
// before surfacing the error. On failure, the failed mutation state PERSISTS
// (not cleared to null). The draft is only restored if the user has not edited
// since the mutation cleared the draft — detected via draftRevision, so
// type-then-delete (which produces "" but increments the revision) counts as
// an edit and prevents restore. F4/F10: uses mutationId (not generation alone)
// so out-of-order failure cannot overwrite a newer mutation's error.
// F10: checks active mutation before the capability refresh — a newer
// mutation may have started.
// I2: the capability refresh is requested through the store-owned scheduler
// so it serializes/coalesces with rereads; stale mutation recovery is
// suppressed by the mutationId guard inside the scheduler effect.
// Task 2A-Ops-3: after the capability refresh resolves, caps are published
// only when both generation AND mutation operation ID remain current — a
// newer same-generation mutation must not receive stale capability
// publication or stale error state.
async function handleMutationError(
  err: unknown,
  service: ConversationService,
  ref: string | null,
  gen: number,
  mutationId: number,
  mutation: ConversationMutationState,
  draftSnapshot: string | null,
  revisionAtSubmit: number,
  entryErrorRev: number,
  getDraftRevision: () => number,
  getErrorOwnerRev: () => number,
  set: (partial: Partial<ConversationState>) => void,
  get: () => ConversationState,
  requestCapabilityRefresh: (
    service: ConversationService,
    ref: string,
    gen: number,
    mutationId: number,
  ) => Promise<void>,
): Promise<void> {
  // F10: Check active mutation BEFORE the capability refresh. If a newer
  // mutation has already started, this error is stale — bail out.
  if (get().pendingMutation?.mutationId !== mutationId) return;

  // I2: on actionUnavailable, schedule the capability refresh through the
  // store-owned drain scheduler AND await its completion before publishing
  // capabilities/surfacing error. This preserves prior observable ordering and
  // mutation guards — caps are published before the error is visible.
  if (isActionUnavailableError(err) && ref !== null) {
    await requestCapabilityRefresh(service, ref, gen, mutationId);
  }

  // F10: Re-check mutationId — out-of-order failure cannot change a newer
  // mutation, error, or draft. The capability refresh has completed (awaited);
  // now surface the error.
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

// Re-export the MobileCapabilities type for consumers that import from the
// store module.
export type { MobileCapabilities, MobileConversation, MobileTimelineItem };
