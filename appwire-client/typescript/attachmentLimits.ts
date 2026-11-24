// Attachment size/count/type limits shared by every composer that stages
// images for a send (parity-m5-composer.md §G, contracts §Attachments):
// max 8 attachments, max 8 MiB per file. The 8-count cap is CUMULATIVE
// across the whole composer session (paste + drag + file-picker share one
// running total) - callers pass the count already reserved so far, not a
// fresh read of "how many are there right now", since a batch (e.g.
// dropping several files at once) must count each prior file in the SAME
// batch too.
export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024;

// A composer that decodes an image before sending it bounds the decode with
// the source's own bytes and pixels: far above a camera photo, and a ceiling
// on the memory the decode takes.
export const MAX_SOURCE_BYTES = 25 * 1024 * 1024;
export const MAX_SOURCE_PIXELS = 8064 * 6048;

// The decoded result is redrawn with its long edge at most this many pixels
// and encoded as JPEG at this quality: a camera photo comes out a few MB,
// well inside MAX_ATTACHMENT_BYTES.
export const MAX_IMAGE_LONG_EDGE = 2048;
export const IMAGE_JPEG_QUALITY = 0.85;

export interface RejectableFile {
  type: string;
  size: number;
  name: string;
}

/** Why an attachment was refused, for each composer to put in its own words. */
export type AttachmentRejection = "notImage" | "tooMany" | "tooLarge";

// admissionRejection is the check a file faces before it is staged at all:
// only images, and no more than MAX_ATTACHMENTS counting those already
// reserved. A non-image is refused first, whatever the count.
export function admissionRejection(file: { type: string }, reservedCount: number): AttachmentRejection | undefined {
  if (!file.type.startsWith("image/")) return "notImage";
  if (reservedCount >= MAX_ATTACHMENTS) return "tooMany";
  return undefined;
}

// sizeRejection is the size check alone, for the bytes that will be sent: a
// caller that scales an image first (the phone) measures the result.
export function sizeRejection(bytes: number): AttachmentRejection | undefined {
  return bytes > MAX_ATTACHMENT_BYTES ? "tooLarge" : undefined;
}

// rejectionReason returns undefined when `file` is acceptable, or the web
// composer's words for the limit it broke, which it shows after "Couldn't
// attach": the bare filename for a non-image, else the filename and the limit.
// Admission comes first, so a file that breaks both count and size reports the
// count.
export function rejectionReason(file: RejectableFile, reservedCount: number): string | undefined {
  const name = file.name || "unknown";
  switch (admissionRejection(file, reservedCount) ?? sizeRejection(file.size)) {
    case "notImage":
      return name;
    case "tooMany":
      return `${name} (maximum ${MAX_ATTACHMENTS} images)`;
    case "tooLarge":
      return `${name} (maximum 8 MB)`;
    case undefined:
      return undefined;
  }
}

// fitImageDimensions is the size an image is redrawn at before encoding: its
// long edge capped to maxEdge (MAX_IMAGE_LONG_EDGE by default) and the short
// edge scaled by the same ratio, rounded to whole pixels and never below one.
// An image that already fits keeps its dimensions unchanged.
export function fitImageDimensions(
  width: number,
  height: number,
  maxEdge: number = MAX_IMAGE_LONG_EDGE,
): { width: number; height: number } {
  const longEdge = Math.max(width, height);
  if (longEdge <= maxEdge) return { width, height };
  return {
    width: Math.max(1, Math.round((width * maxEdge) / longEdge)),
    height: Math.max(1, Math.round((height * maxEdge) / longEdge)),
  };
}
