import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { PluginPreviewResponse } from "../../protocol/types.gen";
import { usePluginPreview } from "./usePluginPreview";

const RESPONSE: PluginPreviewResponse = { plugins: [] };

function flush(): Promise<void> {
  return Promise.resolve();
}

describe("usePluginPreview", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  test("loads one preview after the declared debounce and includes explicit selection", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/plugin/preview", (params) => {
      expect(params).toEqual({ cwd: "/repo", launchOverrides: { enabledPlugins: ["a"] } });
      return RESPONSE;
    });
    const { result } = renderHook(() =>
      usePluginPreview({
        client,
        cwd: "/repo",
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
    expect(client.calls.filter((call) => call.method === "evener/plugin/preview")).toHaveLength(1);
  });

  test("coalesces cwd changes and does not guess plugin counts while loading", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    let resolve!: (response: PluginPreviewResponse) => void;
    client.on("evener/plugin/preview", () => new Promise((done) => (resolve = done)));
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) => usePluginPreview({ client, cwd, launchOverrides: {}, pluginRevision: 0 }),
      { initialProps: { cwd: "/one" } },
    );
    rerender({ cwd: "/two" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
    });
    expect(client.calls.filter((call) => call.method === "evener/plugin/preview")).toHaveLength(1);
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
    const responses: Array<(response: PluginPreviewResponse) => void> = [];
    client.on("evener/plugin/preview", () => new Promise((resolve) => responses.push(resolve)));
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) => usePluginPreview({ client, cwd, launchOverrides: {}, pluginRevision: 0 }),
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
        plugins: [
          {
            name: "stale",
            source: "test",
            selected: false,
            skillCount: 0,
            agentCount: 0,
            commandCount: 0,
            hookCount: 0,
            mcpCount: 0,
          },
        ],
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

  test("retry reloads the same key and revision refresh starts a new request", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/plugin/preview", () => RESPONSE);
    const { result, rerender } = renderHook(
      ({ revision }: { revision: number }) =>
        usePluginPreview({ client, cwd: "/repo", launchOverrides: {}, pluginRevision: revision }),
      { initialProps: { revision: 0 } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(client.calls.filter((call) => call.method === "evener/plugin/preview")).toHaveLength(1);
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
    expect(client.calls.filter((call) => call.method === "evener/plugin/preview")).toHaveLength(3);
  });

  test("reports request errors and retry can recover", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    client.on("evener/plugin/preview", () => {
      throw new Error("preview failed");
    });
    const { result } = renderHook(() =>
      usePluginPreview({ client, cwd: "/repo", launchOverrides: {}, pluginRevision: 0 }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "error", message: "preview failed" });
  });
});
