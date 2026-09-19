import { afterEach, describe, expect, test, vi } from "vitest";
import { CONNECTION_REPLACED_ERROR } from "../../credentialLabels";
import { errorText, friendlyErrorMessage, sessionActionError } from "../../errors";
import { deferred } from "../../testing/deferred";
import { FakeClient } from "../../testing/fakeClient";
import type { AuthStatusResponse, InstanceEntry, InstanceListResponse } from "../../types.gen";
import {
  createCredentialInstancesStore,
  foreignListingChange,
  isStaleListingRefusal,
  staleListingHeld,
} from "./instances";

const WORK: InstanceEntry = {
  name: "work",
  providerId: "openai-codex",
  protocol: "openai-responses",
  auth: "oauth-openai-codex",
  isDefault: true,
  implicit: false,
  authModes: ["oauth"],
  activeSource: "oauth",
  hasStoredFile: false,
  hasStoredOAuth: true,
  envVar: "",
  storedEmail: "me@example.com",
  credentialRequired: true,
};

const LISTING: InstanceListResponse = {
  instances: [WORK],
  availableProviders: [{ id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true }],
};

function readyClient(listing: InstanceListResponse = LISTING): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => listing);
  return fake;
}

function listReads(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "evener/instance/list").length;
}

afterEach(() => {
  vi.useRealTimers();
});

describe("two stores share nothing", () => {
  test("a listing read and a connection change in one store leave the other untouched", async () => {
    const first = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const second = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    first.connectionChanged(readyClient(), "ready");

    expect(await first.getState().fetch()).toBe(true);
    expect(first.getState().instances).toEqual([WORK]);
    expect(first.getState().diagnostics).toEqual([]);
    expect(first.getState().userLayer).toBe("");
    expect(first.getState().writesRefused).toBe(false);
    expect(second.getState().instances).toEqual([]);
    await expect(second.getState().fetch()).rejects.toThrow(/no client connected/);

    // Replacing the first store's client marks ITS held listing, not the second's.
    first.connectionChanged(readyClient(), "ready");
    expect(first.getState().listingFromPreviousConnection).toBe(true);
    expect(staleListingHeld(first.getState())).toBe(true);
    expect(second.getState().listingFromPreviousConnection).toBe(false);
  });

  test("a landed write's per-instance bookkeeping is the store's own", async () => {
    const first = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const second = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const firstClient = readyClient();
    firstClient.on("evener/instance/setDefault", () => LISTING);
    const secondClient = readyClient();
    first.connectionChanged(firstClient, "ready");
    second.connectionChanged(secondClient, "ready");
    await first.getState().fetch();
    await second.getState().fetch();

    // A refresh in the second store is out while the FIRST store lands a write
    // on the same instance name: only the first store's count moved, so the
    // second store's refresh still applies.
    const pending = deferred<InstanceListResponse>();
    secondClient.on("evener/instance/refreshModels", () => pending.promise);
    const refresh = second.getState().refreshModels("work");
    await first.getState().setDefault("work");
    pending.resolve({ ...LISTING, instances: [{ ...WORK, models: [{ id: "m1" }] }] });
    await refresh;
    expect(second.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1"]);
    expect(first.getState().instances[0]?.models).toBeUndefined();
  });
});

describe("listing reads and writes", () => {
  test("only the most recently started request replaces the listing", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    const oldAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/list", () => (listReads(fake) === 1 ? oldAnswer.promise : LISTING));
    store.connectionChanged(fake, "ready");

    const old = store.getState().fetch();
    expect(await store.getState().fetch()).toBe(true);
    oldAnswer.resolve({ instances: [], availableProviders: [] });
    expect(await old).toBe(false);
    expect(store.getState().instances).toEqual([WORK]);
    expect(store.getState().loading).toBe(false);
  });

  test("a failed read lands its message in error and resolves false", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => {
      throw new Error("offline");
    });
    store.connectionChanged(fake, "ready");
    expect(await store.getState().fetch()).toBe(false);
    expect(store.getState().error).toBe("offline");
    expect(store.getState().loading).toBe(false);
  });

  test("a write applies the listing it answers with and clears the replaced-connection mark", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    const renamed = { ...LISTING, instances: [{ ...WORK, isDefault: false }] };
    fake.on("evener/instance/setDefault", () => renamed);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    expect(await store.getState().setDefault("work")).toBe(true);
    expect(store.getState().instances[0]?.isDefault).toBe(false);
    expect(fake.calls.at(-1)).toEqual({
      method: "evener/instance/setDefault",
      params: { name: "work", originClientId: "tab-1" },
    });
  });

  test("a superseded instance write schedules the core's own reconcile read", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    const readAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/list", () => (listReads(fake) === 1 ? LISTING : readAnswer.promise));
    const write = deferred<InstanceListResponse>();
    fake.on("evener/instance/remove", () => write.promise);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    const remove = store.getState().remove("work");
    // A read starts after the write and supersedes its answer.
    const read = store.getState().fetch();
    write.resolve(LISTING);
    expect(await remove).toBe(false);
    const reads = listReads(fake);
    readAnswer.resolve(LISTING);
    await read;
    // The core scheduled the reconcile read itself; no caller had to ask, so a
    // screen that issued the write and then unmounted cannot strand it.
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(reads + 1);
  });

  test("a write from a replaced connection's listing is refused with the shared words until this connection reads", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    store.connectionChanged(readyClient(), "ready");
    await store.getState().fetch();

    const restore = deferred<InstanceListResponse>();
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => restore.promise);
    replacement.on("evener/instance/setDefault", () => LISTING);
    // The replacement's own restore read is issued by the store and held open.
    store.connectionChanged(replacement, "ready");

    const refusal = await store
      .getState()
      .setDefault("work")
      .then(
        () => undefined,
        (err: unknown) => err as Error,
      );
    expect(isStaleListingRefusal(refusal)).toBe(true);
    expect(refusal?.message).toBe(CONNECTION_REPLACED_ERROR);
    expect(replacement.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(0);

    restore.resolve(LISTING);
    await vi.waitFor(() => expect(store.getState().listingFromPreviousConnection).toBe(false));
    expect(await store.getState().setDefault("work")).toBe(true);
  });

  test("a same-client state transition keeps the rows this client's", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    store.connectionChanged(fake, "reconnecting");
    expect(store.getState().listingFromPreviousConnection).toBe(false);
    expect(store.getState().instances).toEqual([WORK]);
  });
});

