import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ACTIONS } from "../../../keybindings/actions";
import { parseChord, serializeChord } from "../../../keybindings/chord";
import {
  CHARACTER_KEY_TRIGGER_BINDING_ID,
  DEFAULT_BINDINGS,
  registerDefaultBindings,
  SETTINGS_SCOPE,
} from "../../../keybindings/defaults";
import { createKeybindingDispatcher } from "../../../keybindings/dispatcher";
import { keybindingsRegistry } from "../../../keybindings/registry";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../../stores/keybindings";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { KeybindingsSection } from "./keybindings";

// Node 26 shadows jsdom's real window.localStorage with its own
// (non-functional under vitest) global, so every test file that touches
// localStorage (the prefs store does) needs this same small in-memory
// stand-in - see stores/prefs.test.ts's own comment.
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

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
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

function rowFor(title: string): HTMLElement {
  const label = screen.getByText(title);
  const row = label.closest("li");
  if (row === null) throw new Error(`no row for "${title}"`);
  return row;
}

test("lists every default action with its title and effective chord, none customized", () => {
  render(<KeybindingsSection />);

  // The row list is sourced live from the keybindings module's default map,
  // never a hand-maintained copy: one row per ACTION (the cheatsheet's two
  // trigger entries share one action id), in default-map order.
  const rows = screen.getAllByRole("listitem");
  expect(rows).toHaveLength(new Set(DEFAULT_BINDINGS.map((b) => b.actionId)).size);
  expect(rows.map((row) => row.textContent)).toEqual([
    expect.stringContaining("Open the command palette"),
    expect.stringContaining("Toggle the sidebar"),
    expect.stringContaining("Focus the composer"),
    expect.stringContaining("Go to the next session needing you"),
    expect.stringContaining("Quote the selection into the composer"),
    expect.stringContaining("Open settings"),
    expect.stringContaining("Focus the next session pane"),
    expect.stringContaining("Focus the previous session pane"),
    expect.stringContaining("Scroll the transcript up one line"),
    expect.stringContaining("Scroll the transcript down one line"),
    expect.stringContaining("Scroll the transcript up one page"),
    expect.stringContaining("Scroll the transcript down one page"),
    expect.stringContaining("Scroll the transcript to the top"),
    expect.stringContaining("Scroll the transcript to the bottom"),
    expect.stringContaining("Close settings"),
    expect.stringContaining("Show the keyboard shortcuts overlay"),
    expect.stringContaining("Close the keyboard shortcuts overlay"),
  ]);
  expect(screen.queryByText("Customized")).toBeNull();

  // jsdom resolves $mod to Control, so palette.open's effective chord renders
  // as the Ctrl + K kbd pair via the KeyHint widget.
  const paletteRow = rowFor("Open the command palette");
  expect(within(paletteRow).getByText("Ctrl")).toBeTruthy();
  expect(within(paletteRow).getByText("K")).toBeTruthy();
  // settings.close's default chord lists every modifier as optional, so only
  // the Escape key renders.
  const settingsRow = rowFor("Close settings");
  expect(within(settingsRow).getByText("Escape")).toBeTruthy();
});

test("the cheatsheet row shows the platform $mod chord (its ? trigger is a second entry of the same action)", () => {
  render(<KeybindingsSection />);
  const row = rowFor("Show the keyboard shortcuts overlay");
  // jsdom resolves $mod to Control; the section's single-chord display shows
  // the base entry (keybindings/display.ts's displayBindingFor).
  expect(within(row).getByText("Ctrl")).toBeTruthy();
  expect(within(row).getByText("/")).toBeTruthy();
});

test("the character-key toggle persists through the prefs store", async () => {
  render(<KeybindingsSection />);
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
  render(<KeybindingsSection />);
  expect(screen.getByRole("status").textContent).toContain("Waiting for the hub");
  expect(rowFor("Open the command palette").textContent).toContain("K");
});

test("an applied override marks the action customized and shows the override chord", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
  );
  await wireClient(client, true);
  render(<KeybindingsSection />);

  const paletteRow = rowFor("Open the command palette");
  expect(within(paletteRow).getByText("Customized")).toBeTruthy();
  expect(within(paletteRow).getByText("P")).toBeTruthy();
  expect(within(paletteRow).queryByText("K")).toBeNull();
  // Untouched actions keep their default chords and no marker.
  const railRow = rowFor("Toggle the sidebar");
  expect(within(railRow).queryByText("Customized")).toBeNull();
  expect(within(railRow).getByText("B")).toBeTruthy();
});

