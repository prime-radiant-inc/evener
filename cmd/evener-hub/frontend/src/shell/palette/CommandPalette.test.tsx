import { act, cleanup, fireEvent, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { ALL_CAPABILITIES as CAPS } from "../../threadTestUtils";
import "../../panes/sessionPanels";
import "../../panes/session";
import { ACTIONS, rebindAction, restoreDefaultBinding, WireError } from "@evener/appwire-client";
import { resetComposerFocusStoreForTests, useComposerFocusRequest } from "../../panes/session/composer/composerFocus";
import { resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "../../panes/session/composer/quoteInsert";
import "../../panes/sessionPanels";
import type {
  ItemModel,
  NavigationSessionSummary,
  SearchResult,
  ThreadModel,
  ThreadReadResponse,
  TurnModel,
} from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { manifest } from "@evener/appwire-client/testing/navigation";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { IDBFactory } from "fake-indexeddb";
import { keybindingsRegistry } from "../../keybindings/appRegistry";
import { Composer } from "../../panes/session/composer/Composer";
import { refreshPendingTurnsProjection } from "../../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { createTestComposerSource } from "../../panes/session/testing/composerSource";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { chordLabel, Dialog, Toast } from "../../widgets";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import { currentAttentionEntryToken } from "../attentionHistory";
import { NeedsYouProvider } from "../NeedsYouProvider";
import { updateNeedsYouPage } from "../needsYouTestUtils";
import { navigate } from "../routing";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette, commandErrorMessage } from "./CommandPalette";
import { closePalette, openPalette, paletteStore } from "./paletteController";
import {
  helpChords,
  paletteIndirectRows,
  paletteNestedRows,
  paletteRowsOfKind,
  paletteSection,
  renderPalette as render,
  scriptSearch,
} from "./paletteTestUtils";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: CAPS,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

function item(id: string, text: string): ItemModel {
  return { id, turnId: "t1", type: "message", text };
}
function turn(items: ItemModel[]): TurnModel {
  return { id: "t1", status: "completed", items };
}

function focusSession(ref: string, overrides: Partial<ThreadModel> = {}): void {
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: "p1",
  });
  threadsStore.setState({ threads: new Map([[ref, testModel({ ref, ...overrides })]]) });
}

beforeEach(() => {
  paletteStore.setState({ open: false, query: "", openSeq: 0 });
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  resetToastStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  // Keep viewport tests isolated from direct matchMedia assignments.
  // @ts-expect-error jsdom baseline has no matchMedia.
  delete window.matchMedia;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- T1 behaviors preserved ---

test("renders no overlay while the palette store is closed", () => {
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("opens an overlay named 'Command palette' when the store opens", () => {
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Escape closes the overlay when no command is selected", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(paletteStore.getState().open).toBe(false);
});

// --- empty-query view: needs-you sessions (UX fix) ------------------------

function needsYouRows(): NavigationSessionSummary[] {
  return [
    {
      ref: "local:ny1",
      host_id: "local",
      session_id: "ny1",
      title: "Session A",
      project: "P",
      state: "awaiting",
      kind: "session",
      live: true,
      children: [],
    },
    {
      ref: "local:ny2",
      host_id: "local",
      session_id: "ny2",
      title: "Session B",
      project: "P",
      state: "awaiting",
      kind: "session",
      live: true,
      children: [],
    },
  ];
}
function setNeedsYouRows(rows: NavigationSessionSummary[] | null): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    visibleResourceKeys: rows === null ? new Set<string>() : new Set([keyID(key)]),
    resources:
      rows === null
        ? new Map()
        : new Map([
            [
              keyID(key),
              {
                key,
                data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
                loadedRevision: 1,
                targetRevision: null,
                forceToken: 0,
                etag: "etag",
                loading: false,
                stale: false,
                error: null,
                generationID: "generation_test",
              },
            ],
          ]),
  });
}

test("the empty-query view lists needs-you sessions when any exist", () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());

  expect(paletteSection("needs-you")).not.toBeNull();
  const rows = paletteRowsOfKind("needsYou");
  expect(rows).toEqual([
    screen.getByRole("option", { name: /Session A/i }),
    screen.getByRole("option", { name: /Session B/i }),
  ]);
});

