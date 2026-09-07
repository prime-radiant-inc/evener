import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { JobOutput } from "./jobOutput";

it("pages output by server byte offsets and retains it on failure without duplicating ignored pages", async () => {
  const requests: unknown[] = [];
  let data = {
    tail: "末尾",
    totalBytes: 20,
    retainedStart: 14,
    truncated: true,
    hasEarlier: true,
  };
  let fail = false;
  const client = {
    onNotification: () => () => {},
    request: async (method, params) => {
      requests.push({ method, params });
      if (fail) throw new Error("offline");
      return { data };
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  await log.refresh();
  data = {
    tail: "prefix",
    totalBytes: 20,
    retainedStart: 8,
    truncated: true,
    hasEarlier: true,
  };
  await log.loadEarlier();
  expect(requests[1]).toEqual({
    method: "evener/jobs/output",
    params: { ref: "local:owner", jobId: "job-id", beforeBytes: 14 },
  });
  expect(log.getSnapshot().content).toBe("prefix末尾");
  fail = true;
  await log.refresh();
  expect(log.getSnapshot().content).toBe("prefix末尾");
  expect(log.getSnapshot().error).toBeTruthy();
  fail = false;
  await log.loadEarlier();
  expect(log.getSnapshot().content).toBe("prefix末尾");
  expect(log.getSnapshot().hasEarlier).toBe(false);
  data = {
    tail: "latest",
    totalBytes: 30,
    retainedStart: 24,
    truncated: true,
    hasEarlier: true,
  };
  await log.refresh();
  expect(log.getSnapshot().content).toBe("latest");
  expect(log.getSnapshot().error).toBeNull();
});

it("serializes output reads and discards responses after leaving the owning job", async () => {
  let complete!: (value: { data: unknown }) => void;
  let reads = 0;
  const client = {
    onNotification: () => () => {},
    request: () => {
      reads++;
      return new Promise((resolve) => {
        complete = resolve;
      });
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  const pending = log.refresh();
  void log.refresh();
  void log.loadEarlier();
  expect(reads).toBe(1);
  log.dispose();
  const prior = log.getSnapshot();
  complete({
    data: { tail: "late", totalBytes: 4, retainedStart: 0, truncated: false },
  });
  await pending;
  expect(log.getSnapshot()).toBe(prior);
});

it("rejects malformed output without losing the loaded page or advancing its byte cursor", async () => {
  let data: unknown = {
    tail: "日本語",
    totalBytes: 100,
    retainedStart: 91,
    truncated: true,
    hasEarlier: true,
  };
  const requests: unknown[] = [];
  const client = {
    onNotification: () => () => {},
    request: async (method, params) => {
      requests.push({ method, params });
      return { data };
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  await log.refresh();
  const retained = log.getSnapshot();
  data = {
    tail: "invalid",
    totalBytes: 100,
    retainedStart: 0.5,
    truncated: true,
    hasEarlier: true,
  };
  await log.loadEarlier();
  expect(log.getSnapshot()).toMatchObject({
    ...retained,
    error: expect.any(String),
  });
  data = {
    tail: "prefix",
    totalBytes: 100,
    retainedStart: 85,
    truncated: true,
    hasEarlier: true,
  };
  await log.loadEarlier();
  expect(requests.slice(1)).toEqual(
    Array(2).fill({
      method: "evener/jobs/output",
      params: { ref: "local:owner", jobId: "job-id", beforeBytes: 91 },
    }),
  );
  expect(log.getSnapshot()).toMatchObject({
    content: "prefix日本語",
    earliestStart: 85,
    error: null,
  });
});
