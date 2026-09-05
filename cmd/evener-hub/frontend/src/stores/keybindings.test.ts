import { waitFor } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { ACTIONS } from "../keybindings/actions";
import { serializeChord } from "../keybindings/chord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID, registerDefaultBindings } from "../keybindings/defaults";
import { type Binding, keybindingsRegistry } from "../keybindings/registry";
import { WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "./keybindings";
import { prefsStore, resetPrefsStoreForTests } from "./prefs";

// Node 26 shadows jsdom's real window.localStorage with its own
// (non-functional under vitest) global, so every test file that touches
// localStorage (the prefs store does, and keybindings.ts now reads the
// character-key pref through it) needs this same small in-memory stand-in -
// see stores/prefs.test.ts's own comment.
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

function bindingsFor(actionId: string): Binding[] {
  return keybindingsRegistry.getState().bindings.filter((b) => b.actionId === actionId);
}

function overridesPayload(revision: number, rules: KeybindingsRule[]): KeybindingsOverrides {
  return { version: 1, revision, rules };
}

function defaultChordOf(bindingId: string): string {
  const binding = keybindingsRegistry.getState().bindings.find((b) => b.id === bindingId);
  if (binding === undefined) throw new Error(`test setup: no binding ${bindingId}`);
  return serializeChord(binding.chord);
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
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
  localStorage.clear();
  resetPrefsStoreForTests();
  vi.restoreAllMocks();
});

describe("keybindings store: startup", () => {
  test("applies the hub overrides to the registry on connect", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);

    const bindings = bindingsFor(ACTIONS.paletteOpen);
    expect(bindings).toHaveLength(1);
    expect(bindings[0]?.id).toBe(`${ACTIONS.paletteOpen}#override`);
    expect(serializeChord(bindings[0]?.chord ?? [])).toBe("Control+P");

    const state = keybindingsStore.getState();
    expect(state.hubSupport).toBe("supported");
    expect(state.revision).toBe(3);
    expect(state.overrides).toEqual([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    expect(state.warnings).toEqual([]);
    expect(state.hubError).toBeNull();
  });

  test("defaults only when the server predates the feature: no request, unsupported state, no crash", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    await wireClient(client, false);

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/get")).toHaveLength(0);
    expect(keybindingsStore.getState().hubSupport).toBe("unsupported");
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("an unreachable hub surfaces hubError and leaves defaults in place", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => {
      throw new Error("socket went away");
    });
    await wireClient(client, true);

    expect(keybindingsStore.getState().hubError).toBe("socket went away");
    expect(bindingsFor(ACTIONS.paletteOpen)).toHaveLength(2);
  });

  test("malformed get payloads surface hubError and leave defaults in place", async () => {
    const client = new FakeClient("ready");
    client.on(
      "evener/settings/keybindings/get",
      () => ({ version: 2, revision: "three", rules: "nope" }) as unknown as KeybindingsOverrides,
    );
    await wireClient(client, true);

    expect(keybindingsStore.getState().hubError).not.toBeNull();
    expect(bindingsFor(ACTIONS.paletteOpen)).toHaveLength(2);
  });

  test("semantically invalid rules are skipped with warnings; valid rules still apply", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(2, [
        { action: "no.such.action", chord: "Control+P" },
        { action: ACTIONS.railToggle, chord: "Control+W" }, // reserved on this platform
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]),
    );
    await wireClient(client, true);

    const state = keybindingsStore.getState();
    expect(state.warnings.map((w) => w.reason)).toEqual(["unknown-action", "reserved-chord"]);
    expect(state.overrides).toEqual([{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([`${ACTIONS.composerFocus}#override`]);
    expect(bindingsFor(ACTIONS.railToggle)).toHaveLength(2);
  });
});

describe("keybindings store: changed reconciliation", () => {
  test("a changed notification rebinds the delta and leaves other bindings untouched (same object identity)", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    const untouched = bindingsFor(ACTIONS.railToggle);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(4, [{ action: ACTIONS.paletteOpen, chord: "Control+U" }]),
    });

    const bindings = bindingsFor(ACTIONS.paletteOpen);
    expect(bindings).toHaveLength(1);
    expect(serializeChord(bindings[0]?.chord ?? [])).toBe("Control+U");
    // The delta discipline: rail.toggle's binding objects were never torn down.
    expect(bindingsFor(ACTIONS.railToggle)).toEqual(untouched);
    expect(bindingsFor(ACTIONS.railToggle)[0]).toBe(untouched[0]);
    expect(keybindingsStore.getState().revision).toBe(4);
  });

  test("a changed notification that drops an override restores the action's defaults", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.paletteOpen)).toHaveLength(1);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(4, []),
    });

    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
    expect(keybindingsStore.getState().overrides).toEqual([]);
    expect(keybindingsStore.getState().revision).toBe(4);
  });

  test("a changed notification can unbind an action (chord null)", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    await wireClient(client, true);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: null }]),
    });

    expect(bindingsFor(ACTIONS.paletteOpen)).toHaveLength(0);
    expect(keybindingsStore.getState().overrides).toEqual([{ action: ACTIONS.paletteOpen, chord: null }]);
  });

  test("a stale changed notification (older revision) is ignored", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(5, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, []),
    });

    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(keybindingsStore.getState().revision).toBe(5);
  });
});

