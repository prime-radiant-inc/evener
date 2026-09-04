// Tests for the cheatsheet overlay (Phase 4a, the p4 plan's Design
// decision 3): trigger chords (⌘/ primary, "?" secondary and
// pref-conditional), editable-target policy, Escape/⌘/ toggle-close,
// registry-sourced rows (an applied override shows its own chord), the
// prefs-persisted character-key toggle, and mobile inertness (AppShell
// mounts the overlay only off mobile, so no action registers and the chords
// stay inert - the rail.toggle no-registration pattern).
//
// The AppShell render/warm-up/store-reset scaffolding below mirrors
// shell/keybindingsMigration.test.tsx's own (helpers duplicated, not
// shared - this project has no cross-test-file test-utils module; see that
// file's and AppShell.test.tsx's notes on the convention).

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ACTIONS } from "../../keybindings/actions";
import {
  CHARACTER_KEY_TRIGGER_BINDING_ID,
  CHEATSHEET_SCOPE,
  DEFAULT_BINDINGS,
  registerDefaultBindings,
} from "../../keybindings/defaults";
import { keybindingsRegistry } from "../../keybindings/registry";
import { initNotifications, resetNotificationsForTests } from "../../notifications";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../stores/keybindings";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { AppShell } from "../AppShell";
import { paletteStore } from "../palette/paletteController";
import { resetMobileViewportForTests } from "../useIsMobile";
import { resetWorkspaceStoreForTests } from "../workspace";
import { CheatsheetOverlay } from "./CheatsheetOverlay";
import { cheatsheetStore, closeCheatsheet, openCheatsheet } from "./cheatsheetController";

// See AppShell.test.tsx for why both stubs are needed (jsdom has no
// ResizeObserver; Node 26 shadows jsdom's localStorage accessor).
class StubResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
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

// jsdom implements no matchMedia at all (useIsMobile.test.ts documents the
// probe), so the mobile test installs one: the mobile query matches, every
// other query does not - AppShell.test.tsx's installMobileViewport, verbatim.
function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

function resetRegistryToDefaults(): void {
  for (const binding of keybindingsRegistry.getState().bindings) {
    keybindingsRegistry.getState().unregisterBinding(binding.id);
  }
  registerDefaultBindings(keybindingsRegistry);
}

function overridesPayload(revision: number, rules: KeybindingsRule[]): KeybindingsOverrides {
  return { version: 1, revision, rules };
}

async function wireClient(client: FakeClient, supported: boolean): Promise<void> {
  connectionStore.getState().connect(client);
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: supported },
  });
  await keybindingsStore.getState().refreshOverrides();
}

