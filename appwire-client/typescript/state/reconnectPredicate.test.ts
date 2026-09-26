// The reconnect predicate two encodings must keep agreeing on.
//
// The hub broadcasts a change to every CONNECTED client, so a change made
// while a client was away reaches it as nothing at all: the notification is
// not a recovery path and the connection becoming ready again is. Both state
// layers decide when to act on that, and they decide it separately -
// state/credentials/instances.ts in connectionChanged (requestedList, and a
// transition it keys on the client as well as the state), and
// state/extensions/storeLifecycle.ts in the lifecycle every extensions store
// wraps. Neither can adopt the other: #1558 measured the move at +21 lines and
// a generalization of the shared helper that exists for one user.
//
// So this file is the contract instead. It drives both cores through the same
// connection scripts and asserts the same decision from each. A change to one
// encoding that is not made to the other fails here.
//
// It asks only "was the list read", never which connection carried the read:
// the credentials core holds its client and swaps it on connectionChanged,
// while an extensions store keeps the client (or port) it was built with and
// is told about the connection only so it can decide. That difference is the
// hosts' business, not the predicate's.

import { describe, expect, test, vi } from "vitest";
import type { ConnectionState } from "../client";
import { FakeClient } from "../testing/fakeClient";
import { createCredentialInstancesStore } from "./credentials/instances";
import { createPluginsStore, PLUGIN_REFETCH_DEBOUNCE_MS } from "./extensions/plugins";

/** One core reduced to what the predicate acts on, over clients this harness
 * owns so a read can be counted wherever it was sent. */
interface Encoding {
  /** A connection to bind, scripted to answer the list. */
  client(): FakeClient;
  bind(client: FakeClient | null, state: ConnectionState): void;
  read(): Promise<unknown>;
  /** The notification that schedules a debounced read, on the bound client. */
  notify(client: FakeClient): void;
  /** List reads sent over every client this harness has handed out. */
  reads(): number;
}

function credentials(): Encoding {
  const clients: FakeClient[] = [];
  const store = createCredentialInstancesStore({ ownClientId: () => "parity" });
  return {
    client() {
      const fake = new FakeClient("ready");
      fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
      clients.push(fake);
      return fake;
    },
    bind: (client, state) => store.connectionChanged(client, state),
    read: () => store.getState().fetch(),
    notify: (client) => client.emitNotification({ method: "evener/auth/updated", params: { provider: "anthropic" } }),
    reads: () => clients.reduce((n, c) => n + c.calls.filter((x) => x.method === "evener/instance/list").length, 0),
  };
}

function extensions(): Encoding {
  const clients: FakeClient[] = [];
  let store: ReturnType<typeof createPluginsStore> | undefined;
  return {
    client() {
      const fake = new FakeClient("ready");
      fake.on("evener/plugin/list", () => ({ plugins: [] }));
      clients.push(fake);
      // An extensions store is built over one client and never swaps it; its
      // host builds a new store (native) or hands it a port that resolves the
      // current one (web). The first client this harness hands out is the one
      // it is built over.
      store ??= createPluginsStore(fake);
      store.start();
      return fake;
    },
    bind: (client, state) => store?.connectionChanged(client, state),
    read: () => store?.getState().fetchPlugins() ?? Promise.resolve(),
    notify: (client) => client.emitNotification({ method: "evener/plugin/updated", params: {} }),
    reads: () => clients.reduce((n, c) => n + c.calls.filter((x) => x.method === "evener/plugin/list").length, 0),
  };
}

// Both cores debounce their notification read by the same window; asserted
// rather than assumed, because a window that had drifted apart would make the
// third case below pass for the wrong reason.
const DEBOUNCE_MS = PLUGIN_REFETCH_DEBOUNCE_MS;

describe.each([
  ["credentials", credentials],
  ["extensions", extensions],
])("the reconnect predicate, as %s encodes it", (_name, build) => {
  test("a list nothing has read is not read by a connection becoming ready", async () => {
    const core = build();
    core.bind(core.client(), "ready");
    await vi.waitFor(() => expect(core.reads()).toBe(0));
  });

  test("a list that has been read is read again when the connection is ready again", async () => {
    const core = build();
    const hub = core.client();
    core.bind(hub, "ready");
    await core.read();
    core.bind(hub, "reconnecting");
    core.bind(hub, "ready");
    await vi.waitFor(() => expect(core.reads()).toBe(2));
  });

  test("a connection update that changes nothing reads nothing and cancels nothing", async () => {
    vi.useFakeTimers();
    try {
      const core = build();
      const hub = core.client();
      core.bind(hub, "ready");
      await core.read();

      // The read the notification scheduled is about a change no recovery read
      // would replace, so the metadata-only update - the handshake's
      // serverInfo and features, on the client and state the core already has
      // - must neither send its own read nor drop that one.
      core.notify(hub);
      core.bind(hub, "ready");
      expect(core.reads()).toBe(1);
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
      expect(core.reads()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  test("a client that replaces the one the list was read through is read again", async () => {
    const core = build();
    core.bind(core.client(), "ready");
    await core.read();
    core.bind(core.client(), "ready");
    await vi.waitFor(() => expect(core.reads()).toBe(2));
  });
});
