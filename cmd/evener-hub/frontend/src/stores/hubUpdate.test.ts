import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { GENERIC_ERROR_MESSAGE, HUB_UNREACHABLE_MESSAGE, WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { UpdateCheckResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import {
  APPLY_TIMEOUT_MS,
  hubUpdateStore,
  RESTART_POLL_MS,
  RESTART_TIMEOUT_MS,
  resetHubUpdateStoreForTests,
} from "./hubUpdate";
import { resetThreadsStoreForTests } from "./threads";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "be70029",
  currentCommit: "be70029",
  latestTag: "snapshot",
  latestCommit: "be7002918fdc60dbdeab71d9dd17e00d3d006c56",
  updateAvailable: false,
  applicable: true,
};

function healthFetch(versions: string[]): typeof fetch {
  let i = 0;
  return vi.fn(async () => {
    const version = versions[Math.min(i, versions.length - 1)];
    i += 1;
    if (version === "DOWN") throw new TypeError("Failed to fetch");
    return new Response(JSON.stringify({ version }), { status: 200 });
  }) as unknown as typeof fetch;
}

interface CheckSettler {
  resolve: (value: UpdateCheckResponse) => void;
  reject: (err: unknown) => void;
}

// scriptedChecks holds every evener/update/check in flight so a test can
// settle them in whatever order it wants.
function scriptedChecks(fake: FakeClient): CheckSettler[] {
  const settlers: CheckSettler[] = [];
  fake.on(
    "evener/update/check",
    () =>
      new Promise<UpdateCheckResponse>((resolve, reject) => {
        settlers.push({ resolve, reject });
      }),
  );
  return settlers;
}

function settler(settlers: CheckSettler[], index: number): CheckSettler {
  const found = settlers[index];
  if (!found) throw new Error(`no check in flight at index ${index}`);
  return found;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetHubUpdateStoreForTests();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("runCheck", () => {
  test("requests evener/update/check with the selected channel and stores the result", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    hubUpdateStore.getState().setChannel("snapshot");

    await act(() => hubUpdateStore.getState().runCheck());

    expect(fake.calls).toEqual([{ method: "evener/update/check", params: { channel: "snapshot" } }]);
    const state = hubUpdateStore.getState();
    expect(state.check).toEqual(UP_TO_DATE);
    expect(state.checking).toBe(false);
    expect(state.checkError).toBeNull();
  });

  test("sends an empty channel when none is selected yet", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);

    await act(() => hubUpdateStore.getState().runCheck());

    expect(fake.calls).toEqual([{ method: "evener/update/check", params: { channel: "" } }]);
  });

  test("stores the hub's own message and clears the previous result on failure", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => {
      throw new WireError("GET x: 403 Forbidden: API rate limit exceeded", -1);
    });

    await act(() => hubUpdateStore.getState().runCheck());

    const state = hubUpdateStore.getState();
    expect(state.check).toBeNull();
    expect(state.checkError).toContain("rate limit");
  });

  test("shows the generic message for a non-WireError rejection, not its raw text", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => {
      throw new Error("TypeError: cyclic object value");
    });

    await act(() => hubUpdateStore.getState().runCheck());

    expect(hubUpdateStore.getState().checkError).toBe(GENERIC_ERROR_MESSAGE);
  });

  test("setChannel clears the stale check result", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    await act(() => hubUpdateStore.getState().runCheck());

    act(() => hubUpdateStore.getState().setChannel("release"));

    expect(hubUpdateStore.getState().channel).toBe("release");
    expect(hubUpdateStore.getState().check).toBeNull();
  });

  test("setChannel clears a stale restartTimedOut flag", () => {
    hubUpdateStore.setState({ restartTimedOut: true });

    act(() => hubUpdateStore.getState().setChannel("release"));

    expect(hubUpdateStore.getState().restartTimedOut).toBe(false);
  });

  test("runCheck clears a stale restartTimedOut flag", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    hubUpdateStore.setState({ restartTimedOut: true });

    await act(() => hubUpdateStore.getState().runCheck());

    expect(hubUpdateStore.getState().restartTimedOut).toBe(false);
  });

  test("ignores stale runCheck response when channel changed during flight", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    let resolveCheck: ((value: UpdateCheckResponse) => void) | undefined;
    const checkPromise = new Promise<UpdateCheckResponse>((resolve) => {
      resolveCheck = resolve;
    });
    fake.on("evener/update/check", () => checkPromise);
    hubUpdateStore.getState().setChannel("release");

    act(() => {
      void hubUpdateStore.getState().runCheck();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(hubUpdateStore.getState().checking).toBe(true);
    act(() => hubUpdateStore.getState().setChannel("snapshot"));
    expect(hubUpdateStore.getState().checking).toBe(false);

    act(() => {
      if (resolveCheck) resolveCheck(UP_TO_DATE);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    const state = hubUpdateStore.getState();
    expect(state.check).toBeNull();
    expect(state.checkError).toBeNull();
  });

  test("keeps the newest result when two checks on the same channel finish out of order", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    const settlers = scriptedChecks(fake);
    hubUpdateStore.getState().setChannel("snapshot");

    act(() => {
      void hubUpdateStore.getState().runCheck();
      void hubUpdateStore.getState().runCheck();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    const newest = { ...UP_TO_DATE, updateAvailable: true, latestCommit: "3b1c5f8aaaa" };
    act(() => {
      settler(settlers, 1).resolve(newest);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => {
      settler(settlers, 0).resolve(UP_TO_DATE);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    const state = hubUpdateStore.getState();
    expect(state.check).toEqual(newest);
    expect(state.checking).toBe(false);
  });

  test("ignores a late failure from a superseded check on the same channel", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    const settlers = scriptedChecks(fake);
    hubUpdateStore.getState().setChannel("snapshot");

    act(() => {
      void hubUpdateStore.getState().runCheck();
      void hubUpdateStore.getState().runCheck();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    act(() => {
      settler(settlers, 1).resolve(UP_TO_DATE);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => {
      settler(settlers, 0).reject(new WireError("GET x: 403 Forbidden: API rate limit exceeded", -1));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    const state = hubUpdateStore.getState();
    expect(state.check).toEqual(UP_TO_DATE);
    expect(state.checkError).toBeNull();
  });
});

describe("apply", () => {
  test("refuses to apply when the check says already up to date", async () => {
    resetHubUpdateStoreForTests();
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener"],
      restarting: true,
    }));
    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());

    await act(() => hubUpdateStore.getState().apply());

    // Fails today: apply only checks channel staleness, so a stored
    // up-to-date check still issues a full download + exec restart.
    expect(fake.calls.some((call) => call.method === "evener/update/apply")).toBe(false);
    expect(hubUpdateStore.getState().applyError).toBe("Already up to date");
    expect(hubUpdateStore.getState().restarting).toBe(false);
  });

  test("requests evener/update/apply, then polls /api/health until the version changes and reloads", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    const fetchImpl = healthFetch(["be70029", "DOWN", "DOWN", "3b1c5f8"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "3b1c5f8aaaa" }));
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener", "/x/evener-dev"],
      restarting: true,
    }));
    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());

    act(() => {
      void hubUpdateStore.getState().apply();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(fake.calls[1]).toEqual({
      method: "evener/update/apply",
      params: { channel: "snapshot" },
      // The hub may synchronously download + verify for up to five minutes
      // (selfupdate.defaultUpgradeTimeout); the generic 30s client timeout
      // would report a slow-but-valid download as failed and never start
      // the restart poll, so apply carries its own longer deadline.
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    });
    expect(APPLY_TIMEOUT_MS).toBeGreaterThan(5 * 60_000);
    expect(hubUpdateStore.getState().restarting).toBe(true);
    expect(hubUpdateStore.getState().applying).toBe(false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_POLL_MS * 4);
    });

    expect(reload).toHaveBeenCalledTimes(1);
    expect(hubUpdateStore.getState().restartTimedOut).toBe(false);
    // The poll has to see the NEW hub, so it must never be answered from a cache,
    // and every attempt carries a deadline so a hung fetch cannot outlive the timeout.
    for (const call of (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls) {
      expect(call[0]).toBe("/api/health");
      expect(call[1]).toMatchObject({ credentials: "same-origin", cache: "no-store" });
      expect(call[1].signal).toBeInstanceOf(AbortSignal);
    }
  });

  test("gives up after RESTART_TIMEOUT_MS when the version never changes", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    resetHubUpdateStoreForTests({ fetchImpl: healthFetch(["be70029"]), reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true }));
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener"],
      restarting: true,
    }));
    await act(() => hubUpdateStore.getState().runCheck());
    act(() => {
      void hubUpdateStore.getState().apply();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_TIMEOUT_MS + RESTART_POLL_MS);
    });

    expect(reload).not.toHaveBeenCalled();
    expect(hubUpdateStore.getState().restarting).toBe(false);
    expect(hubUpdateStore.getState().restartTimedOut).toBe(true);
  });

  test("aborts a hung health fetch and still times out", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    const signals: AbortSignal[] = [];
    // Never settles on its own; only the store's own deadline can end it.
    const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      const signal = init?.signal;
      if (signal) signals.push(signal);
      return new Promise<Response>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(new Error("aborted")));
      });
    }) as unknown as typeof fetch;
    resetHubUpdateStoreForTests({ fetchImpl, reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true }));
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener"],
      restarting: true,
    }));
    await act(() => hubUpdateStore.getState().runCheck());
    act(() => {
      void hubUpdateStore.getState().apply();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_TIMEOUT_MS + RESTART_POLL_MS);
    });

    expect(signals.length).toBeGreaterThan(0);
    expect(signals.every((signal) => signal.aborted)).toBe(true);
    expect(reload).not.toHaveBeenCalled();
    expect(hubUpdateStore.getState().restartTimedOut).toBe(true);
  });

  test("stores applyError and does not poll when the hub refuses", async () => {
    vi.useFakeTimers();
    const fetchImpl = healthFetch(["be70029"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload: vi.fn() });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true }));
    fake.on("evener/update/apply", () => {
      throw new WireError("this hub is a dev build", -1);
    });

    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());
    await act(() => hubUpdateStore.getState().apply());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_POLL_MS * 2);
    });

    expect(hubUpdateStore.getState().applyError).toContain("dev build");
    expect(hubUpdateStore.getState().restarting).toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  test("shows the hub-unreachable message when apply fails on the transport", async () => {
    const fetchImpl = healthFetch(["be70029"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload: vi.fn() });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true }));
    fake.on("evener/update/apply", () => {
      throw new Error('AppwireClient: cannot call "evener/update/apply" while state is "closed"');
    });

    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());
    await act(() => hubUpdateStore.getState().apply());

    expect(hubUpdateStore.getState().applyError).toBe(HUB_UNREACHABLE_MESSAGE);
    expect(hubUpdateStore.getState().restarting).toBe(false);
  });

  test("requires a prior check and rejects apply() without one", async () => {
    vi.useFakeTimers();
    const fetchImpl = healthFetch(["be70029"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload: vi.fn() });
    const fake = connectFakeClient();
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener"],
      restarting: true,
    }));

    await act(() => hubUpdateStore.getState().apply());

    expect(fake.calls).toEqual([]);
    expect(hubUpdateStore.getState().applyError).toContain("Check for updates first");
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  test("guards against concurrent apply() calls", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    const fetchImpl = healthFetch(["be70029", "DOWN", "DOWN", "3b1c5f8"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "3b1c5f8aaaa" }));
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener"],
      restarting: true,
    }));
    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());

    act(() => {
      void hubUpdateStore.getState().apply();
      void hubUpdateStore.getState().apply();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    const applyCalls = fake.calls.filter((c) => c.method === "evener/update/apply");
    expect(applyCalls).toHaveLength(1);
  });
});

