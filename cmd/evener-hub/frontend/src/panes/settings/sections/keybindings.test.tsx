import type { KeybindingsOverrides, KeybindingsRule } from "@evener/appwire-client";
import {
  ACTIONS,
  CHARACTER_KEY_TRIGGER_BINDING_ID,
  ConnectionClosedError,
  DEFAULT_BINDINGS,
  friendlyErrorMessage,
  parseChord,
  RequestTimeoutError,
  registerDefaultBindings,
  SETTINGS_SCOPE,
  serializeChord,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parseKeybinding } from "tinykeys";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { keybindingsRegistry } from "../../../keybindings/appRegistry";
import { createKeybindingDispatcher } from "../../../keybindings/dispatcher";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../../stores/keybindings";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { KeybindingsSection } from "./keybindings";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

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

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
  localStorage.clear();
  resetPrefsStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
  localStorage.clear();
  resetPrefsStoreForTests();
});

function renderEditor() {
  const view = render(<KeybindingsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Customize shortcuts" }));
  return view;
}

function rowFor(title: string): HTMLElement {
  const label = screen.getByText(title);
  const row = label.closest("li");
  if (row === null) throw new Error(`no row for "${title}"`);
  return row;
}

/** The key names a chord renders as, one per <kbd>, in order. */
function keysOf(element: HTMLElement): (string | null)[] {
  return [...element.querySelectorAll("kbd")].map((kbd) => kbd.textContent);
}

function isCustomized(row: HTMLElement): boolean {
  return row.hasAttribute("data-customized");
}

function isUnbound(row: HTMLElement): boolean {
  return row.hasAttribute("data-unbound");
}

function customizedActions(): (string | null)[] {
  return [...document.querySelectorAll("li[data-customized]")].map((row) => row.getAttribute("data-action"));
}

/** Why the section is read-only, from its status line. */
function readOnlyReason(): string | null {
  return screen.getByRole("status").getAttribute("data-reason");
}

/** The reason of each skipped-override warning, in list order. */
function warningReasons(): (string | null)[] {
  return [...document.querySelectorAll("[role='status'] li[data-reason]")].map((warning) =>
    warning.getAttribute("data-reason"),
  );
}

test("basic accessibility controls stay available while customization starts collapsed", async () => {
  render(<KeybindingsSection />);
  expect(screen.getByRole("switch", { name: "Character-key shortcuts" })).toBeTruthy();
  expect(screen.queryAllByRole("listitem")).toEqual([]);
  const toggle = screen.getByRole("button", { name: "Customize shortcuts" });
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  toggle.focus();
  await userEvent.setup().keyboard("{Enter}");
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getAllByRole("listitem").length).toBeGreaterThan(0);
});

test("search matches action names and IDs without losing a captured edit", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  await wireClient(client, true);
  renderEditor();
  const user = userEvent.setup();
  const search = screen.getByRole("textbox", { name: "Search shortcuts" });
  await user.type(search, "COMMAND PALETTE");
  expect(screen.getAllByRole("listitem").map((row) => row.getAttribute("data-action"))).toEqual([ACTIONS.paletteOpen]);
  await user.clear(search);
  await user.type(search, ACTIONS.paletteOpen);
  expect(screen.getAllByRole("listitem").map((row) => row.getAttribute("data-action"))).toEqual([ACTIONS.paletteOpen]);
  await user.click(screen.getByRole("button", { name: "Change the shortcut for Open the command palette" }));
  await user.keyboard("{Control>}y{/Control}");
  await user.click(search);
  await user.clear(search);
  await user.type(search, "no-such-shortcut");
  expect(screen.queryAllByRole("listitem")).toEqual([]);
  await user.clear(search);
  const capture = screen.getByRole("textbox", { name: /Press the new shortcut/ });
  expect(capture.getAttribute("data-state")).toBe("captured");
  expect(keysOf(capture)).toEqual(["Ctrl", "Y"]);
  const toggle = screen.getByRole("button", { name: "Customize shortcuts" });
  await user.click(toggle);
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  await user.click(toggle);
  expect(keysOf(screen.getByRole("textbox", { name: /Press the new shortcut/ }))).toEqual(["Ctrl", "Y"]);
  expect(keybindingsStore.getState().rawOverrides).toEqual([]);
});

test.each([false, true])("retained capture can be resumed from search by keyboard after folding=%s", async (fold) => {
  const client = await wireEditableClient();
  renderEditor();
  const user = userEvent.setup();
  const search = screen.getByRole("textbox", { name: "Search shortcuts" });
  await user.type(search, ACTIONS.paletteOpen);
  const box = await enterCapture("Open the command palette");
  await user.keyboard("{Control>}y{/Control}");
  await user.click(search);
  if (fold) {
    const toggle = screen.getByRole("button", { name: "Customize shortcuts" });
    await user.click(toggle);
    await user.keyboard("{Enter}");
    await user.tab();
    expect(document.activeElement).toBe(search);
  }
  await user.tab();
  expect(document.activeElement).toBe(box);
  await user.keyboard("{Enter}");
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(keybindingsStore.getState().rawOverrides).toEqual([{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]);
});

test("a folded capture survives changing another setting", async () => {
  await wireEditableClient();
  renderEditor();
  const user = userEvent.setup();
  const box = await enterCapture("Open the command palette");
  await user.keyboard("{Control>}y{/Control}");
  const toggle = screen.getByRole("button", { name: "Customize shortcuts" });
  await user.click(toggle);
  await user.click(screen.getByRole("switch", { name: "Character-key shortcuts" }));
  await user.click(toggle);
  expect(screen.getByRole("textbox", { name: /Press the new shortcut/ })).toBe(box);
  expect(keysOf(box)).toEqual(["Ctrl", "Y"]);
});

test("a retained capture save does not steal focus from shortcut search", async () => {
  const client = await wireEditableClient();
  let resolvePatch: ((value: KeybindingsOverrides) => void) | undefined;
  client.on(
    "evener/settings/keybindings/patch",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolvePatch = resolve;
      }),
  );
  renderEditor();
  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "y", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  const search = screen.getByRole("textbox", { name: "Search shortcuts" });
  await userEvent.setup().click(search);
  resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]));
  await waitFor(() => expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull());
  expect(document.activeElement).toBe(search);
});

test("lists every default action with its title and effective chord, none customized", () => {
  renderEditor();

  // The row list is sourced live from the keybindings module's default map,
  // never a hand-maintained copy: one row per ACTION (the cheatsheet's two
  // trigger entries share one action id), in default-map order.
  const rows = screen.getAllByRole("listitem");
  expect(rows.map((row) => row.getAttribute("data-action"))).toEqual([
    ...new Set(DEFAULT_BINDINGS.map((b) => b.actionId)),
  ]);
  expect(customizedActions()).toEqual([]);

  // jsdom resolves $mod to Control, so palette.open's effective chord renders
  // as the Ctrl + K kbd pair via the KeyHint widget.
  expect(keysOf(rowFor("Open the command palette"))).toEqual(["Ctrl", "K"]);
  // settings.close's default chord lists every modifier as optional, so only
  // the Escape key renders.
  expect(keysOf(rowFor("Close settings"))).toEqual(["Escape"]);
});

