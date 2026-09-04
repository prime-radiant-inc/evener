import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ACTIONS } from "../keybindings/actions";
import { serializeChord } from "../keybindings/chord";
import { registerDefaultBindings } from "../keybindings/defaults";
import { type Binding, keybindingsRegistry } from "../keybindings/registry";
import { WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { KeybindingsOverrides, KeybindingsRule } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { keybindingsStore, resetKeybindingsStoreForTests } from "./keybindings";

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
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetKeybindingsStoreForTests();
  resetRegistryToDefaults();
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
