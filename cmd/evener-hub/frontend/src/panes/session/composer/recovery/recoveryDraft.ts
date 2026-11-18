import type { ComposerMention } from "@evener/appwire-client";
import { mergeComposerCompositions, recoverMutationComposition } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import { createSecureUUID } from "../../../../stores/secureUUID";
import { arrayBufferToBase64 } from "../attachments/encodePng";
import type { PendingAttachment } from "../attachments/useAttachments";
import { type CompositionImageAnchor, readCompositionImageRecord } from "../draft";
import { parseSkillDocument, skillAtomMentions } from "../skillDocument";

export interface RecoveredComposerDraft {
  text: string;
  attachments: PendingAttachment[];
  // The record's canonical skill selections, deduplicated in catalog order.
  // A selection is never prose: the payload carried it as a {type: "skill",
  // name} input item, and restoring it puts the name back on the composer's
  // selection list rather than into the textarea.
  skillNames: string[];
  commandNames?: string[];
  mentions?: ComposerMention[];
}

const unavailableImageReferences = new WeakSet<RecoveredComposerDraft>();

// Markers the record named in its own attachment list, kept beside the draft
// rather than in it: they are provenance the record already stated, and adding
// a field to the draft's public shape would make every reader carry a fact
// only the adoption path needs. Same technique as the image-reference flag
// above - the wrapper owns facts about the decoded record by identity.
const declaredImageMarkers = new WeakMap<RecoveredComposerDraft, number[]>();

// What the composition record said about each tile THIS draft just minted.
//
// The record stores its facts against the attachment id that existed when the
// composition was written. Decoding a row mints a NEW id for every tile - it
// has to, the old id belongs to a dead session - so a reader that looks the
// stored id up after decode finds nothing, and the tile it describes silently
// loses its eligibility. That is the whole class: an identity assigned outside
// the write that recorded facts about it.
//
// So the association happens HERE, in the same loop that mints the id: the
// stored tile is matched to the decoded attachment by MARKER, which is sound
// only because both describe the same revision (the record and the row's
// attachment list were written together), and the resulting fact is keyed by
// the id this draft is about to use. Nothing downstream ever compares a
// number to decide eligibility.
const recoveredTileEligibility = new WeakMap<RecoveredComposerDraft, ReadonlyMap<string, boolean>>();

// The missing-image anchors the ROW's own record committed, carried with the
// draft they describe.
//
// The live copy of these facts is the localStorage composition record, and a
// merge writes its text and bytes to IndexedDB while writing that record to
// localStorage - two stores that cannot share a transaction. A browser closed
// between them, or a full quota, leaves a row whose text spells `[image 1]`
// with nothing to say that reference had no bytes, and a reopen ships the
// spelling as prose. The merge transaction writes these anchors into the row
// itself, so they arrive HERE with the text they belong to. Same technique as
// the eligibility map above: a fact the decoder resolved, owned by the draft
// that resolved it.
const durableCompositionAnchors = new WeakMap<RecoveredComposerDraft, CompositionImageAnchor[]>();

/** The anchors committed atomically with this row's text, if it carries any. */
export function rowCommittedAnchors(draft: RecoveredComposerDraft): CompositionImageAnchor[] {
  return durableCompositionAnchors.get(draft) ?? [];
}

/** Per-tile Send eligibility restored with this draft, keyed by its minted id. */
export function recoveryTileEligibility(draft: RecoveredComposerDraft): ReadonlyMap<string, boolean> {
  return recoveredTileEligibility.get(draft) ?? new Map();
}

/** The markers a record's own attachment list named, in declaration order. */
export function recoveryDeclaredMarkers(draft: RecoveredComposerDraft): number[] {
  return declaredImageMarkers.get(draft) ?? [];
}

/** Whether the record could not say which `[image N]` its bytes belonged to.
 *
 * An "unavailable" row carries bytes and a text that may spell their numbers as
 * literal prose, and there is no fact left that separates the two. Adoption must
 * then keep those bytes eligible rather than reading the prose as the answer:
 * the merge may renumber a restored image, and a prose match would attribute the
 * row's spelling to the tile's new number and drop its exemption.
 */
export function recoveryImageReferencesUnavailable(draft: RecoveredComposerDraft): boolean {
  return unavailableImageReferences.has(draft);
}

/** One image a row still OWNS whose reference its outgoing payload omitted.
 *
 * A recovery row carries two different things and they are not the same list:
 * the request it would send (`payload.input`, the sendable selection) and the
 * composition it owns (`attachments`, every tile it kept). When a person
 * deletes a reference and leaves the tile staged, the tile is excluded from the
 * request but still durable in `attachments`. Decoding from the payload alone
 * therefore hides it, and the next save - built from what is on screen - drops
 * its bytes for good.
 *
 * Supplied by the caller because reading a Blob is asynchronous and this
 * decoder is not: the caller has already awaited storage, so it resolves these
 * and hands them in. The facts travel with the bytes they describe.
 */