// An approval blocks its turn mid-tool, so the row's wire state stays
// "active": the dot must read approval_pending, not the state alone.
test("a needs-you row waiting on an approval shows a needs-you dot, not a working one", () => {
  setNeedsYouRows(
    needsYouRows().map((row) => (row.ref === "local:ny1" ? { ...row, state: "active", approval_pending: true } : row)),
  );
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());

  const option = screen.getByRole("option", { name: /Session A/i });
  expect(within(option).getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test.each(["Enter", "click"] as const)(
  "%s on a valid needs-you row closes the palette and opens that session once",
  async (activation) => {
    const user = userEvent.setup();
    setNeedsYouRows(needsYouRows());
    focusSession("ref_source");
    const sourcePaneId = workspaceStore.getState().focusedPaneId;
    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    const historyLength = window.history.length;
    const row = screen.getByRole("option", { name: /Session A/i });

    if (activation === "Enter") await user.keyboard("{Enter}");
    else await user.click(row);

    await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Any1"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(window.history.length).toBe(historyLength + 1);
    expect(currentAttentionEntryToken()).not.toBeNull();
    expect(
      workspaceStore.getState().panes.find((pane) => (pane.params as { ref?: unknown }).ref === "ref_source")?.id,
    ).toBe(sourcePaneId);
  },
);

test.each(["Enter", "click"] as const)(
  "%s on a refreshing needs-you row is refused until the page settles",
  async (activation) => {
    const user = userEvent.setup();
    setNeedsYouRows(needsYouRows());
    focusSession("ref_source");
    // The controller publishes this row as selectable:false, refreshState
    // "refreshing" while its page revalidates.
    updateNeedsYouPage(0, { stale: true, loading: true });
    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    const historyLength = window.history.length;
    const refused = screen.getByRole("option", { name: /Session A/i });

    expect(refused.getAttribute("aria-disabled")).toBe("true");
    expect(refused.getAttribute("data-refresh-state")).toBe("refreshing");

    if (activation === "Enter") await user.keyboard("{Enter}");
    else await user.click(refused);

    expect(paletteStore.getState().open).toBe(true);
    expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
    expect(window.location.pathname).toBe("/");
    expect(window.history.length).toBe(historyLength);

    act(() => updateNeedsYouPage(0, { stale: false, loading: false, error: null }));
    await waitFor(() =>
      expect(screen.getByRole("option", { name: /Session A/i }).getAttribute("aria-disabled")).toBeNull(),
    );
    const settled = screen.getByRole("option", { name: /Session A/i });
    expect(settled.getAttribute("data-refresh-state")).toBeNull();

    if (activation === "Enter") await user.keyboard("{Enter}");
    else await user.click(settled);

    await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Any1"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(window.history.length).toBe(historyLength + 1);
  },
);

test("a failed needs-you page is refused in place", () => {
  setNeedsYouRows(needsYouRows());
  updateNeedsYouPage(0, { stale: true, loading: false, error: new Error("refresh failed") });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  const refused = screen.getByRole("option", { name: /Session A/i });

  expect(refused.getAttribute("aria-disabled")).toBe("true");
  expect(refused.getAttribute("data-refresh-state")).toBe("failed");

  fireEvent.click(refused);
  expect(paletteStore.getState().open).toBe(true);
  expect(window.location.pathname).toBe("/");
});

test("a page that turns stale before the next paint still refuses activation", () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  const row = screen.getByRole("option", { name: /Session A/i });
  expect(row.getAttribute("aria-disabled")).toBeNull();

  // Keep the page change and the click in one synchronous act: React flushes
  // act work only when the outer scope exits, so the click still runs the
  // handler from the pre-update render and must be refused by the live
  // controller snapshot read.
  act(() => {
    updateNeedsYouPage(0, { stale: true, loading: true });
    fireEvent.click(row);
  });

  expect(paletteStore.getState().open).toBe(true);
  expect(window.location.pathname).toBe("/");
});

test("a newer route wins over a needs-you selection deferred until the real modal closes", async () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const selected = screen.getByRole("option", { name: /Session A/i });
  expect(selected.getAttribute("data-kind")).toBe("needsYou");
  expect(selected.getAttribute("data-ref")).toBe("local:ny1");
  const historyLength = window.history.length;

  act(() => {
    fireEvent.click(selected);
    navigate("/settings");
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(window.location.pathname).toBe("/settings"));
  expect(window.location.pathname).toBe("/settings");
  expect(window.history.length).toBe(historyLength + 1);
});

test("an explicitly captured needs-you ref is rejected when its authoritative page turns stale before modal close", async () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const selected = screen.getByRole("option", { name: /Session A/i });
  const selectedRef = selected.getAttribute("data-ref");
  expect(selectedRef).toBe("local:ny1");
  const historyLength = window.history.length;

  act(() => {
    fireEvent.click(selected);
    const resourceKey = keyID({ kind: "section", section: "needs_you", offset: 0, limit: 50 });
    const resource = navigationStore.getState().resources.get(resourceKey);
    if (!resource) throw new Error("needs-you selection page missing");
    navigationStore.setState({
      resources: new Map(navigationStore.getState().resources).set(resourceKey, { ...resource, stale: true }),
    });
  });

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
});

test("a deferred needs-you selection does not survive a later palette activation", async () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const selected = screen.getByRole("option", { name: /Session A/i });
  const historyLength = window.history.length;

  act(() => {
    fireEvent.click(selected);
    openPalette();
  });
  act(() => closePalette());

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
});

let setOtherModalOpen: (open: boolean) => void = () => {};
function OtherModalHost() {
  const [otherOpen, setOtherOpen] = useState(false);
  setOtherModalOpen = setOtherOpen;
  return (
    <Dialog open={otherOpen} onClose={() => setOtherOpen(false)} title="Other modal">
      other
    </Dialog>
  );
}

// React 18 attaches at the app root container, so this document-level bubble
// listener runs after the palette's click handler and mounts the later modal
// before the accepted controller's MutationObserver microtask sees the close.
function openOtherModalAfterClick(): void {
  const openLater = () => {
    document.removeEventListener("click", openLater);
    setOtherModalOpen(true);
  };
  document.addEventListener("click", openLater);
}

// The whole-shell review reported that a pending deferred selection might
// survive a host transition. A host switch replaces host-scoped panes, which
// is a workspace write; the deferral already cancels on any such write.
test("a deferred needs-you selection does not survive a host-transition workspace rewrite", async () => {
  setNeedsYouRows(needsYouRows());
  focusSession("ref_source");
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  act(() => setOtherModalOpen(true));
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const historyLength = window.history.length;
  const selected = screen.getByRole("option", { name: /Session A/i });

  act(() => {
    fireEvent.click(selected);
    // Same workspace owner, new host-scoped panes: a host transition's write,
    // not a viewport variant switch.
    workspaceStore.setState({
      panes: [
        {
          id: "pane_host_switch",
          type: "session",
          params: { ref: "remote:after-host-switch" },
          slot: "main",
        },
      ],
      focusedPaneId: "pane_host_switch",
    });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
  expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();

  act(() => setOtherModalOpen(false));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
  expect(currentAttentionEntryToken()).toBeNull();
  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "remote:after-host-switch" });
});

