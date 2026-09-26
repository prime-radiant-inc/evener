import { canonicalSkillNames } from "@evener/appwire-client";
import { type Node as ProseMirrorNode, Schema } from "prosemirror-model";

export interface SkillEditorValue {
  text: string;
  skillNames: string[];
}

/** Flat inline content: newlines are text, and skills occupy one document position. */
export const skillSchema = new Schema({
  nodes: {
    doc: { content: "inline*", whitespace: "pre" },
    text: { group: "inline" },
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
});

// A namespace or path continuation cannot turn a prefix into an active skill.
const tokenCharacter = /[\p{L}\p{N}_./:-]/u;

// Punctuation that ends a sentence rather than continuing a name: `/review.` and
// `/review:` are complete references, while `/plugin:hidden` still prefers the
// longer name because its colon is followed by a token character.
const sentenceTerminator = /[.:]/;

/** Whether a character continues a skill token rather than bounding one. */
export function isSkillTokenCharacter(character: string): boolean {
  return tokenCharacter.test(character);
}

/**
 * The name `text` spells as a complete reference at `offset`, if any. `names`
 * must be longest-first so a longer canonical name wins over its own prefix.
 * This is the single definition of "complete reference": the document parser
 * and the editor both read it, so an atom can never outlive the text rule.
 */
export function completeSkillReferenceAt(text: string, offset: number, names: readonly string[]): string | undefined {
  if (text[offset] !== "/" || (offset > 0 && tokenCharacter.test(text.charAt(offset - 1)))) return undefined;
  return names.find((candidate) => {
    const end = offset + candidate.length + 1;
    return (
      text.startsWith(`/${candidate}`, offset) &&
      (end === text.length ||
        !tokenCharacter.test(text.charAt(end)) ||
        (sentenceTerminator.test(text.charAt(end)) && !tokenCharacter.test(text.charAt(end + 1))))
    );
  });
}

/** Only complete visible mentions in skillNames become atoms; never append hidden selections. */
export function parseSkillDocument(value: SkillEditorValue): ProseMirrorNode {
  const names = canonicalSkillNames(value.skillNames).sort((a, b) => b.length - a.length);
  const nodes: ProseMirrorNode[] = [];
  let textStart = 0;
  for (let offset = 0; offset < value.text.length; offset++) {
    const name = completeSkillReferenceAt(value.text, offset, names);
    if (!name) continue;
    if (textStart < offset) nodes.push(skillSchema.text(value.text.slice(textStart, offset)));
    nodes.push(skillSchema.nodes.skill.create({ name }));
    offset += name.length;
    textStart = offset + 1;
  }
  if (textStart < value.text.length) nodes.push(skillSchema.text(value.text.slice(textStart)));
  return skillSchema.nodes.doc.create(null, nodes);
}

/** Serialize text and active metadata from the same document, in mention order. */
export function serializeSkillDocument(doc: ProseMirrorNode): SkillEditorValue {
  let text = "";
  const names: string[] = [];
  doc.forEach((node) => {
    if (node.isText) text += node.text;
    else {
      text += `/${node.attrs.name}`;
      names.push(node.attrs.name);
    }
  });
  return { text, skillNames: canonicalSkillNames(names) };
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