describe("keybindings store: patch", () => {
  test("sends expectedRevision and applies the canonical response", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    const requested: KeybindingsRule[] = [{ action: ACTIONS.composerFocus, chord: "Control+M" }];
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, requested));
    await wireClient(client, true);

    const result = await keybindingsStore.getState().patchOverrides(requested);

    expect(result.revision).toBe(4);
    const patchCalls = client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]?.params).toEqual({
      expectedRevision: 3,
      config: { version: 1, rules: requested },
    });
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([`${ACTIONS.composerFocus}#override`]);
    expect(keybindingsStore.getState().revision).toBe(4);
    expect(keybindingsStore.getState().conflict).toBeNull();
  });

  test("a revision conflict refreshes to the server's current state and surfaces the conflict", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        current: overridesPayload(6, [{ action: ACTIONS.railToggle, chord: "Control+R" }]),
      });
    });
    await wireClient(client, true);

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]),
    ).rejects.toThrow("revision conflict");

    const state = keybindingsStore.getState();
    expect(state.revision).toBe(6);
    expect(state.conflict).toBe("revision conflict");
    expect(state.hubError).toBe("revision conflict");
    expect(bindingsFor(ACTIONS.railToggle).map((b) => b.id)).toEqual([`${ACTIONS.railToggle}#override`]);
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
  });

  test("a non-conflict patch failure surfaces hubError and changes nothing", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => {
      throw new WireError("state unavailable", -32014);
    });
    await wireClient(client, true);

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]),
    ).rejects.toThrow("state unavailable");

    expect(keybindingsStore.getState().hubError).toBe("state unavailable");
    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
  });

  test("patch against an unsupported server throws without a request", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, false);

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]),
    ).rejects.toThrow(/unavailable/);
    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
  });

  // Pre-flight semantic validation (the parked 2b minor, made live by the
  // settings editor): a rule the reconcile would skip is rejected BEFORE the
  // hub write, with the validation layer's message.
  test("a chord conflicting with a live binding is rejected pre-flight, without any hub request", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    // palette.open's default (Control+[Meta]+K with its twin) overlaps a
    // strict Control+K claim by another action.
    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+K" }]),
    ).rejects.toThrow(`chord "Control+K" in scope "global" is already bound by "${ACTIONS.paletteOpen}"`);

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    // A client-side authoring error is not hub-sourced: neither hubError nor
    // conflict moves, and the registry is untouched.
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
  });

  test("a platform-reserved chord is rejected pre-flight, without any hub request", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    // Control+W is on the every-platform reserved list (the browser never
    // delivers it to the page).
    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+W" }]),
    ).rejects.toThrow('chord "Control+W" is reserved');

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
  });

  test("an unknown action is rejected pre-flight, without any hub request", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: "no.such.action", chord: "Control+M" }]),
    ).rejects.toThrow('unknown keybinding action "no.such.action"');

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
  });

  test("a conflicting rule inside a larger payload rejects the whole patch pre-flight", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    // The editor submits the FULL desired rule set; two rules claiming the
    // same chord must not reach the hub at all.
    await expect(
      keybindingsStore.getState().patchOverrides([
        { action: ACTIONS.composerFocus, chord: "Control+M" },
        { action: ACTIONS.nextNeedsYou, chord: "Control+M" },
      ]),
    ).rejects.toThrow("already bound by");

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
    expect(bindingsFor(ACTIONS.nextNeedsYou)).toHaveLength(2);
  });

  // The store retains the hub's RAW rules (rawOverrides) so a PATCH composed
  // from them preserves rules validation skips; pre-flight then tolerates
  // warnings the current raw set already produces (baseline), rejecting only
  // what the edit itself introduced.
  test("the raw rule set is retained verbatim while the validated set drives application", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [
        { action: "no.such.action", chord: "Control+P" },
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]),
    );
    await wireClient(client, true);

    const state = keybindingsStore.getState();
    expect(state.rawOverrides).toEqual([
      { action: "no.such.action", chord: "Control+P" },
      { action: ACTIONS.composerFocus, chord: "Control+M" },
    ]);
    // Application/display still see only the validated rule.
    expect(state.overrides).toEqual([{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
    expect(state.warnings.map((w) => w.message)).toEqual(['unknown keybinding action "no.such.action"']);
  });

  test("a patch carrying a preserved invalid rule passes pre-flight and the rule reaches the hub verbatim", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: "no.such.action", chord: "Control+P" }]),
    );
    client.on("evener/settings/keybindings/patch", (params) => overridesPayload(4, params.config.rules));
    await wireClient(client, true);

    // Composed the way the editor composes: raw rules with one swapped. The
    // unknown-action warning is baseline (the current raw set already
    // produces it), so it must NOT reject the edit.
    const result = await keybindingsStore.getState().patchOverrides([
      { action: "no.such.action", chord: "Control+P" },
      { action: ACTIONS.composerFocus, chord: "Control+M" },
    ]);

    expect(result.revision).toBe(4);
    const patchCalls = client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]?.params).toEqual({
      expectedRevision: 3,
      config: {
        version: 1,
        rules: [
          { action: "no.such.action", chord: "Control+P" },
          { action: ACTIONS.composerFocus, chord: "Control+M" },
        ],
      },
    });
    // The preserved rule is still skipped (never applied) after the confirm.
    expect(keybindingsStore.getState().rawOverrides).toEqual([
      { action: "no.such.action", chord: "Control+P" },
      { action: ACTIONS.composerFocus, chord: "Control+M" },
    ]);
    expect(keybindingsStore.getState().overrides).toEqual([{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
  });

  test("baseline tolerance does NOT mask a conflict the edit itself introduces", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: "no.such.action", chord: "Control+P" }]),
    );
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    // palette.open's default overlaps a strict Control+K claim: a NEW
    // warning, not one the raw set already produces.
    await expect(
      keybindingsStore.getState().patchOverrides([
        { action: "no.such.action", chord: "Control+P" },
        { action: ACTIONS.composerFocus, chord: "Control+K" },
      ]),
    ).rejects.toThrow(`chord "Control+K" in scope "global" is already bound by "${ACTIONS.paletteOpen}"`);

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
  });

  test("the edited action's own invalid chord still rejects pre-flight alongside a preserved invalid rule", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: "no.such.action", chord: "Control+P" }]),
    );
    client.on("evener/settings/keybindings/patch", () => overridesPayload(4, []));
    await wireClient(client, true);

    await expect(
      keybindingsStore.getState().patchOverrides([
        { action: "no.such.action", chord: "Control+P" },
        { action: ACTIONS.railToggle, chord: "Control+W" }, // reserved on this platform
      ]),
    ).rejects.toThrow('chord "Control+W" is reserved');

    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    expect(keybindingsStore.getState().rawOverrides).toEqual([{ action: "no.such.action", chord: "Control+P" }]);
  });
});

