// Tests for the hold-modifier hints (Phase 4a, the p4 plan's Design
// decision 4): the ~400ms modifier-alone hold shows the chips, every cleanup
// path hides them (modifier keyup, window blur, visibilitychange, any
// non-modifier keydown, and the hard-timeout backstop), the listeners are
// observers only (no preventDefault, no propagation interference), the
// reduced-motion instant path, registry-sourced effective chords (an applied
// override shows its own chord), and mobile inertness (AppShell mounts the
// component only off mobile - the CheatsheetOverlay pattern, so no listener
// is ever installed on touch).
//
// jsdom's navigator.platform is "" (keybindings/chord.test.ts documents it),
// so currentKeybindingsPlatform() resolves "other" and the tracked modifier
// is Control.
//
// The AppShell render/warm-up/store-reset scaffolding for the mobile test
// mirrors CheatsheetOverlay.test.tsx's own (helpers duplicated, not shared -
// this project has no cross-test-file test-utils module; see that file's
// header note on the convention).

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ACTIONS } from "../../keybindings/actions";
import { registerDefaultBindings } from "../../keybindings/defaults";
import { keybindingsRegistry } from "../../keybindings/registry";
import { resetNotificationsForTests } from "../../notifications";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { connectionStore } from "../../stores/connection";
import { resetKeybindingsStoreForTests } from "../../stores/keybindings";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetPrefsStoreForTests } from "../../stores/prefs";
import { AppShell } from "../AppShell";
import { paletteStore } from "../palette/paletteController";
import { resetMobileViewportForTests } from "../useIsMobile";
import { resetWorkspaceStoreForTests } from "../workspace";
import { HoldHints } from "./HoldHints";
import { HARD_TIMEOUT_MS, HOLD_THRESHOLD_MS, holdHintsStore, resetHoldHintsForTests } from "./holdHintsController";

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

// CheatsheetOverlay.test.tsx's installMobileViewport, verbatim: the mobile
// query matches, every other query (the reduced-motion read included) does
// not.
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

/** The live affordances the chips anchor to (their data attributes are the
 * contract - shell/holdhints/HoldHints.tsx's ANCHORS). */
function addAnchors(): void {
  for (const attr of ["data-search-trigger", "data-rail-toggle", "data-session-tab", "data-composer"]) {
    const element = document.createElement("div");
    element.setAttribute(attr, "");
    document.body.appendChild(element);
  }
}

function removeAnchors(): void {
  for (const attr of ["data-search-trigger", "data-rail-toggle", "data-session-tab", "data-composer"]) {
    document.querySelector(`[${attr}]`)?.remove();
  }
}

function hintRoot(): HTMLElement | null {
  return document.querySelector("[data-hold-hints]");
}

function holdModifier(): void {
  fireEvent.keyDown(document.body, { key: "Control", code: "ControlLeft", ctrlKey: true });
}

