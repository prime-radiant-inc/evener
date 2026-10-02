import { translateAttachmentMarkers } from "./attachmentMarkers";
import type { InputItem } from "./types.gen";

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
): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
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
): InputItem[] {
  return buildInput(translateAttachmentMarkers(text, attachments), attachments, skillNames, commandNames);
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
