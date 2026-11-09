import type { ComposerMention } from "@evener/appwire-client";
import { mergeComposerCompositions, recoverMutationComposition } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { PendingAttachment } from "../attachments/useAttachments";
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

// The shared recovery owns portable record decoding: composer text and marker
// metadata identify associations, while payload input owns image bytes. This
// browser wrapper converts those images to settled PendingAttachment tiles and
// keeps the browser's optional command-list shape.
export function recoveryComposerDraft(record: MutationRecoveryRecord): RecoveredComposerDraft {
  const recovered = recoverMutationComposition(record);
  const draft: RecoveredComposerDraft = {
    text: recovered.text,
    attachments: recovered.attachments.map((attachment) => ({
      ...attachment,
      name: attachment.name ?? "image",
      pending: false,
    })),
    skillNames: recovered.skillNames,
    ...(recovered.commandNames.length ? { commandNames: recovered.commandNames } : {}),
    ...(recovered.mentions ? { mentions: recovered.mentions } : {}),
  };
  if (recovered.imageReferences === "unavailable") unavailableImageReferences.add(draft);
  return draft;
}

// recoveredCompositionIsEmpty reports whether activating this draft would load
// nothing into the composer. A rejected drain with no input is the shape that
// matters: it is a notice (the queue strip shows the refusal and its reason),
// not a draft, and auto-activating it only flashes an empty composer before
// the persistence effect discards it again (lifecycle ruling 2026-11-09,
// issue #1436).
export function recoveredCompositionIsEmpty(draft: RecoveredComposerDraft): boolean {
  return (
    draft.text.trim() === "" &&
    draft.attachments.length === 0 &&
    draft.skillNames.length === 0 &&
    (draft.commandNames?.length ?? 0) === 0 &&
    (draft.mentions?.length ?? 0) === 0
  );
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
