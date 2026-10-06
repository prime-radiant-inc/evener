import type { ThreadCapabilities, ThreadModel } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SerializedDockview } from "dockview-core";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../motion";
import { enterAgentCascade, popAgentCascade, returnFromAgentCascade } from "../panes/zoom/actions";
import { cascadeClient, cascadeContext } from "../panes/zoom/cascadeTestUtils";
import { associatedCascade, cascadeOrigin } from "../panes/zoom/inspectionOrigin";
import { parseZoomParams, type SessionZoomParams } from "../panes/zoom/intent";
import "../panes/zoom";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import {
  activityClient,
  activityDelegate,
  activityDetailsThread,
  activityJob,
} from "../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { PaneScaffold } from "../widgets/panescaffold";
import { ActivitySidebar } from "./activitybar/ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitybar/activitySidebarStore";
import { ClientProvider } from "./clientContext";
import { DockHost } from "./DockHost";
import { conversationPaneLifetime } from "./paneLifetime";
import { type PaneDescriptor, type PaneProps, paneFor, registerPane, registerPaneForTests } from "./paneRegistry";
import { openTopLevelSession } from "./sessionPlacement";
import {
  consumePaneFocus,
  getDockviewApi,
  type OpenPaneRecord,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";

// Fixture pane components, simple enough to assert on directly - "doc" is
// this file's non-singleton fixture, "settings" its singleton one (same
// scheme workspace.test.ts uses).
function DocFixture({ params, focused }: PaneProps<{ ref: string }>) {
  return (
    <div>
      doc pane: {params.ref} (focused={String(focused)})
    </div>
  );
}
function SettingsFixture({ params }: PaneProps<{ section?: string }>) {
  return <div>settings pane: {params.section ?? "none"}</div>;
}

function FocusFixture({ paneId, focused }: PaneProps<{ ref: string }>) {
  return (
    <PaneScaffold title="Delayed details" paneId={paneId} focused={focused} scaffoldMarker="delayed-details">
      delayed details body
    </PaneScaffold>
  );
}

beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());

  registerPaneForTests({
    id: "doc",
    title: (params: { ref: string }) => `Doc ${params.ref}`,
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  registerPaneForTests({
    id: "settings",
    singleton: true,
    title: (params: { section?: string }) => `Settings${params.section ? `: ${params.section}` : ""}`,
    component: lazy(() => Promise.resolve({ default: SettingsFixture })),
  });
  // Real production panes, for the end-to-end tests further down.
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/welcome"); // registerPane("welcome") side effect
  await import("../panes/session"); // registerPane("session") side effect
  await import("../panes/sessionPanels"); // register the three session panel pane types
  await import("../panes/transcript");

  // Then RENDER the two panes whose Suspense reveal a test would otherwise
  // wait out. Importing a module is only half a React.lazy's cost: lazy keeps
  // a payload of its own that stays uninitialized until React first renders
  // the component, so the first render still suspends. An already-resolved
  // promise does not dodge it: the `doc` and `settings` fixtures above are
  // lazy(() => Promise.resolve(...)) and still suspend once each. A reveal
  // that commits outside act waits out react-dom's FALLBACK_THROTTLE_MS
  // (300ms, react-dom 19.2) on a real timer, so warmPane renders inside an
  // awaited act, where the reveal commits as soon as the chunk resolves.
  // Only these two: the `settings` fixture and the real session pane are
  // never awaited through their own Suspense boundary anywhere in this file
  // (measured - every test that opens one settles in single-digit ms off the
  // synchronously-rendered dockview tab title), so warming them would be
  // cost with no benefit.
  await warmPane(
    () => workspaceStore.getState().openPane("doc", { ref: "ref_warm" }),
    () => screen.findByText(/doc pane: ref_warm/),
  );
  // No pane open: DockHost's own boot fallback opens welcome in the main slot.
  await warmPane(
    () => {},
    () => screen.findByText("No session open"),
  );
});

// Renders DockHost once with `open`'s pane in it and awaits its landmark, so
// both halves of that pane's lazy-loading cost are already paid by the time a
// test measures it. See the beforeAll above for why the module cache alone is
// not enough.
async function warmPane(open: () => void, findLandmark: () => Promise<unknown>): Promise<void> {
  open();
  await act(async () => {
    render(<DockHost />);
  });
  await findLandmark();
  // Unmounting also clears DockHost's pending debounced layout save (its own
  // effect cleanup), so no warm render leaks a write into a later test.
  cleanup();
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  localStorage.clear();
}

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("applies the dockview-theme-evener class dockview-theme.css targets", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const { container } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  // dockview-react's className prop lands on the gridview-level wrapper
  // (an ANCESTOR of every .dv-tab/.dv-groupview/.dv-content-container
  // element), not the outermost .dv-shell div - that outer div carries its
  // own separate, hardcoded "dockview-theme-abyss" default (verified via a
  // live probe: dockview-core defaults `options.theme` to its built-in
  // abyss theme independently of the className prop, and applies its
  // className to a DIFFERENT, outer wrapper). Harmless: CSS custom
  // properties resolve from the NEAREST ancestor that defines them, and
  // dockview-theme-evener sits closer to everything this app actually
  // styles - but worth asserting precisely rather than assuming, since the
  // "wrong" class on the outer .dv-shell would otherwise look like a bug
  // on inspection.
  expect(container.querySelector(".dockview-theme-evener")).not.toBeNull();
});

// Pop out is a dockview RIGHT-HEADER action, so it lives inside the same
// per-group header container syncGroupHeaders hides for main (dockview-core's
// tabsContainer owns both the tabs and the right-actions slot, and
// header.hidden display:none's the lot). It is therefore reachable on the
// secondary group - whose header is always visible, whatever its pane count -
// and not on the main pane. Same shape as the always-reachable close (x): the
// main pane deliberately has no header affordances at all.
test("wires the 'Pop out' group-header affordance into the live dockview host", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  });
  await screen.findByText(/doc pane: ref_a/); // secondary group's header is already visible with just this one pane
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await screen.findByText(/doc pane: ref_b/);

  // The affordance is a dockview right-header action rendered by the real
  // host - proof popout is actually reachable (no longer dormant), which the
  // isolated PopoutHeaderAction unit test cannot establish on its own.
  expect(await screen.findByRole("button", { name: "Pop out" })).toBeTruthy();
});

test("renders the content of a pane opened via workspace.openPane", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();
});

// kata fmtz: a pane's own component is React.lazy() (paneRegistry.ts's
// PaneDescriptor.component), and PaneHost wraps it in a Suspense boundary -
// live-verified (real browser, real click, first open of a not-yet-loaded
// pane type in the page's lifetime) to sometimes paint the pane's content
// area completely blank for a beat, even though the surrounding dockview
// chrome (tab/group) is already in the DOM: the boundary's fallback was
// `null`, so there was nothing FOR it to show while the dynamic import was
// still pending. This fixture pane holds its own dynamic import open on a
// manually-resolved promise to make that window deterministic instead of a
// timing race, and swaps the real "doc" fixture back in when it's done so
// later tests are unaffected.
test("shows a loading placeholder instead of a blank pane while a newly-opened pane's own content chunk is still loading", async () => {
  const originalDoc = paneFor("doc") as PaneDescriptor<{ ref: string }>;
  let resolveChunk!: () => void;
  const pendingChunk = new Promise<{ default: typeof DocFixture }>((resolve) => {
    resolveChunk = () => resolve({ default: DocFixture });
  });
  registerPane({ ...originalDoc, component: lazy(() => pendingChunk) });

  workspaceStore.getState().openPane("doc", { ref: "ref_slow" });
  render(<DockHost />);

  // Before the chunk resolves: a loading placeholder, not silence.
  expect(await screen.findByTestId("empty-state")).toBeTruthy();
  expect(screen.queryByText(/doc pane: ref_slow/)).toBeNull();

  await act(async () => {
    resolveChunk();
    await pendingChunk;
  });
  expect(await screen.findByText(/doc pane: ref_slow \(focused=true\)/)).toBeTruthy();

  registerPane(originalDoc); // restore the fast fixture for every later test
});

test("cancels toggle-open focus when a panel deactivates before its lazy pane mounts", async () => {
  const originalDetails = paneFor("sessionDetails") as PaneDescriptor<{ ref: string }>;
  let resolveChunk!: () => void;
  const pendingChunk = new Promise<{ default: typeof FocusFixture }>((resolve) => {
    resolveChunk = () => resolve({ default: FocusFixture });
  });
  registerPane({ ...originalDetails, component: lazy(() => pendingChunk) });

  try {
    const main = workspaceStore.getState().openPane("doc", { ref: "ref_main" });
    render(<DockHost />);
    await screen.findByText(/doc pane: ref_main/);

    const delayed = await act(
      async () => workspaceStore.getState().togglePane("sessionDetails", { ref: "ref_delayed" }).paneId,
    );
    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(screen.queryByText("delayed details body")).toBeNull();

    act(() => {
      workspaceStore.getState().focusPane(main);
    });
    await vi.waitFor(() => expect(tabIsActive("Doc ref_main")).toBe(true));

    const previousFocus = document.createElement("button");
    document.body.append(previousFocus);
    previousFocus.focus();
    act(() => {
      workspaceStore.getState().focusPane(delayed);
    });
    expect(await screen.findByText("Loading…")).toBeTruthy();
    await act(async () => {
      resolveChunk();
      await pendingChunk;
    });
    expect(await screen.findByText("delayed details body")).toBeTruthy();

    expect(document.activeElement).toBe(previousFocus);
    expect(document.activeElement).not.toBe(document.querySelector('[data-pane-scaffold="delayed-details"]'));
    previousFocus.remove();
  } finally {
    registerPane(originalDetails);
  }
});

