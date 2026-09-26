import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  InstanceListResponse,
} from "@evener/appwire-client";
import { ErrorEndpointConflict, WireError } from "@evener/appwire-client";
import { createCredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import { StaleListingRefusal } from "@evener/appwire-client/state/credentials";
import { deferred } from "@evener/appwire-client/testing/deferred";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { renderHook } from "./renderNative.testkit";
import { useProviderSurface } from "./providerSurface";

// useProviderSurface holds the screen's write gate (one write at a time,
// configuration refused while the listing refuses it or holds a replaced
// connection's rows), its unmount lifetime guard, and its sanitized
// single-flight credential probe. This suite drives those rules directly;
// ProvidersScreen.test.tsx mounts the screen over the same render harness.

afterEach(() => {
  vi.useRealTimers();
});

const row = {
  name: "work",
  providerId: "anthropic",
  protocol: "https",
  auth: "apiKey",
  implicit: false,
  isDefault: true,
  activeSource: "store",
  hasStoredOAuth: false,
  credentialRequired: true,
};
const listing = (
  diagnostics: string[],
  writesRefused = false,
): InstanceListResponse => ({
  instances: [row],
  availableProviders: [],
  diagnostics,
  writesRefused,
});

function boundary() {
  const handlers = new Set<(n: AnyNotification) => void>();
  const requests: { method: string; params: unknown }[] = [];
  const io: { request: (method: string, params: unknown) => Promise<unknown> } =
    { request: async () => listing(["initial"]) };
  const client = {
    request: (method: string, params: unknown) => {
      requests.push({ method, params });
      return io.request(method, params);
    },
    onNotification: (handler: (n: AnyNotification) => void) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as ConversationClientLike;
  const store = createCredentialInstancesStore({
    ownClientId: () => "native-test",
  });
  store.connectionChanged(client, "ready");
  return { io, requests, handlers, store, client };
}

// Another client (or the TUI) changed a provider's credentials: the hub's
// broadcast reaches every listener on the connection.
function foreignAuthChange(
  handlers: Set<(n: AnyNotification) => void>,
  provider = "work",
) {
  for (const notify of handlers)
    notify({
      method: "evener/auth/updated",
      params: { provider, activeSource: "oauth" },
    });
}

// A connection whose reads fail: the store keeps the previous connection's rows
// (listingFromPreviousConnection) with loading settled, which is the state the
// stale gate answers. It records its requests, so a re-read through it is
// observable.
function failingConnection(
  requests: { method: string; params: unknown }[],
): ConversationClientLike {
  return {
    request: (method: string, params: unknown) => {
      requests.push({ method, params });
      return Promise.reject(new Error("offline"));
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
}

it("sanitizes a credential probe reply and never publishes the wire text", async () => {
  const { io, store } = boundary();
  await store.getState().fetch();
  io.request = async () => ({
    provider: "wrong",
    status: "success",
    message: "fixture-secret",
  });
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.provider).toBe("work");
  expect(result.current.credentialTest?.pending).toBe(false);
  expect(result.current.credentialTest?.result?.message).toBe(
    "Credentials verified.",
  );
  expect(JSON.stringify(result.current.credentialTest)).not.toContain(
    "fixture-secret",
  );
});

it("runs one credential probe at a time", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const pending = deferred<unknown>();
  io.request = (method: string) =>
    method === "evener/auth/test"
      ? pending.promise
      : Promise.resolve(listing(["changed"]));
  const { result } = renderHook(() => useProviderSurface(store));
  let first!: Promise<void>;
  await act(async () => {
    first = result.current.testCredentials("work");
  });
  await act(async () => {
    await result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.pending).toBe(true);
  pending.resolve({ provider: "work", status: "success", message: "" });
  await act(async () => {
    await first;
  });
  expect(
    requests.filter((request) => request.method === "evener/auth/test"),
  ).toHaveLength(1);
});

it("retires a probe result when a foreign listing change moves the rows", async () => {
  vi.useFakeTimers();
  const { io, store, handlers } = boundary();
  await store.getState().fetch();
  io.request = async (method: string) =>
    method === "evener/auth/test"
      ? { provider: "work", status: "success", message: "" }
      : listing(["changed elsewhere"]);
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.result?.status).toBe("success");
  await act(async () => {
    foreignAuthChange(handlers);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(result.current.credentialTest).toBeNull();
});

it("discards a probe still in flight when a foreign listing change moves the rows", async () => {
  vi.useFakeTimers();
  const { io, store, handlers } = boundary();
  await store.getState().fetch();
  const pending = deferred<unknown>();
  io.request = (method: string) =>
    method === "evener/auth/test"
      ? pending.promise
      : Promise.resolve(listing(["changed elsewhere"]));
  const { result } = renderHook(() => useProviderSurface(store));
  let probe!: Promise<void>;
  await act(async () => {
    probe = result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.pending).toBe(true);
  await act(async () => {
    foreignAuthChange(handlers);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  pending.resolve({ provider: "work", status: "success", message: "" });
  await act(async () => {
    await probe;
  });
  expect(result.current.credentialTest).toBeNull();
});

it("does not echo a probe transport error and does not retry it", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  io.request = async () => {
    throw new Error("fixture-secret");
  };
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.result?.status).toBe(
    "endpoint_failure",
  );
  expect(JSON.stringify(result.current.credentialTest)).not.toContain(
    "fixture-secret",
  );
  expect(
    requests.filter((request) => request.method === "evener/auth/test"),
  ).toHaveLength(1);
});

it("treats a probe refused for a replaced connection's rows as a changed connection", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  store.connectionChanged(failingConnection(requests), "ready");
  await vi.waitFor(() => expect(store.getState().loading).toBe(false));
  expect(store.getState().listingFromPreviousConnection).toBe(true);
  const { result } = renderHook(() => useProviderSurface(store));
  // Let the mount read settle before counting: only the probe's own recovery
  // may add a read.
  await act(async () => {});
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  await act(async () => {
    await result.current.testCredentials("work");
  });
  // Not a failed test: the probe is dropped and the listing re-read.
  expect(result.current.credentialTest).toBeNull();
  expect(requests.some((request) => request.method === "evener/auth/test")).toBe(
    false,
  );
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
});

it("a pull-to-refresh retires a shown probe and re-reads the listing", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  io.request = async (method: string) =>
    method === "evener/auth/test"
      ? { provider: "work", status: "success", message: "" }
      : listing(["refreshed"]);
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await result.current.testCredentials("work");
  });
  expect(result.current.credentialTest?.result?.status).toBe("success");
  await act(async () => {
    result.current.refresh();
  });
  expect(result.current.credentialTest).toBeNull();
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBeGreaterThanOrEqual(2);
});

it("allows one write at a time", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const pending = deferred<unknown>();
  io.request = (method: string) =>
    method === "evener/instance/remove"
      ? pending.promise
      : Promise.resolve(listing(["reconciled"]));
  const { result } = renderHook(() => useProviderSurface(store));
  let first!: Promise<boolean>;
  await act(async () => {
    first = result.current.remove("work");
  });
  await act(async () => {
    await expect(result.current.remove("work")).rejects.toThrow("progress");
  });
  expect(
    requests.filter((request) => request.method === "evener/instance/remove"),
  ).toHaveLength(1);
  pending.resolve(listing(["done"]));
  await act(async () => {
    await first;
  });
  expect(result.current.busy).toBe(false);
});