test("the cheatsheet row shows the platform $mod chord (its ? trigger is a second entry of the same action)", () => {
  renderEditor();
  const row = rowFor("Show the keyboard shortcuts overlay");
  // jsdom resolves $mod to Control; the section's single-chord display shows
  // the base entry (keybindings/display.ts's displayBindingFor); the "?" is
  // the conditional trigger's read-only note after it.
  expect(keysOf(row)).toEqual(["Ctrl", "/", "?"]);
});

test("the character-key toggle persists through the prefs store", async () => {
  renderEditor();
  const toggle = screen.getByRole("switch", { name: "Character-key shortcuts" });
  // Default ON (the WCAG 2.1.4 turn-off exists, per the p4 plan's Design
  // decision 3).
  expect(toggle.getAttribute("aria-checked")).toBe("true");

  await userEvent.setup().click(toggle);
  expect(prefsStore.getState().characterKeyTriggers).toBe(false);
  expect(localStorage.getItem("evener.prefs.characterKeyTriggers")).toBe("0");

  // A fresh hydration (what the next page load does) reads it back.
  resetPrefsStoreForTests();
  expect(prefsStore.getState().characterKeyTriggers).toBe(false);
});

test("with no hub connection the section waits quietly and still shows the defaults", () => {
  renderEditor();
  expect(readOnlyReason()).toBe("hub-unknown");
  expect(keysOf(rowFor("Open the command palette"))).toEqual(["Ctrl", "K"]);
});

test("an applied override marks the action customized and shows the override chord", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  await wireClient(client, true);
  renderEditor();

  const paletteRow = rowFor("Open the command palette");
  expect(isCustomized(paletteRow)).toBe(true);
  expect(within(paletteRow).getByText("P")).toBeTruthy();
  expect(within(paletteRow).queryByText("K")).toBeNull();
  // Untouched actions keep their default chords and no marker.
  const railRow = rowFor("Toggle the sidebar");
  expect(isCustomized(railRow)).toBe(false);
  expect(within(railRow).getByText("B")).toBeTruthy();
});

test("an unbound action renders Unbound and counts as customized", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: null }]),
  );
  await wireClient(client, true);
  renderEditor();

  const paletteRow = rowFor("Open the command palette");
  expect(isUnbound(paletteRow)).toBe(true);
  expect(isCustomized(paletteRow)).toBe(true);
});

test("a hub that predates the feature shows a quiet unsupported status and the defaults", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
  await wireClient(client, false);
  renderEditor();

  expect(readOnlyReason()).toBe("unsupported");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("a hub load failure surfaces the error honestly and keeps the defaults visible", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => {
    throw new Error("socket went away");
  });
  await wireClient(client, true);
  renderEditor();

  expect(screen.getByRole("alert").textContent).toContain(friendlyErrorMessage(new Error("socket went away")));
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("validation warnings from the applied payload are listed quietly", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(2, [
      { action: "no.such.action", chord: "Control+P" },
      { action: ACTIONS.railToggle, chord: "Control+W" }, // reserved on every platform
    ]),
  );
  await wireClient(client, true);
  renderEditor();

  expect(warningReasons()).toEqual(["unknown-action", "reserved-chord"]);
  // Both bad rules were skipped: every action stays on its default.
  expect(customizedActions()).toEqual([]);
});

// --- Phase 4b: the capture-based editor ------------------------------------

/** A supported hub whose patch echoes the requested rules back at the next
 * revision - the hub's real apply-and-confirm behavior, minimal. */
async function wireEditableClient(initial: KeybindingsRule[] = []): Promise<FakeClient> {
  const client = new FakeClient("ready");
  let revision = 1;
  client.on("evener/settings/keybindings/get", () => overridesPayload(revision, initial));
  client.on("evener/settings/keybindings/patch", (params) => {
    revision += 1;
    return overridesPayload(revision, params.config.rules);
  });
  await wireClient(client, true);
  return client;
}

function patchCallsOf(client: FakeClient) {
  return client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
}

// Distinguish chord capture from the shortcut search field.
function captureBox(): HTMLElement {
  return screen.getByRole("textbox", { name: /Press the new shortcut/ });
}

function captureState(box: HTMLElement): string | null {
  return box.getAttribute("data-state");
}

async function enterCapture(title: string): Promise<HTMLElement> {
  await userEvent.setup().click(screen.getByRole("button", { name: `Change the shortcut for ${title}` }));
  return captureBox();
}

test("an unsupported hub keeps every row read-only (no editing affordances)", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  await wireClient(client, false);
  renderEditor();

  expect(readOnlyReason()).toBe("unsupported");
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(screen.queryByRole("button", { name: "Unbind" })).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

// The stale-hub window: a supported hub whose override state has not landed
// (or failed to) for the CURRENT client must not offer editing - a PATCH
// composed there would carry the previous hub's revision and payload.
test("the section is read-only while the hub's overrides are still loading, then editing unlocks", async () => {
  const client = new FakeClient("ready");
  let resolveGet: ((value: KeybindingsOverrides) => void) | undefined;
  client.on(
    "evener/settings/keybindings/get",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolveGet = resolve;
      }),
  );
  client.on("evener/settings/keybindings/patch", (params) => overridesPayload(2, params.config.rules));
  connectionStore.getState().connect(client);
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: true },
  });
  renderEditor();

  // hubSupport is "supported" but nothing is loaded for THIS hub yet: no
  // editing affordances, and a status line says why.
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(readOnlyReason()).toBe("loading");
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();

  // The refresh lands: editing unlocks and a save round-trips.
  // (FakeClient defers the handler to a microtask, like a real RPC.)
  await waitFor(() => expect(resolveGet).toBeDefined());
  resolveGet?.(overridesPayload(1, []));
  const chordButton = await screen.findByRole("button", {
    name: "Change the shortcut for Open the command palette",
  });
  await userEvent.setup().click(chordButton);
  fireEvent.keyDown(captureBox(), { key: "p", ctrlKey: true });
  fireEvent.keyDown(captureBox(), { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
});