// A breakpoint crossing unmounts DockHost while the workspace still considers
// the toggle-opened pane focused. Its pending focus marker must survive the
// teardown so StackHost's PaneScaffold can consume it once the pane's lazy
// content finally mounts on the other side of the swap.
test("a host teardown preserves the still-focused pane's pending focus marker", async () => {
  const originalDetails = paneFor("sessionDetails") as PaneDescriptor<{ ref: string }>;
  const pendingChunk = new Promise<{ default: typeof FocusFixture }>(() => {});
  registerPane({ ...originalDetails, component: lazy(() => pendingChunk) });

  try {
    workspaceStore.getState().openPane("doc", { ref: "ref_main" });
    const view = render(<DockHost />);
    await screen.findByText(/doc pane: ref_main/);

    const delayed = await act(
      async () => workspaceStore.getState().togglePane("sessionDetails", { ref: "ref_swap" }).paneId,
    );
    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(workspaceStore.getState().focusedPaneId).toBe(delayed);

    view.unmount();
    expect(consumePaneFocus(delayed)).toBe(true);
  } finally {
    registerPane(originalDetails);
  }
});

// --- the two-slot layout: one main pane, everything else to its right ----

// Hiding a group's tab bar is dockview's own `header.hidden`, which sets
// display:none on the group's .dv-tabs-and-actions-container - the .dv-tab
// elements themselves stay in the DOM. So "does this group show tabs" is a
// question about the CONTAINER, and any tab-text assertion has to filter by it
// (a bare .dv-tab query would count tabs nobody can see).
function headerHiddenFlags(): boolean[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".dv-tabs-and-actions-container")).map(
    (el) => el.style.display === "none",
  );
}

function visibleTabTexts(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".dv-tabs-and-actions-container"))
    .filter((el) => el.style.display !== "none")
    .flatMap((el) => Array.from(el.querySelectorAll(".dv-tab")).map((t) => t.textContent ?? ""));
}

// Native tab close controls the user can actually reach - i.e. those inside a
// header container that is not display:none. Same filter as visibleTabTexts,
// for the same reason: the elements themselves survive inside a hidden header.
function visibleCloseControlCount(): number {
  return Array.from(document.querySelectorAll<HTMLElement>(".dv-tabs-and-actions-container"))
    .filter((el) => el.style.display !== "none")
    .reduce((n, el) => n + el.querySelectorAll(".dv-default-tab-action").length, 0);
}

// `.dv-active-tab` is applied PER GROUP (dockview-core's own tab.js/
// tabsContainer.js toggle it off each panel's own api.isActive), so once the
// workspace has two groups there are TWO active tabs - one per group - and
// `document.querySelector(".dv-tab.dv-active-tab")` silently returns whichever
// comes FIRST in the DOM. That is the main group's tab, not the workspace's
// focused one. Asking "is the tab with this title the active tab in its own
// group" is the question that stays well-defined however many groups exist.
function tabIsActive(title: string): boolean {
  const tabs = Array.from(document.querySelectorAll<HTMLElement>(".dv-tab")).filter((t) => t.textContent === title);
  if (tabs.length !== 1)
    throw new Error(`expected exactly one tab titled ${JSON.stringify(title)}, found ${tabs.length}`);
  return tabs[0]?.classList.contains("dv-active-tab") ?? false;
}

test("a second pane opens in a group to the RIGHT of the main pane, not stacked on it", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1);

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });

  await screen.findByText(/doc pane: ref_b/);
  // Two groups side by side, so both panes are visible at once - the main pane
  // is never covered by something opened next to it.
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(2);
  expect(screen.getByText(/doc pane: ref_a/)).toBeTruthy();
});

test("a third pane joins the existing right-hand group rather than making a third column", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await screen.findByText(/doc pane: ref_b/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  });
  await screen.findByText(/doc pane: ref_c/);

  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(2); // still two columns
  // Two tabs in the right-hand group (b and c stacked); the main group's own
  // tab bar is hidden, so its pane contributes none.
  expect(visibleTabTexts()).toEqual(["Doc ref_b", "Doc ref_c"]);
});

// The main group's header is hidden ALWAYS (identity, not count - see
// syncGroupHeaders): it holds exactly one pane by construction and Jesse's
// rule bars it from ever growing tabs. The secondary group's header is
// visible ALWAYS, whether it holds one pane or several - kata 65zj: a lone
// secondary pane still needs its native (x) reachable, so "only shows tabs
// past two panes" cannot apply to it the way it does to main.
test("the main group's header is always hidden; the secondary group's is always visible", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  // The lone main pane: dockview's own per-group header, hidden.
  expect(headerHiddenFlags()).toEqual([true]);

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await screen.findByText(/doc pane: ref_b/);
  // The lone SECONDARY pane's header is visible - this is the fix for 65zj,
  // not a lingering bare group.
  expect(headerHiddenFlags()).toEqual([true, false]);

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  });
  await screen.findByText(/doc pane: ref_c/);
  // The right-hand group now stacks two panes; still visible, main still bare.
  expect(headerHiddenFlags()).toEqual([true, false]);
});

// kata 65zj: opening exactly one pane beside the main pane - both real
// producers (a file/image "Open beside" tool-card affordance, and a
// subagent's "open transcript" row) just call workspaceStore.openPane via
// paneActions.openBeside, and this fix lives entirely in syncGroupHeaders
// (group-identity-based, not pane-type-based), so a "doc" fixture proves the
// mechanism for every producer at once - must leave a REACHABLE close
// control, not a one-way door recoverable only by reload or dragging a
// splitter to zero.
test("a lone secondary pane has a reachable native close control, and closing it collapses the column", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1); // no right-hand column yet

  const beside = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_a" })); // e.g. a file's "Open beside"
  await screen.findByText(/doc pane: ref_a/);
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(2);

  // The affordance itself: a visible tab, with a close control inside it.
  expect(visibleTabTexts()).toEqual(["Doc ref_a"]);
  expect(visibleCloseControlCount()).toBe(1);
  const closeAction = Array.from(document.querySelectorAll<HTMLElement>(".dv-tab"))
    .find((t) => t.textContent === "Doc ref_a")
    ?.querySelector(".dv-default-tab-action") as HTMLElement | null;
  expect(closeAction).not.toBeNull();

  const user = userEvent.setup();
  await user.click(closeAction as HTMLElement);

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain(beside);
  await vi.waitFor(() => {
    expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1); // column collapsed away
  });
  expect(screen.getByText(/doc pane: ref_main/)).toBeTruthy(); // main untouched
});

// The main pane is REPLACEABLE, NOT CLOSEABLE (Jesse, round 3). Pinned as an
// invariant on purpose: the absent (x) reads like a missing button to whoever
// finds it next, and "restore the tab bar so it can be closed" would undo both
// halves of the rule at once.
test("the main pane offers no way to close it - it is replaceable, not closeable", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  // No tab bar, therefore no REACHABLE native (x), and no other close control.
  // Counted through the same visible-header filter visibleTabTexts uses, not a
  // `:not([style*='none'])` attribute match: dockview's own (x) element stays in
  // the DOM inside the hidden container (measured live - the main group has one
  // there and zero visible), so a query that ignores the container's display
  // would report a button no user can click and pass for the wrong reason.
  expect(headerHiddenFlags()).toEqual([true]);
  expect(visibleTabTexts()).toEqual([]);
  expect(visibleCloseControlCount()).toBe(0);
  expect(screen.queryByRole("button", { name: /close/i })).toBeNull();

  // And the main slot is never empty: closing it programmatically (the only
  // route left, since no affordance does) puts welcome back rather than
  // leaving a hole.
  act(() => {
    workspaceStore.getState().closePane(workspaceStore.getState().mainPane()!.id);
  });
  await screen.findByText("No session open");
  expect(workspaceStore.getState().mainPane()?.type).toBe("welcome");
});

test("the main pane keeps a visible title with no tab of its own (PaneScaffold header)", async () => {
  workspaceStore.getState().openPane("session", { ref: "ref_untracked" });
  render(<DockHost />);

  // The pane's own PaneScaffold header still names it, and no visible tab does -
  // an unlabelled pane would show neither.
  await screen.findAllByText("ref_untracked");
  expect(visibleTabTexts()).toEqual([]);
});

// Found in a real browser, not in a fixture: routing from "/" to a session left
// the workspace split between the session and a "No session open" placeholder,
// because the boot welcome pane had taken the main slot and the session opened
// beside it. Welcome is the main slot's empty state, so the first real pane
// takes it over.
test("navigating from the boot welcome pane to a real pane replaces it in the main group", async () => {
  render(<DockHost />);
  await screen.findByText("No session open"); // the boot fallback's welcome pane

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  });

  await screen.findByText(/doc pane: ref_a/);
  expect(screen.queryByText("No session open")).toBeNull();
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1); // one column, not a split
  expect(workspaceStore.getState().mainPane()?.type).toBe("doc");
});

test("closing the only main pane relaunches welcome in the main slot", async () => {
  const main = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  act(() => {
    workspaceStore.getState().closePane(main);
  });

  expect(await screen.findByText("No session open")).toBeTruthy();
  expect(workspaceStore.getState().mainPane()?.type).toBe("welcome");
});

test("closing the main pane relaunches welcome there without promoting a right-hand pane", async () => {
  const main = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // secondary group
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    workspaceStore.getState().closePane(main);
  });

  expect(await screen.findByText("No session open")).toBeTruthy();
  expect(workspaceStore.getState().mainPane()?.type).toBe("welcome");
  // ref_b stayed put in its own group rather than being promoted into main.
  expect(screen.getByText(/doc pane: ref_b/)).toBeTruthy();
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(2);
});

test("closing a secondary pane does NOT relaunch welcome - the main slot is still occupied", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  const secondary = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" }));
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    workspaceStore.getState().closePane(secondary);
  });

  await vi.waitFor(() => {
    expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1);
  });
  expect(workspaceStore.getState().panes.map((p) => p.type)).toEqual(["doc"]);
  expect(screen.queryByText("No session open")).toBeNull();
});

test("the newly-opened pane is focused (true in props, active dockview tab)", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  render(<DockHost />);

  expect(await screen.findByText(/doc pane: ref_b \(focused=true\)/)).toBeTruthy();
});

