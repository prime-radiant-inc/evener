import { describe, expect, test, vi } from "vitest";
import { ACTIONS } from "./keybindingActions";
import type { KeybindingsRegistry } from "./keybindingRegistry";
import {
  createKeybindingsStore,
  fromWireOverrides,
  type KeybindingsStoreDeps,
  type KeybindingsSupport,
  keybindingsSupport,
} from "./keybindingsStore";
import { deferred } from "./testing/deferred";
import { FakeClient } from "./testing/fakeClient";
import { memoryKeybindingDraftStorage } from "./testing/keybindingDraftStorage";
import { registryWithDefaults } from "./testing/keybindingRegistry";
import type { KeybindingsOverrides } from "./types.gen";

const getMethod = "evener/settings/keybindings/get";
const patchMethod = "evener/settings/keybindings/patch";
const changedMethod = "evener/settings/keybindings/changed";
const rules = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];

function payload(revision: number, rules: KeybindingsOverrides["rules"]): KeybindingsOverrides {
  return { version: 1, revision, rules };
}

function bindingIdsFor(registry: KeybindingsRegistry, actionId: string): string[] {
  return registry
    .getState()
    .bindings.filter((b) => b.actionId === actionId)
    .map((b) => b.id);
}

/** A store wired the way an app's adapter wires it: supported, one ready
 * generation begun, refreshed once. */
async function readyStore(client: FakeClient, deps: Partial<KeybindingsStoreDeps> = {}) {
  const store = createKeybindingsStore({ client, ...deps });
  store.setSupport("supported");
  store.beginReadyGeneration();
  await store.getState().refreshOverrides();
  return store;
}

function clientServing(revision: number, served: KeybindingsOverrides["rules"] = []): FakeClient {
  const client = new FakeClient("ready");
  client.on(getMethod, () => payload(revision, served));
  return client;
}

