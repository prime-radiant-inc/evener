import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../protocol/model";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../protocol/types.gen";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { ClientProvider } from "./clientContext";
import { DockHost } from "./DockHost";
import { type PaneProps, registerPane } from "./paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// jsdom has no ResizeObserver (dockview-core dials one on mount to drive its
// auto-resizing - see this task's report for the live probe that found
// this); a real ResizeObserver isn't needed to prove any of this file's
// behavior (nothing here asserts on actual pixel geometry), so a no-op stub
// is the one mock this file needs beyond the real dockview library itself.
class StubResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

// Node 26 defines its own global `localStorage` accessor (Node's Web
// Storage API, https://nodejs.org/api/globals.html#localstorage) that
// returns undefined - and prints "ExperimentalWarning: localStorage is not
// available because --localstorage-file was not provided" - unless the
// process is started with --localstorage-file, which this project's test
// script isn't. Verified directly (not assumed): a bare `new JSDOM(...)`
// constructed with the exact same options vitest's own jsdom
// environment uses DOES have a working window.localStorage; only inside
// vitest's test context does plain `localStorage`/`window.localStorage`
// come back undefined, and `node -e 'console.log(typeof localStorage)'`
// against this repo's Node reproduces the identical warning standalone -
// so this is Node's own global shadowing jsdom's real implementation, not
// a jsdom gap or anything about DockHost.tsx itself (which uses the
// standard localStorage API exactly as a real browser provides it). A
// minimal in-memory Storage stub, scoped to this test file only, is the
// workaround - a real fix belongs in vite.config.ts (test.environmentOptions
// or setupFiles), which is on this task's forbidden-files list.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

// Fixture pane components, simple enough to assert on directly - "doc" is
// this file's non-singleton fixture, "settings" its singleton one (same
// scheme workspace.test.ts uses; a fresh paneRegistry module per test file
// means no collision either way).
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

beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();

  registerPane({
    id: "doc",
    title: (params: { ref: string }) => `Doc ${params.ref}`,
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  registerPane({
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
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("applies the dockview-theme-serf class dockview-theme.css targets", async () => {
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
  // dockview-theme-serf sits closer to everything this app actually
  // styles - but worth asserting precisely rather than assuming, since the
  // "wrong" class on the outer .dv-shell would otherwise look like a bug
  // on inspection.
  expect(container.querySelector(".dockview-theme-serf")).not.toBeNull();
});

test("wires the 'Pop out' group-header affordance into the live dockview host", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
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

test("a second pane opens in a group to the RIGHT of the main pane, not stacked on it", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(1);

  workspaceStore.getState().openPane("doc", { ref: "ref_b" });

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

  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  await screen.findByText(/doc pane: ref_c/);

  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(2); // still two columns
  // Two tabs in the right-hand group (b and c stacked); the main group's own
  // tab bar is hidden, so its pane contributes none.
  expect(visibleTabTexts()).toEqual(["Doc ref_b", "Doc ref_c"]);
});

test("a group holding exactly one pane renders no tab bar; a group with two does", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  // The lone main pane: dockview's own per-group header, hidden.
  expect(headerHiddenFlags()).toEqual([true]);

  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);
  expect(headerHiddenFlags()).toEqual([true, true]); // one pane each, both bare

  workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  await screen.findByText(/doc pane: ref_c/);
  // The right-hand group now stacks two panes, so it needs its tabs back; the
  // main group still holds one and stays bare.
  expect(headerHiddenFlags()).toEqual([true, false]);
});

test("the main pane keeps a visible title with no tab of its own (PaneScaffold header)", async () => {
  workspaceStore.getState().openPane("session", { ref: "ref_untracked" });
  render(<DockHost />);

  // The pane's own PaneScaffold header still names it, and no visible tab does -
  // an unlabelled pane would show neither.
  await screen.findAllByText("ref_untracked");
  expect(visibleTabTexts()).toEqual([]);
});

test("closing the only main pane relaunches welcome in the main slot", async () => {
  const main = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  workspaceStore.getState().closePane(main);

  expect(await screen.findByText("No session open")).toBeTruthy();
  expect(workspaceStore.getState().mainPane()?.type).toBe("welcome");
});

test("closing the main pane relaunches welcome there without promoting a right-hand pane", async () => {
  const main = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // secondary group
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().closePane(main);

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
  const secondary = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().closePane(secondary);

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

  workspaceStore.getState().openPane("settings", { section: "appearance" });

  await screen.findByText(/settings pane: appearance/);
  expect(workspaceStore.getState().panes).toHaveLength(2); // still just the two panes, not three
});

test("reopening a singleton pane with different params updates the existing tab's content in place", async () => {
  workspaceStore.getState().openPane("settings", { section: "appearance" });
  render(<DockHost />);
  await screen.findByText(/settings pane: appearance/);

  workspaceStore.getState().openPane("settings", { section: "credentials" });

  expect(await screen.findByText(/settings pane: credentials/)).toBeTruthy();
  expect(workspaceStore.getState().panes).toHaveLength(1);
});

// --- dockview-native interactions mirror back into the store -------------

// These two drive the tab bar of the SECONDARY group, which needs two panes in
// it to show tabs at all (main holds one pane by rule and never shows them) -
// hence three panes, not two.
test("clicking a different tab updates workspaceStore.focusedPaneId", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks on ref_a, focused
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByText("Doc ref_a")); // the tab, not the pane content (unmounted while inactive)

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(second);
});

