// The checkpointed draft editor's primitives that generalize across every
// settings-hub store, verified against both keybindingsStore.ts here and PR
// #1549's transcriptDisplayStore.ts as the design oracle for a second store
// not yet built. discardDraft's whole body - the gate, the discardClassified
// call, the restoreDraft-on-refusal adoption, and the final published fields
// - is byte-identical between the two once each store's own message text and
// restoreDraft are pulled out as parameters; so is persistDraft's own
// mint-id/save/catch shape.
//
// editDraft, saveDraft, rebaseDraft and their settle machinery stay
// store-owned: transcript's draft carries a `layout` a single-payload store
// has no equivalent of (one draft field can propose either layout, so
// "does this edit continue the existing draft" is a per-layout identity
// check keybindings never needs), and its checkpoint/proposal shapes differ
// in ways a Payload type parameter would have to reconcile - the same class
// of decision piece 8 deferred for setSupport, not one to guess at here.

import type { DraftRepository } from "./draftCheckpointPort";

export interface DraftDiscardableFields {
  saving: boolean;
  writeUncertain: boolean;
  storageUnavailable: boolean;
  draftUnreadable: boolean;
}

/** discardDraft's own gate, narrower than a store's assertEditable: discarding
 * needs no confirmed hub state to compose against, so hubSupport/loaded and
 * similar payload-confirmation fields never block it. An unreadable record is
 * the one storage failure discarding can FIX, so it is not a reason to
 * refuse either - throwing the record away is exactly what the user is
 * asking for. */
export function assertDraftDiscardable<Fields extends DraftDiscardableFields>(
  fence: { disposed: boolean },
  getState: () => Fields,
  unavailableMessage: string,
): void {
  const state = getState();
  if (fence.disposed || state.saving || state.writeUncertain || (state.storageUnavailable && !state.draftUnreadable))
    throw new Error(unavailableMessage);
}

export interface PersistedDraftFields {
  storageUnavailable: boolean;
  draftError: string | null;
}

/** Persists a freshly composed checkpoint (a fresh id from the port, then a
 * save through it) before the intent it durably records ever leaves the
 * device. A throw is the port's own failure, in the port's fixed words: a
 * raw storage error may carry a local path, so the host shows this and never
 * the cause; marks BOTH storageUnavailable (the flag refreshOverrides' one-
 * more-restore-attempt and the native domain projection key on) and
 * draftError (the message a host renders) - a caller that only threw would
 * leave draftError describing whatever came before this failure, or nothing
 * at all. save()'s own refusal (another writer replaced the classified
 * record since - see createDraftRepository) is a conflict, not a storage
 * exception: adopts whatever restoreDraft finds on disk, the same posture
 * discardCheckpointedDraft takes on its own refusal, instead of reporting
 * storageUnavailable and leaving the stale classification in place. */
export function persistCheckpointedDraft<Fields extends PersistedDraftFields, Checkpoint extends { id: string }>(
  drafts: Pick<DraftRepository<Checkpoint>, "createId" | "save">,
  input: Omit<Checkpoint, "id">,
  getState: () => Fields,
  setState: (partial: Partial<Fields>) => void,
  restoreDraft: (state: Fields) => Partial<Fields>,
  draftSaveFailedMessage: string,
  draftReviewAgainMessage: string,
): Checkpoint {
  let checkpoint: Checkpoint;
  let saved: boolean;
  try {
    // createId() is this build's own local-write step, not the hub's - a
    // failure minting one (a crypto/random-source failure, say) is exactly
    // as much a local-write failure as save() throwing, and must not
    // escape uncaught with the store never told a write was attempted.
    checkpoint = { ...input, id: drafts.createId() } as Checkpoint;
    saved = drafts.save(checkpoint);
  } catch {
    setState({ storageUnavailable: true, draftError: draftSaveFailedMessage } as Partial<Fields>);
    throw new Error(draftSaveFailedMessage);
  }
  if (!saved) {
    setState(restoreDraft(getState()));
    throw new Error(draftReviewAgainMessage);
  }
  return checkpoint;
}

export interface DiscardedDraftFields {
  draft: unknown;
  draftConflict: boolean;
  draftError: string | null;
  storageUnavailable: boolean;
  draftUnreadable: boolean;
}

/** Throwing a proposal away composes nothing and sends nothing, so the
 * caller's own assertDraftDiscardable is its only gate - this runs the
 * discard itself. One shape whether the record is readable or not: remove
 * what was classified, and check. A refusal (another writer replaced the
 * SAME record while this call was deciding) adopts whatever `restoreDraft`
 * finds on disk instead of assuming success. */
export function discardCheckpointedDraft<Fields extends DiscardedDraftFields, Checkpoint>(
  drafts: Pick<DraftRepository<Checkpoint>, "discardClassified">,
  getState: () => Fields,
  setState: (partial: Partial<Fields>) => void,
  restoreDraft: (state: Fields) => Partial<Fields>,
  draftDiscardFailedMessage: string,
): void {
  let removed: boolean;
  try {
    removed = drafts.discardClassified();
  } catch {
    setState({ storageUnavailable: true, draftError: draftDiscardFailedMessage } as Partial<Fields>);
    throw new Error(draftDiscardFailedMessage);
  }
  if (!removed) {
    // The record this store classified is gone, replaced by something else
    // (another writer, another window): re-read what is actually there now
    // rather than assume success, so a newer checkpoint surfaces instead of
    // staying reported as discarded.
    setState(restoreDraft(getState()));
    return;
  }
  setState({
    draft: null,
    draftConflict: false,
    draftError: null,
    storageUnavailable: false,
    draftUnreadable: false,
  } as Partial<Fields>);
}
