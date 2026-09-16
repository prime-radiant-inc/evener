import { parseKeybinding } from "tinykeys";
import { describe, expect, test, vi } from "vitest";
import { ACTIONS } from "./keybindingActions";
import { registerDefaultBindings } from "./keybindingDefaults";
import { createKeybindingsRegistry, type KeybindingsRegistry } from "./keybindingRegistry";
import {
  createKeybindingsStore,
  fromWireOverrides,
  type KeybindingDraftCheckpoint,
  type KeybindingDraftStorage,
  keybindingsSupport,
} from "./keybindingsStore";
import { FakeClient } from "./testing/fakeClient";
import type { KeybindingsOverrides } from "./types.gen";

function registryWithDefaults(): KeybindingsRegistry {
  const registry = createKeybindingsRegistry(parseKeybinding);
  registerDefaultBindings(registry);
  return registry;
}

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
async function readyStore(client: FakeClient, registry?: KeybindingsRegistry) {
  const store = createKeybindingsStore({ client, registry });
  store.setSupport("supported");
  store.beginReadyGeneration();
  await store.getState().refreshOverrides();
  return store;
}

function memoryDraftStorage() {
  let stored: unknown = null;
  let id = 0;
  const storage: KeybindingDraftStorage = {
    createId: () => String(++id),
    load: () => structuredClone(stored),
    save: (checkpoint) => {
      stored = structuredClone(checkpoint);
    },
    removeIf: (checkpoint) => {
      if (JSON.stringify(checkpoint) === JSON.stringify(stored)) stored = null;
    },
  };
  return { storage, load: () => stored as KeybindingDraftCheckpoint | null };
}

