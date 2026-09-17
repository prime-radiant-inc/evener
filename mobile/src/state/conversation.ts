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
// The store holds the package's ThreadModel (never the raw wire Thread) with
// the phone's display rows projected from it: every notification folds through
// reducer.applyNotification and the rows are re-projected from the model it
// returns, so a frame and a snapshot produce rows through the one projector.
// A re-read via thread/read after evener/thread/resync is the authoritative
// refresh path (triggered by the store-owned drain scheduler, not timers).

import { create } from "zustand";
import {
  applyNotification,
  isStaleCursorError,
  mergeOlderItemPage,
  notificationTargetsThread,
  sessionControls,
  WireError,
} from "@evener/appwire-client";
import type {
  AnyNotification,
  InputItem,
  MutationReceipt,
  ThreadModel,
} from "@evener/appwire-client";
import type {
  BoundText,
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/project";
import {
  activityIdentity,
  attachmentSourceIdentity,
  capItems,
  MAX_ITEM_BYTES,
  ownTimelineIdentities,
  projectConversation,
  RETAINED_ITEM_CAP,
  timelineIdentities,
  timelineIdentity,
  truncateItem,
  truncateText,
} from "../conversation/project";
// Re-exported where they have always been imported from: the bounds are the row
// shape's, and project.ts owns that shape.
export {
  MAX_ITEM_BYTES,
  RETAINED_ITEM_CAP,
  TRUNCATION_MARKER,
  truncateText,
} from "../conversation/project";
import type { ActivityView } from "../services/activity";
import type {
  ConversationReadProjection,
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
  // buffer at the same point for the same reason.
  //
  // The package reducer over the conversation. The display rows are projected
  // from the model it returns (applyNotification below), so the rows a frame
  // produces and the rows a snapshot produces come from the one projector.
  function applyThreadNotification(
    conversation: MobileConversation,
    n: AnyNotification,
  ): MobileConversation {
    return applyNotification(conversation, n, Date.now()) as MobileConversation;
  }
  // The phone's two display bounds, as one pass over the projected rows: the
  // newest RETAINED_ITEM_CAP rows, each text-bearing field cut to
  // MAX_ITEM_BYTES with the marker. Every path that publishes a conversation
  // runs it, so the bound is a property of what is displayed rather than of
  // the sequence of frames that produced it. The model behind the rows keeps
  // its full text (#1535 asks whether the package should bound that).
  //
  // The rows are rebuilt from the model on every frame, but the model hands
  // back the SAME string reference for text no frame touched, so each bound
  // string is cached under the source string it came from and reused until
  // that reference changes: a transcript of settled rows is not re-encoded
  // per delta. The cache is rebuilt from the rows of each publish (the
  // previous one is read through, then dropped), so it holds only what is on
  // screen and needs no invalidation of its own. The one item a delta is
  // streaming into does re-encode once per frame, because its text really is
  // new each time — #1535 is where that stops growing.
  let boundedText = new Map<string, string>();
  // The cache belongs to the conversation whose rows it bound: every string
  // in it is held by that conversation's model or its rows, so once the
  // conversation is dropped the cache is the only thing retaining them. Every
  // transition that drops the conversation releases it. A suspend does not:
  // the rows stay on screen for the resume, which republishes the same text.
  function releaseBoundedTextCache(): void {
    boundedText = new Map();
  }
  // Whether a folded model can change a row. projectConversation reads exactly
  // one of the model's own fields: `turns` — every turn, item, status and error
  // a row is made of hangs off it, and the answerable asks are derived from it
  // too (deriveAskQuestions.ts reads turns alone). Every other field a frame
  // moves (the status, the name, the queue, the jobs tree, the goal, the wire's
  // askPending, and lastFrameAt, which moves on EVERY frame) changes no row.
  function changesRows(previous: MobileConversation, applied: ThreadModel): boolean {
    return applied.turns !== previous.turns;
  }

  function capAndTruncate(conversation: MobileConversation): MobileConversation {
    const previous = boundedText;
    const next = new Map<string, string>();
    const bound: BoundText = (text) => {
      const cached = next.get(text) ?? previous.get(text);
      const bounded = cached ?? truncateText(text, MAX_ITEM_BYTES);
      next.set(text, bounded);
      // A published row carries its BOUNDED text, so the next publish binds
      // that string, not the one the model handed over. Bounding a bounded
      // string is a no-op (truncateText never returns more than the limit), so
      // record it as its own answer and the row costs one encode for its life
      // rather than one more on every publish after the first.
      if (bounded !== text) next.set(bounded, bounded);
      return bounded;
    };
    const items = capItems(conversation.items).map((item) => truncateItem(item, bound));
    boundedText = next;
    return { ...conversation, items };
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
        releaseBoundedTextCache();
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
          set({
            conversation: capAndTruncate(conv),
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
        releaseBoundedTextCache();
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
          set({
            conversation: capAndTruncate(conversation),
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
          // The snapshot is authoritative for the whole conversation (the
          // response-cut note by applyThreadNotification): a frame this store
          // folded while the read was in flight is already in it, and so is
          // every turn the read covers. Older pages this client merged in lie
          // outside that window and go with the model the snapshot replaces;
          // hasEarlierItems and the snapshot's own cursor say the history is
          // there to page back in. This is the web's snapshot recovery
          // (stores/threads.ts refreshTrackedThread replaces its model
          // wholesale from a fresh read for the same reason).
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
          const committedConversation = capAndTruncate(conversation);
          const commitBase = {
            conversation: committedConversation,
            olderCursor,
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
          const page = await service.loadOlder(cursor);
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
            // The page merges into the model this store already holds
            // (reducer.mergeOlderItemPage: older turns before current ones,
            // shared turns and tool call/result pairs folded, the page's own
            // nextCursor carried onto the model), and the rows are
            // re-projected from it. Nothing about a page is row-level any
            // more: the hub never repeats a transcript key within a page
            // (internal/appitempaging/page.go's validateCandidates), and an
            // item the model already holds merges by identity instead of
            // arriving twice. A settled older ask brings no question row with
            // it either — liveAskQuestions reads the whole model, where a
            // later user message answers it (deriveAskQuestions.ts).
            const beforeKeys = new Set(currentConv.items.map(timelineIdentity));
            const pageConversation = capAndTruncate(
              projectConversation(mergeOlderItemPage(currentConv, page)),
            );
            const merged = pageConversation.items;
            // F8: at the cap, the rows a further page would add are exactly
            // what the cap discards, so paging on would fetch what it cannot
            // show. End paging honestly — a cursor of null with
            // hasEarlierItems still true offers a load that early-returns
            // "ignored".
            const atCap = merged.length >= RETAINED_ITEM_CAP;
            const nextCursor = atCap
              ? null
              : (pageConversation.olderCursor ?? null);
            set({
              conversation: pageConversation,
              olderCursor: nextCursor,
              hasEarlierItems: atCap
                ? false
                : page.data.some((turn) => turn.hasEarlierItems === true) ||
                  get().hasEarlierItems,
              hasLaterItems:
                page.data.some((turn) => turn.hasLaterItems === true) ||
                get().hasLaterItems,
              loadingOlder: false,
            });
            return {
              status: "loaded",
              itemKeys: merged
                .map(timelineIdentity)
                .filter((key) => !beforeKeys.has(key)),
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
        releaseBoundedTextCache();
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

        // Every frame about this thread folds into the package reducer's
        // model, and the display rows are a projection of that model: one
        // rule for a frame and for a snapshot, so there is nothing for a row
        // applier to disagree with. The two display bounds run after the
        // projection.
        const applied = applyThreadNotification(state.conversation, n);
        if (applied !== state.conversation) {
          if (changesRows(state.conversation, applied)) {
            set({ conversation: capAndTruncate(projectConversation(applied)) });
          } else {
            // The model advanced — the frame is the authority on whatever it
            // carried, and lastFrameAt moved — but no row changed, so the rows
            // this conversation already published stand, by reference.
            set({ conversation: { ...applied, items: state.conversation.items } });
          }
        }
        if (state.ref === null) return;
        // evener/thread/resync is the authoritative refresh path (the store
        // never re-reads on its own). Any frame about the transcript that the
        // reducer could not place — an unknown method, one naming an item or
        // turn this model does not hold, a warning or a steer whose active
        // turn lies outside the loaded window — leaves `turns` untouched by
        // reference (every applied fold rebuilds it through mapTurn, which
        // hands back the same array when nothing matched), while the model
        // itself is still a new object because the frame is evidence of
        // liveness. That is the gap case: the transcript this client holds is
        // missing what the frame was about, so it asks for the canonical read
        // at once rather than showing an incomplete transcript until the next
        // resync. Thread-level frames (a status, a name, the queue) never
        // touch turns and are not gaps, so they are named out.
        const touchesTranscript =
          n.method.startsWith("item/") ||
          n.method.startsWith("turn/") ||
          n.method === "warning" ||
          n.method === "evener/steering/injected";
        // One exception, by the wire's own rule rather than by this window's
        // contents: a warning that lands with no active turn is dropped in the
        // reducer because warnings are never transcript-persisted (its
        // "warning" case cites internal/apptranscript having no warning-item
        // conversion), so the canonical read cannot carry that warning either.
        // Nothing is missing from the transcript and there is nothing to fetch.
        const droppedByTheWiresOwnRule =
          n.method === "warning" && !state.conversation.activeTurnId;
        if (
          n.method === "evener/thread/resync" ||
          (touchesTranscript &&
            !droppedByTheWiresOwnRule &&
            applied.turns === state.conversation.turns)
        ) {
          requestRehydrate(state.ref);
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
        releaseBoundedTextCache();
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