it("refuses configuration writes while the listing refuses them", async () => {
  const { io, store, requests } = boundary();
  io.request = async () => listing(["refused"], true);
  await store.getState().fetch();
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await expect(result.current.remove("work")).rejects.toThrow(
      "configuration",
    );
  });
  expect(requests.some((request) => request.method === "evener/instance/remove")).toBe(
    false,
  );
  // Independent credential repair is still allowed.
  io.request = async () => listing(["ok"]);
  await act(async () => {
    await result.current.clearStoredKey("work");
  });
  expect(
    requests.some((request) => request.method === "evener/auth/apiKey/clear"),
  ).toBe(true);
});

it("refuses configuration writes on a replaced connection's rows", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  store.connectionChanged(failingConnection(requests), "ready");
  await vi.waitFor(() => expect(store.getState().loading).toBe(false));
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    // The store's own refusal, so the caller names the changed connection
    // rather than reporting an unconfirmed write.
    await expect(result.current.remove("work")).rejects.toBeInstanceOf(
      StaleListingRefusal,
    );
  });
  expect(
    requests.some((request) => request.method === "evener/instance/remove"),
  ).toBe(false);
});

it("stops writing after the screen unmounts", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const { result, unmount } = renderHook(() => useProviderSurface(store));
  unmount();
  await expect(result.current.remove("work")).rejects.toThrow("closed");
  expect(
    requests.some((request) => request.method === "evener/instance/remove"),
  ).toBe(false);
});

it("asserts the endpoint fingerprint on credential writes, removal, and the probe", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    await result.current.setApiKey("work", "fixture-key", "fp-1");
  });
  await act(async () => {
    await result.current.remove("work", "fp-1");
  });
  await act(async () => {
    await result.current.testCredentials("work", "fp-1");
  });
  expect(
    requests.find((request) => request.method === "evener/auth/apiKey/set")
      ?.params,
  ).toMatchObject({
    provider: "work",
    value: "fixture-key",
    expectedEndpointFingerprint: "fp-1",
  });
  expect(
    requests.find((request) => request.method === "evener/instance/remove")
      ?.params,
  ).toMatchObject({ name: "work", expectedEndpointFingerprint: "fp-1" });
  expect(
    requests.find((request) => request.method === "evener/auth/test")?.params,
  ).toMatchObject({ provider: "work", expectedEndpointFingerprint: "fp-1" });
});

