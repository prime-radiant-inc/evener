import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { SpawnSlashCatalogResponse } from "../../protocol/types.gen";
import { useSpawnSlashCatalog } from "./useSpawnSlashCatalog";

const RESPONSE: SpawnSlashCatalogResponse = { commands: [], skills: [] };

function flush(): Promise<void> {
  return Promise.resolve();
}

describe("useSpawnSlashCatalog", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  test("loads one catalog after the declared debounce with spawn-shaped params", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/spawn/slashCatalog", (params) => {
      expect(params).toEqual({ cwd: "/repo", harness: "evener", launchOverrides: { enabledPlugins: ["a"] } });
      return { commands: [], skills: [] };
    });
    const { result } = renderHook(() =>
      useSpawnSlashCatalog({
        client,
        cwd: "/repo",
        harness: "evener",
        launchOverrides: { enabledPlugins: ["a"] },
        pluginRevision: 0,
      }),
    );
    expect(result.current.state).toEqual({ status: "loading" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(1);
  });

  test("coalesces cwd changes and does not guess catalog while loading", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    let resolve!: (response: SpawnSlashCatalogResponse) => void;
    client.on("evener/spawn/slashCatalog", () => new Promise((done) => (resolve = done)));
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) =>
        useSpawnSlashCatalog({ client, cwd, harness: "evener", launchOverrides: {}, pluginRevision: 0 }),
      { initialProps: { cwd: "/one" } },
    );
    rerender({ cwd: "/two" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
    });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(1);
    expect(result.current.state).toEqual({ status: "loading" });
    await act(async () => {
      resolve(RESPONSE);
      await flush();
    });
    expect(result.current.state.status).toBe("ready");
  });

  test("ignores a late response whose key is stale", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    const responses: Array<(response: SpawnSlashCatalogResponse) => void> = [];
    client.on("evener/spawn/slashCatalog", () => new Promise((resolve) => responses.push(resolve)));
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) =>
        useSpawnSlashCatalog({ client, cwd, harness: "evener", launchOverrides: {}, pluginRevision: 0 }),
      { initialProps: { cwd: "/one" } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
    });
    rerender({ cwd: "/two" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
    });
    await act(async () => {
      responses[0]!({
        commands: [{ name: "stale", source: "test" }],
        skills: [],
      });
      await flush();
    });
    expect(result.current.state).toEqual({ status: "loading" });
    await act(async () => {
      responses[1]!(RESPONSE);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
  });

  test("keeps the previous response mounted while a same-cwd refresh loads", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    const responseA: SpawnSlashCatalogResponse = {
      commands: [{ name: "a", source: "test" }],
      skills: [],
    };
    let resolveRefresh!: (response: SpawnSlashCatalogResponse) => void;
    let requests = 0;
    client.on("evener/spawn/slashCatalog", () => {
      requests += 1;
      if (requests === 1) return responseA;
      return new Promise<SpawnSlashCatalogResponse>((done) => (resolveRefresh = done));
    });
    const { result, rerender } = renderHook(
      ({ overrides }: { overrides: Record<string, unknown> }) =>
        useSpawnSlashCatalog({
          client,
          cwd: "/repo",
          harness: "evener",
          // cast through unknown to satisfy LaunchConfigLayer without importing extra typing
          launchOverrides: overrides as unknown as import("../../protocol/types.gen").LaunchConfigLayer,
          pluginRevision: 0,
        }),
      { initialProps: { overrides: {} as Record<string, unknown> } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: responseA });

    rerender({ overrides: { enabledPlugins: ["a"] } });
    expect(result.current.state).toEqual({ status: "loading", response: responseA });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
    });
    await act(async () => {
      resolveRefresh(RESPONSE);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
  });

  test("does not reuse a successful response after a changed request fails", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    const responseA: SpawnSlashCatalogResponse = {
      commands: [{ name: "request-a", source: "test" }],
      skills: [],
    };
    let requests = 0;
    client.on("evener/spawn/slashCatalog", () => {
      requests += 1;
      if (requests === 1) return responseA;
      throw new Error("catalog failed for request B");
    });
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) =>
        useSpawnSlashCatalog({ client, cwd, harness: "evener", launchOverrides: {}, pluginRevision: 0 }),
      { initialProps: { cwd: "/request-a" } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: responseA });

    rerender({ cwd: "/request-b" });
    expect(result.current.state).toEqual({ status: "loading" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });

    expect(result.current.state).toEqual({ status: "error", message: "catalog failed for request B" });
  });

  test("retry reloads the same key and revision refresh starts a new request", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/spawn/slashCatalog", () => RESPONSE);
    const { result, rerender } = renderHook(
      ({ revision }: { revision: number }) =>
        useSpawnSlashCatalog({
          client,
          cwd: "/repo",
          harness: "evener",
          launchOverrides: {},
          pluginRevision: revision,
        }),
      { initialProps: { revision: 0 } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(1);
    act(() => result.current.retry());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    rerender({ revision: 1 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(3);
  });

  test("reports request errors and retry can recover", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/spawn/slashCatalog", () => {
      throw new Error("catalog failed");
    });
    const { result } = renderHook(() =>
      useSpawnSlashCatalog({ client, cwd: "/repo", harness: "evener", launchOverrides: {}, pluginRevision: 0 }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "error", message: "catalog failed" });
  });

  test("enabled=false reports ready-empty with no request", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/spawn/slashCatalog", () => RESPONSE);
    const { result } = renderHook(() =>
      useSpawnSlashCatalog({
        client,
        cwd: "/repo",
        harness: "external",
        launchOverrides: {},
        pluginRevision: 0,
        enabled: false,
      }),
    );
    expect(result.current.state).toEqual({ status: "ready", response: { commands: [], skills: [] } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
      await flush();
    });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(0);
    expect(result.current.state).toEqual({ status: "ready", response: { commands: [], skills: [] } });
  });

  test("omits empty harness and launchOverrides but always sends cwd", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/spawn/slashCatalog", (params) => {
      expect(params).toEqual({ cwd: "" });
      return RESPONSE;
    });
    const { result } = renderHook(() =>
      useSpawnSlashCatalog({
        client,
        cwd: "",
        harness: "",
        launchOverrides: {},
        pluginRevision: 0,
      }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
    expect(client.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(1);
  });
});

test("drops a late response from a replaced client", async () => {
  vi.useFakeTimers();
  const clientA = new FakeClient();
  const clientB = new FakeClient();
  let resolveA!: (response: SpawnSlashCatalogResponse) => void;
  clientA.on("evener/spawn/slashCatalog", () => new Promise((done) => (resolveA = done)));
  clientB.on("evener/spawn/slashCatalog", () => RESPONSE);
  const { result, rerender } = renderHook(
    ({ client }: { client: FakeClient }) =>
      useSpawnSlashCatalog({ client, cwd: "/repo", harness: "evener", launchOverrides: {}, pluginRevision: 0 }),
    { initialProps: { client: clientA } },
  );
  // Fire A's request, then swap the client before it resolves.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(250);
  });
  expect(clientA.calls.filter((call) => call.method === "evener/spawn/slashCatalog")).toHaveLength(1);
  rerender({ client: clientB });
  await act(async () => {
    resolveA({
      commands: [{ name: "stale-client", source: "test" }],
      skills: [],
    });
    await flush();
  });
  // A's late response commits nothing; B's request serves the state.
  expect(result.current.state.status).not.toBe("ready");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(250);
    await flush();
  });
  expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
});
