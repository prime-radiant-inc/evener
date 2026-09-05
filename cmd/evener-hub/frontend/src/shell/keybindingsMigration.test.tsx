// Migration tests for the keybindings dispatcher wiring (Task 2 of the
// webui-keybindings-p2a plan): the six shell chords moved from six ad-hoc
// keydown listeners (AppShell, RailHost, Settings, SelectionQuote) onto the
// single window-level dispatcher (src/keybindings/, installed by
// shell/installKeybindings.ts). These tests pin the chord-policy contract
// that the pre-existing suites do NOT already cover: firing-or-not from
// editable targets, from open modals (per-binding allowInModal), while the
// palette itself is open, on IME composition keydowns, and against a keydown
// another handler already claimed - plus the one deliberate non-migration
// (FocusScope's Tab trap is untouched).
//
// The AppShell render/warm-up/store-reset scaffolding below mirrors
// AppShell.test.tsx's own (helpers duplicated, not shared - this project has
// no cross-test-file test-utils module; see that file's and
// stores/threads.test.ts's own notes on the convention).
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import * as composerFocus from "../panes/session/composer/composerFocus";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { NavigationReadParams, NavigationReadResponse, NavigationSessionLocation } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { keyID } from "../stores/navigation/types";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { FocusScope } from "../widgets/focusscope";
import { AppShell } from "./AppShell";
import { closePalette, paletteStore } from "./palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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

const TREE_SESSION = {
  row_id: "project:proj1:local:s1",
  ref: "local:s1",
  host_id: "local",
  session_id: "s1",
  title: "Session one",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

function navigationRead(params: NavigationReadParams): NavigationReadResponse {
  const envelope = (data: unknown): NavigationReadResponse => ({
    status: "ok",
    generationId: "generation_test",
    revision: 1,
    etag: '"test"',
    data,
  });
  if (params.resource === "location") {
    return envelope({
      generation_id: "generation_test",
      revision: 1,
      ref: params.ref,
      top_level_ref: params.ref,
      top_level: true,
      session: { ...TREE_SESSION, ref: params.ref, session_id: params.ref },
    });
  }
  throw new Error(`unsupported navigation resource: ${params.resource}`);
}

// A FakeClient whose connect() advertises v1 navigation - see AppShell.
// test.tsx's own navClient comment for why a bare FakeClient("ready") leaves
// the navigation store in "error" mode instead.
function navClient(): FakeClient {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", navigationRead);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0 },
  }));
  return client;
}

function installLocationForRoute(ref: string): void {
  const location: NavigationSessionLocation = {
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: ref,
    top_level: true,
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title: ref,
      project: "test-project",
      state: "idle",
      kind: "session",
      live: false,
      children: [],
    },
  };
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: location,
    loadedRevision: location.revision,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: location.generation_id,
  });
  navigationStore.setState({ mode: "v1", clientGenerationID: location.generation_id, resources });
}

function installNeedsYouRows(): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = [
    { ...TREE_SESSION, ref: "local:ny1", session_id: "ny1", title: "Needs you one", state: "awaiting" },
    { ...TREE_SESSION, ref: "local:ny2", session_id: "ny2", title: "Needs you two", state: "awaiting" },
  ];
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  navigationStore.setState({ mode: "v1", resources });
}

// A focused plain <input> appended beside the shell: the editable target the
// editable-policy tests fire their chords from. Returns a cleanup.
function focusedInput(): { input: HTMLInputElement; remove: () => void } {
  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  return { input, remove: () => input.remove() };
}

// An [aria-modal="true"] stand-in for an OverlayPanel Dialog/Sheet, with a
// focusable element inside so chords can be fired from within it.
function openFakeModal(): { modal: HTMLElement; inside: HTMLButtonElement; close: () => void } {
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  const inside = document.createElement("button");
  modal.appendChild(inside);
  document.body.appendChild(modal);
  inside.focus();
  return { modal, inside, close: () => modal.remove() };
}

beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver;
  // @ts-expect-error MemoryStorage deliberately implements only the Storage
  // methods the stores actually call - see AppShell.test.tsx's own stub.
  globalThis.localStorage = new MemoryStorage();
  // Await the lazy pane/dock modules once up front, then pay react-dom's
  // per-boundary fallback throttle in one warm render per route shape - see
  // AppShell.test.tsx's warmRoute for the full reasoning (the cost is real
  // and awaitable here; inside a test it would eat the assertion window).
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/settings/Settings");
  await import("./DockHost");
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open", undefined, { timeout: 10_000 });
  cleanup();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" }, { timeout: 10_000 });
  cleanup();
  resetWorkspaceStoreForTests();
  resetSettingsOverviewStoreForTests();
  window.history.pushState({}, "", "/s/local:warm");
  installLocationForRoute("local:warm");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i, undefined, { timeout: 10_000 });
  cleanup();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v1" });
  resetPrefsStoreForTests();
  // @ts-expect-error see the beforeAll stub.
  globalThis.localStorage = new MemoryStorage();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  paletteStore.setState({ open: false, query: "" });
  resetSettingsOverviewStoreForTests();
  // Same module-singleton reasoning as AppShell.test.tsx's afterEach: the
  // notifications engine's reconnect detector must not carry one test's
  // ready-client into the next.
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  resetNotificationsForTests();
  initNotifications();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- editable targets -------------------------------------------------------