test("a refresh error leaves the section read-only with a truthful status", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => {
    throw new Error("socket went away");
  });
  await wireClient(client, true);
  renderEditor();

  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain(friendlyErrorMessage(new Error("socket went away")));
  expect(readOnlyReason()).toBe("load-failed");
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("a hub with unknown support keeps every row read-only", () => {
  renderEditor();
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("clicking a chord enters capture mode with the prompt focused", async () => {
  await wireEditableClient();
  renderEditor();

  const box = await enterCapture("Open the command palette");

  expect(captureState(box)).toBe("idle");
  expect(document.activeElement).toBe(box);
});

test("held modifiers render live while capturing, before any key is recorded", async () => {
  await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "Control", ctrlKey: true });
  expect(keysOf(box)).toEqual(["Ctrl"]);
  fireEvent.keyDown(box, { key: "Shift", ctrlKey: true, shiftKey: true });
  expect(keysOf(box)).toEqual(["Ctrl", "Shift"]);
  // Releasing keeps the preview in step.
  fireEvent.keyUp(box, { key: "Shift", ctrlKey: true });
  expect(keysOf(box)).toEqual(["Ctrl"]);
  // No chord recorded yet.
  expect(captureState(box)).toBe("holding");
});

test("Enter saves the captured chord through patchOverrides and the row shows the new chord", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  expect(within(box).getByText("P")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.paletteOpen, chord: "Control+P" }] },
  });
  // The confirmed payload reconciled: capture closed, row shows the override.
  await waitFor(() => expect(within(rowFor("Open the command palette")).getByText("P")).toBeTruthy());
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(isCustomized(rowFor("Open the command palette"))).toBe(true);
});

test("Escape cancels the capture without a hub write and leaves the chord unchanged", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Escape" });

  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("K")).toBeTruthy();
  expect(isCustomized(row)).toBe(false);
  // A keyboard-ended capture returns focus to the chord button it started
  // from (a click-away cancel does not - focus went where the user put it).
  expect(document.activeElement).toBe(
    within(row).getByRole("button", { name: "Change the shortcut for Open the command palette" }),
  );
});

test("a second non-modifier key does NOT overwrite the captured chord; Enter saves the FIRST", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  expect(within(box).getByText("P")).toBeTruthy();
  // The stray press before Enter: the recorded chord stands (cancel and
  // re-capture to change it) - the box keeps showing P and W never appears.
  fireEvent.keyDown(box, { key: "w" });
  expect(within(box).getByText("P")).toBeTruthy();
  expect(within(box).queryByText("W")).toBeNull();
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.paletteOpen, chord: "Control+P" }] },
  });
});

test("after capture, modifier presses do not replace the preview and Escape still cancels", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  expect(within(box).getByText("P")).toBeTruthy();
  // The held-modifier echo still updates internally, but the recorded chord
  // owns the preview.
  fireEvent.keyDown(box, { key: "Shift" });
  expect(within(box).getByText("P")).toBeTruthy();
  fireEvent.keyUp(box, { key: "Shift" });
  fireEvent.keyDown(box, { key: "Escape" });

  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("K")).toBeTruthy();
  expect(isCustomized(row)).toBe(false);
});

// Plain Escape is cancel, permanently: it cannot be assigned through the
// editor (the VS Code keybinding-editor convention), so the built-in Escape
// bindings come back via Reset, never via capture. Escape WITH a modifier is
// a normal chord and records. settings.close's own default is Escape with
// every modifier optional, so overriding THAT action with Shift+Escape
// conflicts with nothing.
test("plain Escape cancels but Shift+Escape records as a chord", async () => {
  const client = await wireEditableClient();
  renderEditor();
  let box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "Escape" });
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);

  box = await enterCapture("Close settings");
  fireEvent.keyDown(box, { key: "Escape", shiftKey: true });
  expect(keysOf(box)).toEqual(["Shift", "Escape"]);
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.settingsClose, chord: "Shift+Escape" }] },
  });
});

// Uppercase normalization is ASCII-only: String.toUpperCase can change
// LENGTH on non-ASCII input ("ß" -> "SS"), which would corrupt the chord.
test("a non-ASCII character records verbatim; ASCII letters still normalize to uppercase", async () => {
  const client = await wireEditableClient();
  renderEditor();
  let box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "ß" });
  expect(within(box).getByText("ß")).toBeTruthy();
  expect(within(box).queryByText("SS")).toBeNull();
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "ß" }] },
  });

  box = await enterCapture("Quote the selection into the composer");
  fireEvent.keyDown(box, { key: "a" });
  expect(within(box).getByText("A")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(2));
  expect(patchCallsOf(client)[1]?.params).toEqual({
    expectedRevision: 2,
    config: {
      version: 1,
      rules: [
        { action: ACTIONS.composerFocus, chord: "ß" },
        { action: ACTIONS.selectionQuote, chord: "A" },
      ],
    },
  });
});

// Click-away cancels even when the click target cannot take focus: onBlur
// alone only fires when focus MOVES, so a document-level pointerdown listener
// (capture phase) covers non-focusable clicks while the capture is live.
test("pointerdown outside the capture box cancels without refocusing the chord button", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.pointerDown(document.body);

  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("K")).toBeTruthy();
  // refocus:false - a click-away cancel leaves focus where the user put it,
  // unlike the keyboard cancel which returns focus to the chord button.
  expect(document.activeElement).not.toBe(
    within(row).getByRole("button", { name: "Change the shortcut for Open the command palette" }),
  );
});

test("pointerdown INSIDE the capture box does not cancel", async () => {
  await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.pointerDown(box);

  expect(captureState(captureBox())).toBe("idle");
});

test("the outside-pointerdown listener is removed when the capture closes", async () => {
  const addSpy = vi.spyOn(document, "addEventListener");
  const removeSpy = vi.spyOn(document, "removeEventListener");
  try {
    await wireEditableClient();
    renderEditor();
    await enterCapture("Open the command palette");

    // The capture box's listener is the pointerdown one in the capture phase.
    const added = addSpy.mock.calls.filter((call) => call[0] === "pointerdown" && call[2] === true);
    expect(added).not.toHaveLength(0);
    const listener = added[added.length - 1]?.[1];

    fireEvent.keyDown(captureBox(), { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();

    expect(
      removeSpy.mock.calls.some((call) => call[0] === "pointerdown" && call[1] === listener && call[2] === true),
    ).toBe(true);
  } finally {
    addSpy.mockRestore();
    removeSpy.mockRestore();
  }
});

// Whole-payload PATCHes are composed from the hub's RAW rules: a rule
// validation skipped (here: an action this client does not know, written by
// a newer one) is still the hub's state and must survive an unrelated edit.
test("an unrelated edit preserves a hub rule validation skips (unknown action survives in the PATCH payload)", async () => {
  const client = await wireEditableClient([{ action: "future.new.action", chord: "Control+Alt+9" }]);
  renderEditor();

  // The skipped rule stays a quiet warning; nothing is customized.
  expect(warningReasons()).toEqual(["unknown-action"]);
  expect(customizedActions()).toEqual([]);

  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  // The unknown-action rule passes through untouched, alongside the edit.
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: {
      version: 1,
      rules: [
        { action: "future.new.action", chord: "Control+Alt+9" },
        { action: ACTIONS.paletteOpen, chord: "Control+P" },
      ],
    },
  });
  // The preserved rule's pre-existing warning did not block the save, and
  // the confirmed payload still lists it as a (skipped) warning.
  await waitFor(() => expect(within(rowFor("Open the command palette")).getByText("P")).toBeTruthy());
  expect(warningReasons()).toEqual(["unknown-action"]);
});

