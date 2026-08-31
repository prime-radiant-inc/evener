import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render as renderUI, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps, ReactElement } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type {
  NavigationSessionLocation,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
} from "../../../protocol/types.gen";
import { ClientProvider } from "../../../shell/clientContext";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { activitySummaryStore, resetActivitySummaryStoreForTests } from "../../../stores/activitySummary";
import { connectionStore } from "../../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../../stores/navigation/store";
import { keyID } from "../../../stores/navigation/types";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import { installMobileViewport } from "../testing/mobileViewport";
import "../../sessionPanels";
import { ActivityPanelBody } from "./ActivityPanel";
import { SessionChrome as SessionChromeView } from "./SessionChrome";

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
    mode: "v1",
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
  const fake = new FakeClient("ready");
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
  resetActivitySummaryStoreForTests();
  resetNavigationStoreForTests();
  resetTranscriptDisplayStoreForTests();
});

afterEach(() => {
  cleanup();
  // @ts-expect-error jsdom has no matchMedia by default; individual mobile
  // tests install the narrow viewport explicitly.
  delete window.matchMedia;
  // The beforeEach above only resets threadsStore BEFORE each test. Every
  // test here calls ensureThread(ref) directly for setup - SessionChrome
  // takes its model as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  resetTranscriptDisplayStoreForTests();
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

test("composes the status row, the session menu, and the goal control once the ref's thread is tracked", async () => {
  const fake = connectFakeClient();
  // Seed a goal so GoalControl has something to render: with no goal it
  // renders nothing at all (setting a goal lives in the command palette's
  // /goal builtin), so a goal is what proves GoalControl is actually
  // composed here.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        goal: { status: "active", iterations: 2 },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);

  // Status row: model chip.
  expect(screen.getByTestId("model-switch-value").textContent).toBe("anthropic/claude-sonnet-4-5");
  // Session menu trigger.
  expect(screen.getByRole("button", { name: /session actions/i })).toBeTruthy();
  // Goal control: the goal chip, once a goal is set. Two triggers share this
  // accessible name now (the full chip and the compact glyph trigger that
  // takes over below 560px - GoalControl.tsx), so a role/name query alone is
  // ambiguous; the testid picks the chip specifically.
  expect(screen.getByTestId("goal-chip-trigger")).toBeTruthy();
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
  // GoalControl rides the inline status too: production only ever mounts
  // placement="composer" (Composer.tsx), so leaving it footer-only made it
  // unreachable in the real app — the live E2E pass caught the regression.
  // The testid picks the full chip specifically - a role/name query is
  // ambiguous now that the compact glyph trigger shares its accessible name.
  expect(within(statusContainer).getByTestId("goal-chip-trigger")).toBeTruthy();
});

test("default placement preserves the standalone session chrome presentation", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_footer"));
  await threadsStore.getState().ensureThread("ref_footer");

  render(<SessionChrome ref="ref_footer" />);

  expect(screen.getByTestId("session-chrome")).toBeTruthy();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
});

test("status row has no inline Details/Tasks/Activity buttons; they live in the menu", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);

  expect(screen.queryByRole("button", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Tasks/ })).toBeNull();
  expect(screen.queryByRole("button", { name: /Activity/ })).toBeNull();

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Details" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Tasks/ })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Activity/ })).toBeTruthy();
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
    // Mobile's SessionMenu is a bottom-Sheet drawer of plain buttons, not a
    // role=menu popover (SessionMenu.tsx's isMobile branch).
    await user.click(screen.getByRole("button", { name: "Verbosity…" }));

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

test("menu Tasks item toggles the sessionTasks workspace pane open and closed on desktop", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Tasks/ }));

  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(true);

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Tasks/ }));
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(false);
});

test("menu offers Pin/Archive/Delete when the session is in the tree; omits them otherwise", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  setLocation("ref_a");

  render(<SessionChrome ref="ref_a" />);
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
  await user.click(screen.getByRole("button", { name: "Details" }));

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
  await user.click(screen.getByRole("button", { name: "Tasks" }));

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
  fake.on("evener/jobs/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
  });

  render(<SessionChrome ref="ref_e" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("button", { name: "Activity" }));

  await waitFor(() => expect(calledRef).toBe("ref_e"));
  restoreViewport();
});

// --- panes through the menu (2026-08-05-unified-session-context-menu) --------
//
// Details/Tasks/Activity are the menu's leading group at every width and on
// every host: desktop items toggle the workspace panes, mobile items open the
// Sheets through the panels' imperative handles (openX branches on isMobile).