test("the next-needs-you command defers through the same workspace cancellation", async () => {
  setNeedsYouRows(needsYouRows());
  focusSession("ref_source");
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  act(() => setOtherModalOpen(true));
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    openPalette("/next-needs-you");
    await Promise.resolve();
  });
  const historyLength = window.history.length;

  // The command runs behind the pre-existing dialog, exactly like the row.
  act(() => {
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Command palette" }), { key: "Enter" });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
  expect(window.location.pathname).toBe("/");

  act(() => {
    workspaceStore.setState({
      panes: [
        {
          id: "pane_host_switch",
          type: "session",
          params: { ref: "remote:after-host-switch" },
          slot: "main",
        },
      ],
      focusedPaneId: "pane_host_switch",
    });
  });
  act(() => setOtherModalOpen(false));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
  expect(currentAttentionEntryToken()).toBeNull();
});

test("a deferred needs-you selection does not survive a later unrelated modal", async () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const selected = screen.getByRole("option", { name: /Session A/i });
  const historyLength = window.history.length;

  act(() => {
    openOtherModalAfterClick();
    fireEvent.click(selected);
  });
  // Let the accepted controller observe the palette close while the later
  // modal is still mounted.
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
  expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();

  act(() => setOtherModalOpen(false));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
});

test("a needs-you selection made from inside an open dialog survives until that dialog closes", async () => {
  setNeedsYouRows(needsYouRows());
  focusSession("ref_source");
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  // The user is already inside a dialog when the palette-open chord fires.
  act(() => setOtherModalOpen(true));
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const historyLength = window.history.length;

  act(() => {
    fireEvent.click(screen.getByRole("option", { name: /Session A/i }));
  });
  // The palette is gone, but the pre-existing dialog is still up: the selection
  // is legitimately deferred until that dialog closes.
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
  expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);

  act(() => setOtherModalOpen(false));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Any1"));
  expect(window.history.length).toBe(historyLength + 1);
  expect(currentAttentionEntryToken()).not.toBeNull();
});

test("a modal that opens and closes while a selection is pending cancels it without a later resurrection", async () => {
  setNeedsYouRows(needsYouRows());
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const historyLength = window.history.length;
  const transient = document.createElement("div");
  transient.setAttribute("role", "dialog");
  transient.setAttribute("aria-modal", "true");

  act(() => {
    fireEvent.click(screen.getByRole("option", { name: /Session A/i }));
    // The later modal opens and closes before the controller's observer runs.
    document.body.append(transient);
    transient.remove();
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);

  // A later unrelated controller emission must not fire the cancelled intent.
  act(() => {
    const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
    const resource = navigationStore.getState().resources.get(keyID(key));
    if (!resource) throw new Error("needs-you selection page missing");
    navigationStore.setState({
      resources: new Map(navigationStore.getState().resources).set(keyID(key), { ...resource, loading: true }),
    });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);
});

test("an aria-modal that flips true and back while a selection is pending cancels it", async () => {
  setNeedsYouRows(needsYouRows());
  focusSession("ref_source");
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  act(() => setOtherModalOpen(true));
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const historyLength = window.history.length;
  const flip = document.createElement("div");
  document.body.append(flip);

  try {
    act(() => {
      fireEvent.click(screen.getByRole("option", { name: /Session A/i }));
      // The newer dialog flips aria-modal on and back in place. By the time the
      // controller's observer runs the element reads "false", and the older
      // dialog is what still blocks the selection, so only the counter can
      // record that a newer modal intent came and went.
      flip.setAttribute("aria-modal", "true");
      flip.setAttribute("aria-modal", "false");
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole("dialog", { name: "Other modal" })).toBeTruthy();
    expect(window.location.pathname).toBe("/");
    expect(window.history.length).toBe(historyLength);

    // The older dialog's close must not run the superseded selection.
    act(() => setOtherModalOpen(false));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await act(async () => {
      await Promise.resolve();
    });
    expect(window.location.pathname).toBe("/");
    expect(window.history.length).toBe(historyLength);
    expect(currentAttentionEntryToken()).toBeNull();
  } finally {
    flip.remove();
  }
});

test("a deliberate needs-you selection still opens after the superseding modal closes", async () => {
  setNeedsYouRows(needsYouRows());
  focusSession("ref_source");
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <OtherModalHost />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  const historyLength = window.history.length;

  act(() => {
    openOtherModalAfterClick();
    fireEvent.click(screen.getByRole("option", { name: /Session A/i }));
  });
  await act(async () => {
    await Promise.resolve();
  });
  act(() => setOtherModalOpen(false));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyLength);

  act(() => openPalette());
  await act(async () => {
    fireEvent.click(screen.getByRole("option", { name: /Session B/i }));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Any2"));
  expect(window.history.length).toBe(historyLength + 1);
  expect(currentAttentionEntryToken()).not.toBeNull();
});

test("the empty-query view keeps its old (empty) behavior when there are no needs-you sessions", () => {
  setNeedsYouRows(null);
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());

  expect(screen.queryByRole("option")).toBeNull();
});

// The manifest count refreshes on its own schedule, so a settled empty page
// must end demand even while the manifest still reports rows (#4958's
// zero-row path asked from the count alone).
test("the empty-query view does not demand a page once a settled page owns completion", async () => {
  setNeedsYouRows([]);
  navigationStore.setState({
    manifest: {
      key: { kind: "manifest" },
      data: manifest({
        sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
      }),
      loadedRevision: 1,
      targetRevision: null,
      forceToken: 0,
      etag: "etag",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    },
  });
  const loadSection = vi.fn();
  navigationStore.setState({ loadSection });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => {
    openPalette();
    await Promise.resolve();
  });
  expect(loadSection).not.toHaveBeenCalled();
});

// --- mode machine ---

test("opening seeded with '/' lands in command-filter mode and lists commands", () => {
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));
  expect(screen.getByRole("option", { name: /New session/ })).toBeTruthy();
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
});

test("a '/set' filter narrows to matching commands and drops non-matches", () => {
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/set"));
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
  expect(screen.queryByRole("option", { name: /New session/ })).toBeNull();
});

