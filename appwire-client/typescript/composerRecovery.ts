import { type ComposerMention, canonicalSkillNames, type InputAttachment } from "./composerInput";
import type { MutationRecord } from "./state/mutation/records";
import {
  adoptedMarkerRanges,
  type ImageMarkerRange,
  markerNumbersOf,
  markerPattern,
  markerText,
  validMarkerRanges,
} from "./textareaMarkers";
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
  /**
   * Those same occurrences by EXACT UTF-16 position. A number alone cannot
   * tell an adopted "[image 1]" from an identical literal the person typed
   * beside it, so the repair offered on restore would delete the first
   * spelling. Absent on records written before ranges existed; a reader then
   * marks every occurrence of an adopted number, exactly as those builds did.
   */
  adoptedImageRanges?: readonly ImageMarkerRange[];
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
  const adoption = adoptedInRecord(record, hasComposerSnapshot, attachments);

  return {
    text: record.composerText ?? fallback.text,
    attachments,
    skillNames: inputNames(input, "skill"),
    commandNames: inputNames(input, "command"),
    ...(mentions !== undefined ? { mentions: mentions.map((mention) => ({ ...mention })) } : {}),
    ...(adoption.ranges.length > 0
      ? { adoptedImageMarkers: adoption.markers, adoptedImageRanges: adoption.ranges }
      : {}),
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
): { markers: number[]; ranges: ImageMarkerRange[] } {
  const adopted = record.adoptedImageMarkers;
  const storedRanges = record.adoptedImageRanges;
  if ((adopted === undefined || adopted.length === 0) && (storedRanges === undefined || storedRanges.length === 0)) {
    return { markers: [], ranges: [] };
  }
  // Without a composer snapshot the text is the wire's own translation, so no
  // occurrence in it is the anchor anybody staged.
  if (!hasComposerSnapshot) return { markers: [], ranges: [] };
  const text = record.composerText ?? "";
  const backed = new Set(attachments.map((attachment) => attachment.marker));
  // Ranges first: an occurrence a tile backs is not missing here, so its range
  // is dropped - and dropping ONE spelling's range must not fall back to
  // marking every occurrence of that number.
  // adoptedMarkerRanges already validates every range against `text`.
  const ranges = adoptedMarkerRanges(text, adopted, storedRanges).filter((range) => !backed.has(range.marker));
  return { markers: markerNumbersOf(ranges), ranges };
}

interface MarkerShift {
  offset: number;
  delta: number;
}

function markerNumbers(text: string): number[] {
  return Array.from(text.matchAll(markerPattern()), (match) => Number(match[1]));
}

/** Where `offset` lands after every shift that starts before it. A shift AT the
 * offset is excluded: it describes a literal rewritten from that point on, so
 * the offset already sits after the change. */
function foldedShift(offset: number, shifts: readonly MarkerShift[]): number {
  return shifts.filter((shift) => shift.offset < offset).reduce((delta, shift) => delta + shift.delta, 0);
}

function nextUnusedMarker(used: Set<number>): number {
  let marker = 1;
  while (used.has(marker)) marker += 1;
  used.add(marker);
  return marker;
}

function remapMarkers(text: string, mapping: ReadonlyMap<number, number>): { text: string; shifts: MarkerShift[] } {
  return remapOccurrences(text, (marker) => mapping.get(marker));
}

/** Rewrite marker occurrences one at a time, letting `choose` decide each one
 * from its number and its exact position. A shift is recorded only where a
 * replacement actually happened, at the offset it replaced, so mention and
 * adopted-range offsets move by the edits the text really took. */
