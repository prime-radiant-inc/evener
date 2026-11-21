// Pure notification fold: applyNotification folds one live AppWire
// notification at a time into the UI-ready ThreadModel. hydrateThread
// (transcriptHistory.ts) REPLACES the model wholesale (snapshot recovery, e.g.
// on (re)subscribe). Every function here is pure: given the same inputs,
// produces the same (possibly reference-equal, for no-op cases) output.

import type { ItemModel, ThreadModel, TurnModel } from "./model";
import {
  buildDisplayTurn,
  classifySignal,
  displayTurnSources,
  EMPTY_HISTORY,
  filterOverlay,
  type HistorySignal,
  historyIsLive,
  invalidated,
  MODEL_OUTPUT_ITEM_TYPES,
  type ModelExtras,
  mergeHistory,
  modelImageSessionRoute,
  pruneCoveredOverlay,
  publicModel,
  wireFragment,
  withDisplay,
  withoutNewerPageOmissions,
} from "./transcriptHistory";
import { upsertStableDelegate } from "./transcriptItems";
import type {
  AnyNotification,
  HistoryUpdatedParams,
  OverlayDeltaParams,
  OverlayItem,
  SandboxEscalationRequested,
  ThreadResyncParams,
} from "./types.gen";
import { foldWarningParams } from "./warningFold";

// history/updated: the full current form of every recorded item and turn an
// entry changed, merged by version under the one generation state machine.
function applyHistoryUpdated<M extends ThreadModel>(model: M, params: HistoryUpdatedParams, now: number): M {
  const held = model.history ?? EMPTY_HISTORY;
  const live = { ...model, lastFrameAt: now };
  if (held.failed !== undefined) return live;
  const signal: HistorySignal = {
    bootGeneration: params.bootGeneration,
    epoch: params.epoch,
    incarnation: params.snapshot.incarnation,
  };
  const action = classifySignal(held, signal);
  if (action === "invalidate") return { ...live, history: invalidated(held, signal) };
  if (action === "ignore" || held.invalidatedAtGeneration !== undefined) return live;
  // A notification projected after the page that omitted one of its identities
  // must not put it back: the page's observation still wins.
  const fragment = withoutNewerPageOmissions(
    held,
    wireFragment(params.turns, params.items, modelImageSessionRoute(model)),
    params.snapshot.length,
  );
  const turns = mergeHistory(held.turns, fragment);
  if (turns === held.turns) return live;
  return withDisplay(live, { ...held, turns }, model.overlay ?? {});
}

// Versioned resync invalidates history. A hub's epochless hint only requests
// a read, whose identity determines whether history changed. Invalidating here
// would drop all loaded pages.
function applyResync<M extends ThreadModel>(model: M, params: ThreadResyncParams, now: number): M {
  const held = model.history;
  if (!held) return model;
  const live = { ...model, lastFrameAt: now };
  if (!params.bootGeneration && params.epoch === undefined) return live;
  const signal: HistorySignal = { bootGeneration: params.bootGeneration || undefined, epoch: params.epoch };
  return classifySignal(held, signal) === "invalidate" ? { ...live, history: invalidated(held, signal) } : live;
}

// overlay/delta appends to one stream, preview or tool item. Only the display
// turn showing it is rebuilt. A delta for an item the overlay no longer holds
// (covered, reset or ended) is ignored.
function applyOverlayDelta<M extends ThreadModel>(model: M, params: OverlayDeltaParams, now: number): M {
  const held = model.overlay?.[params.key];
  const live = { ...model, lastFrameAt: now };
  if (!held || !historyIsLive(model.history)) return live;
  const item =
    params.field === "output"
      ? { ...held.item, output: (held.item.output ?? "") + params.delta }
      : { ...held.item, text: (held.item.text ?? "") + params.delta };
  const next: OverlayItem = { ...held, item };
  const overlay = { ...model.overlay, [params.key]: next };
  const index = model.turns.findIndex((turn) => displayTurnSources.get(turn)?.overlay.includes(held));
  const turn = model.turns[index];
  const source = turn && displayTurnSources.get(turn);
  // Every overlay item a display shows is in its turn's source; one that is not
  // shown yet (no turn found) takes the full derivation.
  if (!turn || !source) return withDisplay(live, model.history, overlay);
  const rebuilt = buildDisplayTurn(
    turn.id,
    { ...source, overlay: source.overlay.map((overlayItem) => (overlayItem === held ? next : overlayItem)) },
    modelImageSessionRoute(model),
  );
  return { ...live, overlay, turns: model.turns.map((shown, at) => (at === index ? rebuilt : shown)) };
}

