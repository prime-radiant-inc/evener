import {
  adoptedMarkerRanges,
  type ComposerMention,
  canonicalSkillNames,
  completeSkillReferenceAt,
  type ImageMarkerRange,
  markerNumbersOf,
  reconcileComposerMentions,
  reconcileMarkerRanges,
  sharedPrefixLength,
  sharedSuffixLength,
  validMarkerRanges,
} from "@evener/appwire-client";
import { type Mark, type Node as ProseMirrorNode, Schema } from "prosemirror-model";

export {
  completeSkillReferenceAt,
  type ImageMarkerRange,
  isSkillTokenCharacter,
  sharedPrefixLength,
  sharedSuffixLength,
} from "@evener/appwire-client";

export interface SkillEditorValue {
  text: string;
  skillNames: string[];
  commandNames?: string[];
  mentions?: ComposerMention[];
  /** The "[image N]" occurrences in `text` whose image this value's writer
   * staged but whose bytes this composer does not hold. Carried as a document
   * mark (stagedAnchor) rather than a number list alone, so it lives in the
   * editor's own history: deleting the occurrence removes it, and an undo that
   * restores the occurrence restores the provenance with it. A marker the
   * person types is an unmarked text node and never enters this set. */
  adoptedImageMarkers?: number[];
  /** Those same occurrences by EXACT UTF-16 position. A number alone cannot
   * distinguish an adopted "[image 1]" from an identical literal beside it, so
   * the repair that offers to delete the missing reference would delete the
   * first spelling - the person's own prose. Derived from the marks on every
   * serialize, so it agrees with the document; `adoptedImageMarkers` stays as
   * the marker-number view of the same set for readers that only ask whether
   * any occurrence of a number is unbacked. */
  adoptedImageRanges?: ImageMarkerRange[];
}

/** Flat inline content: newlines are text, and skills occupy one document position. */
export const skillSchema = new Schema({
  nodes: {
    doc: { content: "inline*", whitespace: "pre" },
    text: { group: "inline" },
    command: {
      inline: true,
      group: "inline",
      atom: true,
      selectable: false,
      attrs: { name: {} },
      toDOM: (node) => [
        "span",
        { "data-command-name": node.attrs.name, contenteditable: "false" },
        `/${node.attrs.name}`,
      ],
      leafText: (node) => `/${node.attrs.name}`,
    },
    skill: {
      inline: true,
      group: "inline",
      atom: true,
      selectable: false,
      attrs: { name: {} },
      toDOM: (node) => [
        "span",
        { "data-skill-name": node.attrs.name, contenteditable: "false" },
        `/${node.attrs.name}`,
      ],
      leafText: (node) => `/${node.attrs.name}`,
    },
  },
  marks: {
    // An occurrence of a staged image marker this composer cannot back with
    // bytes. It is invisible (a bare span) and exists only so ProseMirror's
    // history carries the fact with the text it belongs to: a deletion takes
    // it away, an undo brings it back, and a retyped marker is a fresh,
    // unmarked node that sends as the literal prose it is. inclusive: false so
    // typing against the boundary never adopts the person's own keystrokes.
    stagedAnchor: {
      attrs: { marker: {} },
      inclusive: false,
      toDOM: () => ["span", 0],
    },
  },
});

function stagedAnchorMark(marker: number): Mark {
  return skillSchema.marks.stagedAnchor.create({ marker });
}

/** Text nodes for one run of prose, splitting out the exact occurrences the
 * caller adopted. `base` is the run's own UTF-16 offset in the whole
 * serialized text, so an adopted range names the occurrence in THIS run and
 * an identical literal beside it stays plain text. */
function textNodes(text: string, base: number, adopted: readonly ImageMarkerRange[]): ProseMirrorNode[] {
  if (text === "") return [];
  if (adopted.length === 0) return [skillSchema.text(text)];
  const end = base + text.length;
  const here = adopted.filter((range) => range.start >= base && range.end <= end);
  if (here.length === 0) return [skillSchema.text(text)];
  const nodes: ProseMirrorNode[] = [];
  let start = 0;
  for (const range of here) {
    const localStart = range.start - base;
    const localEnd = range.end - base;
    if (localStart < start) continue;
    if (start < localStart) nodes.push(skillSchema.text(text.slice(start, localStart)));
    nodes.push(skillSchema.text(text.slice(localStart, localEnd), [stagedAnchorMark(range.marker)]));
    start = localEnd;
  }
  if (start < text.length) nodes.push(skillSchema.text(text.slice(start)));
  return nodes;
}

