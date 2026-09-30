import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { NavigationSessionLocation, Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render as renderUI, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps, ReactElement } from "react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "../../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../../shell/clientContext";
import { resetFocusedActivityScopeForTests } from "../../../shell/focusedSession";
import { registerPaneForTests } from "../../../shell/paneRegistry";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { connectionStore } from "../../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../../stores/navigation/store";
import { activityClient, activityContext, activitySummary } from "../../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { installMobileViewport } from "../testing/mobileViewport";
import "../../sessionPanels";
import { topNotesStore } from "../../../stores/topNotes";
import { type SessionChromePlacement, SessionChrome as SessionChromeView } from "./SessionChrome";
import { TopNotesPanel } from "./TopNotesPanel";

const here = dirname(fileURLToPath(import.meta.url));

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function emptyActivityTree() {
  return {
    revision: 1,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root session",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function locationWithSession(ref: string): NavigationSessionLocation {
  return {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: ref,
    top_level: true,
    tier: "current",
    session: {
      ref,
      host_id: "local",
      session_id: `sess_${ref}`,
      title: `Session ${ref}`,
      project: "",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
    },
  };
}
function setLocation(ref: string): void {
  const key = { kind: "location", ref } as const;
  const data = locationWithSession(ref);
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data,
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

let chromeClient = new FakeClient("ready");

function SessionChrome(props: ComponentProps<typeof SessionChromeView>) {
  return (
    <ClientProvider client={chromeClient}>
      <SessionChromeView {...props} />
    </ClientProvider>
  );
}

function connectFakeClient(): FakeClient {
  const fake = activityClient();
  chromeClient = fake;
  connectionStore.getState().connect(fake);
  return fake;
}

function render(ui: ReactElement) {
  const client = connectionStore.getState().client ?? new FakeClient("ready");
  return renderUI(ui, { wrapper: ({ children }) => <ClientProvider client={client}>{children}</ClientProvider> });
}

beforeEach(() => {
  chromeClient = new FakeClient("ready");
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetTranscriptDisplayStoreForTests();
  topNotesStore.getState().resetForTests();
});

afterEach(() => {
  cleanup();
  resetActivitySidebarStoreForTests();
  resetFocusedActivityScopeForTests();
  // @ts-expect-error jsdom has no matchMedia by default; individual mobile
  // tests install the narrow viewport explicitly.
  delete window.matchMedia;
});

// Wave 5 T1 carved this slot as an empty placeholder ("renders nothing (T1
// placeholder - T5 fills this in)"); this file supersedes that pin now that
// T5 has actually filled it in - see this stream's own report for the
// commit range. SessionChrome's own contract stays exactly what T1 locked
// (`{ ref: string }`, nothing else) - every real prop (model, capabilities,
// ...) is read from the threads store internally, the same way every other
// pane-level component in this app does.

test("renders nothing for a ref with no tracked model yet (defensive - Session.tsx never mounts this before hydration in practice)", () => {
  const { container } = render(<SessionChrome ref="untracked_ref" />);
  expect(container.firstChild).toBeNull();
});

test("composes the status row and the session menu once the ref's thread is tracked", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await settleActivityDiscovery("ref_a");

  // Status row: model chip.
  expect(screen.getByTestId("model-switch-value").textContent).toBe("anthropic/claude-sonnet-4-5");
  // Session menu trigger.
  expect(screen.getByRole("button", { name: /session actions/i })).toBeTruthy();
});

test("composer placement renders one ordered inline status and actions cluster without footer-only controls", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_composer", {
      evener: {
        ref: "ref_composer",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        goal: { status: "active", iterations: 2 },
        contextUsed: 64_000,
        contextWindow: 128_000,
        contextPressure: 0.5,
        reasoningEffort: "medium",
        reasoningEffortLevels: ["low", "medium", "high"],
        supportsReasoning: true,
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_composer");

  render(<SessionChrome ref="ref_composer" placement="composer" />);
  await settleActivityDiscovery("ref_composer");

  const cluster = screen.getByTestId("session-chrome-inline");
  const statusContainer = within(cluster).getByTestId("session-chrome-inline-status");
  const statusRow = within(cluster).getByTestId("status-row");
  const identity = within(cluster).getByTestId("status-row-identity");
  const context = within(cluster).getByTestId("status-row-context");
  const actions = within(cluster).getByRole("button", { name: "Session actions" });
  expect(within(identity).getByTestId("model-switch-trigger")).toBeTruthy();
  expect(within(identity).getByRole("combobox", { name: "Reasoning effort" })).toBeTruthy();
  expect(statusRow.contains(identity)).toBe(true);
  expect(statusRow.contains(context)).toBe(true);
  expect(cluster.children).toHaveLength(2);
  expect(cluster.children[0]).toBe(statusContainer);
  expect(statusContainer.contains(statusRow)).toBe(true);
  expect(statusContainer.contains(actions)).toBe(false);
  expect(cluster.children[1]?.contains(actions)).toBe(true);
  expect(identity.compareDocumentPosition(context) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(context.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(screen.getAllByTestId("status-row")).toHaveLength(1);
  expect(screen.getAllByRole("button", { name: "Session actions" })).toHaveLength(1);
  expect(screen.queryByTestId("session-chrome")).toBeNull();
  expect(within(cluster).queryByTestId("session-chrome-cadence")).toBeNull();
  // The goal chip is gone entirely (Jesse's 2026-09-28 design ruling, "drop
  // goal inline in the composer", taken to its conclusion by deleting the
  // production-dead GoalControl): the goal objective stays visible and
  // editable through the composer's own CurrentWork goal row and its inline
  // /goal built-in.
  expect(screen.queryByTestId("goal-chip-trigger")).toBeNull();
  expect(screen.queryByTestId("goal-compact-trigger")).toBeNull();
});

// The composer's narrow layout relocates Stop and Steer into this menu
// (Jesse's 2026-09-28 ruling on the #1339 phone-width wrap), so the verbs
// must reach it ONLY through the composer placement: the footer and
// menu-only mounts share SessionMenu with the rail, where no draft exists
// for Steer to send.
test("turn verbs ride the composer placement's menu and no other placement's", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_turn"));
  await threadsStore.getState().ensureThread("ref_turn");
  const turnVerbs = {
    stop: { onSelect: () => {} },
    steer: { onSelect: () => {} },
  };

  const expectNoTurnVerbs = async (placement?: SessionChromePlacement) => {
    render(<SessionChromeView ref="ref_turn" placement={placement} turnVerbs={turnVerbs} />);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    expect(screen.queryByRole("menuitem", { name: "Stop" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
    cleanup();
  };

  render(<SessionChromeView ref="ref_turn" placement="composer" turnVerbs={turnVerbs} />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Stop" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Steer" })).toBeTruthy();
  cleanup();

  // The default (footer) and menu-only mounts share SessionMenu with the
  // rail, where no draft exists for Steer to send.
  await expectNoTurnVerbs();
  await expectNoTurnVerbs("menu");
});

test("default placement preserves the standalone session chrome presentation", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_footer"));
  await threadsStore.getState().ensureThread("ref_footer");

  render(<SessionChrome ref="ref_footer" />);
  await settleActivityDiscovery("ref_footer");

  expect(screen.getByTestId("session-chrome")).toBeTruthy();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
});

test("status row has no inline Details/Tasks/Activity/Notes buttons; they live in the menu", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await settleActivityDiscovery("ref_a");

  expect(screen.queryByRole("button", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Tasks/ })).toBeNull();
  expect(screen.queryByRole("button", { name: /Activity/ })).toBeNull();
  expect(screen.queryByRole("button", { name: "Notes" })).toBeNull();

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Details" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Tasks/ })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Activity/ })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Notes" })).toBeTruthy();
});