// overlay/upserted, overlay/reset and overlay/end: replace the overlay and
// re-derive the display. Nothing applies while the thread is invalid.
function applyOverlayChange<M extends ThreadModel>(
  model: M,
  now: number,
  change: (overlay: Record<string, OverlayItem>) => Record<string, OverlayItem>,
): M {
  const live = { ...model, lastFrameAt: now };
  if (!historyIsLive(model.history)) return live;
  const current = model.overlay ?? {};
  const overlay = change(current);
  if (overlay === current) return live;
  return withDisplay(live, model.history, overlay);
}

// The fold's own form of resolvePendingEscalation: returns the concrete model,
// which is what applyNotificationToThread composes with. The exported wrapper
// below presents the distributive type.
function resolvePendingEscalationModel<M extends ThreadModel>(model: M, escalationId: string): M {
  if (!model.pendingEscalations.some((e) => e.escalationId === escalationId)) return model;
  return { ...model, pendingEscalations: model.pendingEscalations.filter((e) => e.escalationId !== escalationId) };
}

// Removes one pending escalation by id, returning the same reference when the
// id is absent (the no-op-same-reference idiom used throughout this file). Two
// callers reuse it: the threads store's resolveEscalation action, after this
// client's own successful evener/sandbox/escalation/resolve call, and the
// evener/sandbox/escalation/resolved notification case in applyNotification,
// which fires when another client's resolve — or a turn-interrupt or session
// close — retires the card. So a client merely watching the session drops its
// now-stale copy live off the broadcast instead of waiting for its next
// snapshot.
export function resolvePendingEscalation<M extends ThreadModel>(
  model: M,
  escalationId: string,
): ThreadModel & ModelExtras<M> {
  return publicModel<M>(resolvePendingEscalationModel(model, escalationId));
}

// notificationRoutingKey extracts the identity a frame routes by, from the
// frame's own params alone: ref before threadId, else nothing. It is the ONE
// source of truth for that precedence — notificationTargetsThread answers
// per-model matches from it, and the threads store's routing index keys off
// it directly — so the two layers cannot drift apart.
export type NotificationRoutingKey = { ref: string } | { threadId: string };

export function notificationRoutingKey(n: AnyNotification): NotificationRoutingKey | null {
  const params = n.params as { ref?: unknown; threadId?: unknown };
  if (typeof params.ref === "string") return { ref: params.ref };
  if (typeof params.threadId === "string") return { threadId: params.threadId };
  return null;
}

export function notificationTargetsThread(n: AnyNotification, model: ThreadModel): boolean {
  const key = notificationRoutingKey(n);
  if (!key) return false;
  if ("ref" in key) return key.ref === model.ref;
  return key.threadId === model.threadId;
}

// Replaces the turn identified by turnId with `fn(turn)`; turns not matching
// pass through unchanged (same reference). The whole ARRAY is also unchanged
// (same reference) when turnId names no turn in `turns` at all — every
// caller that needs to tell "the frame changed something" from "the frame
// named a turn outside this window" (a mobile store's gap detection, e.g.)
// reads that from array identity, and Array.prototype.map allocates a new
// array unconditionally, even when every mapped element came back identical.
function mapTurn(turns: TurnModel[], turnId: string, fn: (turn: TurnModel) => TurnModel): TurnModel[] {
  let changed = false;
  const mapped = turns.map((t) => {
    if (t.id !== turnId) return t;
    const next = fn(t);
    if (next !== t) changed = true;
    return next;
  });
  return changed ? mapped : turns;
}

