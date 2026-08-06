import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { activitySummaryStore, resetActivitySummaryStoreForTests } from "../../../stores/activitySummary";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetTreeStoreForTests, type TreeResponse, treeStore } from "../../../stores/tree";
import "../../sessionPanels";
import { ActivityPanelBody } from "./ActivityPanel";
import { resetGoalOverridesForTests } from "./GoalControl";
import { SessionChrome } from "./SessionChrome";

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
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
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

// A minimal normalized TreeResponse carrying exactly one top-level local
// session node for `ref` (the shape normalizeTree produces - see
// stores/tree.ts's TreeResponse): enough for findSessionNode to resolve the
// node and for SessionMenu's Pin/Archive/Delete gating to see a top-level,
// local-host session.
function treeWithSession(ref: string): TreeResponse {
  return {
    generated_at: "2026-08-06T00:00:00Z",
    sources: [],
    live: [
      {
        row_id: `row_${ref}`,
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
    ],
    needs_you: [],
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySummaryStoreForTests();
  resetGoalOverridesForTests();
  resetTreeStoreForTests();
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
});

function installMobileViewport(): () => void {
  const original = window.matchMedia;
  window.matchMedia = (() => ({
    matches: true,
    media: "(max-width: 899px)",
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  return () => {
    window.matchMedia = original;
  };
}

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
      serf: {
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
  expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy();
  // Session menu trigger.
  expect(screen.getByRole("button", { name: /session actions/i })).toBeTruthy();
  // Goal control: the goal chip, once a goal is set.
  expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy();
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

test("menu Tasks item toggles the sessionTasks workspace pane on desktop", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Tasks/ }));

  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(true);
});

test("menu offers Pin/Archive/Delete when the session is in the tree; omits them otherwise", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  treeStore.setState({ tree: treeWithSession("ref_a") });

  render(<SessionChrome ref="ref_a" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Archive" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Delete…" })).toBeTruthy();
  await user.keyboard("{Escape}");

  // No tree node for the ref (tree empty/unloaded): the organization and
  // delete items are absent - they are decisions about a rail row.
  act(() => treeStore.setState({ tree: null }));
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Archive" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
});

test("menu Shut down is gated on capabilities.shutdown", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      serf: { ref: "ref_a", capabilities: { ...CAPABILITIES, shutdown: false }, queue: { revision: 0 } },
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
      serf: { ref: "ref_d", capabilities: CAPABILITIES, queue: { revision: 0 }, workMillis: 125_000 },
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
  fake.on("serf/thread/name/set", (params) => {
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
  fake.on("serf/tasks/list", (params) => {
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
  fake.on("serf/jobs/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
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
  fake.on("serf/jobs/list", () => ({ data: emptyActivityTree() }));
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
  fake.on("serf/jobs/list", () => ({ data: emptyActivityTree() }));
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
    await user.click(screen.getByRole("menuitem", { name: "Details" }));
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
  fake.on("serf/jobs/list", () => {
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
  fake.on("serf/jobs/list", () => {
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