test("clicking a tab's native close button updates workspaceStore.panes", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  await screen.findByText(/doc pane: ref_a/);
  const closing = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  const closeAction = document
    .querySelector(".dv-tab.dv-active-tab")
    ?.querySelector(".dv-default-tab-action") as HTMLElement | null;
  expect(closeAction).not.toBeNull();
  await user.click(closeAction as HTMLElement);

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain(closing);
  expect(visibleTabTexts()).toEqual([]); // back to one pane per group: no tab bar anywhere
  // Reopening the same ref proves the id was actually released, not just
  // hidden - a still-tracked "closed" pane would come back focused instead
  // of minting a fresh one.
  const reopened = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  expect(reopened).not.toBe(closing);
});

// --- programmatic close/focus reflect into dockview -----------------------

test("workspace.closePane removes the dockview tab", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks on ref_a
  await screen.findByText(/doc pane: ref_b/);
  expect(visibleTabTexts()).toEqual(["Doc ref_a", "Doc ref_b"]);

  workspaceStore.getState().closePane(first);

  // dockview announces "<title> closed" via an off-screen aria-live region
  // (a nice a11y feature it ships with by default - see this task's
  // report) that also matches a loose /Doc ref_a/ text query, so the tab
  // set is the precise assertion here, not a text search that would
  // false-positive against the announcement. With one pane left in the
  // group its tab bar hides again, so there are no visible tabs at all.
  await vi.waitFor(() => {
    expect(visibleTabTexts()).toEqual([]);
  });
  expect(screen.getByText(/doc pane: ref_b/)).toBeTruthy();
});

test("workspace.focusPane activates the corresponding dockview tab", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_main" });
  render(<DockHost />);
  await screen.findByText(/doc pane: ref_main/);
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // focused initially
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(first);

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(document.querySelector(".dv-tab.dv-active-tab")?.textContent).toContain("Doc ref_a");
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
  queue: false,
  goal: false,
  rename: false,
};

function fixtureThread(ref: string, overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref,
    threadId: `thr_${ref}`,
    name: `Thread ${ref}`,
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    lastFrameAt: 0,
    capabilities: NO_CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
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
  // pre-seeded model, whose fixture turns default to [].
  await screen.findByText(/no turns yet/i);
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
  await screen.findByText(/no turns yet/i);
  expect(document.querySelector(".dv-tab")?.textContent).toBe("Original name");

  threadsStore.setState((s) => {
    const next = new Map(s.threads);
    next.set("ref_x", { ...next.get("ref_x")!, name: "Renamed" });
    return { threads: next };
  });

  await vi.waitFor(() => {
    expect(document.querySelector(".dv-tab")?.textContent).toBe("Renamed");
  });
  // Still the same pane, not a fresh one - the session pane's own body
  // (which doesn't read the thread name at all, only its turns) is
  // untouched throughout the rename.
  expect(screen.getByText(/no turns yet/i)).toBeTruthy();
});