test("reopening a singleton pane focuses the existing tab instead of duplicating it", async () => {
  workspaceStore.getState().openPane("settings", { section: "appearance" });
  workspaceStore.getState().openPane("doc", { ref: "ref_a" }); // moves focus away
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  act(() => {
    workspaceStore.getState().openPane("settings", { section: "appearance" });
  });

  await screen.findByText(/settings pane: appearance/);
  expect(workspaceStore.getState().panes).toHaveLength(2); // still just the two panes, not three
});

test("reopening a singleton pane with different params updates the existing tab's content in place", async () => {
  workspaceStore.getState().openPane("settings", { section: "appearance" });
  render(<DockHost />);
  await screen.findByText(/settings pane: appearance/);

  act(() => {
    workspaceStore.getState().openPane("settings", { section: "credentials" });
  });

  expect(await screen.findByText(/settings pane: credentials/)).toBeTruthy();
  expect(workspaceStore.getState().panes).toHaveLength(1);
});

test("retyping with the same params changes the content in the same Dockview tab and group", async () => {
  const id = workspaceStore.getState().openPane("doc", { ref: "retype_source" });
  workspaceStore.getState().openPane("doc", { ref: "retype_other" });
  render(<DockHost />);
  await screen.findByText(/doc pane: retype_source/);
  await screen.findByText(/doc pane: retype_other/);
  const source = workspaceStore.getState().panes.find((pane) => pane.id === id);
  if (!source) throw new Error("Missing source record");
  const tab = Array.from(document.querySelectorAll(".dv-tab")).find((node) => node.textContent === "Doc retype_source");
  if (!tab) throw new Error("Missing source tab");
  const group = tab.closest(".dv-groupview");
  const before = workspaceStore.getState().layoutJSON() as { grid: unknown };
  const focus = workspaceStore.getState().focusedPaneId;
  act(() => {
    expect(workspaceStore.getState().retypePane(source, "settings", source.params)).toBe(true);
  });
  expect(await screen.findByText("settings pane: none")).toBeTruthy();
  expect(screen.queryByText(/doc pane: retype_source/)).toBeNull();
  expect(tab.isConnected).toBe(true);
  expect(tab.closest(".dv-groupview")).toBe(group);
  expect((workspaceStore.getState().layoutJSON() as { grid: unknown }).grid).toEqual(before.grid);
  expect(workspaceStore.getState().focusedPaneId).toBe(focus);
  expect(screen.getByText(/doc pane: retype_other/)).toBeTruthy();
});

// DockHost must reconcile the real dockview panels with a primary replacement,
// not merely update the store and leave the old main or secondary panels
// visible in the host.
test("a primary replacement removes stale panels from the live DockHost", async () => {
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: "local:session-a" });
  render(<DockHost />);
  await screen.findAllByText("local:session-a");
  act(() => {
    workspace.openPane("doc", { ref: "secondary" });
  });
  await screen.findByText(/doc pane: secondary/);

  const replacementId = await act(async () => workspace.replacePrimary("session", { ref: "local:session-b" }));

  expect(workspaceStore.getState().panes).toEqual([
    { id: replacementId, type: "session", params: { ref: "local:session-b" }, slot: "main" },
  ]);
  expect(await screen.findAllByText("local:session-b")).toBeTruthy();
  expect(screen.queryByText(/local:session-a/)).toBeNull();
  expect(screen.queryByText(/doc pane: secondary/)).toBeNull();
});

// --- dockview-native interactions mirror back into the store -------------

// These two drive the tab bar of the SECONDARY group, which needs two panes in
// it to show tabs at all (main holds one pane by rule and never shows them) -
// hence three panes, not two.
test("clicking a different tab updates workspaceStore.focusedPaneId", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const second = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_a" }));
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks on ref_a, focused
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByText("Doc ref_a")); // the tab, not the pane content (unmounted while inactive)

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(second);
});

// A native tab (x) only exists where a tab bar does - always the SECONDARY
// group, never main (syncGroupHeaders). The behaviour under test (dockview's
// own close mirroring back into the store) is unchanged and still worth
// covering. See the main-pane invariant test above for the deliberate
// absence on the other side.
test("clicking a secondary tab's native close button updates workspaceStore.panes", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  });
  await screen.findByText(/doc pane: ref_a/);
  const closing = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" }));
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  // Scoped to the tab titled ref_b specifically: `.dv-active-tab` alone is
  // per-group and would find the MAIN group's tab first (see tabIsActive).
  const closeAction = Array.from(document.querySelectorAll<HTMLElement>(".dv-tab"))
    .find((t) => t.textContent === "Doc ref_b")
    ?.querySelector(".dv-default-tab-action") as HTMLElement | null;
  expect(closeAction).not.toBeNull();
  await user.click(closeAction as HTMLElement);

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain(closing);
  // One pane (ref_a) remains in the secondary group; its header - and native
  // (x) - stays reachable (kata 65zj: a lone secondary pane is never stranded
  // without a close control).
  expect(visibleTabTexts()).toEqual(["Doc ref_a"]);
  // Reopening the same ref proves the id was actually released, not just
  // hidden - a still-tracked "closed" pane would come back focused instead
  // of minting a fresh one.
  const reopened = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" }));
  expect(reopened).not.toBe(closing);
});

// --- programmatic close/focus reflect into dockview -----------------------

test("workspace.closePane removes the dockview tab", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const first = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_a" }));
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks on ref_a
  await screen.findByText(/doc pane: ref_b/);
  expect(visibleTabTexts()).toEqual(["Doc ref_a", "Doc ref_b"]);

  act(() => {
    workspaceStore.getState().closePane(first);
  });

  // dockview announces "<title> closed" via an off-screen aria-live region
  // (a nice a11y feature it ships with by default - see this task's
  // report) that also matches a loose /Doc ref_a/ text query, so the tab
  // set is the precise assertion here, not a text search that would
  // false-positive against the announcement. One pane (ref_b) remains in the
  // secondary group; its header - and native (x) - stays visible (65zj).
  await vi.waitFor(() => {
    expect(visibleTabTexts()).toEqual(["Doc ref_b"]);
  });
  expect(screen.getByText(/doc pane: ref_b/)).toBeTruthy();
});

test("workspace.focusPane activates the corresponding dockview tab", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const first = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_a" }));
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // focused initially
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    workspaceStore.getState().focusPane(first);
  });

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(tabIsActive("Doc ref_a")).toBe(true);
});

// --- session pane tab titles: PaneTitleCtx <-> the real threads store -----

// This suite exercises tab titles, not capability gating - every field here
// is false/empty, a plausible-but-inert snapshot.
const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

function fixtureThread(ref: string, overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref,
    threadId: `thr_${ref}`,
    name: `Thread ${ref}`,
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
    capabilities: NO_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

test("a session pane's tab title prefers the live ThreadModel name over the raw ref", async () => {
  threadsStore.setState({ threads: new Map([["ref_x", fixtureThread("ref_x", { name: "Debug the flaky test" })]]) });
  workspaceStore.getState().openPane("session", { ref: "ref_x" });
  render(
    <ClientProvider client={new FakeClient("ready")}>
      <DockHost />
    </ClientProvider>,
  );

  // The real session pane's own body (wave 4): synced against the
  // pre-seeded model, whose fixture turns default to [] - so the body settles
  // on its empty state. Matched by testid, not by that empty state's copy:
  // this test is about the TAB title, and has no business pinning wording the
  // session pane owns.
  await screen.findByTestId("empty-state");
  expect(document.querySelector(".dv-tab")?.textContent).toBe("Debug the flaky test");
});

test("a session pane's tab falls back to the raw ref when no thread name is known", async () => {
  // threadsStore has nothing at all for this ref - never hydrated.
  workspaceStore.getState().openPane("session", { ref: "ref_untracked" });
  render(<DockHost />);

  // Both the tab title AND the placeholder pane's own PaneScaffold title
  // show the raw ref when no thread name is known (Session.tsx's own
  // title is params.ref directly) - a bare findByText("ref_untracked")
  // would ambiguously match both, so this waits for (and counts) both
  // explicitly instead.
  const matches = await screen.findAllByText("ref_untracked");
  expect(matches).toHaveLength(2);
  expect(document.querySelector(".dv-tab")?.textContent).toBe("ref_untracked");
});

function setNavigationTitle(ref: string, title: string): void {
  const key = { kind: "location", ref } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: ref,
    top_level: true,
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title,
      project: "test-project",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
    },
  };
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

// Fix 2: a session pane opened before its transcript hydrates (threadsStore
// has no ThreadModel for the ref yet) used to show the raw ref as its tab
// title even when the rail's already-loaded tree data has the friendly
// title. The cached navigation location is exactly that bounded data source -
// already loaded before a freshly-opened session pane's own hydration completes
// - so the raw ref stays the last resort only.
test("a session pane's tab title falls back to the navigation location title when no thread name is known yet", async () => {
  setNavigationTitle("ref_tree", "Fix the flaky CI job");
  workspaceStore.getState().openPane("session", { ref: "ref_tree" });
  render(
    <ClientProvider client={new FakeClient("ready")}>
      <DockHost />
    </ClientProvider>,
  );

  await screen.findByTestId("empty-state");
  expect(document.querySelector(".dv-tab")?.textContent).toBe("Fix the flaky CI job");
});

test("a session pane's tab title live-updates when the thread is renamed, with no remount", async () => {
  threadsStore.setState({ threads: new Map([["ref_x", fixtureThread("ref_x", { name: "Original name" })]]) });
  workspaceStore.getState().openPane("session", { ref: "ref_x" });
  render(
    <ClientProvider client={new FakeClient("ready")}>
      <DockHost />
    </ClientProvider>,
  );
  // The real session pane's own body (wave 4): synced against the
  // pre-seeded model, whose fixture turns default to [].
  await screen.findByTestId("empty-state");
  expect(document.querySelector(".dv-tab")?.textContent).toBe("Original name");

  await act(async () => {
    threadsStore.setState((s) => {
      const next = new Map(s.threads);
      next.set("ref_x", { ...next.get("ref_x")!, name: "Renamed" });
      return { threads: next };
    });
  });

  await vi.waitFor(() => {
    expect(document.querySelector(".dv-tab")?.textContent).toBe("Renamed");
  });
  // Still the same pane, not a fresh one - the session pane's own body
  // (which doesn't read the thread name at all, only its turns) is
  // untouched throughout the rename.
  expect(screen.getByTestId("empty-state")).toBeTruthy();
});