// Appends `incoming` to pendingEscalations, or — if an entry with the same
// escalationId is already present — replaces it in place rather than
// growing the list. Dedup exists because hydration's surface-on-entry
// snapshot (thread.evener.pendingEscalations) and this live notification can
// legitimately race and both deliver the same card; last write wins.
function upsertPendingEscalation(
  escalations: SandboxEscalationRequested[],
  incoming: SandboxEscalationRequested,
): SandboxEscalationRequested[] {
  const idx = escalations.findIndex((e) => e.escalationId === incoming.escalationId);
  if (idx === -1) return [...escalations, incoming];
  return escalations.map((e, i) => (i === idx ? incoming : e));
}

// Folds one live wire notification into model. Most notifications carry
// ref/threadId and are matched via notificationTargetsThread — routing those
// to the right ThreadModel is the caller's job (or not: a mismatch is a safe
// no-op here) either way. turn/completed splits on whether it names the
// model's active turn: the active turn's own settle ends the turn, while any
// other turn's is the no-active-turn announcement path and folds through
// foldNonActiveTurnCompleted instead.
//
// Generic over the model: every case builds its result by spreading `model`
// and overriding only the fields it owns, so a caller's extra fields (native's
// MobileConversation = ThreadModel & { items }, say) survive the fold at
// runtime AND in the return type — the caller needs no cast and no re-spread.
// The return is ThreadModel & ModelExtras<M>, so this holds for extra fields
// while the fields the fold rewrites keep ThreadModel's types.
export function applyNotification<M extends ThreadModel>(
  model: M,
  n: AnyNotification,
  now: number,
): ThreadModel & ModelExtras<M> {
  const next = applyNotificationToThread(model, n, now);
  if (!next.modelRetry || !notificationTargetsThread(n, model)) return publicModel<M>(next);
  // A pending retry is sticky (design doc Component 1): it survives deltas
  // and other mid-grind item completions, clearing only on a turn boundary or
  // the completion of the model's own output item — otherwise a provider
  // grinding through retries looks like the indicator vanished for no reason.
  // No read-model replacement exists for the old turn-boundary clearing rule
  // (a turn boundary carries no notification of its own any more), so only
  // the model-output-item-completed half survives.
  const modelOutputCompleted =
    n.method === "history/updated" &&
    next.history?.turns !== model.history?.turns &&
    (n.params.items ?? []).some((item) => MODEL_OUTPUT_ITEM_TYPES.has(item.type));
  if (!modelOutputCompleted) return publicModel<M>(next);
  const cleared = { ...next };
  delete cleared.modelRetry;
  return publicModel<M>(cleared);
}

/**
 * Ends one round for the entry-backed transcript handoff without dropping
 * overlay slots that the reducer's current history has not recorded yet.
 * Ordinary `applyNotification` keeps its established overlay/end semantics.
 */
export function applyOverlayEndForTranscriptHandoff<M extends ThreadModel>(
  model: M,
  notification: Extract<AnyNotification, { method: "overlay/end" }>,
  now: number,
): ThreadModel & ModelExtras<M> {
  if (!notificationTargetsThread(notification, model)) return publicModel<M>(model);
  const roundId = notification.params.roundId;
  const isEndedRound = (item: OverlayItem) => item.kind !== "notice" && item.roundId === roundId;
  const next = applyOverlayChange(model, now, (overlay) => {
    const remaining = filterOverlay(overlay, isEndedRound);
    if (remaining === overlay) return overlay;
    const ended = filterOverlay(overlay, (item) => !isEndedRound(item));
    return { ...remaining, ...pruneCoveredOverlay(model.history?.turns ?? [], ended) };
  });
  return publicModel<M>(next);
}

// The known thread-scalar notifications: applyNotificationToThread's own case
// for each just restamps one scalar field (and lastFrameAt), never `turns`.
// History/updated, the overlay/* cases (which do touch turns),
// evener/thread/resync (its own clause) and warning (its own drop rule) are
// not named. Everything else, including a forward-compat method this client
// has no case for, is transcript-shaped.
const THREAD_SCALAR_ONLY_METHODS: ReadonlySet<string> = new Set([
  "thread/queueChanged",
  "thread/conversation-provenance/changed",
  "thread/status/changed",
  "thread/model/changed",
  "thread/reasoning-effort/changed",
  "evener/goal/updated",
  "evener/notes/updated",
  "evener/urls/updated",
  "thread/vision-model/changed",
  "evener/task/updated",
  "evener/thread/name/changed",
  "evener/sandbox/escalation/requested",
  "evener/sandbox/escalation/resolved",
  "evener/job/started",
  "evener/job/finished",
  "evener/delegate/updated",
  "evener/jobs/treeUpdated",
  "evener/thread/modelRetry",
]);