test("an async catalog refresh rerenders the open session palette - a newly-loaded plugin command now surfaces the handoff row", async () => {
  const fake = new FakeClient();
  fake.on("evener/command/list", async () => {
    await Promise.resolve();
    return { commands: [{ name: "review", pluginName: "p", source: "plugin" }] };
  });
  connectionStore.setState({ client: fake as never });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );

  act(() => openPalette("/review"));

  await waitFor(() => expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy());
});

test("the palette scopes catalog commands to active diagnostics without mutating the global catalog", async () => {
  const user = userEvent.setup();
  useCommandCatalog.setState({
    commands: [
      { name: "review", pluginName: "enabled", source: "plugin" },
      { name: "secret", pluginName: "excluded", source: "plugin" },
      { name: "whoami", source: "user" },
    ],
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "enabled" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );

  act(() => openPalette("/review"));
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();

  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/secret");
  expect(screen.queryByRole("option", { name: /Continue in the composer/ })).toBeNull();

  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/whoami");
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();

  act(() => {
    threadsStore.setState({
      threads: new Map([["ref_a", testModel({ ref: "ref_a" })]]),
    });
  });
  await waitFor(() => expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy());
  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/review");
  expect(screen.queryByRole("option", { name: /Continue in the composer/ })).toBeNull();
  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/whoami");
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
  expect(useCommandCatalog.getState().commands.map((command) => command.name)).toEqual(["review", "secret", "whoami"]);
});

test("selecting a free-arg APP-GLOBAL command enters args mode with a pill and placeholder, and Esc backs out (does not close)", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/spawn"));
  const filterPlaceholder = screen.getByRole("combobox").getAttribute("placeholder");
  await user.click(screen.getByRole("option", { name: /Start with prompt/ }));

  // Args mode shows the command pill (with its back control), and the input
  // placeholder switches to the command's own argument prompt.
  expect(screen.getByRole("button", { name: "back to command list" })).toBeTruthy();
  expect(screen.getByRole("combobox").getAttribute("placeholder")).not.toBe(filterPlaceholder);

  await user.keyboard("{Escape}");
  // Backed out to command-filter, dialog still open (Esc did not close it).
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "back to command list" })).toBeNull();
  expect(screen.getByRole("option", { name: /Start with prompt/ })).toBeTruthy();
});

// --- execution & error surfacing ---

test("running a navigation command closes the palette and navigates", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/new"));
  await user.click(screen.getByRole("option", { name: /New session/ }));
  expect(window.location.pathname).toBe("/new");
  expect(screen.queryByRole("dialog")).toBeNull();
});

// --- session-scoped commands: delisted, handed off to the composer
// (2026-08-14 decision: "the palette is where you go; the composer is where
// you act on this session") ---
//
// The palette itself is now capability-agnostic for these - it only ever
// detects a NAME prefix match, never touches ThreadCapabilities, and never
// makes a wire call for one. Whether a specific built-in can actually run
// (an ended session, a false capability flag, no active turn) is entirely
// the composer's own concern now - see builtinCommand.test.ts's own coverage
// of that (runBuiltinCommand's unavailableReason / blocked-sentinel cases).

test("a session-scoped built-in (/interrupt) is never listed as a runnable command row - only the ONE handoff row", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  focusSession("ref_a", { status: { type: "idle" }, activeTurnId: undefined });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  await act(async () => openPalette("/interrupt"));

  expect(screen.queryByRole("option", { name: /Interrupt agent/ })).toBeNull();
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(false);
});

test("with a focused session, activating the handoff row inserts the typed text into that session's composer and closes", async () => {
  const user = userEvent.setup();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/interrupt"));

  await user.click(screen.getByRole("option", { name: /Continue in the composer/ }));

  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/interrupt ");
  expect(insert.current?.placement).toBe("prefix");
  const { result: focus } = renderHook(() => useComposerFocusRequest("ref_a"));
  expect(focus.current).not.toBeUndefined();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("with NO focused session, the handoff row explains that instead and Enter shows an inline error, not an insert", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/interrupt"));

  const row = screen.getByRole("option", { name: /Focus a session to run this command/ });
  expect(row.getAttribute("aria-disabled")).toBe("true");

  await user.keyboard("{Enter}");

  expect(screen.getByRole("alert")).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy(); // stays open, same as any other blocked action
});

test("the handoff row appears for /model even on an ended session with false capability flags - the palette does not gate on either", () => {
  focusSession("ref_a", {
    status: { type: "ended" },
    capabilities: { ...CAPS, steer: false, interrupt: false, queue: false },
  });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/model"));

  expect(screen.queryByRole("option", { name: /Switch model/ })).toBeNull();
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
});

test("a failed auto-resume is attributed to the session, not blamed on the command", () => {
  // The hub returns the spawner's own raw text under evenerErrorInfo hubLaunch
  // when the resume behind a cold-session mutation fails; unprefixed it reads
  // as the command having failed, so the palette frames it and keeps the
  // spawner's text inside.
  const raw = "fork/exec evener: no such file";
  const launchFailure = commandErrorMessage(new WireError(raw, -32014, { evenerErrorInfo: "hubLaunch" }));
  expect(launchFailure).not.toBe(raw);
  expect(launchFailure).toContain(raw);
  // Every other rejection is passed through untouched.
  expect(commandErrorMessage(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
});

// UX fix: HELP_ROWS was stale, hand-rolled, and mono-typeface (the design
// bar bans mono for chrome labels). It's now {keys, desc}[] rendered through
// the shared KeyHint widget (Mod resolves to the reviewing platform's own
// symbol), covering every real chord across the app - not just the
// palette's own - including ones the old table never had at all (Mod+I,
// Mod+J, Mod+', ask dock's Mod+Enter and arrow-key navigation) and the
// composer's Enter/Shift+Enter/Mod+Enter, checked against Composer.tsx's
// actual keydown routing (handleKeyDown). The descriptions are copy, reviewed
// in the diff; these tests pin which chords the legend teaches.
async function openHelpFromCommand(user: ReturnType<typeof userEvent.setup>) {
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
}

function chordCount(keys: string[]): number {
  return helpChords().filter((chord) => chord === chordLabel(keys)).length;
}

test("/help renders through KeyHint, with the platform's own Mod glyph", async () => {
  const user = userEvent.setup();
  Object.defineProperty(window.navigator, "platform", { value: "Win32", configurable: true });
  await openHelpFromCommand(user);

  expect(paletteSection("help")).not.toBeNull();
  // KeyHint's own bordered form: one <kbd> per key, not a hand-rolled string.
  expect(helpChords()).toContain("Ctrl+K");
});

test("/help pairs each chord with its action as a term and its definition", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  const terms = screen.getAllByRole("term");
  expect(terms.length).toBeGreaterThan(0);
  expect(screen.getAllByRole("definition")).toHaveLength(terms.length);
  for (const term of terms) expect(term.querySelector("kbd")).not.toBeNull();
});

test("/help shows a rebound app-global chord and drops an unbound one", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);
  expect(chordCount(["Mod", "I"])).toBe(1);
  expect(chordCount(["Mod", "J"])).toBe(1);

  act(() => {
    rebindAction(keybindingsRegistry, ACTIONS.composerFocus, "Control+Shift+Y");
    rebindAction(keybindingsRegistry, ACTIONS.nextNeedsYou, null);
  });

  expect(helpChords()).toContain("Ctrl+Shift+Y");
  expect(chordCount(["Mod", "I"])).toBe(0);
  expect(chordCount(["Mod", "J"])).toBe(0);
  act(() => {
    restoreDefaultBinding(keybindingsRegistry, ACTIONS.composerFocus);
    restoreDefaultBinding(keybindingsRegistry, ACTIONS.nextNeedsYou);
  });
});

