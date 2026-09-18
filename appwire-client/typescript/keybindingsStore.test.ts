import { describe, expect, test, vi } from "vitest";
import { ACTIONS } from "./keybindingActions";
import { serializeChord } from "./keybindingChord";
import type { KeybindingsRegistry } from "./keybindingRegistry";
import {
  createKeybindingsStore,
  fromWireOverrides,
  type KeybindingsStore,
  type KeybindingsStoreDeps,
  type KeybindingsSupport,
  keybindingsSupport,
} from "./keybindingsStore";
import { deferred } from "./testing/deferred";
import { callsTo, FakeClient, gateSettlements } from "./testing/fakeClient";
import { memoryKeybindingDraftStorage } from "./testing/keybindingDraftStorage";
import { registryWithDefaults } from "./testing/keybindingRegistry";
import type { KeybindingsOverrides, KeybindingsRule } from "./types.gen";

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

describe("an undecodable changed broadcast", () => {
  test("schedules the store's own read so the state converges, without dropping the change", async () => {
    const client = clientServing(1);
    const store = await readyStore(client, { registry: registryWithDefaults() });
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    client.on(getMethod, () => payload(2, rules));

    client.emitNotification({ method: changedMethod, params: { version: 1, revision: "two", rules } as never });

    await vi.waitFor(() => expect(store.getState().revision).toBe(2));
    expect(store.getState().rawOverrides).toEqual(rules);
    // The initial load plus exactly one follow-up read.
    expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(2);
    expect(store.getState().hubError).toBeNull();
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

  test("discardDraft removes the checkpoint editDraft saved through a port that compares JSON bytes", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });

    store.getState().editDraft(rules);
    const saved = drafts.stored();
    expect(saved).not.toBeNull();
    store.getState().discardDraft();

    // The port received, byte for byte, what it had stored: the repository
    // normalizes both sides, so the host's own id generator and key order
    // cannot make the two disagree.
    expect(JSON.stringify(drafts.lastRemoveIf())).toBe(JSON.stringify(saved));
    expect(drafts.stored()).toBeNull();
    expect(store.getState().draft).toBeNull();
  });

  test("a store built over a stored checkpoint restores the draft synchronously", () => {
    const drafts = memoryKeybindingDraftStorage();
    drafts.storage.save({ id: "x", baseRevision: 3, rules, writeUncertain: true });
    const store = createKeybindingsStore({ client: new FakeClient("ready"), drafts: drafts.storage });
    expect(store.getState()).toMatchObject({ draft: { revision: 3, rules }, writeUncertain: true });
  });

  test.each<[string, unknown]>([
    ["a whitespace-only action id", [{ action: " ", chord: "Control+P" }]],
    ["a whitespace-only chord", [{ action: ACTIONS.paletteOpen, chord: "\t\n" }]],
  ])("editDraft refuses %s, as the hub would", async (_name, proposed) => {
    const store = await readyStore(clientServing(3), { drafts: memoryKeybindingDraftStorage().storage });
    expect(() => store.getState().editDraft(proposed as KeybindingsRule[])).toThrow("Invalid keybinding draft.");
    expect(store.getState().draft).toBeNull();
  });
});