// The stale-hub window: replacing the client must not let the previous
// hub's payload present as current (a PATCH composed from it would carry
// the old expectedRevision and rules to the NEW hub), and the registry must
// not keep firing the old hub's overrides one layer down.
describe("keybindings store: client replacement (stale-hub window)", () => {
  /** Replaces the wired client with one whose get never resolves: the
   * refresh starts (hubLoading) but never lands, holding the store in the
   * stale window. */
  async function rewireToHangingClient(): Promise<FakeClient> {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => new Promise<KeybindingsOverrides>(() => {}));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: true },
    });
    return client;
  }

  async function wireClientAWithPaletteOverride(): Promise<FakeClient> {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(7, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(keybindingsStore.getState().revision).toBe(7);
    expect(keybindingsStore.getState().loaded).toBe(true);
    return client;
  }

  test("client replacement un-applies the old hub's overrides from the registry and resets the hub state", async () => {
    await wireClientAWithPaletteOverride();

    await rewireToHangingClient();

    // Immediately - hub B's refresh is still in flight - the registry is
    // back on defaults and the store carries none of hub A's payload.
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
    const state = keybindingsStore.getState();
    expect(state.revision).toBe(0);
    expect(state.overrides).toEqual([]);
    expect(state.rawOverrides).toEqual([]);
    expect(state.warnings).toEqual([]);
    expect(state.loaded).toBe(false);
  });

  test("a patch attempted in the stale window throws before any wire request", async () => {
    await wireClientAWithPaletteOverride();
    const clientB = await rewireToHangingClient();

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]),
    ).rejects.toThrow(/unavailable/);

    expect(clientB.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    // Hub A's override stays un-applied: the rejected patch changed nothing.
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("after the new hub's refresh lands, patching works with the NEW hub's revision and rules", async () => {
    const clientA = await wireClientAWithPaletteOverride();

    // Hub B holds its own state at a COLLIDING revision (the overwrite the
    // stale window would have caused): the patch must compose from B's
    // (empty) rule set with B's expectedRevision, never A's payload.
    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => overridesPayload(7, []));
    clientB.on("evener/settings/keybindings/patch", (params) => overridesPayload(8, params.config.rules));
    connectionStore.getState().connect(clientB);
    connectionStore.setState({
      features: { ...(await clientB.connect()).features, keybindingsSettings: true },
    });
    await keybindingsStore.getState().refreshOverrides();

    expect(keybindingsStore.getState().loaded).toBe(true);
    const result = await keybindingsStore
      .getState()
      .patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
    expect(result.revision).toBe(8);
    const patchCalls = clientB.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]?.params).toEqual({
      expectedRevision: 7,
      config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Control+M" }] },
    });
    // Hub A never saw a patch.
    expect(clientA.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
  });
});

