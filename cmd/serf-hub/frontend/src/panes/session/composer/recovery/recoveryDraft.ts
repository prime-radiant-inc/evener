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

export function recoveryComposerDraft(record: MutationRecoveryRecord): RecoveredComposerDraft {
  const input = recordInput(record);
  const text = input
    .filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n");
  const markers = markerNumbers(text);
  const images = input.filter(
    (item): item is InputItem & { data: string } => item.type === "image" && typeof item.data === "string",
  );
  const attachments = images.map((image, index): PendingAttachment => {
    const durableAttachment = record.attachments[index];
    return {
      marker: markers[index] ?? index + 1,
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
