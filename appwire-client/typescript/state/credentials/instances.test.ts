import { afterEach, describe, expect, test, vi } from "vitest";
import { CONNECTION_REPLACED_ERROR } from "../../credentialLabels";
import { deferred } from "../../testing/deferred";
import { FakeClient } from "../../testing/fakeClient";
import type { InstanceEntry, InstanceListResponse } from "../../types.gen";
import { createCredentialInstancesStore, isStaleListingRefusal, staleListingHeld } from "./instances";

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
    const first = createCredentialInstancesStore();
    const second = createCredentialInstancesStore();
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
    const first = createCredentialInstancesStore();
    const second = createCredentialInstancesStore();
    const secondClient = readyClient();
    first.connectionChanged(readyClient(), "ready");
    second.connectionChanged(secondClient, "ready");
    await first.getState().fetch();
    await second.getState().fetch();

    // A refresh in the second store is out while the FIRST store lands a write
    // on the same instance name: only the first store's count moved, so the
    // second store's refresh still applies.
    const pending = deferred<InstanceListResponse>();
    secondClient.on("evener/instance/refreshModels", () => pending.promise);
    const refresh = second.getState().refreshModels("work");
    first.noteLandedMutation("work");
    pending.resolve({ ...LISTING, instances: [{ ...WORK, models: [{ id: "m1" }] }] });
    await refresh;
    expect(second.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1"]);
    expect(first.getState().instances[0]?.models).toBeUndefined();
  });
});

describe("listing reads and writes", () => {
  test("only the most recently started request replaces the listing", async () => {
    const store = createCredentialInstancesStore();
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
    const store = createCredentialInstancesStore();
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
    const store = createCredentialInstancesStore();
    const fake = readyClient();
    const renamed = { ...LISTING, instances: [{ ...WORK, isDefault: false }] };
    fake.on("evener/instance/setDefault", () => renamed);
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    expect(await store.getState().setDefault("work")).toBe(true);
    expect(store.getState().instances[0]?.isDefault).toBe(false);
    expect(fake.calls.at(-1)).toEqual({ method: "evener/instance/setDefault", params: { name: "work" } });
  });

  test("a write from a replaced connection's listing is refused with the shared words until this connection reads", async () => {
    const store = createCredentialInstancesStore();
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
    expect(() => store.requireWritableClient()).toThrow(CONNECTION_REPLACED_ERROR);
    expect(replacement.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(0);

    restore.resolve(LISTING);
    await vi.waitFor(() => expect(store.getState().listingFromPreviousConnection).toBe(false));
    expect(await store.getState().setDefault("work")).toBe(true);
  });

  test("a same-client state transition keeps the rows this client's", async () => {
    const store = createCredentialInstancesStore();
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
    const store = createCredentialInstancesStore();
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    await store.getState().fetch();

    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    await vi.waitFor(() => expect(listReads(fake)).toBe(2));
  });

  test("a store that never read does not fetch on ready", async () => {
    const store = createCredentialInstancesStore();
    const fake = readyClient();
    store.connectionChanged(fake, "ready");
    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    await Promise.resolve();
    expect(fake.calls).toHaveLength(0);
  });

  test("scheduleRefetch coalesces into one read, self-marked only when every request was self", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore();
    const fake = readyClient();
    store.connectionChanged(fake, "ready");

    store.scheduleRefetch(true);
    store.scheduleRefetch(true);
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(1);
    // A self read stamps the marker on each transition it touches.
    const marked = store.getState().selfRefresh;
    expect(marked).toBeGreaterThan(0);

    store.scheduleRefetch(true);
    store.scheduleRefetch(false);
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
    expect(store.getState().selfRefresh).toBe(marked);

    // A connection change cancels a pending refetch.
    store.scheduleRefetch(false);
    store.connectionChanged(readyClient(), "ready");
    await vi.advanceTimersByTimeAsync(300);
    expect(listReads(fake)).toBe(2);
  });
});

describe("the adapter seam", () => {
  test("extend adds store-bound methods to the state the view layer selects over", async () => {
    const store = createCredentialInstancesStore({
      extend: (seam) => ({
        async probe(): Promise<string> {
          seam.requireWritableClient();
          return "ok";
        },
      }),
    });
    store.connectionChanged(readyClient(), "ready");
    expect(await store.getState().probe()).toBe("ok");
    expect(store.getInitialState().probe).toBe(store.getState().probe);
  });

  test("resetForTests returns the listing to empty and drops the connection", async () => {
    const store = createCredentialInstancesStore();
    store.connectionChanged(readyClient(), "ready");
    await store.getState().fetch();
    store.resetForTests();
    expect(store.getState().instances).toEqual([]);
    await expect(store.getState().fetch()).rejects.toThrow(/no client connected/);
  });
});
