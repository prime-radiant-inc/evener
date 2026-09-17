import { afterEach, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  InstanceListResponse,
} from "@evener/appwire-client";
import {
  type CredentialListing,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { isInstanceRemovePersisted, ProviderInstances } from "./providerInstances";

afterEach(() => {
  vi.useRealTimers();
});

// The core's normalized listing: every optional wire field present.
const listing = (
  label: string,
  writesRefused = false,
): CredentialListing => ({
  instances: [],
  availableProviders: [],
  diagnostics: [label],
  userLayer: "",
  writesRefused,
});
function boundary() {
  const handlers = new Set<(n: AnyNotification) => void>();
  const requests: { method: string; params: unknown }[] = [];
  const io = {
    request: async (_method: string, _params: unknown): Promise<unknown> =>
      listing("initial"),
  };
  const client = {
    request: (method, params) => {
      requests.push({ method, params });
      return io.request(method, params);
    },
    onNotification: (handler) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as ConversationClientLike;
  // The screen owns the store and its connection; the model drives it.
  const store = createCredentialInstancesStore({ ownClientId: () => "native-test" });
  store.connectionChanged(client, "ready");
  const model = new ProviderInstances(store);
  return { model, io, requests, handlers, store, client };
}
// Another client (or the TUI) changed a provider's credentials: the hub's
// broadcast reaches every listener on the connection.
function foreignAuthChange(handlers: Set<(n: AnyNotification) => void>, provider = "test") {
  for (const notify of handlers) notify({ method: "evener/auth/updated", params: { provider, activeSource: "oauth" } });
}
// answerProbeWith scripts the client so a credential test gets `probe`'s
// answer and every listing read reports rows changed elsewhere.
function answerProbeWith(io: { request: (method: string, params: unknown) => Promise<unknown> }, probe: () => Promise<unknown>) {
  io.request = (method) => (method === "evener/auth/test" ? probe() : Promise.resolve(listing("changed elsewhere")));
}
it("retains the last provider list after a failed refresh", async () => {
  const { model, io } = boundary();
  await model.refresh();
  io.request = async () => {
    throw new Error("offline");
  };
  await model.refresh();
  expect(model.getSnapshot().data).toEqual(listing("initial"));
  expect(model.getSnapshot().error).toContain("offline");
  expect(model.getSnapshot().loading).toBe(false);
});
it("drops a late response after leaving a hub and detaches its notifications", async () => {
  const a = boundary();
  const b = boundary();
  const pending = deferred<InstanceListResponse>();
  a.io.request = () => pending.promise;
  a.model.start();
  a.model.dispose();
  // Leaving the hub is the screen's to say: it tells the store the connection
  // closed, which detaches the store's listener from that client.
  a.store.connectionChanged(null, "closed");
  await b.model.refresh();
  pending.resolve(listing("hub A"));
  await pending.promise;
  expect(a.handlers.size).toBe(0);
  expect(a.model.getSnapshot().data).toBeNull();
  expect(b.model.getSnapshot().data).toEqual(listing("initial"));
  await expect(a.model.remove("old hub")).rejects.toThrow("closed");
  expect(a.requests).toHaveLength(1);
});
// held is a listing with rows: what a list remounted after a sign-in finds
// already in the store.
const held = { ...listing("held"), availableProviders: [{ id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true }] };
it("start publishes the rows the store already holds for this connection without a read", async () => {
  const { model, io, requests, store } = boundary();
  io.request = async () => held;
  await store.getState().fetch();
  const reads = requests.length;
  model.start();
  expect(model.getSnapshot().data).toEqual(held);
  expect(requests).toHaveLength(reads);
});
it("start adopting held rows also shows the failure a background refetch left with them", async () => {
  const { model, io, store } = boundary();
  io.request = async () => held;
  await store.getState().fetch();
  io.request = async () => {
    throw new Error("offline");
  };
  await store.getState().fetch();
  expect(store.getState().instances).toEqual(held.instances);
  model.start();
  expect(model.getSnapshot().data).toEqual(held);
  expect(model.getSnapshot().error).toBe("Could not load providers: offline");
  expect(model.getSnapshot().loading).toBe(false);
});
it("a replaced connection keeps the rows on screen until its own read lands", async () => {
  vi.useFakeTimers();
  const { model, io, requests, store, client } = boundary();
  io.request = async () => held;
  await model.refresh();
  expect(model.getSnapshot().data).toEqual(held);
  const reads = requests.length;
  const later = { ...held, diagnostics: ["after reconnect"] };
  io.request = async () => later;
  store.connectionChanged({ ...client } as typeof client, "ready");
  // No spinner, no blank: the previous connection's rows stay up.
  expect(model.getSnapshot().data).toEqual(held);
  await vi.advanceTimersByTimeAsync(0);
  expect(requests.length).toBe(reads + 1);
  expect(model.getSnapshot().data).toEqual(later);
});
it("start reads when the rows the store holds belong to a replaced connection", async () => {
  const { model, io, requests, store, client } = boundary();
  io.request = async () => held;
  await store.getState().fetch();
  store.connectionChanged({ ...client } as typeof client, "ready");
  const reads = requests.length;
  model.start();
  await vi.waitFor(() => expect(requests.length).toBe(reads + 1));
});
it("refetches when auth changes during a read instead of publishing the stale result", async () => {
  vi.useFakeTimers();
  const { model, io, handlers } = boundary();
  const pending = deferred<InstanceListResponse>();
  let reads = 0;
  io.request = () =>
    ++reads === 1 ? pending.promise : Promise.resolve(listing("updated"));
  model.start();
  // The change lands while the first read is out; that read's answer is what
  // the screen would otherwise have shown.
  foreignAuthChange(handlers);
  pending.resolve(listing("stale"));
  await vi.advanceTimersByTimeAsync(0);
  expect(model.getSnapshot().data).toEqual(listing("stale"));
  // The core's own refetch for the notification, not a caller's refresh, is
  // what replaces it.
  await vi.advanceTimersByTimeAsync(300);
  expect(model.getSnapshot().data).toEqual(listing("updated"));
  expect(reads).toBe(2);
  model.dispose();
});
it("refuses configuration writes while allowing independent credential repair", async () => {
  vi.useFakeTimers();
  const { model, io, requests } = boundary();
  io.request = async () => listing("bad provider file", true);
  await model.refresh();
  await expect(model.remove("test")).rejects.toThrow("configuration");
  await model.clearStoredKey("test");
  // The post-write read is the core's own coalesced refresh.
  await vi.advanceTimersByTimeAsync(300);
  expect(requests.map((r) => r.method)).toEqual([
    "evener/instance/list",
    "evener/auth/apiKey/clear",
    "evener/instance/list",
  ]);
});
it("does not replay a failed mutation and reconciles with a read", async () => {
  vi.useFakeTimers();
  const { model, io, requests, handlers } = boundary();
  await model.refresh();
  io.request = async (method) => {
    if (method === "evener/instance/remove") throw new Error("connection lost");
    return listing("reconciled");
  };
  await expect(model.remove("test")).rejects.toThrow("connection lost");
  expect(
    requests.filter((r) => r.method === "evener/instance/remove"),
  ).toHaveLength(1);
  // A failed reply may follow a write the hub applied; the hub's echo of that
  // write is what re-reads, never the screen.
  await vi.advanceTimersByTimeAsync(300);
  expect(requests.filter((r) => r.method === "evener/instance/list")).toHaveLength(1);
  foreignAuthChange(handlers);
  await vi.advanceTimersByTimeAsync(300);
  expect(model.getSnapshot().data).toEqual(listing("reconciled"));
  expect(model.getSnapshot().busy).toBe(false);
});
it("blocks overlapping writes and never retains the API key in its snapshot", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  io.request = (method) =>
    method === "evener/auth/apiKey/set"
      ? pending.promise
      : Promise.resolve(listing("reloaded"));
  const write = model.setApiKey("test", "fixture-key");
  await expect(model.setDefault("test")).rejects.toThrow("progress");
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-key");
  pending.resolve({});
  await write;
  expect(
    requests.find((r) => r.method === "evener/auth/apiKey/set")?.params,
  ).toEqual({ provider: "test", value: "fixture-key", originClientId: expect.stringMatching(/^native-/) });
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-key");
});
it("does not let a pre-mutation read replace the reconciled provider configuration", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const stale = deferred<InstanceListResponse>();
  let reads = 0;
  io.request = (method) =>
    method === "evener/instance/list"
      ? ++reads === 1
        ? stale.promise
        : Promise.resolve(listing("new default"))
      : Promise.resolve(listing("mutation response"));
  const read = model.refresh();
  const write = model.setDefault("test");
  await Promise.resolve();
  stale.resolve(listing("old default"));
  await Promise.all([read, write]);
  // The write's own answer is the reconciled configuration; the read that
  // started before it does not replace it.
  expect(model.getSnapshot().data).toEqual(listing("mutation response"));
  expect(model.getSnapshot().loading).toBe(false);
});
it("sanitizes credential test replies before publishing them", async () => {
  const { model, io } = boundary();
  await model.refresh();
  io.request = async () => ({
    provider: "wrong",
    status: "success",
    message: "fixture-secret",
  });
  await model.testCredentials("test");
  expect(model.getSnapshot().credentialTest?.provider).toBe("test");
  expect(model.getSnapshot().credentialTest?.pending).toBe(false);
  expect(model.getSnapshot().credentialTest?.result?.message).toBe(
    "Credentials verified.",
  );
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-secret");
});
it("discards credential results invalidated by a provider refresh", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  io.request = (method) =>
    method === "evener/auth/test"
      ? pending.promise
      : Promise.resolve(listing("changed"));
  const check = model.testCredentials("test");
  await model.refresh();
  pending.resolve({ provider: "test", status: "success", message: "" });
  await check;
  expect(model.getSnapshot().credentialTest).toBeNull();
});
it("a foreign auth change clears a shown credential test result", async () => {
  vi.useFakeTimers();
  const { model, io, handlers } = boundary();
  await model.refresh();
  answerProbeWith(io, async () => ({ provider: "test", status: "success", message: "" }));
  await model.testCredentials("test");
  expect(model.getSnapshot().credentialTest?.result?.status).toBe("success");
  // The listing the result was checked against is gone with the change.
  foreignAuthChange(handlers);
  await vi.advanceTimersByTimeAsync(300);
  expect(model.getSnapshot().data).toEqual(listing("changed elsewhere"));
  expect(model.getSnapshot().credentialTest).toBeNull();
  model.dispose();
});
it("a foreign auth change discards a credential test still in flight", async () => {
  vi.useFakeTimers();
  const { model, io, handlers } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  answerProbeWith(io, () => pending.promise);
  const check = model.testCredentials("test");
  foreignAuthChange(handlers);
  await vi.advanceTimersByTimeAsync(300);
  expect(model.getSnapshot().data).toEqual(listing("changed elsewhere"));
  pending.resolve({ provider: "test", status: "success", message: "" });
  await check;
  expect(model.getSnapshot().credentialTest).toBeNull();
  model.dispose();
});
it("does not echo credential test transport errors or retry the test", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  io.request = async () => {
    throw new Error("fixture-secret");
  };
  await model.testCredentials("test");
  expect(model.getSnapshot().credentialTest?.result?.status).toBe(
    "endpoint_failure",
  );
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-secret");
  expect(requests.filter((r) => r.method === "evener/auth/test")).toHaveLength(
    1,
  );
});

