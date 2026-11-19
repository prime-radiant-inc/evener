// Two panes can hold one destination at the same time, and each mount owns one
// foreground hold. A hold is released by the handle that names it, because a
// ref-keyed release spends whichever hold is NEWEST at that moment. With two
// panes open, that is the newer pane's hold: the older pane's unmount would
// spend it and leave the older pane's own handle counting, so the closed pane
// keeps the destination alive while the acquisition that is still on screen
// reads `claimStanding()` on a handle that no longer stands.

import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { entryTranscriptRoute } from "./entryTranscriptRoutes";
import { blurHumanNote, editHumanNote, focusHumanNote, syncHumanNote } from "./humanNoteDrafts";
import { bodyOwners, type ForegroundHold, foregroundHoldStanding, foregroundHolds } from "./threadOwners";
import { resetThreadsStoreForTests, threadsStore } from "./threads";
import { readResponse } from "./threadsSessionActionsTestUtils";
import { useThreadClaim } from "./useThreadModel";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
});

function Pane({ ref }: { ref: string }) {
  useThreadClaim(ref);
  return null;
}

// Collects each acquisition's own handle, exactly as the pane hook receives it,
// so a test can ask which hold an unmount actually dropped.
function captureHolds(): ForegroundHold[] {
  const holds: ForegroundHold[] = [];
  const ensure = threadsStore.getState().ensureThread.bind(threadsStore.getState());
  vi.spyOn(threadsStore.getState(), "ensureThread").mockImplementation((target, opts) =>
    ensure(target, {
      ...opts,
      onHold: (hold) => {
        holds.push(hold);
        opts?.onHold?.(hold);
      },
    }),
  );
  return holds;
}

// The older pane closes while the read it shares with the newer pane is still
// outstanding. The hold that must be dropped is the older pane's own: the newer
// pane's acquisition is the live one, and ending it at its `claimStanding()`
// check leaves the destination held by a pane nobody has open.
test("the older pane's unmount drops its own hold, not the newer pane's", async () => {
  const ref = "local:two-panes-opening";
  const holds = captureHolds();
  const read = deferred<ThreadReadResponse>();
  let reads = 0;
  connectReadyFakeClient((fake) =>
    fake.on("thread/read", () => {
      reads += 1;
      return read.promise;
    }),
  );

  const older = render(<Pane ref={ref} />);
  await vi.waitFor(() => expect(holds.length, "the older pane takes its hold").toBe(1));
  await vi.waitFor(() => expect(reads, "the older pane's read is issued").toBe(1));
  // The newer pane joins the same pending read.
  const newer = render(<Pane ref={ref} />);
  await vi.waitFor(() => expect(holds.length, "the newer pane takes its own hold").toBe(2));

  // The older pane closes while the read is still outstanding.
  older.unmount();
  expect(
    holds.map(foregroundHoldStanding),
    "the older pane's own hold is dropped and the newer pane's still stands, so its acquisition is the live one",
  ).toEqual([false, true]);
  expect(foregroundHolds(ref), "the newer pane still holds the destination").toBe(1);

  // The read lands and the pane still on screen finishes its own work.
  read.resolve(readResponse(ref));
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(true));
  await vi.waitFor(() => expect(entryTranscriptRoute(ref)?.repository).toBeDefined());
  expect(
    entryTranscriptRoute(ref)?.reader,
    "the pane still on screen finishes its open and keeps the reader",
  ).toBeDefined();
  expect(bodyOwners(ref), "the newer pane's acquisition owns the bodies").toBe(1);

  newer.unmount();
  expect(foregroundHolds(ref), "the newer pane's own release drops its hold").toBe(0);
  expect(bodyOwners(ref), "the newer pane's release drops its body ownership").toBe(0);
}, 20_000);

// The same overlap once both panes are open: the older pane's unmount drops its
// own hold and leaves the newer pane's destination standing, and the newer
// pane's later release is what finally retires it.
test("two open panes release one hold each, newest last", async () => {
  const ref = "local:two-panes-one-ref";
  const client = new FakeClient("ready");
  client.on("thread/read", () => readResponse(ref));
  connectionStore.getState().connect(client);

  const older = render(<Pane ref={ref} />);
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(true));
  expect(foregroundHolds(ref), "the older pane holds the destination").toBe(1);

  const newer = render(<Pane ref={ref} />);
  await vi.waitFor(() => expect(foregroundHolds(ref), "both panes hold the destination").toBe(2));
  expect(bodyOwners(ref), "both panes own the bodies").toBe(2);

  older.unmount();
  expect(foregroundHolds(ref), "the newer pane still holds the destination").toBe(1);
  expect(bodyOwners(ref), "the newer pane still owns the bodies").toBe(1);

  newer.unmount();
  expect(foregroundHolds(ref), "the last pane's release leaves nothing standing").toBe(0);
  expect(bodyOwners(ref), "the last pane's release drops its body ownership").toBe(0);
}, 20_000);

// A debounced note save holds the destination it is about to write at. That
// hold outlives the blur that armed it, so a pane can remount over the same
// ref while the save is still waiting. The save's own exit must then drop the
// count it took: a ref-keyed release names the NEWEST hold, which by that point
// is the replacement pane's, and spending it ends a mounted pane's acquisition
// while the save's own hold keeps the reader and subscription alive after the
// pane that closed.
test("a note save's own exit drops its own hold, not a pane that remounted over it", async () => {
  const ref = "local:note-save-hold";
  connectReadyFakeClient((fake) => fake.on("thread/read", () => readResponse(ref)));
  syncHumanNote(ref, "");
  editHumanNote(ref, "draft that arms the debounce");
  // The blur takes the save's hold and arms the 10s debounce.
  act(() => blurHumanNote(ref, Symbol("blur owner")));
  expect(foregroundHolds(ref), "the pending save holds the destination it will write at").toBe(1);

  // The pane remounts over the same ref while the save waits out its debounce.
  const holds = captureHolds();
  const bodiesBefore = bodyOwners(ref);
  const pane = render(<Pane ref={ref} />);
  await vi.waitFor(() => expect(holds.length, "the replacement pane takes its own hold").toBe(1));
  expect(foregroundHolds(ref), "the save's hold and the pane's both stand").toBe(2);
  const bodiesWithPane = bodyOwners(ref);
  expect(bodiesWithPane, "the pane owns the live bodies").toBe(bodiesBefore + 1);

  // The save's debounce is cancelled (the same exit path a completed save
  // takes through its own finally).
  act(() => focusHumanNote(ref, Symbol("focus after remount")));

  expect(
    holds.map(foregroundHoldStanding),
    "the save drops only its own hold, so the replacement pane's still stands",
  ).toEqual([true]);
  expect(foregroundHolds(ref), "the mounted pane is the only holder left").toBe(1);
  expect(bodyOwners(ref), "the mounted pane keeps the bodies its reader renders").toBe(bodiesBefore);
  expect(entryTranscriptRoute(ref), "the mounted pane's destination survives the save's exit").toBeDefined();
  pane.unmount();
  expect(foregroundHolds(ref), "the pane's own unmount is what finally retires the destination").toBe(0);
}, 20_000);
