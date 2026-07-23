import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { InputAttachment } from "../../../stores/threads";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import type { PendingMethod } from "../composer/queue/pendingReconcile";
import { resetPendingTurnsStoreForTests, submitWithPendingTracking } from "../composer/queue/pendingTurnsStore";
import { PendingChips } from "./PendingChips";

beforeEach(() => {
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
  await submitWithPendingTracking({ ref, method, text, attachments, onFailure: () => {} }, () => Promise.resolve());
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

test("shows only the entries for the given sessionRef", async () => {
  await seedPending("send", "mine", "ref_a");
  await seedPending("send", "theirs", "ref_b");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("mine")).toBeTruthy();
  expect(screen.queryByText("theirs")).toBeNull();
});

test("an image-only pending entry shows the image placeholder rather than blank text", async () => {
  await seedPending("send", "", "ref_a", [{ mediaType: "image/png", data: "AAAA" }]);
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("[image]")).toBeTruthy();
});

test("labels each chip with its method so send/steer/drain read apart", async () => {
  await seedPending("steer", "nudge");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText(/steering/i)).toBeTruthy();
});