it("drops a probe whose asserted endpoint the hub refused, not reporting a failure", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  io.request = async (method: string) => {
    if (method === "evener/auth/test")
      throw new WireError("conflict", -32013, {
        evenerErrorInfo: ErrorEndpointConflict,
      });
    return listing(["reanchored"]);
  };
  const { result } = renderHook(() => useProviderSurface(store));
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  await act(async () => {
    await expect(
      result.current.testCredentials("work", "fp-1"),
    ).rejects.toBeInstanceOf(WireError);
  });
  expect(result.current.credentialTest).toBeNull();
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBeGreaterThan(reads);
});

it("returns the core's applied verdict: a superseded instance write resolves false", async () => {
  const { io, store } = boundary();
  await store.getState().fetch();
  const pendingRemove = deferred<InstanceListResponse>();
  io.request = (method: string) =>
    method === "evener/instance/remove"
      ? pendingRemove.promise
      : Promise.resolve(listing(["newer"]));
  const { result } = renderHook(() => useProviderSurface(store));
  let applied!: Promise<boolean>;
  await act(async () => {
    applied = result.current.remove("work");
  });
  // A read that starts after the write supersedes its answer.
  await act(async () => {
    await store.getState().fetch();
  });
  pendingRemove.resolve(listing(["removed"]));
  await act(async () => {
    expect(await applied).toBe(false);
  });
});

it("ignores a retired probe's endpoint conflict and leaves the newer probe alone", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const firstProbe = deferred<unknown>();
  const secondProbe = deferred<unknown>();
  let probes = 0;
  io.request = (method: string) => {
    if (method === "evener/auth/test") {
      probes += 1;
      return probes === 1 ? firstProbe.promise : secondProbe.promise;
    }
    return Promise.resolve(listing(["reanchored"]));
  };
  const { result } = renderHook(() => useProviderSurface(store));
  let retired!: Promise<void>;
  await act(async () => {
    retired = result.current.testCredentials("work", "fp-1");
  });
  expect(result.current.credentialTest?.pending).toBe(true);
  // A listing read retires probe A.
  await act(async () => {
    await store.getState().fetch();
  });
  expect(result.current.credentialTest).toBeNull();
  let current!: Promise<void>;
  await act(async () => {
    current = result.current.testCredentials("work", "fp-1");
  });
  expect(result.current.credentialTest?.pending).toBe(true);
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  // A's reply arrives after it was retired: an endpoint conflict. It must not
  // re-read (that would retire B) or surface an error.
  firstProbe.reject(
    new WireError("conflict", -32013, { evenerErrorInfo: "conflict" }),
  );
  await act(async () => {
    await retired.catch(() => {});
  });
  expect(result.current.credentialTest?.pending).toBe(true);
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads);
  secondProbe.resolve({ provider: "work", status: "success", message: "" });
  await act(async () => {
    await current;
  });
  expect(result.current.credentialTest?.result?.status).toBe("success");
});

it("adopts a listing the store already holds without re-reading on mount", async () => {
  const { io, store, requests } = boundary();
  io.request = async () => listing(["held"]);
  await store.getState().fetch();
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  renderHook(() => useProviderSurface(store));
  await act(async () => {});
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads);
  expect(store.getState().diagnostics).toEqual(["held"]);
});

it("reads a listing on mount when the store holds none", async () => {
  const { io, store, requests } = boundary();
  io.request = async () => listing(["fresh"]);
  renderHook(() => useProviderSurface(store));
  await act(async () => {});
  expect(
    requests.filter((request) => request.method === "evener/instance/list"),
  ).toHaveLength(1);
});

it("reconciles a superseded instance write with the core's own scheduled read", async () => {
  vi.useFakeTimers();
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const pendingRemove = deferred<InstanceListResponse>();
  io.request = (method: string) =>
    method === "evener/instance/remove"
      ? pendingRemove.promise
      : Promise.resolve(listing(["newer"]));
  const { result } = renderHook(() => useProviderSurface(store));
  let applied!: Promise<boolean>;
  await act(async () => {
    applied = result.current.remove("work");
  });
  // A read that starts after the write supersedes its answer.
  await act(async () => {
    await store.getState().fetch();
  });
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  pendingRemove.resolve(listing(["removed"]));
  await act(async () => {
    expect(await applied).toBe(false);
  });
  // The credential core schedules the reconcile read itself.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
});