function remapOccurrences(
  text: string,
  choose: (marker: number, start: number, end: number) => number | undefined,
): { text: string; shifts: MarkerShift[] } {
  const shifts: MarkerShift[] = [];
  let out = "";
  let last = 0;
  let changed = false;
  for (const match of text.matchAll(markerPattern())) {
    const marker = Number(match[1]);
    const start = match.index ?? 0;
    const end = start + match[0].length;
    const target = choose(marker, start, end);
    if (target === undefined) continue;
    const replacement = markerText(target);
    if (replacement === match[0]) continue;
    out += text.slice(last, start) + replacement;
    last = end;
    changed = true;
    if (replacement.length !== match[0].length)
      shifts.push({ offset: start, delta: replacement.length - match[0].length });
  }
  if (!changed) return { text, shifts: [] };
  return { text: out + text.slice(last), shifts };
}

/** The occurrences `ranges` locate, keyed by number and position, and the
 * numbers they locate at all. A number with a located occurrence has prose
 * beside it that a rename must leave alone; a number with no located
 * occurrence at all came from a caller that named no positions, and only then
 * does that number fall back to a number-wide rewrite. */
function locatedOccurrences(
  text: string,
  ranges: readonly ImageMarkerRange[],
): { keys: Set<string>; markers: Set<number> } {
  const exact = validMarkerRanges(text, ranges);
  return {
    keys: new Set(exact.map((range) => `${range.marker}:${range.start}:${range.end}`)),
    markers: new Set(markerNumbersOf(exact)),
  };
}

/** Rename the adopted occurrences only where `ranges` locates them.
 *
 * An adopted number can be spelled twice in one text: once by the anchor whose
 * bytes are gone, once by prose the person typed beside it. The provenance
 * record says WHICH spelling is the anchor, so a rename that keeps the number
 * meaningful moves that occurrence and leaves the person's text exactly as
 * they wrote it. A number-wide rewrite would silently change their prose and
 * shift every mention past it by the wrong amount.
 *
 * The located numbers distinguish the two caller generations: a number no
 * range locates has no position to respect (a record from before ranges
 * existed), and every occurrence of it is the anchor, as those builds read it. */
function remapAdoptedOccurrences(
  text: string,
  ranges: readonly ImageMarkerRange[],
  mapping: ReadonlyMap<number, number>,
): { text: string; shifts: MarkerShift[] } {
  if (mapping.size === 0) return { text, shifts: [] };
  const located = locatedOccurrences(text, ranges);
  return remapOccurrences(text, (marker, start, end) => {
    const target = mapping.get(marker);
    if (target === undefined) return undefined;
    if (located.markers.has(marker) && !located.keys.has(`${marker}:${start}:${end}`)) return undefined;
    return target;
  });
}

function shiftedMentions(
  mentions: readonly ComposerMention[] | undefined,
  shifts: readonly MarkerShift[],
  appendOffset = 0,
): ComposerMention[] {
  return (mentions ?? []).map((mention) => ({
    ...mention,
    offset: mention.offset + appendOffset + foldedShift(mention.offset, shifts),
  }));
}

/** An adopted range after `remapMarkers` rewrote the text it names.
 *
 * A rename only rewrites digits, so a same-width rename leaves every offset
 * alone; a width change contributes a shift at its own offset, folded in
 * exactly as a mention's offset is. The range's own marker is looked up in
 * `remapping`, and its end is recomputed from the new literal's length -
 * which is why a shift AT the range's start is excluded rather than added. */
function shiftedAdoptedRanges(
  before: string,
  after: string,
  ranges: readonly ImageMarkerRange[],
  remapping: ReadonlyMap<number, number>,
  shifts: readonly MarkerShift[],
): ImageMarkerRange[] {
  const valid = validMarkerRanges(before, ranges);
  if (valid.length === 0) return [];
  const shifted = valid.map((range) => {
    const marker = remapping.get(range.marker) ?? range.marker;
    const start = range.start + foldedShift(range.start, shifts);
    return { marker, start, end: start + markerText(marker).length };
  });
  return validMarkerRanges(after, shifted);
}