describe("a retired payload fences every reply still in flight", () => {
  const applied = { action: ACTIONS.paletteOpen, chord: "Control+P" };
  const late = payload(9, [{ action: ACTIONS.paletteOpen, chord: "Control+Shift+P" }]);

  /** Frees the applied override and squats palette.open's default chord (as
   * a registry with defaults actually registers it) with a foreign binding, so
   * restoring the default throws and unapplyAll rolls back (the oracle's
   * wedge). */
  function wedge(registry: KeybindingsRegistry): void {
    registry.getState().unregisterBinding(`${ACTIONS.paletteOpen}#override`);
    const paletteDefault = registryWithDefaults()
      .getState()
      .bindings.find((b) => b.id === ACTIONS.paletteOpen);
    if (paletteDefault === undefined) throw new Error("test setup: no default for palette.open");
    registry
      .getState()
      .registerBinding({ id: "foreign.squatter", actionId: "foreign", chord: serializeChord(paletteDefault.chord) });
  }

  const resetSites: [string, (store: KeybindingsStore, registry: KeybindingsRegistry) => void][] = [
    ["endReadyGeneration", (store) => store.endReadyGeneration()],
    ["setSupport(unsupported)", (store) => store.setSupport("unsupported")],
    ["detachHub, clean", (store) => expect(store.detachHub()).toBe(true)],
    [
      "detachHub, rollback",
      (store, registry) => {
        wedge(registry);
        expect(store.detachHub()).toBe(false);
      },
    ],
  ];

  const writePaths: [string, typeof getMethod | typeof patchMethod, (store: KeybindingsStore) => Promise<unknown>][] = [
    ["patchOverrides", patchMethod, (store) => store.getState().patchOverrides([applied])],
    ["saveDraft", patchMethod, (store) => store.getState().saveDraft([applied])],
    ["refreshFor", getMethod, (store) => store.getState().refreshOverrides()],
  ];

  const cells = resetSites.flatMap(([site, reset]) =>
    writePaths.map(([path, method, start]) => [site, path, reset, method, start] as const),
  );

  test.each(cells)("%s while a %s reply is in flight", async (_site, _path, reset, method, start) => {
    const registry = registryWithDefaults();
    const client = clientServing(3, [applied]);
    const store = await readyStore(client, { registry, drafts: memoryKeybindingDraftStorage().storage });
    const reply = deferred<KeybindingsOverrides>();
    client.on(method, () => reply.promise);
    const settled = start(store).then(
      () => undefined,
      () => undefined,
    );
    await vi.waitFor(() =>
      expect(client.calls.filter((c) => c.method === method)).toHaveLength(method === getMethod ? 2 : 1),
    );

    reset(store, registry);
    reply.resolve(late);
    await settled;

    // The late reply landed nothing: the payload is retired, nothing is in
    // flight, and neither editor will act on it.
    expect(store.getState()).toMatchObject({ loaded: false, revision: 0, saving: false, hubLoading: false });
    expect(store.getState().rawOverrides).not.toContainEqual(late.rules[0]);
    expect(
      registry
        .getState()
        .bindings.some(
          (b) =>
            b.id === `${ACTIONS.paletteOpen}#override` &&
            b.chord.length > 0 &&
            JSON.stringify(b.chord).includes("Shift"),
        ),
    ).toBe(false);
    expect(() => store.getState().editDraft([])).toThrow("unavailable");
    await expect(store.getState().patchOverrides([])).rejects.toThrow("unavailable");
  });
});