test("/help keeps the press boundaries of a multi-press rebinding", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);

  act(() => rebindAction(keybindingsRegistry, ACTIONS.composerFocus, "Control+Y Control+W"));

  const term = screen.getAllByRole("term").find((el) => el.textContent === "Ctrl+Y");
  if (term === undefined) throw new Error("first press is not the term");
  const definition = term.nextElementSibling;
  // The second press is its own keycap group, not run on to the first with "+".
  expect(definition?.querySelectorAll("kbd")).toHaveLength(2);
  expect(chordCount(["Ctrl", "Y", "Ctrl", "W"])).toBe(0);
  act(() => restoreDefaultBinding(keybindingsRegistry, ACTIONS.composerFocus));
});

test("/help updates a sequence with repeated presses when it is rebound live", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);

  act(() => rebindAction(keybindingsRegistry, ACTIONS.composerFocus, "Control+Y Control+W Control+W"));
  act(() => rebindAction(keybindingsRegistry, ACTIONS.composerFocus, "Control+Y Control+W Control+X"));

  const term = screen.getAllByRole("term").find((el) => el.textContent === "Ctrl+Y");
  if (term === undefined) throw new Error("first press is not the term");
  const pressed = [...(term.nextElementSibling?.querySelectorAll("kbd") ?? [])].map((kbd) => kbd.textContent);
  expect(pressed).toEqual(["Ctrl", "W", "Ctrl", "X"]);
  act(() => restoreDefaultBinding(keybindingsRegistry, ACTIONS.composerFocus));
});

test("/help lists the UX-fix chords the old table never had", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);

  for (const keys of [["Mod", "B"], ["Mod", "I"], ["Mod", "J"], ["Mod", "'"], ["←"], ["→"]]) {
    expect(chordCount(keys), keys.join("+")).toBe(1);
  }
});

// Composer.tsx's own handleKeyDown: plain Enter (or Mod+Enter, always)
// submits (send, or queue when the agent is mid-turn); Shift+Enter only
// steers when Enter-to-send is OFF (with it on, Shift+Enter is a literal
// newline). The palette's own search-result rows use the same three chords
// (open, open in a new tab, jump to a turn), so each appears once for the
// palette and once for the composer - and Mod+Enter a third time for the ask
// dock's answer/approve (ask dock questions and sandbox approvals).
test("/help lists Enter, Shift+Enter, and Mod+Enter for the composer and Mod+Enter for the ask dock", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);

  expect(chordCount(["Enter"])).toBe(2);
  expect(chordCount(["Shift", "Enter"])).toBe(2);
  expect(chordCount(["Mod", "Enter"])).toBe(3);
});

// "/" on an empty composer opens the composer's own INLINE slash-command menu
// (Composer.tsx's own handleKeyDown), and "/" in the palette filters
// app-global commands, handing a session command off to the composer
// (2026-08-14). The legend teaches both, so a user hunting for /goal or
// /model in the palette's "/" filter learns why it isn't there.
test("/help lists '/' for both the palette's command filter and the composer's inline menu", async () => {
  const user = userEvent.setup();
  await openHelpFromCommand(user);

  expect(chordCount(["/"])).toBe(2);
});

test("the help panel is inert: ArrowDown+Enter never fires a hidden registry command (§2.8)", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  // No session focused -> command-filter with the empty "/" filter lists the
  // global commands. Showing help must not leave that list navigable
  // underneath, or ArrowDown+Enter fires a hidden one invisibly (with no
  // client it blocks, surfacing the error strip).
  act(() => openPalette("/"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
  await user.keyboard("{ArrowDown}{Enter}");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(paletteSection("help")).not.toBeNull();
});

test("typing after /help leaves the help panel and returns to a real command list (§2.8)", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
  await user.type(screen.getByRole("combobox"), "settings");
  expect(paletteSection("help")).toBeNull();
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
});

// --- search mode ---

