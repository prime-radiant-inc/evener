import { describe, expect, it } from "vitest";
import type {
  SandboxEscalationRequested,
  Thread,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { projectApproval } from "../../mobile/src/conversation/project";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { ApprovalControls } from "./approvalControls";

const pending: SandboxEscalationRequested = {
  threadId: "thread",
  ref: "local:s",
  escalationId: "approval",
  tool: "exec",
  mode: "workspace",
  kind: "read",
  deniedPath: "/outside",
  command: "cat /outside",
  partiallyRan: true,
  outputSoFar: "partial",
};
function boundary() {
  const thread: Thread = {
    id: "thread",
    sessionId: "s",
    preview: "",
    ephemeral: false,
    modelProvider: "scripted",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "active" },
    cwd: "/workspace",
    cliVersion: "test",
    source: "local",
    turns: [],
    evener: {
      ref: "local:s",
      instanceId: "instance",
      queue: { revision: 0 },
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: false,
        queue: true,
        goal: true,
        rename: true,
      },
      pendingEscalations: [pending],
    },
  };
  const client = {
    request: async () => ({ thread }),
    onNotification: () => () => {},
  } as unknown as ConversationClientLike;
  return { client, service: createConversationService(client) };
}
describe("native approval projection", () => {
  it("hydrates pending harness approvals and applies only matching live resolutions", async () => {
    const { service } = boundary();
    const store = createConversationStore();
    await store.getState().open(service, "local:s");
    expect(store.getState().conversation?.pendingApprovals).toEqual([
      projectApproval(pending),
    ]);
    store.getState().applyNotification({
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "other", ref: "other:s", escalationId: "approval" },
    });
    expect(store.getState().conversation?.pendingApprovals).toHaveLength(1);
    store.getState().applyNotification({
      method: "evener/sandbox/escalation/requested",
      params: { ...pending, deniedPath: "/updated" },
    });
    expect(store.getState().conversation?.pendingApprovals).toEqual([
      projectApproval({ ...pending, deniedPath: "/updated" }),
    ]);
    store.getState().applyNotification({
      method: "evener/sandbox/escalation/resolved",
      params: pending,
    });
    expect(store.getState().conversation?.pendingApprovals).toEqual([]);
  });
});

describe("approval decisions", () => {
  it("sends the displayed current approval once and refreshes after acknowledgement", async () => {
    let finish!: () => void;
    const sent: unknown[] = [];
    let refreshed = 0;
    const client = {
      request: async (method: string, params: unknown) => {
        sent.push({ method, params });
        await new Promise<void>((done) => {
          finish = done;
        });
        return {};
      },
    } as unknown as ConversationClientLike;
    const approval = projectApproval(pending);
    const controls = new ApprovalControls(
      client,
      "local:s",
      () => [approval],
      () => true,
      async () => {
        refreshed++;
      },
    );
    const request = controls.resolve(approval, true);
    await controls.resolve(approval, false);
    expect(sent).toEqual([
      {
        method: "evener/sandbox/escalation/resolve",
        params: { ref: "local:s", escalationId: "approval", approve: true },
      },
    ]);
    finish();
    await request;
    expect(refreshed).toBe(1);
    expect(controls.getSnapshot().pending).toBeNull();
  });
  it("rejects resolved, changed and old-owner approvals before dispatch", async () => {
    let calls = 0;
    let values = [projectApproval(pending)];
    let current = true;
    const displayed = projectApproval(pending);
    const client = {
      request: async () => {
        calls++;
        return {};
      },
    } as unknown as ConversationClientLike;
    const controls = new ApprovalControls(
      client,
      "local:s",
      () => values,
      () => current,
      async () => {},
    );
    values = [{ ...displayed, path: "/different" }];
    await controls.resolve(displayed, true);
    values = [];
    await controls.resolve(displayed, false);
    values = [displayed];
    current = false;
    await controls.resolve(displayed, true);
    current = true;
    controls.dispose();
    await controls.resolve(displayed, true);
    expect(calls).toBe(0);
  });
  it("retains failure and never repeats an unconfirmed decision", async () => {
    let calls = 0;
    const approval = projectApproval(pending);
    const client = {
      request: async () => {
        calls++;
        throw new Error("Lost acknowledgement");
      },
    } as unknown as ConversationClientLike;
    const controls = new ApprovalControls(
      client,
      "local:s",
      () => [approval],
      () => true,
      async () => {},
    );
    await controls.resolve(approval, false);
    expect(calls).toBe(1);
    expect(controls.getSnapshot().error).not.toBeNull();
  });
});

it("does not resurrect an approval resolved while an older snapshot is in flight", async () => {
  const base = boundary();
  let reads = 0;
  let release!: () => void;
  let started!: () => void;
  const began = new Promise<void>((resolve) => {
    started = resolve;
  });
  const client = {
    ...base.client,
    request: async () => {
      const response = await base.client.request("thread/read", {
        ref: "local:s",
        includeTurns: true,
      });
      if (++reads === 2) {
        started();
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      return response;
    },
  } as unknown as ConversationClientLike;
  const service = createConversationService(client),
    store = createConversationStore(),
    sink = createActivityStore().getState();
  await store.getState().openProjected(service, sink, "local:s");
  const refresh = store.getState().rehydrate(service, sink);
  await began;
  store.getState().applyNotification({
    method: "evener/sandbox/escalation/resolved",
    params: pending,
  });
  release();
  await refresh;
  expect(store.getState().conversation?.pendingApprovals).toEqual([]);
});
it("keeps resolutions delivered between the initial snapshot and its response", async () => {
  const base = boundary();
  let release!: () => void;
  let started!: () => void;
  let notification: Parameters<ConversationClientLike["onNotification"]>[0] =
    () => {};
  const began = new Promise<void>((resolve) => {
    started = resolve;
  });
  const client = {
    request: async () => {
      const response = await base.client.request("thread/read", {
        ref: "local:s",
        includeTurns: true,
      });
      started();
      await new Promise<void>((resolve) => {
        release = resolve;
      });
      return response;
    },
    onNotification: (listener: typeof notification) => {
      notification = listener;
      return () => {};
    },
  } as unknown as ConversationClientLike;
  const service = createConversationService(client),
    store = createConversationStore(),
    sink = createActivityStore().getState();
  const open = store.getState().openProjected(service, sink, "local:s");
  await began;
  notification({
    method: "evener/sandbox/escalation/resolved",
    params: pending,
  });
  release();
  await open;
  expect(store.getState().conversation?.pendingApprovals).toEqual([]);
});
it("unsubscribes a failed initial read", async () => {
  let unsubscribed = 0;
  const client = {
    request: async () => {
      throw new Error("Read failed");
    },
    onNotification: () => () => {
      unsubscribed++;
    },
  } as unknown as ConversationClientLike;
  const store = createConversationStore();
  await store
    .getState()
    .openProjected(
      createConversationService(client),
      createActivityStore().getState(),
      "local:s",
    );
  expect(store.getState().status).toBe("error");
  expect(unsubscribed).toBe(1);
});
