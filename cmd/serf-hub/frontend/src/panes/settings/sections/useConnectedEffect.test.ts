import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { useConnectedEffect } from "./useConnectedEffect";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("useConnectedEffect", () => {
  test("runs the attempt immediately when the connection is already ready", () => {
    connectionStore.setState({ state: "ready" });
    const attempt = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useConnectedEffect(attempt, []));
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  test("defers the attempt until the connection becomes ready - a deep link can mount before the client connects", () => {
    const attempt = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useConnectedEffect(attempt, []));
    expect(attempt).not.toHaveBeenCalled();
    act(() => {
      connectionStore.setState({ state: "ready" });
    });
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  test("fires at most once per mount, even if the connection cycles through ready multiple times", () => {
    const attempt = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useConnectedEffect(attempt, []));
    act(() => {
      connectionStore.setState({ state: "ready" });
      connectionStore.setState({ state: "reconnecting" });
      connectionStore.setState({ state: "ready" });
    });
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  test("a rejected attempt is swallowed - never surfaces as an unhandled rejection", async () => {
    connectionStore.setState({ state: "ready" });
    const attempt = vi.fn().mockRejectedValue(new Error("boom"));
    expect(() => renderHook(() => useConnectedEffect(attempt, []))).not.toThrow();
    await act(() => Promise.resolve());
  });

  test("unmounting before the connection is ready unsubscribes - a later ready transition never fires the attempt", () => {
    const attempt = vi.fn().mockResolvedValue(undefined);
    const { unmount } = renderHook(() => useConnectedEffect(attempt, []));
    unmount();
    act(() => {
      connectionStore.setState({ state: "ready" });
    });
    expect(attempt).not.toHaveBeenCalled();
  });

  test("a changed dep restarts the wait - a fresh attempt can fire again", () => {
    connectionStore.setState({ state: "ready" });
    const attempt = vi.fn().mockResolvedValue(undefined);
    const { rerender } = renderHook(({ dep }) => useConnectedEffect(attempt, [dep]), { initialProps: { dep: "a" } });
    expect(attempt).toHaveBeenCalledTimes(1);
    rerender({ dep: "b" });
    expect(attempt).toHaveBeenCalledTimes(2);
  });

  test("isCancelled() is false while mounted and becomes true after unmount - guards a setState-after-unmount race", async () => {
    connectionStore.setState({ state: "ready" });
    let observedBeforeUnmount: boolean | undefined;
    let isCancelledFn: (() => boolean) | undefined;
    let resolveAttempt: (() => void) | undefined;
    const attempt = vi.fn((isCancelled: () => boolean) => {
      isCancelledFn = isCancelled;
      observedBeforeUnmount = isCancelled();
      return new Promise<void>((resolve) => {
        resolveAttempt = resolve;
      });
    });
    const { unmount } = renderHook(() => useConnectedEffect(attempt, []));
    expect(observedBeforeUnmount).toBe(false);
    unmount();
    expect(isCancelledFn?.()).toBe(true);
    resolveAttempt?.();
  });
});