test("in-session search scans the focused ThreadModel's turns", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", {
    turns: [turn([item("i0", "nothing relevant")]), turn([item("i1", "please investigate the frobnitz")])],
  });

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "frobnitz");

  await waitFor(() => expect(paletteSection("in-session")).not.toBeNull());
  const rows = paletteRowsOfKind("insession");
  expect(rows).toHaveLength(1);
  // The match sits in the second turn; the fixture text carries no digits.
  expect(rows[0]?.textContent).toContain("2");
});

// FIX 2 (real-user report): a user hunting for the keyboard shortcut legend
// tried typing "?" in the palette (not "/help" or "/?" - a bare "?", the
// conventional "show me help" gesture) and, since "?" alone stays in search
// mode (mode.ts's computeMode only switches on a leading "/"), it searched
// live/past sessions for a literal "?" and found nothing. Typing "?" now
// opens the same help view HELP_ROWS renders for "/help", directly from
// search mode, with no "/" prefix required.
test('typing a bare "?" in search mode opens the keyboard-shortcuts help view', async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());

  await user.type(screen.getByRole("combobox"), "?");

  expect(paletteSection("help")).not.toBeNull();
});

test('typing past a bare "?" leaves the help view and resumes filtering, same as after /help', async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "?");
  expect(paletteSection("help")).not.toBeNull();

  await user.type(screen.getByRole("combobox"), "x");

  expect(paletteSection("help")).toBeNull();
});

// --- remote search results ---

// Remote search waits out the palette's search debounce. Fake timers own that
// clock, and the stubbed `jest` global lets Testing Library's waitFor polls
// advance it, so the debounce costs a poll rather than real time.
describe("remote search results", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("search mode renders Live and Past sections from AppWire with highlighting", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [
        { id: "local:a", ref: "local:local:a", title: "frobnitz worker", project: "proj", state: "active", age: "now" },
      ],
      past: [{ id: "p1", ref: "local:p1", title: "old frobnitz run", project: "old", state: "ended", age: "2h" }],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");

    await waitFor(() => expect(paletteSection("live")).not.toBeNull());
    expect(paletteSection("past")).not.toBeNull();
    expect(paletteRowsOfKind("past")).toHaveLength(1);
    const [live] = paletteRowsOfKind("live");
    expect(live?.textContent).toContain("frobnitz worker");
    // The matched substring is wrapped in <mark> (§2.3 highlighting).
    expect(within(live as HTMLElement).getByText("frobnitz").tagName).toBe("MARK");
  });

  // --- grouped subagent hits ---

  // A matching subagent nests under its top-level session's row. The parent
  // here has no row of its own, so the palette synthesizes a greyed anchor
  // that still opens the parent session, and the Past count counts matches,
  // not anchors.
  test("a matching subagent nests under a greyed synthesized anchor", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [],
      past: [
        {
          id: "sub1",
          ref: "local:sub1",
          title: "delegate frobnitz",
          project: "proj",
          state: "ended",
          age: "2h",
          topLevel: { id: "root1", ref: "local:root1", title: "Alpha root", state: "ended", age: "3h" },
        },
      ],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");

    await waitFor(() => expect(paletteSection("past")).not.toBeNull());
    // The header's words are copy; its trailing count is the behavior.
    expect(paletteSection("past")?.textContent).toMatch(/1$/);
    const indirect = paletteIndirectRows();
    const nested = paletteNestedRows();
    expect(indirect).toHaveLength(1);
    expect(nested).toHaveLength(1);
    // The anchor comes first, the subagent nests under it.
    expect(indirect[0]?.textContent).toContain("Alpha root");
    expect(indirect[0]?.compareDocumentPosition(nested[0] as Node)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(nested[0]?.textContent).toContain("delegate frobnitz");
  });

  test("Enter on a greyed anchor opens the parent session; ArrowDown then Enter opens the subagent", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [],
      past: [
        {
          id: "sub1",
          ref: "local:sub1",
          title: "delegate frobnitz",
          project: "proj",
          state: "ended",
          age: "2h",
          topLevel: { id: "root1", ref: "local:root1", title: "Alpha root", state: "ended", age: "3h" },
        },
      ],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");
    await waitFor(() => expect(paletteIndirectRows()).toHaveLength(1));

    // The active row is the greyed anchor: it opens the parent, not the child.
    await user.keyboard("{Enter}");
    expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:root1");

    // Walk down to the nested child: it opens the subagent's own session.
    window.history.replaceState(null, "", "/");
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");
    await waitFor(() => expect(paletteNestedRows()).toHaveLength(1));
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:sub1");
  });

  // A live parent that matched keeps one row per session: its ended
  // subagent's hits nest under its Live row, and the Past section carries
  // nothing for the group.
  test("a matched live parent nests its subagent hits under its Live row", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [{ id: "root1", ref: "local:root1", title: "frobnitz root", project: "proj", state: "active", age: "now" }],
      past: [
        {
          id: "sub1",
          ref: "local:sub1",
          title: "delegate",
          project: "proj",
          state: "ended",
          age: "2h",
          topLevel: { id: "root1", ref: "local:root1", title: "frobnitz root", state: "active", age: "now" },
        },
      ],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");

    await waitFor(() => expect(paletteSection("live")).not.toBeNull());
    expect(paletteSection("past")).toBeNull();
    const nested = paletteNestedRows();
    expect(nested).toHaveLength(1);
    expect(nested[0]?.textContent).toContain("delegate");
    // The nested child sits directly under its live anchor.
    const live = paletteRowsOfKind("live");
    expect(live).toHaveLength(1);
    expect(live[0]?.compareDocumentPosition(nested[0] as Node)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    // The pulled-into-Live layout is keyboard-walked: ArrowDown lands on the
    // nested child, and Enter opens the subagent's own session.
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:sub1");
  });

  // An older hub answers without topLevel: rows render exactly as before,
  // flat, and the counts stay the response's own.
  test("a topLevel-less response renders flat rows as before", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [{ id: "l1", ref: "local:l1", title: "frobnitz live", project: "proj", state: "active", age: "now" }],
      past: [
        { id: "p1", ref: "local:p1", title: "frobnitz one", project: "old", state: "ended", age: "2h" },
        { id: "p2", ref: "local:p2", title: "frobnitz two", project: "old", state: "ended", age: "4h" },
      ],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "frobnitz");

    await waitFor(() => expect(paletteSection("past")).not.toBeNull());
    expect(paletteSection("past")?.textContent).toMatch(/2$/);
    expect(paletteRowsOfKind("past")).toHaveLength(2);
    expect(paletteNestedRows()).toHaveLength(0);
    expect(paletteIndirectRows()).toHaveLength(0);
  });

  // Typed against the real SearchResult, not Record<string, unknown>: `ref` is
  // required now (see search.ts), and a fixture omitting it would be describing
  // a response the hub cannot produce.
  async function searchLiveRow(user: ReturnType<typeof userEvent.setup>, result: SearchResult, term: string) {
    scriptSearch({ live: [result], past: [] });
    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), term);
    await waitFor(() => expect(paletteSection("live")).not.toBeNull());
    return screen.getAllByRole("option").find((o) => o.textContent?.includes(term)) as HTMLElement;
  }

  // One ref form, and the type is what enforces it: a hit with no ref is not a
  // state this code can be in, so there is no runtime fallback to test. The hub
  // sets Ref at both construction sites and its field carries no omitempty.
  test("a search result is opened by its qualified ref", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const row = await searchLiveRow(
      user,
      { id: "bare123", ref: "local:qualified", title: "reffuls", project: "p", state: "active", age: "now" },
      "reffuls",
    );
    await user.click(row);
    expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:qualified");
  });

  // An approval blocks its turn mid-tool, so the result's wire state stays
  // "active": the dot must read approval_pending, not the state alone, the
  // same rule #2560 gave the rail's needs-you dot (#2567).
  test("a live search result waiting on an approval shows a needs-you dot, not a working one", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const live = await searchLiveRow(
      user,
      {
        id: "local:a",
        ref: "local:local:a",
        title: "frobnitz worker",
        project: "proj",
        state: "active",
        age: "now",
        approvalPending: true,
      },
      "frobnitz",
    );
    expect(within(live).getByRole("img", { name: "Needs you" })).toBeTruthy();
  });
});

