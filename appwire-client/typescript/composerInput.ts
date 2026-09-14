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
  return input;
}

/** Translate editing anchors only at the send, steer, queue or drain boundary. */
export function buildComposerInput(
  text: string,
  attachments?: readonly InputAttachment[],
  skillNames?: readonly string[],
): InputItem[] {
  return buildInput(translateAttachmentMarkers(text, attachments), attachments, skillNames);
}
