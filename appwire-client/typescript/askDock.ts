// The ask-dock store: the per-ref bookkeeping an answering surface keeps for
// the ask_user questions a session is waiting on - the live batches, the
// per-question answer drafts, the visible tab and the in-flight send - so it
// survives a view remount (a dockview pane, a virtual-list row, a native
// sheet). createAskDockStore is a factory: each app builds one instance, hands
// it the plain send path its answers go out through, and points it at its
// thread source with followThreads; two instances share nothing.
// deriveAskQuestions.ts owns the positional
// live-question scan, reconcileBatches.ts the prune/protect rules and
// askAnswers.ts the reply format; this module owns what sits between them: the
// answer drafts, the exclusion memory, the tab, the greeting and the send.
// Pure logic - no DOM, no React.

import { type AskResolution, composeAskAnswers } from "./askAnswers";
import { type AskQuestionRef, liveAskQuestions } from "./deriveAskQuestions";
import { sessionActionError } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore, type StoreListener } from "./frameworkFreeStore";
import type { ThreadModel } from "./model";
import { type AskBatch, reconcileBatches } from "./reconcileBatches";

export interface AskAnswerState {
  resolution: AskResolution | null;
  note: string;
}

export interface AskDockRefState {
  batches: AskBatch[];
  answers: Record<string, AskAnswerState>;
  // Which question's tab is the visible one, per batch (batchId -> question
  // key) - the one-question-at-a-time dock. Lives here, not in component
  // state, for the same reason answers do: a pane remount must not bounce the
  // reader back to question 1 mid-review. Entries are pruned alongside the
  // batch/question they point at; a missing entry means "show the first
  // question" (the dock's render-time default).
  active: Record<string, string>;
  // Keys this client has permanently finished with after its own durable send
  // commit. Excluding them from the live scan for this ref prevents the
  // original ask_user item from resurfacing before the eventual resolving
  // reply is reflected in the transcript.
  excludedKeys: Set<string>;
  // True once the dock has auto-focused for the current pending set. The dock
  // is the transcript's trailing virtual row, so scrolling far away unmounts
  // it and scrolling back remounts it - a component-level edge (useRef) would
  // treat every remount as a fresh activation and steal focus again. Living
  // here, the edge survives remounts exactly like answers/active do; it resets
  // to false whenever the pending set empties OR is atomically replaced (a
  // resync can swap an answered-elsewhere batch for a new one without ever
  // reading empty - activationEpoch's comment), so a genuinely new question
  // re-activates auto-focus.
  pendingGreeted: boolean;
  // Monotonic per-ref count of pending-set ACTIVATIONS: bumps when the set
  // goes empty -> non-empty, and when one reconcile atomically replaces the
  // whole set (disjoint batch ids, never empty in between - the reconnect/
  // resync case: another client answered the old ask while the agent posted a
  // new one). Same-set additions (a sibling batch while one is sending)
  // deliberately do NOT bump it: the reader was already told. This is the
  // signal the transcript's new-content pill edges on - a boolean pending flag
  // cannot express "still pending, but a different question now".
  activationEpoch: number;
}

// sendBatch's outcome is a discriminated union rather than a thrown error:
// every branch is an expected, named outcome the caller must handle
// differently (error is toast-worthy; stale is a silent no-op - the dock
// re-checked and found nothing left to send), not an exceptional condition.
//
// `message` is the finished sentence to show, not raw rejection text: the
// caller adds no label of its own.
export type SendBatchOutcome = { outcome: "sent" } | { outcome: "error"; message: string } | { outcome: "stale" };

// AskDockState is pure data: the per-ref bookkeeping a view binds to. Every
// action this store exposes is store-bound (AskDockStore) - the shape its own
// reconcile/beginSend/finishSend/followThreads already use - so a view reaches
// them as askDockStore.X, never askDockStore.getState().X.
export interface AskDockState {
  byRef: Map<string, AskDockRefState>;
  // How many batch ids this store has minted. Ids are purely local (never on
  // the wire), so a counter is the whole scheme; it lives in state so a reset
  // to the initial state restarts it, purely for readable test output.
  mintedBatches: number;
}