test("an unbound action renders Unbound and counts as customized", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () =>
    overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: null }]),
  );
  await wireClient(client, true);
  render(<KeybindingsSection />);

  const paletteRow = rowFor("Open the command palette");
  expect(within(paletteRow).getByText("Unbound")).toBeTruthy();
  expect(within(paletteRow).getByText("Customized")).toBeTruthy();
});

test("a hub that predates the feature shows a quiet unsupported status and the defaults", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
  await wireClient(client, false);
  render(<KeybindingsSection />);

  expect(screen.getByRole("status").textContent).toContain("does not support synced keybinding overrides");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("a hub load failure surfaces the error honestly and keeps the defaults visible", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => {
    throw new Error("socket went away");
  });
  await wireClient(client, true);
  render(<KeybindingsSection />);

  expect(screen.getByRole("alert").textContent).toContain("socket went away");
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
  render(<KeybindingsSection />);

  const status = screen.getByRole("status");
  expect(status.textContent).toContain('unknown keybinding action "no.such.action"');
  expect(status.textContent).toContain('chord "Control+W" is reserved');
  // Both bad rules were skipped: every action stays on its default.
  expect(screen.queryByText("Customized")).toBeNull();
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

function captureBox(): HTMLElement {
  return screen.getByRole("textbox", { name: /Press the new shortcut/ });
}

async function enterCapture(title: string): Promise<HTMLElement> {
  await userEvent.setup().click(screen.getByRole("button", { name: `Change the shortcut for ${title}` }));
  return captureBox();
}

test("an unsupported hub keeps every row read-only (no editing affordances)", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  await wireClient(client, false);
  render(<KeybindingsSection />);

  expect(screen.getByRole("status").textContent).toContain("does not support synced keybinding overrides");
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
  render(<KeybindingsSection />);

  // hubSupport is "supported" but nothing is loaded for THIS hub yet: no
  // editing affordances, and a status line says why.
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(screen.getByRole("status").textContent).toContain("read-only until they arrive");
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
  render(<KeybindingsSection />);

  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain("socket went away");
  expect(screen.getByRole("status").textContent).toContain("Editing is unavailable");
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("a hub with unknown support keeps every row read-only", () => {
  render(<KeybindingsSection />);
  expect(screen.queryByRole("button", { name: /shortcut for/ })).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("K")).toBeTruthy();
});

test("clicking a chord enters capture mode with the prompt focused", async () => {
  await wireEditableClient();
  render(<KeybindingsSection />);

  const box = await enterCapture("Open the command palette");

  expect(box.textContent).toContain("Press new shortcut…");
  expect(box.textContent).toContain("Enter to save");
  expect(document.activeElement).toBe(box);
});

test("held modifiers render live while capturing, before any key is recorded", async () => {
  await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "Control", ctrlKey: true });
  expect(within(box).getByText("Ctrl")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Shift", ctrlKey: true, shiftKey: true });
  expect(within(box).getByText("Shift")).toBeTruthy();
  // Releasing keeps the preview in step.
  fireEvent.keyUp(box, { key: "Shift", ctrlKey: true });
  expect(within(box).queryByText("Shift")).toBeNull();
  // No chord recorded yet, and no hub write.
  expect(box.textContent).toContain("Enter to save");
});

test("Enter saves the captured chord through patchOverrides and the row shows the new chord", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
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
  expect(screen.queryByText("Press new shortcut…")).toBeNull();
  expect(within(rowFor("Open the command palette")).getByText("Customized")).toBeTruthy();
});

test("Escape cancels the capture without a hub write and leaves the chord unchanged", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Escape" });

  expect(screen.queryByText("Press new shortcut…")).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("K")).toBeTruthy();
  expect(within(row).queryByText("Customized")).toBeNull();
  // A keyboard-ended capture returns focus to the chord button it started
  // from (a click-away cancel does not - focus went where the user put it).
  expect(document.activeElement).toBe(
    within(row).getByRole("button", { name: "Change the shortcut for Open the command palette" }),
  );
});

