import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ActivityJob, ActivityTree } from "../../../protocol/activityData";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import {
  activityPanelStore,
  EMPTY_ACTIVITY_PANEL_ENTRY,
  resetActivityPanelStoreForTests,
} from "../../../stores/activityPanel";
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

function activityTree(jobId = "job_x"): ActivityTree {
  const job: ActivityJob = {
    jobId,
    ownerSessionId: "s",
    ownerRef: SESSION_REF,
    type: "shell",
    status: "running",
    transcriptRef: `job:${jobId}`,
    terminal: false,
    background: true,
    hasOutput: false,
    description: "test job",
    startedAt: "2026-09-13T20:00:00Z",
    outputBytes: 0,
  };
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "s",
      ref: SESSION_REF,
      label: "root",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 0, complete: false },
      entries: [{ kind: "shell", job }],
      branch: {},
    },
  };
}

beforeEach(() => resetActivityPanelStoreForTests());

afterEach(() => {
  cleanup();
  resetActivityPanelStoreForTests();
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
  expect(result.current.get("watch_x")?.kind).toBe("watch");
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
  const entity = result.current.get("watch_x");
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
  const entity = result.current.get("watch_x");
  if (entity?.kind !== "watch") throw new Error("expected watch entity");
  expect(entity.watch.deliveries).toBe(2);
});

test("stale and ended load changes rebuild metadata against the retained tree", () => {
  const tree = activityTree();
  activityPanelStore.setState({
    entries: new Map([
      [
        SESSION_REF,
        {
          ...EMPTY_ACTIVITY_PANEL_ENTRY,
          load: { kind: "ready", tree },
        },
      ],
    ]),
  });
  const { result } = renderHook(() => useEntityView(SESSION_REF, thread([])));
  const freshView = result.current;
  expect(freshView.get("job_x")).toMatchObject({ stale: false, ended: false });

  act(() => {
    const entry = activityPanelStore.getState().entries.get(SESSION_REF);
    if (!entry) throw new Error("expected activity entry");
    activityPanelStore.setState({
      entries: new Map([
        [
          SESSION_REF,
          {
            ...entry,
            load: {
              kind: "ready",
              tree,
              staleError: { headline: "Refresh failed", sentence: "Refresh failed." },
            },
          },
        ],
      ]),
    });
  });
  const staleView = result.current;
  expect(staleView).not.toBe(freshView);
  expect(staleView.get("job_x")).toMatchObject({ stale: true, ended: false });

  act(() => {
    const entry = activityPanelStore.getState().entries.get(SESSION_REF);
    if (!entry) throw new Error("expected activity entry");
    activityPanelStore.setState({
      entries: new Map([[SESSION_REF, { ...entry, load: { kind: "ended", tree } }]]),
    });
  });
  expect(result.current).not.toBe(staleView);
  expect(result.current.get("job_x")).toMatchObject({ stale: false, ended: true });
});
