import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient, failing } from "../../testing/fakeClient";
import type { AuthStatusResponse } from "../../types.gen";
import { AUTH_STATUSES_REFETCH_DEBOUNCE_MS, createAuthStatusesStore } from "./authStatuses";

const LIST = "evener/auth/list";

const rejected: AuthStatusResponse = {
  provider: "lunaroute",
  supported: true,
  signedIn: true,
  activeSource: "store",
  hasStoredOAuth: false,
  error: "The provider rejected this credential (HTTP 401). Replace the key or sign in again.",
};
const fine: AuthStatusResponse = { ...rejected, provider: "work", error: undefined };

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createAuthStatusesStore(fake) };
}

describe("the hub's credential statuses (evener/auth/list)", () => {
  test("nothing is known until a read lands, then each provider's status is found by name", async () => {
    const { fake, store } = storeWithFake();
    expect(store.getState().authStatuses).toBeNull();
    fake.on(LIST, () => ({ providers: [rejected, fine] }));
    await store.getState().fetchAuthStatuses();
    expect(store.getState().authStatuses?.get("lunaroute")?.error).toBe(rejected.error);
    expect(store.getState().authStatuses?.get("work")?.error).toBeUndefined();
    expect(store.getState().authStatusesError).toBeNull();
  });

  test("a failed read keeps the statuses it had and records why", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ providers: [rejected] }));
    await store.getState().fetchAuthStatuses();
    fake.on(LIST, failing("hub unavailable"));
    await expect(store.getState().fetchAuthStatuses()).resolves.toBeUndefined();
    expect(store.getState().authStatuses?.get("lunaroute")?.error).toBe(rejected.error);
    expect(store.getState().authStatusesError).toBe("hub unavailable");
  });

  test("a hub that sends no providers (Go's nil slice) reads as none", async () => {
    const { fake, store } = storeWithFake();
    // The generated type says an array; the wire can carry null.
    fake.on(LIST, () => ({ providers: null }) as unknown as { providers: AuthStatusResponse[] });
    await store.getState().fetchAuthStatuses();
    expect(store.getState().authStatuses?.size).toBe(0);
  });
});

test("a reply that lands after reset publishes nothing", async () => {
  const { fake, store } = storeWithFake();
  let answer: (value: { providers: AuthStatusResponse[] }) => void = () => {};
  fake.on(LIST, () => new Promise((resolve) => (answer = resolve)));
  const read = store.getState().fetchAuthStatuses();
  await vi.waitFor(() => expect(fake.calls.some((call) => call.method === LIST)).toBe(true));
  store.reset();
  answer({ providers: [rejected] });
  await read;
  expect(store.getState().authStatuses).toBeNull();
});

describe("following the hub", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // A host that never asked for the statuses must not start asking on its own
  // (storeLifecycle.ts): a reconnect re-reads only what something has read.
  test("a store nothing has read reads nothing on a reconnect", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ providers: [fine] }));
    store.connectionChanged(fake, "ready");
    store.connectionChanged(fake, "idle");
    store.connectionChanged(fake, "ready");
    await vi.advanceTimersByTimeAsync(0);
    expect(fake.calls.filter((call) => call.method === LIST)).toHaveLength(0);
  });

  test("a burst of announcements is one read", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ providers: [fine] }));
    await store.getState().fetchAuthStatuses();
    store.start();
    for (let i = 0; i < 3; i++) fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(AUTH_STATUSES_REFETCH_DEBOUNCE_MS);
    expect(fake.calls.filter((call) => call.method === LIST)).toHaveLength(2);
  });

  // The hub's broadcast only reaches connected clients, so a change made
  // while this one was away is read again when the connection is back.
  test("statuses already read are read again when the connection is ready again", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "ready");
    fake.on(LIST, () => ({ providers: [fine] }));
    await store.getState().fetchAuthStatuses();
    fake.on(LIST, () => ({ providers: [rejected] }));
    store.connectionChanged(fake, "idle");
    store.connectionChanged(fake, "ready");
    await vi.advanceTimersByTimeAsync(0);
    expect(store.getState().authStatuses?.get("lunaroute")?.error).toBe(rejected.error);
  });

  // A replaced client is another hub: the statuses read from the previous one
  // go at once, before the new hub's are read, so none of its errors are shown
  // against the new hub's providers, even if that read fails.
  test("a replaced client drops the previous hub's statuses and reads the new hub's", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "ready");
    fake.on(LIST, () => ({ providers: [rejected] }));
    await store.getState().fetchAuthStatuses();

    // The store's port now reaches the new hub, whose read fails.
    fake.on(LIST, failing("hub unavailable"));
    store.connectionChanged(new FakeClient("ready"), "ready");
    expect(store.getState().authStatuses).toBeNull();
    await vi.advanceTimersByTimeAsync(0);
    // The new hub was read, and its failure keeps nothing of the previous one.
    expect(fake.calls.filter((call) => call.method === LIST)).toHaveLength(2);
    expect(store.getState()).toMatchObject({ authStatuses: null, authStatusesError: "hub unavailable" });
  });

  // The hub announces every credential change and every rejection it records
  // or clears on evener/auth/updated, so a read status list is read again.
  test("start() follows evener/auth/updated: the statuses are read again after the debounce", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ providers: [fine] }));
    await store.getState().fetchAuthStatuses();
    store.start();
    fake.on(LIST, () => ({ providers: [rejected] }));

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(AUTH_STATUSES_REFETCH_DEBOUNCE_MS);
    expect(store.getState().authStatuses?.get("lunaroute")?.error).toBe(rejected.error);
    expect(fake.calls.filter((call) => call.method === LIST)).toHaveLength(2);
  });
});
