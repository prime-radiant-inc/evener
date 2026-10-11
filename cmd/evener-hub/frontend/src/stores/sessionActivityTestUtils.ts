import type {
  EvenerWatchInfo,
  JobActivityJob,
  SessionActivityContext,
  SessionActivityReadParams,
  SessionActivityScope,
  SessionActivitySummary,
  SessionDelegate,
  SessionWatch,
  Thread,
  ThreadReadResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";

export const activityRef = "remote:owner";
export function activityContext(ref = activityRef): SessionActivityContext {
  return {
    ref,
    sessionId: "owner",
    rootRef: ref,
    ancestors: [],
    ancestryKnown: true,
    epoch: "epoch",
    availability: "live",
  };
}
/** A summary for the scope asked about. A store refuses a reply for another
 * scope and retries it in the background, so a fake answers the scope it is
 * asked for: the session's own, or the subtree a subagent count reads. */
export function activitySummary(ref = activityRef, scope: SessionActivityScope = "session"): SessionActivitySummary {
  return {
    context: activityContext(ref),
    scope,
    delegates: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
    jobs: { known: true, total: 201, active: 2, failed: 0, completed: 199 },
    watches: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
  };
}
/** An `evener/thread/activity/read` fake answering the scope asked for. */
export function answerActivityRead({ ref, scope }: SessionActivityReadParams): SessionActivitySummary {
  return activitySummary(ref, scope);
}
export function activityDelegate(overrides: Partial<SessionDelegate> = {}): SessionDelegate {
  return {
    runGeneration: 1,
    delegateId: "delegate-1",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:child",
    description: "inspect",
    task: "inspect",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    terminal: false,
    resumable: true,
    ...overrides,
  };
}
export function activityThread(ref = activityRef): ThreadReadResponse {
  return {
    thread: {
      id: "owner",
      sessionId: "owner",
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
        ref,
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
  };
}
export function activityDetailsThread(ref = "remote:about-owner", overrides: Partial<Thread> = {}): ThreadReadResponse {
  const base = activityThread(ref).thread;
  return {
    thread: {
      ...base,
      id: "about-owner",
      modelProvider: "anthropic/claude-sonnet",
      createdAt: 1_780_000_000,
      updatedAt: 1_780_000_060,
      cwd: "/work/session",
      projectPath: "/work",
      gitInfo: { branch: "feature/overview" },
      evener: {
        ...base.evener,
        contextUsed: 42_000,
        contextWindow: 100_000,
        contextPressure: 0.42,
        workMillis: 4_200,
        usage: { inputTokens: 100_000, outputTokens: 20_000 },
        cost: "~$1.00",
      },
      ...overrides,
    },
  };
}
export function activityClient(): FakeClient {
  const client = new FakeClient("ready");
  client.on("thread/read", ({ ref }) => activityThread(ref));
  client.on("thread/unsubscribe", () => ({}));
  client.on("evener/thread/activity/read", answerActivityRead);
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: ref, rootRef: ref })],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [],
    page: { complete: true, issues: [] },
  }));
  return client;
}

export function activityJob(overrides: Partial<JobActivityJob> = {}): JobActivityJob {
  return {
    jobId: "job_raw",
    ownerSessionId: "owner",
    ownerRef: activityRef,
    type: "shell",
    status: "running",
    terminal: false,
    background: true,
    hasOutput: true,
    description: "run checks",
    command: "go test",
    startedAt: "2026-09-30T12:00:00Z",
    outputBytes: 8,
    transcriptRef: "job:authoritative",
    ...overrides,
  };
}
export function activityWatch(overrides: Partial<EvenerWatchInfo> = {}, receiverRef = activityRef): SessionWatch {
  const watch: EvenerWatchInfo = {
    id: "watch_raw",
    source: "job_raw",
    deliveries: 0,
    createdAt: "2026-09-30T12:00:00Z",
    active: true,
    ...overrides,
  };
  return { sourceRef: activityRef, ownerRef: receiverRef, receiverRef, state: watch.active ? "armed" : "ended", watch };
}
