// daemonResidents.test.ts — store-level unit tests for the daemon-residents
// polling store. Component-level polling and rendering tests live in
// hubResidents.test.tsx.
//
// Pattern mirrors settingsOverview.test.ts: each test resets store + connection
// in beforeEach, cleans up in afterEach.

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { DaemonIdentity, DaemonListResponse, DaemonRetireResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import {
  _clearDaemonResidentsInflightForTests,
  daemonResidentsStore,
  resetDaemonResidentsStoreForTests,
  useDaemonResidentsStore,
} from "./daemonResidents";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const EMPTY_RESPONSE: DaemonListResponse = {
  defaultTimeoutMillis: 3600000,
  daemons: [],
};

const IDENTITY_A: DaemonIdentity = {
  ref: "local:test-daemon-a",
  pid: 1001,
  startedAt: "2026-09-10T00:00:00Z",
  generation: "gen-a",
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetDaemonResidentsStoreForTests();
});

afterEach(() => {
  // No fake timers used in this file; real timer reset not needed here.
});

describe("initial state", () => {
  test("data starts null, loading false, error null, pending empty", () => {
    const state = daemonResidentsStore.getState();
    expect(state.data).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
    expect(state.pending.size).toBe(0);
  });
});

describe("refresh", () => {
  test("requests evener/daemon/list with empty params and stores the result", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);

    await daemonResidentsStore.getState().refresh();

    expect(fake.calls).toEqual([{ method: "evener/daemon/list", params: {} }]);
    const state = daemonResidentsStore.getState();
    expect(state.data).toEqual(EMPTY_RESPONSE);
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
  });

  test("sets loading true while the request is in flight", async () => {
    const fake = connectFakeClient();
    let resolveRequest: (() => void) | undefined;
    fake.on(
      "evener/daemon/list",
      () =>
        new Promise<DaemonListResponse>((resolve) => {
          resolveRequest = () => resolve(EMPTY_RESPONSE);
        }),
    );

    const pending = daemonResidentsStore.getState().refresh();
    await Promise.resolve(); // let the request handler actually be invoked
    expect(daemonResidentsStore.getState().loading).toBe(true);

    resolveRequest?.();
    await pending;
    expect(daemonResidentsStore.getState().loading).toBe(false);
  });

  test("concurrent calls before the first resolves share one in-flight request", async () => {
    const fake = connectFakeClient();
    let handlerCallCount = 0;
    fake.on("evener/daemon/list", () => {
      handlerCallCount += 1;
      return EMPTY_RESPONSE;
    });

    await Promise.all([daemonResidentsStore.getState().refresh(), daemonResidentsStore.getState().refresh()]);

    // Only one request should have been issued despite two concurrent refresh() calls
    expect(fake.calls).toHaveLength(1);
    expect(handlerCallCount).toBe(1);
  });

  test("a failed refresh preserves existing data and populates error", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);
    await daemonResidentsStore.getState().refresh(); // first call: succeeds
    expect(daemonResidentsStore.getState().data).toEqual(EMPTY_RESPONSE);

    fake.on("evener/daemon/list", () => {
      throw new Error("network down");
    });
    await daemonResidentsStore.getState().refresh();

    const state = daemonResidentsStore.getState();
    expect(state.data).toEqual(EMPTY_RESPONSE); // stale data kept, not blanked
    expect(state.error).toBe("network down");
    expect(state.loading).toBe(false);
  });

  test("newest-client generation wins a late response", async () => {
    const fake = connectFakeClient();
    let resolveA!: (data: DaemonListResponse) => void;

    fake.on("evener/daemon/list", () => {
      return new Promise<DaemonListResponse>((resolve) => {
        resolveA = resolve;
      });
    });

    // Start first refresh (generation 1 inside runRefresh)
    const p1 = daemonResidentsStore.getState().refresh();
    await Promise.resolve(); // let request A start

    // Clear inflight so a new refresh starts a second, independent request
    // rather than joining the existing one.
    _clearDaemonResidentsInflightForTests();

    // Second request resolves immediately with different data
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 9999,
      daemons: [],
    }));
    const p2 = daemonResidentsStore.getState().refresh();
    await p2; // generation 2 resolved first

    expect(daemonResidentsStore.getState().data?.defaultTimeoutMillis).toBe(9999);

    // Now resolve generation 1 (late response)
    resolveA({ defaultTimeoutMillis: 1111, daemons: [] });
    await p1;

    // Generation check: generation-1 response discarded; generation-2 data stays
    expect(daemonResidentsStore.getState().data?.defaultTimeoutMillis).toBe(9999);
  });

  test("rejects with a helpful error when no client is connected", async () => {
    // No client connected: store has client=null
    await daemonResidentsStore.getState().refresh();
    const state = daemonResidentsStore.getState();
    expect(state.data).toBeNull();
    expect(state.error).toMatch(/no client connected/);
  });
});