// Exercise the shared SessionMenu through its real chrome adapter, proving
// capability reaches the menu instead of testing only the menu's boolean prop.
test.each(["desktop", "mobile"] as const)(
  "%s Notes menu must not expose an unsupported blank panel",
  async (viewport) => {
    const restoreViewport = viewport === "mobile" ? installMobileViewport() : () => {};
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_no_notes", {
        evener: { ref: "ref_no_notes", capabilities: { ...CAPABILITIES, sharedNotes: false }, queue: { revision: 0 } },
      }),
    );
    await threadsStore.getState().ensureThread("ref_no_notes");

    try {
      render(<SessionChrome ref="ref_no_notes" placement="composer" />);
      await user.click(screen.getByRole("button", { name: "Session actions" }));
      const opener = screen.queryByRole("menuitem", { name: "Notes" });
      expect.soft(opener).toBeNull();
      if (opener) await user.click(opener);
      expect(topNotesStore.getState().isExpanded("ref_no_notes")).toBe(false);
    } finally {
      restoreViewport();
    }
  },
);

test.each([
  { status: "idle", editable: true },
  { status: "active", editable: true },
  { status: "ended", editable: false },
  { status: "closed", editable: false },
  { status: "notLoaded", editable: false },
] as const)(
  "Notes navigation keeps supported $status content reachable (editable=$editable)",
  async ({ status, editable }) => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_notes", {
        status: { type: status },
        evener: {
          ref: "ref_notes",
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
          humanNote: "human read sentinel",
          agentNote: "agent read sentinel",
          sessionUrls: [{ id: "u1", url: "https://notes.test/read", label: "reference sentinel" }],
        },
      }),
    );
    await threadsStore.getState().ensureThread("ref_notes");

    // Saved notLoaded sessions mount the menu-only placement, not the composer.
    render(<SessionChrome ref="ref_notes" placement={status === "notLoaded" ? "menu" : "composer"} />);
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Notes" }));
    expect(topNotesStore.getState().isExpanded("ref_notes")).toBe(true);
    // Menu invocation requests editor focus too, matching the palette /notes
    // instead of leaving keyboard and mouse openers inconsistent.
    expect(topNotesStore.getState().hasPendingFocus("ref_notes")).toBe(true);

    const model = threadsStore.getState().threads.get("ref_notes")!;
    render(<TopNotesPanel sessionRef="ref_notes" model={model} />);

    expect(screen.getByTestId("shared-notes-agent").textContent).toBe("agent read sentinel");
    expect(screen.getByRole("link", { name: "reference sentinel" }).getAttribute("href")).toBe(
      "https://notes.test/read",
    );
    if (editable) {
      expect((screen.getByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement).value).toBe(
        "human read sentinel",
      );
      expect(screen.getByRole("button", { name: "Remove reference sentinel" })).toBeTruthy();
    } else {
      expect(screen.getByTestId("shared-notes-human").textContent).toBe("human read sentinel");
      expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Remove reference sentinel" })).toBeNull();
    }
    expect(fake.calls.filter((call) => call.method === "notes/human/set" || call.method === "urls/remove")).toEqual([]);
  },
);