export interface RecoveredExcludedImage {
  marker: number;
  name?: string;
  mediaType?: string;
  data: string;
}

/** The attachments this row owns that its payload did not carry.
 *
 * Synchronous, so a caller can ask "is there anything to read?" without
 * awaiting. Reading a Blob is what costs a turn of the event loop, and a row
 * whose request carried everything it owns has nothing to read.
 */
export function excludedRecoveryAttachmentRefs(
  record: MutationRecoveryRecord,
): readonly MutationRecoveryRecord["attachments"][number][] {
  const carried = new Set(recoverMutationComposition(record).attachments.map((item) => item.marker));
  return record.attachments.filter((attachment) => !carried.has(attachment.marker));
}

/** The retained images this row owns that its payload did not carry.
 *
 * Reads the same durable record the decoder reads, at the same revision: the
 * payload's own images are marked by marker, and every other attachment the row
 * keeps is a tile its request deliberately excluded. Their bytes come from the
 * row, never from the wire.
 */
export async function excludedRecoveryImages(record: MutationRecoveryRecord): Promise<RecoveredExcludedImage[]> {
  return Promise.all(
    excludedRecoveryAttachmentRefs(record).map(async (attachment) => ({
      marker: attachment.marker,
      ...(attachment.name ? { name: attachment.name } : {}),
      mediaType: attachment.mediaType,
      data: arrayBufferToBase64(await attachment.blob.arrayBuffer()),
    })),
  );
}

// The shared recovery owns portable record decoding: composer text and marker
// metadata identify associations, while payload input owns image bytes. This
// browser wrapper converts those images to settled PendingAttachment tiles and
// keeps the browser's optional command-list shape.
export function recoveryComposerDraft(
  record: MutationRecoveryRecord,
  excluded: readonly RecoveredExcludedImage[] = [],
): RecoveredComposerDraft {
  const recovered = recoverMutationComposition(record);
  // The record this row's composition was written with, read BEFORE the ids are
  // minted below so the facts can be attached to them as they are created.
  //
  // A record whose revision does not match the row is NOT an answer about this
  // row: text and bytes are committed to IndexedDB, these facts to
  // localStorage, and a crash between the two leaves the pair disagreeing. The
  // only safe reading of a mismatch is the one that withholds permission.
  //
  // Withholding must be EXPLICIT. An empty fact list is not withholding: the
  // adoption below treats a restored tile whose marker the text does not spell
  // as exempt, which is the grant this change exists to stop. So a mismatch
  // marks every restored tile ineligible instead of falling back to no facts.
  //
  // A record from before this field existed has no stamp and is trusted, which
  // is what it did before the field existed. A record moved to a replacement
  // row by `moveCompositionImageRecord` also carries no stamp: it describes the
  // same composition under a different row id, and the replacement row's
  // revision counts something else.
  const storedRecord = readCompositionImageRecord(record.clientMutationId);
  const storedRevisionMatches =
    storedRecord !== undefined &&
    (storedRecord.revision === undefined || storedRecord.revision === (record.compositionRevision ?? 0));
  const storedTiles = storedRevisionMatches ? storedRecord.tiles : [];
  const withholdEligibility = storedRecord !== undefined && !storedRevisionMatches;
  // A tile the row's own request left out is never sendable on restore,
  // whichever record survived. Its reference is gone - that is why the request
  // excluded it - so reading a record that calls it eligible would undo the
  // person's deletion. This is a fact about the tile, taken from the row the
  // bytes came from.
  const excludedMarkers = new Set(excluded.map((image) => image.marker));
  const eligibilityById = new Map<string, boolean>();
  const draft: RecoveredComposerDraft = {
    text: recovered.text,
    attachments: [
      ...recovered.attachments,
      // The tiles this row kept and its request left out. They are part of the
      // composition, so they come back with it - visible, holding their bytes,
      // and NOT sendable: their reference is gone, which is why the request
      // excluded them, and a reopen must not undo that decision.
      ...excluded.map((image) => ({
        marker: image.marker,
        mediaType: image.mediaType ?? "image/png",
        data: image.data,
        name: image.name ?? "image",
      })),
    ].map((attachment) => {
      // A fresh tile in a fresh composer, so a fresh identity. The merge may
      // renumber the marker, and the restored-image exemption is recorded
      // against this id when the row is activated.
      const id = createSecureUUID();
      // Pair the stored fact by MARKER, which is legitimate here and nowhere
      // else: the record and this attachment list are the same revision, so a
      // marker in one names the same image as that marker in the other. Once
      // this draft exists, only the id is consulted.
      const stored = storedTiles.find((tile) => tile.marker === attachment.marker);
      // Withholding must be EXPLICIT: an absent fact is read by adoption as
      // permission, which is the grant that let a deleted reference be sent
      // again. So every reason to withhold writes `false` rather than omitting
      // the entry.
      if (excludedMarkers.has(attachment.marker)) eligibilityById.set(id, false);
      else if (stored) eligibilityById.set(id, stored.eligible);
      else if (withholdEligibility) eligibilityById.set(id, false);
      return {
        ...attachment,
        id,
        name: attachment.name ?? "image",
        pending: false,
      };
    }),
    skillNames: recovered.skillNames,
    ...(recovered.commandNames.length ? { commandNames: recovered.commandNames } : {}),
    ...(recovered.mentions ? { mentions: recovered.mentions } : {}),
  };
  recoveredTileEligibility.set(draft, eligibilityById);
  // The anchors this row's text carries, committed by the same transaction that
  // wrote that text. Present on any row a merge created; absent on rows written
  // before the field existed, where the localStorage record remains the answer.
  durableCompositionAnchors.set(draft, (record as { anchors?: CompositionImageAnchor[] }).anchors ?? []);
  if (recovered.imageReferences === "unavailable") unavailableImageReferences.add(draft);
  declaredImageMarkers.set(
    draft,
    record.attachments.map((attachment) => attachment.marker),
  );
  return draft;
}

