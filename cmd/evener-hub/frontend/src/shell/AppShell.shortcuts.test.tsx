import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import * as composerFocus from "../panes/session/composer/composerFocus";
import type { SessionZoomParams } from "../panes/zoom/intent";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import {
  installLocationForRoute,
  installMobileViewport,
  installNeedsYouRows,
  LIVE_CYCLE_A,
  LIVE_CYCLE_B,
  navClient,
  navClientWithLive,
  rail,
  warmRoute,
} from "./appShellTestUtils";
import { focusedSessionPaneId, focusedSessionRef } from "./focusedSession";
import { resolveFocusedComposer } from "./holdhints/HoldHints";
import { paletteStore } from "./palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// Await the welcome/session panes' lazy-loaded modules ONCE up front so
// React.lazy resolves from a warm module cache - mirrors App.test.tsx's own
// beforeAll pattern for the same reason: the slow part of lazy-loading is
// the transform/import work, an awaitable completion, not something to race
// with a widened findBy deadline.
beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/settings/Settings");
  await import("../panes/spawn/Spawn");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path) - pre-warmed for the same
  // reason as the two panes above.
  await import("./DockHost");

  // Then render each pane once, for the React.lazy half of the cost - see
  // warmRoute (appShellTestUtils). Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", (wait) => screen.findByText("No session open", undefined, wait));
  await warmRoute("/new", (wait) => screen.findByRole("button", { name: "Start" }, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
  await warmRoute("/settings/hub", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  // notifications/index.ts keeps module state (its init guard, the
  // "reconnect" detector's sawReady, the attention baseline) for every test
  // in this file, and a test that connects a client straight to "ready" arms
  // that detector. Reset and re-init before each test's own fresh connect so
  // every test starts from the state a fresh module evaluation leaves:
  // engine started, seeded from the idle connection and reset navigation
  // store above, with nothing carried over from the previous test.
  resetNotificationsForTests();
  initNotifications();
  navigationStore.setState({ mode: "v3" });
  // afterEach restores Vitest globals; recreate deterministic storage before
  // clearing it so DockHost cannot restore the prior test's layout.
  installLocalStorage(new MemoryStorage());
  localStorage.clear();
  // The settings pane's last-visited-section memory (prefs.ts's
  // lastSettingsSection) is a module-singleton field backed by this same
  // storage: rehydrate against the cleared storage, or one test's settings
  // visit picks the next test's bare-/settings landing section.
  resetPrefsStoreForTests();
  resetSettingsHostForTests();
  // The credential statuses store is a module singleton too: once a test's
  // Providers & credentials section has read it, it re-reads on every later
  // connect, so the next test's client would see an evener/auth/list it never
  // caused.
  resetAuthStatusesStoreForTests();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // The command palette store is a module singleton; reset it so one test's
  // open palette never leaks into the next.
  paletteStore.setState({ open: false, query: "" });
  // Several tests below navigate to /settings/* and mount the real
  // HubSection, whose mount effect touches settingsOverviewStore for
  // real - same module-singleton reasoning as warmRoute's own reset (appShellTestUtils).
  resetSettingsOverviewStoreForTests();
  vi.unstubAllGlobals();
});

test('clicking "New session" navigates to /new and opens the spawn pane', async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  const button = await screen.findByRole("button", { name: "New session" });

  await user.click(button);

  expect(window.location.pathname).toBe("/new");
  // /new now opens the real spawn pane (the old "not available yet" welcome
  // fallback is gone) - its Start button proves the pane mounted.
  // The spawn pane's own submit verb is "Start" - it lives in the prompt
  // card's corner the way Send does in the composer, both surfaces being the
  // same shared card.
  expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
});

// --- command palette wiring (this task) -------------------------------

test("Cmd+K opens the command palette from anywhere in the app", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Meta>}k{/Meta}");

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Ctrl+K opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Control>}k{/Control}");

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

// --- Mod+I focuses the focused session pane's composer (UX fix) ----------

test("Mod+I focuses the focused session pane's composer", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).toHaveBeenCalledWith("local:ref_abc123", workspaceStore.getState().focusedPaneId);
  focusSpy.mockRestore();
});

test("Mod+I is a no-op when the focused pane isn't a session", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
});

// SHOULD-FIX: Mod+I/Mod+J used to fire straight through an open modal - see
// AppShell.tsx's own "BLOCKER fix" comment above onKeyDown.

test("Mod+I is a no-op while the command palette is open", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await user.keyboard("{Meta>}k{/Meta}");
  await screen.findByRole("dialog", { name: "Command palette" });

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
});

