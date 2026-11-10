import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { RAIL_ACTIVITY_POLL_MS, useSessionActivityRead } from "./railActivity";

const ACTIVITY = {
  ref: "local:a",
  minutes: [0, 0, 0, 0, 0, 0, 0],
  runningSubagents: 0,
  latestIntent: "Reading the row tests.",
};

function readyWith(client: FakeClient): void {
  connectionStore.setState({ state: "ready", client });
}

beforeEach(() => {
  resetConnectionStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetConnectionStoreForTests();
});

test("a working session reads the hub's pulse", async () => {
  const client = new FakeClient();
  client.on("evener/activity/read", () => ({ sessions: [ACTIVITY] }));
  readyWith(client);

  const { result } = renderHook(() => useSessionActivityRead("local:a", true));

  await waitFor(() => expect(result.current?.activity.latestIntent).toBe(ACTIVITY.latestIntent));
});

test("the read is dropped when the connection leaves ready", async () => {
  const client = new FakeClient();
  client.on("evener/activity/read", () => ({ sessions: [ACTIVITY] }));
  readyWith(client);

  const { result } = renderHook(() => useSessionActivityRead("local:a", true));
  await waitFor(() => expect(result.current).not.toBeNull());

  await act(async () => {
    connectionStore.setState({ state: "reconnecting", client: null });
  });
  expect(result.current).toBeNull();
});

test("the read is dropped when the session stops working", async () => {
  const client = new FakeClient();
  client.on("evener/activity/read", () => ({ sessions: [ACTIVITY] }));
  readyWith(client);

  const { result, rerender } = renderHook(
    ({ active }: { active: boolean }) => useSessionActivityRead("local:a", active),
    {
      initialProps: { active: true },
    },
  );
  await waitFor(() => expect(result.current).not.toBeNull());

  rerender({ active: false });
  expect(result.current).toBeNull();

  // The old read is gone, not merely hidden: a new working stretch must ask
  // the hub again rather than briefly re-serving the previous turn's read.
  rerender({ active: true });
  expect(result.current).toBeNull();
  await waitFor(() => expect(result.current).not.toBeNull());
});

test("a hub without the method stops polling for good", async () => {
  vi.useFakeTimers();
  const client = new FakeClient();
  let calls = 0;
  client.on("evener/activity/read", () => {
    calls++;
    throw new WireError("method not found", -32601);
  });
  readyWith(client);

  renderHook(() => useSessionActivityRead("local:a", true));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(calls).toBe(1);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(RAIL_ACTIVITY_POLL_MS * 3);
  });
  expect(calls).toBe(1);
});