/** Only complete visible mentions in skillNames become atoms; never append hidden selections. */
export function parseSkillDocument(value: SkillEditorValue): ProseMirrorNode {
  const skills = canonicalSkillNames(value.skillNames);
  const commands = canonicalSkillNames(value.commandNames);
  // A value carrying exact ranges names the occurrences it means; one from an
  // older build carries only numbers, and then every occurrence of a number is
  // adopted - the reading those builds implemented.
  const adopted = adoptedMarkerRanges(value.text, value.adoptedImageMarkers, value.adoptedImageRanges);
  const names = [...new Set([...skills, ...commands])].sort((a, b) => b.length - a.length);
  const usedSkills = new Set<string>();
  const nodes: ProseMirrorNode[] = [];
  let textStart = 0;
  for (let offset = 0; offset < value.text.length; offset++) {
    const name = completeSkillReferenceAt(value.text, offset, names);
    if (!name) continue;
    const mention = value.mentions?.find((item) => item.offset === offset && item.name === name);
    if (value.mentions && !mention) continue;
    const kind =
      mention?.kind ??
      (skills.includes(name) && (!commands.includes(name) || !usedSkills.has(name)) ? "skill" : "command");
    if (!(kind === "skill" ? skills : commands).includes(name)) continue;
    nodes.push(...textNodes(value.text.slice(textStart, offset), textStart, adopted));
    const type = kind === "skill" ? skillSchema.nodes.skill : skillSchema.nodes.command;
    nodes.push(type.create({ name }));
    if (kind === "skill") usedSkills.add(name);
    offset += name.length;
    textStart = offset + 1;
  }
  nodes.push(...textNodes(value.text.slice(textStart), textStart, adopted));
  return skillSchema.nodes.doc.create(null, nodes);
}

/** Serialize text and active metadata from the same document, in mention order. */
export function serializeSkillDocument(doc: ProseMirrorNode): SkillEditorValue {
  let text = "";
  const names: string[] = [];
  const commands: string[] = [];
  const mentions: ComposerMention[] = [];
  const adopted: ImageMarkerRange[] = [];
  doc.forEach((node) => {
    if (node.isText) {
      const run = node.text ?? "";
      // A mark proves provenance only for the occurrence it actually covers:
      // the exact literal, not an adjacent run of text that inherited it. The
      // node's own offset in the serialized text is what makes this an exact
      // range: two identical spellings are two nodes, and only the marked one
      // is adopted.
      for (const mark of node.marks) {
        if (mark.type.name !== "stagedAnchor") continue;
        adopted.push({ marker: Number(mark.attrs.marker), start: text.length, end: text.length + run.length });
      }
      text += run;
    } else {
      const kind = node.type.name === "command" ? "command" : "skill";
      mentions.push({ kind, name: node.attrs.name, offset: text.length });
      text += `/${node.attrs.name}`;
      (kind === "command" ? commands : names).push(node.attrs.name);
    }
  });
  // The marks are the truth, but only where the node really is one whole
  // marker literal: an integrity edit can split or extend a run after a mark
  // was set, and a range whose slice is not markerText(marker) names an
  // occurrence that no longer exists.
  const adoptedImageRanges = validMarkerRanges(text, adopted);
  const adoptedImageMarkers = markerNumbersOf(adoptedImageRanges);
  const value = {
    text,
    skillNames: canonicalSkillNames(names),
    ...(adoptedImageRanges.length ? { adoptedImageMarkers, adoptedImageRanges } : {}),
  };
  // Names alone suffice only when every matching reference is an actual skill
  // atom. Keep locations when commands or same-spelling prose need distinct
  // intent, including after deletion of the final command. The re-parse carries
  // the document's own adoption so a marked occurrence cannot read as prose
  // the parse would have re-derived differently.
  const needsMentions = commands.length > 0 || (value.skillNames.length > 0 && !parseSkillDocument(value).eq(doc));
  return {
    ...value,
    ...(commands.length ? { commandNames: canonicalSkillNames(commands) } : {}),
    ...(needsMentions ? { mentions } : {}),
  };
}

/** Shift existing atom identities across a plain-text splice without selecting new prose. */
export function patchSelectionText(value: SkillEditorValue, text: string): SkillEditorValue {
  // The adoption is re-derived from the marks, so a splice that removes or
  // moves an adopted occurrence has to move its range too: without this the
  // patched document would mark whatever now sits at the old offset, or mark
  // all of a number's occurrences when the stale range came up empty.
  const ranges = reconcileAdoptionAcrossSplice(value, text);
  const adopted: SkillEditorValue = {
    ...value,
    adoptedImageMarkers: markerNumbersOf(ranges),
    adoptedImageRanges: ranges,
  };
  if (!value.mentions) return serializeSkillDocument(parseSkillDocument({ ...adopted, text }));
  const mentions = reconcileComposerMentions(value.text, text, value.mentions);
  return serializeSkillDocument(parseSkillDocument({ ...adopted, text, mentions }));
}