test("Mod+I is a no-op while a Dialog/Sheet ([aria-modal=true]) is open", async () => {
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  const focusTarget = document.createElement("button");
  modal.appendChild(focusTarget);
  document.body.appendChild(modal);
  focusTarget.focus();

  fireEvent.keyDown(focusTarget, { key: "i", metaKey: true });

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
  modal.remove();
});

// --- hold hints: the composer chip agrees with composer.focus on a cascade --

test("the composer hold-hint targets a focused cascade's leaf composer, not the neighbouring parent conversation", () => {
  // A parent conversation pane sits beside a focused cascade. composer.focus
  // (AppShell's registerAction) reads the FOCUSED session through
  // focusedSessionRef/focusedSessionPaneId, which name the cascade's LEAF -
  // so the hold-hint chip must anchor to that leaf's composer. Anchoring only
  // ordinary session panes (the pre-fix resolver) missed the cascade and fell
  // back to the first composer in the document, the parent's.
  const parent = document.createElement("div");
  parent.setAttribute("data-composer", "local:parent");
  const child = document.createElement("div");
  child.setAttribute("data-composer", "local:child");
  document.body.append(parent, child);
  const cascadeParams: SessionZoomParams = {
    ref: "local:child",
    source: { type: "session", params: { ref: "local:parent" } },
    edges: [],
  };
  workspaceStore.setState({
    panes: [
      { id: "pane_parent", type: "session", params: { ref: "local:parent" }, slot: "main" },
      { id: "pane_cascade", type: "sessionZoom", params: cascadeParams, slot: "secondary" },
    ],
    focusedPaneId: "pane_cascade",
  });
  try {
    // The shortcut focuses the cascade's leaf ref ...
    expect(focusedSessionRef()).toBe("local:child");
    // ... and the hint resolves to that same leaf's composer, not the parent's.
    expect(resolveFocusedComposer()).toBe(child);
  } finally {
    parent.remove();
    child.remove();
  }
});

test("the composer hold-hint targets the focused pane's composer when a pane and a cascade show the same conversation", () => {
  // An ordinary pane and a cascade column can both show the SAME conversation.
  // composer.focus addresses the focused session's ref AND pane id
  // (focusedSessionPaneId), so with the cascade focused the chip must anchor to
  // the cascade's composer, not the ordinary pane's - even though both anchors
  // carry the same ref and the ordinary pane's anchor comes first in document
  // order.
  const ordinary = document.createElement("div");
  ordinary.setAttribute("data-composer", "local:same");
  ordinary.setAttribute("data-composer-pane", "pane_ordinary");
  const cascade = document.createElement("div");
  cascade.setAttribute("data-composer", "local:same");
  cascade.setAttribute("data-composer-pane", "pane_cascade");
  document.body.append(ordinary, cascade);
  const cascadeParams: SessionZoomParams = {
    ref: "local:same",
    source: { type: "session", params: { ref: "local:same" } },
    edges: [],
  };
  workspaceStore.setState({
    panes: [
      { id: "pane_ordinary", type: "session", params: { ref: "local:same" }, slot: "main" },
      { id: "pane_cascade", type: "sessionZoom", params: cascadeParams, slot: "secondary" },
    ],
    focusedPaneId: "pane_cascade",
  });
  try {
    // The shortcut addresses the cascade's ref AND pane ...
    expect(focusedSessionRef()).toBe("local:same");
    expect(focusedSessionPaneId()).toBe("pane_cascade");
    // ... and the hint resolves to the cascade's composer, not the ordinary
    // pane's (the pre-fix resolver matched the ref alone and took the first).
    expect(resolveFocusedComposer()).toBe(cascade);
  } finally {
    ordinary.remove();
    cascade.remove();
  }
});

test("the composer hold-hint still falls back to the first composer when no session is focused", () => {
  const first = document.createElement("div");
  first.setAttribute("data-composer", "local:first");
  const second = document.createElement("div");
  second.setAttribute("data-composer", "local:second");
  document.body.append(first, second);
  try {
    // No session is focused (the reset workspace focuses none): the chord
    // no-ops, and the pre-change single-composer behavior (the first anchor)
    // holds.
    expect(focusedSessionRef()).toBeNull();
    expect(resolveFocusedComposer()).toBe(first);
  } finally {
    first.remove();
    second.remove();
  }
});

// --- Mod+J cycles needs-you sessions (UX fix) -----------------------------
// (needs-you rows are installed directly via installNeedsYouRows, not via fetch)

test("Mod+J opens the first needs-you session when nothing is focused", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = navClient();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());

  await user.keyboard("{Meta>}j{/Meta}");

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
});