test("a plain Enter with nothing captured neither saves nor cancels", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "Enter" });

  expect(captureState(captureBox())).toBe("idle");
  expect(patchCallsOf(client)).toHaveLength(0);
});

// The spacebar's event.key is a literal " " - the tinykeys grammar's press
// SEPARATOR - so capture maps it to the canonical, matchable name "Space"
// (tinykeys' matcher also compares event.code, which IS "Space"). A literal
// " " would not survive parseChord at all.
test("capturing Space records the canonical name and the chord round-trips through the grammar", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: " " });
  expect(keysOf(box)).toEqual(["Space"]);
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Space" }] },
  });
  // The saved chord parses back to the same press (it would throw on " ").
  expect(serializeChord(parseChord(parseKeybinding, "Space"))).toBe("Space");
  // The confirmed payload reconciled onto the registry with the same chord.
  await waitFor(() => expect(keysOf(rowFor("Focus the composer"))).toEqual(["Space"]));
});

// IME composition keydowns (isComposing, key "Process"/"Unidentified") are
// not presses: they record nothing and an IME commit Enter must not save.
test("IME composition events neither record nor save; a non-composing press still works", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Focus the composer");

  // Mid-composition keydowns: no chord recorded.
  fireEvent.keyDown(box, { key: "Process", isComposing: true });
  fireEvent.keyDown(box, { key: "a", isComposing: true });
  expect(captureState(box)).toBe("idle");
  // A composition-commit Enter does NOT save...
  fireEvent.keyDown(box, { key: "Enter", isComposing: true });
  fireEvent.keyDown(box, { key: "Unidentified" });
  expect(patchCallsOf(client)).toHaveLength(0);
  expect(captureBox()).toBeTruthy();

  // ...and a real press afterwards still records and saves.
  fireEvent.keyDown(box, { key: "m", ctrlKey: true });
  expect(within(box).getByText("M")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Control+M" }] },
  });
});

// Browsers that signal IME input ONLY through keyCode 229 (no isComposing,
// an ordinary-looking key) hit the dispatcher's second guard; capture must
// run the same one or composition input / a commit Enter records as a chord.
test("a keydown reported only via keyCode 229 records nothing and does not save", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "a", keyCode: 229 });
  expect(captureState(box)).toBe("idle");
  fireEvent.keyDown(box, { key: "Enter", keyCode: 229 });
  expect(patchCallsOf(client)).toHaveLength(0);
  expect(captureBox()).toBeTruthy();

  // A real press afterwards still records and saves.
  fireEvent.keyDown(box, { key: "m", ctrlKey: true });
  expect(within(box).getByText("M")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
});

// A save is a hub round trip; a click-away cancel closes the box while the
// PATCH is in flight. The per-capture generation token keeps the stale
// continuation from closing or repainting a capture it did not start.
test("a save resolving after a click-away cancel does not clobber a reopened capture", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  let resolvePatch: ((value: KeybindingsOverrides) => void) | undefined;
  client.on(
    "evener/settings/keybindings/patch",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolvePatch = resolve;
      }),
  );
  await wireClient(client, true);
  renderEditor();

  // Open a capture and start a save; the PATCH hangs.
  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  // Click away: the capture cancels while the save is in flight...
  fireEvent.pointerDown(document.body);
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();

  // ...and the user reopens the same row's capture.
  const reopened = await enterCapture("Open the command palette");
  expect(captureState(reopened)).toBe("idle");

  // The stale save resolves: the reopened capture stays open and untouched.
  resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
  await waitFor(() => expect(keybindingsStore.getState().revision).toBe(2));
  expect(captureBox()).toBe(reopened);
  expect(captureState(reopened)).toBe("idle");
});

test("a stale save's error does not surface into a reopened capture", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  let rejectPatch: ((error: Error) => void) | undefined;
  const pendingPatch = new Promise<KeybindingsOverrides>((_resolve, reject) => {
    rejectPatch = reject;
  });
  client.on("evener/settings/keybindings/patch", () => pendingPatch);
  await wireClient(client, true);
  renderEditor();

  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  fireEvent.pointerDown(document.body);
  const reopened = await enterCapture("Open the command palette");
  expect(captureState(reopened)).toBe("idle");

  // The old save FAILS: its error belongs to the cancelled capture and must
  // not surface on the ROW. (The store still records the failed patch as
  // hubError - the section-level alert is the store's contract for any
  // failed patch, capture or not; what the generation token guards is the
  // row/capture continuation.)
  await act(async () => {
    rejectPatch?.(new Error("hub exploded"));
    await expect(pendingPatch).rejects.toThrow("hub exploded");
  });
  expect(within(rowFor("Open the command palette")).queryByRole("alert")).toBeNull();
  // hubError also makes the whole section read-only (editable includes
  // hubError === null), so the reopened capture CLOSES: an interactive box
  // must not strand in a section whose controls have all gone read-only.
  // The finding-13 guarantee this test pins is the error's containment, not
  // the capture's survival across an editability loss.
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
});

// Writes serialize at the store: an edit made while ANOTHER row's write is
// in flight queues behind it (no controls are disabled - the queue absorbs
// the concurrency), composes its expectedRevision and payload from the
// first write's confirmed state, and - via the finding-13 generation token
// - still cannot touch a capture that was cancelled while it queued.
test("a save queued behind another row's in-flight write lands in order and ignores its cancelled capture", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  let firstExpectedRevision: number | undefined;
  let resolveFirstPatch: ((value: KeybindingsOverrides) => void) | undefined;
  client.on("evener/settings/keybindings/patch", (params) => {
    if (firstExpectedRevision === undefined) {
      firstExpectedRevision = params.expectedRevision;
      return new Promise<KeybindingsOverrides>((resolve) => {
        resolveFirstPatch = resolve;
      });
    }
    return overridesPayload(params.expectedRevision + 1, params.config.rules);
  });
  await wireClient(client, true);
  renderEditor();

  // Row B (command palette) Reset starts PATCH #1, which hangs in flight.
  const resetButton = within(rowFor("Open the command palette")).getByRole("button", { name: "Reset" });
  await userEvent.setup().click(resetButton);
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  // While it hangs, the composer row's controls stay ENABLED (the queue
  // absorbs the concurrency - nothing is disabled during a write)...
  const chordButton = screen.getByRole("button", { name: "Change the shortcut for Focus the composer" });
  expect((chordButton as HTMLButtonElement).disabled).toBe(false);
  await userEvent.setup().click(chordButton);
  const box = captureBox();
  fireEvent.keyDown(box, { key: "m", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  // ...and the save QUEUES: no second PATCH has hit the wire yet.
  expect(patchCallsOf(client)).toHaveLength(1);

  // The capture is cancelled before its save executes.
  fireEvent.pointerDown(document.body);
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();

  // PATCH #1 lands: the queued save composes against the post-Reset state
  // (expectedRevision 2, rules WITHOUT the dropped palette override) and
  // applies to the store...
  resolveFirstPatch?.(overridesPayload((firstExpectedRevision ?? 0) + 1, []));
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(2));
  expect(patchCallsOf(client)[1]?.params).toEqual({
    expectedRevision: 2,
    config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Control+M" }] },
  });
  await waitFor(() => expect(keybindingsStore.getState().revision).toBe(3));

  // ...but the cancelled capture is never touched: it stays closed, no row
  // error appears, focus is not stolen back to the chord button.
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(within(rowFor("Focus the composer")).queryByRole("alert")).toBeNull();
  expect(document.activeElement).not.toBe(
    screen.getByRole("button", { name: "Change the shortcut for Focus the composer" }),
  );
});