export function mergeComposerCompositions<A extends { marker: number }>(
  current: ComposerComposition<A>,
  recovered: RecoveredComposition<A>,
): ComposerComposition<A> {
  let currentText = current.text;
  let currentAttachments = [...current.attachments];
  let currentMentions = current.mentions?.map((mention) => ({ ...mention }));
  const adopted = new Set(current.adoptedImageMarkers ?? []);
  // This side's adopted occurrences by position, carried through every rewrite
  // below. A range the rewrites do not touch keeps its offset; a rename moves
  // it; an occurrence the rewrite does not spell in its new text is dropped
  // by validMarkerRanges.
  let currentAdoptedRanges = adoptedMarkerRanges(current.text, current.adoptedImageMarkers, current.adoptedImageRanges);
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
      const before = currentText;
      currentText = remapped.text;
      currentMentions = shiftedMentions(currentMentions, remapped.shifts);
      currentAdoptedRanges = shiftedAdoptedRanges(
        before,
        currentText,
        currentAdoptedRanges,
        new Map(),
        remapped.shifts,
      );
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
    // The ranges are already in the current text's coordinates, which is the
    // text the rename reads.
    const used = new Set([
      ...spelledHere,
      ...currentAttachments.map((attachment) => attachment.marker),
      ...literalMarkers,
    ]);
    const adoptedMapping = new Map<number, number>();
    for (const marker of adoptedCollisions) adoptedMapping.set(marker, nextUnusedMarker(used));
    // The rename may move only the occurrence the provenance located. A
    // literal the person typed at the same number is their prose: rewriting it
    // would change their text, and a shift the text did not take would move
    // every mention past it by a length nobody typed.
    const remapped = remapAdoptedOccurrences(currentText, currentAdoptedRanges, adoptedMapping);
    const before = currentText;
    currentText = remapped.text;
    currentMentions = shiftedMentions(currentMentions, remapped.shifts);
    currentAdoptedRanges = shiftedAdoptedRanges(
      before,
      currentText,
      currentAdoptedRanges,
      adoptedMapping,
      remapped.shifts,
    );
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
  // The recovered side renumbers through the same rule as the current side:
  // its own adoption may only move the occurrences its record located. A tile
  // marker in the same map is rewritten everywhere, because each of its
  // spellings is that tile's anchor; an adopted number with no located
  // occurrence at all (a record from before ranges existed) keeps the
  // number-wide reading, which is what those builds did.
  const recoveredRanges = adoptedMarkerRanges(
    recovered.text,
    recovered.adoptedImageMarkers,
    recovered.adoptedImageRanges,
  );
  const remappedRecovered =
    recovered.imageReferences === "recorded"
      ? remapAdoptedOccurrences(recovered.text, recoveredRanges, recoveredMapping)
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
  // The ranges, from the same two sides. The recovered side's are first
  // expressed in ITS OWN text (renumbered with its attachments and adoption),
  // then moved past the join - which is the only step that changes their
  // offsets without changing their text.
  const recoveredSpelledMarkers = new Set(recoveredAdopted);
  const recoveredOwnedRanges =
    recovered.imageReferences === "recorded"
      ? shiftedAdoptedRanges(
          recovered.text,
          remappedRecovered.text,
          recoveredRanges.filter((range) =>
            recoveredSpelledMarkers.has(recoveredMapping.get(range.marker) ?? range.marker),
          ),
          recoveredMapping,
          remappedRecovered.shifts,
        )
      : [];
  const adoptedRanges = validMarkerRanges(text, [
    ...currentAdoptedRanges.filter((range) => currentSpelled.has(range.marker)),
    ...recoveredOwnedRanges.map((range) => ({
      ...range,
      start: range.start + appendOffset,
      end: range.end + appendOffset,
    })),
  ]);
  return {
    text,
    attachments: [...currentAttachments, ...recoveredAttachments],
    skillNames: canonicalSkillNames([...current.skillNames, ...recovered.skillNames]),
    commandNames: canonicalSkillNames([...current.commandNames, ...recovered.commandNames]),
    ...(mentions !== undefined ? { mentions } : {}),
    ...(adoptedImageMarkers.length > 0
      ? { adoptedImageMarkers, ...(adoptedRanges.length > 0 ? { adoptedImageRanges: adoptedRanges } : {}) }
      : {}),
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