test("job tabs use their own hydrated titles without extra reads or cross-owner leakage", async () => {
  const fake = new FakeClient("ready");
  fake.on("evener/jobs/get", ({ ref, jobId }) => ({
    data: activityJob({ jobId, ownerRef: ref, description: ref === "owner:a" ? "Release build" : "Release monitor" }),
  }));
  fake.on("evener/jobs/output", () => ({
    data: { offsetBytes: 0, bytesReturned: 5, totalBytes: 5, retainedStartBytes: 0, encoding: "utf8", data: "ready" },
  }));
  connectionStore.getState().connect(fake);
  workspaceStore.getState().openPane("doc", { ref: "main" });
  const build = workspaceStore.getState().openPane("transcript", { ref: "job:same", parentRef: "owner:a" });
  try {
    await act(async () => {
      render(
        <ClientProvider client={fake}>
          <DockHost />
        </ClientProvider>,
      );
    });
    await screen.findByRole("heading", { name: "Release build" });
    expect(await screen.findByRole("tab", { name: "Release build" })).toBeTruthy();
    act(() => {
      workspaceStore.getState().openPane("transcript", { ref: "job:same", parentRef: "owner:b" });
    });
    await screen.findByRole("heading", { name: "Release monitor" });
    expect(await screen.findByRole("tab", { name: "Release monitor" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Release build" })).toBeTruthy();
    act(() => {
      threadsStore.setState({ threads: new Map([["unrelated", fixtureThread("unrelated", { name: "Other work" })]]) });
    });
    expect(screen.getByRole("tab", { name: "Release monitor" })).toBeTruthy();
    act(() => {
      workspaceStore.getState().closePane(build);
    });
    expect(screen.queryByRole("tab", { name: "Release build" })).toBeNull();
    expect(screen.getByRole("tab", { name: "Release monitor" })).toBeTruthy();
    expect(fake.calls.filter((call) => call.method === "evener/jobs/get")).toHaveLength(2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
  } finally {
    cleanup();
    connectionStore.setState({ client: null, state: "idle" });
  }
});

// Fix 1: proof PaneTab is actually wired into the live dockview host (not
// just exercised in isolation - see PaneTab.test.tsx for the dot's own
// state-mapping coverage), the same "wires the affordance into the live
// host" shape PopoutHeaderAction's own DockHost test already establishes.
// The session pane opens into the SECONDARY slot deliberately, not the main
// one: the main group's header is hidden by design (syncGroupHeaders,
// DockHost.tsx), which hides its whole tab subtree - including this dot -
// from the accessibility tree, so role-based queries would find nothing
// whatever the dot's actual state. The secondary group's header is always
// visible (same reasoning PopoutHeaderAction's own DockHost test uses).
test("a session pane's tab shows a live status dot wired to the real dockview host, absent for a quiet thread", async () => {
  threadsStore.setState({ threads: new Map([["ref_x", fixtureThread("ref_x", { name: "Session X" })]]) });
  workspaceStore.getState().openPane("doc", { ref: "ref_main" }); // occupies the main slot
  workspaceStore.getState().openPane("session", { ref: "ref_x" }, { slot: "secondary" });
  render(
    <ClientProvider client={new FakeClient("ready")}>
      <DockHost />
    </ClientProvider>,
  );
  await screen.findByTestId("empty-state");

  // Scoped to the session tab's own .dv-tab element: the pane's OWN header
  // carries its own Cadence dot (also role="img") - this test is about the
  // TAB's dot, not a duplicate assertion on chrome PaneTab.test.tsx already
  // owns and Session.test.tsx already pins.
  function sessionTab(): HTMLElement {
    const tab = Array.from(document.querySelectorAll<HTMLElement>(".dv-tab")).find((t) =>
      t.textContent?.includes("Session X"),
    );
    if (!tab) throw new Error("expected the session pane's own tab");
    return tab;
  }

  // Idle (this fixture's default status): no dot at all.
  expect(within(sessionTab()).queryByRole("img")).toBeNull();

  act(() => {
    threadsStore.setState((s) => {
      const next = new Map(s.threads);
      next.set("ref_x", { ...next.get("ref_x")!, status: { type: "awaiting" } });
      return { threads: next };
    });
  });
  expect(await within(sessionTab()).findByRole("img", { name: "Needs you" })).toBeTruthy();

  // The main slot's own "doc" tab never carries a dot, whatever
  // threadsStore says - it isn't a session pane at all.
  const docTab = Array.from(document.querySelectorAll<HTMLElement>(".dv-tab")).find(
    (t) => t.textContent === "Doc ref_main",
  );
  if (!docTab) throw new Error("expected the doc pane's own tab");
  expect(within(docTab).queryByRole("img", { hidden: true })).toBeNull();
});

// --- layout persistence -----------------------------------------------

const LAYOUT_KEY = "evener.workspace.layout.v2";

// The debounce timer fires outside any React-tracked event, so advancing
// it must be wrapped in act() or the resulting state update isn't flushed
// before the next assertion reads the DOM/localStorage.
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

// The `activeView` of every group in the CURRENTLY SAVED layout - dockview's own
// record of which tab is active, per group (see workspace.ts's restoreLayout for
// the reading side). Returns [] when nothing is saved yet.
//
// Why the tests below wait on THIS rather than merely "the key is non-null":
// mounting already schedules a save of its own, and the debounce coalesces - so
// a save from the initial mount can land BEFORE the focus change this file cares
// about, satisfying a non-null check while the persisted activeView is still the
// pre-focus one. Unmounting then cancels the pending correct save (DockHost
// clears the timer on teardown, deliberately), leaving the test to restore a
// layout that never recorded the focus it was supposed to be about - passing or
// failing for reasons unrelated to what it claims to test. Waiting for the
// specific pane id closes that window.
function savedActiveViews(): string[] {
  const raw = localStorage.getItem(LAYOUT_KEY);
  if (raw === null) return [];
  const parsed = JSON.parse(raw) as { grid: { root: unknown } };
  const out: string[] = [];
  const walk = (node: unknown): void => {
    if (typeof node !== "object" || node === null) return;
    const n = node as { type?: string; data?: unknown };
    if (n.type === "branch" && Array.isArray(n.data)) {
      for (const child of n.data) walk(child);
    } else if (n.type === "leaf") {
      const active = (n.data as { activeView?: string } | undefined)?.activeView;
      if (active !== undefined) out.push(active);
    }
  };
  walk(parsed.grid.root);
  return out;
}

function inspectionRecord(id: string): OpenPaneRecord {
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === id);
  if (!pane) throw new Error(`Missing committed inspection fixture pane ${id}`);
  return pane;
}

async function mountedSecondaryInspector() {
  const client = cascadeClient((ref) => cascadeContext(ref, ref === "child" ? ["root"] : []));
  connectionStore.getState().connect(client);
  window.history.replaceState({}, "", "/s/root");
  const mainId = workspaceStore.getState().openPane("session", { ref: "root" });
  const mount = () =>
    render(
      <ClientProvider client={client}>
        <MotionProvider>
          <DockHost />
        </MotionProvider>
      </ClientProvider>,
    );
  const view = await act(async () => mount());
  await screen.findByRole("heading", { name: "root" });
  const neighborId = await act(async () => workspaceStore.getState().openPane("doc", { ref: "neighbor" }));
  await screen.findByText(/doc pane: neighbor/);
  const inspectorId = await act(async () =>
    enterAgentCascade(activityDelegate({ ownerRef: "root", childRef: "child", delegateId: "edge-child" }), mainId),
  );
  await screen.findByRole("button", { name: "Return to previous view" });
  const api = getDockviewApi();
  if (!api) throw new Error("Missing actual mounted Dockview API");
  act(() => api.layout(1200, 700));
  expect(api.panels.map((panel) => panel.id)).toEqual([mainId, neighborId, inspectorId]);
  expect(cascadeOrigin(inspectionRecord(inspectorId))).toBe(inspectionRecord(mainId));
  return {
    mainId,
    neighborId,
    inspectorId,
    api,
    view,
    mount,
    dispose() {
      cleanup();
      connectionStore.setState({ client: null, state: "idle" });
    },
  };
}

function inspectorPanel(layout: SerializedDockview, id: string) {
  const panel = layout.panels[id];
  if (!panel?.params) throw new Error(`Missing serialized inspector ${id}`);
  return panel;
}

test("cascade tabs keep the selected leaf title across pop, drill and native reactivation", async () => {
  const fixture = await mountedSecondaryInspector();
  try {
    const { api, mainId, neighborId, inspectorId } = fixture;
    expect(api.getPanel(inspectorId)?.title).toBe("child");
    await act(async () => popAgentCascade(inspectorId, "root"));
    expect(api.getPanel(inspectorId)?.title).toBe("root");
    await act(async () =>
      enterAgentCascade(
        activityDelegate({ ownerRef: "root", childRef: "child", delegateId: "edge-child" }),
        inspectorId,
      ),
    );
    expect(api.getPanel(inspectorId)?.title).toBe("child");
    const intent = inspectionRecord(inspectorId).params;
    const savedTitle = api.getPanel(inspectorId)?.title;
    await userEvent.click(screen.getByRole("tab", { name: "Doc neighbor" }));
    expect(api.activePanel?.id).toBe(neighborId);
    await userEvent.click(screen.getByRole("tab", { name: savedTitle }));
    expect(api.activePanel?.id).toBe(inspectorId);
    expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
    expect(inspectionRecord(inspectorId).params).toBe(intent);
    expect(cascadeOrigin(inspectionRecord(inspectorId))).toBe(inspectionRecord(mainId));
    expect(screen.getAllByTestId("cascade-column").map((column) => column.dataset.scopeRef)).toEqual(["root", "child"]);
    expect(api.getPanel(inspectorId)?.title).toBe(savedTitle);
  } finally {
    fixture.dispose();
  }
});

test.each([
  {
    type: "session",
    savedTitle: "Saved neighbor",
    recoveredTitle: "Recovered name",
    unnamedTitle: "",
    changedTitle: "ref_changed",
  },
  {
    type: "transcript",
    savedTitle: "Saved neighbor",
    recoveredTitle: "Recovered name",
    unnamedTitle: "",
    changedTitle: "ref_changed",
  },
  {
    type: "sessionTasks",
    savedTitle: "Tasks · Saved neighbor",
    recoveredTitle: "Tasks · Recovered name",
    unnamedTitle: "Tasks · ref_saved_neighbor",
    changedTitle: "Tasks · ref_changed",
  },
  {
    type: "sessionDetails",
    savedTitle: "Details · Saved neighbor",
    recoveredTitle: "Details · Recovered name",
    unnamedTitle: "Details · ref_saved_neighbor",
    changedTitle: "Details · ref_changed",
  },
] as const)(
  "restored inactive $type keeps its saved title until authoritative name recovery",
  async ({ type, savedTitle, recoveredTitle, unnamedTitle, changedTitle }) => {
    const ref = "ref_saved_neighbor";
    const client = new FakeClient("ready");
    const mount = () =>
      render(
        <ClientProvider client={client}>
          <DockHost />
        </ClientProvider>,
      );
    threadsStore.setState({ threads: new Map([[ref, fixtureThread(ref, { name: "Saved neighbor" })]]) });
    workspaceStore.getState().openPane("doc", { ref: "main" });
    const neighborId = workspaceStore.getState().openPane(type, { ref });
    const activeId = workspaceStore.getState().openPane("settings", { section: "general" });
    const view = await act(async () => mount());
    const api = getDockviewApi();
    if (!api) throw new Error("Missing actual Dockview API");
    const saved = api.toJSON();
    expect(api.activePanel?.id).toBe(activeId);
    expect(saved.panels[neighborId]?.title).toBe(savedTitle);
    expect(document.querySelector(`[data-pane-scaffold="session:${ref}"]`)).toBeNull();

    view.unmount();
    resetWorkspaceStoreForTests();
    resetThreadsStoreForTests();
    resetNavigationStoreForTests();
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
    await act(async () => mount());
    const restored = getDockviewApi();
    if (!restored) throw new Error("Missing restored Dockview API");
    expect(threadsStore.getState().threads.has(ref)).toBe(false);
    expect(restored.activePanel?.id).toBe(activeId);
    expect(restored.toJSON().panels[neighborId]).toEqual(saved.panels[neighborId]);
    expect(document.querySelector(`[data-pane-scaffold="session:${ref}"]`)).toBeNull();

    await act(async () => {
      threadsStore.setState({ threads: new Map([[ref, fixtureThread(ref, { name: "Recovered name" })]]) });
    });
    expect(restored.getPanel(neighborId)?.title).toBe(recoveredTitle);
    await act(async () => {
      threadsStore.setState({ threads: new Map([[ref, fixtureThread(ref, { name: "" })]]) });
    });
    expect(restored.getPanel(neighborId)?.title).toBe(unnamedTitle);
    expect(restored.getPanel(neighborId)?.params).toEqual(saved.panels[neighborId]?.params);
    expect(restored.activePanel?.id).toBe(activeId);
    await act(async () => {
      expect(workspaceStore.getState().retypePane(inspectionRecord(neighborId), type, { ref: "ref_changed" })).toBe(
        true,
      );
    });
    expect(restored.getPanel(neighborId)?.title).toBe(changedTitle);
    expect(restored.activePanel?.id).toBe(activeId);
  },
);

test.each([false, true])(
  "secondary inspector restores distinct panels and saved focus with captured route=%s",
  async (routed) => {
    const fixture = await mountedSecondaryInspector();
    try {
      const { mainId, neighborId, inspectorId } = fixture;
      const saved = workspaceStore.getState().layoutJSON() as SerializedDockview;
      const intent = inspectorPanel(saved, inspectorId).params?.paneParams;
      expect(parseZoomParams(intent)).toMatchObject({
        ref: "child",
        source: { type: "transcript", params: { ref: "root" } },
        edges: [{ ownerRef: "root", childRef: "child", delegateId: "edge-child" }],
        inspection: { origin: { paneId: mainId, type: "session", ref: "root" } },
      });
      expect(saved.activeGroup).toBe(fixture.api.getPanel(inspectorId)?.group.id);
      fixture.view.unmount();
      resetWorkspaceStoreForTests();
      localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
      if (routed) openTopLevelSession("root");
      await act(async () => fixture.mount());
      const api = getDockviewApi();
      if (!api) throw new Error("Missing restored Dockview API");
      act(() => api.layout(1200, 700));
      const main = inspectionRecord(mainId);
      const inspector = inspectionRecord(inspectorId);
      expect(workspaceStore.getState().panes.map((pane) => [pane.id, pane.type, pane.slot])).toEqual([
        [mainId, "session", "main"],
        [neighborId, "doc", "secondary"],
        [inspectorId, "sessionZoom", "secondary"],
      ]);
      expect(api.getPanel(inspectorId)?.group).toBe(api.getPanel(neighborId)?.group);
      expect(api.getPanel(inspectorId)?.group).not.toBe(api.getPanel(mainId)?.group);
      expect(api.toJSON().grid).toEqual(saved.grid);
      expect(inspector.params).toEqual(intent);
      expect(cascadeOrigin(inspector)).toBe(main);
      expect(associatedCascade(main)).toBe(inspector);
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect(api.activePanel?.id).toBe(inspectorId);
      expect(window.location.pathname).toBe("/s/root");
      expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual([
        "root",
        "child",
      ]);
      await act(async () => returnFromAgentCascade(inspectorId));
      expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, neighborId]);
      expect(inspectionRecord(mainId)).toBe(main);
      expect(workspaceStore.getState().focusedPaneId).toBe(mainId);
      expect(api.activePanel?.id).toBe(mainId);
    } finally {
      fixture.dispose();
    }
  },
);

test.each(["different root", "missing child"] as const)(
  "secondary inspector cannot retain boot focus over a captured %s conversation",
  async (routeKind) => {
    const fixture = await mountedSecondaryInspector();
    try {
      const saved = workspaceStore.getState().layoutJSON() as SerializedDockview;
      fixture.view.unmount();
      resetWorkspaceStoreForTests();
      localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
      const routedRef = routeKind === "different root" ? "fresh-root" : "child";
      openTopLevelSession(routeKind === "different root" ? routedRef : "root");
      if (routeKind === "missing child") {
        workspaceStore.getState().openPane("session", { ref: routedRef }, { slot: "secondary" });
      }
      await act(async () => fixture.mount());
      const conversations = workspaceStore
        .getState()
        .panes.filter((pane) => pane.type === "session" && (pane.params as { ref: string }).ref === routedRef);
      expect(conversations).toHaveLength(1);
      expect(conversations[0]?.slot).toBe(routeKind === "different root" ? "main" : "secondary");
      expect(workspaceStore.getState().focusedPaneId).toBe(conversations[0]?.id);
      expect(getDockviewApi()?.activePanel?.id).toBe(conversations[0]?.id);
      expect(workspaceStore.getState().focusedPaneId).not.toBe(fixture.inspectorId);
      if (routeKind === "missing child") {
        expect(workspaceStore.getState().mainPane()).toMatchObject({
          id: fixture.mainId,
          type: "session",
          params: { ref: "root" },
        });
        expect(cascadeOrigin(inspectionRecord(fixture.inspectorId))).toBe(inspectionRecord(fixture.mainId));
      }
    } finally {
      fixture.dispose();
    }
  },
);

test("secondary inspector saves a retired locator before host reconciliation and cannot adopt a restored same-ID origin", async () => {
  const fixture = await mountedSecondaryInspector();
  try {
    const { mainId, neighborId, inspectorId, api } = fixture;
    const original = inspectionRecord(mainId);
    const originalLifetime = conversationPaneLifetime(original);
    const replacement: OpenPaneRecord = { ...original, params: { ref: "root" } };
    let saved: SerializedDockview | null = null;
    act(() => {
      workspaceStore.getState().closePane(mainId);
      workspaceStore.setState({
        panes: [replacement, ...workspaceStore.getState().panes],
        focusedPaneId: inspectorId,
      });
      expect(parseZoomParams(api.getPanel(inspectorId)?.params?.paneParams)?.inspection?.origin).toEqual({
        paneId: mainId,
        type: "session",
        ref: "root",
      });
      saved = workspaceStore.getState().layoutJSON() as SerializedDockview;
      expect(parseZoomParams(inspectorPanel(saved, inspectorId).params?.paneParams)?.inspection).toEqual({
        origin: null,
      });
    });
    expect(originalLifetime.alive).toBe(false);
    expect(conversationPaneLifetime(replacement)).not.toBe(originalLifetime);
    fixture.view.unmount();
    resetWorkspaceStoreForTests();
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
    await act(async () => fixture.mount());
    const restored = inspectionRecord(mainId);
    const inspector = inspectionRecord(inspectorId);
    expect(cascadeOrigin(inspector)).toBeNull();
    expect(associatedCascade(restored)).toBeNull();
    expect(parseZoomParams(inspector.params)?.inspection).toEqual({ origin: null });
    await act(async () => returnFromAgentCascade(inspectorId));
    expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, neighborId]);
    expect(inspectionRecord(mainId)).toBe(restored);
    expect(workspaceStore.getState().focusedPaneId).not.toBe(mainId);
    expect(consumePaneFocus(mainId)).toBe(false);
    expect(window.location.pathname).toBe("/s/root");
  } finally {
    fixture.dispose();
  }
});

