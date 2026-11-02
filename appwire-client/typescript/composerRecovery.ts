import { type ComposerMention, canonicalSkillNames, type InputAttachment } from "./composerInput";
import type { MutationRecord } from "./state/mutation/records";
import { markerPattern, markerText } from "./textareaMarkers";
import type { InputItem } from "./types.gen";

export interface ComposerComposition<A extends { marker: number }> {
  text: string;
  attachments: A[];
  skillNames: string[];
  commandNames: string[];
  mentions?: ComposerMention[];
}

export interface RecoveredComposition<A extends { marker: number }> extends ComposerComposition<A> {
  imageReferences: "recorded" | "unavailable";
}

function recordInput(record: MutationRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function inputNames(input: readonly InputItem[], type: "skill" | "command"): string[] {
  return canonicalSkillNames(
    input.flatMap((item) => (item.type === type && typeof item.name === "string" ? [item.name] : [])),
  );
}

function inputText(input: readonly InputItem[]): { text: string; mentions?: ComposerMention[] } {
  const items = input.filter(
    (item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string",
  );
  const text = items.map((item) => item.text).join("\n");
  if (!items.some((item) => item.mentions !== undefined)) return { text };

  let itemOffset = 0;
  const mentions: ComposerMention[] = [];
  for (const [index, item] of items.entries()) {
    if (index > 0) itemOffset += 1;
    for (const mention of item.mentions ?? []) {
      if (mention.kind !== "skill" && mention.kind !== "command") continue;
      mentions.push({ kind: mention.kind, name: mention.name, offset: itemOffset + mention.offset });
    }
    itemOffset += item.text.length;
  }
  return { text, mentions };
}

export function recoverMutationComposition(record: MutationRecord): RecoveredComposition<InputAttachment> {
  const input = recordInput(record);
  const fallback = inputText(input);
  const images = input.filter(
    (item): item is InputItem & { data: string } => item.type === "image" && typeof item.data === "string",
  );
  const attachments = images.map((image, index): InputAttachment => {
    const metadata = record.attachments[index];
    return {
      marker: metadata?.marker ?? index + 1,
      mediaType: image.mediaType ?? metadata?.mediaType ?? "image/png",
      data: image.data,
      ...(image.name !== undefined || metadata?.name !== undefined ? { name: image.name ?? metadata?.name } : {}),
    };
  });
  const hasComposerSnapshot = record.composerText !== undefined;
  const mentions = hasComposerSnapshot ? record.composerMentions : fallback.mentions;

  return {
    text: record.composerText ?? fallback.text,
    attachments,
    skillNames: inputNames(input, "skill"),
    commandNames: inputNames(input, "command"),
    ...(mentions !== undefined ? { mentions: mentions.map((mention) => ({ ...mention })) } : {}),
    imageReferences: hasComposerSnapshot ? "recorded" : "unavailable",
  };
}

interface MarkerShift {
  offset: number;
  delta: number;
}

function markerNumbers(text: string): number[] {
  return Array.from(text.matchAll(markerPattern()), (match) => Number(match[1]));
}

function nextUnusedMarker(used: Set<number>): number {
  let marker = 1;
  while (used.has(marker)) marker += 1;
  used.add(marker);
  return marker;
}

function remapMarkers(text: string, mapping: ReadonlyMap<number, number>): { text: string; shifts: MarkerShift[] } {
  const shifts: MarkerShift[] = [];
  const remapped = text.replace(markerPattern(), (match, digits: string, offset: number) => {
    const marker = mapping.get(Number(digits));
    if (marker === undefined) return match;
    const replacement = markerText(marker);
    if (replacement.length !== match.length) shifts.push({ offset, delta: replacement.length - match.length });
    return replacement;
  });
  return { text: remapped, shifts };
}

function shiftedMentions(
  mentions: readonly ComposerMention[] | undefined,
  shifts: readonly MarkerShift[],
  appendOffset = 0,
): ComposerMention[] {
  return (mentions ?? []).map((mention) => ({
    ...mention,
    offset:
      mention.offset +
      appendOffset +
      shifts.filter((shift) => shift.offset < mention.offset).reduce((delta, shift) => delta + shift.delta, 0),
  }));
}

export function mergeComposerCompositions<A extends { marker: number }>(
  current: ComposerComposition<A>,
  recovered: RecoveredComposition<A>,
): ComposerComposition<A> {
  let currentText = current.text;
  let currentAttachments = [...current.attachments];
  let currentMentions = current.mentions?.map((mention) => ({ ...mention }));
  const recoveredAttachmentMarkers = new Set(recovered.attachments.map((attachment) => attachment.marker));
  const literalMarkers = new Set(
    markerNumbers(recovered.text).filter(
      (marker) => recovered.imageReferences === "unavailable" || !recoveredAttachmentMarkers.has(marker),
    ),
  );

  if (literalMarkers.size > 0) {
    const used = new Set([
      ...markerNumbers(currentText),
      ...currentAttachments.map((attachment) => attachment.marker),
      ...literalMarkers,
    ]);
    const currentMapping = new Map<number, number>();
    for (const attachment of currentAttachments) {
      if (literalMarkers.has(attachment.marker) && !currentMapping.has(attachment.marker)) {
        currentMapping.set(attachment.marker, nextUnusedMarker(used));
      }
    }
    if (currentMapping.size > 0) {
      const remapped = remapMarkers(currentText, currentMapping);
      currentText = remapped.text;
      currentMentions = shiftedMentions(currentMentions, remapped.shifts);
      currentAttachments = currentAttachments.map((attachment) => {
        const marker = currentMapping.get(attachment.marker);
        return marker === undefined ? attachment : { ...attachment, marker };
      });
    }
  }

  const used = new Set([
    ...markerNumbers(currentText),
    ...currentAttachments.map((attachment) => attachment.marker),
    ...literalMarkers,
  ]);

  const recoveredMapping = new Map<number, number>();
  const recoveredAttachments = recovered.attachments.map((attachment) => {
    let marker = recoveredMapping.get(attachment.marker);
    if (marker === undefined) {
      marker = nextUnusedMarker(used);
      recoveredMapping.set(attachment.marker, marker);
    }
    return { ...attachment, marker };
  });
  const remappedRecovered =
    recovered.imageReferences === "recorded"
      ? remapMarkers(recovered.text, recoveredMapping)
      : { text: recovered.text, shifts: [] };
  const appendOffset = currentText.length > 0 && remappedRecovered.text.length > 0 ? currentText.length + 2 : 0;
  const text =
    currentText.length > 0 && remappedRecovered.text.length > 0
      ? `${currentText}\n\n${remappedRecovered.text}`
      : currentText || remappedRecovered.text;
  const hasMentions = current.mentions !== undefined || recovered.mentions !== undefined;
  const mentions = hasMentions
    ? [...(currentMentions ?? []), ...shiftedMentions(recovered.mentions, remappedRecovered.shifts, appendOffset)]
    : undefined;

  return {
    text,
    attachments: [...currentAttachments, ...recoveredAttachments],
    skillNames: canonicalSkillNames([...current.skillNames, ...recovered.skillNames]),
    commandNames: canonicalSkillNames([...current.commandNames, ...recovered.commandNames]),
    ...(mentions !== undefined ? { mentions } : {}),
  };
}

export function nextComposerImageMarker(
  text: string,
  attachments: readonly { marker: number }[],
  highWaterMark = 0,
): number {
  const used = new Set([...markerNumbers(text), ...attachments.map((attachment) => attachment.marker)]);
  let marker = Math.max(highWaterMark, ...attachments.map((attachment) => attachment.marker), 0) + 1;
  while (used.has(marker)) marker += 1;
  return marker;
}