test("SessionChrome keeps its actions-menu tasks entry count-free", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_outcomes", {
      evener: {
        ref: "ref_outcomes",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        tasks: { total: 7, done: 1, cancelled: 5, remaining: 1 },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_outcomes");

  render(<SessionChrome ref="ref_outcomes" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Tasks" })).toBeTruthy();
});

test("SessionChrome keeps the menu entry count-free even when an outcome is omitted", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_remaining", {
      evener: {
        ref: "ref_remaining",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        tasks: { total: 7, done: 1, remaining: 5 },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_remaining");

  render(<SessionChrome ref="ref_remaining" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Tasks" })).toBeTruthy();
});

test("desktop Session actions opens the full Verbosity Dialog, persists selection, and restores trigger focus", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_verbosity"));
  await threadsStore.getState().ensureThread("ref_verbosity");

  render(<SessionChrome ref="ref_verbosity" />);

  const actions = screen.getByRole("button", { name: "Session actions" });
  await user.click(actions);
  await user.click(screen.getByRole("menuitem", { name: "Verbosity…" }));

  const dialog = screen.getByRole("dialog", { name: "Verbosity" });
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(
    within(dialog)
      .getAllByRole("radio")
      .map((radio) => radio.textContent),
  ).toEqual(["Chat", "Intent", "Tools", "Activity", "Full", "Custom"]);
  const activity = within(dialog).getByRole("radio", { name: "Activity" });
  await user.click(activity);
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(
    makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }),
  );
  expect(document.activeElement).toBe(activity);

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
  expect(document.activeElement).toBe(actions);

  await user.click(actions);
  await user.click(screen.getByRole("menuitem", { name: "Verbosity…" }));
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
  expect(document.activeElement).toBe(actions);
});