// --- keyboard navigation ---

test("ArrowDown moves the active row (aria-selected) with wraparound", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));
  const options = screen.getAllByRole("option");
  expect(options[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(screen.getAllByRole("option")[1]?.getAttribute("aria-selected")).toBe("true");
  expect(screen.getAllByRole("option")[0]?.getAttribute("aria-selected")).toBe("false");
});

test("Enter on an exact app-global command name runs it", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/settings"));

  await user.keyboard("{Enter}");

  expect(window.location.pathname).toBe("/settings");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on an exact session-scoped command name (/status) hands off to the composer instead of running it", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/status"));

  await user.keyboard("{Enter}");

  // /status used to toggle the sessionDetails workspace pane directly - the
  // palette no longer runs ANY session command itself (2026-08-14 decision).
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", { ref: "ref_a" })).toBe(false);
  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/status ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a fuzzy near-miss of a session-scoped command name (/stat) still hands off, not a raw send", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }, { name: "q" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/stat"));

  await user.keyboard("{Enter}");

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/stat ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on an unknown slash command sends the raw query - the escape hatch for genuinely unrecognized text", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a");
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/frobnicate main"));

  await user.keyboard("{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/frobnicate main");
  expect(screen.queryByRole("dialog")).toBeNull();
});

// RoboRev Low (PR 1393 fresh review, 0708b9b): the slash fallthrough fired the
// raw text at the focused session with `void` - on a recovery-fenced local
// session the store's shared admission refuses the send, and the unhandled
// rejection closed the palette and silently lost the typed input. The
// fallthrough must keep the composer's failure contract for a refused send:
// surface why the text went nowhere (the fenced admission's own refusal among
// the reasons) and keep the palette open so the input survives.
test("Enter on an unknown slash command against a fenced session toasts the refusal and keeps the palette open", async () => {
  const user = userEvent.setup();
  focusSession("local:ref_a");
  // A Stop this page started is its own fence while it drains: the hub holds
  // Stopping > 0 and refuses turn/start even though the snapshot reads idle.
  // This is deliberately NOT the Send-resumes face (which excludes a draining
  // Stop), so the fallthrough's refusal path is what runs here.
  act(() => {
    threadsStore.setState((state) => ({ stoppingRefs: new Set(state.stoppingRefs).add("local:ref_a") }));
  });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  const send = vi.spyOn(threadsStore.getState(), "send");
  act(() => openPalette("/frobnicate main"));

  await user.keyboard("{Enter}");

  // The admission's own refusal surfaces as an error toast, the palette stays
  // open with the typed text, and the rejection never escapes unhandled.
  const refusal: Error = await send.mock.results[0]?.value.catch((err: Error) => err);
  expect(refusal).toBeInstanceOf(Error);
  await waitFor(() => expect(getToasts()).toEqual([expect.objectContaining({ kind: "error", text: refusal.message })]));
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect((screen.getByRole("combobox") as HTMLInputElement).value).toBe("/frobnicate main");
});