it("stores credential JSON once, reconciles a lost reply and never publishes its contents", async () => {
  vi.useFakeTimers();
  const { model, io, requests, handlers } = boundary();
  await model.refresh();
  const credential = '{"type":"authorized_user","refresh_token":"fixture-sensitive"}';
  io.request = async (method) => {
    if (method === "evener/auth/credentialJson/set") throw new Error(credential);
    return listing("reconciled");
  };
  await expect(model.setCredentialJson("vertex", credential)).rejects.toThrow();
  expect(requests.filter((request) => request.method === "evener/auth/credentialJson/set")).toEqual([
    {
      method: "evener/auth/credentialJson/set",
      params: { provider: "vertex", value: credential, originClientId: expect.stringMatching(/^native-/) },
    },
  ]);
  // The write may have landed despite the lost reply: the hub's echo, no
  // longer this screen's own (the refused write retired its marker), refetches.
  foreignAuthChange(handlers, "vertex");
  await vi.advanceTimersByTimeAsync(300);
  expect(model.getSnapshot().data).toEqual(listing("reconciled"));
  expect(JSON.stringify(model.getSnapshot())).not.toContain("fixture-sensitive");
  expect(model.getSnapshot().busy).toBe(false);
});

it("reconciles a successful credential JSON write through the authoritative list", async () => {
  vi.useFakeTimers();
  const { model, io, requests } = boundary();
  await model.refresh();
  const credential = '{"type":"authorized_user","refresh_token":"fixture"}';
  io.request = async (method) =>
    method === "evener/auth/credentialJson/set"
      ? { provider: "vertex", status: "success" }
      : listing("credential-json-saved");

  await model.setCredentialJson("vertex", credential);

  // The post-write read is the core's own coalesced refresh.
  await vi.advanceTimersByTimeAsync(300);
  expect(requests.map((request) => request.method)).toEqual([
    "evener/instance/list",
    "evener/auth/credentialJson/set",
    "evener/instance/list",
  ]);
  expect(model.getSnapshot().data).toEqual(listing("credential-json-saved"));
  expect(JSON.stringify(model.getSnapshot())).not.toContain(credential);
});

