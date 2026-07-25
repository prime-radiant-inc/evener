// askDockStore is the module-level, per-ref bookkeeping for the ask_user
// answering dock - mirrors stores/threads.ts's own singleton-vanilla-store
// pattern (a Map keyed by ref, reactively re-derived off threadsStore's own
// changes) so a dock's in-progress answers and in-flight-send state survive
// a dockview pane remount (wave-5 binding constraint: "durable state ...
// ask bookkeeping ... lives in stores, never component state that matters
// across a tab switch").
//
// Reconciliation is wired at module load via threadsStore.subscribe, not a
// component-level useEffect: this is what lets a session's FIRST hydrate
// (which can complete before any AskDock ever mounts) still populate
// batches immediately, and it mirrors threads.ts's own connectionStore.
// subscribe wiring exactly. See reconcileBatches.ts's own header for why a
// purely positional signal isn't enough on its own, and this file's
// sendBatch for how the in-flight submission race is actually resolved
// (freeze the batch, settle it directly off the request's own outcome -
// never by waiting to observe its echo back in the transcript).
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../../../../protocol/errors";
import type { ThreadModel } from "../../../../protocol/model";
import { ConflictError, threadsStore } from "../../../../stores/threads";
import { type AskResolution, composeAskAnswers } from "./askCompose";
import { liveAskQuestions } from "./deriveAskQuestions";
import { type AskBatch, reconcileBatches } from "./reconcileBatches";

export interface AskAnswerState {
  resolution: AskResolution | null;
  note: string;
}

export interface AskDockRefState {
  batches: AskBatch[];
  answers: Record<string, AskAnswerState>;
  // Keys this client has permanently finished with (settled by our own
  // successful send, or discarded by our own Conflict) - excluded from
  // liveAskQuestions' result before it ever reaches reconcileBatches, for
  // the lifetime of this ref's tracked state. Necessary because a
  // Conflict-discarded call's own ask_user item never changes status in
  // the transcript itself (only a REAL resolving reply - ours or another
  // client's - would positionally exclude it via deriveAskQuestions, and
  // that reply's own notification is not guaranteed to have landed yet by
  // the time some unrelated model change next triggers reconciliation);
  // without this, a later, unrelated ask_user ack could resurrect a
  // just-discarded question into a fresh batch (contracts-composer-queue-
  // pending.md: "a later acknowledged ask_user call starts a completely
  // fresh pending set instead of merging into the stale conflicted one").
  excludedKeys: Set<string>;
}

// sendBatch's outcome is a discriminated union rather than a thrown error:
// every branch here is an expected, named outcome the caller (AskDock.tsx)
// must handle differently (conflict hands composed text to
// onFallbackToComposer; error is toast-worthy per the wave's failure-
// feedback convention; stale is a silent no-op - the dock re-checked and
// found nothing left to send), not an exceptional condition.
export type SendBatchOutcome =
  | { outcome: "sent" }
  | { outcome: "conflict"; text: string }
  | { outcome: "error"; message: string }
  | { outcome: "stale" };

export interface AskDockState {
  byRef: Map<string, AskDockRefState>;
  setAnswer(ref: string, key: string, resolution: AskResolution | null): void;
  setNote(ref: string, key: string, note: string): void;
  // sendBatch composes `batchId`'s current answers and submits them through
  // the plain threadsStore.send() path (spec: no dedicated wire method for
  // answers exists - verified). Re-checks the batch still exists and isn't
  // already sending before ever calling send() - a stale click (the ask
  // already resolved elsewhere, or a double-click on the same batch) is a
  // silent no-op, never a duplicate/blind request.
  sendBatch(ref: string, batchId: string): Promise<SendBatchOutcome>;
}

const EMPTY_REF_STATE: AskDockRefState = { batches: [], answers: {}, excludedKeys: new Set() };

// Batch ids are purely local identifiers (never sent over the wire) - a
// monotonic counter is simplest and sufficient; resetAskDockStoreForTests
// below resets it too, purely for readable test output, not correctness.
let nextBatchId = 0;
function mintBatchId(): string {
  nextBatchId += 1;
  return `ask-batch-${nextBatchId}`;
}

// answerFor reads a key's current answer state with the same "missing
// means untouched" default sendBatch's own composition needs (an
// unresolved question composes as an explicit skip - askCompose.ts).
function answerFor(refState: AskDockRefState, key: string): AskAnswerState {
  return refState.answers[key] ?? { resolution: null, note: "" };
}

function setBatchSending(ref: string, batchId: string, sending: boolean): void {
  askDockStore.setState((s) => {
    const refState = s.byRef.get(ref);
    if (!refState) return s;
    const nextByRef = new Map(s.byRef);
    nextByRef.set(ref, {
      ...refState,
      batches: refState.batches.map((b) => (b.id === batchId ? { ...b, sending } : b)),
    });
    return { byRef: nextByRef };
  });
}