it("chains a superseded write's reconcile after an in-flight read settles", async () => {
  vi.useFakeTimers();
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const write = deferred<unknown>();
  const inFlight = deferred<InstanceListResponse>();
  io.request = (method: string) =>
    method === "evener/instance/remove" ? write.promise : inFlight.promise;
  const { result } = renderHook(() => useProviderSurface(store));
  let applied!: Promise<boolean>;
  await act(async () => {
    applied = result.current.remove("work");
  });
  // A read starts after the write and is still in flight.
  let read!: Promise<boolean>;
  await act(async () => {
    read = store.getState().fetch();
  });
  expect(store.getState().loading).toBe(true);
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  write.resolve(listing(["written"]));
  await act(async () => {
    expect(await applied).toBe(false);
  });
  // The core scheduled its own reconcile read; it has not run yet.
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads);
  io.request = async () => listing(["reconciled"]);
  inFlight.resolve(listing(["stale"]));
  await act(async () => {
    await read;
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
  expect(store.getState().diagnostics).toEqual(["reconciled"]);
});

it("re-derives an in-flight write on a remount, so it still gates the new screen", async () => {
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const pending = deferred<unknown>();
  io.request = (method: string) =>
    method === "evener/instance/remove"
      ? pending.promise
      : Promise.resolve(listing(["reconciled"]));
  const first = renderHook(() => useProviderSurface(store));
  let inFlight!: Promise<boolean>;
  await act(async () => {
    inFlight = first.result.current.remove("work");
  });
  expect(first.result.current.busy).toBe(true);
  // A connection change remounts the list; the store (and its gate) persist.
  first.unmount();
  const second = renderHook(() => useProviderSurface(store));
  expect(second.result.current.busy).toBe(true);
  await act(async () => {
    await expect(second.result.current.remove("work")).rejects.toThrow(
      "progress",
    );
  });
  expect(
    requests.filter((request) => request.method === "evener/instance/remove"),
  ).toHaveLength(1);
  pending.resolve(listing(["done"]));
  await act(async () => {
    await inFlight;
  });
  expect(second.result.current.busy).toBe(false);
});

it("does not start a second read while the store's restore read is in flight", async () => {
  const { io, store, requests } = boundary();
  io.request = async () => listing(["initial"]);
  await store.getState().fetch();
  // A replacement connection starts the store's own restore read, still out.
  const restore = deferred<InstanceListResponse>();
  const later = {
    request: (method: string, params: unknown) => {
      requests.push({ method, params });
      return restore.promise;
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
  store.connectionChanged(later, "ready");
  expect(store.getState().loading).toBe(true);
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {});
  // The mount must coalesce with the restore read, not supersede it.
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads);
  restore.resolve(listing(["restored"]));
  await act(async () => {});
  expect(result.current.busy).toBe(false);
});

it("a refresh coalesces with an in-flight restore read and re-reads once it settles", async () => {
  const { io, store, requests } = boundary();
  io.request = async () => listing(["initial"]);
  await store.getState().fetch();
  const restore = deferred<InstanceListResponse>();
  const later = {
    request: (method: string, params: unknown) => {
      requests.push({ method, params });
      return restore.promise;
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
  store.connectionChanged(later, "ready");
  expect(store.getState().loading).toBe(true);
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  const { result } = renderHook(() => useProviderSurface(store));
  await act(async () => {
    result.current.refresh();
  });
  // The in-flight restore is this screen's read too.
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads);
  restore.resolve(listing(["restored"]));
  await act(async () => {});
  await act(async () => {});
  // The deferred refresh runs once the restore settles.
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
});

it("performs a superseded write's reconcile after a remount once the read settles", async () => {
  vi.useFakeTimers();
  const { io, store, requests } = boundary();
  await store.getState().fetch();
  const write = deferred<unknown>();
  const inFlight = deferred<InstanceListResponse>();
  io.request = (method: string) =>
    method === "evener/instance/remove" ? write.promise : inFlight.promise;
  const first = renderHook(() => useProviderSurface(store));
  let applied!: Promise<boolean>;
  await act(async () => {
    applied = first.result.current.remove("work");
  });
  let read!: Promise<boolean>;
  await act(async () => {
    read = store.getState().fetch();
  });
  expect(store.getState().loading).toBe(true);
  // The list remounts while the write is still out; its response is superseded.
  first.unmount();
  renderHook(() => useProviderSurface(store));
  write.resolve(listing(["written"]));
  await act(async () => {
    expect(await applied).toBe(false);
  });
  const reads = requests.filter(
    (request) => request.method === "evener/instance/list",
  ).length;
  io.request = async () => listing(["reconciled"]);
  inFlight.resolve(listing(["stale"]));
  await act(async () => {
    await read;
  });
  // The core's scheduled read — not the unmounted hook's — reconciles it.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(
    requests.filter((request) => request.method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
  expect(store.getState().diagnostics).toEqual(["reconciled"]);
});