// compositionIsEmpty reports whether a composition carries nothing the
// composer would show. Mount restoration and recovery persistence must agree
// on it: one decides whether a recovered record is a draft, the other decides
// whether an activated draft is an empty cleanup.
export function compositionIsEmpty(parts: {
  text: string;
  attachments: readonly unknown[];
  skillNames: readonly string[];
  commandNames?: readonly string[];
  mentions?: readonly unknown[];
}): boolean {
  return (
    parts.text.trim() === "" &&
    parts.attachments.length === 0 &&
    parts.skillNames.length === 0 &&
    (parts.commandNames?.length ?? 0) === 0 &&
    (parts.mentions?.length ?? 0) === 0
  );
}

// recoveredCompositionIsEmpty reports whether activating this draft would load
// nothing into the composer. A rejected drain with no input is the shape that
// matters: it is a notice (the pending stack shows the refusal and its reason),
// not a draft, and auto-activating it only flashes an empty composer before
// the persistence effect discards it again (lifecycle ruling 2026-11-09,
// issue #1436).
export function recoveredCompositionIsEmpty(draft: RecoveredComposerDraft): boolean {
  return compositionIsEmpty(draft);
}

export function mergeRecoveryComposerDraft(
  currentText: string,
  currentAttachments: PendingAttachment[],
  recovered: RecoveredComposerDraft,
  currentSkillNames: readonly string[] = [],
  currentCommandNames: readonly string[] = [],
  currentMentions?: readonly ComposerMention[],
): RecoveredComposerDraft {
  // skillDocument is browser-owned parsing state. The shared merge accepts
  // explicit UTF-16 locations, so this wrapper fills either missing side with
  // the same parser the mounted browser composer uses before delegating.
  let currentLocations = currentMentions;
  let recoveredLocations = recovered.mentions;
  if (currentMentions || recovered.mentions) {
    currentLocations =
      currentMentions ??
      skillAtomMentions(
        parseSkillDocument({
          text: currentText,
          skillNames: [...currentSkillNames],
          commandNames: [...currentCommandNames],
        }),
      );
    recoveredLocations = recovered.mentions ?? skillAtomMentions(parseSkillDocument(recovered));
  }

  const merged = mergeComposerCompositions(
    {
      text: currentText,
      attachments: currentAttachments,
      skillNames: [...currentSkillNames],
      commandNames: [...currentCommandNames],
      ...(currentLocations !== undefined ? { mentions: [...currentLocations] } : {}),
    },
    {
      text: recovered.text,
      attachments: recovered.attachments,
      skillNames: recovered.skillNames,
      commandNames: recovered.commandNames ?? [],
      ...(recoveredLocations !== undefined ? { mentions: [...recoveredLocations] } : {}),
      imageReferences: unavailableImageReferences.has(recovered) ? "unavailable" : "recorded",
    },
  );

  return {
    text: merged.text,
    attachments: merged.attachments,
    skillNames: merged.skillNames,
    ...(merged.commandNames.length ? { commandNames: merged.commandNames } : {}),
    ...(merged.mentions ? { mentions: merged.mentions } : {}),
  };
}