describe("connection changes", () => {
  test("once a view has read the listing, a client becoming ready again restores it", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    await vi.waitFor(() => expect(listReads(fake)).toBe(2));
  });

  test("a store that never read does not fetch on ready", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    await Promise.resolve();
    expect(fake.calls).toHaveLength(0);
  });

  test("a connection change cancels a pending refetch", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    store.connectionChanged(fake, "ready");

    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "oauth" } });
    store.connectionChanged(readyClient(), "ready");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(0);
  });
});

describe("resetForTests", () => {
  test("returns the listing to empty and drops the connection", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    store.connectionChanged(readyClient(), "ready");
    await store.getState().fetch();
    store.resetForTests();
    expect(store.getState().instances).toEqual([]);
    await expect(store.getState().fetch()).rejects.toThrow(/no client connected/);
  });
});

// Sentinel secret material: distinctive enough that a substring search over
// state, error text and console output is a real assertion, and shaped like
// no real credential so the repo's secret scanner does not read it as one.
const API_KEY = "never-echo-sentinel-api-key-value";
const CREDENTIAL_JSON = '{"type":"authorized_user","refresh_token":"never-echo-sentinel-refresh-token-value"}';
const SIGNED_IN: AuthStatusResponse = {
  provider: "work",
  supported: true,
  signedIn: true,
  activeSource: "api_key",
  hasStoredOAuth: false,
  hasStoredFile: true,
};

const SENTINELS = [API_KEY, CREDENTIAL_JSON, "never-echo-sentinel"];

function nowhere(haystack: string) {
  for (const secret of SENTINELS) expect(haystack).not.toContain(secret);
}

function consoleText(spies: ReturnType<typeof vi.spyOn>[]): string {
  return JSON.stringify(spies.flatMap((spy) => spy.mock.calls));
}

