import { expect, test } from "vitest";
import { type ActivityClient, ActivityList } from "./activityList";

const tree = (continuation?: string) => ({
  revision: 1,
  root: {
    kind: "session" as const,
    sessionId: "session",
    ref: "local:session",
    label: "session",
    aggregate: "ended",
    counts: { active: 0, failed: 0, completed: 1, complete: !continuation },
    entries: [
      {
        kind: "shell" as const,
        job: {
          jobId: "job",
          ownerSessionId: "session",
          ownerRef: "local:session",
          type: "shell",
          status: "completed",
          terminal: true,
          background: false,
          hasOutput: false,
          description: "job",
          startedAt: "2026-01-01T00:00:00Z",
          outputBytes: 0,
        },
      },
    ],
    branch: continuation ? { continuation, truncated: true } : {},
  },
});

test("delegate updated notification refreshes and loadMore queues behind refresh", async () => {
  let notify: ((n: { method: string; params: { ref: string; threadId: string } }) => void) | undefined;
  const requests: unknown[] = [];
  let release!: () => void;
  const client = {
    onNotification(callback: typeof notify) {
      notify = callback;
      return () => undefined;
    },
    request(_method: string, params: unknown) {
      requests.push(params);
      if (requests.length === 1) return new Promise((resolve) => (release = () => resolve({ data: tree("next") })));
      return Promise.resolve({ data: tree() });
    },
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  list.start();
  notify?.({ method: "evener/delegate/updated", params: { ref: "local:session", threadId: "session" } });
  release();
  await list.refresh();
  expect(requests).toHaveLength(2);
  expect(list.getSnapshot().tree?.root.branch).toEqual({});
});