describe("keybindings store: reconcile resilience", () => {
  test("dropping an override whose default chord was reclaimed degrades to a warning and restores every action", async () => {
    // The reviewer's reproduction. Payload 1 is legal via freed-chord
    // reclaim: palette.open moves away, rail.toggle claims its default chord.
    const paletteDefault = defaultChordOf(ACTIONS.paletteOpen);
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [
        { action: ACTIONS.paletteOpen, chord: "Control+Y" },
        { action: ACTIONS.railToggle, chord: paletteDefault },
      ]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(bindingsFor(ACTIONS.railToggle).map((b) => b.id)).toEqual([`${ACTIONS.railToggle}#override`]);

    // Payload 2 drops palette.open's override: restoring its default needs
    // the chord rail.toggle is holding. The reconcile must not throw.
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, [{ action: ACTIONS.railToggle, chord: paletteDefault }]),
    });

    const state = keybindingsStore.getState();
    expect(state.hubError).toBeNull();
    expect(state.revision).toBe(2);
    // The restore wins: the rule claiming the restored default's chord is
    // skipped with a conflict warning, so BOTH actions fall back to their
    // defaults and every action stays bound.
    expect(state.warnings).toHaveLength(1);
    expect(state.warnings[0]?.reason).toBe("conflict");
    expect(state.warnings[0]?.rule).toEqual({ action: ACTIONS.railToggle, chord: paletteDefault });
    expect(state.warnings[0]?.conflictWith).toBe(ACTIONS.paletteOpen);
    expect(state.overrides).toEqual([]);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
    expect(bindingsFor(ACTIONS.railToggle).map((b) => b.id)).toEqual([
      ACTIONS.railToggle,
      `${ACTIONS.railToggle}#mod-twin`,
    ]);
  });

  test("a reconcile failure on the changed path sets hubError, keeps the revision retryable, and rolls back", async () => {
    const paletteDefault = defaultChordOf(ACTIONS.paletteOpen);
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    expect(keybindingsStore.getState().revision).toBe(1);

    // Out-of-band wedge: a foreign binding squats palette.open's default
    // chord, so restoring it (when the override is dropped) conflicts with a
    // binding no rule can be reassigned to.
    keybindingsRegistry
      .getState()
      .registerBinding({ id: "foreign", actionId: "foreign.action", chord: paletteDefault });

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, []),
    });

    const failed = keybindingsStore.getState();
    // Nothing escaped the notification dispatch; the failure is surfaced.
    expect(failed.hubError).not.toBeNull();
    // The revision did NOT advance past a failed apply...
    expect(failed.revision).toBe(1);
    // ...and the registry rolled back to its last good state.
    const paletteBindings = bindingsFor(ACTIONS.paletteOpen);
    expect(paletteBindings.map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(serializeChord(paletteBindings[0]?.chord ?? [])).toBe("Control+P");

    // Removing the wedge makes the SAME revision retryable (the stale guard
    // did not eat it).
    keybindingsRegistry.getState().unregisterBinding("foreign");
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(2, []),
    });
    const retried = keybindingsStore.getState();
    expect(retried.hubError).toBeNull();
    expect(retried.revision).toBe(2);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("resetKeybindingsStoreForTests is safe after a wedging attempt", async () => {
    const paletteDefault = defaultChordOf(ACTIONS.paletteOpen);
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    keybindingsRegistry
      .getState()
      .registerBinding({ id: "foreign", actionId: "foreign.action", chord: paletteDefault });

    expect(() => resetKeybindingsStoreForTests()).not.toThrow();
    expect(keybindingsStore.getState().revision).toBe(0);
    expect(keybindingsStore.getState().overrides).toEqual([]);
  });
});

describe("keybindings store: hubError/conflict clear symmetry", () => {
  // The parked 2b asymmetry: conflict was cleared ONLY by a successful patch
  // while hubError cleared on any successful apply, so a revision-race notice
  // outlived the state it described. Both now clear together.
  async function stageConflict(client: FakeClient): Promise<void> {
    client.on("evener/settings/keybindings/get", () => overridesPayload(3, []));
    client.on("evener/settings/keybindings/patch", () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        current: overridesPayload(6, []),
      });
    });
    await wireClient(client, true);
    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]),
    ).rejects.toThrow("revision conflict");
    expect(keybindingsStore.getState().conflict).toBe("revision conflict");
    expect(keybindingsStore.getState().hubError).toBe("revision conflict");
  }

  test("a successful changed notification after a conflict clears BOTH conflict and hubError", async () => {
    const client = new FakeClient("ready");
    await stageConflict(client);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(7, []),
    });

    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(keybindingsStore.getState().revision).toBe(7);
  });

  test("a successful refresh after a conflict clears BOTH conflict and hubError", async () => {
    const client = new FakeClient("ready");
    await stageConflict(client);
    client.on("evener/settings/keybindings/get", () => overridesPayload(7, []));

    await keybindingsStore.getState().refreshOverrides();

    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(keybindingsStore.getState().hubError).toBeNull();
  });

  test("a support drop after a conflict clears BOTH conflict and hubError", async () => {
    const client = new FakeClient("ready");
    await stageConflict(client);

    // The connection losing its feature set (disconnect/reconnect without
    // the feature) makes the conflict notice as stale as hubError.
    connectionStore.setState({ features: undefined });

    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(keybindingsStore.getState().hubError).toBeNull();
  });

  test("a stale changed notification (older revision) clears NEITHER conflict nor hubError", async () => {
    const client = new FakeClient("ready");
    await stageConflict(client);

    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(4, []),
    });

    expect(keybindingsStore.getState().conflict).toBe("revision conflict");
    expect(keybindingsStore.getState().hubError).toBe("revision conflict");
  });
});