test("desktop Verbosity keeps Edit hub defaults wired to Settings Transcript", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_verbosity_settings"));
  await threadsStore.getState().ensureThread("ref_verbosity_settings");
  window.history.replaceState({}, "", "/");

  render(<SessionChrome ref="ref_verbosity_settings" />);
  await user.click(screen.getByRole("button", { name: "Session actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Verbosity…" }));
  await user.click(screen.getByRole("button", { name: "Edit hub defaults" }));

  expect(window.location.pathname).toBe("/settings/transcript");
});

test("mobile Session actions opens the full Verbosity bottom Sheet", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_verbosity_mobile"));
  await threadsStore.getState().ensureThread("ref_verbosity_mobile");

  try {
    render(<SessionChrome ref="ref_verbosity_mobile" />);
    const actions = screen.getByRole("button", { name: "Session actions" });
    await user.click(actions);
    await user.click(screen.getByRole("menuitem", { name: "Verbosity…" }));

    const sheet = screen.getByRole("dialog", { name: "Verbosity" });
    expect(sheet.className).toContain("bottom");
    expect(within(sheet).getAllByRole("radio")).toHaveLength(6);
    expect(within(sheet).getByText(/^Customize & advanced/)).toBeTruthy();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Verbosity" })).toBeNull();
    expect(document.activeElement).toBe(actions);
  } finally {
    restoreViewport();
  }
});

// Desktop Tasks opens the activity sidebar preselected to its tasks tab (the
// same retarget the rail row and the composer's current-task button share),
// not a workspace pane. Mobile still opens the Sheet (the mobile tasks-panel
// test below keeps that).
test("desktop Tasks menu item opens the activity sidebar on the tasks tab, never a pane", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  await import("../index");
  workspaceStore.getState().openPane("session", { ref: "ref_a" });
  render(<SessionChrome ref="ref_a" />);
  await settleActivityDiscovery("ref_a");
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Tasks/ }));

  expect(activitySidebarStore.getState().open).toBe(true);
  expect(activitySidebarStore.getState().tab).toBe("tasks");
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(false);

  // Idempotent open, like the menu's sibling pane openers: re-selecting keeps
  // the sidebar on the tasks tab and still opens no pane.
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Tasks ✓" }));
  expect(activitySidebarStore.getState().open).toBe(true);
  expect(activitySidebarStore.getState().tab).toBe("tasks");
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionTasks")).toBe(false);
});

test("menu offers Pin/Archive/Delete when the session is in the tree; omits them otherwise", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  setLocation("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await settleActivityDiscovery("ref_a");
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Archive" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Delete…" })).toBeTruthy();
  await user.keyboard("{Escape}");

  // A missing location keeps organization actions absent.
  act(() => resetNavigationStoreForTests());
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Archive" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
});

test("session-menu archive addresses the canonical ref so a remote row's decision sticks", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("host-a:r1"));
  await threadsStore.getState().ensureThread("host-a:r1");
  setLocation("host-a:r1");
  navigationStore.setState({ applyNavigationMutation: vi.fn().mockResolvedValue(undefined) });
  fake.on("evener/archive/set", (params) => {
    // The bare session ID ("sess_host-a:r1") would be stored as this hub's own
    // decision, which the remote row never reads back.
    expect(params).toEqual({ kind: "session", id: "host-a:r1", archived: true });
    return { ok: true, navigation: { generation_id: "generation_test", targets: [] } };
  });

  render(<SessionChrome ref="host-a:r1" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Archive" }));

  expect(fake.calls).toContainEqual({
    method: "evener/archive/set",
    params: { kind: "session", id: "host-a:r1", archived: true },
  });
});

