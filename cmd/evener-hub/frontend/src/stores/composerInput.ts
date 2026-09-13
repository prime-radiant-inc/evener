import { translateAttachmentMarkers } from "../protocol/attachmentMarkers";
import type { InputItem } from "../protocol/types.gen";

/** Staged image bytes; marker identifies its editing anchor, never a wire field. */
export interface InputAttachment {
  marker: number;
  mediaType: string;
  data: string;
  name?: string;
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
  for (const name of skillNames ?? []) {
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