// R09: the palette's slash fallthrough is one of the direct callers of the
// shared store send, so a Send-resumes fenced ref inherits the whole journey
// here. The raw text parks durably, the store drives the resume, and the parked
// turn/start drains once after the resume's hydration reconciles the uncertain
// row. The palette closes on the durable accept, before the resume lands.
test("Enter on an unknown slash command against a Send-resumes fenced session parks the send, drives the resume, closes the palette, and drains once", async () => {
  globalThis.indexedDB = new IDBFactory();
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:ref_a";
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  let resumed = false;
  let uncertain = "";
  const fenced = () =>
    wireThread(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPS, send: false, queue: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  // The resumed, authoritative snapshot names the uncertain row as accepted,
  // which reconciles it.
  const idle = () =>
    wireThread(ref, {
      status: { type: "idle" },
      evener: {
        ref,
        capabilities: CAPS,
        mutationStateAuthoritative: true,
        queue: { revision: 1, clientMutationIds: [uncertain] },
      },
    });
  fake.on(
    "thread/read",
    ({ requestGeneration }) =>
      ({
        thread: resumed ? idle() : fenced(),
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 0 },
        requestGeneration,
      }) satisfies ThreadReadResponse,
  );
  let resumeRequested!: () => void;
  const requested = new Promise<void>((resolve) => {
    resumeRequested = resolve;
  });
  let resolveResume!: (response: ThreadReadResponse) => void;
  const heldResume = new Promise<ThreadReadResponse>((resolve) => {
    resolveResume = resolve;
  });
  fake.on("thread/resume", () => {
    resumeRequested();
    return heldResume;
  });
  let starts = 0;
  fake.on("turn/start", (params) => {
    starts += 1;
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: `thr_${ref}`,
        projectionState: "reflected",
      },
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  // Track and hydrate the fenced ref: the fallthrough reads the focused pane's
  // session, and the driver's refreshThread needs a tracked ref to re-read.
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: "p1",
  });
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  await act(async () => {
    const row = await storage.enqueueIntent({
      targetRef: ref,
      threadId: `thr_${ref}`,
      method: "turn/queue",
      payload: { ref, input: [{ type: "text", text: "uncertain" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "uncertain" }] },
    });
    uncertain = row.clientMutationId;
    await storage.markUnknown(uncertain, "blockedUnknown");
    await refreshPendingTurnsProjection(ref);
  });
  await flushPendingTurnsProjectionForTests();
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <Toast />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/frobnicate main"));
  await user.keyboard("{Enter}");
  // The durable accept closes the palette before the resume lands.
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await act(async () => {
    await requested;
  });
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  // The row is parked: nothing reached the wire while the resume was held.
  expect(starts).toBe(0);
  await act(async () => {
    resumed = true;
    resolveResume({ thread: idle() });
  });
  await flushPendingTurnsProjectionForTests();
  expect(threadsStore.getState().resumeFailures.size).toBe(0);
  // The resume RPC starts an entry read, whose authoritative reconciliation
  // clears the fence asynchronously, after pending-turn projection can settle.
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false));
  await waitFor(() => expect(starts).toBe(1));
  expect(starts).toBe(1);
});

// The failure arm: the palette still closes on the durable accept (the row is
// parked and Send remains the single action), and the resume's rejection
// surfaces through the composer's toast, which names the reason.
test("a Send-resumes fallthrough whose resume fails parks the row, closes the palette, and toasts through the composer", async () => {
  globalThis.indexedDB = new IDBFactory();
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:ref_fail";
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on(
    "thread/read",
    () =>
      ({
        thread: wireThread(ref, {
          status: { type: "notLoaded" },
          evener: {
            ref,
            capabilities: { ...CAPS, send: false, queue: false },
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
      }) satisfies ThreadReadResponse,
  );
  fake.on("thread/resume", () => {
    throw new Error("resume rejected");
  });
  let starts = 0;
  fake.on("turn/start", (params) => {
    starts += 1;
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: `thr_${ref}`,
        projectionState: "reflected",
      },
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: "p1",
  });
  await threadsStore.getState().ensureThread(ref);
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/frobnicate main"));
  await user.keyboard("{Enter}");
  await waitFor(() =>
    expect(getToasts()).toContainEqual(
      expect.objectContaining({ kind: "error", text: expect.stringContaining("resume rejected") }),
    ),
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  await flushPendingTurnsProjectionForTests();
  const rows = await storage.listOutbox(ref);
  expect(rows.map((record) => record.method)).toEqual(["turn/start"]);
  expect(starts).toBe(0);
});

// 2026-08-14: a picked session-scoped command - built-in OR plugin catalog -
// no longer runs from the palette at all (nor, for a plugin command, sends
// its qualified form immediately). Both now resolve to the SAME single
// handoff row, which inserts the RAW TEXT THE USER TYPED (not a resolved
// invocation - sessionScopedHandoffMatch only ever answers "does something
// match", never "which") via the SAME per-ref insert/focus seams
// SelectionQuote's "Quote in reply" already uses (quoteInsert.ts/
// composerFocus.ts). The composer's OWN inline slash menu is what resolves a
// plugin command's qualified "/plugin:name" form, once the user keeps typing
// there (slashCompletion.ts's mergeSlashCommands).

test("selecting a plugin catalog entry's handoff row inserts the raw typed text into the composer instead of sending", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/review"));

  await user.click(screen.getByRole("option", { name: /Continue in the composer/ }));

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/review ");
  // SHOULD-FIX: "prefix", not the default "append" - a slash command only
  // parses at the draft's start, so it must land there even ahead of
  // whatever the user already typed, not after it.
  expect(insert.current?.placement).toBe("prefix");
  const { result: focus } = renderHook(() => useComposerFocusRequest("ref_a"));
  expect(focus.current).not.toBeUndefined();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a plugin command with arguments hands off the FULL typed text, args included", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/review main"));

  await user.keyboard("{Enter}");

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/review main ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("two catalog entries sharing a name still collapse to ONE handoff row, not one per entry", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", pluginName: "p", source: "plugin" },
      { name: "review", pluginName: "q", source: "plugin" },
    ],
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }, { name: "q" }] } });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/review"));

  expect(screen.getAllByRole("option", { name: /Continue in the composer/ })).toHaveLength(1);
});

// Mount the Toast region so any command toast has somewhere to render (not
// asserted here - just ensures no unmounted-region warnings).
test("mounting alongside a Toast region does not throw", () => {
  render(
    <NeedsYouProvider>
      <CommandPalette />
      <Toast />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));
  expect(screen.getByRole("dialog")).toBeTruthy();
});
