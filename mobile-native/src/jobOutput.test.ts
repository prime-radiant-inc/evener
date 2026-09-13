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

it("loads the earlier page requested while a refresh is in flight", async () => {
  const requests: { beforeBytes?: number }[] = [];
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let holdNext = false;
  const client = {
    onNotification: () => () => {},
    request: async (_method, params) => {
      const { beforeBytes } = params as { beforeBytes?: number };
      requests.push({ beforeBytes });
      if (holdNext) {
        holdNext = false;
        await held;
      }
      return beforeBytes === undefined
        ? {
            data: {
              tail: "tail",
              totalBytes: 20,
              retainedStart: 14,
              truncated: true,
              hasEarlier: true,
            },
          }
        : {
            data: {
              tail: "earlier",
              totalBytes: 20,
              retainedStart: 8,
              truncated: true,
              hasEarlier: true,
            },
          };
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  await log.refresh();

  // A refresh is in flight when the user asks for the earlier page.
  holdNext = true;
  const refreshing = log.refresh();
  const earlier = log.loadEarlier();
  release();
  await Promise.all([refreshing, earlier]);

  expect(requests.map((r) => r.beforeBytes)).toEqual([
    undefined,
    undefined,
    14,
  ]);
  expect(log.getSnapshot().content).toBe("earliertail");
  expect(log.getSnapshot().loading).toBe(false);
});

it("refreshes after an earlier page requested first is in flight", async () => {
  const requests: { beforeBytes?: number }[] = [];
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let holdNext = false;
  const client = {
    onNotification: () => () => {},
    request: async (_method, params) => {
      const { beforeBytes } = params as { beforeBytes?: number };
      requests.push({ beforeBytes });
      if (holdNext) {
        holdNext = false;
        await held;
      }
      return {
        data: {
          tail: beforeBytes === undefined ? "tail" : "earlier",
          totalBytes: 20,
          retainedStart: beforeBytes === undefined ? 14 : 8,
          truncated: true,
          hasEarlier: true,
        },
      };
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  await log.refresh();

  // A reconnect refreshes without a user press while the earlier page is in
  // flight; that refresh must not be dropped either.
  holdNext = true;
  const earlier = log.loadEarlier();
  const refreshing = log.refresh();
  release();
  await Promise.all([earlier, refreshing]);

  expect(requests.map((r) => r.beforeBytes)).toEqual([undefined, 14, undefined]);
  expect(log.getSnapshot().content).toBe("tail");
});

it("clears the error when a queued refresh succeeds after a failed page", async () => {
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let holdNext = false;
  const client = {
    onNotification: () => () => {},
    request: async (_method, params) => {
      const { beforeBytes } = params as { beforeBytes?: number };
      if (holdNext) {
        holdNext = false;
        await held;
        throw new Error("offline");
      }
      return {
        data: {
          tail: beforeBytes === undefined ? "tail" : "earlier",
          totalBytes: 20,
          retainedStart: beforeBytes === undefined ? 14 : 8,
          truncated: true,
          hasEarlier: true,
        },
      };
    },
  } as ConversationClientLike;
  const log = new JobOutput(client, "local:owner", "job-id");
  await log.refresh();

  // The earlier page fails, and the refresh queued behind it succeeds in the
  // same load — which publishes without clearing the failure.
  holdNext = true;
  const earlier = log.loadEarlier();
  const refreshing = log.refresh();
  release();
  await Promise.all([earlier, refreshing]);

  expect(log.getSnapshot().content).toBe("tail");
  expect(log.getSnapshot().error).toBeNull();
});