// "+" is tinykeys' modifier DELIMITER, so a naive read says a captured "+"
// chord ("Shift++", "Control++") cannot parse. The installed tinykeys splits
// modifiers with a lookbehind (/(?<=\w|\])\+/), so a trailing "+" parses as
// the key, and capture records the modifiers actually held - the chord
// matches the very keydown that produced it. This test pins that end-to-end
// through the REAL dispatcher so a tinykeys regression turns red here.
test("capturing + saves a chord that round-trips and fires on a real + keydown", async () => {
  const client = await wireEditableClient();
  const focusSpy = vi.fn();
  const unregister = keybindingsRegistry.getState().registerAction(ACTIONS.composerFocus, focusSpy);
  try {
    await withDispatcher(async () => {
      renderEditor();
      const box = await enterCapture("Focus the composer");

      // US layout: "+" is Shift+=, so the keydown carries Shift.
      fireEvent.keyDown(box, { key: "+", shiftKey: true });
      // KeyHint renders "+" SEPARATORS between kbds, so assert the kbd run
      // itself rather than a bare text match.
      expect([...box.querySelectorAll("kbd")].map((kbd) => kbd.textContent)).toEqual(["Shift", "+"]);
      fireEvent.keyDown(box, { key: "Enter" });

      await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
      expect(patchCallsOf(client)[0]?.params).toEqual({
        expectedRevision: 1,
        config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Shift++" }] },
      });
      // The serialized chord parses back to itself (a delimiter collision
      // would throw or misparse the key).
      expect(serializeChord(parseChord(parseKeybinding, "Shift++"))).toBe("Shift++");
      expect(serializeChord(parseChord(parseKeybinding, "Control++"))).toBe("Control++");

      // The confirmed payload reconciled, and the REAL dispatcher's matcher
      // fires the action on the + keydown.
      await waitFor(() =>
        expect([...rowFor("Focus the composer").querySelectorAll("kbd")].map((kbd) => kbd.textContent)).toEqual([
          "Shift",
          "+",
        ]),
      );
      fireEvent.keyDown(document.body, { key: "+", shiftKey: true });
      expect(focusSpy).toHaveBeenCalledTimes(1);
    });
  } finally {
    unregister();
  }
});

test("a conflicting chord is rejected pre-flight: inline message, no hub write, capture stays open", async () => {
  const client = await wireEditableClient([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
  renderEditor();
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });

  await within(rowFor("Focus the composer")).findByRole("alert");
  expect(patchCallsOf(client)).toHaveLength(0);
  // The capture stays open so the user can try another chord or cancel...
  expect(captureBox()).toBeTruthy();
  // ...and composer.focus's registry bindings never moved off the default
  // (the row itself shows the capture box while capturing, so assert the
  // registry, not the render).
  expect(
    keybindingsRegistry
      .getState()
      .bindings.filter((b) => b.actionId === ACTIONS.composerFocus)
      .map((b) => b.id),
  ).toEqual([ACTIONS.composerFocus, `${ACTIONS.composerFocus}#mod-twin`]);
  // The recorded (conflicting) chord stands while this capture lives
  // (finding 30): the way to another chord is cancel and RE-CAPTURE, and
  // the fresh capture then saves clean.
  fireEvent.keyDown(captureBox(), { key: "m", ctrlKey: true });
  expect(within(captureBox()).getByText("P")).toBeTruthy();
  fireEvent.keyDown(captureBox(), { key: "Escape" });
  const box2 = await enterCapture("Focus the composer");
  fireEvent.keyDown(box2, { key: "m", ctrlKey: true });
  fireEvent.keyDown(box2, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
});

test("a platform-reserved chord is rejected pre-flight with the validation message", async () => {
  const client = await wireEditableClient();
  renderEditor();
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "w", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });

  await within(rowFor("Focus the composer")).findByRole("alert");
  expect(patchCallsOf(client)).toHaveLength(0);
});

test("Unbind writes a chord:null rule and the row renders Unbound and Customized", async () => {
  const client = await wireEditableClient();
  renderEditor();

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.paletteOpen, chord: null }] },
  });
  const row = rowFor("Open the command palette");
  await waitFor(() => expect(isUnbound(row)).toBe(true));
  expect(isCustomized(row)).toBe(true);
  // An unbound action offers capture (to set a new chord) but not Unbind.
  expect(within(row).getByRole("button", { name: "Set a shortcut for Open the command palette" })).toBeTruthy();
  expect(within(row).queryByRole("button", { name: "Unbind" })).toBeNull();
});

test("Reset removes the action's rule and restores the default chord", async () => {
  const client = await wireEditableClient([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
  renderEditor();
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("P")).toBeTruthy();
  expect(isCustomized(row)).toBe(true);

  await userEvent.setup().click(within(row).getByRole("button", { name: "Reset" }));

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  // The patch drops the action's rule entirely (the payload is the whole
  // desired rule set).
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [] },
  });
  await waitFor(() => expect(within(row).getByText("K")).toBeTruthy());
  expect(isCustomized(row)).toBe(false);
  expect(within(row).queryByRole("button", { name: "Reset" })).toBeNull();
});

test("a failed Unbind surfaces the hub error inline on the row", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  client.on("evener/settings/keybindings/patch", () => {
    throw new WireError("socket went away", -32000);
  });
  await wireClient(client, true);
  renderEditor();

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));

  const row = rowFor("Open the command palette");
  await waitFor(() => expect(within(row).getByRole("alert").textContent).toContain("socket went away"));
  expect(within(row).getByText("K")).toBeTruthy();
});

