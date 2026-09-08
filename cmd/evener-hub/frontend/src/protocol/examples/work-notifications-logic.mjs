import assert from "node:assert/strict";
import { ActivityList } from "@evener/appwire-client";
import { observeNotifications } from "./bounded-notifications.mjs";

const METHODS = new Set([
  "evener/job/started",
  "evener/job/finished",
  "evener/delegate/updated",
  "evener/jobs/treeUpdated",
  "evener/task/updated",
  "evener/goal/updated",
]);
const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string" && value.trim().length > 0;
const integer = (value) => Number.isSafeInteger(value) && value >= 0;

function validateCommon(value) {
  assert.ok(isRecord(value) && text(value.threadId) && text(value.ref), "Invalid work notification identity.");
}

function decodeJob(value) {
  if (value?.reason !== undefined) assert.equal(typeof value.reason, "string", "Invalid work job reason.");
  assert.ok(
    isRecord(value) && text(value.jobId) && text(value.jobType) && text(value.status) && integer(value.outputBytes),
    "Invalid work job notification.",
  );
  return {
    jobId: value.jobId,
    jobType: value.jobType,
    status: value.status,
    outputBytes: value.outputBytes,
    ...(value.reason === undefined ? {} : { reason: value.reason }),
  };
}

function decodeDelegate(value) {
  assert.ok(
    isRecord(value) &&
      text(value.delegateId) &&
      text(value.status) &&
      text(value.lifecycle) &&
      text(value.phase) &&
      integer(value.projectionRevision) &&
      typeof value.resumable === "boolean" &&
      typeof value.needsAttention === "boolean",
    "Invalid delegate notification.",
  );
  return {
    delegateId: value.delegateId,
    status: value.status,
    lifecycle: value.lifecycle,
    phase: value.phase,
    projectionRevision: value.projectionRevision,
    resumable: value.resumable,
    needsAttention: value.needsAttention,
  };
}

function decodeTask(value) {
  assert.ok(isRecord(value) && integer(value.total) && integer(value.done), "Invalid task notification.");
  assert.ok(value.done <= value.total, "Invalid task counts.");
  for (const key of ["cancelled", "remaining"])
    if (value[key] !== undefined) assert.ok(integer(value[key]), `Invalid task ${key}.`);
  if (value.cancelled !== undefined) assert.ok(value.cancelled <= value.total, "Invalid task counts.");
  if (value.remaining !== undefined) assert.ok(value.remaining <= value.total, "Invalid task counts.");
  if (value.current !== undefined)
    assert.ok(
      isRecord(value.current) && integer(value.current.id) && text(value.current.description),
      "Invalid current task.",
    );
  return structuredClone(value);
}

function decodeGoal(value) {
  if (value === null) return null;
  assert.ok(isRecord(value) && text(value.status) && integer(value.iterations), "Invalid goal notification.");
  return structuredClone(value);
}

export function decodeWorkNotification(notification) {
  if (!isRecord(notification) || typeof notification.method !== "string") return null;
  if (!METHODS.has(notification.method)) return null;
  const params = notification.params;
  validateCommon(params);
  const event = { method: notification.method, threadId: params.threadId, ref: params.ref };
  switch (notification.method) {
    case "evener/job/started":
    case "evener/job/finished":
      assert.ok(Object.hasOwn(params, "job"), "Missing work job notification.");
      return { ...event, job: decodeJob(params.job) };
    case "evener/delegate/updated":
      assert.ok(Object.hasOwn(params, "delegate"), "Missing delegate notification.");
      return { ...event, delegate: decodeDelegate(params.delegate) };
    case "evener/jobs/treeUpdated":
      assert.ok(integer(params.revision), "Invalid jobs tree revision.");
      return { ...event, revision: params.revision };
    case "evener/task/updated":
      return { ...event, ...decodeTask(params) };
    case "evener/goal/updated":
      assert.ok(Object.hasOwn(params, "goal"), "Missing goal notification.");
      return { ...event, goal: decodeGoal(params.goal) };
    default:
      throw new Error("Unsupported work notification.");
  }
}