// --- layout persistence -----------------------------------------------

const LAYOUT_KEY = "serf.workspace.layout.v2";

// The debounce timer fires outside any React-tracked event, so advancing
// it must be wrapped in act() or the resulting state update isn't flushed
// before the next assertion reads the DOM/localStorage.
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

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

test("unmounting clears a pending debounced save instead of writing after teardown", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_a/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await Promise.resolve();
  advance(200); // mid-debounce: a save is pending, not yet fired

  unmount();
  advance(1000); // long past the debounce window, but nothing is mounted to fire it

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

test("a routed pane opened before mount merges into a stale saved layout as its focused member", async () => {
  // Phase 1: generate a REAL stale saved layout via a real save round-trip
  // (dockview's own serialization shape is opaque/versioned, so a real
  // save is the only faithful source - same rationale as the restore test
  // above). Two of its three panes deliberately reuse the SAME small
  // id-suffix sequence (pane_doc_1, pane_doc_2, ...) that phase 2's own
  // freshly-reset nextPaneSeq counter will mint next - the realistic shape
  // of the id-collision hazard this test also guards against (see
  // workspace.ts's bumpPastRestoredIds), not a contrived id string.
  workspaceStore.getState().openPane("doc", { ref: "ref_stale_a" }); // pane_doc_1
  workspaceStore.getState().openPane("doc", { ref: "ref_stale_b" }); // pane_doc_2
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_stale_b/);

  vi.useFakeTimers();
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_stale_c" }); // pane_doc_3
  });
  await Promise.resolve();
  advance(500);
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  vi.useRealTimers();
  unmount();
  resetWorkspaceStoreForTests(); // in-memory workspace (incl. nextPaneSeq) resets; localStorage (the stale layout) does not

  // Phase 2: simulates AppShell's routing already having opened a pane (a
  // deep link) BEFORE DockHost ever mounts and reads localStorage - the
  // target behavior (per the controller ruling this task implements): the
  // saved layout restores as the BASE, and the routed pane opens INSIDE
  // it, focused - not wholesale replaced by it (the old, provisional
  // suppress-on-routed fix) and not itself suppressing the restore.
  // resetWorkspaceStoreForTests() just above means this mint ALSO starts
  // from pane_doc_1 - deliberately colliding, in id-suffix terms, with
  // phase 1's own first two ids.
  workspaceStore.getState().openPane("doc", { ref: "ref_routed" }); // pane_doc_1 again, pre-restore
  render(<DockHost />);

  expect(await screen.findByText(/doc pane: ref_routed \(focused=true\)/)).toBeTruthy();
  const tabs2 = document.querySelectorAll(".dv-tab");
  // All three restored tabs are present, in their saved order, PLUS the
  // routed one appended last - a real merge, not a replacement either
  // direction. Critically, "Doc ref_stale_b" is still its own distinct tab
  // with its own content: under the id-collision bug this also regression-
  // tests, the routed pane's freshly-minted id silently collided with
  // stale_b's restored one, and DockHost's reconciliation clobbered
  // stale_b's real dockview panel with the routed pane's params instead of
  // creating a fourth, separate one - collapsing this to 3 tabs, not 4.
  expect(Array.from(tabs2).map((t) => t.textContent)).toEqual([
    "Doc ref_stale_a",
    "Doc ref_stale_b",
    "Doc ref_stale_c",
    "Doc ref_routed",
  ]);
  expect(document.querySelector(".dv-tab.dv-active-tab")?.textContent).toBe("Doc ref_routed");
  expect(workspaceStore.getState().panes).toHaveLength(4);

  // Clicking back to "Doc ref_stale_b" proves its CONTENT survived intact,
  // not just its tab title - under the id-collision bug, this tab's real
  // dockview panel got its params silently overwritten to ref_routed's
  // (see the comment above), so its content would read "ref_routed" here
  // too despite the tab still being labeled "Doc ref_stale_b" at the point
  // that bug's clobbering write happens to land.
  const user = userEvent.setup();
  await user.click(screen.getByText("Doc ref_stale_b"));
  expect(await screen.findByText(/doc pane: ref_stale_b \(focused=true\)/)).toBeTruthy();
});

