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
  MutationRecoveryRecord,
} from "./records";

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
  // The host's own ClientIdentity capability (createClientIdentity), not a
  // package default: recordSubmittedHere and pendingTurnEntries both ask "is
  // this record this client's own", and a caller with no identity handy
  // should build one rather than this module silently assuming "unattributed
  // only" the way reconcilePendingEntries's own default does.
  identity: Pick<ClientIdentity, "isOwnMutationRecord">;
}

export interface PendingTurnsState<A extends MutationAttachmentRef = MutationAttachmentRef> {
  outbox: Map<string, MutationOutboxRecord<A>>;
  optimistic: Map<string, MutationOptimisticRecord<A>>;
  recovery: Map<string, MutationRecoveryRecord<A>>;
  submittingRefs: ReadonlySet<string>;
  // Every client mutation id this store's own durable projection has held,
  // for as long as the store lives. The durable records themselves are the
  // primary evidence of "this client submitted it", and they are
  // deliberately short-lived: an authoritative read settles every identity
  // the daemon reports back out of storage. Provenance has to outlive the
  // record, because routing asks about it after the hydrate too - see
  // `reconcilePendingEntries`. Ids only, never pruned.
  submittedHere: ReadonlySet<string>;
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
  // draft port when so. Returns whether it cleared: unchanged from
  // `draftRevisionAtStart` (no edit landed while the submission was in
  // flight) and still textually and selection-identical to what was sent.
  settleSubmittedDraft(ref: string, submitted: SubmittedDraft): boolean;
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
    submittedHere: new Set(),
  })) as PendingTurnsStore<A>;

  store.recordSubmittedHere = (snapshot) => {
    const known = store.getState().submittedHere;
    // The outbox is shared per origin: another client's records read back
    // out of it are visible here but are not this store's submissions, and
    // claiming them would reroute this store's own routing behind their
    // sends.
    const discovered = [...snapshot.outbox, ...snapshot.optimistic]
      .filter((record) => deps.identity.isOwnMutationRecord(record))
      .map((record) => record.clientMutationId)
      .filter((id) => !known.has(id));
    if (discovered.length === 0) return;
    store.setState((state) => ({ submittedHere: new Set([...state.submittedHere, ...discovered]) }));
  };

  store.beginSubmission = (ref) => {
    if (store.getState().submittingRefs.has(ref)) return false;
    store.setState((state) => ({ submittingRefs: new Set(state.submittingRefs).add(ref) }));
    return true;
  };

  store.endSubmission = (ref) => {
    store.setState((state) => {
      const submittingRefs = new Set(state.submittingRefs);
      submittingRefs.delete(ref);
      return { submittingRefs };
    });
  };

  store.settleSubmittedDraft = (ref, submitted) => {
    if (deps.draft.readDraftRevision(ref) !== submitted.draftRevisionAtStart) return false;
    const draft = deps.draft.readComposerDraft(ref);
    const skillNames = [...submitted.skillNames];
    const selectionsUnchanged =
      draft.skillNames.length === skillNames.length && draft.skillNames.every((name, i) => name === skillNames[i]);
    if (draft.text !== submitted.text || !selectionsUnchanged) return false;
    deps.draft.clearDraft(ref);
    return true;
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
