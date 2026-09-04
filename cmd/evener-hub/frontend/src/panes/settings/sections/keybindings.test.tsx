import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { ACTIONS } from "../../../keybindings/actions";
import { DEFAULT_BINDINGS, registerDefaultBindings } from "../../../keybindings/defaults";
import { keybindingsRegistry } from "../../../keybindings/registry";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../../stores/keybindings";
import { KeybindingsSection } from "./keybindings";

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
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
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
  // never a hand-maintained copy: one row per action, in default-map order.
  const rows = screen.getAllByRole("listitem");
  expect(rows).toHaveLength(DEFAULT_BINDINGS.length);
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
