import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ACTIONS } from "../../../keybindings/actions";
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
