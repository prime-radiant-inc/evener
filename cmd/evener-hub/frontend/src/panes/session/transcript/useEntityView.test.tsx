import type { ItemModel, ThreadModel, TurnModel } from "@evener/appwire-client";
import { findEntityView } from "@evener/appwire-client";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { sessionActivitySnapshot, useSessionActivity } from "../../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityJob,
  activitySummary,
} from "../../../stores/sessionActivityTestUtils";
import { useEntityView } from "./useEntityView";

const SESSION_REF = "local:s";

function watchItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "watch-item",
    turnId: "turn-1",
    position: { entry: 1, item: 1 },
    type: "commandExecution",
    text: "",
    toolName: "job_watch",
    argumentsJSON: '{"job_id":"job_x"}',
    output: '{"watch_id":"watch_x","deliveries":1}',
    raw: { watch_id: "watch_x", watching: true, deliveries: 1 },
    status: "completed",
    ...overrides,
  };
}

function prose(text: string): ItemModel {
  return {
    id: "prose-item",
    turnId: "turn-1",
    type: "agentMessage",
    text,
    status: "completed",
  };
}

function turn(items: ItemModel[]): TurnModel {
  return { id: "turn-1", status: "completed", items };
}

function thread(turns: TurnModel[]): ThreadModel {
  return { ref: SESSION_REF, turns } as unknown as ThreadModel;
}

beforeEach(() => connectionStore.setState({ client: null, state: "idle" }));
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
});

test("prose-only turn replacement preserves the derived map identity", () => {
  const watch = watchItem();
  const firstModel = thread([turn([prose("first"), watch])]);
  const { result, rerender } = renderHook(({ model }) => useEntityView(SESSION_REF, model), {
    initialProps: { model: firstModel },
  });
  const firstView = result.current;

  rerender({ model: thread([turn([prose("replacement prose"), { ...watch }])]) });

  expect(result.current).toBe(firstView);
  expect(findEntityView(result.current, "watch", "watch_x", SESSION_REF)?.kind).toBe("watch");
});

test("completed watch output enrichment rebuilds the derived map", () => {
  const firstItem = watchItem();
  const { result, rerender } = renderHook(({ model }) => useEntityView(SESSION_REF, model), {
    initialProps: { model: thread([turn([firstItem])]) },
  });
  const firstView = result.current;

  rerender({
    model: thread([
      turn([
        {
          ...firstItem,
          output: '{"watch_id":"watch_x","deliveries":2}',
          raw: { watch_id: "watch_x", watching: true, deliveries: 2 },
        },
      ]),
    ]),
  });

  expect(result.current).not.toBe(firstView);
  const entity = findEntityView(result.current, "watch", "watch_x", SESSION_REF);
  if (entity?.kind !== "watch") throw new Error("expected watch entity");
  expect(entity.watch.deliveries).toBe(2);
});

test("raw-only watch summary enrichment rebuilds the derived map", () => {
  const firstItem = watchItem();
  const { result, rerender } = renderHook(({ model }) => useEntityView(SESSION_REF, model), {
    initialProps: { model: thread([turn([firstItem])]) },
  });
  const firstView = result.current;

  rerender({
    model: thread([
      turn([
        {
          ...firstItem,
          raw: { watch_id: "watch_x", watching: true, deliveries: 2 },
        },
      ]),
    ]),
  });

  expect(result.current).not.toBe(firstView);
  const entity = findEntityView(result.current, "watch", "watch_x", SESSION_REF);
  if (entity?.kind !== "watch") throw new Error("expected watch entity");
  expect(entity.watch.deliveries).toBe(2);
});

test("transcript retains its shared collection demand after tab disposal and publishes retained recovery metadata", async () => {
  const client = activityClient();
  const context = { ...activityContext(SESSION_REF), availability: "retained" as const };
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...activitySummary(SESSION_REF, scope), context }));
  client.on("evener/thread/jobs/list", () => ({
    context,
    scope: "session",
    jobs: [activityJob({ ownerRef: SESSION_REF, description: "retained job" })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  const transcript = renderHook(() => useEntityView(SESSION_REF, thread([])));
  const tab = renderHook(() => useSessionActivity(SESSION_REF, "session", "jobs"));
  await waitFor(() =>
    expect(findEntityView(transcript.result.current, "job", "job_raw", SESSION_REF)).toMatchObject({
      stale: false,
      ended: true,
    }),
  );
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/thread/watches/list")).toHaveLength(0);
  tab.unmount();
  client.on("evener/thread/jobs/list", () => {
    throw new Error("temporary source");
  });
  act(() => client.emitNotification(activityChangedNotification({ ref: SESSION_REF, threadId: "owner" }, ["jobs"])));
  await waitFor(() =>
    expect(findEntityView(transcript.result.current, "job", "job_raw", SESSION_REF)).toMatchObject({
      stale: true,
      ended: true,
    }),
  );
  client.on("evener/thread/jobs/list", () => ({
    context,
    scope: "session",
    jobs: [activityJob({ ownerRef: SESSION_REF })],
    page: { complete: true, issues: [] },
  }));
  act(() => client.emitNotification(activityChangedNotification({ ref: SESSION_REF, threadId: "owner" }, ["jobs"])));
  await waitFor(() =>
    expect(findEntityView(transcript.result.current, "job", "job_raw", SESSION_REF)).toMatchObject({
      stale: false,
      ended: true,
    }),
  );
  transcript.unmount();
  expect(sessionActivitySnapshot(client, SESSION_REF, "session")).toBeNull();
  const calls = client.calls.length;
  act(() => client.emitNotification(activityChangedNotification({ ref: SESSION_REF, threadId: "owner" }, ["jobs"])));
  expect(client.calls).toHaveLength(calls);
});