describe("keybindings store: atomic un-apply", () => {
  /** Replaces the wired client with one whose get never resolves: the
   * refresh starts (hubLoading) but never lands. Same shape as the
   * stale-window describe's helper. */
  async function rewireToHangingClient(): Promise<FakeClient> {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => new Promise<KeybindingsOverrides>(() => {}));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: true },
    });
    return client;
  }

  test("client replacement un-applies a reclaimed-chord pair atomically", async () => {
    // Payload 1 is legal via freed-chord reclaim: palette.open moves away,
    // rail.toggle claims palette.open's default chord. Restoring BOTH on
    // un-apply needs the two-phase unwind: restoring palette.open while
    // rail.toggle's override still squats its default chord throws.
    const paletteDefault = defaultChordOf(ACTIONS.paletteOpen);
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [
        { action: ACTIONS.paletteOpen, chord: "Control+Y" },
        { action: ACTIONS.railToggle, chord: paletteDefault },
      ]),
    );
    await wireClient(clientA, true);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(bindingsFor(ACTIONS.railToggle).map((b) => b.id)).toEqual([`${ACTIONS.railToggle}#override`]);

    await rewireToHangingClient();

    // BOTH actions are back at their defaults: no orphaned override binding.
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
    expect(bindingsFor(ACTIONS.railToggle).map((b) => b.id)).toEqual([
      ACTIONS.railToggle,
      `${ACTIONS.railToggle}#mod-twin`,
    ]);
  });

  test("a wedged unwind rolls the registry back, keeps the applied map, and never throws", async () => {
    const paletteDefault = defaultChordOf(ACTIONS.paletteOpen);
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]),
    );
    await wireClient(clientA, true);

    // Out-of-band wedge: free the override binding and squat palette.open's
    // default chord with a foreign binding, so restoring the default throws.
    keybindingsRegistry.getState().unregisterBinding(`${ACTIONS.paletteOpen}#override`);
    keybindingsRegistry.getState().registerBinding({
      id: "foreign.squatter",
      actionId: "foreign.action",
      chord: paletteDefault,
    });

    // Client replacement's un-apply must NOT throw and must roll back: the
    // registry keeps the wedged shape exactly.
    const clientB = new FakeClient("ready");
    let resolveGet: ((payload: KeybindingsOverrides) => void) | undefined;
    clientB.on(
      "evener/settings/keybindings/get",
      () =>
        new Promise<KeybindingsOverrides>((resolve) => {
          resolveGet = resolve;
        }),
    );
    connectionStore.getState().connect(clientB);
    connectionStore.setState({
      features: { ...(await clientB.connect()).features, keybindingsSettings: true },
    });

    expect(bindingsFor(ACTIONS.paletteOpen)).toEqual([]);
    expect(keybindingsRegistry.getState().bindings.some((b) => b.id === "foreign.squatter")).toBe(true);

    // Hub B's refresh lands an EMPTY payload. Because the applied map
    // survived the rolled-back unwind, the reconcile RETRIES the restore of
    // palette.open, hits the same wedge, rolls back, and surfaces hubError -
    // the revision does not advance and the state never presents as loaded.
    // (Had the unwind cleared the map, the empty payload would apply clean
    // against the wedged registry and confirm revision 1.)
    await waitFor(() => expect(resolveGet).toBeDefined());
    resolveGet?.(overridesPayload(1, []));
    await waitFor(() => expect(keybindingsStore.getState().hubError).not.toBeNull());

    expect(keybindingsStore.getState().revision).toBe(0);
    expect(keybindingsStore.getState().loaded).toBe(false);
    expect(bindingsFor(ACTIONS.paletteOpen)).toEqual([]);
    expect(keybindingsRegistry.getState().bindings.some((b) => b.id === "foreign.squatter")).toBe(true);
  });
});

describe("keybindings store: patch gating", () => {
  test("a patch attempted while a refresh is in flight throws before any wire request", async () => {
    let pending: Promise<KeybindingsOverrides> | undefined;
    let resolvePending: ((payload: KeybindingsOverrides) => void) | undefined;
    const client = new FakeClient("ready");
    client.on(
      "evener/settings/keybindings/get",
      () => pending ?? overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    client.on("evener/settings/keybindings/patch", (params) => overridesPayload(2, params.config.rules));
    await wireClient(client, true);
    expect(keybindingsStore.getState().loaded).toBe(true);

    // A refresh is in flight (hubLoading): its payload can land at ANY
    // revision, so a concurrent PATCH's expectedRevision would race it.
    pending = new Promise<KeybindingsOverrides>((resolve) => {
      resolvePending = resolve;
    });
    const refresh = keybindingsStore.getState().refreshOverrides();
    await waitFor(() => expect(keybindingsStore.getState().hubLoading).toBe(true));

    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]),
    ).rejects.toThrow(/unavailable/);
    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    // The rejection applies to the CURRENT generation (the loading-window
    // race), so hubError still surfaces: the fence's throw-only posture is
    // only for writes whose generation has ended (finding 22).
    expect(keybindingsStore.getState().hubError).toBe("Hub keybindings settings are unavailable.");

    resolvePending?.(overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
    await refresh;
    expect(keybindingsStore.getState().hubLoading).toBe(false);
  });
});

describe("keybindings store: character-key pref in the restore simulation", () => {
  /** What the cheatsheetController does while the pref is off. */
  function turnCharacterKeyPrefOff(): void {
    prefsStore.setState({ characterKeyTriggers: false });
    keybindingsRegistry.getState().unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
  }

  function wireCharacterKeyPayload(): Promise<FakeClient> {
    // composer.focus takes "Shift+?" (the character-key chord with a
    // REQUIRED shift); cheatsheet.toggle moves to Control+Slash. With the
    // pref OFF there is no "?" binding to conflict with, so this payload
    // must apply clean.
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [
        { action: ACTIONS.composerFocus, chord: "Shift+?" },
        { action: ACTIONS.cheatsheetToggle, chord: "Control+Slash" },
      ]),
    );
    client.on("evener/settings/keybindings/patch", (params) => overridesPayload(2, params.config.rules));
    return wireClient(client, true).then(() => client);
  }

  test("with the pref off, resetting cheatsheet.toggle past a composer Shift+? override is accepted", async () => {
    turnCharacterKeyPrefOff();
    const client = await wireCharacterKeyPayload();
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(keybindingsStore.getState().revision).toBe(1);

    // The Reset: the PATCH payload drops cheatsheet.toggle's override, so
    // its defaults restore. The restore simulation must mirror the pref -
    // with the "?" trigger absent from the live registry, the restored set
    // must not claim it either, so it cannot phantom-conflict with the
    // composer override that survives the patch.
    const result = await keybindingsStore
      .getState()
      .patchOverrides([{ action: ACTIONS.composerFocus, chord: "Shift+?" }]);
    expect(result.revision).toBe(2);
    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(1);
    expect(serializeChord(bindingsFor(ACTIONS.cheatsheetToggle)[0]?.chord ?? [])).toBe("Control+/");
  });

  test("with the pref on, the same reset still rejects: the restored ? binding would conflict", async () => {
    const client = await wireCharacterKeyPayload();
    expect(keybindingsStore.getState().hubError).toBeNull();

    // Pref ON: the restore legitimately reclaims "?", which overlaps the
    // surviving composer Shift+? - the preflight must still reject.
    await expect(
      keybindingsStore.getState().patchOverrides([{ action: ACTIONS.composerFocus, chord: "Shift+?" }]),
    ).rejects.toThrow(/already bound by/);
    expect(client.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
  });
});

