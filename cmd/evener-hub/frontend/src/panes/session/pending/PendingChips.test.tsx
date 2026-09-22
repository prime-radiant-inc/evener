import { cleanup, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import type { InputAttachment } from "../../../stores/threads";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import type { PendingMethod } from "../composer/queue/pendingReconcile";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "../composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../composer/queue/testing/flushPendingTurnsProjection";
import { PendingChips } from "./PendingChips";

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  // PendingChips reads the shared pendingTurnsStore, which subscribes to the
  // threads store for reconciliation - reset both so entries seeded here can't
  // be reaped by another test's thread snapshot, and vice versa.
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

// A successful perform() deliberately does NOT reconcile the entry (see
// pendingTurnsStore's own contract), so a resolving perform leaves the chip
// pending - exactly the optimistic in-flight state PendingChips renders.
async function seedPending(
  method: PendingMethod,
  text: string,
  ref = "ref_a",
  attachments?: InputAttachment[],
  skillNames: string[] = [],
) {
  const wireMethod = {
    send: "turn/start",
    steer: "turn/steer",
    queue: "turn/queue",
    drain: "turn/drainAsSteer",
    promote: "turn/promoteQueuedAsSteer",
  }[method];
  const input = [
    ...(text ? [{ type: "text", text }] : []),
    ...(attachments ?? []).map((attachment) => ({
      type: "image",
      mediaType: attachment.mediaType,
      data: attachment.data,
      name: attachment.name,
    })),
    ...skillNames.map((name) => ({ type: "skill", name })),
  ];
  const storage = new MutationOutboxIndexedDB();
  await storage.enqueueIntent({
    targetRef: ref,
    method: wireMethod,
    payload: { ref, input },
    attachments: (attachments ?? []).map((attachment, index) => ({
      presentationId: `presentation_${index}`,
      marker: attachment.marker,
      name: attachment.name ?? "attachment",
      mediaType: attachment.mediaType,
      blob: new Blob(),
    })),
    optimisticDisplay: { method: wireMethod, input },
  });
  storage.close();
  await refreshPendingTurnsProjection(ref);
  await flushPendingTurnsProjectionForTests();
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

test("chips send only: steer/drain are the ghost stack's surface, never a chip", async () => {
  await seedPending("send", "a send");
  await seedPending("steer", "a steer");
  await seedPending("drain", "a drain");
  await seedPending("queue", "a queued one");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("a send")).toBeTruthy();
  expect(screen.queryByText("a steer")).toBeNull();
  expect(screen.queryByText("a drain")).toBeNull();
  expect(screen.queryByText("a queued one")).toBeNull();
});

test("a pending promote never chips (the ghost stack owns it)", async () => {
  await seedPending("promote", "a promote");
  const { container } = render(<PendingChips sessionRef="ref_a" />);
  expect(screen.queryByText("a promote")).toBeNull();
  expect(container.innerHTML).toBe(""); // the strip renders null with no send entries
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

// The canceled counterpart of the test above: a row Stop canceled is a durable
// QueueStrip row ("Canceled by Stop", with its Retry affordance), not an
// in-flight chip - without the exclusion it would read as still Sending here
// while QueueStrip simultaneously reports it canceled.
test("canceled is owned by QueueStrip rather than PendingChips", async () => {
  const storage = new MutationOutboxIndexedDB();
  await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "stopped" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stopped" }] },
  });
  await storage.cancelUnattempted("ref_a");
  storage.close();
  await refreshPendingTurnsProjection("ref_a");
  // Same flush seedPending performs: the first refresh after the runtime's
  // creation can be superseded by the runtime's own discovery scan, so the
  // settled projection - not a racing one - is what the render must observe.
  await flushPendingTurnsProjectionForTests();

  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.queryByText("stopped")).toBeNull();
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

// A slash-completed skill submission carries no typed prose, so the marker IS
// the only thing there is to show - the same [skill: …] the queue strip renders
// for the identical selection. Without it the in-flight chip reads as a bare
// "Sending" with an empty body.
test("a skill-only pending chip shows its skill marker rather than an empty body", async () => {
  await seedPending("send", "", "ref_a", undefined, ["pkg:probe"]);
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("[skill: pkg:probe]")).toBeTruthy();
});

test("a pending chip renders its preview text and skill marker together", async () => {
  await seedPending("send", "nudge", "ref_a", undefined, ["pkg:probe"]);
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("nudge [skill: pkg:probe]")).toBeTruthy();
});

test("labels a send chip as Sending", async () => {
  await seedPending("send", "nudge");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText(/sending/i)).toBeTruthy();
});