// The client's own write failures (a timeout, a lost connection) show their
// friendly form on the row, never the client's wiring text.
test.each([
  ["the client's request timeout", new RequestTimeoutError('AppwireClient: "a method" timed out after 1ms')],
  ["the client's lost socket", new Error("AppwireClient: socket closed (code 1006)")],
])("a failed Unbind from %s shows its friendly form on the row", async (_name, err) => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  client.on("evener/settings/keybindings/patch", () => {
    throw err;
  });
  await wireClient(client, true);
  renderEditor();

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));

  const row = rowFor("Open the command palette");
  await waitFor(() => expect(within(row).getByRole("alert").textContent).toContain(friendlyErrorMessage(err)));
  expect(screen.queryByText(/AppwireClient/)).toBeNull();
});

test("a confirmed hub payload clears a stale row error - and leaves an unrelated capture alone", async () => {
  // The hub rejects writes while it recovers; the failed PATCH never
  // confirmed, so the GET stays at revision 1 and the refresh re-confirms
  // the SAME payload (the stale guard is `<`: an equal-revision refresh
  // still applies and clears hubError).
  let failPatch = true;
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  client.on("evener/settings/keybindings/patch", (params) => {
    if (failPatch) throw new WireError("state unavailable", -32000);
    return overridesPayload(2, params.config.rules);
  });
  await wireClient(client, true);
  renderEditor();

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));
  const row = rowFor("Open the command palette");
  await waitFor(() => expect(within(row).getByRole("alert").textContent).toContain("state unavailable"));
  expect(keybindingsStore.getState().hubError).not.toBeNull();

  // The hub answers a refresh again: hubError clears, and the row's stale
  // inline error clears WITH it - the bindings it described are no longer
  // the truth (the palette was never unbound).
  failPatch = false;
  await act(() => keybindingsStore.getState().refreshOverrides());
  expect(keybindingsStore.getState().hubError).toBeNull();
  await waitFor(() => expect(within(row).queryByRole("alert")).toBeNull());

  // The clearing is scoped to the stale error: a capture open on another
  // row rides through a later confirmed payload untouched. A NOTIFICATION,
  // not a refresh: a refresh flips hubLoading, which makes the section
  // read-only for the flight and cancels captures by design (finding 18) -
  // the notification path confirms a payload without the loading gate.
  const box = await enterCapture("Focus the composer");
  act(() => {
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(1, []),
    });
  });
  expect(captureBox()).toBe(box);
});

// The cheatsheet.toggle row is the one multi-entry action: the editor edits
// the base ($mod+/) chord only; the conditional "?" trigger is the
// character-key setting's own entry, shown read-only with a note.
test("the cheatsheet row shows the conditional ? trigger as a read-only note", async () => {
  await wireEditableClient();
  renderEditor();

  const row = rowFor("Show the keyboard shortcuts overlay");
  // One editable chord button (the base entry)...
  expect(
    within(row).getByRole("button", { name: "Change the shortcut for Show the keyboard shortcuts overlay" }),
  ).toBeTruthy();
  expect(within(row).getAllByRole("button", { name: /shortcut for/ })).toHaveLength(1);
  // ...and the "?" entry as a note, not a second editable chord.
  const note = within(row).getByTestId("conditional-trigger-note");
  expect(keysOf(note)).toEqual(["?"]);
});

test("the ? note disappears with the conditional binding (character-key setting off)", async () => {
  await wireEditableClient();
  keybindingsRegistry.getState().unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
  renderEditor();

  const row = rowFor("Show the keyboard shortcuts overlay");
  expect(within(row).queryByTestId("conditional-trigger-note")).toBeNull();
});

test("an override on cheatsheet.toggle owns the whole chord set: no ? note", async () => {
  await wireEditableClient([{ action: ACTIONS.cheatsheetToggle, chord: "Control+Slash" }]);
  renderEditor();

  const row = rowFor("Show the keyboard shortcuts overlay");
  expect(within(row).queryByTestId("conditional-trigger-note")).toBeNull();
  expect(isCustomized(row)).toBe(true);
});

// While capture is active the window-level dispatcher must not act: the
// capture box consumes every keydown (preventDefault + stopPropagation).
// These tests wire the REAL dispatcher and settings scope, the same way
// Settings.tsx does for the live pane.
async function withDispatcher(run: () => Promise<void>): Promise<void> {
  const dispatcher = createKeybindingDispatcher();
  const detach = dispatcher.attach(window);
  try {
    await run();
  } finally {
    detach();
    dispatcher.dispose();
  }
}

test("a captured chord never reaches the dispatcher (rail.toggle opts out of defaultPrevented)", async () => {
  await wireEditableClient();
  const railSpy = vi.fn();
  const unregister = keybindingsRegistry.getState().registerAction(ACTIONS.railToggle, railSpy);
  try {
    await withDispatcher(async () => {
      renderEditor();
      const box = await enterCapture("Open the command palette");

      // Control+B is rail.toggle's live default chord; capturing it must not
      // toggle the rail - rail.toggle's binding even opts OUT of the
      // defaultPrevented gate, so only the capture box's stopPropagation
      // keeps the press from the dispatcher.
      fireEvent.keyDown(box, { key: "b", ctrlKey: true });

      expect(railSpy).not.toHaveBeenCalled();
      expect(keysOf(box)).toEqual(["Ctrl", "B"]);
    });
  } finally {
    unregister();
  }
});

test("Escape cancels the capture and does NOT fire settings.close; outside capture it closes", async () => {
  await wireEditableClient();
  const closeSpy = vi.fn();
  const popScope = keybindingsRegistry.getState().pushScope(SETTINGS_SCOPE);
  const unregister = keybindingsRegistry.getState().registerAction(ACTIONS.settingsClose, closeSpy);
  try {
    await withDispatcher(async () => {
      renderEditor();
      const box = await enterCapture("Open the command palette");

      fireEvent.keyDown(box, { key: "Escape" });

      // Capture cancelled, pane still open.
      expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
      expect(closeSpy).not.toHaveBeenCalled();

      // Not capturing: Escape reaches the settings scope's close binding.
      fireEvent.keyDown(document.body, { key: "Escape" });
      expect(closeSpy).toHaveBeenCalledTimes(1);
    });
  } finally {
    unregister();
    popScope();
  }
});

// Editability is a per-render prop driven by hub support; a capture is row
// state with a save in flight on the wire. Support loss mid-capture must
// cancel the capture like a click-away (generation bumped, no refocus):
// otherwise the interactive box strands in a read-only section and the
// resolving save's continuation would repaint it.
test("editable flipping false mid-capture closes the box and a resolving in-flight save does not touch the row", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  let resolvePatch: ((value: KeybindingsOverrides) => void) | undefined;
  const pendingPatch = new Promise<KeybindingsOverrides>((resolve) => {
    resolvePatch = resolve;
  });
  client.on("evener/settings/keybindings/patch", () => pendingPatch);
  await wireClient(client, true);
  renderEditor();

  // Open a capture and start a save; the PATCH hangs.
  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  // Support drops while the save is in flight: the row goes read-only and
  // the capture must close with it.
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
  });
  await waitFor(() => expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull());
  expect(screen.queryByRole("button", { name: /shortcut for Open the command palette/ })).toBeNull();

  // The stale save resolves: no capture reopens, no row-level error.
  await act(async () => {
    resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
    await pendingPatch;
  });
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(within(rowFor("Open the command palette")).queryByRole("alert")).toBeNull();
});

