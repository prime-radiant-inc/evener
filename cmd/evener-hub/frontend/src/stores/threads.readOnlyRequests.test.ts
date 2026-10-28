import "fake-indexeddb/auto";
import {
  ClientNotReadyError,
  type ConnectionState,
  errorKind,
  type ModelListResponse,
  type Task,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import { resetThreadsStoreForTests, setProductionEntryCacheForTests, threadsStore } from "./threads";
import { ConflictError } from "./threadsErrors";

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

const settleCallerContinuations = nextMacrotask;

describe("useThreadsStore.listTasks", () => {
  // Fixtures use the task row's persisted snake_case wire fields.
  const TASKS_DATA: Task[] = [
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
  const OUTPUT_DATA = {
    offsetBytes: 6,
    bytesReturned: 4,
    totalBytes: 10,
    retainedStartBytes: 0,
    encoding: "utf8",
    data: "6789",
  };
  // The single-job read carries the activity-job shape verbatim (appwire/types_jobs.go's
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

  test("jobOutput preserves an explicit zero selector and omits a default page limit", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    await threadsStore.getState().jobOutput("ref_a", "job_1", 64, 256);
    await threadsStore.getState().jobOutput("ref_a", "job_1", 0, 0);

    const calls = fake.calls.filter((c) => c.method === "evener/jobs/output");
    expect(calls).toHaveLength(2);
    expect(calls[0]?.params).toEqual({ ref: "ref_a", jobId: "job_1", beforeBytes: 64, maxBytes: 256 });
    expect(calls[1]?.params).toEqual({ ref: "ref_a", jobId: "job_1", beforeBytes: 0 });
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

// listModels/listTasks (T1 addendum, sanctioned NEEDS_CONTEXT gap for the
// chrome stream): both are plain read-only wire calls with no turn-CAS
// concept - verified against every server-side handler
// (cmd/evener-hub/app_rpc.go's registerMiscHandlers, cmd/evener-hub/
// app source ListTasks implementations, and server/appwire_jobs_models.go's
// handleAppTasksList/handleAppModelList): none
// of them ever construct appwire.Conflict(). Neither action maps errors -
// a WireError (even one shaped like a Conflict, which cannot actually occur
// here) passes through unchanged, same as resolveEscalation above.
describe("useThreadsStore.listModels", () => {
  function modelListResponse(): ModelListResponse {
    return {
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5" },
        { provider: "openai", model: "gpt-5.5" },
      ],
      diagnostics: [{ provider: "ollama", message: "ollama: connection refused" }],
      recent: [{ provider: "anthropic", model: "claude-sonnet-4-5" }],
    };
  }

  test("sends model/list with no params and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const result = await threadsStore.getState().listModels();

    const call = fake.calls.find((c) => c.method === "model/list");
    expect(call?.params).toEqual({});
    expect(result).toEqual(modelListResponse());
  });

  test("caches across calls within the session - a second call does not re-request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    await threadsStore.getState().listModels();
    await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
  });

  test("concurrent calls before the first resolves share one request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const [a, b] = await Promise.all([threadsStore.getState().listModels(), threadsStore.getState().listModels()]);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
    expect(a).toEqual(modelListResponse());
    expect(b).toEqual(modelListResponse());
  });

  test("evener/auth/updated clears the cache so the next call re-requests", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("model/list", () => {
      call += 1;
      return { data: [{ provider: "google-vertex", model: `model-${call}` }] };
    });

    const before = await threadsStore.getState().listModels();
    // A stored credential can make new models discoverable (a Vertex
    // credential JSON enables the publisher-model listing), so the listing
    // cached before it is stale.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const after = await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(before.data[0]?.model).toBe("model-1");
    expect(after.data[0]?.model).toBe("model-2");
  });

  // deferredModelList scripts model/list to hang until the test resolves it,
  // one resolver per request in arrival order, so a test can interleave the
  // auth-updated notification with listings still in flight.
  function deferredModelList(fake: FakeClient): Array<(resp: ModelListResponse) => void> {
    const pending: Array<(resp: ModelListResponse) => void> = [];
    fake.on(
      "model/list",
      () =>
        new Promise<ModelListResponse>((resolve) => {
          pending.push(resolve);
        }),
    );
    return pending;
  }
  const stale: ModelListResponse = { data: [{ provider: "google-vertex", model: "stale" }] };
  const fresh: ModelListResponse = { data: [{ provider: "google-vertex", model: "fresh" }] };

  test("a listing in flight when evener/auth/updated arrives still answers its caller but does not become the cache", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    pending[0]?.(stale);
    expect((await first).data[0]?.model).toBe("stale");

    const after = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    pending[1]?.(fresh);
    expect((await after).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a caller arriving after evener/auth/updated does not de-dupe onto a pre-credential listing still in flight", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const second = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    expect(pending).toHaveLength(2);

    pending[0]?.(stale);
    pending[1]?.(fresh);
    expect((await first).data[0]?.model).toBe("stale");
    expect((await second).data[0]?.model).toBe("fresh");
  });

  test("a pre-credential listing settling does not evict the newer in-flight listing from the dedupe slot", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const second = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    pending[0]?.(stale);
    await first;

    // The third caller must share the second request, not start a third: a
    // task yield (not a turn count) lets any request it would have issued
    // reach the fake before the negative assertion.
    const third = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2);

    pending[1]?.(fresh);
    expect((await second).data[0]?.model).toBe("fresh");
    expect((await third).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("refresh:true bypasses the cache and issues a fresh request", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("model/list", () => {
      call += 1;
      return { data: [{ provider: "anthropic", model: `model-${call}` }] };
    });

    const first = await threadsStore.getState().listModels();
    const second = await threadsStore.getState().listModels(true);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(first.data[0]?.model).toBe("model-1");
    expect(second.data[0]?.model).toBe("model-2");
  });

  test("a refresh landing before an older in-flight request keeps the cache fresh - the stale late answer never overwrites it", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preConnection = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // A keyless/no-credential-change connection emits no evener/auth/updated
    // between the pre-connection listing and the post-connection refresh, so
    // the epoch guard alone cannot protect the cache here.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");
    pending[0]?.(stale);
    // The older request still answers its own caller; it must not become the cache.
    expect((await preConnection).data[0]?.model).toBe("stale");

    expect((await threadsStore.getState().listModels()).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a refresh supersedes the shared in-flight list so a concurrent caller awaits the newest request", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preRefresh = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // A refresh with no intervening evener/auth/updated (a keyless connection,
    // a config-only save) leaves the dedupe slot in place, so the slot must be
    // superseded here or the next caller joins the pre-refresh request.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    const concurrent = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2);

    pending[1]?.(fresh);
    pending[0]?.(stale);
    // The older request still answers its own caller.
    expect((await preRefresh).data[0]?.model).toBe("stale");
    expect((await refreshed).data[0]?.model).toBe("fresh");
    // The concurrent caller joined the refresh, not the pre-refresh listing.
    expect((await concurrent).data[0]?.model).toBe("fresh");
  });

  test("a request in flight across a test reset cannot repopulate the fresh cache", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const inFlight = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // The reset clears the cache and the in-flight slot; a request already in
    // flight must lose the cache write to the reset the way it loses it to a
    // newer request, or its late answer becomes the post-reset cache.
    resetThreadsStoreForTests();
    pending[0]?.(fresh);
    await inFlight;

    const listSpy = vi.fn(() => ({ data: [{ provider: "google-vertex", model: "after" }] }));
    fake.on("model/list", listSpy);
    const after = await threadsStore.getState().listModels();
    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(after.data[0]?.model).toBe("after");
  });

  test("a warm cache does not answer a non-refresh caller while a refresh is in flight", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);
    // Warm the cache with the pre-refresh listing.
    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    pending[0]?.(stale);
    expect((await first).data[0]?.model).toBe("stale");

    // A refresh is now in flight. A non-refresh caller arriving alongside it
    // must not be handed the warm cache: the refresh was issued precisely to
    // replace that listing, and two concurrent model pickers would otherwise
    // receive different (one stale, one fresh) results.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);
    const concurrent = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2); // joined the refresh, issued no third request

    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");
    expect((await concurrent).data[0]?.model).toBe("fresh");
  });

  test("an older in-flight request landing first still loses the cache to a later refresh", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preConnection = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    pending[0]?.(stale);
    expect((await preConnection).data[0]?.model).toBe("stale");
    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");

    expect((await threadsStore.getState().listModels()).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a failed call does not cache a rejected promise - the next call retries rather than repeating the same rejection", async () => {
    const fake = connectFakeClient();
    let shouldFail = true;
    fake.on("model/list", () => {
      if (shouldFail) throw new Error("boom");
      return modelListResponse();
    });

    await expect(threadsStore.getState().listModels()).rejects.toThrow("boom");

    shouldFail = false;
    const result = await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(result).toEqual(modelListResponse());
  });

  test("propagates a rejection unchanged - not mapped to ConflictError even when it is Conflict-shaped (model/list can never actually return one)", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => {
      throw new WireError("shouldn't happen", -32013, { evenerErrorInfo: "conflict" });
    });

    const rejection = threadsStore.getState().listModels();
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listModels()).rejects.toThrow(/no client connected/i);
  });

  test("resetThreadsStoreForTests clears the models cache, same as every other module-private cache", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);

    resetThreadsStoreForTests();

    const fake2 = connectFakeClient();
    fake2.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake2.calls.filter((c) => c.method === "model/list")).toHaveLength(1); // fresh fetch, not a stale cache hit
  });

  // Issue #195's RCA: read-only, so it waits out a reconnect (via
  // requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listModels();
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
    await expect(pending).resolves.toEqual(modelListResponse());
  });

  // The ready-wait is skipped entirely for a warm cache/inflight hit (see
  // listModels's own comment): a call that needs no wire round-trip must
  // not block on a reconnect it has no reason to care about.
  test("a cached response resolves immediately even while reconnecting, without waiting", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels(); // warms the cache
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);

    fake.emitStateChange("reconnecting");
    await expect(threadsStore.getState().listModels()).resolves.toEqual(modelListResponse());
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1); // no second wire call
  });
});