describe("never echo a secret", () => {
  test("no secret reaches published state, thrown errors, user-facing text or the console on any path", async () => {
    const spies = (["log", "info", "warn", "error", "debug"] as const).map((level) =>
      vi.spyOn(console, level).mockImplementation(() => {}),
    );
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => SIGNED_IN);
    fake.on("evener/auth/credentialJson/set", () => SIGNED_IN);
    fake.on("evener/auth/apiKey/clear", () => ({ ...SIGNED_IN, signedIn: false, activeSource: "none" }));
    fake.on("evener/auth/logout", () => ({
      removed: true,
      status: { ...SIGNED_IN, signedIn: false, activeSource: "none" },
    }));
    fake.on("evener/auth/test", () => ({ provider: "work", status: "success", message: "" }));
    fake.on("evener/auth/device/poll", () => ({ state: "pending" }));
    fake.on("evener/auth/status", () => SIGNED_IN);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    const seen: string[] = [];
    const record = () => seen.push(JSON.stringify(store.getState()));
    store.subscribe(record);

    // Success paths: the secret goes on the wire and nowhere else.
    await store.getState().setApiKey("work", API_KEY, "fp-1");
    await store.getState().setCredentialJson("work", CREDENTIAL_JSON);
    await store.getState().clearStoredKey("work");
    await store.getState().logout("work");
    await store.getState().testCredentials("work");
    await store.getState().devicePoll("work", "flow-1");
    await store.getState().authStatus("work");
    expect(fake.calls.find((call) => call.method === "evener/auth/apiKey/set")?.params).toEqual({
      provider: "work",
      value: API_KEY,
      expectedEndpointFingerprint: "fp-1",
      originClientId: "tab-1",
    });

    // Failure paths: a refused write, a probe that fails, a listing read that
    // fails while a write is out, and a write refused by the stale gate.
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("refused by the hub");
    });
    fake.on("evener/auth/credentialJson/set", () => {
      throw new Error("refused by the hub");
    });
    fake.on("evener/auth/test", () => {
      throw new Error("endpoint unreachable");
    });
    const failures: unknown[] = [];
    for (const attempt of [
      () => store.getState().setApiKey("work", API_KEY),
      () => store.getState().setCredentialJson("work", CREDENTIAL_JSON),
      () => store.getState().testCredentials("work"),
    ]) {
      await attempt().then(
        () => expect.fail("expected a rejection"),
        (err: unknown) => failures.push(err),
      );
    }
    fake.on("evener/instance/list", () => {
      throw new Error("listing unavailable");
    });
    await store.getState().fetch();
    store.connectionChanged(readyClient(), "ready");
    await store
      .getState()
      .setApiKey("work", API_KEY)
      .then(
        () => expect.fail("expected the stale-listing refusal"),
        (err: unknown) => failures.push(err),
      );

    expect(failures).toHaveLength(4);
    for (const err of failures) {
      const failure = err as Error & { data?: unknown };
      nowhere(failure.message);
      nowhere(JSON.stringify(failure.data ?? null));
      nowhere(errorText(failure));
      nowhere(friendlyErrorMessage(failure));
      nowhere(sessionActionError("Could not save the key", failure));
    }
    for (const snapshot of seen) nowhere(snapshot);
    nowhere(JSON.stringify(store.getState()));
    nowhere(consoleText(spies));
    for (const spy of spies) spy.mockRestore();
  });
});