test("editable true → false → true leaves the row read-only then editable again, and a fresh capture opens", async () => {
  const client = await wireEditableClient();
  renderEditor();

  await enterCapture("Open the command palette");

  // Support drops: capture cancelled, editing affordance gone.
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
  });
  await waitFor(() => expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull());
  expect(screen.queryByRole("button", { name: /shortcut for Open the command palette/ })).toBeNull();

  // Support returns (the store re-refreshes the hub payload on its own):
  // the chord button comes back and opens a NEW capture.
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: true },
    });
  });
  const chordButton = await screen.findByRole("button", { name: "Change the shortcut for Open the command palette" });
  await userEvent.setup().click(chordButton);
  expect(captureState(captureBox())).toBe("idle");
});

// Reset availability derives from the hub's RAW rules: a persisted rule
// validation skips (reserved here) never reaches the validated set, but
// dropping it is exactly the meaningful action - so the row must offer
// Reset even though the effective bindings never changed.
// A conflict-skip warning names the holder the way the rows do - by TITLE
// (with the action id alongside) - not by bare action id: this list is what
// the user reads to understand why a saved rule did not apply.
test("a persisted rule skipped for conflicting with a default names the holder by title in the warnings list", async () => {
  // composerFocus claiming Control+K collides with palette.open's default.
  await wireEditableClient([{ action: ACTIONS.composerFocus, chord: "Control+K" }]);
  renderEditor();

  await waitFor(() => expect(warningReasons()).toEqual(["conflict"]));
  // The skipped rule changed nothing: composer stays on its default.
  expect(isCustomized(rowFor("Focus the composer"))).toBe(false);
});

test("a persisted rule skipped by validation shows Reset, and clicking it drops the rule from the hub payload", async () => {
  // Control+W is reserved on every platform: the rule is skipped with a
  // warning and never applies.
  const client = await wireEditableClient([{ action: ACTIONS.railToggle, chord: "Control+W" }]);
  renderEditor();

  const row = rowFor("Toggle the sidebar");
  const reset = within(row).getByRole("button", { name: "Reset" });
  // An action with no raw rule shows no Reset (regression).
  expect(within(rowFor("Open the command palette")).queryByRole("button", { name: "Reset" })).toBeNull();
  // The Customized marker stays on the effective bindings: the skipped rule
  // changed nothing, so no marker.
  expect(isCustomized(row)).toBe(false);

  await userEvent.setup().click(reset);
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  // The PATCH drops the rule: the full replacement rule set without it.
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [] },
  });
  await waitFor(() => expect(within(rowFor("Toggle the sidebar")).queryByRole("button", { name: "Reset" })).toBeNull());
});

// Finding 33: a preflight rejection (a Reset that would re-conflict a
// restored default) sets ONLY the row error - hubError stays null by design,
// so the hubError transition cannot clear it. The next confirmed hub payload
// (here a `changed` notification from an edit made elsewhere) supersedes the
// bindings the error described, and the apply-serial bump clears the row error.
test("a Reset preflight rejection's row error clears on the next confirmed payload", async () => {
  const client = await wireEditableClient([
    { action: ACTIONS.composerFocus, chord: "Control+K" },
    { action: ACTIONS.paletteOpen, chord: "Control+P" },
  ]);
  renderEditor();

  // Reset on the palette row drops the palette override: the simulation
  // restores palette.open's default Control+K, which composer's override
  // still claims - an introduced conflict the hub write never sees.
  const row = rowFor("Open the command palette");
  await userEvent.setup().click(within(row).getByRole("button", { name: "Reset" }));
  await within(row).findByRole("alert");
  expect(patchCallsOf(client)).toHaveLength(0);
  expect(keybindingsStore.getState().hubError).toBeNull();

  // Nothing hub-sourced follows on its own: the error persists until a
  // confirmed payload lands (the composer rule was removed through another
  // client), then clears with the apply.
  act(() => {
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    });
  });
  await waitFor(() => expect(within(row).queryByRole("alert")).toBeNull());
  expect(keybindingsStore.getState().hubError).toBeNull();
});

// Finding 33, fence half: a write QUEUED in a ready generation that a
// support flap ends rejects at the call-time fence - throw-only, hubError
// stays null by design (finding 22) - leaving a row error no hubError
// transition can clear. The next confirmed payload clears it.
test("a generation-fenced queued write's row error clears on the next confirmed payload", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  let resolvePatch: ((value: KeybindingsOverrides) => void) | undefined;
  client.on(
    "evener/settings/keybindings/patch",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolvePatch = resolve;
      }),
  );
  await wireClient(client, true);
  renderEditor();

  // Write 1 (palette Unbind) hangs in flight; write 2 (composer Unbind)
  // queues behind it in the same generation.
  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  await userEvent.setup().click(within(rowFor("Focus the composer")).getByRole("button", { name: "Unbind" }));

  // A support flap ends the ready generation: the flap-back begins a NEW
  // generation and re-refreshes (revision 3 again), confirming state - that
  // apply-serial bump happens BEFORE either row error exists.
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
  });
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: true },
    });
  });
  await waitFor(() => expect(keybindingsStore.getState().loaded).toBe(true));

  // Write 1's response lands in the DEAD generation (dropped, synthesized
  // resolution); write 2 then executes and the call-time fence throws - the
  // composer row shows the error while hubError stays null.
  resolvePatch?.(overridesPayload(4, []));
  const composerRow = rowFor("Focus the composer");
  await within(composerRow).findByRole("alert");
  expect(keybindingsStore.getState().hubError).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(1);

  // A confirmed payload for the NEW generation supersedes the bindings the
  // error described: the row error clears.
  act(() => {
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(5, []),
    });
  });
  await waitFor(() => expect(within(composerRow).queryByRole("alert")).toBeNull());
});

