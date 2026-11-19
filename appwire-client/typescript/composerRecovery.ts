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
  /**
   * Markers whose occurrences in THIS text came from another writer's draft
   * rather than from this composition's own staging. They belong to no
   * attachment here: the bytes never travelled with the text, so a tile at the
   * same number is a different image (see the web composer's
   * unbackedImageMarkersIn). A merge must leave them alone when it detaches a
   * tile from a number an incoming literal also spells, or the renumber would
   * rewrite the adopted prose into a reference carrying somebody else's bytes.
   */
  adoptedImageMarkers?: readonly number[];
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
  const adoptedImageMarkers = adoptedInRecord(record, hasComposerSnapshot, attachments);

  return {
    text: record.composerText ?? fallback.text,
    attachments,
    skillNames: inputNames(input, "skill"),
    commandNames: inputNames(input, "command"),
    ...(mentions !== undefined ? { mentions: mentions.map((mention) => ({ ...mention })) } : {}),
    ...(adoptedImageMarkers.length > 0 ? { adoptedImageMarkers } : {}),
    imageReferences: hasComposerSnapshot ? "recorded" : "unavailable",
  };
}

/** The adopted anchors this record's composition carries.
 *
 * The durable provenance is the one fact that survives a reload; a record an
 * older build wrote has none, and its markers are prose (the same rule the
 * draft's own stagedImageMarkers follows). Intersected with what the text
 * still spells and with the attachments that did NOT travel: a marker this
 * record holds bytes for is backed here, so the reader has no missing
 * reference to refuse - which is also what keeps a legitimate recovered tile
 * sendable. */
function adoptedInRecord(
  record: MutationRecord,
  hasComposerSnapshot: boolean,
  attachments: readonly InputAttachment[],
): number[] {
  const adopted = record.adoptedImageMarkers;
  if (adopted === undefined || adopted.length === 0) return [];
  // Without a composer snapshot the text is the wire's own translation, so no
  // occurrence in it is the anchor anybody staged.
  if (!hasComposerSnapshot) return [];
  const backed = new Set(attachments.map((attachment) => attachment.marker));
  const spelled = new Set(markerNumbers(record.composerText ?? ""));
  return [...new Set(adopted)]
    .filter((marker) => spelled.has(marker) && !backed.has(marker))
    .sort((left, right) => left - right);
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
  const adopted = new Set(current.adoptedImageMarkers ?? []);
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
      // Only an occurrence this composition's own staging wrote belongs to the
      // tile being detached. An adopted occurrence at the same number is
      // another writer's prose: leaving it spelled keeps the send-time check
      // refusing THAT occurrence, and keeps an incoming literal's repair from
      // deleting the wrong text. The tile still moves, so an incoming literal
      // can never be paired with this tile's bytes.
      const ownedMapping = new Map([...currentMapping].filter(([marker]) => !adopted.has(marker)));
      const remapped = remapMarkers(currentText, ownedMapping);
      currentText = remapped.text;
      currentMentions = shiftedMentions(currentMentions, remapped.shifts);
      currentAttachments = currentAttachments.map((attachment) => {
        const marker = currentMapping.get(attachment.marker);
        return marker === undefined ? attachment : { ...attachment, marker };
      });
    }
  }

  // An adopted occurrence at a number an incoming literal also spells is the
  // one case a number cannot settle: the text would hold two occurrences of
  // "[image N]" with different owners, and the repair that removes the missing
  // one would find whichever came first. The adopted occurrence has no bytes
  // anywhere, so its number carries no meaning worth keeping - move it to a
  // free one. The refusal follows it there, and the incoming prose keeps the
  // number it actually names.
  const spelledHere = new Set(markerNumbers(currentText));
  const adoptedCollisions = new Set(
    [...adopted].filter((marker) => literalMarkers.has(marker) && spelledHere.has(marker)),
  );
  let adoptedAfterMerge = new Set(adopted);
  if (adoptedCollisions.size > 0) {
    const used = new Set([
      ...spelledHere,
      ...currentAttachments.map((attachment) => attachment.marker),
      ...literalMarkers,
    ]);
    const adoptedMapping = new Map<number, number>();
    for (const marker of adoptedCollisions) adoptedMapping.set(marker, nextUnusedMarker(used));
    const remapped = remapMarkers(currentText, adoptedMapping);
    currentText = remapped.text;
    currentMentions = shiftedMentions(currentMentions, remapped.shifts);
    adoptedAfterMerge = new Set([...adopted].map((marker) => adoptedMapping.get(marker) ?? marker));
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
  // The recovered side's OWN adoption travels with its own text: those
  // occurrences are ones that side's writer could not back, so they take the
  // same renumbering its attachments took. A number this side already spells
  // - in its text or as a tile's marker - belongs to this side's occurrence,
  // so the recovered one moves off it, the mirror of the current side's rule.
  const recoveredAdopted: number[] = [];
  if (recovered.imageReferences === "recorded") {
    const recoveredSpelled = new Set(markerNumbers(recovered.text));
    const currentUsed = new Set([
      ...markerNumbers(currentText),
      ...currentAttachments.map((attachment) => attachment.marker),
    ]);
    for (const marker of new Set(recovered.adoptedImageMarkers ?? [])) {
      if (!recoveredSpelled.has(marker)) continue;
      // Its bytes travelled with this record, so nothing is missing here.
      if (recoveredMapping.has(marker)) continue;
      const target = currentUsed.has(marker) ? nextUnusedMarker(used) : marker;
      used.add(target);
      recoveredMapping.set(marker, target);
      recoveredAdopted.push(target);
    }
  }
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

  // Adoption is scoped to the side that supplied its occurrence. A dormant
  // record on this side - an anchor the person removed while the record kept
  // its provenance for an undo - names an occurrence THIS text no longer has,
  // so it must never latch onto one the recovered message brought: that prose
  // is somebody else's, and the repair that deletes the "missing" reference
  // would delete the queued text instead. Keeping each side's record to its
  // own occurrences is what makes the refusal follow the anchor rather than
  // the number.
  const currentSpelled = new Set(markerNumbers(currentText));
  const mergedAdopted = [...[...adoptedAfterMerge].filter((marker) => currentSpelled.has(marker)), ...recoveredAdopted];
  const adoptedImageMarkers = [...new Set(mergedAdopted)].sort((left, right) => left - right);
  return {
    text,
    attachments: [...currentAttachments, ...recoveredAttachments],
    skillNames: canonicalSkillNames([...current.skillNames, ...recovered.skillNames]),
    commandNames: canonicalSkillNames([...current.commandNames, ...recovered.commandNames]),
    ...(mentions !== undefined ? { mentions } : {}),
    ...(adoptedImageMarkers.length > 0 ? { adoptedImageMarkers } : {}),
  };
}

export function nextComposerImageMarker(
  text: string,
  attachments: readonly { marker: number }[],
  highWaterMark = 0,
  reservedMarkers: readonly number[] = [],
): number {
  // A reserved number is one a staged image may not take: the draft's own
  // dormant adoption record still names it, so a new tile there would be
  // refused as the missing image it is not. Reservations are skipped, never
  // claimed.
  const used = new Set([
    ...markerNumbers(text),
    ...attachments.map((attachment) => attachment.marker),
    ...reservedMarkers,
  ]);
  let marker = Math.max(highWaterMark, ...attachments.map((attachment) => attachment.marker), 0) + 1;
  while (used.has(marker)) marker += 1;
  return marker;
}