describe("foreignListingChange", () => {
  async function storeWithListing() {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => SIGNED_IN);
    fake.on("evener/instance/setDefault", () => ({ ...LISTING, instances: [{ ...WORK, isDefault: false }] }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    const seen: boolean[] = [];
    store.subscribe((state, previous) => seen.push(foreignListingChange(state, previous)));
    return { store, fake, seen };
  }
  type Scenario = [
    name: string,
    foreign: boolean,
    drive: (s: Awaited<ReturnType<typeof storeWithListing>>) => Promise<void>,
  ];
  const scenarios: Scenario[] = [
    [
      "the store's own post-write refresh",
      false,
      async ({ store }) => {
        await store.getState().setApiKey("work", API_KEY);
        await vi.advanceTimersByTimeAsync(300);
      },
    ],
    ["a caller's own fetchSelf", false, async ({ store }) => void (await store.getState().fetchSelf())],
    [
      "a foreign echo's read",
      true,
      async ({ fake }) => {
        fake.emitNotification({
          method: "evener/auth/updated",
          params: { provider: "work", activeSource: "oauth", originClientId: "tab-2" },
        });
        await vi.advanceTimersByTimeAsync(300);
      },
    ],
    ["an applied write", true, async ({ store }) => void (await store.getState().setDefault("work"))],
    [
      "a read that fails, moving no rows",
      true,
      async ({ store, fake }) => {
        fake.on("evener/instance/list", () => {
          throw new Error("offline");
        });
        await store.getState().fetch();
        expect(store.getState().error).toBe("offline");
      },
    ],
  ];
  test.each(scenarios)("%s is foreign: %s", async (_name, foreign, drive) => {
    vi.useFakeTimers();
    const harness = await storeWithListing();
    await drive(harness);
    expect(harness.seen.some(Boolean)).toBe(foreign);
  });
});

describe("listingEstablished", () => {
  test("false until a listing lands, true across reads and writes, kept across a client change, reset with the store", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    expect(store.getState().listingEstablished).toBe(false);
    const fake = readyClient({ instances: [], availableProviders: [] });
    fake.on("evener/instance/setDefault", () => LISTING);
    store.connectionChanged(fake, "ready");
    expect(store.getState().listingEstablished).toBe(false);

    // An empty listing is still a listing: established, with no rows.
    expect(await store.getState().fetch()).toBe(true);
    expect(store.getState().listingEstablished).toBe(true);
    expect(store.getState().instances).toEqual([]);

    await store.getState().setDefault("work");
    expect(store.getState().listingEstablished).toBe(true);

    // A replaced client keeps the rows on screen, marked as the previous
    // connection's, until its own read lands: established says a listing has
    // been applied, listingFromPreviousConnection says whose.
    store.connectionChanged(readyClient(), "ready");
    expect(store.getState().listingEstablished).toBe(true);
    expect(store.getState().listingFromPreviousConnection).toBe(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(store.getState().listingFromPreviousConnection).toBe(false);
    store.resetForTests();
    expect(store.getState().listingEstablished).toBe(false);
  });
});

describe("a refused write", () => {
  test("an instance write reads nothing of its own; the hub's instance echo is what reads", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/instance/remove", () => {
      throw new Error("connection lost");
    });
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    await expect(store.getState().remove("work")).rejects.toThrow("connection lost");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(1);
    expect(fake.calls.filter((call) => call.method === "evener/instance/remove")).toHaveLength(1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "oauth" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
  });

  test("a credential write reads nothing of its own; the hub's echo, retired of its marker, reads once", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("refused");
    });
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    await expect(store.getState().setApiKey("work", API_KEY)).rejects.toThrow("refused");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "api_key" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
  });
});

