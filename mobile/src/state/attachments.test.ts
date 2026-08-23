// AttachmentState (Zustand) tests — a per-session store of pending
// attachments with add/remove/clear and an error slot. Tests use a fake store
// created via the real createAttachmentStore so the state transitions are
// exercised against the real reducer.

import { describe, expect, it } from "vitest";
import { createAttachmentStore } from "./attachments";

describe("AttachmentStore", () => {
  it("starts empty with no error", () => {
    const store = createAttachmentStore();
    expect(store.getState().attachments).toEqual([]);
    expect(store.getState().error).toBeNull();
  });

  it("add appends a pending attachment", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg", "a.jpg");
    const atts = store.getState().attachments;
    expect(atts).toHaveLength(1);
    expect(atts[0]!.handle).toBe("h1");
    expect(atts[atts.length - 1]!.mediaType).toBe("image/jpeg");
    expect(atts[atts.length - 1]!.name).toBe("a.jpg");
    expect(typeof atts[atts.length - 1]!.id).toBe("string");
  });

  it("add assigns a unique id per attachment", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    store.getState().add("h2", "image/png");
    const atts = store.getState().attachments;
    expect(atts[0]!.id).not.toBe(atts[1]!.id);
  });

  it("remove drops the attachment with the matching id", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    const id = store.getState().attachments[0]!.id;
    store.getState().add("h2", "image/png");
    store.getState().remove(id);
    const atts = store.getState().attachments;
    expect(atts).toHaveLength(1);
    expect(atts[0]!.handle).toBe("h2");
  });

  it("remove is a no-op for an unknown id", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    store.getState().remove("nope");
    expect(store.getState().attachments).toHaveLength(1);
  });

  it("clear empties the attachment list", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    store.getState().add("h2", "image/png");
    store.getState().clear();
    expect(store.getState().attachments).toEqual([]);
  });

  it("setError sets and clears the error message", () => {
    const store = createAttachmentStore();
    store.getState().setError("Too many images");
    expect(store.getState().error).toBe("Too many images");
    store.getState().setError(null);
    expect(store.getState().error).toBeNull();
  });

  it("add accepts an attachment with no name", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    expect(store.getState().attachments[0]!.name).toBeUndefined();
  });
});

describe("AttachmentStore — PendingAttachment shape", () => {
  it("stores the opaque handle and mediaType", () => {
    const store = createAttachmentStore();
    store.getState().add("opaque-123", "image/heic");
    const att = store.getState().attachments[0]!;
    expect(att.handle).toBe("opaque-123");
    expect(att.mediaType).toBe("image/heic");
  });
});