test("session-menu pin assignment uses typed AppWire and converges its navigation receipt", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_pin"));
  fake.on("evener/session-pin/assign", (params) => {
    expect(params).toEqual({ sessionRef: "ref_pin", sectionId: "research" });
    return {
      ok: true,
      changed: true,
      assignment: {
        sessionRef: "local:sess_ref_pin",
        section: { id: "research", name: "Research", memberCount: 1 },
      },
      navigation: {
        generation_id: "generation_test",
        targets: [{ kind: "pin_section", section_id: "research", revision: 2 }],
      },
    };
  });
  await threadsStore.getState().ensureThread("ref_pin");
  setLocation("ref_pin");
  connectionStore.setState({ client: new FakeClient("ready") });
  const pinKey = { kind: "pin_catalog", offset: 0, limit: 100 } as const;
  const pinCatalog = {
    key: pinKey,
    data: {
      generation_id: "generation_test",
      revision: 1,
      pin_sections: [{ id: "research", name: "Research", count: 0 }],
      remaining: 0,
    },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "etag-pins",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  };
  const convergenceOrder: string[] = [];
  const trackPinSection = vi.fn((sectionID: string) => convergenceOrder.push(`track:${sectionID}`));
  const applyNavigationMutation = vi.fn(async () => {
    convergenceOrder.push("apply");
  });
  navigationStore.setState((state) => {
    const resources = new Map(state.resources);
    resources.set(keyID(pinKey), pinCatalog);
    return {
      resources,
      loadPinCatalogPages: vi.fn().mockResolvedValue(undefined),
      trackPinSection,
      applyNavigationMutation,
    };
  });

  render(<SessionChrome ref="ref_pin" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
  await user.click(await screen.findByRole("button", { name: "Research" }));

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/session-pin/assign",
      params: { sessionRef: "ref_pin", sectionId: "research" },
    }),
  );
  expect(applyNavigationMutation).toHaveBeenCalledWith({
    generation_id: "generation_test",
    targets: [{ kind: "pin_section", section_id: "research", revision: 2 }],
  });
  expect(convergenceOrder).toEqual(["track:research", "apply"]);
});

test("menu Shut down is gated on capabilities.shutdown", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, shutdown: false }, queue: { revision: 0 } },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await settleActivityDiscovery("ref_a");
  await user.click(screen.getByRole("button", { name: /session actions/i }));

  expect(screen.getByRole("menuitem", { name: "Shut down" }).getAttribute("aria-disabled")).toBe("true");
});

test("the details panel reads the work time of the SAME ref passed to SessionChrome", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_d", {
      evener: { ref: "ref_d", capabilities: CAPABILITIES, queue: { revision: 0 }, workMillis: 125_000 },
    }),
  );
  await threadsStore.getState().ensureThread("ref_d");

  render(<SessionChrome ref="ref_d" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Details" }));

  expect(screen.getByTestId("session-details-work-time").textContent).toContain("2m");
  restoreViewport();
});

test("every composed piece acts on the SAME ref passed to SessionChrome", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_b", { name: "Session B" }));
  await threadsStore.getState().ensureThread("ref_b");
  let renamedTo: unknown;
  fake.on("evener/thread/name/set", (params) => {
    renamedTo = params;
    return {};
  });

  render(<SessionChrome ref="ref_b" />);

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  const input = dialog.querySelector("input");
  if (!input) throw new Error("rename dialog missing its input");
  await user.clear(input);
  await user.type(input, "New name");
  await user.click(screen.getByRole("button", { name: "Rename" }));

  await waitFor(() => expect(renamedTo).toEqual({ ref: "ref_b", name: "New name" }));
});