describe("authStatus", () => {
  test("reads the provider's status through the read gate, arming no marker and refreshing nothing", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/status", () => SIGNED_IN);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    const before = store.getState();

    expect(await store.getState().authStatus("work")).toEqual(SIGNED_IN);
    expect(fake.calls.at(-1)).toEqual({ method: "evener/auth/status", params: { provider: "work" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(1);
    expect(store.getState()).toBe(before);

    // A read, so it stays available while the rows on screen are a replaced
    // connection's; no client at all is still a programmer error.
    store.connectionChanged(readyClient(), "ready");
    await vi.advanceTimersByTimeAsync(0);
    const restored = new FakeClient("ready");
    restored.on("evener/instance/list", () => LISTING);
    restored.on("evener/auth/status", () => SIGNED_IN);
    store.connectionChanged(restored, "ready");
    expect(store.getState().listingFromPreviousConnection).toBe(true);
    expect(await store.getState().authStatus("work")).toEqual(SIGNED_IN);
    store.resetForTests();
    await expect(store.getState().authStatus("work")).rejects.toThrow(/no client connected/);
  });

  test("a status reply from a connection since replaced is refused with the shared words", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    const reply = deferred<AuthStatusResponse>();
    fake.on("evener/auth/status", () => reply.promise);
    store.connectionChanged(fake, "ready");

    const status = store.getState().authStatus("work");
    store.connectionChanged(readyClient(), "ready");
    reply.resolve(SIGNED_IN);
    await expect(status).rejects.toThrow(CONNECTION_REPLACED_ERROR);
  });
});

describe("credential mutations", () => {
  test("a listing read in flight when a credential write is issued cannot publish after it", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    const preWrite = deferred<InstanceListResponse>();
    let reads = 0;
    fake.on("evener/instance/list", () => (++reads === 1 ? preWrite.promise : LISTING));
    const write = deferred<AuthStatusResponse>();
    fake.on("evener/auth/apiKey/set", () => write.promise);
    store.connectionChanged(fake, "ready");

    const read = store.getState().fetch();
    const save = store.getState().setApiKey("work", API_KEY);
    // The read's answer arrives after the write was issued: it describes the
    // rows before the write, so it is not applied.
    preWrite.resolve({ instances: [{ ...WORK, storedEmail: "stale@example.com" }], availableProviders: [] });
    expect(await read).toBe(false);
    expect(store.getState().instances).toEqual([]);
    write.resolve(SIGNED_IN);
    await save;
    expect(store.getState().loading).toBe(false);
    // The store's own post-write refresh lands the post-write rows.
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().instances).toEqual([WORK]);
  });

  test("a read issued after a credential write keeps the answer's row, not the pre-write held row", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, activeSource: "none" }],
      availableProviders: [],
    }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    expect(store.getState().instances[0]?.activeSource).toBe("none");

    // The credential write is issued and held open, then a read starts - so the
    // read is issued AFTER the write but answers after the write has landed.
    // Its row is the post-write one, while the store holds the pre-write row it
    // read; a credential write installs no row of its own, so keeping the held
    // row here would show the pre-write row until the self-refresh lands.
    const write = deferred<AuthStatusResponse>();
    fake.on("evener/auth/apiKey/set", () => write.promise);
    const save = store.getState().setApiKey("work", API_KEY);
    const readAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/list", () => readAnswer.promise);
    const read = store.getState().fetch();

    write.resolve({ ...SIGNED_IN, activeSource: "store" });
    await save;
    readAnswer.resolve({ instances: [{ ...WORK, activeSource: "store" }], availableProviders: [] });
    expect(await read).toBe(true);
    expect(store.getState().instances[0]?.activeSource).toBe("store");
  });

  test("a landed credential write retires the instance's in-flight refreshModels", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, models: [{ id: "live-old" }] }],
      availableProviders: [],
    }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-old"]);

    // A model refresh is out when the credential write lands.
    const refreshAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/refreshModels", () => refreshAnswer.promise);
    const refresh = store.getState().refreshModels("work");
    const write = deferred<AuthStatusResponse>();
    fake.on("evener/auth/apiKey/set", () => write.promise);
    const save = store.getState().setApiKey("work", API_KEY);
    write.resolve({ ...SIGNED_IN, activeSource: "store" });
    await save;

    // The post-write listing is what the store holds.
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, activeSource: "store", models: [{ id: "live-post" }] }],
      availableProviders: [],
    }));
    await store.getState().fetch();
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-post"]);

    // The refresh's snapshot predates the write: its stale inventory must be
    // discarded, not merged over the post-write listing.
    refreshAnswer.resolve({ instances: [{ ...WORK, models: [{ id: "stale-live" }] }], availableProviders: [] });
    await refresh;
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-post"]);
  });

  test("a credential write that errors after applying still retires the in-flight refreshModels", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, models: [{ id: "live-old" }] }],
      availableProviders: [],
    }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // A model refresh is out when the credential write applies but a later step
    // fails (its status readback): the RPC rejects with no signal that the write
    // stood (the hub's writeApplied marks it only server-side).
    const refreshAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/refreshModels", () => refreshAnswer.promise);
    const refresh = store.getState().refreshModels("work");
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("status read failed");
    });
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, models: [{ id: "live-post" }] }],
      availableProviders: [],
    }));
    await expect(store.getState().setApiKey("work", API_KEY)).rejects.toThrow("status read failed");

    // The errored write retired the in-flight refresh and re-read the listing,
    // so the discarded refresh's inventory is replaced rather than left stale.
    expect(listReads(fake)).toBe(1);
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-post"]);

    // The refresh's snapshot predates the applied write: retired, it must not
    // land over the post-write inventory.
    refreshAnswer.resolve({ instances: [{ ...WORK, models: [{ id: "stale-live" }] }], availableProviders: [] });
    await refresh;
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-post"]);
  });

  test("a credential write that errors without a refresh in flight reads nothing of its own", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("status read failed");
    });
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    await expect(store.getState().setApiKey("work", API_KEY)).rejects.toThrow("status read failed");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(1);
  });

  test("an errored credential write after a retired refresh settles reads nothing when none is in flight", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // A refresh is out, a landed write retires it, and it then settles - which
    // leaves its monotonic version token behind with no refresh in flight.
    const refreshAnswer = deferred<InstanceListResponse>();
    fake.on("evener/instance/refreshModels", () => refreshAnswer.promise);
    const refresh = store.getState().refreshModels("work");
    fake.on("evener/auth/apiKey/set", () => SIGNED_IN);
    await store.getState().setApiKey("work", API_KEY);
    await vi.advanceTimersByTimeAsync(300); // the landed write's own refetch
    expect(listReads(fake)).toBe(2);
    refreshAnswer.resolve(LISTING);
    await refresh;

    // The later errored write has no refresh in flight, so it must not read
    // anything of its own - the lingering token is not a live refresh.
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("status read failed");
    });
    await expect(store.getState().setApiKey("work", API_KEY)).rejects.toThrow("status read failed");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
  });

  test("an overlapping refresh cannot reuse a settled refresh's version and land a stale answer", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, models: [{ id: "live-old" }] }],
      availableProviders: [],
    }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // A and B are out; B (the newer) settles first, then C starts.
    const answerA = deferred<InstanceListResponse>();
    const answerB = deferred<InstanceListResponse>();
    const answerC = deferred<InstanceListResponse>();
    let nth = 0;
    fake.on("evener/instance/refreshModels", () => [answerA.promise, answerB.promise, answerC.promise][nth++]!);
    const refreshA = store.getState().refreshModels("work");
    const refreshB = store.getState().refreshModels("work");
    answerB.resolve({ instances: [{ ...WORK, models: [{ id: "live-b" }] }], availableProviders: [] });
    await refreshB;
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-b"]);

    const refreshC = store.getState().refreshModels("work");

    // A's answer was computed before B and C: it must not land now, even though
    // B's settlement would once have let C reuse A's version.
    answerA.resolve({ instances: [{ ...WORK, models: [{ id: "live-a" }] }], availableProviders: [] });
    await refreshA;
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-b"]);

    answerC.resolve({ instances: [{ ...WORK, models: [{ id: "live-c" }] }], availableProviders: [] });
    await refreshC;
    expect(store.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-c"]);
  });

  test("a refresh settling after its client was swapped out and back keeps a newer refresh counted", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...WORK, models: [{ id: "live-old" }] }],
      availableProviders: [],
    }));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // An old refresh is out on this client object, the connection swaps to
    // another client and back to the same object, and a newer refresh starts.
    const oldAnswer = deferred<InstanceListResponse>();
    const newAnswer = deferred<InstanceListResponse>();
    let nth = 0;
    fake.on("evener/instance/refreshModels", () => (nth++ === 0 ? oldAnswer.promise : newAnswer.promise));
    const oldRefresh = store.getState().refreshModels("work");
    store.connectionChanged(new FakeClient("ready"), "ready");
    store.connectionChanged(fake, "ready");
    await vi.advanceTimersByTimeAsync(300); // the restore read
    const newRefresh = store.getState().refreshModels("work");

    // The old refresh settles: it must not decrement the newer refresh's count.
    oldAnswer.resolve({ instances: [{ ...WORK, models: [{ id: "live-old" }] }], availableProviders: [] });
    await oldRefresh;

    // An errored write still sees the newer refresh in flight, so it reads.
    const before = listReads(fake);
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("status read failed");
    });
    await expect(store.getState().setApiKey("work", API_KEY)).rejects.toThrow("status read failed");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(before + 1);

    newAnswer.resolve({ instances: [{ ...WORK, models: [{ id: "live-new" }] }], availableProviders: [] });
    await newRefresh;
  });

  test("a landed write refreshes the listing once, self-marked; a foreign echo refreshes unmarked", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => SIGNED_IN);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();
    const marked = store.getState().selfRefresh;

    await store.getState().setApiKey("work", API_KEY);
    fake.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "api_key", originClientId: "tab-1" },
    });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);

    const afterOwn = store.getState().selfRefresh;
    fake.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "oauth", originClientId: "tab-2" },
    });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(3);
    expect(store.getState().selfRefresh).toBe(afterOwn);
  });

  test("a stale marker does not shadow a live one for the same provider", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    const first = deferred<AuthStatusResponse>();
    let sets = 0;
    fake.on("evener/auth/apiKey/set", () => (++sets === 1 ? first.promise : SIGNED_IN));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // The first mutation SETTLES (its response lands), then ages past the echo
    // window without its echo; the second is issued inside that window, so it is
    // still live when the echo arrives.
    const aged = store.getState().setApiKey("work", API_KEY);
    first.resolve(SIGNED_IN);
    await aged;
    await vi.advanceTimersByTimeAsync(1000);
    await store.getState().setApiKey("work", API_KEY);
    // 2500ms after the first mutation's stamp: that marker is stale, the
    // second's 1500ms-old one is live.
    await vi.advanceTimersByTimeAsync(1500);

    const marked = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "api_key" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);

    // The skip is what makes this second notification foreign: had the echo
    // consumed the stale marker instead, the live one would still be here to
    // absorb this unrelated change and present it as the store's own refresh.
    const afterOwn = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "oidc" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBe(afterOwn);
  });

  test("a notification on one store's client never refetches another store", async () => {
    vi.useFakeTimers();
    const first = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const second = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const firstClient = readyClient();
    const secondClient = readyClient();
    first.connectionChanged(firstClient, "ready");
    second.connectionChanged(secondClient, "ready");

    firstClient.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "oauth" },
    });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(firstClient)).toBe(1);
    expect(listReads(secondClient)).toBe(0);

    // A replaced client stops listening on the one that went away.
    first.connectionChanged(readyClient(), "ready");
    firstClient.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "oauth" },
    });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(firstClient)).toBe(1);
  });
});

