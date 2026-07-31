import type { InputItem } from "../../../../protocol/types.gen";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import { markerText } from "../attachments/textareaMarkers";
import type { PendingAttachment } from "../attachments/useAttachments";

export interface RecoveredComposerDraft {
  text: string;
  attachments: PendingAttachment[];
}

function recordInput(record: MutationRecoveryRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function markerNumbers(text: string): number[] {
  return Array.from(text.matchAll(/\[image (\d+)\]/g), (match) => Number(match[1]));
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
  return { text, attachments };
}

export function mergeRecoveryComposerDraft(
  currentText: string,
  currentAttachments: PendingAttachment[],
  recovered: RecoveredComposerDraft,
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
  return {
    text,
    attachments: [...currentAttachments, ...attachments],
  };
}