/** Whether a frame may change the thread's `turns`. A denylist on purpose: a
 * method nobody classified counts as transcript-shaped, so a consumer that
 * treats a frame leaving `turns` untouched as a gap errs toward refetching. */
export function notificationTouchesTranscript(method: string): boolean {
  return !THREAD_SCALAR_ONLY_METHODS.has(method);
}

function applyNotificationToThread<M extends ThreadModel>(model: M, n: AnyNotification, now: number): M {
  switch (n.method) {
    case "history/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyHistoryUpdated(model, n.params, now);
    }

    case "evener/thread/resync": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyResync(model, n.params, now);
    }

    case "overlay/upserted": {
      if (!notificationTargetsThread(n, model)) return model;
      const item = n.params.item;
      return applyOverlayChange(model, now, (overlay) => ({ ...overlay, [item.key]: item }));
    }

    case "overlay/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyOverlayDelta(model, n.params, now);
    }

    case "overlay/reset": {
      if (!notificationTargetsThread(n, model)) return model;
      const streamId = n.params.streamId;
      return applyOverlayChange(model, now, (overlay) => filterOverlay(overlay, (item) => item.streamId === streamId));
    }

    case "overlay/end": {
      if (!notificationTargetsThread(n, model)) return model;
      const roundId = n.params.roundId;
      return applyOverlayChange(model, now, (overlay) =>
        filterOverlay(overlay, (item) => item.kind !== "notice" && item.roundId === roundId),
      );
    }

    case "thread/queueChanged": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, queue: n.params.queue, lastFrameAt: now };
    }

    case "thread/conversation-provenance/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      // No transcript change, but the state names inputs that ended before
      // reaching canonical history (`retiredInputs`). That is the only durable
      // evidence a pending row has that its message will never arrive, so a
      // client rendering intent must keep it.
      return {
        ...model,
        ...(n.params.provenance === null ? {} : { conversationProvenance: n.params.provenance }),
        lastFrameAt: now,
      };
    }

    case "thread/status/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      const status = n.params.status;
      // The running turn is the frame's activeTurnId, and none when it names
      // none: an idle frame ends the running turn.
      const {
        runningTurnId: _previous,
        quotaResumesAt: heldQuotaResumesAt,
        quotaWaitStartedAt: heldQuotaStamp,
        modelRetry: heldChip,
        ...rest
      } = model;
      // A wait ends with its turn — a terminal transition, or a replacement:
      // an active frame naming a turn the model does not show running, with
      // no idle between. The wait's whole record retires as one unit —
      // deadline, stamp, and the chip that carried the wait's own facts —
      // never depending on whether the new turn announces a wait of its own.
      // An ordinary retry's chip is not a wait: the zero-delay progress state
      // a terminal frame can follow survives, as it always has.
      const replacedTurn =
        status.type === "active" &&
        n.params.activeTurnId !== undefined &&
        model.runningTurnId !== undefined &&
        n.params.activeTurnId !== model.runningTurnId;
      const waitEndsHere = status.type !== "active" || replacedTurn;
      // The turn this frame ended: a terminal frame's own endedTurnId, or
      // the running turn it leaves; a replacement ends the PREVIOUSLY running
      // turn — the frame has no endedTurnId, and the model's running turn is
      // still the replaced one at this point. A wait whose chip names a
      // different turn than the one ending is another turn's word and
      // survives.
      const endedTurnId =
        n.params.endedTurnId ??
        (status.type === "active" ? (replacedTurn ? model.runningTurnId : undefined) : model.runningTurnId);
      const endedWaitChip =
        waitEndsHere &&
        heldChip !== undefined &&
        heldChip.errorClass === "quota_exceeded" &&
        // Only the wait's own chip (its start's positive delay): the wake's
        // zero-delay chip is the retry's progress state, and progress is
        // ordinary retry behavior that survives the boundary as it always has.
        heldChip.delayMs > 0 &&
        endedTurnId !== undefined &&
        (heldChip.turnId ?? model.runningTurnId) === endedTurnId;
      return {
        ...(rest as M),
        ...(heldChip !== undefined && !endedWaitChip ? { modelRetry: heldChip } : {}),
        ...(n.params.activeTurnId ? { runningTurnId: n.params.activeTurnId } : {}),
        status,
        // The work-clock anchor (activeTurnStartedAt) has no live push to
        // refresh it, so a cold-hydrated live anchor would keep clocking
        // now-minus-anchor forever once the turn ends (StatusRow.tsx feeds it to
        // totalWorkMillis unconditionally). Drop it on any non-active
        // transition so the model never carries a live anchor while at rest.
        activeTurnStartedAt: status.type === "active" ? model.activeTurnStartedAt : undefined,
        // A usage-limit wait belongs to ONE running turn, so it ends with that
        // turn's status the same way the anchor above does. The daemon's own
        // end-of-wait signal (a no-delay quota_exceeded retry) does not cover
        // every way a wait ends: a Stop ends it through ctx.Done() and returns
        // before that emit (agent/session_quota_wait.go), so a model hydrated
        // during a six-hour wait would otherwise keep a future instant and say
        // "Waiting for quota" for the whole of the next turn while its output
        // streams. Re-adding it only while active keeps the fold's shape
        // unchanged for every model that never held one; the next read
        // refreshes the instant as it does every other snapshot field.
        ...(status.type === "active" && !replacedTurn && heldQuotaResumesAt !== undefined
          ? { quotaResumesAt: heldQuotaResumesAt }
          : {}),
        // The wait's stamp follows the same one-turn rule as the instant
        // above: retained while the turn keeps running, dropped with the
        // wait on any non-active transition or replacement. The ended stamp
        // is not the wait — it is the retirement marker that survives to
        // refuse a late frame for a wait that already ended.
        ...(status.type === "active" && !replacedTurn && heldQuotaStamp !== undefined
          ? { quotaWaitStartedAt: heldQuotaStamp }
          : {}),
        // A wait ends with its turn: a non-active frame, or the replacement
        // above, records the held stamp as ended so a late frame or read
        // still naming the wait cannot restamp it.
        ...(heldQuotaStamp !== undefined && waitEndsHere
          ? { quotaWaitEndedStamp: Math.max(model.quotaWaitEndedStamp ?? 0, heldQuotaStamp) }
          : {}),
        // The failure count is otherwise snapshot-only, so a client that
        // attached while the session was clean would keep saying nothing
        // however many failures followed - the watcher the count exists for.
        // A status change is a turn boundary, the only moment it can have
        // moved. Absent here means "no update" (an old daemon omits it), never
        // "nobody counted": clearing it would blank a figure the hydrate
        // legitimately gave us. Absence at HYDRATE is where unknown lives.
        failedToolCalls: n.params.failedToolCalls ?? model.failedToolCalls,
        // askPending is snapshot-authoritative and this is the wire refreshing
        // it, not the reducer deriving it: the hub stamps the flag on the frame
        // that goes with every clear of the pending set (a resolving user turn,
        // an interrupt), so a client stops showing "question waiting" without a
        // reread. Same absent-means-no-update rule as the count above; the ask
        // dock's own in-tool signal is still separate and still not this.
        askPending: n.params.askPending ?? model.askPending,
        // Capabilities are snapshot-only too, and two of them (send, queue)
        // are defined BY this very transition: the hub gates send on "no turn
        // in flight" and queue on "a turn in flight"
        // (server/appwire_thread_projection.go's appCapabilities; steer is harness
        // support alone). A set cut before the
        // turn therefore describes the wrong session by the time the composer
        // reads it back, which is how a running session came to show no Steer,
        // no Stop and a dead Send until the page was reloaded.
        // Same absent-means-no-update rule as the count above: a source that
        // omits the capability sends none, and clearing on absence would strip
        // the session of every action it advertised.
        capabilities: n.params.capabilities ?? model.capabilities,
        capabilitySource: n.params.capabilities ? "statusFrame" : model.capabilitySource,
        lastFrameAt: now,
      };
    }

    case "thread/model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      // ThreadModelChangedParams (appwire/types_turn.go) carries
      // reasoningEffortLevels/supportsReasoning/defaultReasoningEffort
      // alongside modelProvider/model, describing the NEW model's full
      // reasoning profile - not a partial patch, so an omitted/empty ladder or
      // default on the new payload replaces (not preserves) whatever the old
      // model's picker showed.
      return {
        ...model,
        modelProvider: n.params.modelProvider,
        model: n.params.model,
        // The frame names the provider itself, so the snapshot's separate
        // provider instance would only be a second, staler name for it. Only
        // cleared when a read set one, so a thread that never carried the key
        // keeps its shape.
        ...(model.modelProfile === undefined ? {} : { modelProfile: undefined }),
        reasoningEffortLevels: n.params.reasoningEffortLevels ?? [],
        supportsReasoning: n.params.supportsReasoning ?? false,
        defaultReasoningEffort: n.params.defaultReasoningEffort,
        contextUsed: n.params.contextUsed ?? (n.params.contextWindow !== undefined ? 0 : model.contextUsed),
        contextWindow: n.params.contextWindow ?? model.contextWindow,
        contextPressure: n.params.contextPressure ?? (n.params.contextWindow !== undefined ? 0 : model.contextPressure),
        contextAssumed: n.params.contextAssumed ?? false,
        compactionThreshold: n.params.compactionThreshold ?? 0,
        lastFrameAt: now,
      };
    }

    case "thread/reasoning-effort/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, reasoningEffort: n.params.reasoningEffort, lastFrameAt: now };
    }

    case "evener/goal/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, goal: n.params.goal ?? null, lastFrameAt: now };
    }

    case "evener/notes/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, humanNote: n.params.humanNote ?? "", agentNote: n.params.agentNote ?? "", lastFrameAt: now };
    }

    case "evener/urls/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, sessionUrls: n.params.urls ?? [], lastFrameAt: now };
    }

    case "thread/vision-model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, visionModel: n.params.visionModel, lastFrameAt: now };
    }

    case "evener/task/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        tasks: {
          total: n.params.total,
          done: n.params.done,
          ...(n.params.cancelled === undefined ? {} : { cancelled: n.params.cancelled }),
          ...(n.params.remaining === undefined
            ? n.params.cancelled === undefined
              ? {}
              : { remaining: Math.max(0, n.params.total - n.params.done - n.params.cancelled) }
            : { remaining: n.params.remaining }),
          ...(n.params.current ? { current: n.params.current } : {}),
        },
        lastFrameAt: now,
      };
    }

    case "evener/thread/name/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, name: n.params.name, lastFrameAt: now };
    }

    // PendingEscalations is THREAD-level human-client state, never a
    // transcript item (appwire/types_thread.go's ThreadEvener.PendingEscalations doc
    // comment: "a HUMAN-CLIENT field only ... never part of the model's
    // transcript or any model-visible projection"). The catalog entry
    // (appwire/protocol.go:185) declares this notification with its real
    // generated payload type (SandboxEscalationRequested), used verbatim
    // here rather than a local "(inline)" interface. See
    // upsertPendingEscalation for the dedup rationale.
    case "evener/sandbox/escalation/requested": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        pendingEscalations: upsertPendingEscalation(model.pendingEscalations, n.params),
        lastFrameAt: now,
      };
    }

    // The resolved twin of requested (wire-honesty spec Part B): a previously-
    // raised escalation left the pending set — resolved, turn-interrupted, or
    // cleared by session close. The payload carries no outcome (see
    // SandboxEscalationResolved's Go doc); this client clears its own copy by
    // id via the same helper the local resolve action reuses. resolvePending-
    // Escalation is a same-reference no-op for an id we never held, but a
    // targeted live frame still stamps lastFrameAt like every other case here.
    case "evener/sandbox/escalation/resolved": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...resolvePendingEscalationModel(model, n.params.escalationId), lastFrameAt: now };
    }

    // The model holds no job LIST at this layer, so the lifecycle pair leaves
    // its liveness signal (lastFrameAt) and a timestamp the jobs panel watches
    // to know its list went stale — the whole reason the panel need not poll.
    // Both ends bump it: starting and finishing both change what
    // evener/jobs/list returns. Live steering (below) is handled separately:
    // unlike jobs, it becomes a transcript item.
    case "evener/job/started":
    case "evener/job/finished": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/delegate/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      const delegates = upsertStableDelegate(model.delegates ?? [], n.params.delegate);
      if (delegates === model.delegates) return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
      return { ...model, delegates, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/jobs/treeUpdated": {
      if (!notificationTargetsThread(n, model)) return model;
      if (model.jobsTreeRevision !== null && n.params.revision <= model.jobsTreeRevision) return model;
      return { ...model, jobsTreeRevision: n.params.revision, jobsUpdatedAt: now };
    }

    case "warning": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // No active turn: there is nowhere wire-true to put it, and — unlike
      // evener/steering/injected's race window — warnings are not transcript-
      // persisted at all (internal/apptranscript has no warning-item
      // conversion), so the next snapshot would not carry it either. Drop
      // it client-side; only the liveness signal survives.
      // A v6 model shows warnings as overlay notices, and its display turns
      // are derived, so a minted item would vanish at the next derivation. The
      // hub's own relay-attach warning (cmd/evener-hub/app_rpc.go) still comes
      // as this notification and needs a v6 home before Task 20 deletes it.
      // bootGeneration "" (EMPTY_HISTORY's own spelling) means no real read
      // ever stamped one — applyReadModel's caller (a service layer that
      // hydrates outside this package's own snapshot branch) can mint a
      // `.history` of its own for merge continuity alone (applyReadModel's
      // doc comment) without that model ever having gone through a real v6
      // read, so a genuine bootGeneration is what actually distinguishes the
      // two here, not `.history`'s mere presence.
      if (!activeTurnId || model.history?.bootGeneration) return { ...model, lastFrameAt: now };
      const params = n.params;
      const folded = foldWarningParams(params);
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // Same collision-proofing as evener/steering/injected: count what's
          // already there rather than a global counter, so multiple
          // warnings in one turn land with distinct, order-preserving ids.
          const warningCount = turn.items.filter((it) => it.type === "warning").length;
          const item: ItemModel = {
            id: `item_warning_live_${activeTurnId}_${warningCount}`,
            turnId: activeTurnId,
            type: "warning",
            text: folded.text,
            status: "completed",
            warning: { source: folded.source, title: folded.title, hint: folded.hint, code: folded.code },
          };
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    case "evener/thread/modelRetry": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      // The retry chip this frame installs: the retry line's own data
      // (attempt, model, elapsed), also the wait's chip metadata while the
      // wait it names stands.
      const chip = {
        attempt: params.attempt,
        maxAttempts: params.maxAttempts,
        delayMs: params.delayMs,
        ...(params.errorClass ? { errorClass: params.errorClass } : {}),
        ...(params.statusCode ? { statusCode: params.statusCode } : {}),
        ...(params.turnId ? { turnId: params.turnId } : {}),
        ...(params.model ? { model: params.model } : {}),
        groupElapsedMs: params.groupElapsedMs,
        attemptCap: params.attemptCap,
        receivedAt: now,
      };
      // A usage-limit wait announces itself and its own end the way any retry
      // group does: a start announcement whose delay runs to the reset, then
      // the zero-delay wake when the retry goes out
      // (agent/session_quota_wait.go's emitQuotaRetry). Both carry the wait's
      // stamp — a strictly increasing daemon sequence, never a clock — so
      // every ordering decision between waits compares stamps. A frame for an
      // older wait of the same turn is never news: a turn id names only the
      // call site. No lastFrameAt restamp on purpose — see
      // ThreadModel.modelRetry. The quiet/stall clock is measuring a real
      // silence; this explains it rather than resetting it.
      if (params.errorClass !== "quota_exceeded") return { ...model, modelRetry: chip };
      const stamp = params.waitStartedAt ?? 0;
      const heldStamp = model.quotaWaitStartedAt ?? 0;
      const endedStamp = model.quotaWaitEndedStamp ?? 0;
      const deadlineFromFrame = params.resumesAt ? new Date(params.resumesAt).toISOString() : undefined;
      if (stamp > 0) {
        if (params.delayMs > 0) {
          // A start announcement. An equal stamp re-announces the wait the
          // model already holds: it fills the chip's metadata (model, elapsed,
          // receipt instant) and never moves the deadline — metadata matching
          // is by stamp, not turn id, and the instant the wait already carries
          // is the one the daemon said.
          if (stamp === heldStamp) {
            return { ...model, modelRetry: chip };
          }
          // A stamp the wake already ended names a wait that is over, and an
          // older stamp names a wait the held one superseded: neither is
          // news, whatever turn the frame carries.
          if (stamp <= endedStamp || (heldStamp > 0 && stamp < heldStamp)) return model;
          // A new wait: its stamp, its own deadline when it named one, and
          // its chip replace the record as one unit. A start that names no
          // instant installs no deadline: there is no receipt clock to fall
          // back to, so the wait shows nothing until a read carries the
          // instant.
          const installed: M = { ...model, modelRetry: chip, quotaWaitStartedAt: stamp };
          if (deadlineFromFrame !== undefined) installed.quotaResumesAt = deadlineFromFrame;
          else delete installed.quotaResumesAt;
          return installed;
        }
        // The wake. Matching the held wait ends it: deadline, stamp and the
        // chip's wait metadata go together, and the ended stamp survives to
        // refuse a later frame or read that still names this wait.
        if (stamp === heldStamp) {
          const cleared: M = {
            ...model,
            modelRetry: chip,
            quotaWaitEndedStamp: Math.max(endedStamp, stamp),
          };
          delete cleared.quotaResumesAt;
          delete cleared.quotaWaitStartedAt;
          return cleared;
        }
        // A wake for a wait the model does not hold — an older wait's, or
        // one whose start never reached this client — records only its
        // ended stamp: no chip, no clear, whatever turn the frame carries.
        if (stamp <= endedStamp) return model;
        return { ...model, quotaWaitEndedStamp: stamp };
      }
      // An unstamped frame (a daemon that predates the stamp) never
      // INSTALLS a wait — no deadline, no chip, no elapsed attribution. A
      // delay is not an absolute time, and this client's clock is not the
      // daemon's, so there is no receipt clock either: a daemon that predates
      // the stamp shows no wait in the pane, period (its row still shows the
      // wait from the owning root's roster sample — a different producer).
      // The one thing an unstamped frame may do is END a wait, and only by
      // turn ownership: a zero-delay wake naming the wait's owning turn
      // clears the held wait whole (stamped or not), the same retirement a
      // turn boundary applies; a wake naming any other turn changes nothing.
      // A wake with no held wait to end changes nothing either: the chip is
      // an installation (the retry's progress for a wait this client never
      // saw would attribute elapsed time nothing measured), so the frame is
      // a quota no-op unless the model holds the wait whose owner it names.
      if (params.delayMs !== 0) return model;
      const heldQuotaWait =
        model.quotaResumesAt !== undefined ||
        model.quotaWaitStartedAt !== undefined ||
        (model.modelRetry !== undefined && model.modelRetry.errorClass === "quota_exceeded");
      if (!heldQuotaWait) return model;
      const waitOwnerTurn = model.modelRetry?.turnId ?? model.runningTurnId;
      if (waitOwnerTurn === undefined || params.turnId !== waitOwnerTurn) return model;
      const waitEnded: M = {
        ...model,
        modelRetry: chip,
        // The held stamp, when the wait carried one, dies as the retirement
        // marker the same way a turn boundary records it — the ended stamp
        // survives to refuse a later frame or read still naming this wait.
        ...(heldStamp > 0 ? { quotaWaitEndedStamp: Math.max(endedStamp, heldStamp) } : {}),
      };
      delete waitEnded.quotaResumesAt;
      delete waitEnded.quotaWaitStartedAt;
      return waitEnded;
    }

    default:
      return model;
  }
}