// Reload keeps the focused tab (round-3 C1). Live diagnosis: dockview DOES
// persist the active tab (each grid leaf's own `activeView`, verified in a real
// browser), and restoreLayout DOES honour it - but handleReady then re-opens
// every URL-routed pane through openPane(), which focuses whatever it resolves
// to. On a reload the routed pane is almost always ALREADY in the restored
// layout, so that re-open is a pure focus steal: the address bar still names
// the pane the user first deep-linked to, and reload snaps focus back to it no
// matter which tab was active when the page unloaded. The fix is
// keepExistingFocus on the merge re-open - an already-open pane keeps the
// restored focus, a genuinely new one (a deep link that ISN'T in the saved
// layout) still focuses, which is the whole point of a deep link.
test("a reload keeps the focused tab even when the URL routes to a different, already-restored pane", async () => {
  // Phase 1: three panes, saved with the SECOND one active - i.e. neither the
  // routed pane (the first) nor the most-recently-opened one, so neither a
  // "routed wins" nor a "last opened wins" bug could pass this by accident.
  const routedParams = { ref: "ref_routed" };
  workspaceStore.getState().openPane("doc", routedParams);
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_c/);

  workspaceStore.getState().focusPane(second);
  await screen.findByText(/doc pane: ref_b \(focused=true\)/);

  vi.useFakeTimers();
  act(() => {
    vi.advanceTimersByTime(500); // flush the debounced save of the focus change
  });
  const saved = localStorage.getItem(LAYOUT_KEY);
  expect(saved).not.toBeNull();
  vi.useRealTimers();
  unmount();
  resetWorkspaceStoreForTests();

  // Phase 2: the reload. AppShell's routing glue re-opens the pane the address
  // bar still names (ref_routed) before DockHost mounts, exactly as it does on
  // a real page load.
  workspaceStore.getState().openPane("doc", routedParams);
  render(<DockHost />);

  await screen.findByText(/doc pane: ref_b \(focused=true\)/);
  expect(document.querySelector(".dv-tab.dv-active-tab")?.textContent).toBe("Doc ref_b");
  // No duplicate: the routed pane merged into the restored one by params.
  expect(document.querySelectorAll(".dv-tab")).toHaveLength(3);
});

test("a deep link to a pane the saved layout does NOT contain still wins focus", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().focusPane(second);
  await screen.findByText(/doc pane: ref_b \(focused=true\)/);

  vi.useFakeTimers();
  act(() => {
    vi.advanceTimersByTime(500);
  });
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  vi.useRealTimers();
  unmount();
  resetWorkspaceStoreForTests();

  workspaceStore.getState().openPane("doc", { ref: "ref_fresh" }); // not in the saved layout
  render(<DockHost />);

  expect(await screen.findByText(/doc pane: ref_fresh \(focused=true\)/)).toBeTruthy();
  expect(document.querySelector(".dv-tab.dv-active-tab")?.textContent).toBe("Doc ref_fresh");
});

test("a corrupt saved layout never suppresses a routed pane - the deep link wins alone (failure-mode floor)", async () => {
  localStorage.setItem(LAYOUT_KEY, JSON.stringify({ nonsense: true }));
  workspaceStore.getState().openPane("doc", { ref: "ref_routed" });

  render(<DockHost />);

  // restoreLayout()'s own structural-validation failure clears whatever
  // fromJSON left behind and empties the store (see workspace.ts) - the
  // routed pane, captured before the attempt, is then the only thing
  // re-opened afterward: the same outright "wins alone" guarantee the
  // pre-merge implementation always provided, preserved here as the
  // failure-mode floor rather than the general case.
  expect(await screen.findByText(/doc pane: ref_routed \(focused=true\)/)).toBeTruthy();
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Doc ref_routed"]);
});

// "no saved layout -> welcome fallback unchanged" (the third scenario this
// task's merge-restore behavior must preserve) is already covered above by
// "falls back to opening welcome when localStorage has nothing saved" -
// with no stored layout at all, handleReady's restore branch never runs,
// so that test's behavior is identical before and after this task's change
// by construction, not merely by coincidence.