test.each([
  { name: "missing target", locator: { paneId: "absent-pane", type: "session", ref: "root" } },
  { name: "wrong type", locator: { paneId: "pane_session_1", type: "transcript", ref: "root" } },
  { name: "empty ID", locator: { paneId: "", type: "session", ref: "root" } },
  { name: "invalid type", locator: { paneId: "pane_session_1", type: "doc", ref: "root" } },
  { name: "invalid ref", locator: { paneId: "pane_session_1", type: "session", ref: 42 } },
])("secondary inspector recovers a $name locator without binding the surviving source", async ({ locator }) => {
  const fixture = await mountedSecondaryInspector();
  try {
    const { mainId, neighborId, inspectorId } = fixture;
    const saved = workspaceStore.getState().layoutJSON() as SerializedDockview;
    const panel = inspectorPanel(saved, inspectorId);
    panel.params = { ...panel.params, paneParams: { ...panel.params?.paneParams, inspection: { origin: locator } } };
    fixture.view.unmount();
    resetWorkspaceStoreForTests();
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
    await act(async () => fixture.mount());
    const main = inspectionRecord(mainId);
    const inspector = inspectionRecord(inspectorId);
    expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, neighborId, inspectorId]);
    expect(parseZoomParams(inspector.params)?.inspection).toEqual({ origin: null });
    expect(cascadeOrigin(inspector)).toBeNull();
    expect(associatedCascade(main)).toBeNull();
    expect(conversationPaneLifetime(inspector).composer).toBeNull();
    await act(async () => returnFromAgentCascade(inspectorId));
    expect(inspectionRecord(mainId)).toBe(main);
    expect(workspaceStore.getState().focusedPaneId).not.toBe(mainId);
    expect(consumePaneFocus(mainId)).toBe(false);
  } finally {
    fixture.dispose();
  }
});

