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
  copyItemTextPresence,
  isStaleCursorError,
  isActiveItem,
  isToolCallItemId,
  isToolResultItemId,
  itemTextPresence,
  itemIdentityMatches,
  markItemIdentityOnly,
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
  InputItem,
  ItemModel,
  MutationReceipt,
  ThreadItem,
  TurnModel,
  ThreadModel,
} from "@evener/appwire-client";
import type {
  BoundText,
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/project";
import {
  activityIdentity,
  attachmentSourceId,
  attachmentSourceIdentity,
  capItems,
  MAX_ITEM_BYTES,
  ownTimelineIdentities,
  projectConversation,
  RETAINED_ITEM_CAP,
  timelineIdentity,
  timelineIdentities,
  truncateText,
  truncateItem as sharedTruncateItem,
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

// Keep attachments beside their source whenever both rows are retained: a
// page-owned attachment kept by withPageHistory can land far from wherever
// its source ends up in the merged list (the source can be dropped as a
// duplicate and reprojected elsewhere, or simply sit later in the projected
// snapshot than the page's own front-of-list rows) — an attachment with no
// source beside it reads as unrelated to any message.
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

// A candidate row is superseded by a set of identities when either: its OWN
// identity is one of them (it duplicates a row that set already carries), or
// — for an attachment — its source's identity is, which the hub reissuing
// the source's wire id (its transcript key unchanged) makes a SEPARATE
// check: a reissued attachment's own identity never equals the candidate's,
// so the first clause alone would keep both, one holding the superseded
// image set. loadOlder (F10, below) and withPageHistory's retainedPageRow
// both ask this, each against its own pair of sets — see the one-line note
// at each call site for why that call's second set is broad or narrow.
function supersededBy(
  candidate: MobileTimelineItem,
  ownIdentities: ReadonlySet<string>,
  supersedingSources: ReadonlySet<string>,
): boolean {
  if ([...ownTimelineIdentities(candidate)].some((id) => ownIdentities.has(id)))
    return true;
  const sourceId = attachmentSourceIdentity(candidate);
  return sourceId !== null && supersedingSources.has(sourceId);
}

// A source's identity when its own item explicitly clears its output images
// (outputImages: [], distinct from omitted/undefined — the wire's only way
// to say "these are gone", not merely "unchanged since the last frame":
// appwire's nil/non-nil-empty/non-empty rule, the reducer's own
// outputImagesToItemImages comment). withPageHistory's own
// projectedAttachmentSources only names a source with a CURRENT attachment
// ROW, and an explicit clear projects no row at all (there is nothing left
// to render) — without this, a page-owned attachment for that source
// survived the merge untouched after its source explicitly removed it.
function explicitlyClearedAttachmentSources(model: ThreadModel): Set<string> {
  const cleared = new Set<string>();
  for (const turn of model.turns) {
    for (const item of turn.items) {
      if (item.outputImages !== undefined && item.outputImages.length === 0) {
        cleared.add(item.transcriptKey ?? item.id);
      }
    }
  }
  return cleared;
}

// Exported so this module's own test file and any other reader can bound a
// row exactly as the display pipeline does. The bound defaults to the plain
// byte bound; the store's own publishes pass the caching callback instead, so
// a settled row costs one encode for its life rather than one per publish.
export function truncateItem(
  item: MobileTimelineItem,
  bound: BoundText = (text) => truncateText(text, MAX_ITEM_BYTES),
): MobileTimelineItem {
  return sharedTruncateItem(item, bound);
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
    const next = applyNotification(conversation, n, Date.now());
    carryFoldIdentities(conversation.turns, next.turns);
    return next;
  }
  // I3: Page-owned item IDs — the item identities loadOlder pulled in as
  // older history. A rehydrate's snapshot is authoritative for everything it
  // carries; the page history it does not carry is prepended from here, so an
  // older page loaded during the read is not lost. Cleared on every
  // conversation transition (open/close/reset/openProjected). D23d moves the
  // older pages into the model and deletes this.
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
  // Whether a folded model can change a row. projectConversation reads two of
  // the model's own fields: `turns` — every turn, item, status and error a row
  // is made of hangs off it — and `askPending`, the wire fact liveAskQuestions
  // (deriveAskQuestions.ts) gates its whole item scan on since #1731 (piece A)
  // round 4: a status frame that flips askPending with no item of its own can
  // turn an already-projected tool row into a question row, or take one away,
  // with turns untouched by reference. Every OTHER field a frame moves (the
  // status word itself, the name, the queue, the jobs tree, the goal, and
  // lastFrameAt, which moves on EVERY frame) changes no row.
  function changesRows(previous: MobileConversation, applied: ThreadModel): boolean {
    return (
      applied.turns !== previous.turns ||
      applied.askPending !== previous.askPending
    );
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
  // The older pages live outside the model until D23d moves them into it, so
  // every publish carries them: page-owned rows the projection does not
  // contain are prepended, in front of the rows the model produced.
  function withPageHistory(
    previous: MobileConversation | null,
    projected: MobileConversation,
  ): MobileConversation {
    if (previous === null || pageOwnedIds.size === 0) return projected;
    const identities = new Set(
      projected.items.flatMap((item) => [...timelineIdentities(item)]),
    );
    // Sources the snapshot has its OWN attachment row for — narrower than
    // `identities`, which a source row alone (no attachment yet) also
    // populates. A page-owned attachment is superseded once the snapshot
    // re-emits an attachment for its source, under any wire id (the hub
    // reissues the source's id while its transcript key stands, so the new
    // attachment row's own id can differ from the page's) — OR once its
    // source explicitly clears its own images, which projects no attachment
    // row at all (explicitlyClearedAttachmentSources's own comment).
    const projectedAttachmentSources = new Set([
      ...projected.items
        .map((row) => attachmentSourceIdentity(row))
        .filter((id): id is string => id !== null),
      ...explicitlyClearedAttachmentSources(projected),
    ]);
    const pageRows: MobileTimelineItem[] = [];
    for (const item of previous.items) {
      if (!pageOwnedIds.has(timelineIdentity(item))) continue;
      const retained = retainedPageRow(item, identities, projectedAttachmentSources);
      if (retained !== null) pageRows.push(retained);
    }
    if (pageRows.length === 0) return projected;
    // A retained page attachment and its source can end up apart: the
    // source may be dropped here as a duplicate and sit, reprojected, inside
    // `projected.items` rather than at the front where the page row was.
    // attachToSources moves every attachment beside its source wherever that
    // source lands in the concatenated list, not just within `pageRows`.
    return {
      ...projected,
      items: attachToSources([...pageRows, ...projected.items]),
    };
  }

  // What a paged row still owns once the projection has caught up with part of
  // it. A row is one identity for most kinds — it duplicates the projection or it
  // does not — but a clustered activity row IS its members, and the projection
  // growing to hold ONE of them makes only that member a duplicate. Dropping the
  // whole row would delete history nobody else has; keeping it whole would show
  // that member twice. So the cluster is rebuilt from the members the projection
  // does not hold, exactly the way the projector builds one (project.ts's
  // clusterActivityRun: identity, label and detail come from the first
  // member, the state is running when any member runs, and a single member
  // is a plain activity row rather than a cluster of one).
  //
  // duplicates() asks supersededBy against `identities` (every projected
  // row's own identity) and `projectedAttachmentSources` (narrow: only rows
  // the snapshot has ALREADY reprojected as an attachment) — narrow because a
  // source row the snapshot has reprojected with no attachment of its own yet
  // must not supersede a page's only copy.
  function retainedPageRow(
    item: MobileTimelineItem,
    identities: ReadonlySet<string>,
    projectedAttachmentSources: ReadonlySet<string>,
  ): MobileTimelineItem | null {
    const duplicates = (candidate: MobileTimelineItem): boolean =>
      supersededBy(candidate, identities, projectedAttachmentSources);
    if (item.kind !== "activity" || item.members === undefined) {
      return duplicates(item) ? null : item;
    }
    // Each member is judged on its own page ownership, not the row's: a
    // cluster that grew across the page/live boundary would otherwise let
    // its page-owned first member carry a live member the authoritative
    // snapshot just dropped back onto the screen as "history". Every page
    // cluster member is recorded individually (loadOlder unions
    // ownTimelineIdentities per row), so real page clusters keep their
    // members; only the non-page riders drop.
    const members = item.members.filter(
      (member) =>
        !identities.has(activityIdentity(member)) &&
        pageOwnedIds.has(activityIdentity(member)),
    );
    if (members.length === item.members.length) return duplicates(item) ? null : item;
    const first = members[0];
    if (first === undefined) return null;
    // Every identity-bearing field comes from the new first member, including
    // the absence of one: spreading `item` would keep the SUPERSEDED member's
    // transcriptKey and position, which name what the projection now holds, so
    // the next publish would read this row as a duplicate and drop the history
    // it still carries.
    return {
      ...item,
      id: first.id,
      label: first.label,
      family: first.family,
      detail: first.detail,
      state: members.some((member) => member.state === "running") ? "running" : "completed",
      transcriptKey: first.transcriptKey,
      position: first.position,
      ...(members.length === 1 ? { members: undefined } : { members }),
    };
  }

  // Prune page ownership for identities the displayed rows no longer carry:
  // once the cap has trimmed a row, its page entry is stale and a later
  // re-introduction must be judged on its own.
  function pruneEvictedIds(items: MobileTimelineItem[]): void {
    if (pageOwnedIds.size === 0) return;
    const retainedIds = new Set(items.flatMap((item) => [...timelineIdentities(item)]));
    for (const id of [...pageOwnedIds]) {
      if (!retainedIds.has(id)) pageOwnedIds.delete(id);
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
    itemFoldIdentities?: WeakMap<ItemModel, ReadonlySet<string>>,
    activeTurnId?: string,
  ): TurnModel[] {
    // RoboRev round 29: the row side carries bare ids too, not just each
    // row's key-first identity. A KEYLESS backing item matches a keyed row
    // by bare id under the package's own rule (itemIdentityMatches falls to
    // the id when one side carries no key), so a keyed row that contributed
    // only its transcript key left that item outside the window and the
    // bound shed the payload behind a row still on screen. Clustered
    // members and attachment sources follow the same rule, and a bare-id
    // hit against a row the item conflicts with on transcript key only
    // over-keeps — the same safe direction the item-side check below takes.
    const retainedIdentities = new Set<string>();
    for (const row of retainedItems) {
      for (const identity of timelineIdentities(row)) retainedIdentities.add(identity);
      retainedIdentities.add(row.id);
      if (row.kind === "activity" && row.members) {
        for (const member of row.members) retainedIdentities.add(member.id);
      }
      const source = attachmentSourceId(row);
      if (source !== null) retainedIdentities.add(source);
    }
    let trimmed = false;
    const bounded = turns.map((turn) => {
      if (turn.items.length === 0) return turn;
      // Review round 26: the active turn's payloads are the live working
      // set, not retained history. The dual-write row appliers lag the
      // model half — a steering append has no row applier yet — so the
      // bound would trim live items before any display row exists to back
      // them, and every caller runs it, not just publishModel: an
      // in-flight page or rehydrate can land mid-stream. Skipping before
      // the bookkeeping also keeps the live turn out of the compact sets.
      // The exemption ends when the turn settles: a completion clears
      // activeTurnId, and the next bound pass compacts it like any
      // settled turn.
      if (activeTurnId !== undefined && turn.id === activeTurnId) return turn;
      // Item identity is the package's own rule (itemIdentityMatches:
      // transcriptKey when both sides carry one, else id) approximated from
      // above: a retained row keeps the turn that could supply it alive
      // whether it matches by transcript key or by bare id — the same rule
      // mergeTurnHistory matches fragments by, clustered members included.
      // Both sides read from above now that the row side carries bare ids:
      // an item can hit a bare id whose row it conflicts with on transcript
      // key, but a false hit only over-keeps — the safe direction for merge
      // parity (round 29).
      // Review round 14: a merged item's own identity is not the only one
      // that backs a row — its fold sources' identities do too. An alias
      // chain can settle content on an identity no row carries while the
      // row still names the keyless id the chain consumed, so a turn whose
      // items all match by their own identities alone can still back a
      // visible row through the sources those items folded from (review
      // round 15: those sources are remembered as identity strings).
      const inWindow = turn.items.some(
        (item) =>
          retainedIdentities.has(item.transcriptKey ?? item.id) ||
          retainedIdentities.has(item.id) ||
          [...(itemFoldIdentities?.get(item) ?? [])].some((identity) => retainedIdentities.has(identity)),
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
        markItemIdentityOnly(
          markItemTextOmitted({
            id: item.id,
            turnId: item.turnId,
            type: item.type,
            text: "",
            ...(item.transcriptKey !== undefined ? { transcriptKey: item.transcriptKey } : {}),
            ...(item.position !== undefined ? { position: item.position } : {}),
            ...(item.callId !== undefined ? { callId: item.callId } : {}),
          }),
        ),
    );
  }

  // The dedupe key of a remembered skeleton: composite identity, so two
  // skeletons that share an id but differ on transcript key (or vice versa)
  // stay distinct entries while a re-shed of the same item replaces its own
  // entry instead of growing the set.
  function skeletonKey(skeleton: ItemModel): string {
    return `${skeleton.id}\u0000${skeleton.transcriptKey ?? ""}`;
  }

  // Each merged item's folded-from identities, remembered past the merge
  // that produced them. The keep-window check needs them (review round 14):
  // a fold can land content on an identity the display rows never carried —
  // an alias chain settles on the second alias's keyed identity while the
  // page's row still names the keyless id the chain started from — so
  // whether a merged turn backs a visible row can only be answered through
  // the identities its items folded FROM, and the live-path bound sites ask
  // long after the merge is gone. Review round 15: only the identity
  // strings are remembered, never the source items — holding the sources
  // themselves would keep every superseded payload of an alias chain alive
  // for exactly as long, the retention this bound exists to close — and an
  // untouched item keeps the identities an earlier merge already recorded,
  // so a later unrelated merge cannot erase a prior chain's aliases, while
  // a source that was itself a merged item contributes the identities IT
  // folded from (the per-merge provenance does not chain across merges).
  // Review round 16: the results the tool fold absorbed onto a rewritten
  // call land in the same memory — a folded call is the only payload
  // behind its result's row once the row set keeps the result but not the
  // call — and notification replacements re-key it (below), so a live
  // update cannot orphan a recorded chain.
  const mergedItemFoldIdentities = new WeakMap<ItemModel, ReadonlySet<string>>();
  function recordItemFoldSources(
    turns: TurnModel[],
    itemFoldSources: (item: ItemModel) => readonly ItemModel[],
    toolResultFoldSources: (item: ItemModel) => readonly ItemModel[],
  ): void {
    const addIdentities = (identities: Set<string>, source: ItemModel): void => {
      identities.add(source.transcriptKey ?? source.id);
      identities.add(source.id);
      for (const carried of mergedItemFoldIdentities.get(source) ?? []) identities.add(carried);
    };
    for (const turn of turns) {
      for (const item of turn.items) {
        const sources = itemFoldSources(item);
        // The results the tool fold absorbed onto this item. Only the
        // keep-window reads them — the strip's real-source test and the
        // reconciliation's freshness must not see call-precedence
        // candidates as fold sources.
        const absorbed = toolResultFoldSources(item);
        // Untouched — no fold combined anything into it. The window check
        // already reads the item's own fields; recording the entry would
        // overwrite the identities an earlier merge remembered for it.
        if (sources.length === 1 && sources[0] === item && absorbed.length === 0) continue;
        const identities = new Set(mergedItemFoldIdentities.get(item));
        for (const source of sources) addIdentities(identities, source);
        for (const result of absorbed) addIdentities(identities, result);
        if (identities.size > 0) mergedItemFoldIdentities.set(item, identities);
      }
    }
  }

  // Replacements re-key the identity-string ancestry, which is keyed by
  // object. Each replacement is built off the model item its producer
  // found by identity — the package's notification folds (a streaming
  // delta, a settlement, a full-view settle; review round 16) and the
  // merge's no-op path, which returns the freshly hydrated items directly
  // when the retained side contributes nothing (review round 23) — so it
  // carries the same identity the entry was recorded under: re-key the
  // ancestry to the replacements, or the fold memory is silently orphaned
  // and the next bound pass trims the turn whose row is still visible.
  // Fill-only: an item that already carries an entry keeps it.
  function carryFoldIdentities(before: readonly TurnModel[], after: readonly TurnModel[]): void {
    const remembered = new Map<string, ReadonlySet<string>>();
    for (const turn of before) {
      for (const item of turn.items) {
        const identities = mergedItemFoldIdentities.get(item);
        if (identities === undefined) continue;
        for (const key of [item.transcriptKey ?? item.id, item.id]) {
          const existing = remembered.get(key);
          remembered.set(key, existing === undefined ? identities : new Set([...existing, ...identities]));
        }
      }
    }
    if (remembered.size === 0) return;
    for (const turn of after) {
      for (const item of turn.items) {
        if (mergedItemFoldIdentities.get(item) !== undefined) continue;
        const identities = remembered.get(item.transcriptKey ?? item.id) ?? remembered.get(item.id);
        if (identities !== undefined) mergedItemFoldIdentities.set(item, identities);
      }
    }
  }

  // Conservative collision scan: which compact turns remember an identity
  // the incoming side carries? The package's itemIdentityMatches rule
  // (transcriptKey when both sides carry one, else id) is approximated from
  // above by testing both fields — a false collision only injects skeletons
  // the package then fails to match and the strip removes, so the common
  // no-collision case costs one lookup per remembered identity and never
  // over-folds. RoboRev panel follow-up (#2152): callId is a collision
  // dimension of its own between remembered TOOL skeletons and incoming tool
  // call/result items. A result-only fragment re-serves the RESULT of a call
  // the compact turn remembers — the callId fold already collapses that pair
  // into one item pre-compaction, so the turn remembers only the call
  // skeleton and NO remembered identity names the result. Without this
  // dimension the fragment survives as its own turn beside the host. Both
  // sides are tool-classified by the package's own id rule: the callId fold
  // only ever folds tool calls with tool results, so a non-tool item sharing
  // a callId string is not a fold candidate, and a false collision still only
  // injects skeletons the package fails to match and the strip removes.
  // The incoming side's tool call ids for that second dimension: a compact
  // turn's remembered tool skeleton collides when an incoming tool call or
  // tool result item shares its callId, because the package's callId fold is
  // the machinery that would merge them.
  function addIncomingToolCallId(item: { id: string; callId?: string }, callIds: Set<string>): void {
    if (item.callId === undefined) return;
    if (isToolCallItemId(item.id) || isToolResultItemId(item.id)) callIds.add(item.callId);
  }

  function compactedTurnsCollidingWith(identities: Set<string>, toolCallIds: ReadonlySet<string>): Set<string> {
    const colliding = new Set<string>();
    if (compactedTurnItems.size === 0) return colliding;
    for (const [turnId, skeletons] of compactedTurnItems) {
      for (const skeleton of skeletons) {
        if (
          identities.has(skeleton.transcriptKey ?? skeleton.id) ||
          identities.has(skeleton.id) ||
          (skeleton.callId !== undefined &&
            (isToolCallItemId(skeleton.id) || isToolResultItemId(skeleton.id)) &&
            toolCallIds.has(skeleton.callId))
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
        pageOwnedIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
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
          // D18 B3 round 8: the store's own paging cursor at entry. A racing
          // loadOlder that succeeds with no retained rows still advances this
          // value; a failed one moves it not at all — the difference the
          // cursor merge below reads to keep a successful page's advancement
          // without letting a failed page pin a stale pre-race cursor.
          const entryOlderCursor = get().olderCursor;
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
          // The snapshot is authoritative for every row it carries (the
          // response-cut note by applyThreadNotification): a frame this store
          // folded while the read was in flight is already in it. The one
          // thing the snapshot cannot know about is older history this client
          // paged in, so page-owned rows the snapshot omits are prepended and
          // the page's own cursor is kept. D23d moves the pages into the model
          // and this merge goes with them.
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
          const merged = preservePageHistory
            ? withPageHistory(currentConvForMerge, conversation)
            : conversation;
          // The page's cursor is the newer one when its history is kept: the
          // reread's reflects the full readProjection, which does not include
          // the paged rows.
          let mergedCursor = preservePageHistory
            ? currentSnapshot.olderCursor
            : olderCursor;
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
          const rehydrateCapped = capItems(merged.items);
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
            const freshToolCallIds = new Set<string>();
            for (const turn of conversation.turns) {
              for (const item of turn.items) {
                freshIdentities.add(item.transcriptKey ?? item.id);
                freshIdentities.add(item.id);
                addIncomingToolCallId(item, freshToolCallIds);
              }
            }
            // Two rules drop retained items before the merge, both about
            // transients no authoritative read can justify keeping.
            //
            // Rule 1 — the fresh read covers a turn when it carries the
            // turn's id OR shares an item identity with it (the merge
            // folds turns through shared items too, so a turn reissued
            // under a new id is just as covered — RoboRev round 7). On
            // the retained side of a covered turn, an item the fresh
            // read neither re-issues nor matches is a live twin the
            // snapshot supersedes (the response-cut contract: a frame
            // folded during the read is in the snapshot that arrives),
            // and the merge would otherwise keep it beside the canonical
            // copy — under a live id with no transcript key to match, a
            // re-served steering item used to come back as a twin, and
            // the next row-changing frame projected both. Matched items
            // stay (they are the merge's own fold inputs) and page items
            // stay — page ownership is item-level here, so a page
            // fragment of the LIVE turn paging in cannot hide that
            // turn's live twins behind the page exemption (RoboRev
            // round 8). Turns the fresh read does not cover keep
            // everything: they are the page history this block exists
            // to preserve, their folds run through the remembered
            // aliases below, and their out-of-window payloads compact
            // in the bound afterwards.
            //
            // Rule 2 — a retained warning outside page ownership is a
            // transient: the transcript never persists warnings, so no
            // authoritative read can ever justify keeping one, while the
            // merge would otherwise fold an unmatched retained warning
            // back into the committed model — and the next row-changing
            // frame would project it onto the screen again. A warning
            // whose failure row the PAGE owns is retained history like any
            // other page row (the round-31 coverage rule reads exactly
            // that), so only the non-page warnings drop.
            const freshTurnIds = new Set(conversation.turns.map((turn) => turn.id));
            const freshItemByKey = new Map<string, ItemModel>();
            const freshItemById = new Map<string, ItemModel>();
            // The containing turn's status recorded PER ITEM, not keyed
            // through item.turnId — the wire can omit turnId, and
            // hydration turns the omission into "" (RoboRev round 15).
            const freshTurnStatusByKey = new Map<string, string | undefined>();
            const freshTurnStatusById = new Map<string, string | undefined>();
            for (const turn of conversation.turns) {
              for (const item of turn.items) {
                freshItemByKey.set(item.transcriptKey ?? item.id, item);
                freshItemById.set(item.id, item);
                freshTurnStatusByKey.set(item.transcriptKey ?? item.id, turn.status);
                freshTurnStatusById.set(item.id, turn.status);
              }
            }
            // Snapshot matching goes through the package's own identity
            // rule — itemIdentityMatches — never a bare-id set read: two
            // items that both carry transcript keys match by key alone,
            // so a reissued item sharing the bare id under a NEW key is a
            // distinct item, and a bare-id lookup would wrongly treat the
            // retained keyed copy as matched — hiding it from the twin
            // filter and resurrecting the obsolete row beside its
            // replacement (RoboRev round 17). Coverage, the retained-item
            // filter, and the reconciliation strips below all read this
            // one helper.
            const snapshotMatchFor = (
              item: ItemModel,
            ): { fresh: ItemModel; turnStatus: string | undefined } | undefined => {
              const byKey = freshItemByKey.get(item.transcriptKey ?? item.id);
              if (byKey !== undefined && itemIdentityMatches(item, byKey)) {
                return {
                  fresh: byKey,
                  turnStatus: freshTurnStatusByKey.get(item.transcriptKey ?? item.id),
                };
              }
              const byId = freshItemById.get(item.id);
              if (byId !== undefined && itemIdentityMatches(item, byId)) {
                return { fresh: byId, turnStatus: freshTurnStatusById.get(item.id) };
              }
              return undefined;
            };
            const turnCoveredBySnapshot = (turn: TurnModel): boolean => {
              return (
                freshTurnIds.has(turn.id) ||
                turn.items.some((item) => snapshotMatchFor(item) !== undefined)
              );
            };
            // Rule 3 — a retained item's pending delta chunks are the
            // response streaming ahead of the transcript: the snapshot's
            // settled text folds every chunk the wire received before its
            // cut, so those chunks are stale on the retained side — the
            // item merge spreads hydrated items over retained ones
            // ({ ...older, ...newer }) and a hydrated item omits
            // pendingText entirely, which kept the stale chunks riding
            // beside the text that already contains them, re-appended by
            // every later publish (RoboRev round 5). Only the leading run
            // of chunks the snapshot's text provably ends with strips:
            // a chunk the text does not end with is still ahead of the
            // response (the read was cut before the wire folded it), and
            // stays live for the stream to continue on.
            const stripSettledChunks = (item: ItemModel): ItemModel => {
              const pending = item.pendingText;
              if (pending === undefined || pending.length === 0) return item;
              const match = snapshotMatchFor(item);
              if (match === undefined) return item;
              const fresh = match.fresh;
              // A fresh item whose text the read omitted says nothing
              // about the stream: the chunks stay live whatever the
              // retained base reads.
              if (itemTextPresence(fresh) !== "provided") return item;
              // A SETTLED snapshot item — completed or failed, text
              // provided — is the whole response: its text has no room
              // for chunks appended to any earlier base, prefix or no
              // prefix ("Hello there" against base "Hello" with pending
              // [" world"] used to keep the chunk and render "Hello
              // there world" — RoboRev round 11).
              //
              // While the item still STREAMS, reconcile position-
              // relative against the retained base, not by a suffix
              // check on the full text: the wire folds chunks in order,
              // so the snapshot's text is the retained base plus
              // everything it folded before the cut — and the cut can
              // sit AHEAD of the chunks the client received (base
              // "Hello", pending [" world"], text "Hello world!"),
              // which a suffix scan cannot see (RoboRev rounds 6-7).
              // The largest run of chunks the advance starts with is
              // exactly what the wire settled — walked once at an
              // advancing offset (RoboRev round 8: slice-and-join per
              // prefix was quadratic in the chunk count).
              let settled = 0;
              // The snapshot item counts as settled by the package's own
              // activity rule — item status with the containing turn's as
              // the fallback — so a statusless item in a completed turn
              // settles too (RoboRev round 14) — with the containing
              // turn recorded per item, so an omitted turnId cannot
              // read as a missing one (round 15).
              const snapshotSettled = !isActiveItem(fresh, match.turnStatus);
              if (snapshotSettled) {
                settled = pending.length;
              } else if (fresh.text.startsWith(item.text)) {
                const advance = fresh.text.slice(item.text.length);
                let offset = 0;
                for (const chunk of pending) {
                  if (!advance.startsWith(chunk, offset)) break;
                  offset += chunk.length;
                  settled += 1;
                }
                if (settled < pending.length && offset < advance.length) {
                  // The walk stopped at a chunk the advance cannot
                  // account for, while the advance still holds unmatched
                  // text — the wire diverged from the chunk stream after
                  // the matched prefix (round 13), or before any of it
                  // (round 12): either way the remaining chunks belong
                  // to a generation the wire already abandoned. A walk
                  // that consumed the whole advance is the honest
                  // partial fold — the trailing chunks are still ahead
                  // of the cut and stay live.
                  settled = pending.length;
                }
              } else {
                // The snapshot's provided text does not advance the
                // retained base: an authoritative replacement (or an
                // explicitly empty settle) has no home for chunks
                // appended to the old base — clear them all (RoboRev
                // round 10).
                settled = pending.length;
              }
              if (settled === 0) return item;
              const live = pending.slice(settled);
              return live.length === 0
                ? // The spread drops the reducer's non-enumerable
                  // omitted-text marker; a stripped sparse item would
                  // read as an authoritative empty settle and block the
                  // page that later brings its real text (RoboRev
                  // round 9).
                  copyItemTextPresence(item, { ...item, pendingText: undefined })
                : copyItemTextPresence(item, { ...item, pendingText: live });
            };
            // Rule 4 — the wire's copy of a matched non-page item is
            // authoritative for the content payloads it carries: the item
            // merge falls back to the retained value whenever the fresh
            // side omits one (newer.field ?? older.field), so a payload
            // the wire WITHDREW — a live input image a snapshot no longer
            // carries — used to ride the merge back and the next
            // row-changing frame resurrected it (RoboRev round 6).
            // Fields a fresh re-issue never carries by construction —
            // pending chunks, reasoning summaries, observed timings, wire
            // timestamps a live frame can stamp — keep their fallback:
            // those are local/live state the snapshot cannot speak for.
            // Page-owned items keep everything (the round-31 rule: the
            // page's retained history is not the snapshot's to withdraw).
            const SNAPSHOT_AUTHORITY_FIELDS = [
              "images",
              "outputImages",
              "output",
              "error",
              "raw",
              "prevalOnly",
              "exitCode",
              "argumentsJSON",
              "description",
              "toolName",
              "callId",
              "eventKind",
              "steeringKind",
              "source",
            ] as const;
            const applySnapshotAuthority = (item: ItemModel): ItemModel => {
              const match = snapshotMatchFor(item);
              if (match === undefined) return item;
              const fresh = match.fresh;
              let stripped: ItemModel | undefined;
              // Payloads stay the page's to withdraw-or-keep (the
              // round-31 rule), but the LIFECYCLE below applies to
              // matched page-owned items too: a paged item running
              // locally cannot outlive the fresh copy that settled
              // (RoboRev round 17).
              if (!pageOwnedIds.has(item.transcriptKey ?? item.id)) {
                for (const field of SNAPSHOT_AUTHORITY_FIELDS) {
                  const retained = (item as unknown as Record<string, unknown>)[field];
                  const settledOnFresh = (fresh as unknown as Record<string, unknown>)[field];
                  if (retained === undefined || settledOnFresh !== undefined) continue;
                  // Same marker rule as the chunk strip above: the clone
                  // keeps the item's omitted-text presence.
                  stripped ??= copyItemTextPresence(item, { ...item });
                  (stripped as unknown as Record<string, unknown>)[field] = undefined;
                }
              }
              // The lifecycle is the snapshot's to settle too: a retained
              // inProgress claim cannot outlive a fresh copy the activity
              // rule reads as settled — the rank merge would keep the
              // stale claim and every later frame would reproject
              // "Writing…" on the finished response (RoboRev round 16).
              if (
                item.status === "inProgress" &&
                !isActiveItem(fresh, match.turnStatus)
              ) {
                stripped ??= copyItemTextPresence(item, { ...item });
                (stripped as unknown as Record<string, unknown>).status = undefined;
              }
              return stripped ?? item;
            };
            const retainedTurnsForMerge = currentConvForMerge.turns.map(
              (turn) => {
                const afterTwins = turnCoveredBySnapshot(turn)
                  ? turn.items.filter(
                      (item) =>
                        pageOwnedIds.has(item.transcriptKey ?? item.id) ||
                        snapshotMatchFor(item) !== undefined,
                    )
                  : turn.items;
                const kept = afterTwins.filter(
                  (item) =>
                    item.type !== "warning" ||
                    pageOwnedIds.has(item.transcriptKey ?? item.id),
                );
                const stripped = kept
                  .map(stripSettledChunks)
                  .map(applySnapshotAuthority);
                return kept.length === turn.items.length &&
                  stripped.every((item, index) => item === kept[index])
                  ? turn
                  : { ...turn, items: stripped };
              },
            );
            const injectedFresh = injectCompactedSkeletons(
              retainedTurnsForMerge,
              compactedTurnsCollidingWith(freshIdentities, freshToolCallIds),
            );
            const history = mergeTurnHistoryWithFolds(injectedFresh.turns, conversation.turns);
            mergedTurns = history.turns;
            // Review round 3: the wire-cursor gate must read only RETAINED
            // transcript evidence — memory must not let discarded history
            // override the fresh wire cursor. Rounds 3/7/8 answered that
            // with a skeleton-free re-merge that drops compact-only turns
            // from its inputs, and the package's own coverage semantics
            // decide every claim that re-merge sees: canonical fields the
            // fresh matches lack, unmatched empty turns' usage, warnings
            // never claiming, per-item field survival. RoboRev round 30:
            // the re-merge lost the ALIAS relationships the real merge saw
            // through the injected skeletons — a compact turn's item can
            // return keyed under a NEW bare id (the hub reissues under a
            // new wire id while the transcript key stands), a complete
            // reread can then re-serve the same content KEYLESS under the
            // ORIGINAL id, and the re-merge, matching nothing, claimed the
            // restored turn as uncovered history and let the stale
            // retained cursor override the complete reread's absent one.
            // Round 31: the re-merge stays the claim authority, and the
            // real merge's own membership only EXCLUDES a turn it proved
            // fully consumed through remembered aliases — every real item
            // folded into an output a fresh source also reached, and every
            // canonical field the fresh side of its group supplies. Such a
            // turn is the reread's own content; anything less keeps the
            // re-merge's verdict, so omitted usage, unmatched empty turns
            // and warnings still claim exactly as the package computes.
            // The re-merge itself never sees a skeleton (round 3), and
            // compact-only turns stay excluded outright (rounds 7-8,
            // whether or not a collision injected anything).
            const coverageFreshItemRefs = new Set(
              conversation.turns.flatMap((turn) => turn.items),
            );
            const coverageInjectedRefs = new Set(injectedFresh.injected);
            // The merge's no-op path returns the fresh items by reference
            // with no membership recorded (round 23): the retained side
            // contributed nothing there, so no turn can be alias-consumed
            // and the re-merge settles the gate exactly as before.
            const coverageMergeNoOp = history.turns === conversation.turns;
            const coverageOutputCarriesFresh = new Set<ItemModel>();
            const coverageOutputFreshSources = new Map<ItemModel, ItemModel[]>();
            const coverageRetainedItemOutput = new Map<ItemModel, ItemModel>();
            if (!coverageMergeNoOp) {
              for (const turn of history.turns) {
                for (const item of turn.items) {
                  for (const source of history.itemFoldSources(item)) {
                    if (coverageFreshItemRefs.has(source)) {
                      coverageOutputCarriesFresh.add(item);
                      const freshSources = coverageOutputFreshSources.get(item) ?? [];
                      freshSources.push(source);
                      coverageOutputFreshSources.set(item, freshSources);
                    } else if (!coverageInjectedRefs.has(source)) {
                      coverageRetainedItemOutput.set(source, item);
                    }
                  }
                }
              }
            }
            // A retained turn's fresh matches in the REAL merge: the fresh
            // turns of the group it folded into (an unmatched turn's group
            // holds only itself, so it has none).
            const coverageFreshTurnsById = new Map(
              conversation.turns.map((turn) => [turn.id, turn]),
            );
            const coverageTurnOutputId = new Map<string, string>();
            for (const [outputId, olderTurnIds] of history.olderTurnFolds) {
              for (const olderTurnId of olderTurnIds) {
                coverageTurnOutputId.set(olderTurnId, outputId);
              }
            }
            const coverageAliasConsumed = (turn: TurnModel): boolean => {
              if (coverageMergeNoOp) return false;
              // An empty turn can only have matched by turn id — the
              // re-merge sees that match itself, and its group membership
              // is what supplies the re-merge's overlap evidence.
              if (turn.items.length === 0) return false;
              for (const item of turn.items) {
                if (coverageInjectedRefs.has(item)) continue;
                const output = coverageRetainedItemOutput.get(item);
                if (output === undefined || !coverageOutputCarriesFresh.has(output)) {
                  return false;
                }
                // A DIRECT identity match with a fresh source of the same
                // output: the re-merge sees it too, so the turn keeps its
                // re-merge membership — its matched items are what supply
                // the re-merge's transcript-overlap evidence.
                const freshSources = coverageOutputFreshSources.get(output) ?? [];
                if (freshSources.some((fresh) => itemIdentityMatches(item, fresh))) {
                  return false;
                }
              }
              const outputId = coverageTurnOutputId.get(turn.id);
              if (outputId === undefined) return false;
              const freshMatches = history.newerTurnFolds.get(outputId) ?? [];
              // The package's turnCoverageFields: every field merges with
              // ?? in mergePageTurn, so null and undefined both read as
              // absent for them (absentForCoverage with nullishMerged).
              for (const field of ["startedAt", "completedAt", "durationMs", "usage", "cost", "error"] as const) {
                const retainedValue = turn[field];
                if (retainedValue === undefined || retainedValue === null) continue;
                const supplied = freshMatches.some((freshTurnId) => {
                  const freshTurn = coverageFreshTurnsById.get(freshTurnId);
                  return freshTurn !== undefined && freshTurn[field] !== undefined && freshTurn[field] !== null;
                });
                if (!supplied) return false;
              }
              return true;
            };
            const coverageOlderTurns = retainedTurnsForMerge.filter(
              (turn) =>
                !(turn.items.length === 0 && compactedTurnItems.has(turn.id)) &&
                !coverageAliasConsumed(turn),
            );
            const coverage =
              injectedFresh.injected.length > 0 ||
              coverageOlderTurns.length !== retainedTurnsForMerge.length
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
              ...retainedTurnsForMerge.flatMap((turn) => turn.items),
              ...conversation.turns.flatMap((turn) => turn.items),
            ];
            mergedTurns = stripInjectedSkeletons(
              mergedTurns,
              injectedFresh.injected,
              rehydrateRealSources,
              history.itemFoldSources,
            );
            // The merge's no-op path returns the freshly hydrated items
            // directly when the retained side contributes nothing — new
            // objects no fold recorded — so an unchanged reread would
            // silently drop the ancestry the retained items they replace
            // carried: re-key it to the identity-matching fresh items
            // before recording (review round 23).
            carryFoldIdentities(retainedTurnsForMerge, mergedTurns);
            recordItemFoldSources(mergedTurns, history.itemFoldSources, history.toolResultFoldSources);
            transferFoldedCompactedEntries(mergedTurns, history.olderTurnFolds);
            // #1919 follow-up: bound the merged result AFTER the merge, so
            // turns inside the keep-window keep everything the older
            // fragments supplied, and only out-of-window payloads trim. The
            // final retained rows (rehydrateCapped) are the window; the pass
            // settles page turn ownership with the same bound.
            mergedTurns = boundRetainedTurns(
              mergedTurns,
              rehydrateCapped,
              mergedItemFoldIdentities,
              conversation.activeTurnId,
            );
          }
          if (replacesInstance) {
            pageOwnedIds.clear();
            pageOwnedTurnIds.clear();
            pageOwnedCompactTurnIds.clear();
            compactedTurnItems.clear();
          }
          // The snapshot's thread-level fields are authoritative (see the
          // response-cut note by applyThreadNotification); the rows are its
          // own, plus the page history prepended above.
          const committedConversation = capAndTruncate({
            ...merged,
            turns: mergedTurns,
            olderCursor: wireOlderCursor,
          });
          pruneEvictedIds(committedConversation.items);
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
            // dropped, keeping the newer (live tail) version. `currentIds` (the
            // conversation's own identities, frozen before this loop) is
            // supersededBy's SECOND, attachment-source set here — broad,
            // unlike retainedPageRow's: a bare source row already in the live
            // conversation means the live model has moved past this position,
            // so an older page's attachment for that source is stale even
            // with no attachment row alongside it.
            // Task 2A-Items: also dedupe within the incoming page by updating
            // the seen set during traversal, preserving order and first
            // occurrence semantics.
            const existingIds = new Set(
              currentConv.items.flatMap((item) => [...timelineIdentities(item)]),
            );
            const currentIds = new Set(existingIds);
            const deduped: MobileTimelineItem[] = [];
            for (const item of result.items) {
              if (supersededBy(item, existingIds, currentIds)) continue;
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
            // Every identity the row IS, members included: a cluster rebuilt
            // around a later member (retainedPageRow) must still read as page
            // history on the next publish.
            for (const item of deduped) {
              for (const id of ownTimelineIdentities(item)) pageOwnedIds.add(id);
            }
            // Prepend older (deduped) items, then trim from the oldest (front)
            // so the newest live tail is retained (finding 8); the same cap and
            // byte bound every other publish applies. mergedInput is the
            // pre-cap merged set the at-cap signal below reads (F8): the
            // overflow, never the final row count, is what ends paging.
            const mergedInput = [...deduped, ...currentConv.items];
            const pageMerged = capItems(mergedInput);
            const pageConversation = capAndTruncate({
              ...currentConv,
              items: pageMerged,
            });
            const merged = pageConversation.items;
            // Prune page ownership for IDs the cap dropped, so a later page
            // load or re-introduction is judged on its own.
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
            const pageToolCallIds = new Set<string>();
            for (const turn of result.turnsPage?.data ?? []) {
              for (const item of turn.items ?? []) {
                pageIdentities.add(item.transcriptKey ?? item.id);
                pageIdentities.add(item.id);
                addIncomingToolCallId(item, pageToolCallIds);
              }
            }
            const injectedPage = injectCompactedSkeletons(
              currentConv.turns,
              compactedTurnsCollidingWith(pageIdentities, pageToolCallIds),
            );
            const mergeConv: MobileConversation =
              injectedPage.injected.length > 0
                ? { ...currentConv, turns: injectedPage.turns }
                : currentConv;
            // The row-level dedupe above already treats an older page's
            // attachment for a source the live conversation carries as
            // stale — "the live model has moved past this position." The
            // model merge must honor the same precedence, or its
            // nullish fallback (newer.images ?? older.images) would fold
            // the page's image back into the model behind the row
            // filter's back, and the next row-changing frame would
            // resurrect the attachment the filter rejected (RoboRev
            // round 10). A page item matched to a live NON-PAGE item
            // therefore loses the attachment payloads the live item
            // omits; page-owned identities keep everything (the round-31
            // rule — and a page fragment supplementing a page row is
            // exactly the #1919 restoration this block exists for).
            const liveItemByKey = new Map<string, ItemModel>();
            const liveItemById = new Map<string, ItemModel>();
            for (const turn of currentConv.turns) {
              for (const item of turn.items) {
                liveItemByKey.set(item.transcriptKey ?? item.id, item);
                liveItemById.set(item.id, item);
              }
            }
            const pageTurnsForMerge = (result.turnsPage?.data ?? []).map(
              (turn) => {
                if (turn.items === undefined) return turn;
                const items = turn.items.map((wireItem) => {
                  const identity = wireItem.transcriptKey ?? wireItem.id;
                  if (pageOwnedIds.has(identity)) return wireItem;
                  const live =
                    liveItemByKey.get(identity) ??
                    liveItemById.get(wireItem.id);
                  if (live === undefined) return wireItem;
                  let stripped: ThreadItem | undefined;
                  for (const field of ["images", "outputImages"] as const) {
                    const pageValue = (wireItem as unknown as Record<string, unknown>)[field];
                    const liveValue = (live as unknown as Record<string, unknown>)[field];
                    if (pageValue === undefined || liveValue !== undefined) continue;
                    stripped ??= { ...wireItem };
                    (stripped as unknown as Record<string, unknown>)[field] = undefined;
                  }
                  return stripped ?? wireItem;
                });
                return { ...turn, items };
              },
            );
            const turnsPageForMerge = result.turnsPage
              ? { ...result.turnsPage, data: pageTurnsForMerge }
              : result.turnsPage;
            const pageMerge = result.turnsPage
              ? mergeOlderItemPageWithFolds(
                  mergeConv,
                  turnsPageForMerge ?? result.turnsPage,
                )
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
            if (pageMerge)
              recordItemFoldSources(
                strippedPageTurns,
                pageMerge.folds.itemFoldSources,
                pageMerge.folds.toolResultFoldSources,
              );
            // The compact turns are the merge's NEWER (retained) side here;
            // the retained-side folds name the carrier a bridged compact
            // turn's content landed in.
            transferFoldedCompactedEntries(strippedPageTurns, pageMerge?.folds.newerTurnFolds);
            // #1919 follow-up: bound the retained turn payloads against the
            // final retained rows (pageMerged), after the merge — the pass
            // prunes pageOwnedTurnIds with the same bound, moving a page turn
            // whose payloads left the keep-window to the compact set.
            const boundedTurns = boundRetainedTurns(
              strippedPageTurns,
              pageMerged,
              mergedItemFoldIdentities,
              currentConv.activeTurnId,
            );
            set({
              // conversation.olderCursor is the wire truth (result.nextCursor),
              // never the capped nextCursor above.
              // atCap only stops the STORE's own paging honestly (F8); it says
              // nothing about whether the daemon actually has more history, so
              // sessionTokens must not read it as "this is the whole session".
              conversation: {
                ...pageConversation,
                turns: boundedTurns,
                olderCursor: result.nextCursor,
              },
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
        // An explicit reset is the wire's one frame that REMOVES a model
        // item the window holds. The page-history layer keys on
        // pageOwnedIds, and withPageHistory reads every page-owned
        // identity the projection no longer carries as history to keep —
        // without dropping the retracted row's page entry here, the
        // publish would resurrect the very row the reset removed.
        if (n.method === "item/agentMessage/reset") {
          const params = n.params as { itemId: string };
          const target = state.conversation.turns
            .flatMap((turn) => turn.items)
            .find((item) => item.id === params.itemId);
          if (target) {
            pageOwnedIds.delete(target.transcriptKey ?? target.id);
          }
        }
        // An active turn's FULL completion is the other frame that
        // removes model items: the stamp's item list replaces the turn's,
        // so an item the stamp omits is withdrawn exactly like a reset —
        // and its page entries must go with it, or withPageHistory would
        // resurrect the row the frame withdrew (RoboRev round 18).
        if (n.method === "turn/completed") {
          const params = n.params as {
            turn?: { id?: string; itemsView?: string; items?: ThreadItem[] };
          };
          const stamp = params.turn;
          if (
            stamp?.itemsView === "full" &&
            stamp.id !== undefined &&
            state.conversation.activeTurnId === stamp.id
          ) {
            const oldTurn = state.conversation.turns.find(
              (turn) => turn.id === stamp.id,
            );
            // The stamp's item list can be OMITTED, not empty-bracketed —
            // Go's omitempty drops an empty list, and the reducer reads
            // that exactly as one: every item is withdrawn (RoboRev
            // round 19).
            const kept = stamp.items ?? [];
            for (const old of oldTurn?.items ?? []) {
              if (kept.some((item) => itemIdentityMatches(old, item))) {
                continue;
              }
              pageOwnedIds.delete(old.transcriptKey ?? old.id);
              pageOwnedIds.delete(`${old.id}:attachments`);
              if (old.transcriptKey !== undefined) {
                pageOwnedIds.delete(`${old.transcriptKey}:attachments`);
              }
            }
          }
        }
        if (applied !== state.conversation) {
          if (changesRows(state.conversation, applied)) {
            const projected = withPageHistory(
              state.conversation,
              projectConversation(applied),
            );
            const bounded = capAndTruncate(projected);
            // #1919 follow-up: a row-changing frame can repopulate a
            // compacted turn's entire payload (a completion's full view)
            // with no applier pass left to bound it — the rows the cap kept
            // are the keep-window, exactly as the rehydrate and loadOlder
            // merges bound theirs; the active turn is exempted inside the
            // helper (its payloads are the live working set).
            const conversation: MobileConversation = {
              ...bounded,
              turns: boundRetainedTurns(
                bounded.turns,
                bounded.items,
                mergedItemFoldIdentities,
                bounded.activeTurnId,
              ),
            };
            pruneEvictedIds(conversation.items);
            set({ conversation });
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
        // The transient-warning settle finding (RoboRev, Sep-18, on this
        // piece's pre-restack branch): a warning folds into the active
        // turn's items and a bare turn/completed settles the turn without
        // touching them, so the projected warning row lingered with nothing
        // in flight to clear it. The wire never persists warnings, so the
        // canonical read is the one honest way to drop what the settle
        // kept: a completed turn that still holds warning items requests
        // it. A full settle stamp replaces the turn's items wire-authoritatively
        // (no warnings survive it) and needs nothing here.
        if (n.method === "turn/completed") {
          const settledTurnId = (n.params as { turn?: { id?: string } }).turn
            ?.id;
          const settledTurn =
            settledTurnId === undefined
              ? undefined
              : applied.turns.find((turn) => turn.id === settledTurnId);
          if (settledTurn?.items.some((item) => item.type === "warning")) {
            requestRehydrate(state.ref);
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