export interface AskDockThreadsSnapshot {
  readonly threads: ReadonlyMap<string, ThreadModel>;
}

/** The thread source a store follows: anything whose subscribers see the
 * ref -> ThreadModel map before and after each change. A store whose state
 * carries `threads` satisfies it as-is. */
export interface AskDockThreads {
  subscribe(listener: StoreListener<AskDockThreadsSnapshot>): () => void;
}

/** Submits one composed [answers] reply for a ref through the host's plain
 * send path; a rejection is the local enqueue failure sendBatch reports. */
export type AskAnswerSender = (ref: string, text: string) => Promise<void>;

export interface AskDockPorts {
  /** Absent for a host that drives beginSend/finishSend around its own send
   * path; sendBatch then throws rather than dropping answers. */
  send?: AskAnswerSender;
}

export interface AskDockStore extends FrameworkFreeStore<AskDockState> {
  setAnswer(ref: string, key: string, resolution: AskResolution | null): void;
  setNote(ref: string, key: string, note: string): void;
  // setActive records which question tab is visible for a batch. A key that
  // does not belong to the named batch is a no-op (never navigate the reader
  // to a question that is not there).
  setActive(ref: string, batchId: string, key: string): void;
  // markPendingGreeted records that the dock has auto-focused for this ref's
  // current pending set. No-op while nothing is pending - there is nothing to
  // greet.
  markPendingGreeted(ref: string): void;
  // sendBatch composes `batchId`'s current answers and submits them through
  // the host's send path (no dedicated wire method for answers exists). It
  // re-checks the batch still exists and isn't already sending before ever
  // calling send - a stale click (the ask already resolved elsewhere, or a
  // double-click on the same batch) is a silent no-op, never a duplicate/blind
  // request. Throws if the store was built without a sender: that is a
  // programming error, not an outcome the dock can show.
  sendBatch(ref: string, batchId: string): Promise<SendBatchOutcome>;
  /** Follow `threads`: every ref whose ThreadModel reference changes is
   * reconciled against its live question scan. Returns the disposer. */
  followThreads(threads: AskDockThreads): () => void;
  /** Fold one ref's current live question list (minus the keys this store has
   * settled itself) into its batches, drafts, tab and activation. The wired
   * path calls this with deriveAskQuestions' scan; a host without a
   * ThreadModel calls it with its own. */
  reconcile(ref: string, live: readonly AskQuestionRef[]): void;
  /** Freeze a batch for submission. False when it is missing or already
   * sending - the caller must not send. */
  beginSend(ref: string, batchId: string): boolean;
  /** Settle a frozen batch: accepted removes it and remembers its keys as
   * excluded; rejected thaws it, intact and retryable. A batch that is not
   * sending is left alone. */
  finishSend(ref: string, batchId: string, accepted: boolean): void;
}

const EMPTY_REF_STATE: AskDockRefState = {
  batches: [],
  answers: {},
  active: {},
  excludedKeys: new Set(),
  pendingGreeted: false,
  activationEpoch: 0,
};

// answerFor reads a key's current answer state with the same "missing means
// untouched" default sendBatch's own composition needs (an unresolved question
// composes as an explicit skip - askAnswers.ts).
function answerFor(refState: AskDockRefState, key: string): AskAnswerState {
  return refState.answers[key] ?? { resolution: null, note: "" };
}

