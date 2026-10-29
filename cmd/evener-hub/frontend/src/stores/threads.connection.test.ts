import type { ConnectionState, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectFakeClient } from "../testing/connectionTestUtils";
import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import { connectionStore, useConnectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { resetThreadsStoreForTests } from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `ordinary-connection-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

describe("FakeClient", () => {
  test("request() rejects while not ready, mirroring AppwireClient's own ready-gate, without recording the call", async () => {
    const fake = new FakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a"));

    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: true })).rejects.toThrow(
      /cannot call "thread\/read" while state is "connecting"/,
    );
    expect(fake.calls).toHaveLength(0); // never "sent" — the real client never reaches socket.send() in this case either
  });
});

describe("useConnectionStore", () => {
  // Counts BOTH halves of the listener lifecycle. A live-listener count alone
  // cannot tell "connect returned early" apart from "connect detached and
  // re-attached" — both leave exactly one — so the idempotency check needs the
  // registration and unsubscribe tallies to mean anything.
  class CountingClient extends FakeClient {
    registrations = 0;
    unsubscribes = 0;
    override onStateChange(cb: (s: ConnectionState) => void): () => void {
      this.registrations++;
      const detach = super.onStateChange(cb);
      return () => {
        this.unsubscribes++;
        detach();
      };
    }
  }

  test("connect captures the client's current state immediately", () => {
    const fake = new FakeClient("connecting");
    connectionStore.getState().connect(fake);
    expect(connectionStore.getState().state).toBe("connecting");
  });

  test("connect mirrors every subsequent client state change", () => {
    const fake = connectFakeClient("idle");
    fake.emitStateChange("connecting");
    expect(connectionStore.getState().state).toBe("connecting");
    fake.emitStateChange("ready");
    expect(connectionStore.getState().state).toBe("ready");
    fake.emitStateChange("reconnecting");
    expect(connectionStore.getState().state).toBe("reconnecting");
  });

  test("connect is idempotent: a second call with the same client does not double-subscribe", () => {
    const fake = new CountingClient("idle");
    connectionStore.getState().connect(fake);
    connectionStore.getState().connect(fake); // second call, same client instance

    // Assert the early return itself. Counting live listeners (or setState
    // calls) cannot: once connect() detaches the previous listener before
    // wiring a replacement, dropping the early return still leaves exactly
    // one listener, and the assertion passes against the broken code.
    expect(fake.registrations).toBe(1);
    expect(fake.unsubscribes).toBe(0);

    const setStateSpy = vi.spyOn(connectionStore, "setState");
    fake.emitStateChange("connecting");
    expect(setStateSpy).toHaveBeenCalledTimes(1); // one onStateChange listener, not two
  });

  // Wiring adopts an already-ready client's cached metadata. AppShell owns
  // the actual dial, so this attachment still never calls client.connect().
  test("connect adopts cached serverInfo without calling the client's own connect()", () => {
    const connect = vi.spyOn(FakeClient.prototype, "connect");
    const fake = connectFakeClient();
    expect(connectionStore.getState().serverInfo).toBeDefined();
    expect(connectionStore.getState().serverInfo).toBe(fake.initializeResult?.serverInfo);
    expect(connect).not.toHaveBeenCalled();
  });

  test("hook reflects store state and updates on change", () => {
    const fake = connectFakeClient("idle");
    const { result } = renderHook(() => useConnectionStore((s) => s.state));
    expect(result.current).toBe("idle");

    act(() => {
      fake.emitStateChange("ready");
    });
    expect(result.current).toBe("ready");
  });

  test("a replaced client's later state cannot overwrite the current client", () => {
    const a = connectFakeClient("ready");
    const b = new FakeClient("ready");
    connectionStore.getState().connect(b);

    a.emitStateChange("closed");

    expect(connectionStore.getState().client).toBe(b);
    expect(connectionStore.getState().state).toBe("ready");

    // The fence must not over-fire: the client that replaced a still owns the
    // store and its own transitions have to land.
    b.emitStateChange("reconnecting");
    expect(connectionStore.getState().state).toBe("reconnecting");
  });

  // The detach half, proven on its own. The identity check below would keep
  // the store correct even without this, so nothing else here fails when the
  // unsubscribe is dropped — and a replaced client would then keep a live
  // subscription for the rest of the page's life.
  test("wiring a replacement invokes the outgoing client's unsubscribe", () => {
    const outgoing = new CountingClient("ready");
    connectionStore.getState().connect(outgoing);
    expect(outgoing.unsubscribes).toBe(0);

    connectionStore.getState().connect(new FakeClient("ready"));
    expect(outgoing.unsubscribes).toBe(1);
  });

  // A subscriber can call connect() from inside the synchronous setState
  // dispatch of an outer connect(). The inner frame completes first and owns
  // the module slot; if the outer frame then overwrote it, the inner client's
  // subscription would never be detachable again.
  test("a connect re-entered during publication keeps the inner client's unsubscribe", () => {
    const outer = new CountingClient("ready");
    const inner = new CountingClient("ready");

    const stopWatching = connectionStore.subscribe(() => {
      if (connectionStore.getState().client === outer) {
        connectionStore.getState().connect(inner);
      }
    });
    connectionStore.getState().connect(outer);
    stopWatching();

    expect(connectionStore.getState().client).toBe(inner);
    // The outer frame must retire its own listener rather than clobber the
    // slot, so replacing inner later still detaches inner.
    connectionStore.getState().connect(new FakeClient("ready"));
    expect(inner.unsubscribes).toBe(1);
  });

  // The real client transitions synchronously inside connect()/close(), so a
  // subscriber that drives it during publication must not have its transition
  // dropped on the floor.
  test("a state change during publication is not lost", () => {
    const client = new FakeClient("idle");
    const stopWatching = connectionStore.subscribe(() => {
      if (connectionStore.getState().client === client && client.state === "idle") {
        client.emitStateChange("connecting");
      }
    });
    connectionStore.getState().connect(client);
    stopWatching();

    expect(connectionStore.getState().state).toBe("connecting");
  });

  // The unsubscribe returned by onStateChange is the cooperative half of the
  // fence. It is not sufficient on its own: a real client may have already
  // captured the callback into an in-flight dispatch, so detaching cannot
  // un-invoke it. The identity check has to stand without it.
  test("a late callback from a replaced client is ignored even when its unsubscribe does not detach", () => {
    class UndetachableClient extends FakeClient {
      captured: ((s: ConnectionState) => void) | null = null;
      override onStateChange(cb: (s: ConnectionState) => void): () => void {
        this.captured = cb;
        return () => {};
      }
    }

    const stale = new UndetachableClient("ready");
    connectionStore.getState().connect(stale);
    const leaked = stale.captured;
    expect(leaked).not.toBeNull();

    const current = new FakeClient("ready");
    connectionStore.getState().connect(current);

    leaked?.("closed");

    expect(connectionStore.getState().client).toBe(current);
    expect(connectionStore.getState().state).toBe("ready");
  });
});
