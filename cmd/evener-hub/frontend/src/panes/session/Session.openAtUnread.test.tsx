// #4845 slice 2: where a session opens when the opt-in open-at-unread
// preference is on, and the live end it keeps when the preference is off.
//
// The landing's own scroll arithmetic is covered by useTranscriptScroll's
// "opening at the unseen reply" suite and the hook's gate by
// useSeenDivider.test.ts; these cases prove the pane actually wires the
// preference into that decision against the real VirtualList, and that the
// landing leaves the last-visit marker alone.
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { sessionReadProgress } from "../../stores/readProgress";
import { answerActivityRead } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../stores/threads";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import "./index";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { readResponse, setNavigationTitle, stubSessionSlots, turnFixture } from "./sessionTestUtils";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { readSeenWatermark, writeSeenWatermark } from "./transcript/flow/seenWatermark";

stubSessionSlots();

// jsdom computes no layout: give the transcript port a real box so VirtualList
// renders rows (the same stub Session.states.test.tsx documents).
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  globalThis.IDBKeyRange = IDBKeyRange;
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  resetPrefsStoreForTests();
  sessionReadProgress.reset();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

const REF = "ref_open_at_unread";

function turnsWithUnseenReply(secondStatus: "completed" | "failed" = "completed") {
  return [turnFixture("turn_1", "first"), { ...turnFixture("turn_2", "second"), status: secondStatus }];
}

// The first unseen turn is the one after the stored last-visit watermark.
function visitBeforeTheReply(): void {
  writeSeenWatermark(REF, "turn_1");
}

function replyService(secondStatus: "completed" | "failed" = "completed") {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => Promise.resolve(readResponse(REF, { turns: turnsWithUnseenReply(secondStatus) })));
  fake.on("evener/thread/activity/read", answerActivityRead);
  return fake;
}

async function renderSession(fake: ReturnType<typeof connectReadyFakeClient>) {
  const rendered = render(
    <ClientProvider client={fake}>
      <Session params={{ ref: REF }} paneId="p1" focused={true} />
    </ClientProvider>,
  );
  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(2));
  return rendered;
}

test("with the preference off (the default), a session with a finished unseen reply opens at the live end", async () => {
  visitBeforeTheReply();
  expect(prefsStore.getState().openAtUnreadReplies).toBe(false);

  await renderSession(replyService());

  // Away from the live end is what the pill announces; the live end has none.
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
  expect(screen.getAllByTestId("seen-divider")).toHaveLength(1);
});

test("with the preference on, it lands at the start of the first unseen reply", async () => {
  visitBeforeTheReply();
  prefsStore.getState().setOpenAtUnreadReplies(true);

  await renderSession(replyService());

  // The landing puts the reader above the live end, so the jump pill is on
  // offer and the divider marks the reply it landed on.
  expect(screen.getByTestId("new-content-pill")).toBeTruthy();
  const divider = screen.getByTestId("seen-divider");
  expect(divider.closest('[data-testid="turn-block"]')?.getAttribute("data-turn-id")).toBe("turn_2");
});

// A turn that failed still ends where it ends, so its start is a place a
// reader can have left off (the phone's latestSettledTurnId does the same).
test("a failed first unseen reply is read-eligible: the preference lands there too", async () => {
  writeSeenWatermark(REF, "turn_1");
  prefsStore.getState().setOpenAtUnreadReplies(true);

  await renderSession(replyService("failed"));

  expect(screen.getByTestId("new-content-pill")).toBeTruthy();
});

// Landing is not reading: the last-visit watermark is written when the reader
// leaves, so an opening at the unseen reply must not advance it on the way in.
test("the landing leaves the last-visit marker alone while the pane stays open", async () => {
  visitBeforeTheReply();
  prefsStore.getState().setOpenAtUnreadReplies(true);

  const { unmount } = await renderSession(replyService());

  expect(readSeenWatermark(REF)).toBe("turn_1");
  unmount();
  expect(readSeenWatermark(REF)).toBe("turn_2");
});

// The rail's unread mark is read progress, a different store from the
// transcript's last-visit marker. Opening marks the session read at once
// (markSeen.ts) while the transcript watermark stays where the reader left it.
test("opening marks read progress without touching the last-visit marker", async () => {
  visitBeforeTheReply();
  prefsStore.getState().setOpenAtUnreadReplies(true);
  setNavigationTitle(REF, "Unread session", true, {
    unseen: true,
    updated_at: "2026-11-22T05:00:00.000Z",
  });

  await renderSession(replyService());

  const row = { ref: REF, unseen: true, updated_at: "2026-11-22T05:00:00.000Z" };
  await waitFor(() => expect(sessionReadProgress.isSeen(row)).toBe(true));
  expect(readSeenWatermark(REF)).toBe("turn_1");
});