it("does not reconcile or publish after a credential write finishes after disposal", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  const pending = deferred<unknown>();
  io.request = (method) =>
    method === "evener/auth/credentialJson/set"
      ? pending.promise
      : Promise.resolve(listing("should-not-read"));

  const write = model.setCredentialJson("vertex", '{"refresh_token":"fixture"}');
  model.dispose();
  pending.resolve({ provider: "vertex", status: "success" });
  await write;

  expect(requests.map((request) => request.method)).toEqual([
    "evener/instance/list",
    "evener/auth/credentialJson/set",
  ]);
  expect(model.getSnapshot().data).toEqual(listing("initial"));
});

// The removal carries the row's endpoint fingerprint, so the hub compares the
// destination this screen showed against the one the name resolves to at the
// write. Without it the hub accepts an empty assertion and a stale screen can
// delete whatever now answers to the name (the web pane's removal sends the
// same value).
it("sends the row's endpoint fingerprint with a removal", async () => {
  const { model, io, requests } = boundary();
  io.request = async () => listing("fingerprinted");
  await model.refresh();

  await model.remove("test", "fp-test");

  const removal = requests.find((request) => request.method === "evener/instance/remove");
  expect(removal?.params).toEqual({ name: "test", expectedEndpointFingerprint: "fp-test" });
});

