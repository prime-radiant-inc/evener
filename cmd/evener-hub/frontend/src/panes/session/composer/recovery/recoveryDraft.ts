import type { InputItem } from "../../../../protocol/types.gen";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import { markerText } from "../attachments/textareaMarkers";
import type { PendingAttachment } from "../attachments/useAttachments";
import { addSkillSelection } from "../skillSelections";

export interface RecoveredComposerDraft {
  text: string;
  attachments: PendingAttachment[];
  // The record's canonical skill selections, deduplicated in catalog order.
  // A selection is never prose: the payload carried it as a {type: "skill",
  // name} input item, and restoring it puts the name back on the composer's
  // selection list rather than into the textarea.
  skillNames: string[];
}

function recordInput(record: MutationRecoveryRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function markerNumbers(text: string): number[] {
  return Array.from(text.matchAll(/\[image (\d+)\]/g), (match) => Number(match[1]));
}

function skillSelections(input: InputItem[]): string[] {
  return input
    .filter((item): item is InputItem & { name: string } => item.type === "skill" && typeof item.name === "string")
    .reduce((names: string[], item) => addSkillSelection(names, item.name), []);
}

// The record carries both halves of the pairing the composer needs: the text
// as it was typed (composerText, marker anchors intact - the payload's own
// text was translated to prose at the submit boundary) and, on each durable
// attachment, the marker it was staged under. Neither is inferred here. A
// record with no recorded marker predates that pairing, so its attachments
// fall back to their position in the payload, which is the order buildInput
// and durableAttachments both wrote them in.
export function recoveryComposerDraft(record: MutationRecoveryRecord): RecoveredComposerDraft {
  const input = recordInput(record);
  const text =
    record.composerText ??
    input
      .filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
      .map((item) => item.text)
      .join("\n");
  const images = input.filter(
    (item): item is InputItem & { data: string } => item.type === "image" && typeof item.data === "string",
  );
  const attachments = images.map((image, index): PendingAttachment => {
    const durableAttachment = record.attachments[index];
    return {
      marker: durableAttachment?.marker ?? index + 1,
      name: image.name ?? durableAttachment?.name ?? "image",
      mediaType: image.mediaType ?? durableAttachment?.mediaType ?? "image/png",
      data: image.data,
      pending: false,
    };
  });
  return { text, attachments, skillNames: skillSelections(input) };
}

export function mergeRecoveryComposerDraft(
  currentText: string,
  currentAttachments: PendingAttachment[],
  recovered: RecoveredComposerDraft,
  currentSkillNames: readonly string[] = [],
): RecoveredComposerDraft {
  const usedMarkers = new Set([
    ...markerNumbers(currentText),
    ...currentAttachments.map((attachment) => attachment.marker),
  ]);
  const markerMapping = new Map<number, number>();
  const attachments = recovered.attachments.map((attachment) => {
    let marker = 1;
    while (usedMarkers.has(marker)) marker += 1;
    usedMarkers.add(marker);
    markerMapping.set(attachment.marker, marker);
    return { ...attachment, marker };
  });
  const recoveredText = recovered.text.replace(/\[image (\d+)\]/g, (match, marker: string) => {
    const replacement = markerMapping.get(Number(marker));
    return replacement === undefined ? match : markerText(replacement);
  });
  const text = [currentText, recoveredText].filter((part) => part.length > 0).join("\n\n");
  // Selections union the same way the text does, current names first, so a
  // recovery edit never drops a skill the user already selected - and never
  // duplicates one the record carries too.
  const skillNames = recovered.skillNames.reduce(
    (names: string[], name) => addSkillSelection(names, name),
    [...currentSkillNames],
  );
  return {
    text,
    attachments: [...currentAttachments, ...attachments],
    skillNames,
  };
}
