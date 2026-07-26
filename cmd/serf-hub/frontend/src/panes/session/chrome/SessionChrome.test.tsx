import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetGoalOverridesForTests } from "./GoalControl";
import { SessionChrome } from "./SessionChrome";

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
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetGoalOverridesForTests();
});

afterEach(() => {
  cleanup();
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

test("composes the status row, session actions, goal control, details panel, and tasks panel once the ref's thread is tracked", async () => {
  const fake = connectFakeClient();
  // Seed a goal so GoalControl has something to render: with no goal it
  // renders nothing at all now (the "Set goal…" entry point moved into the ⋯
  // menu), so a goal is what proves GoalControl is actually composed here.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: {}, goal: { status: "active", iterations: 2 } },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);

  // Status row: model chip.
  expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy();
  // Session actions menu trigger.
  expect(screen.getByRole("button", { name: /session actions/i })).toBeTruthy();
  // Goal control: the goal chip, once a goal is set.
  expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy();
  // Tasks panel trigger.
  expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
  // Details panel trigger - also the [data-details-trigger] hook the palette's
  // "Toggle session details" command reaches for, so this is what makes that
  // command live in the assembled chrome.
  expect(document.querySelector("[data-details-trigger]")).toBe(screen.getByRole("button", { name: "Details" }));
});

test("the details panel reads the work time of the SAME ref passed to SessionChrome", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_d", {
      serf: { ref: "ref_d", capabilities: CAPABILITIES, queue: {}, workMillis: 125_000 },
    }),
  );
  await threadsStore.getState().ensureThread("ref_d");

  render(<SessionChrome ref="ref_d" />);
  await user.click(screen.getByRole("button", { name: "Details" }));

  expect(screen.getByTestId("session-details-work-time").textContent).toContain("2m");
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
  await user.click(screen.getByRole("button", { name: /save/i }));

  await waitFor(() => expect(renamedTo).toEqual({ ref: "ref_b", name: "New name" }));
});

test("the tasks panel fetches for the SAME ref passed to SessionChrome", async () => {
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
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(calledRef).toBe("ref_c"));
});

// --- footer overflow (kata vybn) ---------------------------------------------
//
// jsdom ships no ResizeObserver, so a stub drives SessionChrome's own
// useNarrowerThan the same way popover.test.tsx's "re-measures placement"
// test drives Popover's - a fabricated contentRect.width, fed straight to
// the observer callback, never a real DOM measurement (jsdom reports zero
// for those regardless). That whole-row wrap actually stops at the chosen
// breakpoint is a browser-verified, not a unit-tested, claim - see this
// task's report.
function stubResizeObserver(): { fire: (widthPx: number) => void; restore: () => void } {
  const callbacks: ResizeObserverCallback[] = [];
  class StubResizeObserver {
    constructor(cb: ResizeObserverCallback) {
      callbacks.push(cb);
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  const original = globalThis.ResizeObserver;
  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver;
  return {
    fire(widthPx: number) {
      const entry = { contentRect: { width: widthPx } } as ResizeObserverEntry;
      act(() => {
        for (const cb of callbacks) cb([entry], {} as ResizeObserver);
      });
    },
    restore() {
      globalThis.ResizeObserver = original;
    },
  };
}

test("collapses Details and Tasks into the ... menu once the chrome measures narrower than the breakpoint", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_narrow"));
  await threadsStore.getState().ensureThread("ref_narrow");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_narrow" />);
    ro.fire(300); // well under NARROW_CHROME_WIDTH_PX

    // Neither trigger renders inline on the row any more...
    expect(screen.queryByRole("button", { name: "Details" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Tasks" })).toBeNull();

    // ...they're in the "..." menu instead, SAME labels, leading the list.
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    const menuItems = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(menuItems.slice(0, 2)).toEqual(["Details", "Tasks"]);

    // Selecting the "Details" item does the SAME thing the inline trigger
    // did: opens the session-details sheet.
    await user.click(screen.getByRole("menuitem", { name: "Details" }));
    expect(screen.getByText("Session details")).toBeTruthy();
  } finally {
    ro.restore();
  }
});

test("keeps Details and Tasks inline (and out of the ... menu) once the chrome measures at or above the breakpoint", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_wide"));
  await threadsStore.getState().ensureThread("ref_wide");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_wide" />);
    ro.fire(1000); // well above NARROW_CHROME_WIDTH_PX

    expect(screen.getByRole("button", { name: "Details" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /session actions/i }));
    expect(screen.queryByRole("menuitem", { name: "Details" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Tasks" })).toBeNull();
  } finally {
    ro.restore();
  }
});

test("re-expands Details and Tasks back onto the row when the chrome widens past the breakpoint again", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_reflow"));
  await threadsStore.getState().ensureThread("ref_reflow");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_reflow" />);
    ro.fire(300);
    expect(screen.queryByRole("button", { name: "Details" })).toBeNull();

    ro.fire(1000);
    expect(screen.getByRole("button", { name: "Details" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
  } finally {
    ro.restore();
  }
});
