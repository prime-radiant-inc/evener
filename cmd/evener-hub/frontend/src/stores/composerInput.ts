import type { InputItem } from "../protocol/types.gen";
import { translateAttachmentMarkers } from "./attachmentMarkers";

/** Staged image bytes; marker identifies its editing anchor, never a wire field. */
export interface InputAttachment {
  marker: number;
  mediaType: string;
  data: string;
  name?: string;
}

/** Preserve editing anchors when assembling durable recovery input. */
export function buildInput(text: string, attachments?: readonly InputAttachment[]): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
  for (const attachment of attachments ?? []) {
    const image: InputItem = { type: "image", mediaType: attachment.mediaType, data: attachment.data };
    if (attachment.name !== undefined) image.name = attachment.name;
    input.push(image);
  }
  return input;
}

/** Translate editing anchors only at the send, steer, queue or drain boundary. */
export function buildComposerInput(text: string, attachments?: readonly InputAttachment[]): InputItem[] {
  return buildInput(translateAttachmentMarkers(text, attachments), attachments);
}
