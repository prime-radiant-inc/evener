import { afterEach, describe, expect, test, vi } from "vitest";
import { CONNECTION_REPLACED_ERROR } from "../../credentialLabels";
import { errorText, friendlyErrorMessage, sessionActionError } from "../../errors";
import { deferred } from "../../testing/deferred";
import { FakeClient } from "../../testing/fakeClient";
import type { AuthStatusResponse, InstanceEntry, InstanceListResponse } from "../../types.gen";
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

  test("a connection change cancels a pending refetch", async () => {
    vi.useFakeTimers();
    const store = createCredentialInstancesStore();
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
    const store = createCredentialInstancesStore();
    store.connectionChanged(readyClient(), "ready");
    await store.getState().fetch();
    store.resetForTests();
    expect(store.getState().instances).toEqual([]);
    await expect(store.getState().fetch()).rejects.toThrow(/no client connected/);
  });
});

// Sentinel secret material: distinctive enough that a substring search over
// state, error text and console output is a real assertion.
const API_KEY = "sk-fixture-SECRET-KEY-9f3a7c";
const CREDENTIAL_JSON = '{"type":"authorized_user","refresh_token":"fixture-REFRESH-SECRET-51d"}';
const SIGNED_IN: AuthStatusResponse = {
  provider: "work",
  supported: true,
  signedIn: true,
  activeSource: "api_key",
  hasStoredOAuth: false,
  hasStoredFile: true,
};

function nowhere(haystack: string, sentinels: string[] = [API_KEY, CREDENTIAL_JSON, "REFRESH-SECRET", "SECRET-KEY"]) {
  for (const secret of sentinels) expect(haystack).not.toContain(secret);
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

describe("credential mutations", () => {
  test("a listing read in flight when a credential write is issued cannot publish after it", async () => {
    const store = createCredentialInstancesStore();
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
    await vi.waitFor(() => expect(store.getState().instances).toEqual([WORK]));
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

  test("a notification on one store's client never refetches another store", async () => {
    vi.useFakeTimers();
    const first = createCredentialInstancesStore();
    const second = createCredentialInstancesStore();
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

  test("without a client identity the mutation carries no originClientId", async () => {
    const store = createCredentialInstancesStore();
    const fake = readyClient();
    fake.on("evener/auth/apiKey/set", () => SIGNED_IN);
    store.connectionChanged(fake, "ready");
    await store.getState().setApiKey("work", API_KEY);
    expect(fake.calls.at(-1)).toEqual({
      method: "evener/auth/apiKey/set",
      params: { provider: "work", value: API_KEY },
    });
  });
});