describe("keybindings store: support loss", () => {
  test("support resolving to UNSUPPORTED un-applies the hub's overrides from the registry", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);

    // The feature set is KNOWN and no longer advertises keybindings: the
    // section claims "the built-in defaults are in effect", so the registry
    // must stop firing the override.
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });

    expect(keybindingsStore.getState().hubSupport).toBe("unsupported");
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("a transient disconnect of a SUPPORTED hub keeps the overrides applied", async () => {
    // The ruled behavior, pinned: while a supported hub is temporarily
    // unreachable the feature set is unknown, not contradicted - the rows
    // show the overrides read-only and the chords keep firing.
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);

    connectionStore.setState({ state: "idle", features: undefined });

    expect(keybindingsStore.getState().hubSupport).toBe("unknown");
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(serializeChord(bindingsFor(ACTIONS.paletteOpen)[0]?.chord ?? [])).toBe("Control+P");
  });
});

describe("keybindings store: support flap discards hub state", () => {
  test("a lower-revision refresh after an unsupported flap is ACCEPTED, and edits compose from the new payload", async () => {
    const client = new FakeClient("ready");
    let current = overridesPayload(7, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    client.on("evener/settings/keybindings/get", () => current);
    client.on("evener/settings/keybindings/patch", (params) =>
      overridesPayload(params.expectedRevision + 1, params.config.rules),
    );
    await wireClient(client, true);
    expect(keybindingsStore.getState().revision).toBe(7);

    // Support drops: the registry un-applies AND the hub payload state
    // resets - a retained revision 7 would eat the returning hub's
    // lower-revision refresh via the stale guard.
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
    const dropped = keybindingsStore.getState();
    expect(dropped.hubSupport).toBe("unsupported");
    expect(dropped.loaded).toBe(false);
    expect(dropped.revision).toBe(0);
    expect(dropped.overrides).toEqual([]);
    expect(dropped.rawOverrides).toEqual([]);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);

    // The same hub returns at a LOWER revision (restored backup, reset
    // state file): the refresh must not be discarded as stale.
    current = overridesPayload(3, [{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: true },
    });
    await waitFor(() => expect(keybindingsStore.getState().loaded).toBe(true));
    expect(keybindingsStore.getState().revision).toBe(3);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([`${ACTIONS.composerFocus}#override`]);

    // An edit composes from the NEW hub's payload with the NEW revision.
    await keybindingsStore
      .getState()
      .patchOverrides([
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.paletteOpen, chord: "Control+Y" },
      ]);
    const patchCalls = client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(1);
    expect(patchCalls[0]?.params).toEqual({
      expectedRevision: 3,
      config: {
        version: 1,
        rules: [
          { action: ACTIONS.composerFocus, chord: "Control+M" },
          { action: ACTIONS.paletteOpen, chord: "Control+Y" },
        ],
      },
    });
  });

  test("a changed notification arriving while UNSUPPORTED does not touch the registry or the store", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);

    // A late notification from before the support drop must not re-install
    // overrides into the registry that was just un-applied.
    client.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: overridesPayload(9, [{ action: ACTIONS.paletteOpen, chord: "Control+Y" }]),
    });

    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
    const state = keybindingsStore.getState();
    expect(state.revision).toBe(0);
    expect(state.loaded).toBe(false);
    expect(state.hubError).toBeNull();
  });

  test("a patch response landing after support loss does not apply", async () => {
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

    const patch = keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    await waitFor(() => expect(resolvePatch).toBeDefined());

    // Support drops while the PATCH is in flight: the response must not
    // re-apply over the un-applied registry and reset hub state.
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
    resolvePatch?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
    const result = await patch;

    expect(result.revision).toBe(0);
    expect(keybindingsStore.getState().revision).toBe(0);
    expect(keybindingsStore.getState().hubError).toBeNull();
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("a refresh rejecting after support loss leaves the state clean (no hubError)", async () => {
    // The FIRST get lands normally; the second hangs so support can drop
    // while the refresh is in flight.
    const client = new FakeClient("ready");
    let rejectGet: ((error: Error) => void) | undefined;
    let hang = false;
    client.on("evener/settings/keybindings/get", () => {
      if (!hang) return overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
      return new Promise<KeybindingsOverrides>((_, reject) => {
        rejectGet = reject;
      });
    });
    await wireClient(client, true);
    expect(keybindingsStore.getState().loaded).toBe(true);

    hang = true;
    const refresh = keybindingsStore.getState().refreshOverrides();
    await waitFor(() => expect(keybindingsStore.getState().hubLoading).toBe(true));

    // Support drops mid-flight: the cleanup un-applies and resets the hub
    // state (hubLoading false, hubError null).
    connectionStore.setState({
      features: { ...(await client.connect()).features, keybindingsSettings: false },
    });
    expect(keybindingsStore.getState().hubSupport).toBe("unsupported");
    expect(keybindingsStore.getState().hubLoading).toBe(false);

    // The in-flight refresh now rejects: the catch/finally must not
    // overwrite the support-drop cleanup with a stale "could not load"
    // hubError - the section claims the built-in defaults are in effect
    // (finding 23).
    rejectGet?.(new Error("socket went away"));
    await refresh;

    const state = keybindingsStore.getState();
    expect(state.hubError).toBeNull();
    expect(state.hubLoading).toBe(false);
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });
});

