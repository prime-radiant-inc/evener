import { cleanup, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import type { InputAttachment } from "../../../stores/threads";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import type { PendingMethod } from "../composer/queue/pendingReconcile";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../composer/queue/pendingTurnsStore";
import { PendingChips } from "./PendingChips";

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  // PendingChips reads the shared pendingTurnsStore, which subscribes to the
  // threads store for reconciliation - reset both so entries seeded here can't
  // be reaped by another test's thread snapshot, and vice versa.
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => cleanup());

// A successful perform() deliberately does NOT reconcile the entry (see
// pendingTurnsStore's own contract), so a resolving perform leaves the chip
// pending - exactly the optimistic in-flight state PendingChips renders.
async function seedPending(method: PendingMethod, text: string, ref = "ref_a", attachments?: InputAttachment[]) {
  const wireMethod = {
    send: "turn/start",
    steer: "turn/steer",
    queue: "turn/queue",
    drain: "turn/drainAsSteer",
  }[method];
  const input = [
    ...(text ? [{ type: "text", text }] : []),
    ...(attachments ?? []).map((attachment) => ({
      type: "image",
      mediaType: attachment.mediaType,
      data: attachment.data,
      name: attachment.name,
    })),
  ];
  const storage = new MutationOutboxIndexedDB();
  await storage.enqueueIntent({
    targetRef: ref,
    method: wireMethod,
    payload: { ref, input },
    attachments: (attachments ?? []).map((attachment, index) => ({
      presentationId: `presentation_${index}`,
      name: attachment.name ?? "attachment",
      mediaType: attachment.mediaType,
      blob: new Blob(),
    })),
    optimisticDisplay: { method: wireMethod, input },
  });
  storage.close();
  await refreshPendingTurnsProjection(ref);
}

test("renders nothing when there are no pending entries", () => {
  const { container } = render(<PendingChips sessionRef="ref_a" />);
  expect(container.innerHTML).toBe("");
});

test("renders a compact chip for a pending send, showing its preview text", async () => {
  await seedPending("send", "hello there");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("hello there")).toBeTruthy();
});

test("chips the send/steer/drain methods but not queue (QueueStrip already chips queue)", async () => {
  await seedPending("send", "a send");
  await seedPending("steer", "a steer");
  await seedPending("drain", "a drain");
  await seedPending("queue", "a queued one");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("a send")).toBeTruthy();
  expect(screen.getByText("a steer")).toBeTruthy();
  expect(screen.getByText("a drain")).toBeTruthy();
  expect(screen.queryByText("a queued one")).toBeNull();
});

test("blocked unknown is owned by QueueStrip rather than PendingChips", async () => {
  const storage = new MutationOutboxIndexedDB();
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "uncertain" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "uncertain" }] },
  });
  await storage.markUnknown(outbox.clientMutationId, "blockedUnknown");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");

  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.queryByText("uncertain")).toBeNull();
});

test("shows only the entries for the given sessionRef", async () => {
  await seedPending("send", "mine", "ref_a");
  await seedPending("send", "theirs", "ref_b");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("mine")).toBeTruthy();
  expect(screen.queryByText("theirs")).toBeNull();
});

test("an image-only pending entry shows the image placeholder rather than blank text", async () => {
  await seedPending("send", "", "ref_a", [{ marker: 1, mediaType: "image/png", data: "AAAA" }]);
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("[image]")).toBeTruthy();
});

test("labels each chip with its method so send/steer/drain read apart", async () => {
  await seedPending("steer", "nudge");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText(/steering/i)).toBeTruthy();
});
