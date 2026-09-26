// The pending-turns projection store: one host's live view of its own
// durable outbox/optimistic/recovery records, reconciled against a thread's
// current model through `reconcilePendingEntries`, plus the bookkeeping a
// submission's lifecycle needs - which client mutation ids this page itself
// has submitted, which refs have a submission in flight, and whether a
// composer's stored draft still matches what was actually sent once that
// submission settles.
//
// Framework-free: no React, no zustand, no storage. `outbox`/`optimistic`/
// `recovery` are plain settable fields the host writes after its own durable
// read (IndexedDB on the web) - this module never reads or writes storage
// itself. The two things a host's storage and drafts DO have to answer -
// "what is ref X's current thread model" and "what did ref X's composer
// draft hold, and how do I clear it" - are the injected ports below, sized
// to exactly what this module calls.
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type { ThreadModel } from "../../model";
import { type PendingMethod, type PendingTurnEntry, reconcilePendingEntries } from "./pendingEntries";
import type {
  ClientIdentity,
  MutationAttachmentRef,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationOutboxState,
  MutationRecoveryRecord,
} from "./records";

// A pure read over `model`'s active turn: true once an identified user
// message (one carrying its own clientMutationId) has landed in it and
// nothing but a system message has followed since - the "sent, not yet
// answered" state a composer shows with no confirmation timer of its own,
// retired the moment a real assistant frame lands.
export function awaitingFirstFrameSend(model: ThreadModel | undefined): boolean {
  const activeTurn = model?.turns.find((turn) => turn.id === model.activeTurnId);
  if (!activeTurn) return false;
  let sawIdentifiedUserMessage = false;
  for (const item of activeTurn.items) {
    if (item.type === "userMessage" && item.clientMutationId) {
      sawIdentifiedUserMessage = true;
      continue;
    }
    if (sawIdentifiedUserMessage && item.type !== "systemMessage") return false;
  }
  return sawIdentifiedUserMessage;
}

// `ref`'s recovery records, oldest submission first - the order a recovery
// list renders in.
export function recoveryEntries<A extends MutationAttachmentRef = MutationAttachmentRef>(
  recovery: ReadonlyMap<string, MutationRecoveryRecord<A>>,
  ref: string,
): MutationRecoveryRecord<A>[] {
  return [...recovery.values()]
    .filter((record) => record.targetRef === ref)
    .sort((left, right) => left.intentSequence - right.intentSequence);
}

// `ref`'s outbox records still waiting on a blocked resend, oldest first.
export function blockedEntries<A extends MutationAttachmentRef = MutationAttachmentRef>(
  outbox: ReadonlyMap<string, MutationOutboxRecord<A>>,
  ref: string,
): MutationOutboxRecord<A>[] {
  return outboxEntriesByState(outbox, ref, "blockedUnknown");
}

// `ref`'s outbox records in the given durable state, oldest first - the one
// read the blocked-retry and stop-canceled lists share.
export function outboxEntriesByState<A extends MutationAttachmentRef = MutationAttachmentRef>(
  outbox: ReadonlyMap<string, MutationOutboxRecord<A>>,
  ref: string,
  state: MutationOutboxState,
): MutationOutboxRecord<A>[] {
  return [...outbox.values()]
    .filter((record) => record.targetRef === ref && record.state === state)
    .sort((left, right) => left.intentSequence - right.intentSequence);
}

// What a ref's pending-turns projection needs from the host's thread store:
// the current model, read fresh whenever `pendingTurnEntries` reconciles -
// never a snapshot handed in ahead of time, since a store instance outlives
// any one read of it.
export interface PendingTurnsThreadsPort {
  getThreadModel(ref: string): ThreadModel | undefined;
}

// What a submission's completion needs from the host's composer-draft
// storage (localStorage on the web): whether the draft has been edited since
// the submission started (`readDraftRevision`), its current content
// (`readComposerDraft`), and clearing it once confirmed unedited
// (`clearDraft`). No storage mechanism is named here.
export interface PendingTurnsDraftPort {
  readDraftRevision(ref: string): number;
  readComposerDraft(ref: string): { text: string; skillNames: readonly string[] };
  clearDraft(ref: string): void;
}

export interface PendingTurnsStoreDeps {
  threads: PendingTurnsThreadsPort;
  draft: PendingTurnsDraftPort;
  // The host's own ClientIdentity capability (createClientIdentity):
  // recordSubmittedHere and pendingTurnEntries both ask "is this record this
  // client's own", and reconcilePendingEntries's own isOwnMutationRecord
  // parameter is required, with no partial default - a caller with no
  // identity handy has to build one rather than this module silently
  // assuming "unattributed only" on its behalf.
  identity: Pick<ClientIdentity, "isOwnMutationRecord">;
}

export interface PendingTurnsState<A extends MutationAttachmentRef = MutationAttachmentRef> {
  outbox: Map<string, MutationOutboxRecord<A>>;
  optimistic: Map<string, MutationOptimisticRecord<A>>;
  recovery: Map<string, MutationRecoveryRecord<A>>;
  submittingRefs: ReadonlySet<string>;
  // Every client mutation id this store's own durable projection has held,
  // for as long as the store lives, mapped to that record's `createdAt`.
  // The durable records themselves are the primary evidence of "this client
  // submitted it", and they are deliberately short-lived: an authoritative
  // read settles every identity the daemon reports back out of storage.
  // Provenance has to outlive the record, because routing asks about it
  // after the hydrate too - see `reconcilePendingEntries`. The carried
  // `createdAt` is read there for steer-family entries - the one timestamp
  // the post-settle projection cannot re-derive, since the authoritative
  // entry carries none - and the pre-settle reload re-discovers it through
  // the same durable-read scan. Written at the same `recordSubmittedHere`
  // site, never pruned.
  submittedHere: ReadonlyMap<string, number>;
}