describe("saveDraft's post-reply sequence: fence, decode, apply, storage", () => {
  const applied = { action: ACTIONS.paletteOpen, chord: "Control+P" };
  const proposed = [{ action: ACTIONS.paletteOpen, chord: "Control+Shift+P" }];

  interface Scenario {
    name: string;
    /** The rules the draft proposes (default: a rebind of palette.open). */
    rules?: KeybindingsOverrides["rules"];
    /** Runs while the reply is on the wire. */
    arrange?: (store: KeybindingsStore, registry: KeybindingsRegistry) => void;
    /** The hub's reply (default: the proposed rules confirmed at revision 4). */
    reply?: KeybindingsOverrides;
    rejects: boolean;
    after: { writeUncertain: boolean; stored: "intact" | null; hubError: boolean };
    /** Brings the store back to a confirmed state the way its host would. */
    settle: (store: KeybindingsStore) => Promise<void>;
  }

  const refresh = async (store: KeybindingsStore) => {
    await store.getState().refreshOverrides();
  };
  const scenarios: Scenario[] = [
    {
      name: "fenced out by the generation ending before the reply lands",
      arrange: (store) => store.endReadyGeneration(),
      rejects: false,
      after: { writeUncertain: true, stored: "intact", hubError: false },
      settle: async (store) => {
        store.beginReadyGeneration();
        await refresh(store);
      },
    },
    {
      name: "fenced out by support dropping before the reply lands",
      arrange: (store) => store.setSupport("unsupported"),
      rejects: false,
      after: { writeUncertain: true, stored: "intact", hubError: false },
      settle: async (store) => {
        store.setSupport("supported");
        await vi.waitFor(() => expect(store.getState().loaded).toBe(true));
      },
    },
    {
      // The oracle's wedge: the draft drops the override, and a foreign binding
      // squats palette.open's default chord meanwhile, so restoring the default
      // throws inside the reconcile and the registry rolls back.
      name: "the reconciler throws on the confirmed rules (a foreign binding took the default chord meanwhile)",
      rules: [],
      arrange: (_store, registry) => {
        const paletteDefault = registryWithDefaults()
          .getState()
          .bindings.find((b) => b.id === ACTIONS.paletteOpen);
        if (paletteDefault === undefined) throw new Error("test setup: no default for palette.open");
        registry.getState().registerBinding({
          id: "foreign.squatter",
          actionId: "foreign",
          chord: serializeChord(paletteDefault.chord),
        });
      },
      rejects: true,
      after: { writeUncertain: false, stored: null, hubError: true },
      settle: refresh,
    },
    {
      name: "a malformed reply",
      reply: { version: 2 } as unknown as KeybindingsOverrides,
      rejects: true,
      after: { writeUncertain: true, stored: "intact", hubError: true },
      settle: refresh,
    },
    {
      name: "success",
      rejects: false,
      after: { writeUncertain: false, stored: null, hubError: false },
      settle: refresh,
    },
  ];

  test.each(scenarios)(
    "$name",
    async ({ rules = proposed, arrange, reply = payload(4, rules), rejects, after, settle }) => {
      const registry = registryWithDefaults();
      const drafts = memoryKeybindingDraftStorage();
      const client = clientServing(3, [applied]);
      const store = await readyStore(client, { registry, drafts: drafts.storage });
      const wire = deferred<KeybindingsOverrides>();
      client.on(patchMethod, () => wire.promise);

      const save = store.getState().saveDraft(rules);
      await vi.waitFor(() => expect(client.calls.filter((c) => c.method === patchMethod)).toHaveLength(1));
      const checkpoint = drafts.stored();
      expect(checkpoint).toMatchObject({ baseRevision: 3, rules, writeUncertain: true });
      arrange?.(store, registry);
      wire.resolve(reply);
      if (rejects) await expect(save).rejects.toThrow();
      else await expect(save).resolves.toBeDefined();

      // Nothing is in flight; the outcome is recorded once, in state AND on the
      // port: an unknown outcome keeps its checkpoint, a known one releases it.
      expect(store.getState().saving).toBe(false);
      expect(store.getState().writeUncertain).toBe(after.writeUncertain);
      expect(drafts.stored()).toEqual(after.stored === "intact" ? checkpoint : null);
      expect(store.getState().hubError !== null).toBe(after.hubError);

      await settle(store);
      expect(store.getState()).toMatchObject({
        saving: false,
        writeUncertain: false,
        draftConflict: false,
        loaded: true,
      });
      expect(() => store.getState().editDraft([])).not.toThrow();
    },
  );
});