// Finding 35: after an un-apply ROLLBACK on support loss the hubError alert
// reports the old overrides still in effect; the status line must not
// contradict it by claiming the built-in defaults. (Clean unsupported keeps
// the defaults wording - pinned by the pre-existing "does not support synced
// keybinding overrides" test.)
test("a wedged support loss keeps the overrides firing and the status does not claim the defaults are in effect", async () => {
  const client = await wireEditableClient([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
  renderEditor();

  // The wedge: a foreign binding squatting palette.open's DEFAULT chord, so
  // the support drop's un-apply restore exact-matches, throws, and rolls
  // back. palette.open's default is $mod+K with legacyEitherMod, so the
  // restored base serializes "Control+[Meta]+K" (Meta OPTIONAL).
  act(() => {
    keybindingsRegistry.getState().registerBinding({
      id: "foreign.squatter",
      actionId: "foreign.action",
      chord: "Control+[Meta]+K",
    });
  });
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
  });

  await screen.findByRole("alert");
  expect(readOnlyReason()).toBe("unsupported-overrides-retained");
  // ...and the listing backs it up: the palette override is still firing
  // and still marked Customized.
  const row = rowFor("Open the command palette");
  expect(isCustomized(row)).toBe(true);
  expect(within(row).getByText("P")).toBeTruthy();
});

// Finding 37: the round-3 gate locks editing while hubError is set, and a
// successful refresh clears it - the error block offers a Retry control that
// re-drives the refresh. The control unmounts the moment the refresh starts
// (refreshFor's entry clears hubError), so a double-click has no second
// target; the in-flight window with a PRESERVED rollback error disables it
// instead (separate test).
test("a failed patch locks editing and shows a Retry that re-drives the refresh and restores editability (finding 37)", async () => {
  const client = new FakeClient("ready");
  let pending: Promise<KeybindingsOverrides> | undefined;
  client.on("evener/settings/keybindings/get", () => pending ?? overridesPayload(1, []));
  let failPatch = true;
  client.on("evener/settings/keybindings/patch", (params) => {
    if (failPatch) throw new Error("hub exploded");
    return overridesPayload(2, params.config.rules);
  });
  await wireClient(client, true);
  renderEditor();

  // A transient server error: the PATCH rejects on the live generation, so
  // hubError surfaces and editing locks behind the round-3 gate.
  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));
  await waitFor(() =>
    expect(keybindingsStore.getState().hubError).toBe(friendlyErrorMessage(new Error("hub exploded"))),
  );
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  const retry = screen.getByRole("button", { name: "Retry" });

  // The hub recovers. Arm a hanging get so the in-flight window is
  // observable: clicking Retry starts the refresh, which clears hubError at
  // entry - the alert and the Retry control unmount, leaving no second
  // click target (the double-fire guard on this path).
  failPatch = false;
  let resolveGet: ((p: KeybindingsOverrides) => void) | undefined;
  pending = new Promise<KeybindingsOverrides>((resolve) => {
    resolveGet = resolve;
  });
  const getCalls = () => client.calls.filter((c) => c.method === "evener/settings/keybindings/get").length;
  const before = getCalls();
  await userEvent.setup().click(retry);
  await waitFor(() => expect(keybindingsStore.getState().hubLoading).toBe(true));
  expect(getCalls()).toBe(before + 1);
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();

  // The refresh lands: hubError clears and editability returns.
  resolveGet?.(overridesPayload(1, []));
  await waitFor(() => expect(keybindingsStore.getState().hubError).toBeNull());
  await screen.findByRole("button", { name: "Change the shortcut for Open the command palette" });
});

// Finding 37: on an unsupported hub a refresh cannot help (refreshFor's
// entry guard refuses it) - no Retry, even when the rollback alert is
// showing (that state's retry is connection-change-driven, findings 31/35).
test("Retry does not appear on an unsupported hub, even with the rollback alert (finding 37)", async () => {
  const client = await wireEditableClient([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
  renderEditor();
  // The wedge rolls the support drop's un-apply back (finding-35 staging):
  // the rollback alert shows, but the hub is unsupported - no Retry.
  act(() => {
    keybindingsRegistry.getState().registerBinding({
      id: "foreign.squatter",
      actionId: "foreign.action",
      chord: "Control+[Meta]+K",
    });
  });
  await act(async () => {
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
  });
  await screen.findByRole("alert");
  expect(readOnlyReason()).toBe("unsupported-overrides-retained");
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
});

// Finding 37, in-flight window: through a rewire rollback the hubError is
// PRESERVED while the new hub's refresh is in flight (unapplyRolledBack), so
// the Retry control stays mounted - disabled while hubLoading, making a
// double-click a no-op. When the refresh lands and clears the error, the
// control unmounts.
test("Retry is disabled while a refresh is in flight through the rollback window (finding 37)", async () => {
  const clientA = new FakeClient("ready");
  clientA.on("evener/settings/keybindings/get", () =>
    overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  await wireClient(clientA, true);
  renderEditor();

  // The wedge: a foreign binding squatting palette.open's default chord, so
  // the rewire's un-apply restore throws and rolls back.
  act(() => {
    keybindingsRegistry.getState().registerBinding({
      id: "foreign.squatter",
      actionId: "foreign.action",
      chord: "Control+[Meta]+K",
    });
  });
  const clientB = new FakeClient("ready");
  let resolveGet: ((p: KeybindingsOverrides) => void) | undefined;
  clientB.on(
    "evener/settings/keybindings/get",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolveGet = resolve;
      }),
  );
  act(() => connectionStore.getState().connect(clientB));
  await act(async () => {
    connectionStore.setState({
      features: { ...(await clientB.connect()).features, keybindingsSettings: true },
    });
  });
  await waitFor(() => expect(keybindingsStore.getState().hubLoading).toBe(true));
  expect(keybindingsStore.getState().hubError).not.toBeNull();

  // Retry is mounted (supported hub, hubError present) but disabled for the
  // flight: clicking it fires no second get.
  const retry = screen.getByRole("button", { name: "Retry" });
  expect((retry as HTMLButtonElement).disabled).toBe(true);
  const getCalls = () => clientB.calls.filter((c) => c.method === "evener/settings/keybindings/get").length;
  const before = getCalls();
  await userEvent.setup().click(retry);
  expect(getCalls()).toBe(before);

  // Unwedge; the refresh lands, clears the rollback error, and the control
  // unmounts.
  act(() => keybindingsRegistry.getState().unregisterBinding("foreign.squatter"));
  resolveGet?.(overridesPayload(1, []));
  await waitFor(() => expect(keybindingsStore.getState().hubError).toBeNull());
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
});

// The store keeps its own failure messages, and gives a hub WireError and the
// client's connection-state failure their friendly form, so the alert never
// shows the client's wiring text.
test.each([
  ["a hub-reported WireError", new WireError("keybindings.toml is being rewritten", -32000)],
  ["the client's connection-state failure", new ConnectionClosedError("AppwireClient: closed")],
  ["the client's request timeout", new RequestTimeoutError('AppwireClient: "a method" timed out after 1ms')],
])("a hub load failure from %s shows its friendly form", async (_name, err) => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => {
    throw err;
  });
  await wireClient(client, true);
  renderEditor();

  expect(screen.getByRole("alert").textContent).toContain(friendlyErrorMessage(err));
  expect(screen.queryByText(/AppwireClient/)).toBeNull();
});