test("the tasks panel fetches for the SAME ref passed to SessionChrome", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_c"));
  await threadsStore.getState().ensureThread("ref_c");
  let calledRef: unknown;
  fake.on("evener/tasks/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
  });

  render(<SessionChrome ref="ref_c" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Tasks" }));

  await waitFor(() => expect(calledRef).toBe("ref_c"));
  restoreViewport();
});

// The tasks half of this pair (above) and the activity half join the same two
// facts from opposite ends: ActivityPanel.test.tsx proves the panel fetches for
// whatever sessionRef prop it is HANDED, and this proves SessionChrome hands
// it its own. Neither alone catches a chrome that wires the panel to a wrong
// or stale ref - both files stay green while the sheet quietly reports
// another session's activity.
test("the activity panel fetches for the SAME ref passed to SessionChrome", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_e"));
  await threadsStore.getState().ensureThread("ref_e");
  let calledRef: unknown;
  fake.on("evener/thread/jobs/list", (params) => {
    calledRef = params.ref;
    return {
      context: activityContext(params.ref),
      scope: params.scope ?? "session",
      jobs: [],
      page: { complete: true, issues: [] },
    };
  });

  render(<SessionChrome ref="ref_e" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Activity" }));

  await waitFor(() => expect(calledRef).toBe("ref_e"));
  restoreViewport();
});

// --- panes through the menu (2026-08-05-unified-session-context-menu) --------
//
// Details/Tasks/Activity are the menu's leading group at every width and on
// every host: on desktop Details toggles its workspace pane while Tasks and
// Activity open the activity sidebar, and mobile items open the Sheets
// through the panels' imperative handles (openX branches on isMobile).

test.each([["Details", "sessionDetails"]] as const)(
  "desktop %s menu item opens and closes its pane for the SessionChrome ref",
  async (label, type) => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_inline"));
    fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
    await threadsStore.getState().ensureThread("ref_inline");

    render(<SessionChrome ref="ref_inline" />);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: label }));
    expect(workspaceStore.getState().panes).toContainEqual(
      expect.objectContaining({ type, params: { ref: "ref_inline" } }),
    );

    // The checked adornment is a live toggle, not a label: selecting a checked
    // item must CLOSE its pane.
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: `${label} ✓` }));
    expect(workspaceStore.getState().panes.some((pane) => pane.type === type)).toBe(false);
  },
);

// Desktop Activity opens the activity sidebar (the zoom system's triage
// surface), not a workspace pane; its checked adornment is the sidebar's own
// open state. Mobile still opens the Sheet (the mobile test below keeps that).
test("desktop Activity menu item toggles the activity sidebar", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_inline"));
  fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
  await threadsStore.getState().ensureThread("ref_inline");

  render(<SessionChrome ref="ref_inline" />);
  // The chrome's Activity ✓ reads the sidebar scoped to THIS session, which
  // production guarantees by the pane being focused while its menu is used;
  // the bare test render focuses it explicitly through a fixture pane.
  const restoreSession = registerPaneForTests({
    id: "session",
    title: () => "session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  try {
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "ref_inline" });
    });
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Activity" }));
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionActivity")).toBe(false);

    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Activity ✓" }));
    expect(activitySidebarStore.getState().open).toBe(false);
  } finally {
    restoreSession();
  }
});

test("the menu marks every pre-opened session pane as checked", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_checked"));
  fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
  await threadsStore.getState().ensureThread("ref_checked");
  workspaceStore.getState().openPane("sessionDetails", { ref: "ref_checked" });
  workspaceStore.getState().openPane("sessionTasks", { ref: "ref_checked" });
  // Activity's check is the sidebar's open state, scoped to this session.
  activitySidebarStore.getState().openWith();
  const restoreSession = registerPaneForTests({
    id: "session",
    title: () => "session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  try {
    workspaceStore.getState().openPane("session", { ref: "ref_checked" });

    render(<SessionChrome ref="ref_checked" />);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    expect(screen.getByRole("menuitem", { name: "Details ✓" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Tasks ✓" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Activity ✓" })).toBeTruthy();
  } finally {
    restoreSession();
  }
});

