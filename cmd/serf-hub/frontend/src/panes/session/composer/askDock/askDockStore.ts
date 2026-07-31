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
// sendBatch for how the in-flight submission race is actually resolved:
// freeze the batch until its durable outbox commit, then let the outbox and
// recovery surfaces own later network outcomes.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { sessionActionError } from "../../../../protocol/errors";
import type { ThreadModel } from "../../../../protocol/model";
import { threadsStore } from "../../../../stores/threads";
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
  // Which question's tab is the visible one, per batch (batchId -> question
  // key) - kata 99yf's one-question-at-a-time dock. Lives here, not in
  // component state, for the same wave-5 reason answers do: a dockview pane
  // remount must not bounce the reader back to question 1 mid-review.
  // Entries are pruned alongside the batch/question they point at
  // (reconcileRef/removeBatch); a missing entry means "show the first
  // question" (AskDock's render-time default).
  active: Record<string, string>;
  // Keys this client has permanently finished with after our own durable send
  // commit. Excluding them from liveAskQuestions for this ref prevents the
  // original ask_user item from resurfacing before the eventual resolving
  // reply is reflected in the transcript.
  excludedKeys: Set<string>;
}

// sendBatch's outcome is a discriminated union rather than a thrown error:
// every branch here is an expected, named outcome the caller (AskDock.tsx)
// must handle differently (error is toast-worthy per the wave's failure-
// feedback convention; stale is a silent no-op - the dock re-checked and
// found nothing left to send), not an exceptional condition.
//
// `message` is the finished sentence to show, not raw rejection text: the
// caller adds no label of its own. See sendBatch's own error branch.
export type SendBatchOutcome = { outcome: "sent" } | { outcome: "error"; message: string } | { outcome: "stale" };

export interface AskDockState {
  byRef: Map<string, AskDockRefState>;
  setAnswer(ref: string, key: string, resolution: AskResolution | null): void;
  setNote(ref: string, key: string, note: string): void;
  // setActive records which question tab is visible for a batch. A key that
  // does not belong to the named batch is a no-op (never navigate the reader
  // to a question that is not there).
  setActive(ref: string, batchId: string, key: string): void;
  // sendBatch composes `batchId`'s current answers and submits them through
  // the plain threadsStore.send() path (spec: no dedicated wire method for
  // answers exists - verified). Re-checks the batch still exists and isn't
  // already sending before ever calling send() - a stale click (the ask
  // already resolved elsewhere, or a double-click on the same batch) is a
  // silent no-op, never a duplicate/blind request.
  sendBatch(ref: string, batchId: string): Promise<SendBatchOutcome>;
}

const EMPTY_REF_STATE: AskDockRefState = { batches: [], answers: {}, active: {}, excludedKeys: new Set() };

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

// removeBatch drops a durably submitted batch's live presence (no longer
// rendered or sendable) but keeps its keys on record in
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
    const nextActive: Record<string, string> = {};
    for (const [id, key] of Object.entries(refState.active)) {
      if (id !== batchId) nextActive[id] = key;
    }
    const nextExcluded = new Set(refState.excludedKeys);
    for (const key of removedKeys) nextExcluded.add(key);
    const nextByRef = new Map(s.byRef);
    nextByRef.set(ref, {
      batches: refState.batches.filter((b) => b.id !== batchId),
      answers: nextAnswers,
      active: nextActive,
      excludedKeys: nextExcluded,
    });
    return { byRef: nextByRef };
  });
}

// nextUnansweredKey finds the tab to auto-advance to after a one-click
// resolution lands (kata 99yf): the first still-unanswered question AFTER
// `fromIndex` in posting order, wrapping to earlier questions if none
// follows, so answering the last tab circles back to whatever was skipped.
// `answers` must be the post-write map (the just-answered question reads as
// answered). Returns undefined when every other question is answered too -
// the reader stays put on the last tab rather than the dock yanking them
// away from a finished set.
function nextUnansweredKey(
  batch: AskBatch,
  answers: Record<string, AskAnswerState>,
  fromIndex: number,
): string | undefined {
  const total = batch.questions.length;
  for (let step = 1; step < total; step++) {
    const q = batch.questions[(fromIndex + step) % total];
    if (q !== undefined && (answers[q.key]?.resolution ?? null) === null) return q.key;
  }
  return undefined;
}