function validateThreadResponse(response, ref) {
  assert.ok(isRecord(response) && isRecord(response.thread), "Invalid thread read response.");
  const thread = response.thread;
  assert.ok(
    isRecord(thread.evener) && thread.evener.ref === ref && text(thread.id),
    "Thread read belongs to another session.",
  );
  return structuredClone(thread);
}

function validateTaskList(response) {
  assert.ok(isRecord(response) && Object.hasOwn(response, "data"), "Invalid task-list response.");
  if (response.data === null) return null;
  assert.ok(Array.isArray(response.data), "Invalid task list.");
  return structuredClone(response.data);
}

async function readActivity(hub, ref, threadId) {
  const list = new ActivityList(hub, ref, threadId);
  try {
    await list.refresh();
    const budget = 10;
    let pages = 1;
    while (pages < budget) {
      const branch = list.branches().find((candidate) => candidate.continuation);
      if (!branch) break;
      await list.loadMore(branch.id, branch.continuation);
      pages += 1;
    }
    const state = list.getSnapshot();
    const remainingBranches = list
      .branches()
      .map(({ id, continuation, truncated }) => ({ id, continuation, truncated }));
    return {
      state: state.unsupported
        ? "unavailable"
        : state.ended
          ? "ended"
          : state.error
            ? "failed"
            : remainingBranches.some((branch) => branch.continuation)
              ? "incomplete"
              : "read",
      tree: structuredClone(state.tree),
      pages,
      remainingBranches,
    };
  } finally {
    list.dispose();
  }
}

async function readWorkSnapshot(hub, ref) {
  const threadResponse = await hub.request("thread/read", { ref, includeTurns: false, subscribe: true });
  const thread = validateThreadResponse(threadResponse, ref);
  const [tasksResponse, jobs] = await Promise.all([
    hub.request("evener/tasks/list", { ref }),
    readActivity(hub, ref, thread.id),
  ]);
  return {
    ref,
    thread: {
      id: thread.id,
      evener: {
        ref,
        ...(Object.hasOwn(thread.evener, "goal") ? { goal: thread.evener.goal } : {}),
        ...(thread.evener.tasks ? { tasks: thread.evener.tasks } : {}),
        ...(thread.evener.diagnostics?.delegates ? { delegates: thread.evener.diagnostics.delegates } : {}),
      },
    },
    tasks: validateTaskList(tasksResponse),
    jobs,
  };
}

export async function runWorkNotifications(hub, input, options = {}) {
  assert.ok(
    isRecord(input) && Object.keys(input).every((key) => key === "ref") && text(input.ref),
    "Provide a work-notification reference.",
  );
  const captured = structuredClone(input);
  let subscribed = false;
  try {
    const readSnapshot = (client) => {
      subscribed = true;
      return readWorkSnapshot(client, captured.ref);
    };
    const result = await observeNotifications(hub, {
      methods: [...METHODS],
      decodeNotification: (notification) => {
        const event = decodeWorkNotification(notification);
        return event && event.ref === captured.ref ? event : null;
      },
      readSnapshot,
      ...options,
    });
    return { ...result, ref: captured.ref };
  } finally {
    if (subscribed) await hub.request("thread/unsubscribe", { ref: captured.ref }).catch(() => {});
  }
}

export function summarizeWorkNotifications(result) {
  return {
    outcome: result.outcome,
    ref: result.ref,
    eventCount: result.eventCount,
    overflow: result.overflow,
    changedDuringReadback: result.changedDuringReadback,
    taskAvailable: result.readback?.tasks !== null,
    jobState: result.readback?.jobs?.state ?? null,
    connectionInterrupted: result.connectionInterrupted,
  };
}