test("desktop Activity toggles closed only when the sidebar is scoped to this session", async () => {
  // The scope follows focus: focusing session B re-scopes the open sidebar to
  // B, so B's chrome menu reads "Activity ✓" and the item toggles closed.
  // "Open on session A while B's menu is in use" is unreachable in
  // production (dockview unmounts an inactive pane's chrome) - the reachable
  // contract is that Activity never closes a sidebar scoped elsewhere.
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_b"));
  fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
  await threadsStore.getState().ensureThread("ref_b");
  const restoreSession = registerPaneForTests({
    id: "session",
    title: () => "session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  try {
    // Sidebar open on session A.
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith();
    expect(activitySidebarStore.getState().open).toBe(true);

    // Focusing session B re-scopes the open sidebar to it (the scope follows
    // focus): B's chrome shows the checked item, and selecting it closes.
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "ref_b" });
    });
    render(<SessionChrome ref="ref_b" />);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Activity ✓" }));
    expect(activitySidebarStore.getState().open).toBe(false);
  } finally {
    restoreSession();
  }
});

test("desktop Activity closes a leftover sessionActivity pane for this session when it opens the sidebar", async () => {
  // The rail's twin: an upgrade or a restored layout can carry the
  // pre-sidebar pane into the desktop shell, where no affordance opens it and
  // no ✓ marks it. Opening the sidebar on the session supersedes the pane.
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_orphan"));
  fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
  await threadsStore.getState().ensureThread("ref_orphan");
  const restoreSession = registerPaneForTests({
    id: "session",
    title: () => "session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  const restoreActivityPane = registerPaneForTests({
    id: "sessionActivity",
    title: () => "activity",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  try {
    workspaceStore.getState().openPane("session", { ref: "ref_orphan" });
    workspaceStore.getState().openPane("sessionActivity", { ref: "ref_orphan" });
    render(<SessionChrome ref="ref_orphan" />);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Activity" }));
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(workspaceStore.getState().panes.some((p) => p.type === "sessionActivity")).toBe(false);
  } finally {
    restoreSession();
    restoreActivityPane();
  }
});

test("mobile chrome opens Sheets without changing workspace panes", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_mobile"));
  await threadsStore.getState().ensureThread("ref_mobile");

  try {
    render(<SessionChrome ref="ref_mobile" />);
    expect(workspaceStore.getState().panes).toEqual([]);
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Details" }));
    expect(await screen.findByRole("heading", { name: "Session details" })).toBeTruthy();
    expect(workspaceStore.getState().panes).toEqual([]);
  } finally {
    restoreViewport();
  }
});

// Mobile cadence relocation (2026-07-30-mobile-session-layout-design.md,
// decision 3): the session header's liveness cadence moves into the footer
// chrome row, because the pane header itself is hidden on mobile. Rendered
// always, shown only below the breakpoint via CSS - panes never ask "am I
// mobile?".
test("composes the session liveness cadence into the chrome row", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_cad", { status: { type: "active" } }));
  await threadsStore.getState().ensureThread("ref_cad");

  render(<SessionChrome ref="ref_cad" />);
  await settleActivityDiscovery("ref_cad");

  const slot = document.querySelector('[data-testid="session-chrome-cadence"]');
  expect(slot).not.toBeNull();
  // The Cadence widget itself renders inside the slot.
  expect(slot!.querySelector('[data-testid="cadence-dot"]')).not.toBeNull();
});

test("the cadence slot is desktop-hidden and mobile-shown (CSS source, jsdom has no layout)", () => {
  const css = readFileSync(join(here, "sessionchrome.module.css"), "utf8");
  const base = css.match(/\.cadenceSlot \{([^}]*)\}/);
  expect(base).not.toBeNull();
  expect(base![1]).toContain("display: none");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const slot = mobile![1]!.match(/\.cadenceSlot \{([^}]*)\}/);
  expect(slot).not.toBeNull();
  expect(slot![1]).not.toContain("display: none");
});

