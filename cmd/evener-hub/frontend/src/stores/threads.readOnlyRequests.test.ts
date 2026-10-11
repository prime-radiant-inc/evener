import "fake-indexeddb/auto";
import { type ConnectionState, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import { ConflictError, resetThreadsStoreForTests, setProductionEntryCacheForTests, threadsStore } from "./threads";

// These direct read-only RPCs exercise the real store client/readiness path,
// not transcript acquisition. No route selector is installed.
let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `read-only-requests-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});
function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

describe("useThreadsStore.listTasks", () => {
  // Fixtures use the task row's persisted snake_case wire fields.
  const TASKS_DATA = [
    { id: 1, type: "implement", description: "Wire up listModels/listTasks", prompt: "…", status: "done" },
    {
      id: 2,
      type: "verify",
      description: "Confirm tests pass",
      prompt: "…",
      status: "in_progress",
      depends_on: [1],
    },
  ];

  test("sends evener/tasks/list with {ref} and returns the raw data field, not the response wrapper", async () => {
    const fake = connectFakeClient();
    fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

    const result = await threadsStore.getState().listTasks("ref_a");

    const call = fake.calls.find((c) => c.method === "evener/tasks/list");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(result).toEqual(TASKS_DATA);
  });

  test("propagates a source-backed rejection (actionUnavailable) unchanged, not mapped to ConflictError", async () => {
    const fake = connectFakeClient();
    // Mirrors a source that omits the capability: appwire.Unavailable(...),
    // code -32014, evenerErrorInfo "actionUnavailable" - never a Conflict.
    fake.on("evener/tasks/list", () => {
      throw new WireError("remote source does not expose evener tasks", -32014, {
        evenerErrorInfo: "actionUnavailable",
      });
    });

    const rejection = threadsStore.getState().listTasks("ref_remote");
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toMatchObject({ evenerErrorInfo: "actionUnavailable" });
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listTasks("ref_a")).rejects.toThrow(/no client connected/i);
  });

  // Issue #195's RCA: listTasks is read-only, so it waits out a reconnect
  // (via requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listTasks("ref_a");
    let settled = false;
    pending.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    await flushUntil(() => false, 5);
    expect(settled).toBe(false); // still waiting, not rejected synchronously
    expect(fake.calls).toHaveLength(0); // never sent while reconnecting

    fake.emitReady();
    await expect(pending).resolves.toEqual(TASKS_DATA);
  });
});

describe("useThreadsStore.listJobs / jobOutput", () => {
  // JobsListResponse.Data is the legacy untyped recursive activity tree;
  // JobsOutputResponse.Data is the concrete JobOutputTail. These
  // fixtures mirror the current wire JSON field names verbatim.
  const JOBS_DATA = {
    revision: 5,
    root: {
      sessionId: "sess_root",
      ref: "ref_a",
      label: "Root",
      aggregate: "working",
      counts: { active: 2, failed: 0, completed: 1, complete: true },
      entries: [
        {
          kind: "shell",
          job: {
            jobId: "job_1",
            ownerSessionId: "sess_root",
            ownerRef: "ref_a",
            type: "shell",
            status: "completed",
            description: "run tests",
            command: "go test ./...",
            terminal: true,
            background: true,
            hasOutput: true,
            startedAt: "2026-07-31T12:00:00Z",
            endedAt: "2026-07-31T12:01:00Z",
            exitCode: 0,
            outputBytes: 123,
          },
        },
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_1",
            childSessionId: "sess_child",
            childRef: "ref_child",
            turns: [
              {
                jobId: "job_2",
                ownerSessionId: "sess_root",
                ownerRef: "ref_a",
                type: "delegate",
                status: "running",
                terminal: false,
                background: true,
                hasOutput: false,
                description: "scout",
                startedAt: "2026-07-31T12:05:00Z",
                outputBytes: 0,
              },
            ],
            branch: {},
          },
        },
      ],
      branch: {},
    },
  };
  const OUTPUT_DATA = { tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true };
  // The single-job read carries the activity-job shape verbatim (appwire/types.go's
  // JobActivityJob), including the untruncated command.
  const JOB_DATA = {
    jobId: "job_1",
    ownerSessionId: "sess_root",
    ownerRef: "ref_a",
    type: "shell",
    status: "completed",
    terminal: true,
    background: false,
    hasOutput: true,
    description: "run tests",
    command: "go test ./... -run Foo -count=1",
    startedAt: "2026-07-31T12:00:00Z",
    endedAt: "2026-07-31T12:01:00Z",
    exitCode: 0,
    outputBytes: 123,
  };

  test("listJobs sends evener/jobs/list with {ref} and returns the raw data field", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    const result = await threadsStore.getState().listJobs("ref_a");

    const call = fake.calls.find((c) => c.method === "evener/jobs/list");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(result).toEqual(JOBS_DATA);
  });

  test("listJobs includes continuation only when non-empty and keeps the AppWire method name", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    await threadsStore.getState().listJobs("ref_a", "page-2");
    await threadsStore.getState().listJobs("ref_a", "");

    const calls = fake.calls.filter((c) => c.method === "evener/jobs/list");
    expect(calls).toHaveLength(2);
    expect(calls[0]?.params).toEqual({ ref: "ref_a", continuation: "page-2" });
    expect(calls[1]?.params).toEqual({ ref: "ref_a" });
  });

  test("jobOutput sends evener/jobs/output with {ref, jobId} and returns the raw data field", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    const result = await threadsStore.getState().jobOutput("ref_a", "job_1");

    const call = fake.calls.find((c) => c.method === "evener/jobs/output");
    expect(call?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
    expect(result).toEqual(OUTPUT_DATA);
  });

  test("jobOutput passes beforeBytes and maxBytes through only when positive", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    await threadsStore.getState().jobOutput("ref_a", "job_1", 64, 256);
    await threadsStore.getState().jobOutput("ref_a", "job_1", 0, 0);

    const calls = fake.calls.filter((c) => c.method === "evener/jobs/output");
    expect(calls).toHaveLength(2);
    expect(calls[0]?.params).toEqual({ ref: "ref_a", jobId: "job_1", beforeBytes: 64, maxBytes: 256 });
    expect(calls[1]?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
  });

  test("jobGet sends evener/jobs/get with {ref, jobId} and returns the raw data field", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/get", () => ({ data: JOB_DATA }));

    const result = await threadsStore.getState().jobGet("ref_a", "job_1");

    const call = fake.calls.find((c) => c.method === "evener/jobs/get");
    expect(call?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
    expect(result).toEqual(JOB_DATA);
  });

  test("each job read throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listJobs("ref_a")).rejects.toThrow(/no client connected/i);
    await expect(threadsStore.getState().jobOutput("ref_a", "job_1")).rejects.toThrow(/no client connected/i);
    await expect(threadsStore.getState().jobGet("ref_a", "job_1")).rejects.toThrow(/no client connected/i);
  });

  // Issue #195's RCA: both are read-only, so they wait out a reconnect (via
  // requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("listJobs waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listJobs("ref_a");
    let settled = false;
    pending.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    await flushUntil(() => false, 5);
    expect(settled).toBe(false); // still waiting, not rejected synchronously
    expect(fake.calls).toHaveLength(0); // never sent while reconnecting

    fake.emitReady();
    await expect(pending).resolves.toEqual(JOBS_DATA);
  });

  test("jobOutput waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().jobOutput("ref_a", "job_1");
    let settled = false;
    pending.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    await flushUntil(() => false, 5);
    expect(settled).toBe(false);
    expect(fake.calls).toHaveLength(0);

    fake.emitReady();
    await expect(pending).resolves.toEqual(OUTPUT_DATA);
  });

  // requireReadyClient always returns a FRESH requireClient() read once
  // ready, never the client it started waiting on - a wait can span a
  // manual retry (shell/ConnectionBanner.tsx) that swaps in a genuinely
  // different client object while the read is still pending.
  test("a rewire mid-wait retries against the new client, not the stale one it started waiting on", async () => {
    const stale = connectFakeClient("reconnecting");
    const pending = threadsStore.getState().listJobs("ref_a");
    await flushUntil(() => false, 5);

    const fresh = new FakeClient("ready");
    fresh.on("evener/jobs/list", () => ({ data: JOBS_DATA }));
    connectionStore.getState().connect(fresh);

    await expect(pending).resolves.toEqual(JOBS_DATA);
    expect(fresh.calls.filter((c) => c.method === "evener/jobs/list")).toHaveLength(1);
    expect(stale.calls).toHaveLength(0); // the stale client never saw this call
  });
});