describe("two stores share nothing", () => {
  test("a hub payload applied through one store reconciles only that store's registry and state", async () => {
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () =>
      payload(3, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]),
    );
    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => payload(7, []));
    const registryA = registryWithDefaults();
    const registryB = registryWithDefaults();
    const defaultsB = bindingIdsFor(registryB, ACTIONS.paletteOpen);

    const storeA = await readyStore(clientA, registryA);
    const storeB = await readyStore(clientB, registryB);

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
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () => payload(1, []));
    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => payload(1, []));
    const storeA = await readyStore(clientA, registryWithDefaults());
    const storeB = await readyStore(clientB, registryWithDefaults());
    const before = storeB.getState();

    clientA.emitNotification({
      method: "evener/settings/keybindings/changed",
      params: payload(2, [{ action: ACTIONS.paletteOpen, chord: null }]),
    });

    expect(storeA.getState()).toMatchObject({ revision: 2, overrides: [{ action: ACTIONS.paletteOpen, chord: null }] });
    expect(storeB.getState()).toBe(before);
  });

  test("two draft editors over two storage ports checkpoint independently", async () => {
    const draftsA = memoryDraftStorage();
    const draftsB = memoryDraftStorage();
    const clientA = new FakeClient("ready");
    clientA.on("evener/settings/keybindings/get", () => payload(3, []));
    const clientB = new FakeClient("ready");
    clientB.on("evener/settings/keybindings/get", () => payload(9, []));
    const storeA = createKeybindingsStore({ client: clientA, drafts: draftsA.storage });
    const storeB = createKeybindingsStore({ client: clientB, drafts: draftsB.storage });
    for (const store of [storeA, storeB]) {
      store.setSupport("supported");
      store.beginReadyGeneration();
      await store.getState().refreshOverrides();
    }

    storeA.getState().editDraft([{ action: ACTIONS.paletteOpen, chord: "Meta+P" }]);

    expect(draftsA.load()).toMatchObject({ baseRevision: 3, writeUncertain: false });
    expect(draftsB.load()).toBeNull();
    expect(storeA.getState().draft).toEqual({
      version: 1,
      revision: 3,
      rules: [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }],
    });
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

describe("without a registry", () => {
  test("the hub's rules publish verbatim, nothing is validated and nothing needs un-applying", async () => {
    const client = new FakeClient("ready");
    const rules = [{ action: "from.a.newer.client", chord: "Control+Shift+Q" }];
    client.on("evener/settings/keybindings/get", () => payload(2, rules));
    const store = await readyStore(client);
    expect(store.getState()).toMatchObject({ revision: 2, overrides: rules, rawOverrides: rules, warnings: [] });

    store.setSupport("unsupported");
    expect(store.getState()).toMatchObject({ hubSupport: "unsupported", overrides: [], revision: 0, hubError: null });
  });
});

describe("the checkpointed draft editor", () => {
  test("persists the intent before the PATCH leaves, clears it on the ack and applies the canonical payload", async () => {
    const drafts = memoryDraftStorage();
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => payload(3, []));
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];
    let checkpointAtDispatch: KeybindingDraftCheckpoint | null = null;
    client.on("evener/settings/keybindings/patch", () => {
      checkpointAtDispatch = drafts.load();
      return payload(4, rules);
    });
    const store = createKeybindingsStore({ client, drafts: drafts.storage });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    await store.getState().saveDraft(rules);

    expect(checkpointAtDispatch).toMatchObject({ baseRevision: 3, rules, writeUncertain: true });
    expect(drafts.load()).toBeNull();
    expect(store.getState()).toMatchObject({
      revision: 4,
      rawOverrides: rules,
      draft: null,
      saving: false,
      writeUncertain: false,
      draftConflict: false,
    });
    expect(client.calls.at(-1)).toMatchObject({
      method: "evener/settings/keybindings/patch",
      params: { expectedRevision: 3, config: { version: 1, rules } },
    });
  });

  test("a lost reply leaves the write uncertain and blocks edits until an authoritative read", async () => {
    const drafts = memoryDraftStorage();
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => payload(3, []));
    client.on("evener/settings/keybindings/patch", () => {
      throw new Error("token secret");
    });
    const store = createKeybindingsStore({ client, drafts: drafts.storage });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];

    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    expect(store.getState()).toMatchObject({ writeUncertain: true, draftConflict: true, draft: { rules } });
    expect(store.getState().draftError).not.toContain("secret");
    expect(() => store.getState().editDraft([])).toThrow("unavailable");

    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({
      writeUncertain: false,
      draftConflict: false,
      draftError: null,
      draft: { rules },
    });
    expect(drafts.load()).toMatchObject({ writeUncertain: false });
  });

  test("a read carrying loadError after a lost reply supersedes the write's failure copy but settles nothing", async () => {
    const drafts = memoryDraftStorage();
    const client = new FakeClient("ready");
    let response: KeybindingsOverrides = payload(3, []);
    client.on("evener/settings/keybindings/get", () => response);
    client.on("evener/settings/keybindings/patch", () => {
      throw new Error("lost reply");
    });
    const store = createKeybindingsStore({ client, drafts: drafts.storage });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    await expect(store.getState().saveDraft([{ action: ACTIONS.paletteOpen, chord: "Meta+P" }])).rejects.toThrow();
    expect(store.getState().draftError).not.toBeNull();

    response = { ...payload(0, []), loadError: "state file unreadable" };
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      writeUncertain: true,
      draftError: null,
      loadError: "state file unreadable",
    });
    expect(drafts.load()).toMatchObject({ writeUncertain: true });
  });

  /** A store whose ready generation ends while a checkpointed write is in
   * flight, then begins a new one on the same instance (the web adapter's
   * singleton does exactly this across a reconnect). */
  async function saveAcrossGenerationEnd(settle: "resolve" | "reject") {
    const drafts = memoryDraftStorage();
    const client = new FakeClient("ready");
    client.on("evener/settings/keybindings/get", () => payload(3, []));
    let finish: () => void = () => {};
    client.on(
      "evener/settings/keybindings/patch",
      () =>
        new Promise<KeybindingsOverrides>((resolve, reject) => {
          finish = () => (settle === "resolve" ? resolve(payload(4, rules)) : reject(new Error("lost reply")));
        }),
    );
    const store = createKeybindingsStore({ client, drafts: drafts.storage });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];
    const save = store.getState().saveDraft(rules);
    expect(store.getState().saving).toBe(true);
    // The fake defers its handler by a microtask; the reply must be on the wire
    // before the generation ends.
    await vi.waitFor(() =>
      expect(client.calls.some((c) => c.method === "evener/settings/keybindings/patch")).toBe(true),
    );

    store.endReadyGeneration();
    finish();
    await save.then(
      () => undefined,
      () => undefined,
    );
    return { store, drafts, save };
  }

  test("a generation ending mid-write leaves the write uncertain, not saving, and the next read settles it", async () => {
    const { store, drafts } = await saveAcrossGenerationEnd("resolve");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, loaded: false });

    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, loaded: true });
    expect(drafts.load()).toMatchObject({ writeUncertain: false });
    expect(() => store.getState().editDraft([])).not.toThrow();
  });

  test("a write rejecting after its generation ended writes nothing back and the store stays editable after reconnect", async () => {
    const { store, save } = await saveAcrossGenerationEnd("reject");
    await expect(save).rejects.toThrow("lost reply");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftError: null });

    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState().saving).toBe(false);
    expect(() => store.getState().editDraft([])).not.toThrow();
  });

  test("a store built over a stored checkpoint restores the draft synchronously", () => {
    const drafts = memoryDraftStorage();
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];
    drafts.storage.save({ id: "x", baseRevision: 3, rules, writeUncertain: true });
    const store = createKeybindingsStore({ client: new FakeClient("ready"), drafts: drafts.storage });
    expect(store.getState()).toMatchObject({ draft: { revision: 3, rules }, writeUncertain: true });
  });
});

describe("payload rules shared by both apps", () => {
  test("a GET carrying loadError is authoritative even at a lower revision", async () => {
    const client = new FakeClient("ready");
    let response: KeybindingsOverrides = payload(5, [{ action: ACTIONS.paletteOpen, chord: "Control+P" }]);
    client.on("evener/settings/keybindings/get", () => response);
    const store = await readyStore(client, registryWithDefaults());
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

  test("keybindingsSupport and fromWireOverrides are the two decoders every adapter shares", () => {
    expect(keybindingsSupport(undefined)).toBe("unknown");
    expect(keybindingsSupport({ keybindingsSettings: true })).toBe("supported");
    expect(keybindingsSupport({ keybindingsSettings: false })).toBe("unsupported");
    expect(keybindingsSupport({})).toBe("unsupported");
    expect(fromWireOverrides({ version: 1, revision: 2, rules: [{ action: "a", chord: null }] })).toEqual({
      version: 1,
      revision: 2,
      rules: [{ action: "a", chord: null }],
    });
    expect(fromWireOverrides({ version: 2, revision: 2, rules: [] })).toBeUndefined();
    expect(fromWireOverrides({ version: 1, revision: -1, rules: [] })).toBeUndefined();
    expect(fromWireOverrides({ version: 1, revision: 1, rules: [{ action: 1, chord: null }] })).toBeUndefined();
    expect(fromWireOverrides({ version: 1, revision: 1, rules: [], loadError: 4 })).toBeUndefined();
  });
});
