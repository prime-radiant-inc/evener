import { SCOPED_HOST_READ_DEBOUNCE_MS } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { usePluginComponents } from "./usePluginComponents";

// The hook's own contract is the seam between two tested layers: the store's
// debounce/stale-answer rules (pinned by usePluginPreview's tests) and the
// read input's one-target unwrap (pinned by scopedHostRead's tests). What
// only this hook can pin is the wiring: it answers with the target's one
// result ROW, never the envelope, and issues the one-target batch.
test("answers with the target's result row after the debounce", async () => {
  vi.useFakeTimers();
  const fake = new FakeClient();
  const row = { plugin: "linter", marketplace: "acme", components: { skills: ["lint"] } };
  fake.on("evener/plugin/components", () => ({ results: [row] }));
  const { result } = renderHook(() =>
    usePluginComponents({ client: fake, target: { plugin: "linter", marketplace: "acme" } }),
  );
  expect(result.current.state).toEqual({ status: "loading" });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS);
    await Promise.resolve();
  });
  expect(result.current.state).toEqual({ status: "ready", response: row });
  expect(fake.calls).toEqual([
    { method: "evener/plugin/components", params: { targets: [{ plugin: "linter", marketplace: "acme" }] } },
  ]);
});

afterEach(() => {
  vi.useRealTimers();
});