/** The adoption a plain-text splice leaves behind, from the value's own pair. */
function reconcileAdoptionAcrossSplice(value: SkillEditorValue, text: string): ImageMarkerRange[] {
  // The ranges this value actually names in its own text: an explicit range
  // list when it has one, otherwise every occurrence of an adopted number.
  const current = adoptedMarkerRanges(value.text, value.adoptedImageMarkers, value.adoptedImageRanges);
  if (current.length === 0) return [];
  const prefix = sharedPrefixLength(value.text, text);
  const suffix = sharedSuffixLength(value.text, text, prefix);
  return reconcileMarkerRanges(value.text, text, current, prefix, suffix);
}

/**
 * The serialized text with every atom's label blanked to spaces, same length,
 * so offsets stay valid. A chip DISPLAYS `/name` but is not text the user
 * typed: scanning the raw serialization lets a chip at the end of the draft
 * read as a slash command someone just started.
 */
export function maskSkillAtoms(value: SkillEditorValue): string {
  let text = "";
  parseSkillDocument(value).forEach((node) => {
    text += node.isText ? node.text : " ".repeat(node.attrs.name.length + 1);
  });
  return text;
}

/**
 * The text with a canonical reference appended for every name it does not
 * already spell out. A queued entry can carry a selection with no prose to hang
 * it on, and the composer shows a selection as its own `/<name>` in the
 * sentence - so the reference is written out rather than the selection dropped.
 */
export function materializeSkillReferences(text: string, names: readonly string[]): string {
  const present = new Set(serializeSkillDocument(parseSkillDocument({ text, skillNames: [...names] })).skillNames);
  const missing = names.filter((name) => !present.has(name));
  if (missing.length === 0) return text;
  // The text is kept exactly as it is - only the join to the appended
  // references is decided here, so trailing whitespace the user typed survives.
  const joiner = text === "" || /\s$/.test(text) ? "" : " ";
  return `${text}${joiner}${missing.map((name) => `/${name}`).join(" ")}`;
}

/** A queue selection may lack a visible label, so restore one of its own kind. */
export function materializeSelectionReferences(value: SkillEditorValue): SkillEditorValue {
  let current = serializeSkillDocument(parseSkillDocument(value));
  for (const kind of ["skill", "command"] as const) {
    const wanted = kind === "skill" ? value.skillNames : value.commandNames;
    for (const name of canonicalSkillNames(wanted)) {
      const present = kind === "skill" ? current.skillNames : (current.commandNames ?? []);
      if (present.includes(name)) continue;
      const joiner = current.text === "" || /\s$/.test(current.text) ? "" : " ";
      const offset = current.text.length + joiner.length;
      const mentions = current.mentions ?? skillAtomMentions(parseSkillDocument(current));
      current = serializeSkillDocument(
        parseSkillDocument({
          text: `${current.text}${joiner}/${name}`,
          skillNames: kind === "skill" ? [...current.skillNames, name] : current.skillNames,
          commandNames: kind === "command" ? [...(current.commandNames ?? []), name] : current.commandNames,
          mentions: [...mentions, { kind, name, offset }],
          // Appending a reference must not retire the anchors this value
          // already carried as unbacked.
          ...(current.adoptedImageMarkers ? { adoptedImageMarkers: current.adoptedImageMarkers } : {}),
          ...(current.adoptedImageRanges ? { adoptedImageRanges: current.adoptedImageRanges } : {}),
        }),
      );
    }
  }
  return current;
}

export function skillAtomMentions(doc: ProseMirrorNode): ComposerMention[] {
  const mentions: ComposerMention[] = [];
  let offset = 0;
  doc.forEach((node) => {
    if (node.isText) offset += node.nodeSize;
    else {
      mentions.push({ kind: node.type.name === "command" ? "command" : "skill", name: node.attrs.name, offset });
      offset += node.attrs.name.length + 1;
    }
  });
  return mentions;
}

/** Map a UTF-16 serialized offset to a flat document position; bias snaps atom interiors. */
export function textOffsetToDocumentPosition(doc: ProseMirrorNode, offset: number, bias: -1 | 1 = -1): number {
  let textOffset = 0;
  let result = doc.content.size;
  doc.forEach((node, position) => {
    const length = node.isText ? node.nodeSize : node.attrs.name.length + 1;
    if (offset >= textOffset && offset <= textOffset + length) {
      if (node.isText) {
        result = position + offset - textOffset;
      } else {
        const afterAtom = offset !== textOffset && (offset === textOffset + length || bias === 1);
        result = position + (afterAtom ? 1 : 0);
      }
    }
    textOffset += length;
  });
  return offset < 0 ? 0 : result;
}

/** Map a flat document position to its UTF-16 offset in serialized text. */
export function documentPositionToTextOffset(doc: ProseMirrorNode, position: number): number {
  let offset = 0;
  doc.forEach((node, start) => {
    if (position <= start) return;
    offset += node.isText ? Math.min(position - start, node.nodeSize) : node.attrs.name.length + 1;
  });
  return offset;
}
