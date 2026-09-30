import type { SessionActivityStore } from "./sessionActivityStore";
import { FakeClient } from "./testing/fakeClient";
import type {
  JobActivityJob,
  SessionActivityContext,
  SessionActivityScope,
  SessionActivitySummary,
  SessionDelegate,
  SessionJobsResponse,
  ThreadReadResponse,
} from "./types.gen";
export const activityRef = "remote:session";
export const activityContext = (epoch = "epoch-1"): SessionActivityContext => ({
  ref: activityRef,
  sessionId: "session",
  rootRef: activityRef,
  ancestors: [],
  ancestryKnown: true,
  epoch,
  availability: "live",
});
export const summaryFixture = (scope: SessionActivityScope = "session"): SessionActivitySummary => ({
  context: activityContext(),
  scope,
  delegates: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
  jobs: { known: true, total: 201, active: 2, failed: 1, completed: 198 },
  watches: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
});
export const jobFixture = (jobId = "shell-1", status = "running"): JobActivityJob => ({
  jobId,
  ownerSessionId: "session",
  ownerRef: activityRef,
  type: "shell",
  status,
  terminal: status !== "running",
  background: true,
  hasOutput: false,
  description: jobId,
  startedAt: "2026-09-30T12:00:00Z",
  outputBytes: 0,
});
export const delegateFixture = (delegateId = "delegate-1"): SessionDelegate => ({
  delegateId,
  ownerRef: activityRef,
  rootRef: activityRef,
  childRef: `remote:${delegateId}`,
  description: "work",
  task: "inspect",
  type: "agent",
  lifecycle: "idle",
  phase: "done",
  status: "completed",
  terminal: true,
  resumable: true,
});
export const jobsFixture = (
  jobs: JobActivityJob[] = [jobFixture()],
  nextCursor?: string,
  epoch = "epoch-1",
): SessionJobsResponse => ({
  context: activityContext(epoch),
  scope: "session",
  jobs,
  page: { complete: nextCursor === undefined, issues: [], ...(nextCursor === undefined ? {} : { nextCursor }) },
});
export const threadFixture = (): ThreadReadResponse => ({
  thread: {
    id: "session",
    sessionId: "session",
    preview: "",
    ephemeral: false,
    modelProvider: "scripted",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "idle" },
    cwd: "/scratch",
    cliVersion: "test",
    source: "evener",
    evener: {
      ref: activityRef,
      capabilities: {
        send: false,
        steer: false,
        interrupt: false,
        compact: false,
        clear: false,
        forkFromTurn: false,
        shutdown: false,
        changeModel: false,
        changeVisionModel: false,
        queue: false,
        goal: false,
        sharedNotes: false,
        rename: false,
      },
      queue: { revision: 0 },
    },
  },
});
export function activityClient(state: "ready" | "connecting" = "ready"): FakeClient {
  const client = new FakeClient(state);
  client.on("thread/read", () => threadFixture());
  client.on("thread/unsubscribe", () => ({}));
  client.on("evener/thread/activity/read", ({ scope }) => summaryFixture(scope));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateFixture()],
  }));
  client.on("evener/thread/jobs/list", () => jobsFixture());
  client.on("evener/thread/watches/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    watches: [],
  }));
  return client;
}
export function activityChanged(
  client: FakeClient,
  resources: ("summary" | "delegates" | "jobs" | "watches")[],
  sessionId = "session",
  ref = activityRef,
): void {
  client.emitNotification({
    method: "evener/thread/activity/changed",
    params: { threadId: "session", ref, sessionId, resources },
  });
}
export async function activityState(store: SessionActivityStore, ready: () => boolean): Promise<void> {
  if (ready()) return;
  await new Promise<void>((resolve) => {
    const release = store.subscribe(() => {
      if (ready()) {
        release();
        resolve();
      }
    });
  });
}
