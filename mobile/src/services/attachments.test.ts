// AttachmentService tests — normalization, validation, and opaque-handle storage.
//
// V1: the native picker is a placeholder. The service validates media types and
// byte caps, normalizes the handle, and stores handles without ever surfacing
// a native path in error copy.

import { describe, expect, it } from "vitest";
import {
  type AttachmentInput,
  createAttachmentService,
  MAX_ATTACHMENT_BYTES,
  MAX_ATTACHMENTS,
} from "./attachments";

function input(over: Partial<AttachmentInput> = {}): AttachmentInput {
  return {
    handle: "native-handle-1",
    mediaType: "image/jpeg",
    name: "photo.jpg",
    size: 1024,
    ...over,
  };
}

describe("AttachmentService — media type validation", () => {
  it("accepts image/jpeg", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "image/jpeg" }));
    expect(result.ok).toBe(true);
  });

  it("accepts image/png", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "image/png" }));
    expect(result.ok).toBe(true);
  });

  it("accepts image/webp", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "image/webp" }));
    expect(result.ok).toBe(true);
  });

  it("accepts image/heic", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "image/heic" }));
    expect(result.ok).toBe(true);
  });

  it("rejects image/gif with an error", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "image/gif" }));
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toMatch(/unsupported/i);
    }
  });

  it("rejects application/pdf with an error", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "application/pdf" }));
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toMatch(/unsupported/i);
    }
  });

  it("rejects an empty media type", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ mediaType: "" }));
    expect(result.ok).toBe(false);
  });
});

describe("AttachmentService — byte cap", () => {
  it("accepts an image at exactly 8 MiB", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(
      input({ size: MAX_ATTACHMENT_BYTES, mediaType: "image/jpeg" }),
    );
    expect(result.ok).toBe(true);
  });

  it("rejects an image larger than 8 MiB post-normalization", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(
      input({ size: MAX_ATTACHMENT_BYTES + 1, mediaType: "image/jpeg" }),
    );
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toMatch(/8 mib|too large|exceeds/i);
    }
  });
});

describe("AttachmentService — opaque handle use", () => {
  it("stores the native handle verbatim and never returns it in error copy", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(
      input({
        handle: "file:///var/mobile/Media/photo.jpg",
        mediaType: "image/gif",
      }),
    );
    expect(result.ok).toBe(false);
    if (!result.ok) {
      // Error copy must never include the native path.
      expect(result.error).not.toMatch(/file:\/\//i);
      expect(result.error).not.toMatch(/var\/mobile/i);
    }
  });

  it("produces a PendingAttachment carrying the opaque handle", () => {
    const svc = createAttachmentService();
    const result = svc.normalize(input({ handle: "opaque-handle-xyz" }));
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.attachment.handle).toBe("opaque-handle-xyz");
    }
  });

  it("assigns a stable unique id per attachment", () => {
    const svc = createAttachmentService();
    const a = svc.normalize(input());
    const b = svc.normalize(input({ handle: "h2" }));
    expect(a.ok).toBe(true);
    expect(b.ok).toBe(true);
    if (a.ok && b.ok) {
      expect(a.attachment.id).not.toBe(b.attachment.id);
    }
  });
});

describe("AttachmentService — count cap", () => {
  it("enforces an eight-count cap", () => {
    const svc = createAttachmentService();
    for (let i = 0; i < MAX_ATTACHMENTS; i++) {
      const r = svc.normalize(input({ handle: `h${i}` }));
      expect(r.ok).toBe(true);
    }
    const ninth = svc.normalize(input({ handle: "h9" }));
    expect(ninth.ok).toBe(false);
    if (!ninth.ok) {
      expect(ninth.error).toMatch(/eight|8|too many|max/i);
    }
  });

  it("respects an existing count passed via options", () => {
    const svc = createAttachmentService();
    const r = svc.normalize(input(), { existingCount: MAX_ATTACHMENTS });
    expect(r.ok).toBe(false);
  });
});

describe("AttachmentService — name passthrough", () => {
  it("carries an optional name", () => {
    const svc = createAttachmentService();
    const r = svc.normalize(input({ name: "vacation.heic" }));
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.attachment.name).toBe("vacation.heic");
    }
  });

  it("allows an undefined name", () => {
    const svc = createAttachmentService();
    const r = svc.normalize(input({ name: undefined }));
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.attachment.name).toBeUndefined();
    }
  });
});