// A removal that stood but left a copy of the OAuth record on disk comes back as
// the hub's own discriminator (appwire.ErrorInstanceRemovePersisted). The
// instance is gone, so the model treats it as the standing removal it was:
// reconcile the listing the removal left behind and resolve with the hub's
// message for the screen to warn with - never a failed removal.
it("reconciles and reports a removal that stood with a leftover OAuth copy", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  const HUB_MESSAGE =
    "removed test, but a credential the removal set aside is still on disk: /state/auth/test.json.removing-1 (delete refused)";
  io.request = async (method) => {
    if (method === "evener/instance/remove") {
      throw new WireError(HUB_MESSAGE, -32603, {
        evenerErrorInfo: "instanceRemovePersisted",
      });
    }
    return listing("reconciled");
  };

  await expect(model.remove("test")).resolves.toEqual({
    kind: "removedPersisted",
    message: HUB_MESSAGE,
  });
  // The listing the removal left behind is re-read, so the removed row leaves it.
  expect(requests.filter((r) => r.method === "evener/instance/list")).toHaveLength(2);
  expect(model.getSnapshot().data).toEqual(listing("reconciled"));
  expect(model.getSnapshot().busy).toBe(false);
  expect(
    isInstanceRemovePersisted(
      new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: "instanceRemovePersisted" }),
    ),
  ).toBe(true);
  expect(isInstanceRemovePersisted(new WireError("refused", -32013))).toBe(false);
});

// A refusal carries no such discriminator, so it stays the plain failure it was:
// it rejects and nothing is re-read on its behalf.
it("a removal failure without the persisted discriminator still rejects and does not reconcile", async () => {
  const { model, io, requests } = boundary();
  await model.refresh();
  io.request = async (method) => {
    if (method === "evener/instance/remove") {
      throw new WireError("removing test was refused: the endpoint moved", -32013);
    }
    return listing("should-not-read");
  };

  await expect(model.remove("test")).rejects.toThrow("refused");
  expect(requests.filter((r) => r.method === "evener/instance/list")).toHaveLength(1);
  expect(model.getSnapshot().data).toEqual(listing("initial"));
});