function showHints(): void {
  holdModifier();
  act(() => vi.advanceTimersByTime(HOLD_THRESHOLD_MS));
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
  resetHoldHintsForTests();
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  removeAnchors();
  window.history.pushState({}, "", "/");
  paletteStore.setState({ open: false, query: "" });
  resetHoldHintsForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetNavigationStoreForTests();
  resetKeybindingsStoreForTests();
  resetNotificationsForTests();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test("hints appear after the hold threshold with the modifier held - not before", () => {
  addAnchors();
  render(<HoldHints />);
  expect(hintRoot()).toBeNull();

  holdModifier();
  act(() => vi.advanceTimersByTime(HOLD_THRESHOLD_MS - 1));
  expect(hintRoot()).toBeNull();

  act(() => vi.advanceTimersByTime(1));
  const root = hintRoot();
  if (root === null) throw new Error("hints did not appear at the threshold");
  // One chip per mounted anchor; jsdom resolves $mod to Control.
  const palette = within(root).getByText("K").closest("[data-hint]");
  expect(palette?.getAttribute("data-hint")).toBe("palette");
  expect(within(root).getByText("B").closest("[data-hint]")?.getAttribute("data-hint")).toBe("rail-toggle");
  expect(within(root).getByText("I").closest("[data-hint]")?.getAttribute("data-hint")).toBe("composer");
  const tabs = root.querySelector('[data-hint="session-tabs"]');
  if (tabs === null) throw new Error("no session-tabs chip");
  // The tab chip carries BOTH cycling chords.
  expect(within(tabs as HTMLElement).getByText("ArrowLeft")).toBeTruthy();
  expect(within(tabs as HTMLElement).getByText("ArrowRight")).toBeTruthy();
});

test("modifier keyup hides the hints (and a release before the threshold never shows them)", () => {
  addAnchors();
  render(<HoldHints />);

  // Released early: no chips.
  holdModifier();
  act(() => vi.advanceTimersByTime(HOLD_THRESHOLD_MS - 100));
  fireEvent.keyUp(document.body, { key: "Control", code: "ControlLeft" });
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();

  // Shown, then released: gone.
  showHints();
  expect(hintRoot()).not.toBeNull();
  fireEvent.keyUp(document.body, { key: "Control", code: "ControlLeft" });
  expect(hintRoot()).toBeNull();
  expect(holdHintsStore.getState().visible).toBe(false);
});

test("window blur hides the hints and cancels a pending hold", () => {
  addAnchors();
  render(<HoldHints />);

  showHints();
  expect(hintRoot()).not.toBeNull();
  fireEvent(window, new Event("blur"));
  expect(hintRoot()).toBeNull();

  holdModifier();
  fireEvent(window, new Event("blur"));
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();
});

test("visibilitychange hides the hints and cancels a pending hold", () => {
  addAnchors();
  render(<HoldHints />);

  showHints();
  expect(hintRoot()).not.toBeNull();
  fireEvent(document, new Event("visibilitychange"));
  expect(hintRoot()).toBeNull();

  holdModifier();
  fireEvent(document, new Event("visibilitychange"));
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();
});

test("any non-modifier keydown hides the hints and cancels a pending hold", () => {
  addAnchors();
  render(<HoldHints />);

  // A chord starting while visible hides them (on macOS this keydown may be
  // the ONLY event the chord ever delivers - the keyup is not delivered).
  showHints();
  expect(hintRoot()).not.toBeNull();
  fireEvent.keyDown(document.body, { key: "k", code: "KeyK", ctrlKey: true });
  expect(hintRoot()).toBeNull();

  // A key pressed during the pending window cancels the hold outright.
  holdModifier();
  act(() => vi.advanceTimersByTime(100));
  fireEvent.keyDown(document.body, { key: "x", code: "KeyX", ctrlKey: true });
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();
});

test("the hard timeout hides the hints even when every event-based cleanup missed", () => {
  addAnchors();
  render(<HoldHints />);

  showHints();
  expect(hintRoot()).not.toBeNull();
  // No keyup, no blur, nothing - the backstop alone must end it.
  act(() => vi.advanceTimersByTime(HARD_TIMEOUT_MS - 1));
  expect(hintRoot()).not.toBeNull();
  act(() => vi.advanceTimersByTime(1));
  expect(hintRoot()).toBeNull();
  // And the lapsed hold does not re-arm itself from modifier auto-repeat.
  fireEvent.keyDown(document.body, { key: "Control", code: "ControlLeft", ctrlKey: true, repeat: true });
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();
});

test("the hold counts only with the modifier held ALONE", () => {
  addAnchors();
  render(<HoldHints />);

  fireEvent.keyDown(document.body, { key: "Control", code: "ControlLeft", ctrlKey: true, shiftKey: true });
  act(() => vi.advanceTimersByTime(2 * HOLD_THRESHOLD_MS));
  expect(hintRoot()).toBeNull();
});

test("the listeners are observers only: no preventDefault, no propagation interference", () => {
  addAnchors();
  const seen: KeyboardEvent[] = [];
  // Attached BEFORE <HoldHints/> mounts, on a different target (document
  // sits earlier in the bubble path than window): any stopPropagation or
  // stopImmediatePropagation from the hold listeners would show here.
  const recorder = (event: KeyboardEvent) => seen.push(event);
  document.addEventListener("keydown", recorder);
  render(<HoldHints />);

  fireEvent.keyDown(document.body, { key: "Control", code: "ControlLeft", ctrlKey: true });
  act(() => vi.advanceTimersByTime(HOLD_THRESHOLD_MS));
  expect(hintRoot()).not.toBeNull();

  for (const event of seen) {
    expect(event.defaultPrevented).toBe(false);
  }
  expect(seen.some((event) => event.key === "Control")).toBe(true);

  // And the dispatcher still owns its chords while the hints are visible:
  // ⌘K reaches it untouched (the hints' own keydown observer hides them).
  const openPalette = vi.fn();
  const unregister = keybindingsRegistry.getState().registerAction(ACTIONS.paletteOpen, () => {
    openPalette();
  });
  fireEvent.keyDown(document.body, { key: "k", code: "KeyK", ctrlKey: true });
  expect(openPalette).toHaveBeenCalledTimes(1);
  unregister();
  document.removeEventListener("keydown", recorder);
});

test("chips show the EFFECTIVE chord: an applied override replaces the default", () => {
  addAnchors();
  const registry = keybindingsRegistry.getState();
  registry.unregisterBinding(ACTIONS.paletteOpen);
  registry.unregisterBinding(`${ACTIONS.paletteOpen}#mod-twin`);
  registry.registerBinding({
    id: `${ACTIONS.paletteOpen}#override`,
    actionId: ACTIONS.paletteOpen,
    chord: "Control+P",
  });
  render(<HoldHints />);

  showHints();
  const root = hintRoot();
  if (root === null) throw new Error("hints did not appear");
  const chip = root.querySelector('[data-hint="palette"]');
  if (chip === null) throw new Error("no palette chip");
  expect(within(chip as HTMLElement).getByText("P")).toBeTruthy();
  expect(within(chip as HTMLElement).queryByText("K")).toBeNull();
});

test("an unbound action renders no chip, and a missing anchor renders none either", () => {
  addAnchors();
  const registry = keybindingsRegistry.getState();
  // Unbound-by-override: palette.open's effective binding set is empty.
  for (const binding of registry.bindings.filter((b) => b.actionId === ACTIONS.paletteOpen)) {
    registry.unregisterBinding(binding.id);
  }
  registry.registerBinding({ id: `${ACTIONS.paletteOpen}#override`, actionId: ACTIONS.paletteOpen, chord: [] });
  // The composer's anchor is not mounted at all in this test.
  document.querySelector("[data-composer]")?.remove();
  render(<HoldHints />);

  showHints();
  const root = hintRoot();
  if (root === null) throw new Error("hints did not appear");
  expect(root.querySelector('[data-hint="palette"]')).toBeNull();
  expect(root.querySelector('[data-hint="composer"]')).toBeNull();
  expect(root.querySelector('[data-hint="rail-toggle"]')).not.toBeNull();
});

test("prefers-reduced-motion marks the root for the instant (no-fade) path", () => {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(prefers-reduced-motion: reduce)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
  addAnchors();
  render(<HoldHints />);
  showHints();
  expect(hintRoot()?.hasAttribute("data-reduced-motion")).toBe(true);
});

test("without the reduced-motion preference the root is unmarked", () => {
  // jsdom has no matchMedia at all: the guarded read degrades to false.
  addAnchors();
  render(<HoldHints />);
  showHints();
  expect(hintRoot()?.hasAttribute("data-reduced-motion")).toBe(false);
});

test("mobile: no listeners installed, no chips on a touch viewport", async () => {
  // Real timers: RTL's findByText cannot advance vitest's fake ones, and
  // with no listeners installed there is no pending hold timer to advance.
  vi.useRealTimers();
  installMobileViewport();
  resetMobileViewportForTests();
  addAnchors();
  const addListener = vi.spyOn(window, "addEventListener");
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open", undefined, { timeout: 10_000 });

  // The component never mounted: nothing subscribed to key/keyup/blur.
  const watchedTypes = new Set(addListener.mock.calls.map(([type]) => type));
  expect(watchedTypes.has("keyup")).toBe(false);
  expect(watchedTypes.has("blur")).toBe(false);

  // And the hold does nothing.
  holdModifier();
  expect(holdHintsStore.getState().visible).toBe(false);
  expect(hintRoot()).toBeNull();
});