// The "..." menu must never overflow onto a line of its own. It used to:
// with StatusRow, the goal chip and .right as flat items of a
// flex-wrap:wrap .chrome, .right was the item the wrap pushed down whole
// whenever the status facts plus the triggers exceeded the chrome width -
// a full extra footer row holding only the "...", and a reflow every time
// the content crossed the threshold. The fix is structural: .chrome never
// wraps and has exactly two children - .body (which owns compression)
// and .right (flex:none, so the menu always shares the one top-level
// line). These two tests lock both halves of that.
test("the chrome row is exactly [.body, .right], with the status content inside .body", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_structure"));
  await threadsStore.getState().ensureThread("ref_structure");

  render(<SessionChrome ref="ref_structure" />);

  const chrome = await screen.findByTestId("session-chrome");
  const body = screen.getByTestId("session-chrome-body");
  // Exactly two direct children: the compressing body, then the right group.
  expect(chrome.children).toHaveLength(2);
  expect(chrome.children[0]).toBe(body);
  const right = chrome.children[1] as Element;
  // The menu lives in .right, the compressible content in .body.
  expect(right.querySelector('[data-testid="session-chrome-body"]')).toBeNull();
  expect(right.contains(screen.getByRole("button", { name: /session actions/i }))).toBe(true);
  expect(body.contains(screen.getByTestId("status-row"))).toBe(true);
  expect(body.contains(screen.getByTestId("session-chrome-cadence"))).toBe(true);
});

test("the chrome CSS makes body a non-wrapping inline-size query container", () => {
  const css = readFileSync(join(here, "sessionchrome.module.css"), "utf8");
  const chrome = css.match(/\.chrome \{([^}]*)\}/);
  const body = css.match(/\.body \{([^}]*)\}/);
  const right = css.match(/\.right \{([^}]*)\}/);
  expect(chrome?.[1]).toContain("flex-wrap: nowrap");
  expect(chrome?.[1]).toContain("min-width: 0");
  expect(body?.[1]).toContain("flex-wrap: nowrap");
  expect(body?.[1]).toContain("min-width: 0");
  expect(body?.[1]).toContain("container-type: inline-size");
  expect(right?.[1]).toContain("flex: none");
});

test("the inline chrome keeps its fixed actions outside the shrinkable status query container", () => {
  const css = readFileSync(join(here, "sessionchrome.module.css"), "utf8");
  const inline = css.match(/\.inline \{([^}]*)\}/);
  const body = css.match(/\.body \{([^}]*)\}/);
  expect(inline?.[1]).toContain("display: flex");
  expect(inline?.[1]).toContain("flex-wrap: nowrap");
  expect(inline?.[1]).toContain("min-width: 0");
  expect(inline?.[1]).not.toContain("container-type");
  expect(body?.[1]).toContain("container-type: inline-size");
});

test("triggerless chrome shares summary ownership and refreshes its menu on typed invalidation", async () => {
  const fake = connectFakeClient(),
    ref = "ref_activity_bg";
  let active = 1;
  fake.on("thread/read", () => readResponse(ref));
  fake.on("evener/thread/activity/read", () => ({
    ...activitySummary(ref),
    delegates: { known: true, total: active, active, completed: 0, failed: 0 },
    jobs: { known: true, total: 0, active: 0, completed: 0, failed: 0 },
  }));
  await threadsStore.getState().ensureThread(ref);
  render(<SessionChrome ref={ref} />);
  await settleActivityDiscovery(ref);
  expect(fake.calls.filter((c) => c.method === "evener/thread/activity/read")).toHaveLength(1);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Activity · 1 active" })).toBeTruthy();
  await user.keyboard("{Escape}");
  active = 3;
  act(() =>
    fake.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["summary"] },
    }),
  );
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/thread/activity/read")).toHaveLength(2));
  await settleActivityDiscovery(ref);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Activity · 3 active" })).toBeTruthy();
  expect(
    fake.calls.filter((c) => c.method === "evener/thread/jobs/list" || c.method === "evener/thread/delegates/list"),
  ).toHaveLength(0);
});