// The submitted values `settleSubmittedDraft` compares the current draft
// against: what the composer sent, and the draft revision counter it read
// just before starting - "unchanged" means neither a later edit (the
// revision moved) nor a different edit already restored (the text/selections
// no longer match) has happened since.
export interface SubmittedDraft {
  draftRevisionAtStart: number;
  text: string;
  skillNames: readonly string[];
}

export interface PendingTurnsStore<A extends MutationAttachmentRef = MutationAttachmentRef>
  extends FrameworkFreeStore<PendingTurnsState<A>> {
  // Scans a durable-read snapshot for this client's own outbox/optimistic
  // records and merges any newly-discovered clientMutationIds into
  // `submittedHere`. A no-op (no `setState`, `getState()` unchanged) when
  // every discovered id is already known.
  recordSubmittedHere(snapshot: { outbox: MutationOutboxRecord<A>[]; optimistic: MutationOptimisticRecord<A>[] }): void;
  // Marks `ref` as having a submission in flight. Returns false (and leaves
  // state alone) if one already is - the guard a caller turns into its own
  // rejection - otherwise marks it and returns true.
  beginSubmission(ref: string): boolean;
  // Releases `ref`'s in-flight marker. Safe to call whether or not
  // `beginSubmission` actually marked it.
  endSubmission(ref: string): void;
  // Decides whether the composer draft `submitted` describes should be
  // cleared now that its submission has committed, and clears it through the
  // draft port when so. `cleared` is that decision: unchanged from
  // `draftRevisionAtStart` (no edit landed while the submission was in
  // flight) and still textually and selection-identical to what was sent.
  // `draftUnchanged` is the narrower, revision-only half of that same check -
  // a caller that also needs "did the user touch the draft at all", such as a
  // recovery commit's own flag, reads it here rather than re-reading the
  // draft port a second time and risking the two answers disagreeing.
  settleSubmittedDraft(ref: string, submitted: SubmittedDraft): { cleared: boolean; draftUnchanged: boolean };
  // The pending-turn rows `ref` shows right now: this store's outbox and
  // optimistic records reconciled against the thread model the threads port
  // reads fresh, filtered to `method` when given.
  pendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[];
}

export function createPendingTurnsStore<A extends MutationAttachmentRef = MutationAttachmentRef>(
  deps: PendingTurnsStoreDeps,
): PendingTurnsStore<A> {
  // Mutated in place and returned as the same object rather than spread into
  // a copy (state/connection/core.ts's D28 round-1 fix and its own comment):
  // a spread would hand callers a different object than the one these
  // methods call `setState`/`getState` through, invisible to anything that
  // spies on or wraps it.
  const store = createFrameworkFreeStore<PendingTurnsState<A>>(() => ({
    outbox: new Map(),
    optimistic: new Map(),
    recovery: new Map(),
    submittingRefs: new Set(),
    submittedHere: new Map(),
  })) as PendingTurnsStore<A>;

  store.recordSubmittedHere = (snapshot) => {
    const known = store.getState().submittedHere;
    // The outbox is shared per origin: another client's records read back
    // out of it are visible here but are not this store's submissions, and
    // claiming them would reroute this store's own routing behind their
    // sends. The carried value is the record's createdAt - the one timestamp
    // the post-settle projection cannot re-derive (the authoritative entry
    // carries none).
    const discovered = [...snapshot.outbox, ...snapshot.optimistic]
      .filter((record) => deps.identity.isOwnMutationRecord(record))
      .map((record) => [record.clientMutationId, record.createdAt] as const)
      .filter(([id]) => !known.has(id));
    if (discovered.length === 0) return;
    store.setState((state) => ({ submittedHere: new Map([...state.submittedHere, ...discovered]) }));
  };

  store.beginSubmission = (ref) => {
    if (store.getState().submittingRefs.has(ref)) return false;
    store.setState((state) => ({ submittingRefs: new Set(state.submittingRefs).add(ref) }));
    return true;
  };

  store.endSubmission = (ref) => {
    if (!store.getState().submittingRefs.has(ref)) return;
    store.setState((state) => {
      const submittingRefs = new Set(state.submittingRefs);
      submittingRefs.delete(ref);
      return { submittingRefs };
    });
  };

  store.settleSubmittedDraft = (ref, submitted) => {
    const draftUnchanged = deps.draft.readDraftRevision(ref) === submitted.draftRevisionAtStart;
    if (!draftUnchanged) return { cleared: false, draftUnchanged };
    const draft = deps.draft.readComposerDraft(ref);
    const skillNames = [...submitted.skillNames];
    const selectionsUnchanged =
      draft.skillNames.length === skillNames.length && draft.skillNames.every((name, i) => name === skillNames[i]);
    if (draft.text !== submitted.text || !selectionsUnchanged) return { cleared: false, draftUnchanged };
    deps.draft.clearDraft(ref);
    return { cleared: true, draftUnchanged };
  };

  store.pendingTurnEntries = (ref, method) => {
    const state = store.getState();
    return reconcilePendingEntries(
      ref,
      [...state.outbox.values(), ...state.optimistic.values()],
      deps.threads.getThreadModel(ref),
      state.submittedHere,
      (record) => deps.identity.isOwnMutationRecord(record),
    ).filter((entry) => method === undefined || entry.method === method);
  };

  return store;
}