describe("instance mutations and their own echo", () => {
  // An instance mutation's hub broadcast carries no provider - no single
  // provider/activeSource pair summarizes "the list changed" - so before this
  // correlation the originator read its own echo as another client's change and
  // refetched, and the web's ProviderConnection invalidated on that foreign
  // read. The mutation now stamps originClientId, and the echo carrying this
  // client's own id is consumed as a self-marked refresh.
  test("a mutation stamps originClientId and its provider-less own echo is self-marked, not foreign", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/instance/create", () => LISTING);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // The wire stamp itself is pinned by the sibling test below; this one is
    // about what the echo carrying it does.
    await store.getState().create({ name: "work", base: "anthropic" });

    // Watch only the echo-driven read: the create's own applied answer is a
    // listing change a flow is right to invalidate on.
    const marked = store.getState().selfRefresh;
    const foreign: boolean[] = [];
    store.subscribe((state, previous) => foreign.push(foreignListingChange(state, previous)));

    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);
    expect(foreign.some(Boolean)).toBe(false);

    // A notification naming another client is foreign however provider-less it
    // is: it refetches, and the transition is not the store's own refresh.
    const afterOwn = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-2" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(3);
    expect(store.getState().selfRefresh).toBe(afterOwn);
    expect(foreign.some(Boolean)).toBe(true);
  });

  test("a provider-less notification with no id stays foreign, not spent on an outstanding mutation", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/instance/create", () => LISTING);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    await store.getState().create({ name: "work", base: "anthropic" });
    const marked = store.getState().selfRefresh;
    const foreign: boolean[] = [];
    store.subscribe((state, previous) => foreign.push(foreignListingChange(state, previous)));

    // The server's own live-prefetch pass - or a write from an older/TUI client -
    // broadcasts with neither a provider nor an id, so it names no mutation this
    // client could correlate on and must not be read as its own echo.
    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
    expect(store.getState().selfRefresh).toBe(marked);
    expect(foreign.some(Boolean)).toBe(true);
  });

  test("an early echo spends the issuing mutation's marker, so a later refusal cannot steal a newer one", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => LISTING);
    const first = deferred<InstanceListResponse>();
    const second = deferred<InstanceListResponse>();
    let edits = 0;
    fake.on("evener/instance/edit", () => (++edits === 1 ? first.promise : second.promise));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    const a = store.getState().edit({ name: "work", baseUrl: "https://a" });
    const b = store.getState().edit({ name: "work", baseUrl: "https://b" });
    // A's echo arrives first and must spend A's marker, not B's.
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    // A is refused after its echo; retiring A's (already spent) marker must not
    // take B's.
    first.reject(new Error("refused"));
    await a.catch(() => {});
    const marked = store.getState().selfRefresh;

    // B's own echo is still this client's, so it refreshes self-marked.
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);
    second.resolve(LISTING);
    await b;
  });

  test("a stale instance marker is pruned, not stranded, so it cannot absorb a later echo", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/instance/create", () => LISTING);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // A's echo never arrives - a hub that ignores originClientId broadcasts an
    // id-less, provider-less notification, which stays foreign - so A's marker
    // is stranded until it ages out.
    await store.getState().create({ name: "work", base: "anthropic" });
    await vi.advanceTimersByTimeAsync(2500);
    // Arming B prunes A's stranded marker.
    await store.getState().create({ name: "work", base: "anthropic" });
    const marked = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);

    // Only B's marker ever existed here: a second own-id echo finds none and is
    // foreign. Were A's stale marker still around it would absorb this one.
    const afterOwn = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBe(afterOwn);
  });

  test("an in-flight marker survives the echo window, so a slow mutation's own late echo still correlates", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => LISTING);
    const slow = deferred<InstanceListResponse>();
    const fast = deferred<InstanceListResponse>();
    let edits = 0;
    fake.on("evener/instance/edit", () => (++edits === 1 ? slow.promise : fast.promise));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    // A's RPC is still outstanding when it ages past the echo window.
    const a = store.getState().edit({ name: "work", baseUrl: "https://a" });
    await vi.advanceTimersByTimeAsync(2500);
    // Arming B must not prune A's in-flight marker.
    const b = store.getState().edit({ name: "work", baseUrl: "https://b" });
    const marked = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);

    // A's response lands late and its own echo follows: it must still be read as
    // this client's. Were A pruned when B was armed, this echo would find no
    // marker and be misread as a foreign listing change.
    const afterFirstEcho = store.getState().selfRefresh;
    slow.resolve(LISTING);
    await a;
    // A's superseded answer schedules the core's own reconcile read. Let it run
    // now, so it does not coalesce with the echo's refetch below and mark that
    // read foreign.
    await vi.advanceTimersByTimeAsync(300);
    fake.emitNotification({ method: "evener/auth/updated", params: { originClientId: "tab-1" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(afterFirstEcho);

    fast.resolve(LISTING);
    await b;
  });

  test("an id-bearing echo spends the live marker, not a settled stale one", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    const first = deferred<AuthStatusResponse>();
    let sets = 0;
    fake.on("evener/auth/apiKey/set", () => (++sets === 1 ? first.promise : SIGNED_IN));
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    const aged = store.getState().setApiKey("work", API_KEY);
    first.resolve(SIGNED_IN);
    await aged;
    await vi.advanceTimersByTimeAsync(1000);
    await store.getState().setApiKey("work", API_KEY);
    await vi.advanceTimersByTimeAsync(1500); // the first marker is stale, the second live

    const marked = store.getState().selfRefresh;
    fake.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", originClientId: "tab-1" },
    });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBeGreaterThan(marked);

    // The id-bearing echo must have spent the LIVE marker (retiring the stale
    // one on the way). Had it spent the stale marker instead, the live one
    // would still be here to absorb this unrelated change as the store's own.
    const afterOwn = store.getState().selfRefresh;
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "oidc" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(store.getState().selfRefresh).toBe(afterOwn);
  });

  test("every instance mutation stamps originClientId", async () => {
    const store = createCredentialInstancesStore({ ownClientId: () => "tab-1" });
    const fake = readyClient();
    fake.on("evener/instance/create", () => LISTING);
    fake.on("evener/instance/edit", () => LISTING);
    fake.on("evener/instance/remove", () => LISTING);
    fake.on("evener/instance/setDefault", () => LISTING);
    fake.on("evener/instance/setModelDisabled", () => LISTING);
    fake.on("evener/instance/refreshModels", () => LISTING);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    await store.getState().create({ name: "work", base: "anthropic" });
    await store.getState().edit({ name: "work", newName: "work-2" });
    await store.getState().remove("work", "fp-1");
    await store.getState().setDefault("work");
    await store.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await store.getState().refreshModels("work");

    for (const method of [
      "evener/instance/create",
      "evener/instance/edit",
      "evener/instance/remove",
      "evener/instance/setDefault",
      "evener/instance/setModelDisabled",
      "evener/instance/refreshModels",
    ]) {
      const call = fake.calls.filter((candidate) => candidate.method === method).at(-1);
      expect((call?.params as { originClientId?: string } | undefined)?.originClientId, method).toBe("tab-1");
    }
  });
});
