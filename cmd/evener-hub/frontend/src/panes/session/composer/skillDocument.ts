import { type ComposerMention, canonicalSkillNames } from "@evener/appwire-client";
import { type Node as ProseMirrorNode, Schema } from "prosemirror-model";

export interface SkillEditorValue {
  text: string;
  skillNames: string[];
  commandNames?: string[];
  mentions?: ComposerMention[];
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
  const skills = canonicalSkillNames(value.skillNames);
  const commands = canonicalSkillNames(value.commandNames);
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
    if (textStart < offset) nodes.push(skillSchema.text(value.text.slice(textStart, offset)));
    const type = kind === "skill" ? skillSchema.nodes.skill : skillSchema.nodes.command;
    nodes.push(type.create({ name }));
    if (kind === "skill") usedSkills.add(name);
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
  const commands: string[] = [];
  const mentions: ComposerMention[] = [];
  doc.forEach((node) => {
    if (node.isText) text += node.text;
    else {
      const kind = node.type.name === "command" ? "command" : "skill";
      mentions.push({ kind, name: node.attrs.name, offset: text.length });
      text += `/${node.attrs.name}`;
      (kind === "command" ? commands : names).push(node.attrs.name);
    }
  });
  const value = { text, skillNames: canonicalSkillNames(names) };
  // Names alone suffice only when every matching reference is an actual skill
  // atom. Keep locations when commands or same-spelling prose need distinct
  // intent, including after deletion of the final command.
  const needsMentions = commands.length > 0 || !parseSkillDocument(value).eq(doc);
  return {
    ...value,
    ...(commands.length ? { commandNames: canonicalSkillNames(commands) } : {}),
    ...(needsMentions ? { mentions } : {}),
  };
}

/** Shift existing atom identities across a plain-text splice without selecting new prose. */
export function patchSelectionText(value: SkillEditorValue, text: string): SkillEditorValue {
  if (!value.mentions) return serializeSkillDocument(parseSkillDocument({ ...value, text }));
  let prefix = 0;
  while (prefix < Math.min(value.text.length, text.length) && value.text[prefix] === text[prefix]) prefix++;
  let suffix = 0;
  while (
    suffix < Math.min(value.text.length, text.length) - prefix &&
    value.text[value.text.length - suffix - 1] === text[text.length - suffix - 1]
  )
    suffix++;
  const oldEnd = value.text.length - suffix;
  const delta = text.length - value.text.length;
  const mentions = value.mentions.flatMap((item) =>
    item.offset + item.name.length + 1 <= prefix
      ? [item]
      : item.offset >= oldEnd
        ? [{ ...item, offset: item.offset + delta }]
        : [],
  );
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
