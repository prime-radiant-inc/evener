import { type Node as ProseMirrorNode, Schema } from "prosemirror-model";
import { canonicalSkillNames } from "../../../../../../../appwire-client/typescript/composerInput";

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

/** Only complete visible mentions in skillNames become atoms; never append hidden selections. */
export function parseSkillDocument(value: SkillEditorValue): ProseMirrorNode {
  const names = canonicalSkillNames(value.skillNames).sort((a, b) => b.length - a.length);
  const nodes: ProseMirrorNode[] = [];
  let textStart = 0;
  for (let offset = 0; offset < value.text.length; offset++) {
    if (value.text[offset] !== "/" || (offset > 0 && tokenCharacter.test(value.text.charAt(offset - 1)))) continue;
    const name = names.find((candidate) => {
      const end = offset + candidate.length + 1;
      return (
        value.text.startsWith(`/${candidate}`, offset) &&
        (end === value.text.length || !tokenCharacter.test(value.text.charAt(end)))
      );
    });
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

/** Map a UTF-16 serialized offset to a flat document position; bias snaps atom interiors. */
export function textOffsetToDocumentPosition(doc: ProseMirrorNode, offset: number, bias: -1 | 1 = -1): number {
  let textOffset = 0;
  let result = doc.content.size;
  doc.forEach((node, position) => {
    const length = node.isText ? node.nodeSize : node.attrs.name.length + 1;
    if (offset >= textOffset && offset <= textOffset + length) {
      result = node.isText
        ? position + offset - textOffset
        : position + (offset === textOffset ? 0 : offset === textOffset + length || bias === 1 ? 1 : 0);
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
