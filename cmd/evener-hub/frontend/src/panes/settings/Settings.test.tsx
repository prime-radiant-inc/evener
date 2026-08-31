import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { chromeStore, resetChromeStoreForTests } from "../../shell/chromeStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests } from "../../stores/credentials";
import Settings from "./Settings";

// jsdom does not implement window.matchMedia at all - useIsMobile.test.ts's
// own header comment documents this; every test file that drives mobile
// layout stubs it locally (no shared test-utils module in this project -
// see stores/threads.test.ts's own precedent for duplicating rather than
// sharing this kind of helper).
function stubMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockReturnValue({
    matches,
    media: "",
    addEventListener: () => {},
    removeEventListener: () => {},
  }) as unknown as typeof window.matchMedia;
}

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // @ts-expect-error restores jsdom's own honest default between tests.
  delete window.matchMedia;
  resetWorkspaceStoreForTests();
  resetChromeStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

// FIX 1: settings has no way out other than clicking a session in the rail
// (Escape does nothing, no close affordance) - the main slot's tab bar is
// deliberately close-less (DockHost.test.tsx), so the exit lives in the
// pane's own header + Escape instead.
function seedOpenSettingsPane() {
  workspaceStore.setState({
    panes: [{ id: "settings-1", type: "settings", params: {}, slot: "main" }],
    focusedPaneId: "settings-1",
  });
}

test("a close button in the header closes this pane", async () => {
  stubMatchMedia(false);
  seedOpenSettingsPane();
  const user = userEvent.setup();
  render(<Settings params={{}} paneId="settings-1" focused={true} />);

  await user.click(screen.getByRole("button", { name: "Close settings" }));

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain("settings-1");
});

test("Escape closes this pane", () => {
  stubMatchMedia(false);
  seedOpenSettingsPane();
  render(<Settings params={{}} paneId="settings-1" focused={true} />);

  fireEvent.keyDown(screen.getByRole("button", { name: "General" }), { key: "Escape" });

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain("settings-1");
});

// FIX 2: this used to simulate "something inside settings already handled
// Escape" with a hand-installed document-level preventDefault listener - a
// stand-in for the real thing, not the real thing. That stand-in couldn't
// have caught OverlayPanel forgetting to call preventDefault on its own
// Escape handling (see OverlayPanel.tsx's handleKeyDown), because it never
// exercised OverlayPanel at all. These render a REAL Dialog (credentials'
// AddInstanceDialog, reached exactly as a user would - the "+ Add provider
// instance" button) inside Settings' actual section content, so the keydown
// that bubbles up to Settings' own handleKeyDown is the genuine one
// OverlayPanel produces.
function connectFakeClientWithNoInstances(): void {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => ({
    instances: [],
    availableProviders: [{ id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true }],
  }));
  connectionStore.getState().connect(fake);
}

test("Escape closes a real Dialog open inside a settings section, not the settings pane", async () => {
  stubMatchMedia(false);
  seedOpenSettingsPane();
  connectFakeClientWithNoInstances();
  const user = userEvent.setup();
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  await user.click(await screen.findByRole("button", { name: "+ Add provider instance" }));
  expect(screen.getByRole("dialog")).toBeTruthy();

  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(workspaceStore.getState().panes.map((p) => p.id)).toContain("settings-1");
});

test("Escape closes the settings pane when no dialog is open inside it", async () => {
  stubMatchMedia(false);
  seedOpenSettingsPane();
  connectFakeClientWithNoInstances();
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  const addButton = await screen.findByRole("button", { name: "+ Add provider instance" });
  expect(screen.queryByRole("dialog")).toBeNull();

  fireEvent.keyDown(addButton, { key: "Escape" });

  expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain("settings-1");
});

test("a non-Escape key does not close this pane", () => {
  stubMatchMedia(false);
  seedOpenSettingsPane();
  render(<Settings params={{}} paneId="settings-1" focused={true} />);

  fireEvent.keyDown(screen.getByRole("button", { name: "General" }), { key: "a" });

  expect(workspaceStore.getState().panes.map((p) => p.id)).toContain("settings-1");
});

test("bare params show the default (General) section", () => {
  stubMatchMedia(false);
  render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("button", { name: "General" })).toBeTruthy();
  // General's own nav link is the active one.
  expect(screen.getByRole("button", { name: "General" }).getAttribute("aria-current")).toBe("page");
});

test("params.section selects that section", () => {
  stubMatchMedia(false);
  render(<Settings params={{ section: "theme" }} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("button", { name: "Theme" }).getAttribute("aria-current")).toBe("page");
});