test("secondary inspector and real source survive an unloadable unrelated saved panel", async () => {
  const fixture = await mountedSecondaryInspector();
  try {
    const { mainId, neighborId, inspectorId } = fixture;
    const saved = workspaceStore.getState().layoutJSON() as SerializedDockview;
    const neighbor = inspectorPanel(saved, neighborId);
    neighbor.params = { paneType: "sessionNotes", paneParams: { ref: "retired-neighbor" } };
    fixture.view.unmount();
    resetWorkspaceStoreForTests();
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(saved));
    await act(async () => fixture.mount());
    const main = inspectionRecord(mainId);
    const inspector = inspectionRecord(inspectorId);
    expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, inspectorId]);
    expect(getDockviewApi()?.panels.map((panel) => panel.id)).toEqual([mainId, inspectorId]);
    expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
    expect(cascadeOrigin(inspector)).toBe(main);
    expect(main).toMatchObject({ type: "session", slot: "main", params: { ref: "root" } });
    expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
    await act(async () => returnFromAgentCascade(inspectorId));
    expect(workspaceStore.getState().panes).toEqual([main]);
    expect(workspaceStore.getState().focusedPaneId).toBe(mainId);
  } finally {
    fixture.dispose();
  }
});

test("secondary inspector boot with failed real fromJSON preserves live records, lifetimes and source draft", async () => {
  const mainId = workspaceStore.getState().openPane("session", { ref: "root" });
  const main = inspectionRecord(mainId);
  const lifetime = conversationPaneLifetime(main);
  const composer = lifetime.composer;
  if (!composer) throw new Error("Missing real source composer owner");
  composer.editText("keep the live source draft");
  const neighborId = workspaceStore.getState().openPane("doc", { ref: "neighbor" });
  const inspectorId = enterAgentCascade(
    activityDelegate({ ownerRef: "root", childRef: "child", delegateId: "edge-child" }),
    mainId,
  );
  const inspector = inspectionRecord(inspectorId);
  const inspectionLifetime = conversationPaneLifetime(inspector);
  localStorage.setItem(LAYOUT_KEY, JSON.stringify({ nonsense: true }));
  const client = cascadeClient((ref) => cascadeContext(ref, ref === "child" ? ["root"] : []));
  connectionStore.getState().connect(client);
  try {
    await act(async () =>
      render(
        <ClientProvider client={client}>
          <MotionProvider>
            <DockHost />
          </MotionProvider>
        </ClientProvider>,
      ),
    );
    await screen.findByRole("button", { name: "Return to previous view" });
    expect(inspectionRecord(mainId)).toBe(main);
    expect(inspectionRecord(inspectorId)).toBe(inspector);
    expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, neighborId, inspectorId]);
    expect(conversationPaneLifetime(main)).toBe(lifetime);
    expect(lifetime.alive).toBe(true);
    expect(lifetime.composer).toBe(composer);
    expect(composer.getSnapshot().text).toBe("keep the live source draft");
    expect(conversationPaneLifetime(inspector)).toBe(inspectionLifetime);
    expect(inspectionLifetime.alive).toBe(true);
    expect(cascadeOrigin(inspector)).toBe(main);
    expect(getDockviewApi()?.panels.map((panel) => panel.id)).toEqual([mainId, neighborId, inspectorId]);
    expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
    await act(async () => returnFromAgentCascade(inspectorId));
    expect(inspectionRecord(mainId)).toBe(main);
    expect(lifetime.composer).toBe(composer);
    expect(composer.getSnapshot().text).toBe("keep the live source draft");
  } finally {
    cleanup();
    connectionStore.setState({ client: null, state: "idle" });
  }
});

test.each([
  { sourceType: "transcript", routedRef: null },
  { sourceType: "session", routedRef: "root" },
  { sourceType: "session", routedRef: "other-root" },
] as const)(
  "a real promoted $sourceType restores saved intent against route $routedRef",
  async ({ sourceType, routedRef }) => {
    const context = (ref: string) =>
      cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "child" ? ["root"] : []);
    const client = cascadeClient(context);
    connectionStore.getState().connect(client);
    const id = workspaceStore.getState().openPane(sourceType, { ref: "root" });
    const mount = () =>
      render(
        <ClientProvider client={client}>
          <MotionProvider>
            <DockHost />
          </MotionProvider>
        </ClientProvider>,
      );
    try {
      const view = await act(async () => mount());
      await screen.findByRole("heading", { name: "root" });
      const tab = document.querySelector(".dv-tab");
      if (!tab) throw new Error("Missing real transcript tab");
      const group = tab.closest(".dv-groupview");
      act(() => workspaceStore.getState().openPane("doc", { ref: "kept-secondary" }));
      await screen.findByText(/doc pane: kept-secondary/);
      act(() => workspaceStore.getState().focusPane(id));
      const before = workspaceStore.getState().layoutJSON() as { grid: unknown };
      const sourceRecord = workspaceStore.getState().panes.find((pane) => pane.id === id);
      if (!sourceRecord) throw new Error("Missing legacy source fixture");
      await act(async () => {
        workspaceStore.getState().retypePane(sourceRecord, "sessionZoom", {
          ref: "grandchild",
          source: { type: sourceType, params: sourceRecord.params },
          edges: [
            { ownerRef: "root", childRef: "child", delegateId: "edge-child" },
            { ownerRef: "child", childRef: "grandchild", delegateId: "edge-grandchild" },
          ],
        });
      });
      expect(tab.isConnected).toBe(true);
      expect(tab.closest(".dv-groupview")).toBe(group);
      expect((workspaceStore.getState().layoutJSON() as { grid: unknown }).grid).toEqual(before.grid);
      const intent = workspaceStore.getState().panes.find((pane) => pane.id === id)?.params as SessionZoomParams;
      vi.useFakeTimers();
      const current = workspaceStore.getState().panes.find((pane) => pane.id === id);
      if (!current) throw new Error("Missing promoted record");
      await act(async () => workspaceStore.getState().retypePane(current, "sessionZoom", { ...intent }));
      advance(400);
      const saved = localStorage.getItem(LAYOUT_KEY);
      if (!saved) throw new Error("Missing real cascade save");
      const layout = JSON.parse(saved) as {
        grid: unknown;
        panels: Record<string, { params: { paneType: string; paneParams: SessionZoomParams } }>;
      };
      expect(layout.panels[id]?.params).toEqual({ paneType: "sessionZoom", paneParams: intent });
      expect(layout.grid).toEqual(before.grid);
      expect(saved).not.toContain("data:image");
      expect(saved).not.toContain("olderCursor");
      vi.useRealTimers();
      view.unmount();
      resetWorkspaceStoreForTests();
      if (routedRef) workspaceStore.getState().openPane("session", { ref: routedRef });
      await act(async () => mount());
      if (routedRef === "other-root") {
        expect(workspaceStore.getState().panes).toHaveLength(1);
        expect(workspaceStore.getState().mainPane()).toMatchObject({
          type: "session",
          params: { ref: "other-root" },
        });
        expect(screen.queryByRole("button", { name: "Return to previous view" })).toBeNull();
        expect(screen.queryByText(/doc pane: kept-secondary/)).toBeNull();
        return;
      }
      await screen.findByRole("button", { name: "Return to previous view" });
      expect(workspaceStore.getState().panes.find((pane) => pane.id === id)?.params).toEqual(intent);
      expect((workspaceStore.getState().layoutJSON() as { grid: unknown }).grid).toEqual(before.grid);
      expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual([
        "child",
        "grandchild",
      ]);
      const footer = screen.getByTestId("statusbar");
      expect(within(footer).getByText("grandchild").getAttribute("aria-current")).toBe("page");
      expect(screen.getByText(/doc pane: kept-secondary/)).toBeTruthy();
    } finally {
      cleanup();
      connectionStore.setState({ client: null, state: "idle" });
    }
  },
);