// advancesOnAnswer is the one-click-resolution rule for auto-advance: a
// single-select option pick, a skip, or a fallback is a COMPLETE answer the
// moment it lands, so the dock moves on. Multi-select checkboxes, free
// text, and a serf-decide leaning are all mid-edit states (more typing or
// more boxes may follow), so they never move the reader mid-gesture.
function advancesOnAnswer(questionMultiSelect: boolean, resolution: AskResolution): boolean {
  if (resolution.kind === "skip" || resolution.kind === "fallback") return true;
  return resolution.kind === "option" && !questionMultiSelect;
}

export const askDockStore = createStore<AskDockState>(() => ({
  byRef: new Map(),

  setAnswer(ref, key, resolution) {
    askDockStore.setState((s) => {
      const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
      const nextAnswers = { ...refState.answers, [key]: { resolution, note: answerFor(refState, key).note } };
      // Auto-advance (kata 99yf): a one-click resolution landing on the tab
      // the reader is currently on moves the dock to the next unanswered
      // question. Only the null -> answered transition does this (un-
      // selecting, or editing an already-answered question, stays put), and
      // only when the answered key IS the visible tab (an active entry that
      // names another question means the reader already moved on; an absent
      // entry means the render-time default - the first question - which
      // can only be the answered one here, since only the visible tab's
      // controls exist to set a resolution).
      let nextActive = refState.active;
      if (resolution !== null && answerFor(refState, key).resolution === null) {
        const batch = refState.batches.find((b) => b.questions.some((q) => q.key === key));
        const index = batch?.questions.findIndex((q) => q.key === key) ?? -1;
        const question = index >= 0 ? batch?.questions[index] : undefined;
        if (
          batch !== undefined &&
          question !== undefined &&
          advancesOnAnswer(question.multiSelect, resolution) &&
          (refState.active[batch.id] === undefined || refState.active[batch.id] === key)
        ) {
          const next = nextUnansweredKey(batch, nextAnswers, index);
          if (next !== undefined) nextActive = { ...refState.active, [batch.id]: next };
        }
      }
      const nextByRef = new Map(s.byRef);
      nextByRef.set(ref, {
        ...refState,
        answers: nextAnswers,
        active: nextActive,
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

  setActive(ref, batchId, key) {
    askDockStore.setState((s) => {
      const refState = s.byRef.get(ref) ?? EMPTY_REF_STATE;
      const batch = refState.batches.find((b) => b.id === batchId);
      if (!batch?.questions.some((q) => q.key === key)) return s;
      if (refState.active[batchId] === key) return s;
      const nextByRef = new Map(s.byRef);
      nextByRef.set(ref, { ...refState, active: { ...refState.active, [batchId]: key } });
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
      setBatchSending(ref, batchId, false);
      // Composed here, not by the caller: this is the only side of the seam
      // that still holds the local enqueue rejection, so it is the only side
      // that can distinguish a failed send from a failed session resume
      // (protocol/errors.ts's sessionActionError). No RPC was eligible to
      // start, so the batch stays intact and retryable.
      return { outcome: "error", message: sessionActionError("Couldn't send answers", err) };
    }
  },
}));

// reconcileRef folds one ref's current ThreadModel into its ask-dock
// bookkeeping: recompute the live question set (minus anything this client
// has permanently excluded - see AskDockRefState's own doc comment),
// reconcile batches against it (reconcileBatches.ts owns the actual merge/
// prune/protect rules), and prune any per-question answer draft whose key no
// longer belongs to any batch (durably submitted or resolved by someone else;
// there is nothing left for that draft to attach to).
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
    // An active-tab entry is valid only while its batch still exists AND its
    // question still belongs to that batch - otherwise drop it so the dock
    // falls back to its render-time default rather than pointing at a tab
    // that no longer exists.
    const nextActive: Record<string, string> = {};
    for (const [batchId, key] of Object.entries(refState.active)) {
      const batch = nextBatches.find((b) => b.id === batchId);
      if (batch?.questions.some((q) => q.key === key)) nextActive[batchId] = key;
    }

    const nextByRef = new Map(s.byRef);
    nextByRef.set(ref, {
      batches: nextBatches,
      answers: nextAnswers,
      active: nextActive,
      excludedKeys: refState.excludedKeys,
    });
    return { byRef: nextByRef };
  });
}

// Registered once, at module load (same lifetime as threads.ts's own
// connectionStore.subscribe wiring) - fires on every threadsStore change,
// but only actually reconciles the refs whose tracked ThreadModel reference
// changed (a same-reference no-op elsewhere in threadsStore, e.g. an update
// touching only an unrelated store field, correctly does nothing here).
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