test("desktop: nav and content render simultaneously, with no back button", () => {
  stubMatchMedia(false);
  render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "General" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back to settings" })).toBeNull();
});

test("desktop: clicking a nav link requests navigation to that section's URL", async () => {
  // Settings.tsx's own `params` are owned by its caller (in the real app,
  // workspaceStore via DockHost) - re-rendering with a NEW activeId after
  // navigate() is that integration's job, exercised in AppShell.test.tsx
  // (mirroring how Welcome.test.tsx's own "clicking New session" test only
  // checks the URL, not a re-render of Welcome itself). This isolated
  // render only proves Settings.tsx requests the right URL.
  stubMatchMedia(false);
  const user = userEvent.setup();
  render(<Settings params={{}} paneId="settings-1" focused={true} />);

  await user.click(screen.getByRole("button", { name: "Storage" }));

  expect(window.location.pathname).toBe("/settings/storage");
});

// --- mobile (<900px): URL-derived master-detail (2026-08-16 design) --------
//
// On mobile the settings pane is two full-screen levels derived from the URL,
// not from component state: bare /settings is the section LIST (the root),
// /settings/{section} is that section's DETAIL. Back lives in the shell's
// top bar via the chrome store's paneBack channel (StackHost reads it) -
// there is no in-content back button. The list stays MOUNTED while a detail
// shows (CSS-hidden, never unmounted), so its filter text and scroll
// position survive the round trip.

test("mobile: bare params show the section list as the root view, not a section's content", () => {
  stubMatchMedia(true);
  render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.queryByTestId("settings-content")).toBeNull();
});

test("mobile: a section param shows that section's content", () => {
  stubMatchMedia(true);
  render(<Settings params={{ section: "bogus" }} paneId="settings-1" focused={true} />);
  expect(screen.getByTestId("settings-content")).toBeTruthy();
  expect(screen.getByText("This section hasn't been built yet.")).toBeTruthy();
});

test("mobile: the section detail has no in-content back button - back lives in the shell top bar", () => {
  stubMatchMedia(true);
  render(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);
  expect(screen.queryByRole("button", { name: "Back to settings" })).toBeNull();
});

test("mobile: a focused section publishes a paneBack to the section list; the list publishes none", () => {
  stubMatchMedia(true);
  const { rerender } = render(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);

  const paneBack = chromeStore.getState().paneBack;
  expect(paneBack).toBeTypeOf("function");
  act(() => paneBack?.());
  expect(window.location.pathname).toBe("/settings");

  // The list is the drill-down's ROOT: nothing further up to publish, so the
  // shell's Back keeps its ordinary meaning (exit settings) there.
  rerender(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(chromeStore.getState().paneBack).toBeNull();
});

test("mobile: the section list publishes 'Settings' as the pane title; a focused section publishes its label", () => {
  stubMatchMedia(true);
  const { rerender } = render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(chromeStore.getState().paneTitle).toBe("Settings");

  rerender(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);
  expect(chromeStore.getState().paneTitle).toBe("Hub");
});

test("mobile: the list stays mounted across a drill-down round trip - its filter text survives", async () => {
  stubMatchMedia(true);
  const user = userEvent.setup();
  const { rerender } = render(<Settings params={{}} paneId="settings-1" focused={true} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "stor");

  // Drill in: the link requests the URL; AppShell's routing glue owns the
  // params update that follows (replacePrimary on popstate) - in this
  // isolated render that step is done by hand, the same idiom the desktop
  // navigation test above documents.
  await user.click(screen.getByRole("button", { name: "Storage" }));
  expect(window.location.pathname).toBe("/settings/storage");
  rerender(<Settings params={{ section: "storage" }} paneId="settings-1" focused={true} />);

  expect(screen.getByRole("searchbox", { name: "Filter settings" })).toHaveProperty("value", "stor");
  expect(screen.getByTestId("settings-content")).toBeTruthy();

  // Back out the way the shell's top bar takes us: the published paneBack.
  act(() => chromeStore.getState().paneBack?.());
  expect(window.location.pathname).toBe("/settings");
  rerender(<Settings params={{}} paneId="settings-1" focused={true} />);

  expect(screen.getByRole("searchbox", { name: "Filter settings" })).toHaveProperty("value", "stor");
  expect(screen.queryByTestId("settings-content")).toBeNull();
});

test("the pane title reflects the focused section", () => {
  stubMatchMedia(false);
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("heading", { name: "Providers & credentials" })).toBeTruthy();
});