// Plain Escape is cancel, permanently: it cannot be assigned through the
// editor (the VS Code keybinding-editor convention), so the built-in Escape
// bindings come back via Reset, never via capture. Escape WITH a modifier is
// a normal chord and records. settings.close's own default is Escape with
// every modifier optional, so overriding THAT action with Shift+Escape
// conflicts with nothing.
test("plain Escape cancels but Shift+Escape records as a chord", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  let box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "Escape" });
  expect(screen.queryByText("Press new shortcut…")).toBeNull();
  expect(patchCallsOf(client)).toHaveLength(0);

  box = await enterCapture("Close settings");
  fireEvent.keyDown(box, { key: "Escape", shiftKey: true });
  expect(within(box).getByText("Shift")).toBeTruthy();
  expect(within(box).getByText("Escape")).toBeTruthy();
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
  render(<KeybindingsSection />);
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
  render(<KeybindingsSection />);
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.pointerDown(document.body);

  expect(screen.queryByText("Press new shortcut…")).toBeNull();
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
  render(<KeybindingsSection />);
  const box = await enterCapture("Open the command palette");

  fireEvent.pointerDown(box);

  expect(captureBox()).toBeTruthy();
  expect(screen.getByText("Press new shortcut…")).toBeTruthy();
});

test("the outside-pointerdown listener is removed when the capture closes", async () => {
  const addSpy = vi.spyOn(document, "addEventListener");
  const removeSpy = vi.spyOn(document, "removeEventListener");
  try {
    await wireEditableClient();
    render(<KeybindingsSection />);
    await enterCapture("Open the command palette");

    // The capture box's listener is the pointerdown one in the capture phase.
    const added = addSpy.mock.calls.filter((call) => call[0] === "pointerdown" && call[2] === true);
    expect(added).not.toHaveLength(0);
    const listener = added[added.length - 1]?.[1];

    fireEvent.keyDown(captureBox(), { key: "Escape" });
    expect(screen.queryByText("Press new shortcut…")).toBeNull();

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
  render(<KeybindingsSection />);

  // The skipped rule stays a quiet warning; nothing is customized.
  expect(screen.getByRole("status").textContent).toContain('unknown keybinding action "future.new.action"');
  expect(screen.queryByText("Customized")).toBeNull();

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
  expect(screen.getByRole("status").textContent).toContain('unknown keybinding action "future.new.action"');
});

test("a plain Enter with nothing captured neither saves nor cancels", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Open the command palette");

  fireEvent.keyDown(box, { key: "Enter" });

  expect(screen.getByText("Press new shortcut…")).toBeTruthy();
  expect(patchCallsOf(client)).toHaveLength(0);
});

// The spacebar's event.key is a literal " " - the tinykeys grammar's press
// SEPARATOR - so capture maps it to the canonical, matchable name "Space"
// (tinykeys' matcher also compares event.code, which IS "Space"). A literal
// " " would not survive parseChord at all.
test("capturing Space records the canonical name and the chord round-trips through the grammar", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: " " });
  expect(within(box).getByText("Space")).toBeTruthy();
  fireEvent.keyDown(box, { key: "Enter" });

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Space" }] },
  });
  // The saved chord parses back to the same press (it would throw on " ").
  expect(serializeChord(parseChord("Space"))).toBe("Space");
  // The confirmed payload reconciled onto the registry with the same chord.
  await waitFor(() => expect(within(rowFor("Focus the composer")).getByText("Space")).toBeTruthy());
});

// IME composition keydowns (isComposing, key "Process"/"Unidentified") are
// not presses: they record nothing and an IME commit Enter must not save.
test("IME composition events neither record nor save; a non-composing press still works", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  // Mid-composition keydowns: no chord recorded.
  fireEvent.keyDown(box, { key: "Process", isComposing: true });
  fireEvent.keyDown(box, { key: "a", isComposing: true });
  expect(box.textContent).toContain("Press new shortcut…");
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
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "a", keyCode: 229 });
  expect(box.textContent).toContain("Press new shortcut…");
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
  render(<KeybindingsSection />);

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
  expect(reopened.textContent).toContain("Press new shortcut…");

  // The stale save resolves: the reopened capture stays open and untouched.
  resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
  await waitFor(() => expect(keybindingsStore.getState().revision).toBe(2));
  expect(screen.getByRole("textbox", { name: /Press the new shortcut/ })).toBe(reopened);
  expect(reopened.textContent).toContain("Press new shortcut…");
});