test("debounces saving the layout to localStorage after a change", async () => {
  // Real timers for the initial mount (findByText's own polling), fake
  // timers only from here on - this sidesteps any question of whether
  // testing-library's polling machinery correctly drives vitest fake
  // timers for an unrelated concern (mounting), and keeps this test
  // focused on the ONE thing it's actually proving: the debounce window
  // itself, asserted synchronously against localStorage after each
  // act(() => advanceTimersByTime()) call.
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();

  vi.useFakeTimers();
  // openPane() itself only mutates the store; DockHost's reconciliation
  // effect (which actually calls dockview's addPanel()) runs on the
  // resulting re-render, which React schedules rather than performing
  // synchronously - act() forces that flush before this test proceeds.
  // dockview's own onDidLayoutChange then fires one microtask after
  // addPanel() (verified via a live probe - see this task's report), so
  // one more microtask turn after act() gets THIS effect's setTimeout
  // actually scheduled before advance() starts moving the fake clock.
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await Promise.resolve();

  advance(300); // under LAYOUT_SAVE_DEBOUNCE_MS (400): not yet saved
  expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();

  advance(150); // 450ms total: past the debounce window
  const saved = localStorage.getItem(LAYOUT_KEY);
  expect(saved).not.toBeNull();
  const parsed = JSON.parse(saved!) as { panels: Record<string, unknown> };
  expect(Object.keys(parsed.panels)).toEqual(["pane_doc_1", "pane_doc_2"]);
});

test.each(["debounce", "teardown"] as const)(
  "a parameter-only retype persists through %s without a native layout change",
  async (settlement) => {
    const id = workspaceStore.getState().openPane("doc", { ref: "params_source" });
    const view = render(<DockHost />);
    await screen.findByText(/doc pane: params_source/);
    vi.useFakeTimers();
    act(() => {
      workspaceStore.getState().openPane("doc", { ref: "params_other" });
    });
    await Promise.resolve();
    advance(450);
    const baseline = localStorage.getItem(LAYOUT_KEY);
    if (!baseline) throw new Error("Missing initial saved layout");
    const source = workspaceStore.getState().panes.find((pane) => pane.id === id);
    if (!source) throw new Error("Missing source record");
    const params = { ref: "params_source", detail: "retained-intent" };
    act(() => {
      workspaceStore.getState().retypePane(source, "doc", params);
    });
    await Promise.resolve();
    advance(200);
    expect(localStorage.getItem(LAYOUT_KEY)).toBe(baseline);
    if (settlement === "teardown") view.unmount();
    else advance(200);
    const saved = localStorage.getItem(LAYOUT_KEY);
    if (!saved) throw new Error("Missing parameter-only save");
    const layout = JSON.parse(saved) as {
      panels: Record<string, { params: { paneType: string; paneParams: unknown } }>;
    };
    expect(layout.panels[id]?.params).toEqual({ paneType: "doc", paneParams: params });
    if (settlement === "teardown") {
      localStorage.removeItem(LAYOUT_KEY);
      advance(1000);
      expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();
    }
  },
);

test("unmounting flushes a pending debounced save rather than writing after teardown", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await Promise.resolve();
  advance(200); // mid-debounce: a save is pending, not yet fired
  expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();

  unmount();

  // The pending write lands DURING teardown, carrying the real layout - both
  // panes, not an empty or null one. A debounce defers a write; it must never
  // silently discard it just because the host came down inside the window
  // (a splitter drag followed straight away by a route the shell renders
  // NotFound for used to forget the new geometry outright).
  const flushed = localStorage.getItem(LAYOUT_KEY);
  expect(flushed).not.toBeNull();
  expect(Object.keys((JSON.parse(flushed!) as { panels: Record<string, unknown> }).panels)).toEqual([
    "pane_doc_1",
    "pane_doc_2",
  ]);

  // ...and nothing fires AFTER teardown: the original hazard this cleanup
  // was written for. Clearing the key and running the clock long past the
  // debounce window proves the timer itself is gone, not merely early.
  localStorage.removeItem(LAYOUT_KEY);
  advance(1000);
  expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();
});

test("collapses several rapid layout changes into a single debounced save", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await Promise.resolve();
  advance(200); // ref_b's own 400ms window hasn't elapsed yet
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" }); // resets the debounce window
  });
  await Promise.resolve();
  advance(200); // 200ms since ref_c: still under ITS window
  expect(localStorage.getItem(LAYOUT_KEY)).toBeNull();

  advance(200); // 400ms since ref_c: past the (reset) window
  const parsed = JSON.parse(localStorage.getItem(LAYOUT_KEY)!) as { panels: Record<string, unknown> };
  expect(Object.keys(parsed.panels)).toHaveLength(3);
});

test("falls back to opening welcome when localStorage has nothing saved", async () => {
  render(<DockHost />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("falls back to opening welcome when localStorage contains malformed JSON", async () => {
  localStorage.setItem(LAYOUT_KEY, "{not valid json");
  render(<DockHost />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("falls back to opening welcome when localStorage contains structurally-invalid dockview JSON", async () => {
  localStorage.setItem(LAYOUT_KEY, JSON.stringify({ nonsense: true }));
  render(<DockHost />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("restores a previously-saved layout on boot instead of falling back to welcome", async () => {
  // Round-tripped through a real save (via the debounced-save path above)
  // rather than a hand-crafted SerializedDockview literal - dockview's own
  // serialization shape is opaque/versioned; a real save is the only
  // faithful source for what a real restore needs to parse.
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_b/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" }); // one more change to trigger a save
  });
  await Promise.resolve();
  advance(500);
  const saved = localStorage.getItem(LAYOUT_KEY);
  expect(saved).not.toBeNull();
  vi.useRealTimers();
  unmount();
  resetWorkspaceStoreForTests(); // fresh boot, nothing opened yet

  render(<DockHost />);

  expect(await screen.findByText(/doc pane: ref_c/)).toBeTruthy(); // the most recently active pane, restored
  const tabs = document.querySelectorAll(".dv-tab");
  expect(tabs).toHaveLength(3);
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Doc ref_a", "Doc ref_b", "Doc ref_c"]);
});

// A layout saved by a build that still shipped a pane type this one no
// longer registers (the removed sessionNotes) must not wipe the workspace -
// and the skipped panel must leave the live dockview api in the same restore:
// the commit between restoreLayout and DockHost's reconciliation renders
// every api panel through paneFor(), which throws for an unregistered type
// and drops the workspace into DockChunkBoundary's error state. The
// FakeDockviewApi doubles in workspace.test.ts assert the store-level skip
// but cannot see that render path; this test round-trips a REAL save, poisons
// one panel, and boots.
test("a saved layout with a retired pane type restores the surviving panes without crashing", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_b/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" }); // one more change to trigger a save
  });
  await Promise.resolve();
  advance(500);
  const saved = localStorage.getItem(LAYOUT_KEY);
  expect(saved).not.toBeNull();
  vi.useRealTimers();
  unmount();

  // Poison the MAIN pane with the retired type - the exact hazard: a live,
  // active tab dockview would mount before any reconciliation could remove
  // it.
  const layout = JSON.parse(saved!) as { panels: Record<string, { params?: unknown }> };
  const poisoned = JSON.stringify({
    ...layout,
    panels: {
      ...layout.panels,
      pane_doc_1: {
        ...layout.panels.pane_doc_1,
        params: { paneType: "sessionNotes", paneParams: { ref: "local:poisoned" } },
      },
    },
  });
  localStorage.setItem(LAYOUT_KEY, poisoned);
  resetWorkspaceStoreForTests(); // fresh boot, nothing opened yet

  render(<DockHost />);

  // The survivors render, the poisoned panel is gone from the store and
  // the DOM (VISIBLE tabs only: elements survive inside a hidden header, and
  // after recovery the survivors' group must keep its tabs and close
  // controls reachable - the store's recovered "main" sits physically in
  // the old secondary group), and the workspace never fell to its
  // chunk-boundary error state.
  expect(await screen.findByText(/doc pane: ref_c/)).toBeTruthy();
  expect(visibleTabTexts()).toEqual(["Doc ref_b", "Doc ref_c"]);
  expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual(["pane_doc_2", "pane_doc_3"]);
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
});

function retireSavedActivityPane(paneId: string): void {
  const raw = localStorage.getItem(LAYOUT_KEY);
  if (raw === null) throw new Error("Expected a real saved Dockview layout");
  const layout = JSON.parse(raw) as { panels: Record<string, { params?: unknown }> };
  const pane = layout.panels[paneId];
  if (!pane) throw new Error(`Missing saved pane ${paneId}`);
  pane.params = { paneType: "sessionActivity", paneParams: { ref: "remote:a" } };
  localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout));
}

function expectReadOnlyRestore(client: FakeClient): void {
  const methods = client.calls.map((call) => call.method);
  expect(methods).toContain("thread/read");
  expect(methods.every((method) => /\/(read|list|subscribe|unsubscribe)$/.test(method))).toBe(true);
  for (const mutation of [
    "evener/session/delete",
    "evener/project/delete",
    "evener/archive/set",
    "thread/shutdown",
    "evener/thread/forceStop",
    "thread/start",
    "turn/start",
  ]) {
    expect(methods).not.toContain(mutation);
  }
}

test.each([
  ["secondary", false],
  ["main", false],
  ["focused", false],
  ["secondary", true],
  ["main", true],
  ["focused", true],
] as const)(
  "saved Activity %s is omitted while Overview open=%s restores independently",
  async (placement, overviewOpen) => {
    resetActivitySidebarStoreForTests();
    const client = activityClient();
    client.on("thread/read", ({ ref }) => activityDetailsThread(ref, { preview: "Restored session" }));
    connectionStore.getState().connect(client);
    try {
      const workspace = workspaceStore.getState();
      let retired: string | undefined;
      if (placement === "main") retired = workspace.openPane("doc", { ref: "retired" });
      const session = workspace.openPane("session", { ref: "remote:a" });
      const details = workspace.openPane("sessionDetails", { ref: "remote:a" }, { slot: "secondary" });
      const doc = workspace.openPane("doc", { ref: "survivor" });
      if (placement !== "main") retired = workspace.openPane("doc", { ref: "retired" });
      if (retired === undefined) throw new Error("Retired placement was not created");
      const retiredId = retired;
      const saved = render(
        <ClientProvider client={client}>
          <DockHost />
        </ClientProvider>,
      );
      await screen.findByText(/doc pane: retired/);
      act(() => workspaceStore.getState().focusPane(placement === "focused" ? retiredId : session));
      saved.unmount();
      retireSavedActivityPane(retiredId);
      resetWorkspaceStoreForTests();
      resetThreadsStoreForTests();
      activitySidebarStore.getState().retarget("remote:a");
      activitySidebarStore.getState().openWith("about");
      if (!overviewOpen) activitySidebarStore.getState().close();
      resetActivitySidebarStoreForTests({ preserveStorage: true });
      await act(async () => {
        render(
          <ClientProvider client={client}>
            <MotionProvider>
              <DockHost />
              <ActivitySidebar />
            </MotionProvider>
          </ClientProvider>,
        );
      });
      const panes = workspaceStore.getState().panes;
      expect(panes.map((pane) => pane.id)).toEqual([session, details, doc]);
      expect(panes.some((pane) => (pane.type as string) === "sessionActivity")).toBe(false);
      expect(panes.find((pane) => pane.slot === "main")?.id).toBe(session);
      expect(panes.some((pane) => pane.id === workspaceStore.getState().focusedPaneId)).toBe(true);
      expect(document.querySelector('[data-pane-scaffold="session:remote:a"]')).toBeTruthy();
      const user = userEvent.setup();
      await user.click(screen.getByRole("tab", { name: "Doc survivor" }));
      expect(await screen.findByText(/doc pane: survivor/)).toBeTruthy();
      await user.click(screen.getByRole("tab", { name: /^Details ·/ }));
      const detailsBody = document.querySelector(`[data-pane-id="${details}"]`);
      if (!(detailsBody instanceof HTMLElement)) throw new Error("Restored Details body missing");
      const body = within(detailsBody);
      expect((await body.findByTestId("session-details-cost")).textContent).toContain("~$1.00");
      expect(body.getByTestId("session-details-context").textContent).toContain("42K / 100K");
      expect(body.getByText("/work/session")).toBeTruthy();
      expect(activitySidebarStore.getState()).toMatchObject({ open: overviewOpen, tab: "about", ref: "remote:a" });
      if (overviewOpen) {
        const sidebar = within(await screen.findByTestId("activity-sidebar"));
        expect(sidebar.getByRole("radio", { name: "About" }).getAttribute("aria-checked")).toBe("true");
        expect(await sidebar.findByText("about-owner")).toBeTruthy();
      } else expect(screen.queryByTestId("activity-sidebar")).toBeNull();
      expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
      expect(document.querySelector('[data-pane-scaffold="session-panel:activity:remote:a"]')).toBeNull();
      expect(visibleTabTexts()).toContain("Doc survivor");
      expect(visibleTabTexts()).toHaveLength(placement === "main" ? 3 : 2);
      expect(visibleCloseControlCount()).toBe(placement === "main" ? 3 : 2);
      expectReadOnlyRestore(client);
    } finally {
      cleanup();
      resetActivitySidebarStoreForTests();
      connectionStore.setState({ client: null, state: "idle" });
    }
  },
);

test.each([false, true])(
  "an Activity-only saved layout applies a valid primary route=%s before Welcome",
  async (routed) => {
    const retired = workspaceStore.getState().openPane("doc", { ref: "retired" });
    const saved = render(<DockHost />);
    await screen.findByText(/doc pane: retired/);
    saved.unmount();
    retireSavedActivityPane(retired);
    resetWorkspaceStoreForTests();
    const client = activityClient();
    connectionStore.getState().connect(client);
    try {
      if (routed) workspaceStore.getState().openPane("session", { ref: "remote:routed" });
      await act(async () => {
        render(
          <ClientProvider client={client}>
            <DockHost />
          </ClientProvider>,
        );
      });
      expect(workspaceStore.getState().panes.map((pane) => pane.type)).toEqual([routed ? "session" : "welcome"]);
      if (routed) {
        expect(document.querySelector('[data-pane-scaffold="session:remote:routed"]')).toBeTruthy();
        expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "remote:routed" });
        expect(screen.queryByText("No session open")).toBeNull();
        expectReadOnlyRestore(client);
      } else expect(await screen.findByText("No session open")).toBeTruthy();
    } finally {
      cleanup();
      connectionStore.setState({ client: null, state: "idle" });
    }
  },
);

