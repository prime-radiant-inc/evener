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
    expect(atts).toEqual([
      expect.objectContaining({
        handle: "h1",
        mediaType: "image/jpeg",
        name: "a.jpg",
        id: expect.any(String),
      }),
    ]);
  });

  it("add assigns a unique id per attachment", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    store.getState().add("h2", "image/png");
    const atts = store.getState().attachments;
    expect(new Set(atts.map((attachment) => attachment.id)).size).toBe(2);
  });

  it("remove drops the attachment with the matching id", () => {
    const store = createAttachmentStore();
    store.getState().add("h1", "image/jpeg");
    const first = store.getState().attachments[0];
    if (first === undefined) throw new Error("expected seeded attachment");
    const id = first.id;
    store.getState().add("h2", "image/png");
    store.getState().remove(id);
    const atts = store.getState().attachments;
    expect(atts).toEqual([expect.objectContaining({ handle: "h2" })]);
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
    expect(store.getState().attachments).toEqual([
      expect.objectContaining({ name: undefined }),
    ]);
  });
});

describe("AttachmentStore — PendingAttachment shape", () => {
  it("stores the opaque handle and mediaType", () => {
    const store = createAttachmentStore();
    store.getState().add("opaque-123", "image/heic");
    expect(store.getState().attachments).toEqual([
      expect.objectContaining({
        handle: "opaque-123",
        mediaType: "image/heic",
      }),
    ]);
  });
});
