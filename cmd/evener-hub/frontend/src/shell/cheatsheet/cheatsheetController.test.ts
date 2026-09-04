import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { ACTIONS } from "../../keybindings/actions";
import { CHARACTER_KEY_TRIGGER_BINDING_ID, registerDefaultBindings } from "../../keybindings/defaults";
import { keybindingsRegistry } from "../../keybindings/registry";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "../../stores/keybindings";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { installCharacterKeyTriggerReconcile } from "./cheatsheetController";

// Node 26 shadows jsdom's real window.localStorage with its own
// (non-functional under vitest) global - the same in-memory stand-in
// stores/keybindings.test.ts carries, needed here because the prefs store
// (the characterKeyTriggers pref this controller reconciles against) reads
// and writes localStorage.
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

function questionTriggerRegistered(): boolean {
  return keybindingsRegistry.getState().bindings.some((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
}

function characterKeyWarnings() {
  return keybindingsStore.getState().warnings.filter((w) => w.reason === "character-key-conflict");
}

// A leaked reconcile subscription would keep firing into later tests (the
// store resets above do not remove subscriptions), so every install is
// tracked and disposed here.
const disposers: Array<() => void> = [];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
  localStorage.clear();
  resetPrefsStoreForTests();
});

afterEach(() => {
  while (disposers.length > 0) disposers.pop()?.();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
  localStorage.clear();
  resetPrefsStoreForTests();
});

describe("cheatsheet controller: character-key trigger reconcile", () => {
  test("pref off then on registers and unregisters the ? trigger with no warning (regression)", () => {
    disposers.push(installCharacterKeyTriggerReconcile());
    expect(questionTriggerRegistered()).toBe(true);

    prefsStore.getState().setCharacterKeyTriggers(false);
    expect(questionTriggerRegistered()).toBe(false);
    expect(characterKeyWarnings()).toEqual([]);

    prefsStore.getState().setCharacterKeyTriggers(true);
    expect(questionTriggerRegistered()).toBe(true);
    expect(characterKeyWarnings()).toEqual([]);
  });

  test("pref flip with a conflicting Shift+? override does NOT register ?, surfaces a warning, and recovers when the conflict clears", async () => {
    disposers.push(installCharacterKeyTriggerReconcile());

    // Pref off: "?" unregisters, so a Shift+? claim by another action
    // conflicts with NOTHING and must apply clean.
    prefsStore.getState().setCharacterKeyTriggers(false);
    expect(questionTriggerRegistered()).toBe(false);

    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.composerFocus, chord: "Shift+?" }]),
    );
    await wireClient(client, true);
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(keybindingsStore.getState().warnings).toEqual([]);
    expect(keybindingsRegistry.getState().bindings.some((b) => b.id === `${ACTIONS.composerFocus}#override`)).toBe(
      true,
    );

    // Pref back on: "?" cannot come back - it would overlap the live
    // composer Shift+?. The reconcile must SKIP the registration and say
    // why on the warnings channel, not shadow or be shadowed.
    prefsStore.getState().setCharacterKeyTriggers(true);
    expect(questionTriggerRegistered()).toBe(false);
    const warnings = characterKeyWarnings();
    expect(warnings).toHaveLength(1);
    expect(warnings[0]?.rule).toEqual({ action: ACTIONS.cheatsheetToggle, chord: "[Shift]+?" });
    expect(warnings[0]?.conflictWith).toBe(ACTIONS.composerFocus);
    expect(warnings[0]?.message).toBe(
      `the "?" cheatsheet trigger was not registered: chord "[Shift]+?" in scope "global" is already bound by "${ACTIONS.composerFocus}"`,
    );

    // The overlap clearing (here: the hub drops the composer override)
    // re-runs the reconcile through the overrides-store subscription: "?"
    // registers and exactly the character-key warning clears.
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, []),
    });
    expect(keybindingsStore.getState().revision).toBe(2);
    expect(questionTriggerRegistered()).toBe(true);
    expect(characterKeyWarnings()).toEqual([]);
  });
});