test("Mod+J still works while an editor has focus", async () => {
  installNeedsYouRows();
  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
  const editor = document.createElement("textarea");
  document.body.appendChild(editor);
  editor.focus();

  fireEvent.keyDown(editor, { key: "j", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
  editor.remove();
});

test("Mod+J cycles from the focused needs-you session to the next one, wrapping", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = navClient();
  window.history.pushState({}, "", "/s/local:ny2");
  installLocationForRoute("local:ny2");
  render(<AppShell client={client} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny2" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);

  await user.keyboard("{Meta>}j{/Meta}");

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
});

test("clicking any [data-search-trigger] element opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  // Any element in the app can mark itself as a search trigger; the global
  // listener AppShell installs opens the palette on its click.
  const trigger = document.createElement("button");
  trigger.setAttribute("data-search-trigger", "");
  trigger.textContent = "Search";
  document.body.appendChild(trigger);
  await user.click(trigger);

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
  trigger.remove();
});

test("clicking the rail's own Search button opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  // The real shipped affordance, not a synthetic stand-in: the rail header's
  // icon-only Search button is the app's one clickable way into the palette,
  // so the wiring between it and the global listener is worth an end-to-end
  // assertion of its own.
  await user.click(await screen.findByTestId("rail-search"));

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

// FIX 1 (real-browser bug): Settings' Escape/close used to call
// workspaceStore.closePane directly, which replaces the main pane with
// welcome IN THE STORE but leaves window.location.pathname on /settings/* -
// so AppShell's own route-reconciliation effect (routePlacementIsApplied /
// openRouteAsPane above) sees the URL still asking for settings, decides
// placement has drifted, and reinstates the settings pane right back into
// main. A pane-store-only assertion (Settings.test.tsx's own unit tests)
// can't see this at all, because nothing there mounts AppShell's
// reconciliation effect - hence this integration test through the real
// shell, matching needsYouCycle.ts's own documented rationale for routing
// exits through navigate()/paneToURL rather than poking the store.
test("Escape closes Settings even when focus never entered the pane (gear-click leaves focus in the rail)", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  // The real-browser repro: focus sits outside the settings subtree (the
  // gear button / body), so a React onKeyDown scoped to the pane's div
  // never fires. The exit must listen at the document level.
  fireEvent.keyDown(document.body, { key: "Escape" });

  await screen.findByText("No session open");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
});

test("Escape in Settings exits to welcome and the URL stays there, not reinstated by route reconciliation", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  fireEvent.keyDown(screen.getByRole("button", { name: "Appearance" }), { key: "Escape" });

  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
  // Give the reconciliation effect a chance to run again (it fires off the
  // pathname/tree/workspacePanes deps below) and confirm it did NOT undo it.
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
  });
  expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
});

// --- Alt+ArrowLeft/Right session-pane cycling (webui-keybindings-p3 Task 1)
//
// AppShell registers session.next/session.previous against the keybindings
// registry (desktop only - the rail.toggle inertness pattern); the actions
// drive workspaceStore.focusPane through shell/sessionCycle.ts. These tests
// pin the WIRING (real dispatcher, real defaults, real shell); the cycling
// order/wrap/no-op semantics themselves are sessionCycle.test.ts's.

test("Alt+ArrowRight/Left cycle the open session panes through the shell, wrapping", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  let b = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    b = workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);

  // Wrap: the last session pane's next is the first.
  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);

  await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);
});

test("Alt+Arrow cycling is a no-op with a single session pane open", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let only = "";
  act(() => {
    only = workspaceStore.getState().openPane("session", { ref: "local:cycle-only" });
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(1));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
  await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
});

test("Alt+Arrow cycling is suppressed from an editable target", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  try {
    await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
    expect(workspaceStore.getState().focusedPaneId).toBe(a);
  } finally {
    input.remove();
  }
});

test("mobile: Alt+Arrow cycling registers nothing and is inert", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  vi.unstubAllGlobals();
});

// --- Alt+Shift+ArrowRight/Left live-session navigation
//
// AppShell registers session.liveNext/session.livePrevious against the
// keybindings registry; the actions navigate across the rail's live section
// in server order through shell/rail/liveSessionCycle.ts. These tests pin
// the WIRING (real dispatcher, real defaults, real shell); the cycling
// order/wrap/no-op semantics themselves are liveSessionCycle.test.ts's.

test("Alt+Shift+ArrowRight/Left navigate across the rail's live sessions, wrapping", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-b");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);

  // Wrap: the last live session's next is the first.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-b");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);
});

test("Alt+Shift+Arrow live-session navigation is suppressed from an editable target", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  try {
    await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
    expect(window.location.pathname).toBe("/s/local%3Alive-a");
  } finally {
    input.remove();
  }
});

test("mobile: Alt+Shift+Arrow live-session navigation registers nothing and is inert", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  vi.unstubAllGlobals();
});
