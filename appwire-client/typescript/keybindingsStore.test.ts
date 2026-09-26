import { describe, expect, test, vi } from "vitest";
import { UnreadableDraftError } from "./draftCheckpointPort";
import { WireError } from "./errors";
import { ACTIONS } from "./keybindingActions";
import { serializeChord } from "./keybindingChord";
import type { KeybindingsRegistry } from "./keybindingRegistry";
import {
  createKeybindingsStore,
  decodeKeybindingDraftFields,
  discardStoredKeybindingDraft,
  fromWireOverrides,
  isReadableKeybindingDraft,
  type KeybindingDraftCheckpoint,
  type KeybindingsStore,
  type KeybindingsStoreDeps,
  type KeybindingsSupport,
  keybindingsSupport,
} from "./keybindingsStore";
import { deferred } from "./testing/deferred";
import { memoryDraftStorage } from "./testing/draftStorage";
import { FakeClient, gateSettlements, type Settlement } from "./testing/fakeClient";
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

/** The Nth call's settlement from gateSettlements, or a loud failure - indexed
 * access into that array is possibly undefined to the type checker even
 * once a `toHaveLength` assertion has proven the call landed. */
function replyAt(settlements: Settlement[], index: number): Settlement {
  const settlement = settlements[index];
  if (!settlement) throw new Error(`no gated request at index ${index}`);
  return settlement;
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
    const draftsA = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const draftsB = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const storeA = await readyStore(clientServing(3), { drafts: draftsA.storage });
    const storeB = await readyStore(clientServing(9), { drafts: draftsB.storage });

    storeA.getState().editDraft(rules);

    expect(draftsA.stored()).toMatchObject({ baseRevision: 3, writeUncertain: false });
    expect(draftsB.stored()).toBeNull();
    expect(storeA.getState().draft).toEqual({ version: 1, revision: 3, rules, generation: 1 });
    expect(storeB.getState().draft).toBeNull();
  });

  // editDraft/saveDraft/rebaseDraft all persist through the SAME repository
  // save() that discardClassified/replaceClassified already CAS through - a
  // second store sharing the port, editing after this store's own classified
  // record has moved on, must not silently overwrite it.
  test("editDraft refuses to overwrite a checkpoint replaced by another store sharing the same port", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const storeA = await readyStore(clientServing(3), { drafts: drafts.storage });
    storeA.getState().editDraft(rules);

    // storeB classifies the record storeA just wrote (its own restoreDraft,
    // via readyStore, runs against the SAME port and sees it).
    const storeB = await readyStore(clientServing(3), { drafts: drafts.storage });
    expect(storeB.getState().draft).toEqual({ version: 1, revision: 3, rules, generation: 1 });

    // storeA edits again, replacing what storeB classified.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    storeA.getState().editDraft(otherRules);

    // storeB's own edit, composed against its now-stale classification, must
    // refuse and adopt storeA's checkpoint - a conflict, not a storage
    // failure that never happened.
    const attempted = [{ action: ACTIONS.railToggle, chord: "Control+R" }];
    expect(() => storeB.getState().editDraft(attempted)).toThrow("Shortcuts changed again. Review the current values.");
    expect(storeB.getState()).toMatchObject({
      storageUnavailable: false,
      draftError: null,
      draft: { version: 1, revision: 3, rules: otherRules },
    });
    expect(drafts.stored()).toMatchObject({ baseRevision: 3, rules: otherRules });
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
  // The decoder is the store-free twin of the live store's restoreDraft
  // projection: both run the same stored value through the same checkpoint
  // decode and must publish the same draft fields, so a host that classifies
  // a record itself (the native provider's store-free discard path) decodes
  // exactly what a live store would have restored. Every fixture is the raw
  // value a port can hand back, run through BOTH paths: the decoder
  // directly, and a store created over a port holding that same value,
  // whose creation-time restore is the live projection. The draft fields
  // must agree; the live store additionally owns the two signals the
  // decoder's contract leaves to others - the unreadable-record
  // classification and the port's own availability.
  test.each<[string, unknown, boolean]>([
    ["a valid checkpoint", { id: "d1", baseRevision: 3, rules, writeUncertain: true }, false],
    ["no record at all", null, false],
    ["an object that is not a checkpoint", { invalid: true }, true],
    ["bytes no JSON parser accepts", "{not json", true],
    // What a byte-aware port hands back for a stored JSON null: a present
    // object whose only key is a symbol a JSON decode can never produce -
    // present and unreadable, never the absent record above. The memory
    // port clones it like any other present object, which is all the
    // package-level conformance needs: present-but-unreadable.
    ["a stored JSON null", { [Symbol("evener.nativePreferenceDrafts.storedNull")]: true }, true],
  ])("decodeKeybindingDraftFields matches the live restore projection for %s", (_name, value, unreadable) => {
    const decoded = decodeKeybindingDraftFields(value);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>(value);
    const store = createKeybindingsStore({ client: new FakeClient("ready"), drafts: drafts.storage });
    const state = store.getState();

    expect({
      draft:
        state.draft === null
          ? null
          : { version: state.draft.version, revision: state.draft.revision, rules: state.draft.rules },
      writeUncertain: state.writeUncertain,
    }).toEqual(decoded);
    expect(state.draftUnreadable).toBe(unreadable);
    expect(state.storageUnavailable).toBe(unreadable);
  });

  test("restored drafts start unconfirmed and the first authoritative payload stamps the live generation", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.storage.save({ id: "d1", baseRevision: 3, rules, writeUncertain: true });
    const store = createKeybindingsStore({ client: clientServing(3, rules), drafts: drafts.storage });

    expect(store.getState()).toMatchObject({
      draft: { version: 1, revision: 3, rules, generation: null },
      writeUncertain: true,
    });

    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      draft: { version: 1, revision: 3, rules, generation: 1 },
      writeUncertain: false,
    });
  });

  test("editing and saving a draft preserve its generation stamp", async () => {
    const client = clientServing(3);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const store = await readyStore(client);

    store.getState().editDraft(rules);
    expect(store.getState().draft).toMatchObject({ revision: 3, rules, generation: 1 });

    const save = store.getState().saveDraft();
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));
    expect(store.getState().draft).toMatchObject({ revision: 3, rules, generation: 1 });

    reply.resolve(payload(4, rules));
    await save;
    expect(store.getState().draft).toBeNull();
  });

  test("rebasing a draft earns the current ready generation", async () => {
    const store = await readyStore(clientServing(3));
    store.getState().editDraft(rules);
    expect(store.getState().draft?.generation).toBe(1);

    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState().draft?.generation).toBe(1);

    store.getState().rebaseDraft(3);
    expect(store.getState().draft?.generation).toBe(3);
  });

  test("a draft is stale across a generation change even when the new hub reports the same revision", async () => {
    const store = await readyStore(clientServing(3));
    store.getState().editDraft(rules);
    expect(store.getState().draft).toMatchObject({ revision: 3, generation: 1 });
    expect(store.getState().draftConflict).toBe(false);

    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    expect(store.getState().revision).toBe(3);
    expect(store.getState().draftConflict).toBe(true);
  });

  test("editing a conflicted draft cannot launder its generation, and only rebase clears the conflict", async () => {
    const store = await readyStore(clientServing(3));
    store.getState().editDraft(rules);

    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState().draftConflict).toBe(true);
    const conflictedGeneration = store.getState().draft?.generation;

    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Meta+Shift+P" }];
    store.getState().editDraft(otherRules);
    expect(store.getState().draft).toEqual({
      version: 1,
      revision: 3,
      rules: otherRules,
      generation: conflictedGeneration,
    });
    expect(store.getState().draftConflict).toBe(true);

    store.getState().rebaseDraft(3);
    expect(store.getState().draft?.generation).not.toBe(conflictedGeneration);
    expect(store.getState().draftConflict).toBe(false);
  });

  test("persists the intent before the PATCH leaves, clears it on the ack and applies the canonical payload", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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

  test("a successful save whose checkpoint was replaced while the PATCH was in flight adopts the replacement", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const store = await readyStore(client, { drafts: drafts.storage });

    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // Another window or app version replaces the SAME on-disk record with
    // its own draft while this write is still out.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    const replacement: KeybindingDraftCheckpoint = {
      id: "other",
      baseRevision: 3,
      rules: otherRules,
      writeUncertain: false,
    };
    drafts.storage.save(replacement);

    reply.resolve(payload(4, rules));
    await save;

    // The write succeeded, but the checkpoint it wrote is gone - replaced,
    // not just removed. The replacement survives on disk, and the store
    // adopts it rather than reporting no draft.
    expect(drafts.stored()).toEqual(replacement);
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules: otherRules, generation: null });
  });

  // Settling an uncertain checkpoint replaces it atomically: a concurrent
  // writer's newer draft (landed while this write's outcome was unknown)
  // must never be silently overwritten by the stale one this store is
  // settling.
  test("settling an uncertain write after its checkpoint was replaced adopts the replacement instead of overwriting it", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });
    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);

    // Another writer replaces the SAME on-disk record while the outcome is
    // still unknown.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    const replacement: KeybindingDraftCheckpoint = {
      id: "other",
      baseRevision: 3,
      rules: otherRules,
      writeUncertain: false,
    };
    drafts.storage.save(replacement);

    // An authoritative read settles the write - but the checkpoint it would
    // settle onto is gone, replaced. It must adopt the replacement, never
    // overwrite it with the stale (now-settled) checkpoint.
    await store.getState().refreshOverrides();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toEqual(replacement);
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules: otherRules, generation: null });
  });

  // settledWrite's checkpoint reclassification is deferred inside the thunk
  // applyHubOverrides resolves only after a successful reconcile: a refresh
  // whose reconcile throws (a wedged registry) never reaches that point, so
  // the checkpoint on disk stays exactly as the write left it instead of
  // being reclassified for a settle that then fails to publish.
  test("a refresh's reconciler throw does not reclassify the checkpoint ahead of publishing the settle", async () => {
    const registry = registryWithDefaults();
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3, rules);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { registry, drafts: drafts.storage });

    await expect(store.getState().saveDraft([])).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);
    const uncertainCheckpoint = drafts.stored();
    expect(uncertainCheckpoint).toMatchObject({ baseRevision: 3, rules: [], writeUncertain: true });

    // A foreign binding squats palette.open's default chord: restoring the
    // default - what reconciling the confirmed empty rules below requires,
    // since the override this generation applied is going away - throws.
    const paletteDefault = registryWithDefaults()
      .getState()
      .bindings.find((b) => b.id === ACTIONS.paletteOpen);
    if (paletteDefault === undefined) throw new Error("test setup: no default for palette.open");
    registry
      .getState()
      .registerBinding({ id: "foreign.squatter", actionId: "foreign", chord: serializeChord(paletteDefault.chord) });

    client.on(getMethod, () => payload(5, []));
    await store.getState().refreshOverrides();
    expect(store.getState().hubError).not.toBeNull();

    // The throw happened before any settle publishes: writeUncertain is
    // unchanged, and the checkpoint on disk is untouched - the repository was
    // never reclassified for a settle that never actually landed.
    expect(store.getState().writeUncertain).toBe(true);
    expect(drafts.stored()).toEqual(uncertainCheckpoint);

    // Once the registry is no longer wedged, the very same settle succeeds
    // against the SAME still-intact checkpoint.
    registry.getState().unregisterBinding("foreign.squatter");
    await store.getState().refreshOverrides();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toMatchObject({ baseRevision: 3, rules: [], writeUncertain: false });
  });

  test("a refresh already reading while a draft write is in flight never settles that write's uncertainty", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3, rules);
    const store = await readyStore(client, { drafts: drafts.storage });
    const patchReplies = gateSettlements(client, patchMethod);
    const getReplies = gateSettlements(client, getMethod);

    // The draft write starts and claims the write token first.
    const save = store.getState().saveDraft([]);
    await vi.waitFor(() => expect(patchReplies).toHaveLength(1));

    // A refresh's own read starts WHILE the write is still in flight - its
    // snapshot began before the write's outcome was known.
    const refresh = store.getState().refreshOverrides();
    await vi.waitFor(() => expect(getReplies).toHaveLength(1));

    // The write's own request comes back with no usable reply: the outcome
    // is unknown (writeUncertain), and `saving` clears - but the fence's
    // write token never moves (nothing superseded it).
    replyAt(patchReplies, 0).reject(new Error("disconnected"));
    await expect(save).rejects.toThrow("disconnected");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true });

    // The STALE refresh, which started before that failure landed, now
    // replies. Its snapshot says nothing about the write it raced - it must
    // not settle the uncertainty the write just left, even though `saving`
    // is false and the write token is unchanged by the time it checks.
    replyAt(getReplies, 0).resolve(payload(3, rules));
    await refresh;
    expect(store.getState().writeUncertain).toBe(true);
    expect(drafts.stored()).toMatchObject({ writeUncertain: true });
  });

  test("a refresh's stale reply does not reclassify the checkpoint the settle would have adopted", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(5, rules);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    await expect(store.getState().saveDraft([])).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);
    const uncertainCheckpoint = drafts.stored();
    expect(uncertainCheckpoint).toMatchObject({ baseRevision: 5, rules: [], writeUncertain: true });

    // A later GET reports a revision LOWER than the one already confirmed (a
    // hub restart serving a legitimately lower number, or a race):
    // applyHubOverrides' stale guard ignores the payload entirely, so the
    // settle thunk it would have run must not fire either - the checkpoint
    // stays exactly as the write left it, and writeUncertain stays true.
    // hubLoading clears anyway: refreshFor's own unconditional reset, not the
    // thunk applyHubOverrides never ran, is what ends the refresh.
    client.on(getMethod, () => payload(2, []));
    await store.getState().refreshOverrides();

    expect(store.getState().writeUncertain).toBe(true);
    expect(store.getState().hubLoading).toBe(false);
    expect(drafts.stored()).toEqual(uncertainCheckpoint);
  });

  // saveDraft uses rejectionPayload, the same as the direct write
  // (patchOverrides), to tell a structured conflict/post-rename failure
  // (a KNOWN outcome) from a transport failure (an unknown one): a known
  // refusal settles writeUncertain rather than leaving it true, and a known
  // post-rename success is reported as a successful save.
  test("a revision conflict during saveDraft is a KNOWN outcome: the canonical lands and the proposal stays for review", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        current: payload(6, [{ action: ACTIONS.railToggle, chord: "Control+R" }]),
      });
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    await expect(store.getState().saveDraft(rules)).rejects.toThrow("revision conflict");

    expect(store.getState()).toMatchObject({
      revision: 6,
      writeUncertain: false,
      draftConflict: true,
      draft: { rules },
    });
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
  });

  test("settling an uncertain write after its checkpoint was replaced evaluates conflict against the authoritative refresh revision, not the write's start revision", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });
    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);

    // Another writer replaces the SAME on-disk record with a draft composed
    // against revision 3 while the outcome is still unknown.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    const replacement: KeybindingDraftCheckpoint = {
      id: "other",
      baseRevision: 3,
      rules: otherRules,
      writeUncertain: false,
    };
    drafts.storage.save(replacement);

    // The hub has since moved to a NEWER revision by the time the settling
    // refresh lands - the replacement (composed against 3) is stale
    // relative to THAT authoritative revision, not the write's pre-apply
    // one, and the adopted conflict must reflect it.
    client.on(getMethod, () => payload(6, []));
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      revision: 6,
      writeUncertain: false,
      draftConflict: true,
      draft: { revision: 3, rules: otherRules },
    });
    expect(drafts.stored()).toEqual(replacement);
  });

  test("settling an uncertain write whose replaceClassified throws marks storage unavailable, not just draftError", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });
    await expect(store.getState().saveDraft(rules)).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);

    // The settle path's own write to the draft port fails (a disk error),
    // the same failure persistDraft already reports as storageUnavailable -
    // refreshOverrides keys its one-more-restore-attempt recovery on that
    // flag, and the native domain projects it to the UI, so a settle
    // failure that only sets draftError is invisible to both.
    drafts.failReplace();
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      storageUnavailable: true,
      draftError: "Could not save the shortcut draft locally.",
    });
  });

  test("editDraft's local save failure marks draftError alongside storageUnavailable", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    drafts.failSave();

    // Native renders draftError ?? hubError: a save failure that only sets
    // storageUnavailable disables the editor with no message at all.
    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");

    expect(store.getState()).toMatchObject({
      storageUnavailable: true,
      draftError: "Could not save the shortcut draft locally.",
    });
  });

  test("refreshing the same classified checkpoint preserves a stale generation after a failed save", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft(rules);

    // The same-revision reconnect makes this draft stale by generation.
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ draftConflict: true, draft: { generation: 1 } });

    // A failed local save leaves the classified checkpoint unchanged. The
    // next refresh must preserve that record's already-earned generation,
    // rather than classify it as a new draft for the current generation.
    drafts.failReplace();
    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      storageUnavailable: false,
      draftConflict: true,
      draft: { revision: 3, rules, generation: 1 },
    });
    await expect(store.getState().saveDraft()).rejects.toThrow(
      "Review the current shortcuts before saving your changes.",
    );
  });

  test("refreshing an equal-content replacement does not preserve the prior generation", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    store.getState().editDraft(rules);

    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ draftConflict: true, draft: { generation: 1 } });

    drafts.failReplace();
    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");
    drafts.failReplace(false);
    drafts.storage.save({ id: "replacement", baseRevision: 3, rules, writeUncertain: false });

    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      storageUnavailable: false,
      draftConflict: false,
      draft: { revision: 3, rules, generation: 3 },
    });
  });

  test("refreshing a raw-different replacement with identical decoded fields does not preserve the prior generation", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    store.getState().editDraft(rules);

    // The same-revision reconnect makes this draft stale by generation.
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ draftConflict: true, draft: { generation: 1 } });

    // A failed local save leaves the classified checkpoint unchanged, so the
    // recovery below compares against the record editDraft itself wrote.
    drafts.failReplace();
    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");
    drafts.failReplace(false);

    // Another writer replaces the record with one that DECODES identically -
    // every field this build knows is unchanged (the same id, baseRevision,
    // rules and writeUncertain) - but whose raw storage bytes differ by an
    // extra field this build's decoder drops. It is still a DIFFERENT record,
    // so it must take the ordinary null-generation restore path rather than
    // carrying the stale generation forward.
    const classified = drafts.stored() as KeybindingDraftCheckpoint;
    drafts.storage.save({ ...classified, futureField: 1 } as KeybindingDraftCheckpoint);

    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      storageUnavailable: false,
      draftConflict: false,
      draft: { revision: 3, rules, generation: 3 },
    });
  });

  test("editDraft's own id-generation failure sets storageUnavailable and draftError, the same as a save failure", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const throwingCreateId: typeof drafts.storage = {
      ...drafts.storage,
      createId: () => {
        throw new Error("crypto unavailable");
      },
    };
    const store = await readyStore(clientServing(3), { drafts: throwingCreateId });

    // createId() mints the checkpoint's id before save() is ever called - a
    // failure there is exactly as much this build's local-write failure as
    // save() throwing, and must not escape uncaught with the store never
    // told a write was attempted.
    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");

    expect(store.getState()).toMatchObject({
      storageUnavailable: true,
      draftError: "Could not save the shortcut draft locally.",
    });
  });

  test("editDraft's persistDraft refusal adopts the replacement instead of reporting storageUnavailable", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const mine: KeybindingDraftCheckpoint = { id: "mine", baseRevision: 3, rules, writeUncertain: false };
    drafts.storage.save(mine);
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules, generation: 1 });

    // Another window replaces the SAME classified record while this store
    // still thinks it owns it - a CAS mismatch, not a storage exception.
    const otherRules = [{ action: ACTIONS.railToggle, chord: "Control+R" }];
    const replacement: KeybindingDraftCheckpoint = {
      id: "other",
      baseRevision: 3,
      rules: otherRules,
      writeUncertain: false,
    };
    drafts.storage.save(replacement);

    const attempted = [{ action: ACTIONS.paletteOpen, chord: "Meta+K" }];
    expect(() => store.getState().editDraft(attempted)).toThrow("Shortcuts changed again. Review the current values.");

    // The refusal adopts what is actually on disk rather than reporting a
    // storage failure and leaving the stale classification in place.
    expect(store.getState()).toMatchObject({
      storageUnavailable: false,
      draftError: null,
      draft: { version: 1, revision: 3, rules: otherRules },
    });
    expect(drafts.stored()).toEqual(replacement);
  });

  // The no-port fallback storage's removeIf/replaceIf report success
  // unconditionally: there is no real backing store behind them, so no
  // concurrent writer to have raced against, and a refusal there would read
  // as "a concurrent writer replaced the record" and adopt restoreDraft
  // (which reads null from the same fallback), silently dropping the
  // in-memory draft over a race that cannot happen.
  test("without a draft port, a revision conflict's settle does not misread the ephemeral fallback as a concurrent replacement", async () => {
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        current: payload(6, [{ action: ACTIONS.railToggle, chord: "Control+R" }]),
      });
    });
    const store = await readyStore(client); // no drafts port: the ephemeral fallback

    await expect(store.getState().saveDraft(rules)).rejects.toThrow("revision conflict");

    expect(store.getState()).toMatchObject({ revision: 6, writeUncertain: false, draftConflict: true });
    // The draft must still be here for review, not silently wiped to null.
    expect(store.getState().draft).not.toBeNull();
    expect(store.getState().draft?.rules).toEqual(rules);
  });

  test("a post-rename durable failure during saveDraft applies the carried canonical state instead of leaving the outcome unknown", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    client.on(patchMethod, () => {
      throw new WireError("sync keybindings state directory: boom", -32603, {
        evenerErrorInfo: "keybindingsPostRename",
        applied: payload(4, rules),
      });
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    const saved = await store.getState().saveDraft(rules);

    expect(saved).toEqual(payload(4, rules));
    expect(store.getState()).toMatchObject({
      revision: 4,
      writeUncertain: false,
      saving: false,
      draft: null,
      // draftConflict must clear with the draft it was describing: forcing
      // it from newerExternal (false here) rather than leaving it to
      // applyHubOverrides' own staleDraft check, which reads the PRE-write
      // draft against the just-confirmed revision and would misread this
      // write's own success as a conflict.
      draftConflict: false,
    });
    expect(drafts.stored()).toBeNull();
  });

  // settleWrite's refusal-adoption is one path for both the post-rename
  // branch and the confirmed-reply success branch below.
  test("a post-rename durable failure whose checkpoint was replaced adopts the replacement", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3);
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const store = await readyStore(client, { drafts: drafts.storage });

    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // Another window or app version replaces the SAME on-disk record with
    // its own draft while this write is still out.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    const replacement: KeybindingDraftCheckpoint = {
      id: "other",
      baseRevision: 3,
      rules: otherRules,
      writeUncertain: false,
    };
    drafts.storage.save(replacement);

    reply.reject(
      new WireError("sync keybindings state directory: boom", -32603, {
        evenerErrorInfo: "keybindingsPostRename",
        applied: payload(4, rules),
      }),
    );
    await save;

    // The write applied, but the checkpoint it wrote is gone - replaced, not
    // just removed. The replacement survives on disk, and the store adopts
    // it rather than reporting no draft.
    expect(drafts.stored()).toEqual(replacement);
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules: otherRules, generation: null });
  });

  // Both rejection branches below apply their payload through
  // applyHubOverridesSettling, the same as the main post-reply sequence: a
  // reconciler throw (a wedged registry that has already rolled back) still
  // publishes `saving: false` alongside the hubError, so the editor is never
  // left disabled with nothing left to clear it.
  function wedgePaletteDefault(registry: KeybindingsRegistry): void {
    const paletteDefault = registryWithDefaults()
      .getState()
      .bindings.find((b) => b.id === ACTIONS.paletteOpen);
    if (paletteDefault === undefined) throw new Error("test setup: no default for palette.open");
    registry
      .getState()
      .registerBinding({ id: "foreign.squatter", actionId: "foreign", chord: serializeChord(paletteDefault.chord) });
  }

  test("a post-rename durable failure whose local reconcile throws still clears saving", async () => {
    const registry = registryWithDefaults();
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3, rules);
    client.on(patchMethod, () => {
      throw new WireError("sync keybindings state directory: boom", -32603, {
        evenerErrorInfo: "keybindingsPostRename",
        applied: payload(4, []),
      });
    });
    const store = await readyStore(client, { registry, drafts: drafts.storage });
    wedgePaletteDefault(registry);

    await expect(store.getState().saveDraft([])).rejects.toThrow();

    expect(store.getState().saving).toBe(false);
    expect(store.getState().hubError).not.toBeNull();
  });

  test("a revision conflict whose local reconcile throws still clears saving", async () => {
    const registry = registryWithDefaults();
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3, rules);
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        current: payload(5, []),
      });
    });
    const store = await readyStore(client, { registry, drafts: drafts.storage });
    wedgePaletteDefault(registry);

    // The reconciler's own throw (a wedged registry) is what the caller
    // sees, the same shape the post-rename branch follows: a settle that
    // fails locally rejects with THAT failure, not the rejection that
    // triggered the settle attempt.
    await expect(store.getState().saveDraft([])).rejects.toThrow(/keybinding conflict/);

    expect(store.getState().saving).toBe(false);
    expect(store.getState().hubError).not.toBeNull();
  });

  test("a lost reply leaves the write uncertain and blocks edits until an authoritative read", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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

  test("editDraft's persistDraft failure marks draftError, not just storageUnavailable", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.failSave();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });

    expect(() => store.getState().editDraft(rules)).toThrow("Could not save the shortcut draft locally.");

    // storageUnavailable alone is invisible to a host that renders draftError
    // as its banner text (nativePreferences.ts's keybindingsDomain falls back
    // to a hub-sourced message that says nothing about a local disk error).
    expect(store.getState()).toMatchObject({
      storageUnavailable: true,
      draftError: "Could not save the shortcut draft locally.",
    });
  });

  test("discardStoredKeybindingDraft removes an unreadable record with no store at all", () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.corrupt();

    expect(discardStoredKeybindingDraft(drafts.storage)).toBe("removed");

    expect(drafts.stored()).toBeNull();
  });

  test("discardStoredKeybindingDraft reports absent when nothing is stored to remove", () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();

    expect(discardStoredKeybindingDraft(drafts.storage)).toBe("absent");
  });

  test("discardStoredKeybindingDraft, given isReadableKeybindingDraft, refuses a record a concurrent writer replaced with a valid one", () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.storage.save({ id: "d1", baseRevision: 3, rules, writeUncertain: false });

    expect(discardStoredKeybindingDraft(drafts.storage, isReadableKeybindingDraft)).toBe("refused");

    expect(drafts.stored()).toEqual({ id: "d1", baseRevision: 3, rules, writeUncertain: false });
  });

  test("discardStoredKeybindingDraft refuses a readable record even when the caller passes no isReadable of its own", () => {
    // The generic draftCheckpointPort.discardStoredDraft defaults to
    // () => false (never refuses) for a caller with no decoder at all - the
    // keybinding-named export must not inherit that default, or a caller
    // that forgets to pass isReadableKeybindingDraft silently deletes a
    // record this build can actually read instead of refusing.
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.storage.save({ id: "d1", baseRevision: 3, rules, writeUncertain: false });

    expect(discardStoredKeybindingDraft(drafts.storage)).toBe("refused");

    expect(drafts.stored()).toEqual({ id: "d1", baseRevision: 3, rules, writeUncertain: false });
  });

  test("an unreadable stored record never locks the section", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.corrupt();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });

    // The RECORD is unreadable, not the port, and the hub is not implicated.
    expect(store.getState()).toMatchObject({
      draft: null,
      storageUnavailable: true,
      draftUnreadable: true,
      loaded: true,
    });
    expect(store.getState().revision).toBe(3);

    // Discarding is allowed and is what clears the record.
    store.getState().discardDraft();
    expect(drafts.stored()).toBeNull();
    expect(store.getState()).toMatchObject({ storageUnavailable: false, draftUnreadable: false });
    expect(() => store.getState().editDraft(rules)).not.toThrow();
  });

  test("a record that becomes unreadable while a write is uncertain clears the stale uncertainty, unblocking discard", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const client = clientServing(3, rules);
    client.on(patchMethod, () => {
      throw new Error("token secret");
    });
    const store = await readyStore(client, { drafts: drafts.storage });

    await expect(store.getState().saveDraft([])).rejects.toThrow();
    expect(store.getState().writeUncertain).toBe(true);

    // The stored checkpoint becomes unreadable (a newer app version wrote a
    // shape this build cannot decode) while the write's outcome is still
    // unknown.
    drafts.corrupt();
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftUnreadable: true });
    // The record is unreadable - there is nothing left to be "uncertain"
    // about and nothing to review a "conflict" against. Both must clear, or
    // discardDraft (the one recovery this state allows) is refused too.
    expect(store.getState().writeUncertain).toBe(false);
    expect(store.getState().draftConflict).toBe(false);
    expect(store.getState().draft).toBeNull();
    expect(() => store.getState().discardDraft()).not.toThrow();
    expect(store.getState()).toMatchObject({ storageUnavailable: false, draftUnreadable: false });
  });

  test("a discard refuses and re-classifies when the unreadable record has been replaced", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.corrupt();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    expect(store.getState().draftUnreadable).toBe(true);

    // Another store or app version replaces the SAME record with a valid,
    // newer checkpoint before the user ever taps discard.
    const newer: KeybindingDraftCheckpoint = { id: "d1", baseRevision: 3, rules, writeUncertain: false };
    drafts.storage.save(newer);

    // The discard must name the record it classified, not whatever is
    // stored right now: it refuses (the bytes it names are gone), and the
    // newer checkpoint survives.
    store.getState().discardDraft();
    expect(drafts.stored()).toEqual(newer);

    // Refusing is not silence: the state re-classifies against what is
    // actually there now, which is readable.
    expect(store.getState().draftUnreadable).toBe(false);
    expect(store.getState().storageUnavailable).toBe(false);
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules, generation: null });
  });

  test("a genuine storage-read failure never erases an earlier unreadable-record recovery", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.corrupt();
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftUnreadable: true });

    // The PORT itself now fails to read - a genuine disk error, not merely
    // an unreadable record. restoreDraft has no idea what is actually
    // stored, so it must leave draftUnreadable as it was rather than clear
    // it, which would silently hide the "Discard unreadable draft" action.
    drafts.failLoad();
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftUnreadable: true });
  });

  test("a recovered port clears the storageUnavailable state its failed restore published", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>({
      id: "d1",
      baseRevision: 3,
      rules,
      writeUncertain: false,
    });
    drafts.failLoad();
    const store = createKeybindingsStore({ client: clientServing(3), drafts: drafts.storage });

    // The creation-time restore hit a dead port: the section is blocked on
    // storageUnavailable, no draft was restored, and - unlike an unreadable
    // RECORD's restore - nothing is claimed about the record either way.
    expect(store.getState()).toMatchObject({
      storageUnavailable: true,
      draft: null,
      draftUnreadable: false,
    });

    // The port heals; the next refresh's recovery seam re-reads it, which
    // both clears storageUnavailable and restores the draft the failed read
    // hid, unblocking the editor.
    drafts.failLoad(false);
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    expect(store.getState()).toMatchObject({
      storageUnavailable: false,
      draft: { version: 1, revision: 3, rules },
      writeUncertain: false,
      loaded: true,
    });
    expect(() => store.getState().editDraft(rules)).not.toThrow();
  });

  test("a discard refuses and re-classifies when the record has been replaced", async () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>({
      id: "d0",
      baseRevision: 3,
      rules,
      writeUncertain: false,
    });
    const store = await readyStore(clientServing(3), { drafts: drafts.storage });
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules, generation: 1 });

    // Another store or app version replaces the SAME record with a valid,
    // newer checkpoint - under different storage bytes - between this
    // store's load and the user's tap on discard.
    const otherRules = [{ action: ACTIONS.paletteOpen, chord: "Control+P" }];
    const newer: KeybindingDraftCheckpoint = { id: "d1", baseRevision: 3, rules: otherRules, writeUncertain: false };
    drafts.storage.save(newer);

    // The discard must name the record this store actually loaded, not a
    // fresh reload at discard time: it refuses, and the newer checkpoint
    // survives - and is what the store now shows, never no draft at all.
    store.getState().discardDraft();
    expect(drafts.stored()).toEqual(newer);
    expect(store.getState().storageUnavailable).toBe(false);
    expect(store.getState().draft).toEqual({ version: 1, revision: 3, rules: otherRules, generation: null });
  });

  test("a store built over a stored checkpoint restores the draft synchronously", () => {
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    drafts.storage.save({ id: "x", baseRevision: 3, rules, writeUncertain: true });
    const store = createKeybindingsStore({ client: new FakeClient("ready"), drafts: drafts.storage });
    expect(store.getState()).toMatchObject({ draft: { revision: 3, rules }, writeUncertain: true });
  });

  test("a direct write is refused while a checkpointed write owns the payload", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // Both paths take the same write token, so a direct write starting here
    // would fence the save's own reply out and strand saving true.
    await expect(store.getState().patchOverrides(rules)).rejects.toThrow("unavailable");

    reply.resolve(payload(4, rules));
    await save;
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
  });

  test("a checkpointed write is refused while a direct write owns the payload", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const direct = store.getState().patchOverrides(rules);
    await vi.waitFor(() => expect(client.calls.filter((c) => c.method === patchMethod)).toHaveLength(1));

    // The reverse of the case above: a checkpointed save starting here would
    // claim a NEWER write token, fencing the direct write's own reply out as
    // superseded even though the hub may have already applied it.
    await expect(store.getState().saveDraft(rules)).rejects.toThrow("unavailable");

    reply.resolve(payload(4, rules));
    await direct;
    expect(store.getState()).toMatchObject({ revision: 4 });
    // The gate lifts once the direct write has settled.
    await expect(store.getState().saveDraft(rules)).resolves.toBeDefined();
  });

  test("a same-turn saveDraft is refused behind a just-issued direct write", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const replies = gateSettlements(client, patchMethod);

    // Both calls issued in the SAME turn, with no await between them: the
    // direct write is only QUEUED (its own PATCH has not fired yet) when
    // saveDraft's gate runs, synchronously, right after.
    const direct = store.getState().patchOverrides(rules);
    const save = store.getState().saveDraft(rules);

    await expect(save).rejects.toThrow("unavailable");
    await vi.waitFor(() => expect(replies).toHaveLength(1));
    // Refusing saveDraft must never have let it send its own PATCH.
    expect(replies).toHaveLength(1);

    replyAt(replies, 0).resolve(payload(4, rules));
    await expect(direct).resolves.toBeDefined();
  });

  test("a save lost to a support flap settles draftConflict the same as the other unknown-outcome paths", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);

    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // A transient disconnect: support drops to "unknown" without ending the
    // generation or retiring the payload (settleLostHubWrite's own comment)
    // - the write's own claim is still intact, but nothing else will ever
    // settle it.
    store.setSupport("unknown");

    reply.reject(new Error("disconnected"));
    await expect(save).rejects.toThrow("disconnected");

    // Same posture as the no-reply-at-all and malformed-reply unknown-outcome
    // settles: the proposal needs review.
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftConflict: true });
  });

  test("a direct write retired by a generation reset no longer blocks saveDraft", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const replies = gateSettlements(client, patchMethod);
    const direct = store.getState().patchOverrides(rules);
    await vi.waitFor(() => expect(replies).toHaveLength(1));

    // A transient disconnect retires the generation while the direct write's
    // own request is still out - it never reaches its reply, let alone the
    // reply's finally cleanup.
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    // The gate lifts on retirement; it does not wait for the abandoned
    // request's own reply to clear it. saveDraft's own PATCH gets its own
    // gated reply so it can settle without touching the abandoned one.
    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(replies).toHaveLength(2));
    replyAt(replies, 1).resolve(payload(4, rules));
    await expect(save).resolves.toBeDefined();

    // The abandoned reply, landing later, is superseded and touches nothing.
    replyAt(replies, 0).resolve(payload(5, rules));
    await direct;
  });

  test("a stale direct write's late reply cannot clear a newer direct write's flag", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const replies = gateSettlements(client, patchMethod);

    const one = store.getState().patchOverrides(rules);
    await vi.waitFor(() => expect(replies).toHaveLength(1));

    // reset() wipes the write queue without settling the first request, so a
    // second direct write can start while the first is still out.
    store.reset();
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();

    const two = store.getState().patchOverrides(rules);
    await vi.waitFor(() => expect(replies).toHaveLength(2));

    // The stale first request settling must not clear the second write's
    // claim: saveDraft must stay refused (never issuing a THIRD PATCH)
    // while the second write is still out. Raced against a bounded timeout
    // instead of a bare `await` - the bug this guards against is exactly a
    // saveDraft call slipping past the gate and hanging on a PATCH reply
    // nothing in this test ever sends.
    replyAt(replies, 0).resolve(payload(4, rules));
    await one;
    const attempt = store.getState().saveDraft(rules);
    attempt.catch(() => {});
    const outcome = await Promise.race([
      attempt.then(
        () => "resolved",
        (error: unknown) => `rejected:${error instanceof Error ? error.message : String(error)}`,
      ),
      new Promise<string>((resolve) => setTimeout(() => resolve("still pending"), 200)),
    ]);
    expect(outcome).toBe("rejected:Hub keybindings settings are unavailable.");
    expect(replies).toHaveLength(2);

    replyAt(replies, 1).resolve(payload(5, rules));
    await two;
  });

  test("an older save's late reply does not clear a newer save's saving flag", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const first = deferred<KeybindingsOverrides>();
    const second = deferred<KeybindingsOverrides>();
    let sends = 0;
    client.on(patchMethod, () => (++sends === 1 ? first.promise : second.promise));

    const one = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(sends).toBe(1));

    // A transient disconnect retires the payload, which frees the editor while
    // the first request is STILL OUT - that is what lets a second save start.
    store.endReadyGeneration();
    expect(store.getState().saving).toBe(false);
    store.beginReadyGeneration();
    await store.getState().refreshOverrides();
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
    store.getState().rebaseDraft(3);

    const two = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(sends).toBe(2));
    expect(store.getState().saving).toBe(true);

    // Superseded, not fenced by a support flip: it may not report the write the
    // editor IS waiting on as finished.
    first.resolve(payload(4, rules));
    await one;
    expect(store.getState().saving).toBe(true);

    second.resolve(payload(4, rules));
    await two;
    expect(store.getState().saving).toBe(false);
  });

  test("a rejection arriving after support went unknown still clears saving", async () => {
    const client = clientServing(3);
    const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<KeybindingsOverrides>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft(rules);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // The transient-disconnect window keeps the payload and the in-flight work
    // but makes isSupported() false, so the reply is fenced with no retirement
    // having published saving false.
    store.setSupport("unknown");
    expect(store.getState().saving).toBe(true);

    reply.reject(new Error("connection lost"));
    await expect(save).rejects.toThrow("connection lost");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true });
  });

  test.each<[string, unknown]>([
    ["a whitespace-only action id", [{ action: " ", chord: "Control+P" }]],
    ["a whitespace-only chord", [{ action: ACTIONS.paletteOpen, chord: "\t\n" }]],
  ])("editDraft refuses %s, as the hub would", async (_name, proposed) => {
    const store = await readyStore(clientServing(3), {
      drafts: memoryDraftStorage<KeybindingDraftCheckpoint>().storage,
    });
    let error: unknown;
    try {
      store.getState().editDraft(proposed as KeybindingsRule[]);
    } catch (caught) {
      error = caught;
    }
    // Malformed user input is a plain validation failure, never
    // UnreadableDraftError: that type is reserved for a stored record this
    // build cannot read, and conflating the two would make a bad paste look
    // like a corrupt draft.
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(UnreadableDraftError);
    expect((error as Error).message).toBe("Invalid keybinding draft.");
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
    const store = await readyStore(client, {
      registry,
      drafts: memoryDraftStorage<KeybindingDraftCheckpoint>().storage,
    });
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
    /** A fenced write settles under a new generation and must remain stale. */
    draftConflictAfterSettle?: boolean;
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
      draftConflictAfterSettle: true,
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
      draftConflictAfterSettle: true,
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
    async ({
      rules = proposed,
      arrange,
      reply = payload(4, rules),
      rejects,
      after,
      settle,
      draftConflictAfterSettle = false,
    }) => {
      const registry = registryWithDefaults();
      const drafts = memoryDraftStorage<KeybindingDraftCheckpoint>();
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
        draftConflict: draftConflictAfterSettle,
        loaded: true,
      });
      expect(() => store.getState().editDraft([])).not.toThrow();
    },
  );
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
