import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
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
      serf: { ref: "ref_d", capabilities: CAPABILITIES, queue: { revision: 0 }, workMillis: 125_000 },
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

// The tasks half of this pair (above) and the jobs half join the same two
// facts from opposite ends: JobsPanel.test.tsx proves the panel fetches for
// whatever sessionRef prop it is HANDED, and this proves SessionChrome hands
// it its own. Neither alone catches a chrome that wires the panel to a wrong
// or stale ref - both files stay green while the sheet quietly reports
// another session's jobs.
test("the jobs panel fetches for the SAME ref passed to SessionChrome", async () => {
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
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  await waitFor(() => expect(calledRef).toBe("ref_e"));
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

// --- jobs panel mounting (2026-07-31-webui-jobs-panel, task 10) --------------
//
// Jobs mirrors the Details/Tasks mounting exactly: an inline trigger on the
// wide row, an overflow menu item (after Details and Tasks) leading the "..."
// menu's own list once the chrome collapses, opened through the panel's
// imperative handle either way.
// Details and Tasks being inline at this width is the neighbouring
// "keeps Details and Tasks inline (and out of the ... menu)" test's job; this
// one adds only what is new — that Jobs joins them on the row.
test("wide chrome renders an inline Jobs trigger", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_jobs_wide"));
  await threadsStore.getState().ensureThread("ref_jobs_wide");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_jobs_wide" />);
    ro.fire(1000); // well above NARROW_CHROME_WIDTH_PX

    expect(screen.getByRole("button", { name: "Jobs" })).toBeTruthy();
  } finally {
    ro.restore();
  }
});

test("narrow chrome hides the inline Jobs trigger and puts a Jobs item in the ... menu after Details and Tasks", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_jobs_narrow"));
  await threadsStore.getState().ensureThread("ref_jobs_narrow");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_jobs_narrow" />);
    ro.fire(300); // well under NARROW_CHROME_WIDTH_PX

    // No inline trigger on the row any more...
    expect(screen.queryByRole("button", { name: "Jobs" })).toBeNull();

    // ...it's in the "..." menu instead, after Details and Tasks, leading
    // the menu's own list.
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    const menuItems = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(menuItems.slice(0, 3)).toEqual(["Details", "Tasks", "Jobs"]);
  } finally {
    ro.restore();
  }
});

test("selecting the Jobs menu item opens the Jobs sheet", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_jobs_open"));
  fake.on("serf/jobs/list", () => ({ data: [] }));
  await threadsStore.getState().ensureThread("ref_jobs_open");
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_jobs_open" />);
    ro.fire(300); // collapsed: the menu item is the only way in

    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: "Jobs" }));

    // The sheet's title is an <h2> (OverlayPanel), so the heading role
    // disambiguates it from the menu item that opened it.
    expect(await screen.findByRole("heading", { name: "Jobs" })).toBeTruthy();
    // ...and its on-open fetch ran and resolved. WHICH ref it asked for is not
    // checked here (the fake answers serf/jobs/list for any ref) - that the
    // panel asks for the ref it is handed is JobsPanel.test.tsx's own
    // "opening fetches and renders one row per job" case, and that this chrome
    // hands it its OWN ref is "the jobs panel fetches for the SAME ref passed
    // to SessionChrome" above.
    expect(await screen.findByText("No jobs yet")).toBeTruthy();
  } finally {
    ro.restore();
  }
});

// Every test above awaits ensureThread BEFORE the first render, so the
// chrome div already exists on SessionChrome's very first commit. The real
// app (Session.tsx, and this kata's own k7harness.html) instead mounts
// SessionChrome immediately and loads the thread asynchronously afterward -
// SessionChrome renders null (`if (!model) return null`) until that
// resolves. A useNarrowerThan built on a plain useRef + an effect keyed only
// on the (constant) threshold runs its setup exactly once, against whatever
// the ref holds at THAT first commit - null, since there's no div yet - and
// never re-runs once the div actually mounts a render later, so the
// observer silently never attaches for the rest of the session (found live
// in the browser harness; no unit test caught it because every other test
// here front-loads the model). This test reproduces that real ordering.
test("still measures the chrome once the thread model loads after the initial mount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_late_model"));
  const ro = stubResizeObserver();

  try {
    render(<SessionChrome ref="ref_late_model" />);
    expect(screen.queryByTestId("session-chrome")).toBeNull(); // model not loaded yet

    await act(async () => {
      await threadsStore.getState().ensureThread("ref_late_model");
    });
    await waitFor(() => expect(screen.queryByTestId("session-chrome")).toBeTruthy());

    ro.fire(300); // well under NARROW_CHROME_WIDTH_PX
    expect(screen.queryByRole("button", { name: "Details" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Tasks" })).toBeNull();
  } finally {
    ro.restore();
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
