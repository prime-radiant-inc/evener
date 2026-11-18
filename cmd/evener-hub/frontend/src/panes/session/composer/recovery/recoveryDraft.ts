import type { ComposerMention } from "@evener/appwire-client";
import { mergeComposerCompositions, recoverMutationComposition } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import { createSecureUUID } from "../../../../stores/secureUUID";
import type { PendingAttachment } from "../attachments/useAttachments";
import { readCompositionImageRecord } from "../draft";
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

// The shared recovery owns portable record decoding: composer text and marker
// metadata identify associations, while payload input owns image bytes. This
// browser wrapper converts those images to settled PendingAttachment tiles and
// keeps the browser's optional command-list shape.
export function recoveryComposerDraft(record: MutationRecoveryRecord): RecoveredComposerDraft {
  const recovered = recoverMutationComposition(record);
  // The record this row's composition was written with, read BEFORE the ids are
  // minted below so the facts can be attached to them as they are created.
  const storedTiles = readCompositionImageRecord(record.clientMutationId)?.tiles ?? [];
  const eligibilityById = new Map<string, boolean>();
  const draft: RecoveredComposerDraft = {
    text: recovered.text,
    attachments: recovered.attachments.map((attachment) => {
      // A fresh tile in a fresh composer, so a fresh identity. The merge may
      // renumber the marker, and the restored-image exemption is recorded
      // against this id when the row is activated.
      const id = createSecureUUID();
      // Pair the stored fact by MARKER, which is legitimate here and nowhere
      // else: the record and this attachment list are the same revision, so a
      // marker in one names the same image as that marker in the other. Once
      // this draft exists, only the id is consulted.
      const stored = storedTiles.find((tile) => tile.marker === attachment.marker);
      if (stored) eligibilityById.set(id, stored.eligible);
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