describe("keybindings store: write serialization", () => {
  test("concurrent patches serialize: each composes expectedRevision and payload after the previous lands", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    client.on("evener/settings/keybindings/patch", (params) =>
      overridesPayload(params.expectedRevision + 1, params.config.rules),
    );
    await wireClient(client, true);

    // Two edits in the SAME tick. Both thunks compose against the raw set,
    // but the second runs only after the first has fully landed, so it
    // folds the first edit's confirmed payload in instead of racing it with
    // the same expectedRevision (a self-inflicted conflict).
    const first = keybindingsStore
      .getState()
      .patchOverrides(() => [
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.paletteOpen, chord: "Control+P" },
      ]);
    const second = keybindingsStore
      .getState()
      .patchOverrides(() => [
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]);
    const [firstResult, secondResult] = await Promise.all([first, second]);

    expect(firstResult.revision).toBe(2);
    expect(secondResult.revision).toBe(3);
    const patchCalls = client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(2);
    expect(patchCalls[0]?.params).toEqual({
      expectedRevision: 1,
      config: { version: 1, rules: [{ action: ACTIONS.paletteOpen, chord: "Control+P" }] },
    });
    expect(patchCalls[1]?.params).toEqual({
      expectedRevision: 2,
      config: {
        version: 1,
        rules: [
          { action: ACTIONS.paletteOpen, chord: "Control+P" },
          { action: ACTIONS.composerFocus, chord: "Control+M" },
        ],
      },
    });
    expect(keybindingsStore.getState().revision).toBe(3);
    expect(keybindingsStore.getState().conflict).toBeNull();
    expect(bindingsFor(ACTIONS.paletteOpen).map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([`${ACTIONS.composerFocus}#override`]);
  });

  test("a failed write does not block the queue: the next write runs against the post-failure state", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    let failFirst = true;
    client.on("evener/settings/keybindings/patch", (params) => {
      if (failFirst) {
        failFirst = false;
        throw new Error("socket went away");
      }
      return overridesPayload(params.expectedRevision + 1, params.config.rules);
    });
    await wireClient(client, true);

    const first = keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    const second = keybindingsStore
      .getState()
      .patchOverrides(() => [
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]);

    await expect(first).rejects.toThrow("socket went away");
    // The first write failed WITHOUT touching the hub: the second still
    // composes from revision 1 and lands.
    const secondResult = await second;
    expect(secondResult.revision).toBe(2);
    const patchCalls = client.calls.filter((c) => c.method === "evener/settings/keybindings/patch");
    expect(patchCalls).toHaveLength(2);
    expect(patchCalls[1]?.params).toEqual({
      expectedRevision: 1,
      config: { version: 1, rules: [{ action: ACTIONS.composerFocus, chord: "Control+M" }] },
    });
  });
});

// A queued write is fenced to the ready generation it was CREATED under:
// compose-at-execution (finding 16) is right within one generation, but a
// write whose generation ended while it queued carries edit intent made
// against the hub the user was looking at - it must die with that
// generation, never land on the new hub.
describe("keybindings store: write generation fence", () => {
  test("a write queued behind an in-flight write rejects after a mid-flight client replacement, with no wire request to the new hub", async () => {
    // Hub A: get lands immediately; the PATCH hangs so a second write queues.
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    let resolvePatchA: ((value: KeybindingsOverrides) => void) | undefined;
    clientA.on(
      "evener/settings/keybindings/patch",
      () =>
        new Promise<KeybindingsOverrides>((resolve) => {
          resolvePatchA = resolve;
        }),
    );
    await wireClient(clientA, true);

    const first = keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    await waitFor(() =>
      expect(clientA.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(1),
    );
    // Write #2 queues behind the in-flight write, created while hub A's
    // state is the displayed state.
    const second = keybindingsStore
      .getState()
      .patchOverrides(() => [
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]);

    // The client is replaced mid-flight: hub B loads cleanly at revision 1.
    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    clientB.on("evener/settings/keybindings/patch", (params) =>
      overridesPayload(params.expectedRevision + 1, params.config.rules),
    );
    connectionStore.getState().connect(clientB);
    connectionStore.setState({
      features: { ...(await clientB.connect()).features, keybindingsSettings: true },
    });
    await waitFor(() => expect(keybindingsStore.getState().loaded).toBe(true));

    // A's hanging PATCH resolves: its response must not apply (the
    // post-response staleness branch), and write #2 - created under A's
    // generation - must REJECT with the unavailable-class error having sent
    // NO request to B. Without the call-time fence, run #2 would recompute
    // against B's now-loaded state, pass every execution-time guard, and
    // land the A-era edit on B.
    resolvePatchA?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
    await first;
    await expect(second).rejects.toThrow(/unavailable/);

    expect(clientB.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(0);
    expect(keybindingsStore.getState().revision).toBe(1);
    expect(bindingsFor(ACTIONS.composerFocus)).toHaveLength(2);
    expect(bindingsFor(ACTIONS.paletteOpen)).toHaveLength(2);
  });

  test("the fence rejection leaves the NEW hub's clean state untouched: no hubError, still editable", async () => {
    // Same setup as the fence test: write #2 queues behind A's hanging
    // PATCH, the client is replaced mid-flight, hub B loads cleanly.
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    let resolvePatchA: ((value: KeybindingsOverrides) => void) | undefined;
    clientA.on(
      "evener/settings/keybindings/patch",
      () =>
        new Promise<KeybindingsOverrides>((resolve) => {
          resolvePatchA = resolve;
        }),
    );
    await wireClient(clientA, true);

    const first = keybindingsStore.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    await waitFor(() =>
      expect(clientA.calls.filter((c) => c.method === "evener/settings/keybindings/patch")).toHaveLength(1),
    );
    const second = keybindingsStore
      .getState()
      .patchOverrides(() => [
        ...keybindingsStore.getState().rawOverrides,
        { action: ACTIONS.composerFocus, chord: "Control+M" },
      ]);

    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => overridesPayload(1, []));
    clientB.on("evener/settings/keybindings/patch", (params) =>
      overridesPayload(params.expectedRevision + 1, params.config.rules),
    );
    connectionStore.getState().connect(clientB);
    connectionStore.setState({
      features: { ...(await clientB.connect()).features, keybindingsSettings: true },
    });
    await waitFor(() => expect(keybindingsStore.getState().loaded).toBe(true));

    resolvePatchA?.(overridesPayload(2, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]));
    await first;
    await expect(second).rejects.toThrow(/unavailable/);

    // The rejection belonged to the DEAD generation: setting hubError would
    // land "settings are unavailable" on hub B's freshly-loaded state, and
    // the round-3 editing gate would read the clean new hub read-only until
    // something cleared it (finding 22). hubError stays null ...
    const state = keybindingsStore.getState();
    expect(state.hubError).toBeNull();
    expect(state.loaded).toBe(true);
    // ... and the new hub stays editable: a fresh write under B's generation
    // composes and lands.
    const result = await keybindingsStore
      .getState()
      .patchOverrides([{ action: ACTIONS.composerFocus, chord: "Control+M" }]);
    expect(result.revision).toBe(2);
  });

  // The same-generation half is pinned by "concurrent patches serialize"
  // (write serialization describe): two writes created in one tick under one
  // generation both land in order - the fence must not reject them.
});