test("Mod+K fires while an input is focused", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const { input, remove } = focusedInput();

  fireEvent.keyDown(input, { key: "k", metaKey: true });

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
  remove();
});

test("Mod+I fires while an input is focused", async () => {
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  const { input, remove } = focusedInput();

  fireEvent.keyDown(input, { key: "i", metaKey: true });

  expect(focusSpy).toHaveBeenCalledWith("local:ref_abc123");
  remove();
});

test("Mod+J fires while an input is focused", async () => {
  installNeedsYouRows();
  render(<AppShell client={navClient()} />);
  await screen.findByText("No session open");
  const { input, remove } = focusedInput();

  fireEvent.keyDown(input, { key: "j", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
  remove();
});

// settings.open (Phase 4a, the p4 plan's Design decision 2): ⌘, runs exactly
// what the palette's "settings" command runs - navigate("/settings") - and
// fires from editable targets (⌘, never collides with typing).
test("Mod+, opens the Settings pane while an input is focused", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const { input, remove } = focusedInput();

  fireEvent.keyDown(input, { key: ",", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("settings"));
  expect(window.location.pathname).toBe("/settings");
  remove();
});

test("Mod+B is suppressed from editable targets (Ctrl+B keeps its emacs meaning)", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const { input, remove } = focusedInput();

  const metaEvent = new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true });
  act(() => {
    input.dispatchEvent(metaEvent);
  });
  const ctrlEvent = new KeyboardEvent("keydown", { key: "b", ctrlKey: true, bubbles: true, cancelable: true });
  act(() => {
    input.dispatchEvent(ctrlEvent);
  });

  expect(prefsStore.getState().sidebarHidden).toBe(false);
  // A suppressed binding claims nothing: the text field's own Ctrl+B behavior
  // (cursor back) must not be preventDefault'd either.
  expect(metaEvent.defaultPrevented).toBe(false);
  expect(ctrlEvent.defaultPrevented).toBe(false);
  remove();
});

// --- open modals (per-binding allowInModal) ---------------------------------

test("Mod+K fired while the palette is already open re-opens it (the deliberate exemption)", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  await user.keyboard("{Meta>}k{/Meta}");
  await screen.findByRole("dialog", { name: "Command palette" });
  const openSeq = paletteStore.getState().openSeq;

  await user.keyboard("{Meta>}k{/Meta}");

  // Every openPalette() call bumps openSeq - the remount-on-open contract the
  // exemption exists to preserve (paletteController.ts's own comment).
  expect(paletteStore.getState().openSeq).toBe(openSeq + 1);
  expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Mod+K and Mod+B still fire while an [aria-modal] dialog is open", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const { inside, close } = openFakeModal();

  fireEvent.keyDown(inside, { key: "k", metaKey: true });
  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();

  act(() => closePalette());
  inside.focus();
  fireEvent.keyDown(inside, { key: "b", metaKey: true });
  expect(prefsStore.getState().sidebarHidden).toBe(true);
  close();
});

test("Mod+I and Mod+J are suppressed while an [aria-modal] dialog is open", async () => {
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  installNeedsYouRows();
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  const { inside, close } = openFakeModal();

  fireEvent.keyDown(inside, { key: "i", metaKey: true });
  fireEvent.keyDown(inside, { key: "j", metaKey: true });

  expect(focusSpy).not.toHaveBeenCalled();
  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ref_abc123" });
  close();
});

test("Mod+, is suppressed while an [aria-modal] dialog is open (allowInModal: false)", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const { inside, close } = openFakeModal();

  fireEvent.keyDown(inside, { key: ",", metaKey: true });

  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
  expect(window.location.pathname).not.toBe("/settings");
  close();
});

// --- claimed events / IME composition ----------------------------------------

test("an isComposing keydown is ignored (IME input never triggers a chord)", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  fireEvent.keyDown(window, { key: "k", metaKey: true, isComposing: true });

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(paletteStore.getState().open).toBe(false);
});

test("Escape claimed by an earlier handler (defaultPrevented) does not close Settings", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });
  const claimEscape = (event: globalThis.KeyboardEvent) => {
    if (event.key === "Escape") event.preventDefault();
  };
  document.addEventListener("keydown", claimEscape, true);

  try {
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
  } finally {
    document.removeEventListener("keydown", claimEscape, true);
  }

  // Mechanism check: with the claiming handler gone, the same Escape DOES
  // close the pane - the suppression above was the gate, not a dead chord.
  fireEvent.keyDown(document.body, { key: "Escape" });
  await screen.findByText("No session open");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
});

// --- deliberately not migrated ----------------------------------------------

test("FocusScope Tab cycling from a text field is unaffected by the dispatcher", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  render(
    <FocusScope trap>
      <input aria-label="Field" />
      <button type="button">After</button>
    </FocusScope>,
  );

  const field = screen.getByRole("textbox", { name: "Field" });
  field.focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "After" }));
  // Trapped: Tabbing past the last tabbable loops back to the first.
  await user.tab();
  expect(document.activeElement).toBe(field);
});