test("a stale save's error does not surface into a reopened capture", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  let rejectPatch: ((error: Error) => void) | undefined;
  client.on(
    "evener/settings/keybindings/patch",
    () =>
      new Promise<KeybindingsOverrides>((_resolve, reject) => {
        rejectPatch = reject;
      }),
  );
  await wireClient(client, true);
  render(<KeybindingsSection />);

  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  fireEvent.pointerDown(document.body);
  const reopened = await enterCapture("Open the command palette");
  expect(reopened.textContent).toContain("Press new shortcut…");

  // The old save FAILS: its error belongs to the cancelled capture and must
  // not surface on the ROW. (The store still records the failed patch as
  // hubError - the section-level alert is the store's contract for any
  // failed patch, capture or not; what the generation token guards is the
  // row/capture continuation.)
  rejectPatch?.(new Error("hub exploded"));
  await new Promise((resolve) => {
    setTimeout(resolve, 0);
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
  render(<KeybindingsSection />);

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
      render(<KeybindingsSection />);
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
      expect(serializeChord(parseChord("Shift++"))).toBe("Shift++");
      expect(serializeChord(parseChord("Control++"))).toBe("Control++");

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
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain(
    `chord "Control+P" in scope "global" is already bound by "${ACTIONS.paletteOpen}"`,
  );
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
  // A subsequent valid chord still saves from the same capture.
  fireEvent.keyDown(captureBox(), { key: "m", ctrlKey: true });
  fireEvent.keyDown(captureBox(), { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
});

test("a platform-reserved chord is rejected pre-flight with the validation message", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);
  const box = await enterCapture("Focus the composer");

  fireEvent.keyDown(box, { key: "w", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain('chord "Control+W" is reserved');
  expect(patchCallsOf(client)).toHaveLength(0);
});

test("Unbind writes a chord:null rule and the row renders Unbound and Customized", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [{ action: ACTIONS.paletteOpen, chord: null }] },
  });
  const row = rowFor("Open the command palette");
  await waitFor(() => expect(within(row).getByText("Unbound")).toBeTruthy());
  expect(within(row).getByText("Customized")).toBeTruthy();
  // An unbound action offers capture (to set a new chord) but not Unbind.
  expect(within(row).getByRole("button", { name: "Set a shortcut for Open the command palette" })).toBeTruthy();
  expect(within(row).queryByRole("button", { name: "Unbind" })).toBeNull();
});

test("Reset removes the action's rule and restores the default chord", async () => {
  const client = await wireEditableClient([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
  render(<KeybindingsSection />);
  const row = rowFor("Open the command palette");
  expect(within(row).getByText("P")).toBeTruthy();
  expect(within(row).getByText("Customized")).toBeTruthy();

  await userEvent.setup().click(within(row).getByRole("button", { name: "Reset" }));

  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));
  // The patch drops the action's rule entirely (the payload is the whole
  // desired rule set).
  expect(patchCallsOf(client)[0]?.params).toEqual({
    expectedRevision: 1,
    config: { version: 1, rules: [] },
  });
  await waitFor(() => expect(within(row).getByText("K")).toBeTruthy());
  expect(within(row).queryByText("Customized")).toBeNull();
  expect(within(row).queryByRole("button", { name: "Reset" })).toBeNull();
});

test("a failed Unbind surfaces the hub error inline on the row", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
  client.on("evener/settings/keybindings/patch", () => {
    throw new Error("socket went away");
  });
  await wireClient(client, true);
  render(<KeybindingsSection />);

  await userEvent.setup().click(within(rowFor("Open the command palette")).getByRole("button", { name: "Unbind" }));

  const row = rowFor("Open the command palette");
  await waitFor(() => expect(within(row).getByRole("alert").textContent).toContain("socket went away"));
  expect(within(row).getByText("K")).toBeTruthy();
});