describe("stale check invalidation", () => {
  test("runCheck clears the previous result while checking", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    const settlers = scriptedChecks(fake);
    hubUpdateStore.getState().setChannel("snapshot");

    // First check resolves so a previous result exists.
    act(() => {
      void hubUpdateStore.getState().runCheck();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => {
      settler(settlers, 0).resolve(UP_TO_DATE);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(hubUpdateStore.getState().check).toEqual(UP_TO_DATE);

    // Second check still in flight: no stale result is shown meanwhile.
    act(() => {
      void hubUpdateStore.getState().runCheck();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(hubUpdateStore.getState().checking).toBe(true);
    expect(hubUpdateStore.getState().check).toBeNull();

    act(() => {
      settler(settlers, 1).resolve(UP_TO_DATE);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(hubUpdateStore.getState().check).toEqual(UP_TO_DATE);
  });

  test("apply refuses a check from another channel", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, channel: "snapshot" as const }));
    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());

    // Channel switched after the check resolved: the stored result is stale.
    act(() => {
      hubUpdateStore.setState({ channel: "release" });
    });
    await act(() => hubUpdateStore.getState().apply());

    expect(hubUpdateStore.getState().applyError).toBe("That result is stale; run a fresh check first");
    expect(fake.calls.some((c) => c.method === "evener/update/apply")).toBe(false);
  });
});
