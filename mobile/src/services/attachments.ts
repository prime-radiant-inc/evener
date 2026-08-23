// AttachmentService — normalization, validation, and opaque-handle storage
// for pending image attachments before send. V1: the native picker is a
// placeholder; this service validates media types and byte caps, normalizes
// the handle, and stores handles without ever surfacing a native path in
// error copy. At most eight normalized image attachments, 8 MiB each.
//
// The service never touches a real file or native API in V1. It is a pure
// validator that produces PendingAttachment entries the store holds until
// send. Error copy is scrubbed of any native path so a leaked handle never
// reaches the UI.

export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024; // 8 MiB

// The accepted normalized image media types for V1.
const ACCEPTED_MEDIA_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/webp",
  "image/heic",
]);

// The raw input the native picker (placeholder) would produce. `handle` is an
// opaque native handle (a URI, file descriptor id, or similar); `size` is the
// post-normalization byte count.
export interface AttachmentInput {
  readonly handle: string;
  readonly mediaType: string;
  readonly name?: string;
  readonly size: number;
}

// A normalized pending attachment awaiting send. `handle` is opaque and
// never surfaced in error copy.
export interface PendingAttachment {
  id: string;
  handle: string;
  mediaType: string;
  name?: string;
}

export type NormalizeResult =
  | { ok: true; attachment: PendingAttachment }
  | { ok: false; error: string };

// Strip any native path from an error message. A leaked handle or path must
// never reach the UI; this is the last line of defense after the structured
// error builders.
function scrubNativePath(msg: string): string {
  return msg
    .replace(/file:\/\/[^\s]*/gi, "<path>")
    .replace(/\/(?:var|tmp|private|Users)[^\s]*/gi, "<path>");
}

let attachmentIdCounter = 0;
function nextAttachmentId(): string {
  attachmentIdCounter += 1;
  return `att-${Date.now().toString(36)}-${attachmentIdCounter}`;
}

export interface AttachmentService {
  normalize(
    input: AttachmentInput,
    options?: { existingCount?: number },
  ): NormalizeResult;
}

export function createAttachmentService(): AttachmentService {
  let acceptedCount = 0;

  return {
    normalize(input, options) {
      const existingCount = options?.existingCount ?? acceptedCount;

      if (!ACCEPTED_MEDIA_TYPES.has(input.mediaType)) {
        return {
          ok: false,
          error: scrubNativePath(
            `Unsupported image type: ${input.mediaType || "(none)"}`,
          ),
        };
      }

      if (input.size > MAX_ATTACHMENT_BYTES) {
        return {
          ok: false,
          error: "Image exceeds the 8 MiB attachment cap",
        };
      }

      if (existingCount + 1 > MAX_ATTACHMENTS) {
        return {
          ok: false,
          error: "Too many images: the eight-attachment cap has been reached",
        };
      }

      acceptedCount += 1;
      return {
        ok: true,
        attachment: {
          id: nextAttachmentId(),
          handle: input.handle,
          mediaType: input.mediaType,
          name: input.name,
        },
      };
    },
  };
}