describe("the two write paths serialize through one queue", () => {
  const applied = { action: ACTIONS.paletteOpen, chord: "Control+P" };
  const proposed = [{ action: ACTIONS.paletteOpen, chord: "Control+Shift+P" }];

  test("a direct PATCH in flight is not superseded by a draft save queued behind it", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const patch = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(callsTo(client, patchMethod)).toBe(1));

    // The draft save is composed against revision 3 while the direct write is
    // still on the wire: it must wait for that write, not claim the write
    // token out from under it and fence its reply.
    const save = store.getState().saveDraft(proposed);
    expect(store.getState().saving).toBe(true);
    expect(callsTo(client, patchMethod)).toBe(1);

    settlements[0]!.resolve(payload(4, [applied]));
    await expect(patch).resolves.toMatchObject({ revision: 4 });

    // The queued save revalidates: the confirmed revision moved to 4, so the
    // revision-3 draft is stale and refuses instead of sending a doomed PATCH
    // whose reply the direct write's settlement already fenced.
    await expect(save).rejects.toThrow();
    expect(callsTo(client, patchMethod)).toBe(1);
    expect(store.getState()).toMatchObject({
      revision: 4,
      rawOverrides: [applied],
      saving: false,
      writeUncertain: false,
      draftConflict: true,
      draft: { revision: 3, rules: proposed },
    });
    expect(drafts.stored()).toMatchObject({ baseRevision: 3, rules: proposed, writeUncertain: false });
  });

  test("a draft save in flight is not superseded by a direct PATCH queued behind it", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const save = store.getState().saveDraft(proposed);
    await vi.waitFor(() => expect(callsTo(client, patchMethod)).toBe(1));

    // The direct write waits for the save's full settlement; it must not
    // supersede the checkpointed save's reply.
    const patch = store.getState().patchOverrides([applied]);
    expect(callsTo(client, patchMethod)).toBe(1);

    settlements[0]!.resolve(payload(4, proposed));
    await expect(save).resolves.toMatchObject({ revision: 4 });

    // The direct write then composes against the save's confirmed state.
    await vi.waitFor(() => expect(callsTo(client, patchMethod)).toBe(2));
    settlements[1]!.resolve(payload(5, [applied]));
    await expect(patch).resolves.toMatchObject({ revision: 5 });
    expect(store.getState()).toMatchObject({ revision: 5, rawOverrides: [applied], saving: false });
    expect(client.calls.at(-1)).toMatchObject({
      method: patchMethod,
      params: { expectedRevision: 4, config: { version: 1, rules: [applied] } },
    });
    expect(drafts.stored()).toBeNull();
  });

  test("a direct PATCH queued behind a save that loses its reply does not dispatch", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const save = store.getState().saveDraft(proposed);
    await vi.waitFor(() => expect(callsTo(client, patchMethod)).toBe(1));
    const patch = store.getState().patchOverrides([applied]);
    expect(callsTo(client, patchMethod)).toBe(1);

    settlements[0]!.reject(new Error("lost reply"));
    await expect(save).rejects.toThrow();
    // The save left its outcome unknown; the queued direct write must not
    // dispatch and bypass the authoritative read that uncertainty requires.
    await expect(patch).rejects.toThrow();
    expect(callsTo(client, patchMethod)).toBe(1);
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true });
  });

  test("a compose thunk that starts another write does not dispatch it concurrently", async () => {
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    let nested: Promise<KeybindingsOverrides> | null = null;
    const outer = store.getState().patchOverrides(() => {
      nested = store.getState().patchOverrides([applied]);
      return [applied];
    });
    // The nested write was created from the outer write's synchronous prefix:
    // it must queue behind the outer write, not go on the wire beside it.
    expect(callsTo(client, patchMethod)).toBe(1);

    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    settlements[0]!.resolve(payload(4, [applied]));
    await expect(outer).resolves.toMatchObject({ revision: 4 });
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve(payload(5, [applied]));
    await expect(nested!).resolves.toBeDefined();
  });

  test("reset during an in-flight write does not corrupt the write queue", async () => {
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    const abandoned = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(callsTo(client, patchMethod)).toBe(1));

    store.reset();
    settlements[0]!.resolve(payload(4, [applied]));
    await abandoned.then(
      () => undefined,
      () => undefined,
    );

    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    // The abandoned write's settlement must not have driven the fresh counter
    // negative: an idle write still dispatches on the calling tick.
    const next = store.getState().patchOverrides([applied]);
    expect(callsTo(client, patchMethod)).toBe(2);
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve(payload(5, [applied]));
    await expect(next).resolves.toMatchObject({ revision: 5 });
  });

  test("a direct PATCH queued before a save still dispatches in order", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const a = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const p = store.getState().patchOverrides([applied]);
    // The save publishes `saving` synchronously but owns the LAST queue slot:
    // the direct write queued before it must still dispatch, not be rejected
    // by the save's saving flag.
    const save = store.getState().saveDraft(proposed);
    expect(store.getState().saving).toBe(true);

    settlements[0]!.resolve(payload(4, [applied]));
    await expect(a).resolves.toMatchObject({ revision: 4 });
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve(payload(5, [applied]));
    await expect(p).resolves.toMatchObject({ revision: 5 });

    // The save now runs against a revision it was not composed for: a
    // draftConflict, so it never dispatches a doomed PATCH.
    await expect(save).rejects.toThrow();
    expect(callsTo(client, patchMethod)).toBe(2);
    expect(store.getState()).toMatchObject({
      revision: 5,
      rawOverrides: [applied],
      saving: false,
      writeUncertain: false,
      draftConflict: true,
    });
  });

  test("reset drains a write queued behind the abandoned in-flight write", async () => {
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    const a = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const b = store.getState().patchOverrides([applied]);

    store.reset();
    settlements[0]!.resolve(payload(4, [applied]));
    await a.then(
      () => undefined,
      () => undefined,
    );

    // The abandoned write's settlement must still release its tail so the
    // write queued behind it drains through its dead-generation fence rather
    // than hanging forever.
    await expect(b).rejects.toThrow();
  });

  test("reset drains a write queued behind a request that never settles", async () => {
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    // The first request is deliberately never settled.
    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const second = store.getState().patchOverrides([applied]);

    store.reset();
    // reset() must release the abandoned head tail itself: the queued write
    // drains through its dead-generation fence, without waiting on a request
    // that will never answer.
    await expect(second).rejects.toThrow();
    // Silence the deliberately-unsettled first write (it stays pending).
    void first;
  });

  test("a save queued behind a write does not dispatch while a refresh is in flight", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const save = store.getState().saveDraft(proposed);

    // A refresh starts while the save waits in the queue and stays pending.
    const heldGet = deferred<KeybindingsOverrides>();
    client.on(getMethod, () => heldGet.promise);
    const read = store.getState().refreshOverrides();
    expect(store.getState().hubLoading).toBe(true);

    // The preceding write settles WITHOUT changing the revision.
    settlements[0]!.resolve(payload(3, [applied]));
    await expect(first).resolves.toBeDefined();

    // The queued save must refuse rather than race the pending authoritative
    // GET with a stale expectedRevision: no second PATCH fires.
    await expect(save).rejects.toThrow();
    expect(callsTo(client, patchMethod)).toBe(1);

    heldGet.resolve(payload(3, [applied]));
    await read;
  });

  test("ending a generation drains a write queued behind a never-settling request", async () => {
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    // The first request is deliberately never settled.
    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const second = store.getState().patchOverrides([applied]);

    store.endReadyGeneration();
    // The queued write drains through its dead-generation fence instead of
    // waiting on a request that will never answer.
    await expect(second).rejects.toThrow();
    void first;
  });

  test("a checkpoint settle failure during revalidation keeps the storage error visible", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const save = store.getState().saveDraft(proposed);
    // The save's revalidation settles the checkpoint when it finds the moved
    // revision; make that settle write fail.
    drafts.failSave(true);
    settlements[0]!.resolve(payload(4, [applied]));
    await expect(first).resolves.toBeDefined();
    await expect(save).rejects.toThrow();
    expect(store.getState()).toMatchObject({ saving: false, draftConflict: true, storageUnavailable: true });
    expect(store.getState().draftError).not.toBeNull();
  });

  test("a save queued across detachHub does not dispatch to the replacement hub", async () => {
    const drafts = memoryKeybindingDraftStorage();
    const client = clientServing(3, [applied]);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client, { drafts: drafts.storage });

    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const save = store.getState().saveDraft(proposed);
    // The save is expected to be refused at detach; keep that rejection handled
    // across the awaits below.
    const saveSettled = save.then(
      () => undefined,
      () => undefined,
    );

    // The host replaces the hub: detachHub retires the payload but does NOT end
    // the generation, so `callGeneration` alone would still look current.
    expect(store.detachHub()).toBe(true);
    // The replacement hub refreshes with the SAME revision.
    client.on(getMethod, () => payload(3, [applied]));
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ loaded: true, revision: 3 });

    // The preceding write settles; the queued save must not send the old hub's
    // draft to the replacement hub.
    settlements[0]!.resolve(payload(3, [applied]));
    await first.then(
      () => undefined,
      () => undefined,
    );
    // Let the queued save reach its dispatch decision.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(callsTo(client, patchMethod)).toBe(1);
    await expect(save).rejects.toThrow();
    await saveSettled;
  });

  test("a direct PATCH queued across detachHub does not dispatch to the replacement hub", async () => {
    const client = clientServing(3, [applied]);
    const settlements = gateSettlements(client, patchMethod);
    const store = await readyStore(client);

    const first = store.getState().patchOverrides([applied]);
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    const queued = store.getState().patchOverrides([applied]);
    const queuedSettled = queued.then(
      () => undefined,
      () => undefined,
    );

    expect(store.detachHub()).toBe(true);
    client.on(getMethod, () => payload(3, [applied]));
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ loaded: true, revision: 3 });

    settlements[0]!.resolve(payload(3, [applied]));
    await first.then(
      () => undefined,
      () => undefined,
    );
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(callsTo(client, patchMethod)).toBe(1);
    await expect(queued).rejects.toThrow();
    await queuedSettled;
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
    ["an empty action id", { version: 1, revision: 1, rules: [{ action: "", chord: "Control+P" }] }, undefined],
    ["an empty chord", { version: 1, revision: 1, rules: [{ action: "palette.open", chord: "" }] }, undefined],
    ["an empty action and chord", { version: 1, revision: 1, rules: [{ action: "", chord: "" }] }, undefined],
    // The hub trims before it checks (appwire/keybindings.go ValidateKeybindingsConfig),
    // so a whitespace-only id names no action and no chord there either: accepting
    // one here would put a rule in rawOverrides that every later whole-payload
    // PATCH is rejected for.
    [
      "a whitespace-only action id",
      { version: 1, revision: 1, rules: [{ action: " ", chord: "Control+P" }] },
      undefined,
    ],
    [
      "a whitespace-only chord",
      { version: 1, revision: 1, rules: [{ action: "palette.open", chord: "\t\n" }] },
      undefined,
    ],
    // The hub's whitespace set is Go's unicode.IsSpace, not JS trim's. NEL is
    // whitespace there and is trimmed away, so a rule of only NEL names no
    // action and every later whole-payload PATCH is rejected for it.
    [
      "an action id of only U+0085",
      { version: 1, revision: 1, rules: [{ action: "\u0085", chord: "Control+P" }] },
      undefined,
    ],
    [
      "a chord of only U+0085",
      { version: 1, revision: 1, rules: [{ action: "palette.open", chord: "\u0085" }] },
      undefined,
    ],
    ["a non-string loadError", { version: 1, revision: 1, rules: [], loadError: 4 }, undefined],
    ["a non-object", "nope", undefined],
  ])("fromWireOverrides: %s", (_name, value, expected) => {
    expect(fromWireOverrides(value)).toEqual(expected);
  });

  test("U+FEFF is an ordinary character to the hub, so the boundary keeps it", () => {
    // JS trim() strips the BOM; Go's unicode.IsSpace does not, so the hub
    // stores and serves such a rule and this client must not discard it.
    const payload = { version: 1, revision: 1, rules: [{ action: "\ufeff", chord: "\ufeff" }] };
    expect(fromWireOverrides(payload)).toEqual(payload);
  });
});