// The cheatsheet.toggle row is the one multi-entry action: the editor edits
// the base ($mod+/) chord only; the conditional "?" trigger is the
// character-key setting's own entry, shown read-only with a note.
test("the cheatsheet row shows the conditional ? trigger as a read-only note", async () => {
  await wireEditableClient();
  render(<KeybindingsSection />);

  const row = rowFor("Show the keyboard shortcuts overlay");
  // One editable chord button (the base entry)...
  expect(
    within(row).getByRole("button", { name: "Change the shortcut for Show the keyboard shortcuts overlay" }),
  ).toBeTruthy();
  expect(within(row).getAllByRole("button", { name: /shortcut for/ })).toHaveLength(1);
  // ...and the "?" entry as a note, not a second editable chord.
  const note = row.querySelector("p");
  expect(note?.textContent).toContain("character-key setting");
  expect(within(row).getByText("?")).toBeTruthy();
});

test("the ? note disappears with the conditional binding (character-key setting off)", async () => {
  await wireEditableClient();
  keybindingsRegistry.getState().unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
  render(<KeybindingsSection />);

  const row = rowFor("Show the keyboard shortcuts overlay");
  expect(row.textContent).not.toContain("character-key setting");
});

test("an override on cheatsheet.toggle owns the whole chord set: no ? note", async () => {
  await wireEditableClient([{ action: ACTIONS.cheatsheetToggle, chord: "Control+Slash" }]);
  render(<KeybindingsSection />);

  const row = rowFor("Show the keyboard shortcuts overlay");
  expect(row.textContent).not.toContain("character-key setting");
  expect(within(row).getByText("Customized")).toBeTruthy();
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
      render(<KeybindingsSection />);
      const box = await enterCapture("Open the command palette");

      // Control+B is rail.toggle's live default chord; capturing it must not
      // toggle the rail - rail.toggle's binding even opts OUT of the
      // defaultPrevented gate, so only the capture box's stopPropagation
      // keeps the press from the dispatcher.
      fireEvent.keyDown(box, { key: "b", ctrlKey: true });

      expect(railSpy).not.toHaveBeenCalled();
      expect(within(box).getByText("Ctrl")).toBeTruthy();
      expect(within(box).getByText("B")).toBeTruthy();
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
      render(<KeybindingsSection />);
      const box = await enterCapture("Open the command palette");

      fireEvent.keyDown(box, { key: "Escape" });

      // Capture cancelled, pane still open.
      expect(screen.queryByText("Press new shortcut…")).toBeNull();
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
  client.on(
    "evener/settings/keybindings/patch",
    () =>
      new Promise<KeybindingsOverrides>((resolve) => {
        resolvePatch = resolve;
      }),
  );
  await wireClient(client, true);
  render(<KeybindingsSection />);

  // Open a capture and start a save; the PATCH hangs.
  const box = await enterCapture("Open the command palette");
  fireEvent.keyDown(box, { key: "p", ctrlKey: true });
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(patchCallsOf(client)).toHaveLength(1));

  // Support drops while the save is in flight: the row goes read-only and
  // the capture must close with it.
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: false },
  });
  await waitFor(() => expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull());
  expect(screen.queryByRole("button", { name: /shortcut for Open the command palette/ })).toBeNull();

  // The stale save resolves: no capture reopens, no row-level error.
  resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
  await new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
  expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull();
  expect(within(rowFor("Open the command palette")).queryByRole("alert")).toBeNull();
});

test("editable true → false → true leaves the row read-only then editable again, and a fresh capture opens", async () => {
  const client = await wireEditableClient();
  render(<KeybindingsSection />);

  await enterCapture("Open the command palette");

  // Support drops: capture cancelled, editing affordance gone.
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: false },
  });
  await waitFor(() => expect(screen.queryByRole("textbox", { name: /Press the new shortcut/ })).toBeNull());
  expect(screen.queryByRole("button", { name: /shortcut for Open the command palette/ })).toBeNull();

  // Support returns (the store re-refreshes the hub payload on its own):
  // the chord button comes back and opens a NEW capture.
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: true },
  });
  const chordButton = await screen.findByRole("button", { name: "Change the shortcut for Open the command palette" });
  await userEvent.setup().click(chordButton);
  expect(captureBox().textContent).toContain("Press new shortcut…");
});