describe("retire", () => {
  test("adds and removes identity.generation from pending, sends correct RPC", async () => {
    const fake = connectFakeClient();
    const retireResponse: DaemonRetireResponse = {
      accepted: true,
      lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
    };
    // Also handle the follow-up refresh that retire triggers
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);
    fake.on("evener/daemon/retire", (params) => {
      expect(params.identity).toEqual(IDENTITY_A);
      // While the retire is in flight, pending should contain the generation key
      expect(daemonResidentsStore.getState().pending.has(IDENTITY_A.generation)).toBe(true);
      return retireResponse;
    });

    const result = await daemonResidentsStore.getState().retire(IDENTITY_A);

    expect(result).toEqual(retireResponse);
    expect(daemonResidentsStore.getState().pending.has(IDENTITY_A.generation)).toBe(false);
    expect(fake.calls).toContainEqual({ method: "evener/daemon/retire", params: { identity: IDENTITY_A } });
  });

  test("pending key is identity.generation, not ref or PID", async () => {
    const fake = connectFakeClient();
    let capturedPending: Set<string> | null = null;
    const retireResponse: DaemonRetireResponse = {
      accepted: false,
      lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [{ category: "turn" }] },
    };
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);
    fake.on("evener/daemon/retire", () => {
      capturedPending = new Set(daemonResidentsStore.getState().pending);
      return retireResponse;
    });

    await daemonResidentsStore.getState().retire(IDENTITY_A);

    // Key must be generation, not ref or pid
    expect(capturedPending).not.toBeNull();
    expect(capturedPending!.has(IDENTITY_A.generation)).toBe(true);
    expect(capturedPending!.has(IDENTITY_A.ref)).toBe(false);
    expect(capturedPending!.has(String(IDENTITY_A.pid))).toBe(false);
  });

  test("throws (propagates error) when retire RPC fails", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);
    fake.on("evener/daemon/retire", () => {
      throw new Error("retire rejected");
    });

    await expect(daemonResidentsStore.getState().retire(IDENTITY_A)).rejects.toThrow("retire rejected");
    // pending should be cleared even on error
    expect(daemonResidentsStore.getState().pending.has(IDENTITY_A.generation)).toBe(false);
  });
});

describe("forceStop", () => {
  test("sends evener/thread/forceStop with ref and expectedDaemon set to the identity", async () => {
    const fake = connectFakeClient();
    fake.on("evener/thread/forceStop", (params) => {
      expect(params.ref).toBe(IDENTITY_A.ref);
      expect(params.expectedDaemon).toEqual(IDENTITY_A);
    });
    // follow-up refresh
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);

    await daemonResidentsStore.getState().forceStop(IDENTITY_A);

    expect(fake.calls).toContainEqual({
      method: "evener/thread/forceStop",
      params: { ref: IDENTITY_A.ref, expectedDaemon: IDENTITY_A },
    });
  });

  test("adds and removes identity.generation from pending on force-stop", async () => {
    const fake = connectFakeClient();
    let capturedPending: Set<string> | null = null;
    fake.on("evener/thread/forceStop", () => {
      capturedPending = new Set(daemonResidentsStore.getState().pending);
    });
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);

    await daemonResidentsStore.getState().forceStop(IDENTITY_A);

    expect(capturedPending).not.toBeNull();
    expect(capturedPending!.has(IDENTITY_A.generation)).toBe(true);
    expect(daemonResidentsStore.getState().pending.has(IDENTITY_A.generation)).toBe(false);
  });
});

describe("useDaemonResidentsStore hook", () => {
  test("reflects store state and re-renders on change", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => EMPTY_RESPONSE);

    const { result } = renderHook(() => useDaemonResidentsStore((s) => s.data));
    expect(result.current).toBeNull();

    await act(async () => {
      await daemonResidentsStore.getState().refresh();
    });
    expect(result.current).toEqual(EMPTY_RESPONSE);
  });

  test("called with no selector returns the whole state", () => {
    const { result } = renderHook(() => useDaemonResidentsStore());
    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(false);
  });
});