// removeBatch drops a settled/discarded batch's LIVE presence (no longer
// rendered, no longer sendable) but keeps its keys on record in
// excludedKeys forever (see AskDockRefState's own doc comment) - the entry
// itself is never deleted once created, unlike a batch, because that
// permanent memory has to survive even once every batch is gone.
function removeBatch(ref: string, batchId: string): void {
  askDockStore.setState((s) => {
    const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
    const removed = refState.batches.find((b) => b.id === batchId);
    if (!removed) return s;
    const removedKeys = new Set(removed.questions.map((q) => q.key));
    const nextAnswers: Record<string, AskAnswerState> = {};
    for (const [key, value] of Object.entries(refState.answers)) {
      if (!removedKeys.has(key)) nextAnswers[key] = value;
    }
    const nextExcluded = new Set(refState.excludedKeys);
    for (const key of removedKeys) nextExcluded.add(key);
    const nextByRef = new Map(s.byRef);
    nextByRef.set(ref, {
      batches: refState.batches.filter((b) => b.id !== batchId),
      answers: nextAnswers,
      excludedKeys: nextExcluded,
    });
    return { byRef: nextByRef };
  });
}

export const askDockStore = createStore<AskDockState>(() => ({
  byRef: new Map(),

  setAnswer(ref, key, resolution) {
    askDockStore.setState((s) => {
      const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
      const nextByRef = new Map(s.byRef);
      nextByRef.set(ref, {
        ...refState,
        answers: { ...refState.answers, [key]: { resolution, note: answerFor(refState, key).note } },
      });
      return { byRef: nextByRef };
    });
  },

  setNote(ref, key, note) {
    askDockStore.setState((s) => {
      const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
      const nextByRef = new Map(s.byRef);
      nextByRef.set(ref, {
        ...refState,
        answers: { ...refState.answers, [key]: { resolution: answerFor(refState, key).resolution, note } },
      });
      return { byRef: nextByRef };
    });
  },

  async sendBatch(ref, batchId) {
    const refState = askDockStore.getState().byRef.get(ref);
    const batch = refState?.batches.find((b) => b.id === batchId);
    if (!refState || !batch || batch.sending) return { outcome: "stale" };

    const composedText = composeAskAnswers(
      batch.questions.map((q) => {
        const answer = answerFor(refState, q.key);
        return { header: q.header, resolution: answer.resolution, note: answer.note, ifUnanswered: q.ifUnanswered };
      }),
    );

    setBatchSending(ref, batchId, true);
    try {
      await threadsStore.getState().send(ref, composedText);
      removeBatch(ref, batchId);
      return { outcome: "sent" };
    } catch (err) {
      if (err instanceof ConflictError) {
        removeBatch(ref, batchId);
        return { outcome: "conflict", text: composedText };
      }
      setBatchSending(ref, batchId, false);
      return { outcome: "error", message: errorText(err) };
    }
  },
}));

// reconcileRef folds one ref's current ThreadModel into its ask-dock
// bookkeeping: recompute the live question set (minus anything this client
// has permanently excluded - see AskDockRefState's own doc comment),
// reconcile batches against it (reconcileBatches.ts owns the actual merge/
// prune/protect rules), and prune any per-question answer draft whose key
// no longer belongs to any batch (settled, discarded, or resolved by
// someone else - there is nothing left for that draft to attach to).
function reconcileRef(ref: string, model: ThreadModel): void {
  const liveAll = liveAskQuestions(model);
  askDockStore.setState((s) => {
    const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
    const live = liveAll.filter((q) => !refState.excludedKeys.has(q.key));
    const nextBatches = reconcileBatches(refState.batches, live, mintBatchId);
    if (nextBatches === refState.batches) return s; // nothing changed, including "still nothing tracked"

    const trackedKeys = new Set(nextBatches.flatMap((b) => b.questions.map((q) => q.key)));
    const nextAnswers: Record<string, AskAnswerState> = {};
    for (const [key, value] of Object.entries(refState.answers)) {
      if (trackedKeys.has(key)) nextAnswers[key] = value;
    }

    const nextByRef = new Map(s.byRef);
    nextByRef.set(ref, { batches: nextBatches, answers: nextAnswers, excludedKeys: refState.excludedKeys });
    return { byRef: nextByRef };
  });
}

// Registered once, at module load (same lifetime as threads.ts's own
// connectionStore.subscribe wiring) - fires on every threadsStore change,
// but only actually reconciles the refs whose tracked ThreadModel reference
// changed (a same-reference no-op elsewhere in threadsStore, e.g. a
// scrollPositions-only update, correctly does nothing here).
threadsStore.subscribe((state, prevState) => {
  if (state.threads === prevState.threads) return;
  for (const [ref, model] of state.threads) {
    if (prevState.threads.get(ref) !== model) reconcileRef(ref, model);
  }
});

export function useAskDockStore(): AskDockState;
export function useAskDockStore<T>(selector: (state: AskDockState) => T): T;
export function useAskDockStore<T>(selector?: (state: AskDockState) => T): T | AskDockState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(askDockStore, selector) : useStore(askDockStore);
}

// resetAskDockStoreForTests resets this module's singleton state between
// tests - same rationale as threads.ts's resetThreadsStoreForTests (one
// Map shared by the whole app). No production code should call this.
export function resetAskDockStoreForTests(): void {
  nextBatchId = 0;
  askDockStore.setState({ byRef: new Map() });
}
