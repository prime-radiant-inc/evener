import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import {
  resetSettingsOverviewStoreForTests,
  settingsOverviewStore,
  useSettingsOverviewStore,
} from "./settingsOverview";
import { resetThreadsStoreForTests } from "./threads";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const SAMPLE_RESPONSE: SettingsOverviewResponse = {
  hub: {
    version: "1.2.3",
    listenAddr: "127.0.0.1:9180",
    runDir: "/tmp/run",
  },
  storage: { stateDir: "/home/user/.serf" },
  agents: [{ name: "default" }],
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  // threads.ts wires a connectionStore.subscribe() detector at module scope
  // (permanent, not per-file) that reacts to EVERY connect() call in the
  // worker, including this file's own connectFakeClient() below, by
  // re-hydrating whatever refs its own module-private bookkeeping still
  // tracks. This file doesn't touch threads.ts at all, so a ref some other
  // file left tracked (module singleton, shared registry under
  // isolate:false) can otherwise land an extra "thread/read" call on THIS
  // file's own fake client, corrupting its unrelated call-count assertions.
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("initial state", () => {
  test("data/error start null, loading starts false", () => {
    const state = settingsOverviewStore.getState();
    expect(state.data).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
  });
});

describe("fetch", () => {
  test("requests serf/settings/overview with empty params and stores the result", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);

    await settingsOverviewStore.getState().fetch();

    expect(fake.calls).toEqual([{ method: "serf/settings/overview", params: {} }]);
    const state = settingsOverviewStore.getState();
    expect(state.data).toEqual(SAMPLE_RESPONSE);
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
  });

  test("sets loading true while the request is in flight", async () => {
    const fake = connectFakeClient();
    let resolveRequest: (() => void) | undefined;
    fake.on(
      "serf/settings/overview",
      () =>
        new Promise<SettingsOverviewResponse>((resolve) => {
          resolveRequest = () => resolve(SAMPLE_RESPONSE);
        }),
    );

    const pending = settingsOverviewStore.getState().fetch();
    await Promise.resolve(); // let the request handler actually be invoked
    expect(settingsOverviewStore.getState().loading).toBe(true);

    resolveRequest?.();
    await pending;
    expect(settingsOverviewStore.getState().loading).toBe(false);
  });

  test("caches: a second call after a successful load does not re-request", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);

    await settingsOverviewStore.getState().fetch();
    await settingsOverviewStore.getState().fetch();

    expect(fake.calls).toHaveLength(1);
  });

  test("concurrent calls before the first resolves share one in-flight request", async () => {
    const fake = connectFakeClient();
    let resolveCount = 0;
    fake.on("serf/settings/overview", () => {
      resolveCount += 1;
      return SAMPLE_RESPONSE;
    });

    await Promise.all([settingsOverviewStore.getState().fetch(), settingsOverviewStore.getState().fetch()]);

    expect(fake.calls).toHaveLength(1);
    expect(resolveCount).toBe(1);
  });

  test("a failed fetch leaves data null and populates error", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => {
      throw new Error("boom");
    });

    await settingsOverviewStore.getState().fetch();

    const state = settingsOverviewStore.getState();
    expect(state.data).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.error).toBe("boom");
  });

  test("a subsequent fetch after a failure retries (failure is not cached)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => {
      throw new Error("boom");
    });
    await settingsOverviewStore.getState().fetch();
    expect(settingsOverviewStore.getState().error).toBe("boom");

    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);
    await settingsOverviewStore.getState().fetch();

    expect(fake.calls).toHaveLength(2);
    expect(settingsOverviewStore.getState().data).toEqual(SAMPLE_RESPONSE);
    expect(settingsOverviewStore.getState().error).toBeNull();
  });

  test("rejects with a helpful error (not a throw) when no client is connected yet", async () => {
    await settingsOverviewStore.getState().fetch();
    const state = settingsOverviewStore.getState();
    expect(state.data).toBeNull();
    expect(state.error).toMatch(/no client connected/);
  });
});

describe("refresh", () => {
  test("always issues a fresh request even when data is already cached", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);
    await settingsOverviewStore.getState().fetch();
    expect(fake.calls).toHaveLength(1);

    await settingsOverviewStore.getState().refresh();
    expect(fake.calls).toHaveLength(2);
  });

  test("a failed refresh preserves the previously loaded data and surfaces the error", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);
    await settingsOverviewStore.getState().fetch();

    fake.on("serf/settings/overview", () => {
      throw new Error("network down");
    });
    await settingsOverviewStore.getState().refresh();

    const state = settingsOverviewStore.getState();
    expect(state.data).toEqual(SAMPLE_RESPONSE); // stale data kept, not blanked
    expect(state.error).toBe("network down");
  });
});

describe("useSettingsOverviewStore hook", () => {
  test("reflects store state and re-renders on change", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);

    const { result } = renderHook(() => useSettingsOverviewStore((s) => s.data));
    expect(result.current).toBeNull();

    await act(async () => {
      await settingsOverviewStore.getState().fetch();
    });
    expect(result.current).toEqual(SAMPLE_RESPONSE);
  });

  test("called with no selector returns the whole state", () => {
    const { result } = renderHook(() => useSettingsOverviewStore());
    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(false);
  });
});
