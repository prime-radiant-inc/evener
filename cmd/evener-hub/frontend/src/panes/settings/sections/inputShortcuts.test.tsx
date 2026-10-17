import { ACTIONS } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installKeybindings } from "../../../shell/installKeybindings";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../../stores/keybindings";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import Settings from "../Settings";

beforeAll(() => installLocalStorage(new MemoryStorage()));
beforeEach(() => {
  window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener() {}, removeEventListener() {} });
  localStorage.clear();
  resetPrefsStoreForTests();
  resetKeybindingsStoreForTests();
  installKeybindings();
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null, features: undefined });
  resetKeybindingsStoreForTests();
  resetPrefsStoreForTests();
  vi.restoreAllMocks();
});

async function connect(mode: "ready" | "failed" | "unsupported") {
  const client = new FakeClient("ready");
  client.on("evener/settings/keybindings/get", () => {
    if (mode === "failed") throw new Error("fixture read failure");
    return { version: 1, revision: 1, rules: [] };
  });
  client.on("evener/settings/keybindings/patch", (params) => ({ version: 1, revision: 2, rules: params.config.rules }));
  connectionStore.getState().connect(client);
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: mode !== "unsupported" },
  });
  await keybindingsStore.getState().refreshOverrides();
  return client;
}

test.each(["ready", "failed", "unsupported"] as const)(
  "Input retains local preference persistence with hub %s",
  async (mode) => {
    const client = await connect(mode);
    render(<Settings params={{ section: "keybindings" }} paneId="settings-input" focused={true} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("switch", { name: "Enter sends" }));
    expect(prefsStore.getState().enterToSend).toBe(true);
    expect(localStorage.getItem("evener.prefs.enterToSend")).toBe("1");
    await user.click(screen.getByRole("switch", { name: "Character-key shortcuts" }));
    expect(prefsStore.getState().characterKeyTriggers).toBe(false);
    expect(client.calls.filter((call) => call.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    if (mode !== "ready") {
      expect(screen.queryByRole("button", { name: "Change the shortcut for Open the command palette" })).toBeNull();
    }
  },
);

test("Input edits shortcuts through the hub owner", async () => {
  const client = await connect("ready");
  render(<Settings params={{ section: "keybindings" }} paneId="settings-input" focused={true} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("switch", { name: "Enter sends" }));
  await user.click(screen.getByRole("button", { name: "Change the shortcut for Open the command palette" }));
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "p", code: "KeyP", ctrlKey: true });
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" });
  await waitFor(() => expect(screen.queryByRole("textbox")).toBeNull());
  const patches = client.calls.filter((call) => call.method === "evener/settings/keybindings/patch");
  expect(patches).toHaveLength(1);
  expect(patches[0]?.params).toMatchObject({
    config: { rules: [{ action: ACTIONS.paletteOpen, chord: "Control+P" }] },
  });
});