function questionTriggerRegistered(): boolean {
  return keybindingsRegistry.getState().bindings.some((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
}

async function openDialog(): Promise<HTMLElement> {
  return screen.findByRole("dialog", { name: "Keyboard shortcuts" });
}

beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver;
  // @ts-expect-error MemoryStorage deliberately implements only the Storage
  // methods the stores actually call - see AppShell.test.tsx's own stub.
  globalThis.localStorage = new MemoryStorage();
  // Await the lazy pane/dock modules once up front, then pay react-dom's
  // per-boundary fallback throttle in one warm render - see
  // keybindingsMigration.test.tsx's beforeAll for the full reasoning.
  await import("../../panes/welcome/Welcome");
  await import("../DockHost");
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open", undefined, { timeout: 10_000 });
  cleanup();
  resetWorkspaceStoreForTests();
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetKeybindingsStoreForTests();
  // @ts-expect-error see the beforeAll stub.
  globalThis.localStorage = new MemoryStorage();
  localStorage.clear();
  resetPrefsStoreForTests();
  resetRegistryToDefaults();
  closeCheatsheet();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  paletteStore.setState({ open: false, query: "" });
  closeCheatsheet();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetNavigationStoreForTests();
  resetKeybindingsStoreForTests();
  resetNotificationsForTests();
  initNotifications();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test("opens on ⌘/ and lists every action with its effective chord, grouped", async () => {
  render(<CheatsheetOverlay />);
  fireEvent.keyDown(document.body, { key: "/", code: "Slash", ctrlKey: true });

  const dialog = await openDialog();
  for (const group of ["Sessions", "Transcript", "Composer", "General"]) {
    expect(within(dialog).getByText(group)).toBeTruthy();
  }
  // One row per action (the overlay's two trigger entries share one action
  // id), sourced live from the default map via keybindings/display.ts.
  const rows = within(dialog).getAllByRole("listitem");
  expect(rows).toHaveLength(new Set(DEFAULT_BINDINGS.map((b) => b.actionId)).size);
  // jsdom resolves $mod to Control: palette.open's row shows Ctrl + K, and
  // the cheatsheet row shows Ctrl + / with the "?" trigger alongside.
  const paletteRow = within(dialog).getByText("Open the command palette").closest("li");
  if (paletteRow === null) throw new Error("no palette row");
  expect(within(paletteRow).getByText("Ctrl")).toBeTruthy();
  expect(within(paletteRow).getByText("K")).toBeTruthy();
  const cheatsheetRow = within(dialog).getByText("Show the keyboard shortcuts overlay").closest("li");
  if (cheatsheetRow === null) throw new Error("no cheatsheet row");
  expect(within(cheatsheetRow).getByText("/")).toBeTruthy();
  expect(within(cheatsheetRow).getByText("?")).toBeTruthy();
  // The hold-hints footer line (the feature itself is the next task).
  expect(within(dialog).getByText(/to see hints/)).toBeTruthy();
});

test("opens on ? outside editable targets", async () => {
  render(<CheatsheetOverlay />);
  fireEvent.keyDown(document.body, { key: "?", code: "Slash", shiftKey: true });
  await openDialog();
});

test("? does NOT fire inside an editable target (it is a printable character)", () => {
  render(<CheatsheetOverlay />);
  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  fireEvent.keyDown(input, { key: "?", code: "Slash", shiftKey: true });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(cheatsheetStore.getState().open).toBe(false);
  input.remove();
});

test("⌘/ fires inside an editable target (it never collides with typing)", async () => {
  render(<CheatsheetOverlay />);
  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  fireEvent.keyDown(input, { key: "/", code: "Slash", ctrlKey: true });
  await openDialog();
  input.remove();
});

test("? does NOT fire while the character-key toggle is off, and comes back when it returns", async () => {
  render(<CheatsheetOverlay />);
  act(() => prefsStore.getState().setCharacterKeyTriggers(false));

  // The binding is unregistered, not merely suppressed - and the open
  // overlay's row stops offering "?".
  expect(questionTriggerRegistered()).toBe(false);
  fireEvent.keyDown(document.body, { key: "?", code: "Slash", shiftKey: true });
  expect(screen.queryByRole("dialog")).toBeNull();

  // ⌘/ is unaffected (it is not a character-key trigger).
  fireEvent.keyDown(document.body, { key: "/", code: "Slash", ctrlKey: true });
  const dialog = await openDialog();
  const row = within(dialog).getByText("Show the keyboard shortcuts overlay").closest("li");
  if (row === null) throw new Error("no cheatsheet row");
  expect(within(row).queryByText("?")).toBeNull();
  expect(within(row).getByText("/")).toBeTruthy();
  act(() => closeCheatsheet());

  act(() => prefsStore.getState().setCharacterKeyTriggers(true));
  expect(questionTriggerRegistered()).toBe(true);
  fireEvent.keyDown(document.body, { key: "?", code: "Slash", shiftKey: true });
  await openDialog();
});

test("a pref seeded off before mount never registers the ? trigger", () => {
  localStorage.setItem("evener.prefs.characterKeyTriggers", "0");
  resetPrefsStoreForTests();
  render(<CheatsheetOverlay />);
  expect(questionTriggerRegistered()).toBe(false);
  fireEvent.keyDown(document.body, { key: "?", code: "Slash", shiftKey: true });
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Escape closes the overlay (and the cheatsheet scope is live only while open)", async () => {
  render(<CheatsheetOverlay />);
  expect(keybindingsRegistry.getState().scopeStack).not.toContain(CHEATSHEET_SCOPE);
  act(() => openCheatsheet());
  const dialog = await openDialog();
  expect(keybindingsRegistry.getState().scopeStack).toContain(CHEATSHEET_SCOPE);

  // The OverlayPanel's own Escape handler owns the close (focus is trapped
  // inside the dialog); it preventDefaults, which the scope-gated
  // cheatsheet.close binding then honors via ignoreIfDefaultPrevented.
  const target = document.activeElement ?? dialog;
  fireEvent.keyDown(target, { key: "Escape", code: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(keybindingsRegistry.getState().scopeStack).not.toContain(CHEATSHEET_SCOPE);
});

test("⌘/ toggles the overlay closed while it is open", async () => {
  render(<CheatsheetOverlay />);
  act(() => openCheatsheet());
  const dialog = await openDialog();
  // Fired from inside the dialog (an aria-modal target): the binding's
  // allowInModal is what lets the same chord close what it opened.
  const target = document.activeElement ?? dialog;
  fireEvent.keyDown(target, { key: "/", code: "Slash", ctrlKey: true });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("rows reflect an applied hub override (registry-sourced, not hardcoded)", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  await wireClient(client, true);
  render(<CheatsheetOverlay />);
  act(() => openCheatsheet());

  const dialog = await openDialog();
  const paletteRow = within(dialog).getByText("Open the command palette").closest("li");
  if (paletteRow === null) throw new Error("no palette row");
  expect(within(paletteRow).getByText("P")).toBeTruthy();
  expect(within(paletteRow).queryByText("K")).toBeNull();
});

test("mobile: nothing mounts, registers, or intercepts on a touch viewport", async () => {
  installMobileViewport();
  resetMobileViewportForTests();
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open", undefined, { timeout: 10_000 });

  // No overlay mounted, no action registered - the trigger chords are inert.
  expect(keybindingsRegistry.getState().actions.get(ACTIONS.cheatsheetToggle)).toBeUndefined();
  fireEvent.keyDown(document.body, { key: "/", code: "Slash", ctrlKey: true });
  fireEvent.keyDown(document.body, { key: "?", code: "Slash", shiftKey: true });
  // (StackHost's own "Sessions" drawer is a role=dialog too - the assertion
  // is specifically that the CHEATSHEET never appears.)
  expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).toBeNull();
  expect(cheatsheetStore.getState().open).toBe(false);
});