// nextUnansweredKey finds the tab to advance to after the current question is
// resolved: the first still-unanswered question AFTER `fromIndex` in posting
// order, wrapping to earlier questions if none follows, so resolving the last
// tab circles back to whatever was skipped. `answers` must be the post-write
// map (the just-answered question reads as answered). Returns undefined when
// every other question is answered too - the reader stays put on the last tab
// rather than the dock yanking them away from a finished set.
//
// Two callers: setAnswer drives the auto-advance (a one-click resolution moves
// on by itself), and the dock's footer button drives the explicit advance (the
// primary action moves the reader on instead of submitting early, once the
// question on screen has an answer).
export function nextUnansweredKey(
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
// moment it lands, so the dock moves on. Multi-select checkboxes, free text,
// and an evener-decide leaning are all mid-edit states (more typing or more
// boxes may follow), so they never move the reader mid-gesture.
function advancesOnAnswer(questionMultiSelect: boolean, resolution: AskResolution): boolean {
  if (resolution.kind === "skip" || resolution.kind === "fallback") return true;
  return resolution.kind === "option" && !questionMultiSelect;
}

// isSendingQuestion answers whether the question key belongs to a batch
// mid-send. setAnswer/setNote refuse those writes: the card's editing controls
// are already disabled, and this is the store-level half of the same contract
// - an edit landing mid-flight would be silently discarded when the send
// resolves and removes the batch.
function isSendingQuestion(refState: AskDockRefState, key: string): boolean {
  return refState.batches.some((b) => b.sending && b.questions.some((q) => q.key === key));
}

// The per-ref transitions. Each returns the next ref state, or undefined when
// the change is a no-op so the store neither copies nor notifies.

function withAnswer(
  refState: AskDockRefState,
  key: string,
  resolution: AskResolution | null,
): AskDockRefState | undefined {
  if (isSendingQuestion(refState, key)) return undefined;
  const prior = answerFor(refState, key);
  const answers = { ...refState.answers, [key]: { ...prior, resolution } };
  // Auto-advance: a one-click resolution landing on the tab the reader is
  // currently on moves the dock to the next unanswered question. Only the
  // null -> answered transition does this (un-selecting, or editing an
  // already-answered question, stays put), and only when the answered key IS
  // the visible tab (an active entry that names another question means the
  // reader already moved on; an absent entry means the render-time default -
  // the first question - which can only be the answered one here, since only
  // the visible tab's controls exist to set a resolution).
  let active = refState.active;
  if (resolution !== null && prior.resolution === null) {
    const batch = refState.batches.find((b) => b.questions.some((q) => q.key === key));
    const index = batch?.questions.findIndex((q) => q.key === key) ?? -1;
    const question = index >= 0 ? batch?.questions[index] : undefined;
    if (
      batch !== undefined &&
      question !== undefined &&
      advancesOnAnswer(question.multiSelect, resolution) &&
      (refState.active[batch.id] === undefined || refState.active[batch.id] === key)
    ) {
      const next = nextUnansweredKey(batch, answers, index);
      if (next !== undefined) active = { ...refState.active, [batch.id]: next };
    }
  }
  return { ...refState, answers, active };
}

function withNote(refState: AskDockRefState, key: string, note: string): AskDockRefState | undefined {
  if (isSendingQuestion(refState, key)) return undefined;
  return { ...refState, answers: { ...refState.answers, [key]: { ...answerFor(refState, key), note } } };
}

function withActive(refState: AskDockRefState, batchId: string, key: string): AskDockRefState | undefined {
  const batch = refState.batches.find((b) => b.id === batchId);
  if (!batch?.questions.some((q) => q.key === key)) return undefined;
  if (refState.active[batchId] === key) return undefined;
  return { ...refState, active: { ...refState.active, [batchId]: key } };
}

function withGreeting(refState: AskDockRefState): AskDockRefState | undefined {
  if (refState.batches.length === 0 || refState.pendingGreeted) return undefined;
  return { ...refState, pendingGreeted: true };
}

function withBatchSending(refState: AskDockRefState, batch: AskBatch, sending: boolean): AskDockRefState {
  return { ...refState, batches: refState.batches.map((b) => (b === batch ? { ...b, sending } : b)) };
}

function withSending(refState: AskDockRefState, batchId: string): AskDockRefState | undefined {
  const batch = refState.batches.find((b) => b.id === batchId);
  if (!batch || batch.sending) return undefined;
  return withBatchSending(refState, batch, true);
}

// An accepted send drops the batch's live presence (no longer rendered or
// sendable) but keeps its keys on record in excludedKeys forever (see
// AskDockRefState's own doc comment) - the ref entry itself is never deleted
// once created, unlike a batch, because that permanent memory has to survive
// even once every batch is gone.
function withSendFinished(refState: AskDockRefState, batchId: string, accepted: boolean): AskDockRefState | undefined {
  const batch = refState.batches.find((b) => b.id === batchId);
  if (!batch?.sending) return undefined;
  if (!accepted) return withBatchSending(refState, batch, false);
  const removedKeys = new Set(batch.questions.map((q) => q.key));
  const answers: Record<string, AskAnswerState> = {};
  for (const [key, value] of Object.entries(refState.answers)) {
    if (!removedKeys.has(key)) answers[key] = value;
  }
  const active: Record<string, string> = {};
  for (const [id, key] of Object.entries(refState.active)) {
    if (id !== batchId) active[id] = key;
  }
  const excludedKeys = new Set(refState.excludedKeys);
  for (const key of removedKeys) excludedKeys.add(key);
  const batches = refState.batches.filter((b) => b !== batch);
  return {
    batches,
    answers,
    active,
    excludedKeys,
    // An emptied pending set ends the greeting: the next question to arrive
    // is a fresh activation and auto-focuses again. The epoch does not move
    // here - the bump happens when the new set actually arrives.
    pendingGreeted: batches.length === 0 ? false : refState.pendingGreeted,
    activationEpoch: refState.activationEpoch,
  };
}

// withLiveQuestions folds one ref's current live question list into its
// bookkeeping: drop anything this client has permanently excluded, reconcile
// batches against the rest (reconcileBatches.ts owns the prune/protect
// rules), and prune any per-question answer draft whose key no longer belongs
// to any batch (durably submitted or resolved by someone else; there is
// nothing left for that draft to attach to).
function withLiveQuestions(
  refState: AskDockRefState,
  liveAll: readonly AskQuestionRef[],
  mintedBatches: number,
): { refState: AskDockRefState; mintedBatches: number } | undefined {
  let minted = mintedBatches;
  const live = liveAll.filter((q) => !refState.excludedKeys.has(q.key));
  const batches = reconcileBatches(refState.batches, live, () => `ask-batch-${++minted}`);
  if (batches === refState.batches) return undefined; // nothing changed, including "still nothing tracked"

  const trackedKeys = new Set(batches.flatMap((b) => b.questions.map((q) => q.key)));
  const answers: Record<string, AskAnswerState> = {};
  for (const [key, value] of Object.entries(refState.answers)) {
    if (trackedKeys.has(key)) answers[key] = value;
  }
  // Recommended default seeding: a question whose options name a recommended
  // one starts ANSWERED with it, so the reader edits a pre-selected default
  // instead of building every answer from nothing - and the dock's footer can
  // require a walk through every question before Send, since each already has
  // a real answer. Only keys with NO entry are filled: an in-progress answer
  // is never clobbered, and a deliberately cleared one (setAnswer null leaves
  // an entry behind) is never re-seeded.
  for (const batch of batches) {
    for (const q of batch.questions) {
      if (answers[q.key]) continue;
      const recommended = q.options.filter((o) => o.recommended).map((o) => o.label);
      if (recommended.length > 0) {
        answers[q.key] = { resolution: { kind: "option", labels: recommended }, note: "" };
      }
    }
  }
  // An active-tab entry is valid only while its batch still exists AND its
  // question still belongs to that batch - otherwise drop it so the dock
  // falls back to its render-time default rather than pointing at a tab that
  // no longer exists.
  const active: Record<string, string> = {};
  for (const [batchId, key] of Object.entries(refState.active)) {
    const batch = batches.find((b) => b.id === batchId);
    if (batch?.questions.some((q) => q.key === key)) active[batchId] = key;
  }

  // Fresh activation = empty -> non-empty, or an atomic REPLACEMENT (one
  // reconcile swapped the whole set: disjoint batch ids, never empty in
  // between - the reconnect/resync case where another client answered the old
  // ask while the agent posted a new one). Both re-arm the greeting and bump
  // the epoch; a same-set addition retains a batch id and deliberately does
  // neither (the reader was already told).
  const retainsPendingBatch = batches.some((b) => refState.batches.some((old) => old.id === b.id));
  const freshActivation = batches.length > 0 && !retainsPendingBatch;

  return {
    mintedBatches: minted,
    refState: {
      batches,
      answers,
      active,
      excludedKeys: refState.excludedKeys,
      // The greeting survives only while at least one greeted batch is still
      // pending; an emptied set and a full replacement both re-arm.
      pendingGreeted: retainsPendingBatch ? refState.pendingGreeted : false,
      activationEpoch: refState.activationEpoch + (freshActivation ? 1 : 0),
    },
  };
}

/** Builds an empty ask-dock store over the host's ports. */
export function createAskDockStore({ send }: AskDockPorts = {}): AskDockStore {
  const store = createFrameworkFreeStore<AskDockState>(() => ({
    byRef: new Map(),
    mintedBatches: 0,
  }));

  // Applies one ref transition, copying and notifying only when it changed.
  const updateRef = (ref: string, change: (refState: AskDockRefState) => AskDockRefState | undefined): boolean => {
    const s = store.getState();
    const next = change(s.byRef.get(ref) ?? EMPTY_REF_STATE);
    if (next === undefined) return false;
    store.setState({ byRef: new Map(s.byRef).set(ref, next) });
    return true;
  };

  const beginSend = (ref: string, batchId: string): boolean =>
    updateRef(ref, (refState) => withSending(refState, batchId));

  const finishSend = (ref: string, batchId: string, accepted: boolean): void => {
    updateRef(ref, (refState) => withSendFinished(refState, batchId, accepted));
  };

  const reconcile = (ref: string, live: readonly AskQuestionRef[]): void => {
    const s = store.getState();
    const next = withLiveQuestions(s.byRef.get(ref) ?? EMPTY_REF_STATE, live, s.mintedBatches);
    if (next === undefined) return;
    store.setState({ byRef: new Map(s.byRef).set(ref, next.refState), mintedBatches: next.mintedBatches });
  };

  return {
    ...store,

    setAnswer(ref, key, resolution) {
      updateRef(ref, (refState) => withAnswer(refState, key, resolution));
    },

    setNote(ref, key, note) {
      updateRef(ref, (refState) => withNote(refState, key, note));
    },

    setActive(ref, batchId, key) {
      updateRef(ref, (refState) => withActive(refState, batchId, key));
    },

    markPendingGreeted(ref) {
      updateRef(ref, withGreeting);
    },

    async sendBatch(ref, batchId) {
      if (send === undefined) {
        throw new Error("ask-dock store has no sender: pass { send } to createAskDockStore before sendBatch");
      }
      const refState = store.getState().byRef.get(ref);
      const batch = refState?.batches.find((b) => b.id === batchId);
      if (!refState || !batch || batch.sending) return { outcome: "stale" };

      // Composed before the batch is frozen: a throw here (a programming
      // error, never a wire outcome) leaves the batch unfrozen and retryable
      // instead of stranding it as sending forever.
      const composedText = composeAskAnswers(
        batch.questions.map((q) => {
          const answer = answerFor(refState, q.key);
          return { header: q.header, resolution: answer.resolution, note: answer.note, ifUnanswered: q.ifUnanswered };
        }),
      );
      if (!beginSend(ref, batchId)) return { outcome: "stale" };
      try {
        await send(ref, composedText);
        finishSend(ref, batchId, true);
        return { outcome: "sent" };
      } catch (err) {
        finishSend(ref, batchId, false);
        // Composed here, not by the caller: this is the only side of the seam
        // that still holds the local enqueue rejection, so it is the only side
        // that can distinguish a failed send from a failed session resume
        // (errors.ts's sessionActionError). No RPC was eligible to start, so
        // the batch stays intact and retryable.
        return { outcome: "error", message: sessionActionError("Couldn't send answers", err) };
      }
    },

    reconcile,
    beginSend,
    finishSend,
    followThreads(threads) {
      // Fires on every change of the source, but only reconciles the refs
      // whose tracked ThreadModel reference changed (a same-reference no-op
      // elsewhere in the source, e.g. an update touching only an unrelated
      // field, correctly does nothing here).
      return threads.subscribe((state, previous) => {
        if (state.threads === previous.threads) return;
        for (const [ref, model] of state.threads) {
          if (previous.threads.get(ref) !== model) reconcile(ref, liveAskQuestions(model));
        }
      });
    },
  };
}