// A characterKeyTriggers flip re-validates the persisted rules against the
// new pref: the simulation treats the "?" entry as pref-authoritative (the
// re-apply runs in the flip instant, before the controller's reconcile has
// re-registered/unregistered it), so a rule skipped under one pref applies
// under the other - and vice versa - with the warnings list following.
describe("keybindings store: pref flips re-validate persisted overrides", () => {
  test("a Shift+? override skipped while the pref is on applies when the pref flips off, and is skipped again when it flips back on", async () => {
    // Pref ON (default), "?" live: the claim conflicts with the built-in
    // trigger and is skipped with a conflict warning.
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.composerFocus, chord: "Shift+?" }]),
    );
    await wireClient(client, true);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([
      ACTIONS.composerFocus,
      `${ACTIONS.composerFocus}#mod-twin`,
    ]);
    const skipped = keybindingsStore.getState().warnings;
    expect(skipped).toHaveLength(1);
    expect(skipped[0]?.reason).toBe("conflict");
    expect(skipped[0]?.conflictWith).toBe(ACTIONS.cheatsheetToggle);
    // The RAW set retains the skipped rule - it is still the hub's state.
    expect(keybindingsStore.getState().rawOverrides).toEqual([{ action: ACTIONS.composerFocus, chord: "Shift+?" }]);

    // Pref OFF: the re-apply simulates "?" as absent (it is still live in
    // the registry for this instant - the controller's reconcile has not
    // unregistered it yet, and in this test file no reconcile is installed
    // at all), so the claim validates clean and APPLIES; the warning clears.
    prefsStore.getState().setCharacterKeyTriggers(false);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([`${ACTIONS.composerFocus}#override`]);
    expect(keybindingsStore.getState().warnings).toEqual([]);
    expect(keybindingsStore.getState().rawOverrides).toEqual([{ action: ACTIONS.composerFocus, chord: "Shift+?" }]);

    // Pref ON again: symmetric - the claim stops validating (the simulated
    // map claims "?" again), the restore wins, and the warning returns.
    prefsStore.getState().setCharacterKeyTriggers(true);
    expect(bindingsFor(ACTIONS.composerFocus).map((b) => b.id)).toEqual([
      ACTIONS.composerFocus,
      `${ACTIONS.composerFocus}#mod-twin`,
    ]);
    const reskipped = keybindingsStore.getState().warnings;
    expect(reskipped).toHaveLength(1);
    expect(reskipped[0]?.reason).toBe("conflict");
    expect(reskipped[0]?.conflictWith).toBe(ACTIONS.cheatsheetToggle);
  });

  test("a pref flip with no ?-related overrides changes nothing", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () =>
      overridesPayload(1, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    await wireClient(client, true);
    const before = bindingsFor(ACTIONS.paletteOpen);
    expect(before.map((b) => b.id)).toEqual([`${ACTIONS.paletteOpen}#override`]);

    prefsStore.getState().setCharacterKeyTriggers(false);
    prefsStore.getState().setCharacterKeyTriggers(true);

    // Same effective map - same binding OBJECTS, nothing torn down - and no
    // warnings appeared.
    expect(bindingsFor(ACTIONS.paletteOpen)).toEqual(before);
    expect(bindingsFor(ACTIONS.paletteOpen)[0]).toBe(before[0]);
    expect(keybindingsStore.getState().warnings).toEqual([]);
    expect(keybindingsStore.getState().overrides).toEqual([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    expect(keybindingsStore.getState().revision).toBe(1);
  });
});
