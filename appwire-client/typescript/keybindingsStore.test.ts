import { describe, expect, test, vi } from "vitest";
import { ConnectionClosedError, friendlyErrorMessage, RequestTimeoutError, WireError } from "./errors";
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
import { FakeClient } from "./testing/fakeClient";
import { registryWithDefaults } from "./testing/keybindingRegistry";
import { nextMacrotask } from "./testing/macrotask";
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
});

describe("an undecodable changed broadcast", () => {
  test("schedules the store's own read so the state converges, without dropping the change", async () => {
    const client = clientServing(1);
    const store = await readyStore(client, { registry: registryWithDefaults() });
    const rules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    client.on(getMethod, () => payload(2, rules));

    client.emitNotification({ method: changedMethod, params: { version: 1, revision: "two", rules } as never });

    await nextMacrotask();
    expect(store.getState().revision).toBe(2);
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
    expect(initial).toMatchObject({ hubSupport: "unknown", loaded: false, revision: 0, hubError: null });
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
    await nextMacrotask();
    expect(store.getState().revision).toBe(4);
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
    ["refreshFor", getMethod, (store) => store.getState().refreshOverrides()],
  ];

  const cells = resetSites.flatMap(([site, reset]) =>
    writePaths.map(([path, method, start]) => [site, path, reset, method, start] as const),
  );

  test.each(cells)("%s while a %s reply is in flight", async (_site, _path, reset, method, start) => {
    const registry = registryWithDefaults();
    const client = clientServing(3, [applied]);
    const store = await readyStore(client, { registry });
    const reply = deferred<KeybindingsOverrides>();
    client.on(method, () => reply.promise);
    const settled = start(store).then(
      () => undefined,
      () => undefined,
    );
    await nextMacrotask();
    expect(client.calls.filter((c) => c.method === method)).toHaveLength(method === getMethod ? 2 : 1);

    reset(store, registry);
    reply.resolve(late);
    await settled;

    // The late reply landed nothing: the payload is retired and nothing is
    // in flight.
    expect(store.getState()).toMatchObject({ loaded: false, revision: 0, hubLoading: false });
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
    await expect(store.getState().patchOverrides([])).rejects.toThrow("unavailable");
  });

  test("a write rejected after its generation ended writes nothing back and the store is editable after reconnect", async () => {
    const client = clientServing(3);
    const store = await readyStore(client);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);

    const write = store.getState().patchOverrides([{ action: ACTIONS.paletteOpen, chord: "Meta+P" }]);
    await nextMacrotask();
    store.endReadyGeneration();
    reply.reject(new Error("disconnected"));
    await expect(write).rejects.toThrow("disconnected");

    // The rejection belonged to a dead generation: no live hubError, and the
    // freshly loaded hub is editable again.
    expect(store.getState()).toMatchObject({ loaded: false, hubError: null, revision: 0 });
    client.on(patchMethod, () => payload(4, []));
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    await expect(store.getState().patchOverrides([])).resolves.toBeDefined();
    expect(client.calls.filter((call) => call.method === patchMethod)).toHaveLength(2);
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

describe("read failure text", () => {
  // The store keeps the messages it writes itself (a malformed reply); every
  // other failure takes its friendly form, so a hub WireError keeps its own
  // explanation and the client's own failures never show their wiring text.
  test.each([
    ["a hub-reported WireError", new WireError("keybindings.toml is being rewritten", -32000)],
    ["the client's connection-state failure", new ConnectionClosedError("AppwireClient: closed")],
    ["the client's request timeout", new RequestTimeoutError('AppwireClient: "a method" timed out after 1ms')],
    ["the client's lost socket", new Error("AppwireClient: socket closed (code 1006)")],
    ["an unexpected client-side exception", new TypeError("cannot read properties of undefined")],
  ])("a failed read from %s records its friendly form", async (_label, err) => {
    const client = new FakeClient("ready");
    client.on(getMethod, () => {
      throw err;
    });
    const store = await readyStore(client);
    expect(store.getState().hubError).toBe(friendlyErrorMessage(err));
  });

  test("a malformed reply keeps the store's own diagnosis rather than the generic sentence", async () => {
    const client = new FakeClient("ready");
    client.on(getMethod, () => ({ version: 1, revision: "two", rules: [] }) as never);
    const store = await readyStore(client);
    expect(store.getState().hubError).not.toBeNull();
    expect(store.getState().hubError).not.toBe(friendlyErrorMessage(new Error("any client-side failure")));
  });
});

describe("the store's one write path", () => {
  test("exposes patchOverrides as its only mutation action and carries no draft state", () => {
    const store = createKeybindingsStore({ client: new FakeClient("ready") });
    const state = store.getState();
    expect(state).toMatchObject({
      hubSupport: "unknown",
      hubLoading: false,
      hubError: null,
      revision: 0,
      appliedSerial: 0,
      overrides: [],
      rawOverrides: [],
      warnings: [],
      loaded: false,
      loadError: null,
      conflict: null,
    });
    expect(
      Object.keys(state)
        .filter((key) => typeof (state as unknown as Record<string, unknown>)[key] === "function")
        .sort(),
    ).toEqual(["patchOverrides", "refreshOverrides"]);
    for (const removed of [
      "draft",
      "saving",
      "writeUncertain",
      "storageUnavailable",
      "draftUnreadable",
      "draftConflict",
      "draftError",
      "editDraft",
      "saveDraft",
      "discardDraft",
      "rebaseDraft",
    ]) {
      expect(state).not.toHaveProperty(removed);
    }
  });

  test("a host edit dispatches the PATCH and lands in the store with no second save step", async () => {
    const client = clientServing(3);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const store = await readyStore(client, { registry: registryWithDefaults() });
    const applied = [{ action: ACTIONS.paletteOpen, chord: "Meta+P" }];

    // One host call: the edit goes to the wire, and there is no draft held
    // while the write is out - nothing for the host to confirm or save later.
    const edit = store.getState().patchOverrides(applied);
    await nextMacrotask();
    expect(client.calls.filter((c) => c.method === patchMethod)).toHaveLength(1);
    expect(store.getState()).not.toHaveProperty("draft");

    reply.resolve(payload(4, applied));
    await edit;
    expect(store.getState()).toMatchObject({
      revision: 4,
      rawOverrides: applied,
      overrides: applied,
      loaded: true,
      hubError: null,
      conflict: null,
    });
    expect(client.calls.at(-1)).toMatchObject({
      method: patchMethod,
      params: { expectedRevision: 3, config: { version: 1, rules: applied } },
    });
  });

  test("refuses a write while the hub carries a load error, preserving its diagnostic", async () => {
    const client = new FakeClient("ready");
    client.on(getMethod, () => ({ version: 1, revision: 0, rules: [], loadError: "decode failed" }));
    const store = await readyStore(client);

    await expect(store.getState().patchOverrides([])).rejects.toThrow("unavailable");

    expect(store.getState()).toMatchObject({ hubError: "decode failed", loadError: "decode failed" });
    expect(client.calls.filter((call) => call.method === patchMethod)).toHaveLength(0);
  });
});
