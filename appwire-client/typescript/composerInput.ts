import { translateAttachmentMarkers } from "./attachmentMarkers";
import type { InputItem } from "./types.gen";

/** Editing-only identity and UTF-16 position on a text item, never activation authority. */
export interface ComposerMention {
  kind: "skill" | "command";
  name: string;
  offset: number;
}

/** Length of the leading UTF-16 run two serialized values share. */
export function sharedPrefixLength(before: string, after: string): number {
  const limit = Math.min(before.length, after.length);
  let index = 0;
  while (index < limit && before[index] === after[index]) index++;
  return index;
}

/** Length of the trailing UTF-16 run two values share, past `prefix`. */
export function sharedSuffixLength(before: string, after: string, prefix: number): number {
  const limit = Math.min(before.length, after.length) - prefix;
  let count = 0;
  while (count < limit && before[before.length - 1 - count] === after[after.length - 1 - count]) count++;
  return count;
}

const skillTokenCharacter = /[\p{L}\p{N}_./:-]/u;
const skillSentenceTerminator = /[.:]/;

/** Whether a character continues a skill reference rather than bounding one. */
export function isSkillTokenCharacter(character: string): boolean {
  return skillTokenCharacter.test(character);
}

/** Return the longest-first name spelled as a complete reference at this offset. */
export function completeSkillReferenceAt(text: string, offset: number, names: readonly string[]): string | undefined {
  if (text[offset] !== "/" || (offset > 0 && skillTokenCharacter.test(text.charAt(offset - 1)))) return undefined;
  return names.find((candidate) => {
    const end = offset + candidate.length + 1;
    return (
      text.startsWith(`/${candidate}`, offset) &&
      (end === text.length ||
        !skillTokenCharacter.test(text.charAt(end)) ||
        (skillSentenceTerminator.test(text.charAt(end)) && !skillTokenCharacter.test(text.charAt(end + 1))))
    );
  });
}

function hasCompleteComposerReference(text: string, mention: ComposerMention): boolean {
  const end = mention.offset + mention.name.length + 1;
  return (
    mention.offset >= 0 &&
    completeSkillReferenceAt(text, mention.offset, [mention.name]) === mention.name &&
    end <= text.length
  );
}

/** Shift existing identities across a text splice, dropping edited labels, never selecting new prose. */
export function reconcileComposerMentions(
  before: string,
  after: string,
  mentions: readonly ComposerMention[],
): ComposerMention[] {
  // A whole-draft prepend can share its leading slash with the old first label.
  // Prefer append when both interpretations fit, retaining the original identities.
  const prepended = !after.startsWith(before) && after.endsWith(before);
  const prefix = prepended ? 0 : sharedPrefixLength(before, after);
  const suffix = sharedSuffixLength(before, after, prefix);
  const oldEnd = before.length - suffix;
  const delta = after.length - before.length;
  return mentions.flatMap((item) => {
    if (item.offset + item.name.length + 1 <= prefix) return hasCompleteComposerReference(after, item) ? [item] : [];
    if (item.offset >= oldEnd) {
      const shifted = { ...item, offset: item.offset + delta };
      return hasCompleteComposerReference(after, shifted) ? [shifted] : [];
    }
    return [];
  });
}

/** Staged image bytes; marker identifies its editing anchor, never a wire field. */
export interface InputAttachment {
  marker: number;
  mediaType: string;
  data: string;
  name?: string;
}

/**
 * Canonicalize skill names: trimmed, empty entries dropped, and duplicates
 * collapsed preserving first-seen order. Skill names reach the composer from
 * drafts, recovery records and queue projections, any of which can carry a
 * stray space, an empty string or a repeat; the wire must never receive an
 * empty or duplicated canonical name, so every path that assembles a selection
 * list goes through this one definition.
 */
export function canonicalSkillNames(names: readonly string[] | undefined): string[] {
  const out: string[] = [];
  for (const raw of names ?? []) {
    const name = raw.trim();
    if (name === "" || out.includes(name)) continue;
    out.push(name);
  }
  return out;
}

/**
 * Preserve editing anchors when assembling durable recovery input. Canonical
 * skill selections append AFTER the ordinary text/attachment conversion, in
 * selection order: a skill item is canonical-only ({type: "skill", name}), so
 * the user's original prose rides its own item and a selection never
 * replaces or edits it (docs/skills.md's "Canonical skill input on AppWire").
 */
export function buildInput(
  text: string,
  attachments?: readonly InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim())
    input.push({
      type: "text",
      text,
      ...(mentions?.length ? { mentions: mentions.map((item) => ({ ...item })) } : {}),
    });
  for (const attachment of attachments ?? []) {
    const image: InputItem = { type: "image", mediaType: attachment.mediaType, data: attachment.data };
    if (attachment.name !== undefined) image.name = attachment.name;
    input.push(image);
  }
  for (const name of canonicalSkillNames(skillNames)) {
    input.push({ type: "skill", name });
  }
  for (const name of canonicalSkillNames(commandNames)) {
    input.push({ type: "command", name });
  }
  return input;
}

/** Translate editing anchors only at the send, steer, queue or drain boundary. */
export function buildComposerInput(
  text: string,
  attachments?: readonly InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): InputItem[] {
  const translated = translateAttachmentMarkers(text, attachments);
  const translatedMentions = mentions?.map((mention) => ({
    ...mention,
    offset: translateAttachmentMarkers(text.slice(0, mention.offset), attachments).length,
  }));
  return buildInput(translated, attachments, skillNames, commandNames, translatedMentions);
}

// Text going back into a draft: after exactly one blank line (the draft's
// own trailing whitespace dropped), or in place of a blank draft. "prefix"
// puts it in front with no separator, for the web palette's slash-command
// insert: a command parses only at the start of the draft. Shared so the web
// and the phone put a queued message back into the composer the same way
// (spec 8.5: "after a blank line ... as the web does").
export function mergeDraftText(existing: string, addition: string, placement: "append" | "prefix" = "append"): string {
  if (placement === "prefix") return `${addition}${existing}`;
  return existing.trim() === "" ? addition : `${existing.replace(/\s+$/, "")}\n\n${addition}`;
}

/**
 * Turns a raw text-selection string into a markdown blockquote: outer
 * whitespace-only lines are dropped, every remaining line gets its own
 * "> " prefix (an internal blank line becomes a bare "> "), and the block
 * ends with one blank line so it reads as its own paragraph once appended
 * after whatever the composer draft already holds. An empty/whitespace-only
 * selection formats to "" - callers never insert a lone blank block.
 */
export function formatQuoteBlock(selectedText: string): string {
  const trimmed = selectedText.trim();
  if (trimmed === "") return "";
  const lines = trimmed.split(/\r\n|\n/);
  return `${lines.map((line) => `> ${line}`).join("\n")}\n\n`;
}
