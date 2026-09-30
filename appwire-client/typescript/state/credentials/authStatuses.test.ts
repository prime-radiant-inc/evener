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

describe("following the hub", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
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
