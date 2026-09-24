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
  foldWarningParams,
  isStaleCursorError,
  isActiveItem,
  isToolCallItemId,
  isToolResultItemId,
  itemTextPresence,
  itemIdentityMatches,
  joinWarningParts,
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
  TurnModel,
  ThreadModel,
  WarningParams,
} from "@evener/appwire-client";
import type {
  BoundText,
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/project";
import {
  attachmentSourceId,
  attachmentSourceIdentity,
  capItems,
  failureRowIdentity,
  liveAsksFor,
  MAX_ITEM_BYTES,
  ownTimelineIdentities,
  projectConversation,
  projectTimeline,
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
    // #2213 round 1: settle the page-ownership record against what the
    // frame removed from the model before anything reads it (the publish
    // below, the reread a settled turn can request).
    clearPageOwnershipForFrameRemovals(conversation, next, n);
    return next;
  }
  // D23d: page-owned timeline identities — the item identities (and
  // failure:<turn id> row identities) the merged model carries as older-page
  // history. The pages live in the model now (loadOlder merges them through
  // the package's mergeOlderItemPage and the rows re-project from that
  // model), so this is the model's own record of which content is page
  // history, recorded from the merge's own inputs — never from projected
  // rows — and pruned only by the retained-turn bound (the model's
  // compaction), never by the display cap. A rehydrate's snapshot is
  // authoritative for everything it carries; the page history it does not
  // carry survives through the model merge, and these identities are what
  // its retention rules read to tell page history from stale live state.
  // Cleared on every conversation transition (open/close/reset/openProjected).
  const pageItemIds = new Set<string>();
  // Track page-owned turn IDs separately from pageItemIds above. Turns are
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
  // transition, same as pageItemIds.
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

  // RoboRev round 34: turn-independent warnings — a warning that arrives
  // with no active turn — are real server diagnostics the wire drops: the
  // reducer has nowhere transcript-true to fold one (its "warning" case
  // returns only the liveness restamp when no turn is active), and the
  // transcript never persists warnings, so no read can recover what the
  // frame carried either. Main's row applier displayed them as live-owned
  // rows; this store's rows are a projection of the model, so a row the
  // model cannot hold lives here, as transient display state outside it.
  // Every timeline rebuild passes through capAndTruncate, the display
  // boundary, which seats each notice at the position it arrived at
  // (round 35: re-appended at the tail, a notice sank below rows that
  // arrived later and the retained window could never evict it), and
  // the conversation transitions that clear the page history clear
  // these with it, so a notice never crosses threads.
  const transientWarnings: {
    row: MobileTimelineItem;
    anchor: string | null;
  }[] = [];
  let liveNoticeSerial = 0;
  // The row an idle warning displays as: the same attention row the
  // canonical projection builds for a model warning item (project.ts's
  // warningItem — kind "failure", title its own field, message and hint
  // joined as detail), under the live serial identity main's applier
  // used, since there is no model item to share an identity with.
  function idleWarningRow(params: WarningParams): MobileTimelineItem {
    const folded = foldWarningParams(params);
    return {
      kind: "failure",
      id: `warning:${(liveNoticeSerial += 1)}`,
      title: folded.title ?? "Warning",
      detail: joinWarningParts([folded.text, folded.hint]),
    };
  }

  // The position a notice arrived at: the identity of the last MODEL row
  // on screen when the notice landed, or null when the timeline held
  // nothing model-backed (the notice predates every row the model has
  // produced since). Seated notices do not qualify (round 36: the second
  // of two back-to-back notices anchored to the first, but the seating
  // walk matches anchors against model rows only, so that notice never
  // seated and the prune deleted it) — consecutive notices stack at the
  // same arrival position in arrival order instead, and leave it
  // together. An attachment row's own identity is GENERATED from its
  // source's wire id, which a reissue (the same source under a new wire
  // id, transcript key standing) re-keys — an anchor to it matched
  // nothing after the reissue and the next rebuild pruned the notice
  // (RoboRev panel round 2) — so attachment rows anchor through their
  // STABLE source identity (attachmentSourceIdentity), which the reissue
  // keeps. The seating walk resolves that anchor on the source row and
  // seats the notice after the attachments that follow it (never between
  // the pair — see capAndTruncate), so the notice keeps the position it
  // arrived at, after the attachments row that was the nearest row then.
  function arrivalAnchor(
    items: MobileTimelineItem[],
    noticeIdentities: ReadonlySet<string>,
  ): string | null {
    for (let index = items.length - 1; index >= 0; index -= 1) {
      const item = items[index];
      const identity = attachmentSourceIdentity(item) ?? timelineIdentity(item);
      if (!noticeIdentities.has(identity)) return identity;
    }
    return null;
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
    const seated = seatTransientWarnings(conversation.items);
    const items = capItems(seated).map((item) => truncateItem(item, bound));
    const retainedIdentities = new Set(items.map(timelineIdentity));
    for (let index = transientWarnings.length - 1; index >= 0; index -= 1) {
      if (
        !retainedIdentities.has(timelineIdentity(transientWarnings[index].row))
      ) {
        transientWarnings.splice(index, 1);
      }
    }
    boundedText = next;
    return { ...conversation, items };
  }

  // The transient notices rejoin the timeline here, each seated at
  // the position it arrived at (after its anchor's row) rather than
  // at the tail: a notice is evidence of a moment, so it stays above
  // the rows that arrived later, and the cap that slides past its
  // position evicts it with it — the notice enters the window as a
  // row like any other. A rebuild whose input rows already carry one
  // — loadOlder merges the current items — must not duplicate it (the
  // carried filter); a notice whose anchor left the model rows (a
  // withdrawal, a reread window that starts past it) has no position
  // to sit at and retires with the prune; and the prune back to what
  // the cap kept means an evicted notice stays evicted.
  // RoboRev review round 4: the seating is extracted so loadOlder can
  // read the same window the publish will show — a notice consumes a
  // cap slot, so the overflow the honest stop reads and the window the
  // retained-turn bound trims against must both count it.
  function seatTransientWarnings(
    items: MobileTimelineItem[],
  ): MobileTimelineItem[] {
    const carriedIdentities = new Set(items.map(timelineIdentity));
    const noticesByAnchor = new Map<string, MobileTimelineItem[]>();
    const noticesBeforeEverything: MobileTimelineItem[] = [];
    for (const notice of transientWarnings) {
      if (carriedIdentities.has(timelineIdentity(notice.row))) continue;
      if (notice.anchor === null) {
        noticesBeforeEverything.push(notice.row);
        continue;
      }
      const bucket = noticesByAnchor.get(notice.anchor);
      if (bucket === undefined) {
        noticesByAnchor.set(notice.anchor, [notice.row]);
      } else {
        bucket.push(notice.row);
      }
    }
    const seated: MobileTimelineItem[] = [...noticesBeforeEverything];
    for (let index = 0; index < items.length; index += 1) {
      const item = items[index];
      seated.push(item);
      // RoboRev round 37: anchors resolve through the identities the row
      // OWNS, not just its own top-level one — pagination can seat an older
      // tool directly beside the one a notice anchored to, and the next
      // projection then clusters the two under the older's identity: the
      // anchored row stays visible as a member while its identity stops
      // matching, which used to retire the notice with the prune. The
      // size guard keeps the common no-notice publish at one check per
      // row.
      if (noticesByAnchor.size === 0) continue;
      const identities = ownTimelineIdentities(item);
      for (const identity of identities) {
        const bucket = noticesByAnchor.get(identity);
        if (bucket === undefined) continue;
        // A notice anchored through an attachment row's source identity
        // seats after the attachments that follow the anchored row, never
        // between them: capItems' cap cut drops a leading attachment
        // whose source fell off the cut and only scans a LEADING RUN of
        // attachments, so a notice seated inside that run would become
        // the first retained row at the cut and the orphans behind it
        // would survive — their lingering source identities then make
        // loadOlder's F10 admission rule refuse genuine older page copies
        // of those sources, forever. The run spans every attachment the
        // anchored row OWNS the source of — a clustered activity carries
        // the attachments of all its members, so a notice anchored to one
        // member seats after them all. This is also the position the
        // notice arrived at: the attachments row was the nearest row when
        // it landed (RoboRev panel round 2).
        while (index + 1 < items.length) {
          const next = items[index + 1];
          const source = attachmentSourceIdentity(next);
          if (source === null || !identities.has(source)) break;
          index += 1;
          seated.push(next);
        }
        seated.push(...bucket);
      }
    }
    return seated;
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
  // D23d: record which of the merged model's identities are page history,
  // from the page merge's own membership — an item any page input folded
  // into (or that IS a page input, untouched) is page history; an item the
  // retained side held alone is not. A page fragment folded into a live
  // item makes the merged item page history (the page supplied content the
  // window lacked), so the rehydrate's retention rules read it as history;
  // an identity already recorded here keeps its page-ness through every
  // later fold that draws it in. Returns this page's own contributions so
  // loadOlder can report the page keys the retained window still holds.
  function recordPageItemIds(
    pageInputs: ReadonlySet<ItemModel>,
    folds: {
      itemFoldSources: (item: ItemModel) => readonly ItemModel[];
      toolResultFoldSources: (item: ItemModel) => readonly ItemModel[];
    },
    mergedTurns: readonly TurnModel[],
  ): Set<string> {
    const recorded = new Set<string>();
    if (pageInputs.size === 0) return recorded;
    for (const turn of mergedTurns) {
      for (const item of turn.items) {
        // RoboRev review rounds 1 and 3: ownership and report are
        // different questions, and the callId fold's absorbed results
        // (round 1) count for both. A merged item whose fold sources
        // include a page input is page history; an item whose backing is
        // only INHERITED page ownership — a prior page's content the
        // merge drew in, or an untouched item that was already the
        // page's — keeps that ownership but is NOT this page's
        // contribution: itemKeys is the reader's no-progress signal
        // (nonempty clears its retry guard), so a page that only
        // re-serves retained content must report empty. A page input is
        // this page's contribution when it supplied a payload field the
        // merged item's other sources lack, or when the merge kept
        // nothing else beside it (a genuinely new item); a duplicate
        // whose every field also sits on a retained side brought nothing.
        const sources = [
          ...folds.itemFoldSources(item),
          ...folds.toolResultFoldSources(item),
        ];
        const others = sources.filter((source) => !pageInputs.has(source));
        let inherited = false;
        let contributed = false;
        for (const source of [
          ...folds.itemFoldSources(item),
          ...folds.toolResultFoldSources(item),
        ]) {
          if (!pageInputs.has(source)) {
            if (pageItemIds.has(source.transcriptKey ?? source.id)) {
              inherited = true;
            }
            continue;
          }
          if (others.length === 0) {
            contributed = true;
            continue;
          }
          if (
            PAGE_CONTRIBUTION_FIELDS.some(
              (field) =>
                (source as unknown as Record<string, unknown>)[field] != null &&
                others.every(
                  (other) =>
                    (other as unknown as Record<string, unknown>)[field] == null,
                ),
            ) ||
            // RoboRev review round 4: text is not a plain nullable here —
            // hydration spells OMITTED text as an empty string plus a
            // presence marker, so a retained sparse item's "" fails the
            // field-null check and a page supplying ONLY the missing text
            // read as contributing nothing. Presence is the real signal:
            // a page input that provides text no other source provides
            // is a contribution, and the restored row keeps its page
            // ownership against a later omitting refresh.
            (itemTextPresence(source) === "provided" &&
              others.every(
                (other) => itemTextPresence(other) !== "provided",
              ))
          ) {
            contributed = true;
          }
        }
        if (!contributed && !inherited) continue;
        for (const id of [item.transcriptKey ?? item.id, item.id]) {
          pageItemIds.add(id);
          if (contributed) recorded.add(id);
        }
      }
    }
    return recorded;
  }

  // The payload fields a page input can supply a merged item its other
  // sources lack — the fields mergePageItem folds from either side.
  const PAGE_CONTRIBUTION_FIELDS = [
    "text",
    "toolName",
    "callId",
    "argumentsJSON",
    "description",
    "eventKind",
    "steeringKind",
    "raw",
    "output",
    "error",
    "prevalOnly",
    "exitCode",
    "images",
    "outputImages",
    "source",
    "reasoningSummaries",
    "status",
    "position",
  ] as const;

  // RoboRev review round 1: follow page ownership through a merge's item
  // folds. Any merged item whose fold sources include page-owned content is
  // itself page history — the identity it NOW carries must be recorded, or
  // the retention rules (the rehydrate twin filter, the snapshot-authority
  // strip) query an identity the page never marked. The rehydrate's own
  // merge can move page-owned content this way: a keyless paged item gains
  // the transcript key of the keyed reissue it folded, and ownership
  // recorded under the old bare id then guards nothing — a later bounded
  // refresh omitting the item drops loaded page history.
  function transferPageItemIdsThroughFolds(
    folds: {
      itemFoldSources: (item: ItemModel) => readonly ItemModel[];
      toolResultFoldSources: (item: ItemModel) => readonly ItemModel[];
    },
    retainedTurns: readonly TurnModel[],
    mergedTurns: readonly TurnModel[],
  ): void {
    // The merge's no-op path returns the fresh items by reference with no
    // fold provenance recorded (round 23) — a keyed reissue that subsumes
    // a keyless page item WHOLE (no older-only payload) then carries no
    // recorded sources at all, and the fold-based pass below finds nothing.
    // The retained side's own page-owned items are the oracle there: index
    // their identities — key and bare id both, the same from-above
    // approximation the retained-turn bound uses, where a false hit only
    // over-keeps — and let an identity-matching merged item inherit.
    const pageOwnedRetainedIdentities = new Set<string>();
    for (const turn of retainedTurns) {
      for (const item of turn.items) {
        if (!pageItemIds.has(item.transcriptKey ?? item.id)) continue;
        pageOwnedRetainedIdentities.add(item.transcriptKey ?? item.id);
        pageOwnedRetainedIdentities.add(item.id);
      }
    }
    for (const turn of mergedTurns) {
      for (const item of turn.items) {
        let pageBacked = false;
        for (const source of [
          ...folds.itemFoldSources(item),
          ...folds.toolResultFoldSources(item),
        ]) {
          if (pageItemIds.has(source.transcriptKey ?? source.id)) {
            pageBacked = true;
            break;
          }
        }
        if (
          !pageBacked &&
          ((item.transcriptKey !== undefined &&
            pageOwnedRetainedIdentities.has(item.transcriptKey)) ||
            pageOwnedRetainedIdentities.has(item.id))
        ) {
          pageBacked = true;
        }
        if (!pageBacked) continue;
        for (const id of [item.transcriptKey ?? item.id, item.id]) {
          pageItemIds.add(id);
        }
      }
    }
  }

  // D23d: settle the page's failure claims over one merge of the model.
  // olderSides/newerSides name, per surviving turn id, the input turn ids
  // the merge folded (the package folds each group's older fragments first,
  // then its newer ones, each mergePageTurn taking the incoming fragment's
  // error when it carries one — so the group's error is the LAST
  // error-carrying fragment of that chain). The claim follows exactly the
  // fragment whose error survived: a page-owned failure keeps its claim
  // under the surviving id (RoboRev round 30's migration, model-side), a
  // survivor whose error came from a turn the page never owned is not the
  // page's (round 38 — a later snapshot that resolves it must clear it),
  // and a claim on an id the fold consumed away retires with it. Every
  // turn the merge returns appears in at least one side's map — a lone
  // input names itself — so a turn with no chain entry is one neither
  // side contributed, which the package never returns.
  function settlePageFailureClaims(
    olderSides: ReadonlyMap<string, readonly string[]>,
    newerSides: ReadonlyMap<string, readonly string[]>,
    errorOf: (turnId: string, side: "older" | "newer") => unknown,
    pageFailure: (turnId: string, side: "older" | "newer") => boolean,
    mergedTurns: readonly TurnModel[],
  ): readonly string[] {
    const mergedIds = new Set(mergedTurns.map((turn) => turn.id));
    // RoboRev review round 1: compute every successor claim against the
    // ORIGINAL ownership set, then replace the claims once. The retiring
    // pass used to delete consumed ids from the live set first, so the
    // successor walk read a claim it had just retired — through the
    // caller's pageFailure callback — and a paged failure folded into a
    // surviving turn never migrated: the error rode this merge's model,
    // but the next clean covering refresh stripped it as unowned.
    const claimsToRetire: string[] = [];
    const claimsToAdd: string[] = [];
    for (const claim of pageItemIds) {
      if (!claim.startsWith("failure:")) continue;
      const turnId = claim.slice("failure:".length);
      if (!mergedIds.has(turnId)) claimsToRetire.push(claim);
    }
    for (const turn of mergedTurns) {
      const claim = failureRowIdentity(turn.id);
      const hasClaim = pageItemIds.has(claim);
      let claimed = false;
      if (turn.error !== undefined) {
        let supplier: { id: string; side: "older" | "newer" } | undefined;
        for (const id of olderSides.get(turn.id) ?? []) {
          if (errorOf(id, "older") !== undefined) supplier = { id, side: "older" };
        }
        for (const id of newerSides.get(turn.id) ?? []) {
          if (errorOf(id, "newer") !== undefined) supplier = { id, side: "newer" };
        }
        claimed =
          supplier !== undefined && pageFailure(supplier.id, supplier.side);
      }
      if (claimed === hasClaim) continue;
      if (claimed) claimsToAdd.push(claim);
      else claimsToRetire.push(claim);
    }
    for (const claim of claimsToRetire) pageItemIds.delete(claim);
    for (const claim of claimsToAdd) pageItemIds.add(claim);
    return claimsToAdd;
  }

  // RoboRev #2213 round 1 (Medium): a reset and a full turn/completed REMOVE
  // items from the model — the wire authoritatively withdrew them — and the
  // page ownership those identities recorded must not outlive the removal.
  // A stale claim promotes the identity's NEXT holder to page history: the
  // wire reuses item ids across stream restarts (the reset→started
  // protocol) and across turns, so a restarted live row would ride a claim
  // that describes content the model no longer holds and survive an
  // authoritative rehydrate that omits it. Ownership follows CONTENT, so a
  // claim clears only when the frame took the identity's last model copy —
  // a page copy another turn still backs keeps it (the identity-keyed rule
  // the recorders write, and the retention a surviving page fragment owns
  // by design). The spellings cleared are the pair every recorder writes —
  // key-first and bare id — which is also the pair an omitted item's
  // attachment row resolves its source by (attachmentSourceIdentity reads
  // the source's transcript key, else the bare id the ":attachments" row id
  // carries), so the attachment's page ownership retires with its source's.
  function clearPageOwnershipForFrameRemovals(
    before: MobileConversation,
    after: MobileConversation,
    n: AnyNotification,
  ): void {
    // Only the removal frames reach this walk; every other kind adds or
    // merges content. These two are the only reducer paths that remove —
    // a reset filters the named item out, and a full completion stamp
    // replaces the active turn's item set (the bare and non-active settle
    // paths never remove).
    if (n.method !== "item/agentMessage/reset" && n.method !== "turn/completed") {
      return;
    }
    // A frame the reducer dropped left the turns untouched.
    if (after.turns === before.turns) return;
    const heldAfter = new Set<string>();
    for (const turn of after.turns) {
      for (const item of turn.items) {
        heldAfter.add(item.transcriptKey ?? item.id);
        heldAfter.add(item.id);
      }
    }
    // RoboRev round 2: retire each absent spelling independently. A full
    // completion can reissue a survivor under a new wire id with its key
    // standing — the key's claim follows the content that still backs it,
    // while the old bare id's claim retires with the copy that left, or a
    // later keyless reuse of that id inherits page history nothing holds
    // anymore.
    for (const turn of before.turns) {
      for (const item of turn.items) {
        if (!heldAfter.has(item.transcriptKey ?? item.id)) {
          pageItemIds.delete(item.transcriptKey ?? item.id);
        }
        if (!heldAfter.has(item.id)) {
          pageItemIds.delete(item.id);
        }
      }
    }
  }

  // RoboRev round 24: a refresh can NAME the live working set's turn while
  // its own bounded snapshot omits the turn itself. Before any pagination
  // exists there is no page merge to carry that turn, and the replace path
  // would discard the live turn and every row it streamed — leaving later
  // item frames no containing turn to update, so the transcript stops
  // following a stream the wire itself still reports active. The snapshot
  // naming the turn active is the same authority the paged merge reads (the
  // round-22 wholesale rule), so the live turn survives the refresh
  // independently of page ownership: preserved from the current model,
  // together with the rows it owns there, whenever the fresh read names it
  // active but does not carry it. The web store reads the same carve-out
  // (preserveLiveActiveTurn). Rows the projection already carries — under
  // any wire id, hub reissues included — and rows a page already front-loaded
  // do not append twice.
  function withLiveActiveTurn(
    previous: MobileConversation | null,
    sameInstance: boolean,
    projected: MobileConversation,
  ): MobileConversation {
    const activeId = projected.activeTurnId;
    if (
      activeId === undefined ||
      previous === null ||
      !sameInstance ||
      projected.turns.some((turn) => turn.id === activeId)
    ) {
      return projected;
    }
    const liveTurn = previous.turns.find((turn) => turn.id === activeId);
    if (liveTurn === undefined) return projected;
    // Items the fresh read carries under another turn are the snapshot's:
    // the read omits the live turn itself, so every identity match is a
    // re-serve, and the preserved copy would ride the model back beside
    // the canonical one — the next row-changing frame would project the
    // twin (the package identity rule, the same match the page merge's
    // twin filter reads).
    const freshItems = projected.turns.flatMap((turn) => turn.items);
    const preservedItems = liveTurn.items.filter(
      (item) => !freshItems.some((fresh) => itemIdentityMatches(item, fresh)),
    );
    const preservedTurn =
      preservedItems.length === liveTurn.items.length
        ? liveTurn
        : { ...liveTurn, items: preservedItems };
    // One asks scan over the COMBINED turns (RoboRev round 28): the
    // question gating reads the wire's askPending plus WHICH asks the
    // combined model still holds pending, and rows appended from two
    // separate scans disagreed with the answer sheet — a snapshot ask the
    // preserved turn's user reply had already answered kept its
    // answerable card in the snapshot's own rows while pendingQuestions
    // (over the combined model) correctly dropped it, until the next
    // row-changing frame. The scan is the one thing the two turn sets
    // must share; the projections themselves stay separate, so
    // clustering stays within each set and the retained cluster shapes
    // survive exactly as the page prepend and the live preserve build
    // them. A row copied from the previous projection would instead bake
    // in the previous askPending (RoboRev round 27) — the projector
    // renders streamed chunks and activity state the same way the live
    // row applier does, so the reprojection is exact.
    const combinedAsks = liveAsksFor({
      ...projected,
      turns: [...projected.turns, preservedTurn],
    });
    const freshRows = projectTimeline(
      { ...projected, turns: projected.turns },
      combinedAsks,
    );
    const preservedRows = projectTimeline(
      { ...projected, turns: [preservedTurn] },
      combinedAsks,
    );
    return {
      ...projected,
      turns: [...projected.turns, preservedTurn],
      items: [...freshRows, ...preservedRows],
    };
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
      // D23d: the shed items left the model, so their page identities go
      // with them — a later re-introduction (a fresh page re-serving the
      // content, or a live frame) is judged on its own, exactly as the
      // row-side ownership prune the bound replaces once did at the row
      // cap. The failure claim stays: a compacted turn keeps its error,
      // so its failure row keeps projecting.
      const shed = compactItemSkeletons(turn.items);
      for (const skeleton of shed) {
        pageItemIds.delete(skeleton.transcriptKey ?? skeleton.id);
        pageItemIds.delete(skeleton.id);
      }
      // Remember identity-only skeletons of the shed items (unioned with
      // whatever the turn shed earlier — a partial restoration must not
      // forget the rest) so a later re-issue under a different turn id can
      // still fold through the package's own merge.
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
        pageItemIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        transientWarnings.length = 0;
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
        pageItemIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        transientWarnings.length = 0;
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
          // paged in — and D23d keeps that in the model itself, so the merge
          // below carries it and the rows re-project from the merged model.
          const currentConvForMerge = currentSnapshot.conversation;
          const sameInstance =
            currentConvForMerge !== null && currentConvForMerge.instanceId === conversation.instanceId;
          const replacesInstance = currentConvForMerge !== null && !sameInstance;
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
          // The live-turn preserve keeps the working set the snapshot's
          // bounded window omits (round 28); its rows are the model's
          // projection, re-derived below.
          const merged = withLiveActiveTurn(
            currentConvForMerge,
            sameInstance,
            conversation,
          );
          // The page's cursor is the newer one when page history is kept
          // (turn ownership is the gate — a page whose rows were all deduped
          // or evicted still owns turns): the reread's reflects the full
          // readProjection, which does not include the paged history.
          let mergedCursor = preserveTurnHistory
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
          // The live-active carve-out above may have appended the named
          // active turn to merged.turns; the page merge below reassigns
          // this from its own fold inputs when it runs.
          let mergedTurns = merged.turns;
          let wireOlderCursor = conversation.olderCursor;
          // RoboRev local round 1 (Medium): when the retained-turn bound
          // below runs, the publish commits the same seated, capped rows it
          // trimmed against (see the bound's comment) instead of
          // re-projecting from the trimmed turns — a notice whose anchor
          // row the bound shed must still seat where the window that kept
          // it places it, exactly as loadOlder commits its own seated
          // pre-bound window.
          let seatedRehydrateItems: MobileTimelineItem[] | null = null;
          // RoboRev review round 2: whether the retained history is
          // CONTINUOUS with the refreshed window — the coverage merge's own
          // transcript-overlap signal. The store's own paging cursor below
          // reads it: page turn ownership alone must not pin the retained
          // cursor (an atCap null included) across a refresh whose window
          // shares no history with what the model retains.
          let retainedOverlapsWindow = false;
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
            const freshItems: ItemModel[] = [];
            for (const turn of conversation.turns) {
              for (const item of turn.items) {
                freshIdentities.add(item.transcriptKey ?? item.id);
                freshIdentities.add(item.id);
                freshItems.push(item);
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
            const freshTurnById = new Map<string, TurnModel>();
            const freshTurnByItemKey = new Map<string, TurnModel>();
            const freshTurnByItemId = new Map<string, TurnModel>();
            // The containing turn's status recorded PER ITEM, not keyed
            // through item.turnId — the wire can omit turnId, and
            // hydration turns the omission into "" (RoboRev round 15).
            const freshTurnStatusByKey = new Map<string, string | undefined>();
            const freshTurnStatusById = new Map<string, string | undefined>();
            for (const turn of conversation.turns) {
              freshTurnById.set(turn.id, turn);
              for (const item of turn.items) {
                freshItemByKey.set(item.transcriptKey ?? item.id, item);
                freshItemById.set(item.id, item);
                freshTurnByItemKey.set(item.transcriptKey ?? item.id, turn);
                freshTurnByItemId.set(item.id, turn);
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
            ): {
              fresh: ItemModel;
              turnStatus: string | undefined;
              turn: TurnModel;
            } | undefined => {
              const byKey = freshItemByKey.get(item.transcriptKey ?? item.id);
              if (byKey !== undefined && itemIdentityMatches(item, byKey)) {
                return {
                  fresh: byKey,
                  turnStatus: freshTurnStatusByKey.get(item.transcriptKey ?? item.id),
                  turn: freshTurnByItemKey.get(item.transcriptKey ?? item.id)!,
                };
              }
              const byId = freshItemById.get(item.id);
              if (byId !== undefined && itemIdentityMatches(item, byId)) {
                return {
                  fresh: byId,
                  turnStatus: freshTurnStatusById.get(item.id),
                  turn: freshTurnByItemId.get(item.id)!,
                };
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
              if (!pageItemIds.has(item.transcriptKey ?? item.id)) {
                for (const field of SNAPSHOT_AUTHORITY_FIELDS) {
                  const retained = (item as unknown as Record<string, unknown>)[field];
                  const settledOnFresh = (fresh as unknown as Record<string, unknown>)[field];
                  if (retained === undefined || settledOnFresh !== undefined) continue;
                  // Same marker rule as the chunk strip above: the clone
                  // keeps the item's omitted-text presence.
                  stripped ??= copyItemTextPresence(item, { ...item });
                // A withdrawn LIST must survive as an explicit removal:
                // every merge reads an absent list as "the other side's
                // stands" (the hub's own input-images rule,
                // imagesToItemImagesForSession), so an undefined strip
                // would let an overlapping page re-serve the withdrawn
                // images through the page fold (RoboRev round 10, under
                // D23d's model-owned rows). The empty list is the removal
                // spelling outputImages already carries on the wire, and
                // hydrate normalizes an empty input-images list away, so
                // the marker stays unambiguous model-side. Scalar fields
                // keep the undefined spelling — the rehydrate's fresh
                // copy omits what the wire withdrew, so the nullish
                // fallback reads the withdraw there without a marker.
                (stripped as unknown as Record<string, unknown>)[field] = Array.isArray(
                  retained,
                )
                  ? []
                  : undefined;
                }
              }
              // The lifecycle is the snapshot's to settle too: a retained
              // status claim cannot outlive a fresh copy the activity
              // rule reads as settled. The stale path is exactly the
              // nullish fallback — a fresh copy carrying NO status of its
              // own inherits the retained one, "inProgress" reprojectioning
              // "Writing…" on the finished response (RoboRev round 16) and
              // "failed"/"interrupted" flipping a completed activity back
              // to a failure on the next row-changing frame (round 24).
              // A fresh copy that carries its own status needs no help:
              // the rank merge reconciles explicit statuses by rank.
              if (
                item.status !== undefined &&
                fresh.status === undefined &&
                !isActiveItem(fresh, match.turnStatus)
              ) {
                stripped ??= copyItemTextPresence(item, { ...item });
                (stripped as unknown as Record<string, unknown>).status = undefined;
              }
              return stripped ?? item;
            };
            const retainedTurnsForMerge = currentConvForMerge.turns.map(
              (rawTurn) => {
                // RoboRev round 26: the snapshot can name a turn active
                // while its bounded window omits the turn itself and
                // re-serves one of the turn's items under another turn
                // id. The turn is then covered ONLY through the shared
                // item, and the twin filter below would delete the
                // turn's unmatched live items — the working set the
                // read itself says is still running — while the history
                // merge folds the turn away through the shared identity,
                // taking the turn id every later frame names with it.
                // Drop only the re-served items from the retained copy
                // (the package identity rule, the same match
                // withLiveActiveTurn reads): the turn keeps its
                // unmatched items through the round-22 wholesale rule
                // and survives the merge under its own id. A turn the
                // fresh read carries by ID keeps everything as before —
                // the merge folds those copies properly, with the
                // retained side supplying fields the fresh fragments
                // omit.
                let turn = rawTurn;
                if (
                  rawTurn.id === conversation.activeTurnId &&
                  !freshTurnById.has(rawTurn.id)
                ) {
                  const items = rawTurn.items.filter(
                    (item) =>
                      !freshItems.some((fresh) =>
                        itemIdentityMatches(item, fresh),
                      ),
                  );
                  if (items.length !== rawTurn.items.length) {
                    turn = { ...rawTurn, items };
                  }
                }
                // A turn the snapshot does not cover keeps everything
                // only when something owns its wholesale retention: the
                // compact memory the remembered-alias folds read
                // (pageOwnedCompactTurnIds/compactedTurnItems — the
                // #1919 restoration world), or the live working set of
                // the turn the SNAPSHOT names active — never the
                // retained model's own flag, which a missed completion
                // leaves stale (RoboRev round 22). Any other uncovered
                // turn gets the
                // same item-level retention as a covered one — its
                // page-owned rows stay, its discarded live items do not
                // ride the model back onto the screen beside them
                // (RoboRev round 21).
                const keepWholesale =
                  !turnCoveredBySnapshot(turn) &&
                  (turn.id === conversation.activeTurnId ||
                    compactedTurnItems.has(turn.id) ||
                    pageOwnedCompactTurnIds.has(turn.id));
                const afterTwins = !keepWholesale
                  ? turn.items.filter(
                      (item) =>
                        pageItemIds.has(item.transcriptKey ?? item.id) ||
                        snapshotMatchFor(item) !== undefined,
                    )
                  : turn.items;
                const kept = afterTwins.filter(
                  (item) =>
                    item.type !== "warning" ||
                    pageItemIds.has(item.transcriptKey ?? item.id),
                );
                const stripped = kept
                  .map(stripSettledChunks)
                  .map(applySnapshotAuthority);
                const reconciled =
                  kept.length === turn.items.length &&
                  stripped.every((item, index) => item === kept[index])
                    ? turn
                    : { ...turn, items: stripped };
                // Turn-level errors merge nullish-fallback like the
                // coverage fields, so a snapshot whose covering turn
                // carries no error must not inherit the retained failure:
                // the next row-changing frame would project a failure
                // row neither side holds (RoboRev round 23). A turn the
                // read omits ENTIRELY is the same authority statement —
                // when its items drop with it and no wholesale retention
                // owns the turn (the round-22 active rule, the compact
                // memory), the error is a ghost the next row-changing
                // frame would reproject from an emptied turn (round 25).
                // The exemption keys on ownership of the failure CONTENT,
                // not of the turn: the failure row's identity is the turn
                // id under the failure: prefix (failureRowIdentity — the
                // spelling loadOlder records for a paginated failure
                // row), so a page that carried the failure owns it as
                // retained history (the round-31 rule) — but a page that
                // loaded only a usage FRAGMENT of the turn owns nothing
                // of a failure the turn acquired live, and shielding it
                // would resurrect the failure the snapshot removed
                // (RoboRev round 24; round 29 fixed the identity read —
                // the bare turn id never matched a genuinely page-owned
                // failure).
                const covering =
                  turnCoveredBySnapshot(turn) === false
                    ? undefined
                    : freshTurnIds.has(turn.id)
                      ? freshTurnById.get(turn.id)
                      : turn.items
                          .map((item) => snapshotMatchFor(item)?.turn)
                          .find((candidate) => candidate !== undefined);
                if (
                  turn.error !== undefined &&
                  !keepWholesale &&
                  !pageItemIds.has(failureRowIdentity(turn.id)) &&
                  (covering === undefined || covering.error === undefined)
                ) {
                  return { ...reconciled, error: undefined };
                }
                return reconciled;
              },
            );
            const injectedFresh = injectCompactedSkeletons(
              retainedTurnsForMerge,
              compactedTurnsCollidingWith(freshIdentities, freshToolCallIds),
            );
            const history = mergeTurnHistoryWithFolds(injectedFresh.turns, conversation.turns);
            // The pre-merge errors the failure-claim walk reads, per side:
            // the retained turns AFTER this block's filters (the ghost
            // guard may have stripped an error) and the fresh read's own.
            const retainedErrorById = new Map(
              retainedTurnsForMerge.map((turn) => [turn.id, turn.error]),
            );
            const freshErrorById = new Map(
              conversation.turns.map((turn) => [turn.id, turn.error]),
            );
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
            retainedOverlapsWindow = coverage.transcriptOverlap;
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
            // D23d (RoboRev review round 1): the rehydrate's own merge can
            // move page-owned content onto a new identity (a keyless paged
            // item gains the transcript key of the reissue it folded), so
            // ownership must follow the fold's provenance before the
            // retention rules below query the merged identities.
            transferPageItemIdsThroughFolds(
              {
                itemFoldSources: history.itemFoldSources,
                toolResultFoldSources: history.toolResultFoldSources,
              },
              retainedTurnsForMerge,
              mergedTurns,
            );
            // D23d (RoboRev rounds 30/33/38, model-side): a page-owned
            // failure whose turn the merge folds under a surviving id
            // migrates with the fold — the merged model carries the error,
            // so the projected failure row names the survivor and the
            // claim must follow the content. The claim transfers only when
            // the page's own error survived the fold chain: a survivor
            // whose error came from the snapshot's turn keeps that error as
            // its own, so a later snapshot that resolves it clears it.
            settlePageFailureClaims(
              history.olderTurnFolds,
              history.newerTurnFolds,
              (turnId, side) =>
                side === "newer"
                  ? freshErrorById.get(turnId)
                  : retainedErrorById.get(turnId),
              (turnId, side) =>
                side === "older" &&
                pageItemIds.has(failureRowIdentity(turnId)),
              mergedTurns,
            );
            // #1919 follow-up: bound the merged result AFTER the merge, so
            // turns inside the keep-window keep everything the older
            // fragments supplied, and only out-of-window payloads trim. The
            // final retained rows are the window; the pass settles page
            // turn ownership with the same bound.
            // RoboRev round 2 (panel Medium 1): the window must be the one
            // the publish below will actually show — capAndTruncate seats
            // the transient warnings before the cap, and a seated notice
            // consumes a cap slot, so an unseated bound would retain page
            // payloads for rows the seated cap just evicted.
            // RoboRev local round 1 (Medium): the publish shows exactly
            // this seated window, so hoist it — a notice the window kept
            // survives the commit even when the bound sheds the turn its
            // anchor row came from.
            const rehydrateSeated = seatTransientWarnings(
              projectTimeline({ ...merged, turns: mergedTurns }),
            );
            mergedTurns = boundRetainedTurns(
              mergedTurns,
              capItems(rehydrateSeated),
              mergedItemFoldIdentities,
              conversation.activeTurnId,
            );
            seatedRehydrateItems = rehydrateSeated;
          }
          if (replacesInstance) {
            pageItemIds.clear();
            pageOwnedTurnIds.clear();
            pageOwnedCompactTurnIds.clear();
            compactedTurnItems.clear();
            transientWarnings.length = 0;
          }
          // The snapshot's thread-level fields are authoritative (see the
          // response-cut note by applyThreadNotification); the rows are a
          // projection of the merged model — the snapshot's own turns plus
          // the page history the model still carries, one projector for a
          // frame and for a snapshot.
          // RoboRev local round 1 (Medium): on the history-preserving path
          // the committed rows are the SAME seated projection the
          // retained-turn bound trimmed against — the projector's input is
          // the pre-trim merged model, exactly the rows loadOlder commits —
          // so a notice the window kept stays seated even when the bound
          // sheds its anchor row. Re-projecting here instead would drop the
          // shed rows, leave the notice no anchor to seat at, and the
          // carried-filter prune would retire a warning the cap kept.
          const committedConversation = capAndTruncate(
            seatedRehydrateItems === null
              ? projectConversation({
                  ...merged,
                  turns: mergedTurns,
                  olderCursor: wireOlderCursor,
                })
              : {
                  ...merged,
                  turns: mergedTurns,
                  olderCursor: wireOlderCursor,
                  items: seatedRehydrateItems,
                },
          );
          // RoboRev review round 2: page turn ownership alone must not pin
          // the store's own paging cursor across a DISJOINT refresh —
          // after the cap honestly stopped paging (the retained cursor
          // null), a fresh window that shares no history with what the
          // model retains speaks for a range the cap never judged, and its
          // own cursor is the only truth the UI can page from. Preserve the
          // retained cursor only when the retained history is continuous
          // with the refreshed window; the racing-page advancement above
          // still wins outright.
          if (preserveTurnHistory && !pageCursorAdvanced && !retainedOverlapsWindow) {
            mergedCursor = olderCursor;
          }
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
            // D23d: the page merges into the model through the package's own
            // mergeOlderItemPageWithFolds and the rows re-project from the
            // merged model. The service hands over the page's wire turns
            // alone (projectOlderTurns and its fake Thread are deleted), so
            // the merge's identity rules ARE the page dedupe — an item the
            // window already holds folds by transcript key or bare id, a
            // cluster split at the pagination boundary keeps its un-held
            // members (the projector clusters what survives), and a page
            // fragment supplements the fields the live side omits (the
            // web's rule, decision 2 — the row filter and the model-side
            // strip pass that mirrored it are gone with the rows). The I3
            // question-row filter is gone the same way: rows re-project
            // through liveAskQuestions, which gates on askPending and
            // excludes every ask before the newest resolution item, so a
            // page's settled ask renders as the tool row it is.
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
            // Record page ownership by turn ID separately from the page item
            // identities — a turn survives here even
            // when every one of its display rows is deduped away or evicted.
            for (const turn of result.turnsPage.data) pageOwnedTurnIds.add(turn.id);
            // Review rounds 1-2: inject identity skeletons for compact
            // turns whose remembered identities the page re-issues, so the
            // package's own turnsMatch/coalescing does the folding exactly
            // as the unbounded main would have. The retained copy stays the
            // newer merge input, so its usage still wins the fold.
            const pageIdentities = new Set<string>();
            const pageToolCallIds = new Set<string>();
            for (const turn of result.turnsPage.data) {
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
            const pageMerge = mergeOlderItemPageWithFolds(
              mergeConv,
              result.turnsPage,
            );
            const mergedTurns = pageMerge.model.turns;
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
              ...pageMerge.olderTurns.flatMap((turn) => turn.items),
            ];
            const strippedPageTurns = stripInjectedSkeletons(
              mergedTurns,
              injectedPage.injected,
              pageRealSources,
              pageMerge.folds.itemFoldSources,
            );
            recordItemFoldSources(
              strippedPageTurns,
              pageMerge.folds.itemFoldSources,
              pageMerge.folds.toolResultFoldSources,
            );
            // The compact turns are the merge's NEWER (retained) side here;
            // the retained-side folds name the carrier a bridged compact
            // turn's content landed in.
            transferFoldedCompactedEntries(strippedPageTurns, pageMerge.folds.newerTurnFolds);
            // D23d: the model's own record of which identities this page
            // contributed, from the merge's membership — and the failure
            // claims follow the folds (RoboRev round 32's migration,
            // model-side: the merged model carries the error under the
            // surviving turn, the projected failure row names that survivor,
            // and a carrier whose retained side carried its own error
            // keeps that error as its own).
            const pageInputs = new Set<ItemModel>();
            for (const turn of pageMerge.olderTurns) {
              for (const item of turn.items) pageInputs.add(item);
            }
            const pageTurnIds = new Set(
              pageMerge.olderTurns.map((turn) => turn.id),
            );
            const pageErrorById = new Map(
              pageMerge.olderTurns.map((turn) => [turn.id, turn.error]),
            );
            const retainedErrorById = new Map(
              mergeConv.turns.map((turn) => [turn.id, turn.error]),
            );
            const pageKeys = recordPageItemIds(
              pageInputs,
              pageMerge.folds,
              strippedPageTurns,
            );
            const pageFailureClaims = settlePageFailureClaims(
              pageMerge.folds.olderTurnFolds,
              pageMerge.folds.newerTurnFolds,
              (turnId, side) =>
                side === "older"
                  ? pageErrorById.get(turnId)
                  : retainedErrorById.get(turnId),
              (turnId, side) =>
                (side === "older" && pageTurnIds.has(turnId)) ||
                pageItemIds.has(failureRowIdentity(turnId)),
              strippedPageTurns,
            );
            // RoboRev review round 5: a failed page turn contributes a
            // row no page item backs — its result folds into a retained
            // call and the turn survives for its error, so the projection
            // adds a failure row the item walks never see. The settle
            // returns the failure identities this page's own errors kept
            // alive through the merge (following turn folds), so the
            // progress report below names them and the honest stop counts
            // them like any row the page contributed.
            for (const claim of pageFailureClaims) pageKeys.add(claim);
            // The rows re-project from the merged model. mergedInput is the
            // pre-cap projection the at-cap signal reads (F8): the overflow,
            // never the final row count, is what ends paging.
            // RoboRev review round 4: the transient warnings seat into it
            // here, with the same seating the publish performs — a notice
            // consumes a cap slot, so the overflow the honest stop reads
            // and the window the retained-turn bound trims against must
            // both count it. capAndTruncate's carried filter makes the
            // re-seat below a no-op.
            const mergedModel = {
              ...pageMerge.model,
              turns: strippedPageTurns,
            };
            const mergedInput = seatTransientWarnings(
              projectTimeline(mergedModel),
            );
            // F8 under D23d: the pre-cap merged projection can overflow
            // with rows the entry window already discarded — an in-window
            // turn keeps payloads beyond its own rows (#1919's keep-window
            // bound), so its out-of-window row items re-project on every
            // merge and the cap re-discards them. That re-discard loses
            // nothing this load brought, so it is not the honest stop:
            // paging ends only when the cap discarded rows the entry window
            // held (the load displaced live history) or rows this page
            // contributed (the next pages' rows would be discarded the same
            // way). A usage-only page over a full window keeps paging.
            const overflow = mergedInput.length - RETAINED_ITEM_CAP;
            let atCap = false;
            if (overflow > 0) {
              const discarded = mergedInput.slice(0, overflow);
              const pageRowIdentities = new Set<string>();
              for (const turn of pageMerge.olderTurns) {
                for (const item of turn.items) {
                  pageRowIdentities.add(item.transcriptKey ?? item.id);
                  pageRowIdentities.add(item.id);
                }
              }
              // RoboRev review round 5: the failure rows a page's failed
              // turns contribute are nobody's item — count their
              // identities too, or a discarded failure row reads as
              // nobody's and paging keeps offering cursors whose every
              // further page discards its own history.
              for (const claim of pageFailureClaims) {
                pageRowIdentities.add(claim);
              }
              // RoboRev round 2 (panel Medium 2): identity/call-result
              // folds can move a page's contribution onto a SURVIVING
              // item's identity — the discarded output row then names an
              // identity no raw page item carries, and the raw-identity
              // check alone would keep offering a cursor whose every
              // further page discards its own contribution the same way.
              // pageKeys is the merge's own provenance view of exactly
              // those: the merged output identities THIS page contributed
              // to, post-fold (the failure claims above ride along in it).
              for (const key of pageKeys) {
                pageRowIdentities.add(key);
              }
              const entryRowIdentities = new Set<string>();
              for (const row of currentConv.items) {
                for (const id of timelineIdentities(row)) entryRowIdentities.add(id);
              }
              atCap = discarded.some((row) =>
                [...timelineIdentities(row)].some(
                  (id) =>
                    entryRowIdentities.has(id) || pageRowIdentities.has(id),
                ),
              );
            }
            const nextCursor = atCap ? null : (result.nextCursor ?? null);
            const pageMerged = capItems(mergedInput);
            const pageConversation = capAndTruncate({
              ...mergedModel,
              items: mergedInput,
            });
            const merged = pageConversation.items;
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
            // The page's own contributions the retained window still holds:
            // what this loadOlder brought that the cap did not discard.
            const retainedIds = new Set<string>();
            for (const row of merged) {
              for (const id of timelineIdentities(row)) retainedIds.add(id);
            }
            return {
              status: "loaded",
              itemKeys: [...pageKeys].filter((id) => retainedIds.has(id)),
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
        pageItemIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        transientWarnings.length = 0;
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
        // RoboRev round 34: a warning with no active turn is the one frame
        // the wire drops entirely (and never persists, so no read can carry
        // it back), yet it is a real diagnostic — the projector emits
        // EventWarning unconditionally, and a prompt-render failure on a
        // model change lands exactly here, while idle. The store keeps it
        // itself: the notice goes to the transient surface capAndTruncate
        // reseats at its arrival position on every rebuild, and this
        // frame takes the full
        // publish path below so the row reaches the screen at once. A
        // warning WITH an active turn needs none of this — the reducer
        // folds it into the turn's items and the projection renders it.
        // RoboRev round 37: an active turn the LOADED WINDOW does not hold
        // is the same drop — the wire can name one (a resumed old turn
        // while the window holds only newer history), and the reducer's
        // fold then finds no turn to attach the warning to (mapTurn hands
        // back the same turns), while the gap reread the frame requests
        // can never carry the warning back either, the wire not
        // persisting warnings. The notice therefore goes to the transient
        // surface whenever the reducer could not place the warning, not
        // only when idle.
        const unplacedWarningNotice =
          n.method === "warning" &&
          (!applied.activeTurnId ||
            !applied.turns.some((turn) => turn.id === applied.activeTurnId))
            ? idleWarningRow(n.params)
            : null;
        if (unplacedWarningNotice !== null) {
          transientWarnings.push({
            row: unplacedWarningNotice,
            anchor: arrivalAnchor(
              state.conversation.items,
              new Set(
                transientWarnings.map((notice) => timelineIdentity(notice.row)),
              ),
            ),
          });
        }
        if (applied !== state.conversation) {
          if (
            changesRows(state.conversation, applied) ||
            unplacedWarningNotice !== null
          ) {
            const projected = projectConversation(applied);
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
        // Nothing is missing from the transcript and there is nothing to
        // fetch — the store displays the dropped warning from its own
        // transient surface instead (transientWarnings, above).
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
        pageItemIds.clear();
        pageOwnedTurnIds.clear();
        pageOwnedCompactTurnIds.clear();
        compactedTurnItems.clear();
        transientWarnings.length = 0;
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