// requireReadyClient's own bounded wait, exercised directly through a
// read-only action (listJobs stands in for all five - same helper) rather
// than duplicated five times. Real timers elsewhere in this file resolve via
// scripted FakeClient events (emitReady, etc.), never elapsed time, so this
// is the one describe block that needs fake timers to advance past
// requireReadyClient's default 15s budget without the suite actually
// waiting 15 real seconds.
describe("useThreadsStore read-only ready-gating (requireReadyClient)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("explicit refresh keeps one readiness deadline across ready-to-rewire races", async () => {
    let client = connectFakeClient("reconnecting");
    const clients = [client];
    let outcome: unknown;
    const refreshing = threadsStore
      .getState()
      .refreshThread("ref_a")
      .then(
        () => {
          outcome = "resolved";
        },
        (error: unknown) => {
          outcome = error;
        },
      );

    for (let attempt = 0; attempt < 2; attempt += 1) {
      await vi.advanceTimersByTimeAsync(5_000);
      client.emitReady();
      // Ready resolves the helper; the connection changes before the refresh
      // continuation captures the current client and its ready epoch.
      await Promise.resolve();
      client = new FakeClient("reconnecting");
      clients.push(client);
      connectionStore.getState().connect(client);
    }
    await vi.advanceTimersByTimeAsync(5_000);

    expect(outcome).toBeInstanceOf(ClientNotReadyError);
    expect(errorKind(outcome)).toBe("hub-unreachable");
    await refreshing;
    expect(clients.flatMap((candidate) => candidate.calls)).toHaveLength(0);
  });

  test("a read call times out with a classified ClientNotReadyError if the client never becomes ready", async () => {
    connectFakeClient("reconnecting"); // never reaches ready in this test
    const pending = threadsStore.getState().listJobs("ref_a");
    let outcome: { ok: true } | { ok: false; err: unknown } | undefined;
    pending.then(
      () => {
        outcome = { ok: true };
      },
      (err: unknown) => {
        outcome = { ok: false, err };
      },
    );

    await vi.advanceTimersByTimeAsync(15_000);

    expect(outcome?.ok).toBe(false);
    if (outcome?.ok !== false) throw new Error("expected the read call to reject");
    expect(outcome.err).toBeInstanceOf(ClientNotReadyError);
    expect((outcome.err as Error).message).toMatch(/timed out waiting for a ready client after 15000ms/);
    // Distinguishable from AppwireClient's own synchronous rejection text,
    // but still classified as hub-unreachable so it gets the same friendly
    // toast (see stores/activitySummary.ts's refreshRoot).
    expect((outcome.err as Error).message).not.toMatch(/cannot call/);
    expect(errorKind(outcome.err)).toBe("hub-unreachable");
  });
});
