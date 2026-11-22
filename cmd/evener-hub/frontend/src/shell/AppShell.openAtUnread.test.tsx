// #4845 slice 2: the needs-you rail, the command palette and Mod+J all land
// at the first unread reply when the opt-in preference is on, and all keep
// the live end when it is off (the default). Each case opens the same session
// through the real entry point and reads the reader's position from the
// transcript's own jump pill: a pane away from the live end offers it, the
// live end does not.

import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { readResponse, turnFixture } from "../panes/session/sessionTestUtils";
import { writeSeenWatermark } from "../panes/session/transcript/flow/seenWatermark";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { sessionReadProgress } from "../stores/readProgress";
import { answerActivityRead } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import { navClient, navigationRead, TREE_SESSION, warmRoute } from "./appShellTestUtils";
import { paletteStore } from "./palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

const REF = "local:unread";
const TITLE = "Needs you 1";

type Entry = "needs-you rail" | "command palette" | "Mod+J";
const ENTRIES: readonly Entry[] = ["needs-you rail", "command palette", "Mod+J"];

let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/settings/Settings");
  await import("../panes/spawn/Spawn");
  await import("./DockHost");
  await warmRoute("/", (wait) => screen.findByText("No session open", undefined, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetNotificationsForTests();
  initNotifications();
  navigationStore.setState({ mode: "v3" });
  installLocalStorage(new MemoryStorage());
  localStorage.clear();
  resetPrefsStoreForTests();
  resetSettingsHostForTests();
  resetAuthStatusesStoreForTests();
  sessionReadProgress.reset();
  writeSeenWatermark(REF, "turn_1");
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  paletteStore.setState({ open: false, query: "" });
  resetSettingsOverviewStoreForTests();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

function unreadReplyClient() {
  const client = navClient();
  // The rail draws the needs-you section the hub serves it, so the client has
  // to answer both the manifest count and the section page. Scripting only the
  // store would leave the rail's loading gate shut (no manifest, no rows).
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({
          sources: [{ id: "local", label: "This host", kind: "local", online: true }],
          attentionSummary: { needsYou: 1, error: 0, working: 0 },
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
      );
    if (params.resource === "section" && params.section === "needs_you")
      return wireSnapshot(params, { sessions: [needsYouRow()], remaining: 0 });
    return navigationRead(params);
  });
  client.on("thread/read", () => Promise.resolve(readResponse(REF, { turns: turns() })));
  client.on("evener/thread/activity/read", answerActivityRead);
  return client;
}

function needsYouRow() {
  return {
    ...TREE_SESSION,
    row_id: `needs-you:${REF}`,
    ref: REF,
    session_id: "unread",
    title: TITLE,
    project: "",
    state: "awaiting",
    live: true,
  };
}

function turns() {
  return [turnFixture("turn_1", "first"), turnFixture("turn_2", "second")];
}

async function mountShell(): Promise<void> {
  const client = unreadReplyClient();
  await client.connect();
  await act(async () => {
    initNavigation(client, capability());
  });
  render(<AppShell client={client} />);
  await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
}

async function openFrom(entry: Entry): Promise<void> {
  const user = userEvent.setup();
  if (entry === "needs-you rail") {
    fireEvent.click(await screen.findByText(TITLE));
  } else if (entry === "command palette") {
    await user.keyboard("{Meta>}k{/Meta}");
    await user.click(await screen.findByRole("option", { name: new RegExp(TITLE, "i") }));
  } else {
    fireEvent.keyDown(window, { key: "j", metaKey: true });
  }
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: REF }));
}

test.each(ENTRIES)("with the preference off, %s keeps the live end", async (entry) => {
  expect(prefsStore.getState().openAtUnreadReplies).toBe(false);
  await mountShell();

  await openFrom(entry);

  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(2));
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

test.each(ENTRIES)("with the preference on, %s lands at the first unread reply", async (entry) => {
  prefsStore.getState().setOpenAtUnreadReplies(true);
  await mountShell();

  await openFrom(entry);

  await waitFor(() => expect(screen.getAllByTestId("turn-block").length).toBe(2));
  expect(screen.getByTestId("new-content-pill")).toBeTruthy();
  expect(screen.getByTestId("seen-divider").closest('[data-testid="turn-block"]')?.getAttribute("data-turn-id")).toBe(
    "turn_2",
  );
});