test.each([
  ["Details", "sessionDetails"],
  ["Tasks", "sessionTasks"],
  ["Activity", "sessionActivity"],
] as const)("desktop %s menu item opens and closes its pane for the SessionChrome ref", async (label, type) => {
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
});

test("the menu marks every pre-opened session pane as checked", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_checked"));
  fake.on("evener/jobs/list", () => ({ data: emptyActivityTree() }));
  await threadsStore.getState().ensureThread("ref_checked");
  workspaceStore.getState().openPane("sessionDetails", { ref: "ref_checked" });
  workspaceStore.getState().openPane("sessionTasks", { ref: "ref_checked" });
  workspaceStore.getState().openPane("sessionActivity", { ref: "ref_checked" });

  render(<SessionChrome ref="ref_checked" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Details ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Tasks ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Activity ✓" })).toBeTruthy();
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
    await user.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByRole("heading", { name: "Session details" })).toBeTruthy();
    expect(workspaceStore.getState().panes).toEqual([]);
  } finally {
    restoreViewport();
  }
});

// --- activity panel background refresh ---------------------------------------
//
// The panels stay mounted triggerless, and ActivityPanel's refreshWhenHidden
// is unconditional: the menu's "Activity · N" label reads the same summary
// the hidden panel refreshes, so background refresh must run without any
// trigger on the row.

test("triggerless chrome refreshes an established Activity summary in the background", async () => {
  const fake = connectFakeClient();
  let fetches = 0;
  fake.on("thread/read", () => readResponse("ref_activity_bg"));
  fake.on("evener/jobs/list", () => {
    fetches += 1;
    return { data: emptyActivityTree() };
  });
  await threadsStore.getState().ensureThread("ref_activity_bg");
  const initial = threadsStore.getState().threads.get("ref_activity_bg");
  if (!initial) throw new Error("missing background activity model");
  const model = { ...initial, jobsUpdatedAt: 1 };
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  activitySummaryStore.setState({
    entries: new Map([
      [
        model.ref,
        { counts: undefined, established: true, mountedBodies: 0, loading: false, lastFetchedBump: 0, requestID: 1 },
      ],
    ]),
  });

  render(<SessionChrome ref={model.ref} />);

  // No trigger ever renders, yet the established summary refreshes against
  // the newer bump - the menu's badge depends on exactly this.
  await waitFor(() => expect(fetches).toBe(1));
});

test("desktop Activity waits for the body's first root attempt before owning later refreshes", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let fetches = 0;
  fake.on("thread/read", () => readResponse("ref_activity_fresh"));
  fake.on("evener/jobs/list", () => {
    fetches += 1;
    const tree = emptyActivityTree();
    tree.root.counts.active = fetches;
    return { data: tree };
  });
  await threadsStore.getState().ensureThread("ref_activity_fresh");
  const initial = threadsStore.getState().threads.get("ref_activity_fresh");
  if (!initial) throw new Error("missing initial activity freshness model");
  threadsStore.setState({ threads: new Map([[initial.ref, { ...initial, jobsUpdatedAt: 1 }]]) });

  const chrome = render(<SessionChrome ref="ref_activity_fresh" />);
  await act(async () => Promise.resolve());
  expect(fetches).toBe(0);
  expect(activitySummaryStore.getState().entries.get(initial.ref)?.established).not.toBe(true);

  const body = render(<ActivityPanelBody sessionRef={initial.ref} model={initial} />);
  await waitFor(() => expect(fetches).toBe(1));
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Activity · 1" })).toBeTruthy();
  await user.keyboard("{Escape}");
  body.unmount();

  const current = threadsStore.getState().threads.get("ref_activity_fresh");
  if (!current) throw new Error("missing activity freshness model");
  threadsStore.setState({ threads: new Map([[current.ref, { ...current, jobsUpdatedAt: 2 }]]) });
  // Two more fetches, not one: the unmount hands refresh ownership back to
  // the chrome, which first catches up on bump 1 (the body's attempt ran at
  // a null bump), and bump 2 - arriving while that catch-up is in flight -
  // is queued and re-issued rather than dropped (the old drop was the
  // stale-badge bug: the UI would never have fetched bump 2's jobs at all).
  await waitFor(() => expect(fetches).toBe(3));
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Activity · 3" })).toBeTruthy();
  await Promise.resolve();
  expect(fetches).toBe(3);
  chrome.unmount();
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
