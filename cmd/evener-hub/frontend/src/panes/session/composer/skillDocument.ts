import {
  type ComposerMention,
  canonicalSkillNames,
  completeSkillReferenceAt,
  markerPattern,
  reconcileComposerMentions,
} from "@evener/appwire-client";
import { type Mark, type Node as ProseMirrorNode, Schema } from "prosemirror-model";

export {
  completeSkillReferenceAt,
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

/** Text nodes for one run of prose, splitting out the occurrences the caller
 * adopted. Anything the adopted set does not name stays plain text. */
function textNodes(text: string, adopted: ReadonlySet<number>): ProseMirrorNode[] {
  if (text === "") return [];
  if (adopted.size === 0) return [skillSchema.text(text)];
  const nodes: ProseMirrorNode[] = [];
  let start = 0;
  for (const match of text.matchAll(markerPattern())) {
    const marker = Number(match[1]);
    if (!adopted.has(marker)) continue;
    const offset = match.index ?? 0;
    if (start < offset) nodes.push(skillSchema.text(text.slice(start, offset)));
    nodes.push(skillSchema.text(match[0], [stagedAnchorMark(marker)]));
    start = offset + match[0].length;
  }
  if (start < text.length) nodes.push(skillSchema.text(text.slice(start)));
  return nodes;
}

/** Only complete visible mentions in skillNames become atoms; never append hidden selections. */
export function parseSkillDocument(value: SkillEditorValue): ProseMirrorNode {
  const skills = canonicalSkillNames(value.skillNames);
  const commands = canonicalSkillNames(value.commandNames);
  const adopted = new Set(value.adoptedImageMarkers ?? []);
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
    nodes.push(...textNodes(value.text.slice(textStart, offset), adopted));
    const type = kind === "skill" ? skillSchema.nodes.skill : skillSchema.nodes.command;
    nodes.push(type.create({ name }));
    if (kind === "skill") usedSkills.add(name);
    offset += name.length;
    textStart = offset + 1;
  }
  nodes.push(...textNodes(value.text.slice(textStart), adopted));
  return skillSchema.nodes.doc.create(null, nodes);
}

/** Serialize text and active metadata from the same document, in mention order. */
export function serializeSkillDocument(doc: ProseMirrorNode): SkillEditorValue {
  let text = "";
  const names: string[] = [];
  const commands: string[] = [];
  const mentions: ComposerMention[] = [];
  const adopted = new Set<number>();
  doc.forEach((node) => {
    if (node.isText) {
      const run = node.text ?? "";
      text += run;
      // A mark proves provenance only for the occurrence it actually covers:
      // the exact literal, not an adjacent run of text that inherited it.
      const marked = new Set(
        node.marks.filter((mark) => mark.type.name === "stagedAnchor").map((mark) => Number(mark.attrs.marker)),
      );
      if (marked.size > 0) {
        for (const match of run.matchAll(markerPattern())) {
          const marker = Number(match[1]);
          if (marked.has(marker)) adopted.add(marker);
        }
      }
    } else {
      const kind = node.type.name === "command" ? "command" : "skill";
      mentions.push({ kind, name: node.attrs.name, offset: text.length });
      text += `/${node.attrs.name}`;
      (kind === "command" ? commands : names).push(node.attrs.name);
    }
  });
  const adoptedImageMarkers = [...adopted].sort((left, right) => left - right);
  const value = {
    text,
    skillNames: canonicalSkillNames(names),
    ...(adoptedImageMarkers.length ? { adoptedImageMarkers } : {}),
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
  if (!value.mentions) return serializeSkillDocument(parseSkillDocument({ ...value, text }));
  const mentions = reconcileComposerMentions(value.text, text, value.mentions);
  return serializeSkillDocument(parseSkillDocument({ ...value, text, mentions }));
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