test("restores a routed primary through replacement before reopening captured secondary routes", async () => {
  workspaceStore.getState().openPane("doc", { ref: "saved_main" });
  workspaceStore.getState().openPane("doc", { ref: "saved_secondary" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: saved_secondary/);

  // DockHost's cleanup flushes the real saved layout before this simulated
  // reload resets the in-memory workspace.
  unmount();
  resetWorkspaceStoreForTests();

  workspaceStore.getState().openPane("settings", { section: "theme" });
  workspaceStore.getState().openPane("doc", { ref: "routed_secondary" }, { slot: "secondary" });
  render(<DockHost />);

  expect(await screen.findByText(/doc pane: routed_secondary/)).toBeTruthy();
  const panes = workspaceStore.getState().panes;
  expect(panes).toHaveLength(2);
  expect(panes.find((pane) => pane.type === "settings")?.slot).toBe("main");
  expect(panes.find((pane) => (pane.params as { ref?: string }).ref === "routed_secondary")?.slot).toBe("secondary");
  expect(panes.some((pane) => (pane.params as { ref?: string }).ref?.startsWith("saved_"))).toBe(false);
  expect(screen.queryByText(/doc pane: saved_/)).toBeNull();
});

test("a routed Settings primary replaces a stale saved layout", async () => {
  workspaceStore.getState().openPane("doc", { ref: "saved_a" });
  workspaceStore.getState().openPane("doc", { ref: "saved_b" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: saved_b/);

  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();

  workspaceStore.getState().openPane("settings", { section: "theme" });
  render(<DockHost />);

  expect(await screen.findByText(/settings pane: theme/)).toBeTruthy();
  const panes = workspaceStore.getState().panes;
  expect(panes).toHaveLength(1);
  expect(panes[0]).toMatchObject({ type: "settings", params: { section: "theme" }, slot: "main" });
  expect(screen.queryByText(/doc pane: saved_/)).toBeNull();
});

test("a corrupt saved layout never suppresses a routed Settings primary", async () => {
  localStorage.setItem(LAYOUT_KEY, JSON.stringify({ nonsense: true }));
  workspaceStore.getState().openPane("settings", { section: "theme" });

  render(<DockHost />);

  expect(await screen.findByText(/settings pane: theme/)).toBeTruthy();
  expect(workspaceStore.getState().panes).toEqual([
    expect.objectContaining({ type: "settings", params: { section: "theme" }, slot: "main" }),
  ]);
});

// "no saved layout -> welcome fallback unchanged" (the third scenario this
// task's merge-restore behavior must preserve) is already covered above by
// "falls back to opening welcome when localStorage has nothing saved" -
// with no stored layout at all, handleReady's restore branch never runs,
// so that test's behavior is identical before and after this task's change
// by construction, not merely by coincidence.

// kata eve5: a plain reload at the hub's root URL routes to "welcome" - but
// welcome is never itself part of a saved layout (openPane's own placement rule
// displaces it from the main slot the instant a real pane opens, and
// nothing else ever puts one back), so on an ordinary reload of "/" the
// routed re-open ALWAYS resolves to "genuinely new", forcing a fresh welcome
// pane into existence and stealing focus onto it - even though the saved
// layout already had a real, restorable focused tab. This is not the
// intended "deep link wins" case at all: nobody deep-linked anywhere, "/" is
// the same fallback route regardless of what was open before, and the
// spurious pane it manufactures lands stacked into the secondary group
// alongside the real restored panes, one tab bar entry that outnumbers what
// was actually saved.
test("a reload at the root route does not spawn a spurious focused welcome tab over a restored layout", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(second);
  await screen.findByText(/doc pane: ref_b \(focused=true\)/);

  unmount(); // flushes the pending debounced save, see the earlier tests
  expect(savedActiveViews()).toContain(second);
  resetWorkspaceStoreForTests();

  // Phase 2: the reload, at "/" - AppShell's openRouteAsPane falls back to
  // opening "welcome" with {} for any route that isn't session/settings/
  // spawn, exactly as it does for the bare root path.
  workspaceStore.getState().openPane("welcome", {});
  render(<DockHost />);

  // The restored, previously-focused pane keeps focus - a bare reload must
  // not silently reassign it to a placeholder nobody asked for.
  expect(await screen.findByText(/doc pane: ref_b \(focused=true\)/)).toBeTruthy();
  expect(tabIsActive("Doc ref_b")).toBe(true);

  // No third, spurious tab: the restored layout already fully accounts for
  // both panes.
  expect(document.querySelectorAll(".dv-tab")).toHaveLength(2);

  // And the tab that IS there for ref_a still works - clicking it actually
  // focuses it, not a dead label sitting beside a phantom active welcome.
  const user = userEvent.setup();
  await user.click(screen.getByText("Doc ref_a"));
  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(tabIsActive("Doc ref_a")).toBe(true);
});

test("workspace focus activates a selected group and switches a different retained tab", async () => {
  const main = workspaceStore.getState().openPane("doc", { ref: "group_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: group_main/);
  const observer = await act(async () => workspaceStore.getState().openPane("doc", { ref: "group_observer" }));
  await screen.findByText(/doc pane: group_observer/);
  await act(async () => workspaceStore.getState().openPane("doc", { ref: "group_grandchild" }));
  await screen.findByText(/doc pane: group_grandchild/);
  act(() => workspaceStore.getState().focusPane(main));
  expect(await screen.findByText(/doc pane: group_main \(focused=true\)/)).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(main);
  expect(tabIsActive("Doc group_grandchild")).toBe(true);
  act(() => workspaceStore.getState().focusPane(observer));
  expect(await screen.findByText(/doc pane: group_observer \(focused=true\)/)).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(observer);
  expect(tabIsActive("Doc group_observer")).toBe(true);
  expect(tabIsActive("Doc group_grandchild")).toBe(false);
});