describe("two stores share nothing", () => {
  test("a hub payload applied through one store reconciles only that store's registry and state", async () => {
    const registryA = registryWithDefaults();
    const registryB = registryWithDefaults();
    const defaultsB = bindingIdsFor(registryB, ACTIONS.paletteOpen);

    const storeA = await readyStore(clientServing(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]), {
      registry: registryA,
    });
    const storeB = await readyStore(clientServing(7), { registry: registryB });

    expect(bindingIdsFor(registryA, ACTIONS.paletteOpen)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(bindingIdsFor(registryB, ACTIONS.paletteOpen)).toEqual(defaultsB);
    expect(storeA.getState()).toMatchObject({ revision: 3, loaded: true, hubSupport: "supported" });
    expect(storeA.getState().overrides).toEqual([{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    expect(storeB.getState()).toMatchObject({ revision: 7, loaded: true, overrides: [] });

    // Ending A's ready generation touches neither B's confirmed state nor
    // B's registry; A's own overrides stay applied (a transient disconnect
    // keeps the hub's shortcuts firing) while its confirmed state drops.
    storeA.endReadyGeneration();
    expect(storeA.getState()).toMatchObject({ loaded: false, revision: 0 });
    expect(bindingIdsFor(registryA, ACTIONS.paletteOpen)).toEqual([`${ACTIONS.paletteOpen}#override`]);
    expect(storeB.getState()).toMatchObject({ loaded: true, revision: 7 });
    expect(bindingIdsFor(registryB, ACTIONS.paletteOpen)).toEqual(defaultsB);
  });

  test("a changed notification on one client reaches only the store subscribed to it", async () => {
    const clientA = clientServing(1);
    const storeA = await readyStore(clientA, { registry: registryWithDefaults() });
    const storeB = await readyStore(clientServing(1), { registry: registryWithDefaults() });
    const before = storeB.getState();

    clientA.emitNotification({
      method: changedMethod,
      params: payload(2, [{ action: ACTIONS.paletteOpen, chord: null }]),
    });

    expect(storeA.getState()).toMatchObject({ revision: 2, overrides: [{ action: ACTIONS.paletteOpen, chord: null }] });
    expect(storeB.getState()).toBe(before);
  });

  test("two draft editors over two storage ports checkpoint independently", async () => {
    const draftsA = memoryKeybindingDraftStorage();
    const draftsB = memoryKeybindingDraftStorage();
    const storeA = await readyStore(clientServing(3), { drafts: draftsA.storage });
    const storeB = await readyStore(clientServing(9), { drafts: draftsB.storage });

    storeA.getState().editDraft(rules);

    expect(draftsA.stored()).toMatchObject({ baseRevision: 3, writeUncertain: false });
    expect(draftsB.stored()).toBeNull();
    expect(storeA.getState().draft).toEqual({ version: 1, revision: 3, rules });
    expect(storeB.getState().draft).toBeNull();
  });
});

describe("store shape", () => {
  test("subscribe delivers new and previous state; getInitialState is what the store was created with", () => {
    const store = createKeybindingsStore({ client: new FakeClient("ready") });
    const initial = store.getState();
    expect(store.getInitialState()).toBe(initial);
    expect(initial).toMatchObject({ hubSupport: "unknown", loaded: false, revision: 0, draft: null, saving: false });
    const listener = vi.fn();
    const stop = store.subscribe(listener);
    store.setSupport("unsupported");
    expect(listener).toHaveBeenCalledWith(expect.objectContaining({ hubSupport: "unsupported" }), initial);
    stop();
    store.setSupport("unknown");
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe("support transitions", () => {
  test("support resolving after the client is ready loads once, under the live generation", async () => {
    const client = clientServing(4);
    const store = createKeybindingsStore({ client, registry: registryWithDefaults() });
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(0);

    store.setSupport("supported");
    await vi.waitFor(() => expect(store.getState().revision).toBe(4));
    expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(1);

    // Publishing the same support again is not a transition and loads nothing.
    store.setSupport("supported");
    await Promise.resolve();
    expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(1);
  });

  test("support published before a generation exists loads nothing until the host's first refresh", async () => {
    const client = clientServing(2);
    const store = createKeybindingsStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await Promise.resolve();
    expect(client.calls).toHaveLength(0);
    await store.getState().refreshOverrides();
    expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(1);
  });
});

describe("hub replacement", () => {
  test("detachHub leaves the previous hub's confirmed state non-current, so nothing can be edited or patched against it", async () => {
    const store = await readyStore(clientServing(5, rules));
    expect(store.getState()).toMatchObject({ loaded: true, revision: 5 });

    // A host following the public doc alone may detach without ending the
    // generation first; the reset payload must not read as confirmed.
    expect(store.detachHub()).toBe(true);

    expect(store.getState()).toMatchObject({ loaded: false, revision: 0, rawOverrides: [], overrides: [] });
    expect(() => store.getState().editDraft([])).toThrow("unavailable");
    await expect(store.getState().patchOverrides([])).rejects.toThrow("unavailable");
  });
});

describe("without a registry", () => {
  test("the hub's rules publish verbatim, nothing is validated and nothing needs un-applying", async () => {
    const served = [{ action: "from.a.newer.client", chord: "Control+Shift+Q" }];
    const store = await readyStore(clientServing(2, served));
    expect(store.getState()).toMatchObject({ revision: 2, overrides: served, rawOverrides: served, warnings: [] });

    store.setSupport("unsupported");
    expect(store.getState()).toMatchObject({ hubSupport: "unsupported", overrides: [], revision: 0, hubError: null });
  });
});

describe("the checkpointed draft editor", () => {
  test("persists the intent before the PATCH leaves, clears it on the ack and applies the canonical payload", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    let checkpointAtDispatch: unknown = null;
    client.on(patchMethod, () => {
      checkpointAtDispatch = drafts.stored();
      return payload(4, rules);
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    await store.getState().saveDraft(rules);

    expect(checkpointAtDispatch).toMatchObject({ baseRevision: 3, rules, writeUncertain: true });
    expect(drafts.stored()).toBeNull();
    expect(store.getState()).toMatchObject({
      revision: 4,
      rawOverrides: rules,
      draft: null,
      saving: false,
      writeUncertain: false,
      draftConflict: false,
    });
    expect(client.calls.at(-1)).toMatchObject({
      method: patchMethod,
      params: { expectedRevision: 3, config: { version: 1, rules } },
    });
  });

  test("a lost reply leaves the write uncertain and blocks edits until an authoritative read", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    // The unknown outcome is the writeUncertain fact, not a message: draftError
    // is the draft port's channel and the raw client error never reaches state.
    expect(store.getState()).toMatchObject({
      writeUncertain: true,
      draftConflict: true,
      draftError: null,
      hubError: null,
      draft: { rules },
    });
    expect(() => store.getState().editDraft([])).toThrow("unavailable");

    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({
      writeUncertain: false,
      draftConflict: false,
      draftError: null,
      draft: { rules },
    });
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
  });

  test("a read carrying loadError after a lost reply settles nothing", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = new FakeClient("ready");
    let response: KeybindingsOverrides = payload(3, []);
    client.on(getMethod, () => response);
    client.on(patchMethod, () => {
      throw new Error("lost reply");
    });
    const store = await readyStore(client, { drafts: drafts.storage });
    await expect(store.getState().saveDraft(rules)).rejects.toThrow();

    response = { ...payload(0, []), loadError: "state file unreadable" };
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      writeUncertain: true,
      draftError: null,
      loadError: "state file unreadable",
    });
    expect(drafts.stored()).toMatchObject({ writeUncertain: true });
  });

  test("a malformed PATCH reply is hub-sourced: it rides hubError and leaves the write uncertain", async () => {
    const client = clientServing(3);
    client.on(patchMethod, () => ({ version: 2 }) as unknown as KeybindingsOverrides);
    const store = await readyStore(client);

    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    expect(store.getState()).toMatchObject({ writeUncertain: true, draftError: null });
    expect(store.getState().hubError).not.toBeNull();
  });

  /** A store whose ready generation ends while a checkpointed write is in
   * flight, then begins a new one on the same instance (the web adapter's
   * singleton does exactly this across a reconnect). */
  async function saveAcrossGenerationEnd(settle: "resolve" | "reject") {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const store = await readyStore(client, { drafts: drafts.storage });
    const save = store.getState().saveDraft(rules);
    expect(store.getState().saving).toBe(true);
    // The fake defers its handler by a microtask; the reply must be on the wire
    // before the generation ends.
    await vi.waitFor(() => expect(client.calls.some((c) => c.method === patchMethod)).toBe(true));

    store.endReadyGeneration();
    if (settle === "resolve") reply.resolve(payload(4, rules));
    else reply.reject(new Error("lost reply"));
    await save.then(
      () => undefined,
      () => undefined,
    );
    return { store, drafts, save };
  }

  test("a generation ending mid-write leaves the write uncertain, not saving, and the next read settles it", async () => {
    const { store, drafts } = await saveAcrossGenerationEnd("resolve");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, loaded: false, draftError: null });

    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, loaded: true, draftError: null });
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
    expect(() => store.getState().editDraft([])).not.toThrow();
  });

  test("a write rejecting after its generation ended writes nothing back and the store stays editable after reconnect", async () => {
    const { store, save } = await saveAcrossGenerationEnd("reject");
    await expect(save).rejects.toThrow("lost reply");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftError: null });

    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
    expect(() => store.getState().editDraft([])).not.toThrow();
  });

  test("a generation ending mid-refresh ends hubLoading with it", async () => {
    const client = new FakeClient("ready");
    const reply = deferred<KeybindingsOverrides>();
    client.on(getMethod, () => reply.promise);
    const store = createKeybindingsStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    const refresh = store.getState().refreshOverrides();
    expect(store.getState().hubLoading).toBe(true);

    store.endReadyGeneration();
    expect(store.getState().hubLoading).toBe(false);
    reply.resolve(payload(1, []));
    await refresh;
    expect(store.getState()).toMatchObject({ hubLoading: false, loaded: false, revision: 0 });
  });

  test("a store built over a stored checkpoint restores the draft synchronously", () => {
    const drafts = memoryKeybindingDraftStorage();
    drafts.storage.save({ id: "x", baseRevision: 3, rules, writeUncertain: true });
    const store = createKeybindingsStore({ client: new FakeClient("ready"), drafts: drafts.storage });
    expect(store.getState()).toMatchObject({ draft: { revision: 3, rules }, writeUncertain: true });
  });
});

