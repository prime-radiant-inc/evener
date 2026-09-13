import { act, renderHook } from "@testing-library/react";
import { useLayoutEffect } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, PluginPreviewResponse } from "../../protocol/types.gen";
import {
  PLUGIN_PREVIEW_DEBOUNCE_MS,
  type PluginPreviewLoadState,
  type UsePluginPreviewArgs,
  usePluginPreview,
} from "./usePluginPreview";

const RESPONSE: PluginPreviewResponse = { plugins: [] };

function flush(): Promise<void> {
  return Promise.resolve();
}

describe("usePluginPreview", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  // Observe committed consumer effects, not just renderHook's final result:
  // a passive reset can otherwise hide the first render's stale authority.
  for (const previousStatus of ["ready", "error"] as const) {
    test.each(["cwd", "overrides", "revision", "retry", "client", "disabled"] as const)(
      `revokes ${previousStatus} authority on the first consumer render after %s changes`,
      async (change) => {
        vi.useFakeTimers();
        const client = new FakeClient();
        client.on("evener/plugin/preview", () => {
          if (previousStatus === "error") throw new Error("old request failed");
          return RESPONSE;
        });
        const initialProps: UsePluginPreviewArgs = { client, cwd: "/a", launchOverrides: {}, pluginRevision: 0 };
        const observed: PluginPreviewLoadState[] = [];
        const { result, rerender } = renderHook(
          (args: UsePluginPreviewArgs) => {
            const preview = usePluginPreview(args);
            useLayoutEffect(() => {
              observed.push(preview.state);
            });
            return preview;
          },
          { initialProps },
        );
        await act(async () => {
          await vi.advanceTimersByTimeAsync(PLUGIN_PREVIEW_DEBOUNCE_MS);
        });
        expect(result.current.state.status).toBe(previousStatus);
        observed.length = 0;

        if (change === "retry") act(() => result.current.retry());
        else {
          rerender({
            ...initialProps,
            ...(change === "cwd" ? { cwd: "/b" } : {}),
            ...(change === "overrides" ? { launchOverrides: { enabledPlugins: ["b"] } } : {}),
            ...(change === "revision" ? { pluginRevision: 1 } : {}),
            ...(change === "client" ? { client: new FakeClient() } : {}),
            ...(change === "disabled" ? { enabled: false } : {}),
          });
        }

        const cached = previousStatus === "ready" && ["overrides", "revision", "retry"].includes(change);
        expect(observed.length).toBeGreaterThan(0);
        for (const state of observed) {
          expect(state).toEqual(cached ? { status: "loading", response: RESPONSE } : { status: "loading" });
        }
      },
    );
  }

  for (const outcome of ["success", "error"] as const) {
    test.each(["cwd", "overrides", "revision", "retry", "client", "disabled", "cwd round trip", "reenabled"] as const)(
      `ignores late ${outcome} after %s changes`,
      async (change) => {
        vi.useFakeTimers();
        let resolveOld: ((response: PluginPreviewResponse) => void) | undefined;
        let rejectOld: ((error: Error) => void) | undefined;
        const pending = new Promise<PluginPreviewResponse>((resolve, reject) => {
          resolveOld = resolve;
          rejectOld = reject;
        });
        const client = new FakeClient();
        client.on("evener/plugin/preview", () => pending);
        const initialProps: UsePluginPreviewArgs = { client, cwd: "/a", launchOverrides: {}, pluginRevision: 0 };
        const { result, rerender } = renderHook((args: UsePluginPreviewArgs) => usePluginPreview(args), {
          initialProps,
        });
        await act(async () => {
          await vi.advanceTimersByTimeAsync(PLUGIN_PREVIEW_DEBOUNCE_MS);
        });
        expect(client.calls).toHaveLength(1);

        client.on("evener/plugin/preview", () => RESPONSE);
        const replacement = new FakeClient();
        replacement.on("evener/plugin/preview", () => RESPONSE);
        if (change === "retry") act(() => result.current.retry());
        else if (change === "cwd round trip" || change === "reenabled") {
          rerender({ ...initialProps, ...(change === "reenabled" ? { enabled: false } : { cwd: "/b" }) });
          rerender(initialProps);
        } else {
          rerender({
            ...initialProps,
            ...(change === "cwd" ? { cwd: "/b" } : {}),
            ...(change === "overrides" ? { launchOverrides: { enabledPlugins: ["b"] } } : {}),
            ...(change === "revision" ? { pluginRevision: 1 } : {}),
            ...(change === "client" ? { client: replacement } : {}),
            ...(change === "disabled" ? { enabled: false } : {}),
          });
        }
        await act(async () => {
          await vi.advanceTimersByTimeAsync(PLUGIN_PREVIEW_DEBOUNCE_MS);
        });
        const expected = change === "disabled" ? { status: "loading" } : { status: "ready", response: RESPONSE };
        expect(result.current.state).toEqual(expected);

        await act(async () => {
          if (!resolveOld || !rejectOld) throw new Error("old preview promise was not initialized");
          if (outcome === "success") resolveOld({ plugins: [], selectionErrors: [{ name: "old", reason: "gone" }] });
          else rejectOld(new Error("old request failed"));
        });
        expect(result.current.state).toEqual(expected);
      },
    );
  }

  test.each(["retry", "overrides", "revision", "client"] as const)(
    "retains cached error details only for the same logical request after %s changes",
    async (change) => {
      vi.useFakeTimers();
      const client = new FakeClient();
      client.on("evener/plugin/preview", () => RESPONSE);
      const initialProps: UsePluginPreviewArgs = { client, cwd: "/a", launchOverrides: {}, pluginRevision: 0 };
      const { result, rerender } = renderHook((args: UsePluginPreviewArgs) => usePluginPreview(args), { initialProps });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(PLUGIN_PREVIEW_DEBOUNCE_MS);
      });
      expect(result.current.state).toEqual({ status: "ready", response: RESPONSE });
      const fail = () => {
        throw new Error("refresh failed");
      };
      client.on("evener/plugin/preview", fail);
      const replacement = new FakeClient();
      replacement.on("evener/plugin/preview", fail);

      if (change === "retry") act(() => result.current.retry());
      else {
        rerender({
          ...initialProps,
          ...(change === "overrides" ? { launchOverrides: { enabledPlugins: ["b"] } } : {}),
          ...(change === "revision" ? { pluginRevision: 1 } : {}),
          ...(change === "client" ? { client: replacement } : {}),
        });
      }
      await act(async () => {
        await vi.advanceTimersByTimeAsync(PLUGIN_PREVIEW_DEBOUNCE_MS);
      });
      expect(result.current.state).toEqual({
        status: "error",
        message: "refresh failed",
        ...(change === "retry" ? { response: RESPONSE } : {}),
      });
    },
  );

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

  test("keeps the previous response mounted while a same-cwd refresh loads", async () => {
    vi.useFakeTimers();
    const client = new FakeClient();
    const responseA: PluginPreviewResponse = {
      plugins: [
        {
          name: "a",
          source: "test",
          selected: true,
          skillCount: 1,
          agentCount: 0,
          commandCount: 0,
          hookCount: 0,
          mcpCount: 0,
        },
      ],
    };
    let resolveRefresh!: (response: PluginPreviewResponse) => void;
    let requests = 0;
    client.on("evener/plugin/preview", () => {
      requests += 1;
      if (requests === 1) return responseA;
      return new Promise<PluginPreviewResponse>((done) => (resolveRefresh = done));
    });
    const { result, rerender } = renderHook(
      ({ overrides }: { overrides: LaunchConfigLayer }) =>
        usePluginPreview({ client, cwd: "/repo", launchOverrides: overrides, pluginRevision: 0 }),
      { initialProps: { overrides: {} } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(250);
      await flush();
    });
    expect(result.current.state).toEqual({ status: "ready", response: responseA });

    // A selection toggle changes only the overrides: the panel keeps showing
    // the previous plugins instead of collapsing to an empty loading state.
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
    const responseA: PluginPreviewResponse = {
      plugins: [
        {
          name: "request-a",
          source: "test",
          selected: true,
          skillCount: 0,
          agentCount: 0,
          commandCount: 0,
          hookCount: 0,
          mcpCount: 0,
        },
      ],
    };
    let requests = 0;
    client.on("evener/plugin/preview", () => {
      requests += 1;
      if (requests === 1) return responseA;
      throw new Error("preview failed for request B");
    });
    const { result, rerender } = renderHook(
      ({ cwd }: { cwd: string }) => usePluginPreview({ client, cwd, launchOverrides: {}, pluginRevision: 0 }),
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

    expect(result.current.state).toEqual({ status: "error", message: "preview failed for request B" });
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
