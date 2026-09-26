// The composer holds its draft in state, so every keystroke re-renders
// Composer. The inline SessionChrome it mounts (status row, goal chip, menu,
// and the hidden Details/Activity panels) depends on none of that draft, so a
// keystroke must not re-render it. StatusRow renders once per SessionChrome
// render, so counting StatusRow renders observes the chrome's own renders
// while the real StatusRow still draws.

import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { activitySummaryStore } from "../../../stores/activitySummary";
import { connectionStore } from "../../../stores/connection";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import "../testing/editorGeometry";
import { resetAskDockStoreForTests } from "../composer/askDock/askDockStore";
import { Composer } from "../composer/Composer";
import { resetPendingTurnsStoreForTests } from "../composer/queue/pendingTurnsStore";

const statusRowRenders = vi.hoisted(() => ({ count: 0 }));

vi.mock("./StatusRow", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./StatusRow")>();
  return {
    ...actual,
    StatusRow: (props: Parameters<typeof actual.StatusRow>[0]) => {
      statusRowRenders.count += 1;
      return <actual.StatusRow {...props} />;
    },
  };
});

// SessionChrome's work-time clock re-renders it every NOW_TICK_MS on its own.
// Holding the clock still keeps a slow run from crossing a tick mid-typing, so
// only typing can move the count.
vi.mock("../liveness", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../liveness")>();
  return { ...actual, useNowTick: () => 1_000_000 };
});

const REF = "ref_a";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: false,
  forkFromTurn: false,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function idleThread(): Thread {
  return {
    id: `thr_${REF}`,
    sessionId: `sess_${REF}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref: REF, capabilities: CAPABILITIES, queue: { revision: 0 } },
  };
}

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  statusRowRenders.count = 0;
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
});

test("typing in the composer does not re-render its inline session chrome", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => ({ thread: idleThread() }) as ThreadReadResponse);
  await threadsStore.getState().ensureThread(REF);
  render(
    <ClientProvider client={fake}>
      <Composer ref={REF} focused={false} />
    </ClientProvider>,
  );
  const textbox = screen.getByRole("textbox", { name: /^message$/i });
  expect(screen.getByTestId("session-chrome-inline-status")).toBeTruthy();
  // The chrome's hidden ActivityPanel fetches the summary its menu label
  // reads; that settle re-renders the chrome on its own, so the baseline is
  // taken once it lands.
  await waitFor(() => expect(activitySummaryStore.getState().entries.get(REF)?.loading).toBe(false));
  const rendersBeforeTyping = statusRowRenders.count;
  expect(rendersBeforeTyping).toBeGreaterThan(0);

  await userEvent.type(textbox, "hello there");

  expect(textbox.textContent).toBe("hello there");
  expect(statusRowRenders.count).toBe(rendersBeforeTyping);
});