describe("payload rules shared by both apps", () => {
  test("a GET carrying loadError is authoritative even at a lower revision", async () => {
    const client = new FakeClient("ready");
    let response: KeybindingsOverrides = payload(5, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    client.on(getMethod, () => response);
    const store = await readyStore(client, { registry: registryWithDefaults() });
    expect(store.getState().revision).toBe(5);

    response = { ...payload(0, []), loadError: "decode keybindings state: unexpected end" };
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      revision: 0,
      rawOverrides: [],
      loadError: "decode keybindings state: unexpected end",
      hubError: "decode keybindings state: unexpected end",
    });
  });

  test.each<[Parameters<typeof keybindingsSupport>[0], KeybindingsSupport]>([
    [undefined, "unknown"],
    [{ keybindingsSettings: true }, "supported"],
    [{ keybindingsSettings: false }, "unsupported"],
    [{}, "unsupported"],
  ])("keybindingsSupport(%j) is %s", (features, expected) => {
    expect(keybindingsSupport(features)).toBe(expected);
  });

  const wellFormed = { version: 1, revision: 2, rules: [{ action: "a", chord: null }] };
  test.each<[string, unknown, KeybindingsOverrides | undefined]>([
    ["a well-formed payload passes through", wellFormed, wellFormed],
    ["a loadError string is accepted", { ...wellFormed, loadError: "gone" }, { ...wellFormed, loadError: "gone" }],
    ["the wrong version", { version: 2, revision: 2, rules: [] }, undefined],
    ["a negative revision", { version: 1, revision: -1, rules: [] }, undefined],
    ["a non-string action", { version: 1, revision: 1, rules: [{ action: 1, chord: null }] }, undefined],
    ["a non-string loadError", { version: 1, revision: 1, rules: [], loadError: 4 }, undefined],
    ["a non-object", "nope", undefined],
  ])("fromWireOverrides: %s", (_name, value, expected) => {
    expect(fromWireOverrides(value)).toEqual(expected);
  });
});
