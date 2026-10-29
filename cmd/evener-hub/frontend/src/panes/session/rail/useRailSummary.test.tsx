// @vitest-environment jsdom

// The rail summary hook is the only owner of the per-ref job projection. These
// cases drive the real hook and then the real model merge, so a cached job
// lane that never refreshes fails here instead of looking complete.
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { railModelFromSummaryAndTurns } from "./railModel";
import {
  type RailJobHistory,
  type RailJobInterval,
  type RailSummary,
  RailSummaryCache,
  type RailSummaryLoader,
} from "./railSummary";
import { useRailSummary } from "./useRailSummary";

const REF = "remote:west";

function job(overrides: Partial<RailJobInterval> = {}): RailJobInterval {
  return { jobId: "job_a", startedAt: 1_500, finishedAt: null, status: "running", ...overrides };
}

function summary(overrides: Partial<RailSummary> = {}): RailSummary {
  return {
    turns: [{ startedAt: 1_000, userInput: true, steering: false, error: false, in: 10, out: 5, resultBytes: 0 }],
    jobs: [job()],
    jobHistory: { status: "complete", revision: 3 },
    ...overrides,
  };
}

interface HarnessProps {
  jobsUpdatedAt: number | null;
  jobsTreeRevision: number | null;
  hydration: number;
  terminal?: boolean;
}

function renderSummary(load: RailSummaryLoader, cache = new RailSummaryCache()) {
  return (props: HarnessProps) =>
    useRailSummary({
      ref: REF,
      load,
      jobsUpdatedAt: props.jobsUpdatedAt,
      jobsTreeRevision: props.jobsTreeRevision,
      hydration: props.hydration,
      terminal: props.terminal ?? false,
      cache,
    });
}

const settled: HarnessProps = { jobsUpdatedAt: 1, jobsTreeRevision: null, hydration: 0 };

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("useRailSummary cache", () => {
  test("loads once on mount and serves the cached summary to a remount", async () => {
    const load = vi.fn(async () => summary());
    const render = renderSummary(load);
    const first = renderHook(render, { initialProps: settled });
    await waitFor(() => expect(first.result.current.summary).not.toBeNull());
    expect(load).toHaveBeenCalledTimes(1);

    first.unmount();
    const second = renderHook(render, { initialProps: settled });
    expect(second.result.current.summary?.jobs).toHaveLength(1);
    expect(load).toHaveBeenCalledTimes(1);
  });

  test("a rerender that changes no job signal does not refetch, even with a new loader identity", async () => {
    const load = vi.fn(async (_ref: string) => summary());
    const cache = new RailSummaryCache();
    let current = load;
    const view = renderHook(
      (props: HarnessProps) =>
        useRailSummary({
          ref: REF,
          load: (ref) => current(ref),
          jobsUpdatedAt: props.jobsUpdatedAt,
          jobsTreeRevision: props.jobsTreeRevision,
          hydration: props.hydration,
          cache,
        }),
      { initialProps: settled },
    );
    await waitFor(() => expect(view.result.current.summary).not.toBeNull());

    const nextLoader = vi.fn(async (_ref: string) => summary({ jobs: [] }));
    current = nextLoader;
    view.rerender({ ...settled });
    expect(load).toHaveBeenCalledTimes(1);
    expect(nextLoader).not.toHaveBeenCalled();
  });

  test("an ended session keeps its terminal cache across job signals and reconnect hydrations", async () => {
    const load = vi.fn(async () => summary());
    const view = renderHook(renderSummary(load), { initialProps: { ...settled, terminal: true } });
    await waitFor(() => expect(view.result.current.summary).not.toBeNull());

    view.rerender({ jobsUpdatedAt: 9, jobsTreeRevision: 4, hydration: 2, terminal: true });
    expect(load).toHaveBeenCalledTimes(1);
    expect(view.result.current.summary?.jobs).toHaveLength(1);
  });

  test("a terminal mount refreshes an entry captured while the session was still live", async () => {
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ finishedAt: 8_000, status: "finished" })],
          jobHistory: { status: "complete", revision: 4 },
        }),
      );
    const cache = new RailSummaryCache();
    const render = renderSummary(load, cache);
    const live = renderHook(render, { initialProps: settled });
    await waitFor(() => expect(live.result.current.summary?.jobs[0]?.finishedAt).toBeNull());
    live.unmount();

    const ended = renderHook(render, { initialProps: { ...settled, hydration: 1, terminal: true } });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(ended.result.current.summary?.jobs[0]?.finishedAt).toBe(8_000));

    ended.rerender({ jobsUpdatedAt: 9, jobsTreeRevision: 3, hydration: 2, terminal: true });
    expect(load).toHaveBeenCalledTimes(2);
  });

  test("a job-only failure stays degraded on remount instead of claiming complete history", async () => {
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockRejectedValueOnce(new Error("job lane damaged"));
    const cache = new RailSummaryCache();
    const render = renderSummary(load, cache);
    const first = renderHook(render, { initialProps: settled });
    await waitFor(() => expect(first.result.current.summary).not.toBeNull());

    first.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(first.result.current.jobHistory?.status).toBe("degraded"));
    first.unmount();

    const second = renderHook(render, { initialProps: { ...settled, jobsUpdatedAt: 2 } });
    expect(second.result.current.jobHistory?.status).toBe("degraded");
    expect(load).toHaveBeenCalledTimes(2);
  });

  test("clearing the cache makes the next mount load again", async () => {
    const load = vi.fn(async () => summary());
    const cache = new RailSummaryCache();
    const render = renderSummary(load, cache);
    const first = renderHook(render, { initialProps: settled });
    await waitFor(() => expect(first.result.current.summary).not.toBeNull());
    first.unmount();

    cache.clear();
    const second = renderHook(render, { initialProps: settled });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    expect(second.result.current.summary).not.toBeNull();
  });
});

describe("useRailSummary refresh", () => {
  test("a job started after the cached summary refreshes the lane in the merged model", async () => {
    const load = vi.fn(async () => summary());
    load.mockResolvedValueOnce(summary());
    load.mockResolvedValueOnce(
      summary({
        jobs: [job({ finishedAt: 2_000, status: "finished" }), job({ jobId: "job_b", startedAt: 3_000 })],
        jobHistory: { status: "complete", revision: 4 },
      }),
    );
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.summary?.jobs).toHaveLength(1));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(view.result.current.summary?.jobs).toHaveLength(2));

    const history = view.result.current.jobHistory;
    expect(history).not.toBeNull();
    const model = railModelFromSummaryAndTurns(view.result.current.summary, [], history as RailJobHistory);
    expect(model.jobs.map((entry) => [entry.jobId, entry.finishedAt])).toEqual([
      ["job_a", 2_000],
      ["job_b", null],
    ]);
    expect(model.jobHistory.revision).toBe(4);
  });

  test("a job finished after the cached summary turns the running lane terminal", async () => {
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockResolvedValueOnce(summary({ jobs: [job({ finishedAt: 4_000, exitCode: 0, status: "finished" })] }));
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.summary).not.toBeNull());

    view.rerender({ ...settled, jobsTreeRevision: 1 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.finishedAt).toBe(4_000));

    const history = view.result.current.jobHistory;
    expect(history).not.toBeNull();
    const model = railModelFromSummaryAndTurns(view.result.current.summary, [], history as RailJobHistory);
    expect(model.jobs[0]?.finishedAt).toBe(4_000);
    expect(model.jobs[0]?.exitCode).toBe(0);
  });

  test("a session that ends while mounted refreshes once to capture the final job lane", async () => {
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ finishedAt: 9_000, exitCode: 0, status: "finished" })],
          jobHistory: { status: "complete", revision: 5 },
        }),
      );
    const view = renderHook(renderSummary(load), { initialProps: { ...settled, terminal: false } });
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.finishedAt).toBeNull());

    view.rerender({ ...settled, terminal: true });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.finishedAt).toBe(9_000));

    view.rerender({ jobsUpdatedAt: 7, jobsTreeRevision: 3, hydration: 2, terminal: true });
    expect(load).toHaveBeenCalledTimes(2);
  });

  test("an older revision never replaces a newer cached projection and asks once more", async () => {
    const load = vi
      .fn(async () => summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockResolvedValueOnce(summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockResolvedValueOnce(
        summary({ jobs: [job({ jobId: "job_old" })], jobHistory: { status: "complete", revision: 2 } }),
      )
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ jobId: "job_new", startedAt: 8_000 })],
          jobHistory: { status: "complete", revision: 6 },
        }),
      );
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(5));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(6));
    expect(load).toHaveBeenCalledTimes(3);
    expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_new");
  });

  test("an unversioned or repeated older revision stops after one retry with the cached lane intact", async () => {
    const unversioned = () =>
      summary({ jobs: [job({ jobId: "job_old" })], jobHistory: { status: "complete", revision: null } });
    const load = vi
      .fn(async () => summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockResolvedValueOnce(summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockImplementationOnce(async () => unversioned())
      .mockImplementationOnce(async () => unversioned());
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(5));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_a");
    expect(view.result.current.jobHistory?.revision).toBe(5);
    expect(view.result.current.jobHistory?.status).toBe("degraded");
  });

  test("a later job signal gets a fresh revision-retry budget after recovery is exhausted", async () => {
    const unversioned = () =>
      summary({ jobs: [job({ jobId: "job_old" })], jobHistory: { status: "complete", revision: null } });
    const load = vi
      .fn(async () => summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockResolvedValueOnce(summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockImplementationOnce(async () => unversioned())
      .mockImplementationOnce(async () => unversioned())
      .mockImplementationOnce(async () => unversioned())
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ jobId: "job_new", startedAt: 8_000 })],
          jobHistory: { status: "complete", revision: 6 },
        }),
      );
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(5));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(view.result.current.jobHistory?.status).toBe("degraded"));

    view.rerender({ ...settled, jobsUpdatedAt: 3 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(5));
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(6));
    expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_new");
  });

  test("a signal change during a stale request leaves the new cycle its own retry", async () => {
    const pendingStale = deferred<RailSummary>();
    const pendingCycleThree = deferred<RailSummary>();
    const unversioned = () =>
      summary({ jobs: [job({ jobId: "job_old" })], jobHistory: { status: "complete", revision: null } });
    const load = vi
      .fn(async () => summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockImplementationOnce(async () => summary({ jobHistory: { status: "complete", revision: 5 } }))
      .mockReturnValueOnce(pendingStale.promise)
      .mockReturnValueOnce(pendingCycleThree.promise)
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ jobId: "job_new", startedAt: 8_000 })],
          jobHistory: { status: "complete", revision: 6 },
        }),
      );
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(5));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    view.rerender({ ...settled, jobsUpdatedAt: 3 });
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () => pendingStale.resolve(unversioned()));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    // The older cycle's stale answer did not spend cycle three's retry.
    expect(load).toHaveBeenCalledTimes(3);

    await act(async () => pendingCycleThree.resolve(unversioned()));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(4));
    await waitFor(() => expect(view.result.current.jobHistory?.revision).toBe(6));
    expect(load).toHaveBeenCalledTimes(4);
  });

  test("a cold mount reports no lane status until the first projection arrives", async () => {
    const pending = deferred<RailSummary>();
    const load = vi.fn().mockReturnValueOnce(pending.promise);
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    expect(view.result.current.jobHistory).toBeNull();

    await act(async () => pending.resolve(summary()));
    await waitFor(() => expect(view.result.current.jobHistory?.status).toBe("complete"));
  });
});

describe("useRailSummary reconnect", () => {
  test("a hydration bump refetches and a pre-reconnect response cannot replace the new generation", async () => {
    const stale = deferred<RailSummary>();
    const fresh = deferred<RailSummary>();
    const load = vi.fn().mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));

    view.rerender({ ...settled, hydration: 1 });
    expect(load).toHaveBeenCalledTimes(1); // the reconnect refresh waits for the open request

    await act(async () => stale.resolve(summary({ jobs: [job({ jobId: "job_stale" })] })));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    // The pre-reconnect response was fenced: it never became the visible lane.
    expect(view.result.current.summary).toBeNull();

    await act(async () =>
      fresh.resolve(
        summary({
          jobs: [job({ jobId: "job_fresh", startedAt: 5_000 })],
          jobHistory: { status: "complete", revision: 5 },
        }),
      ),
    );
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_fresh"));
  });
});

describe("useRailSummary degradation", () => {
  test("a job-only refresh failure keeps the cached turns and reports a degraded lane", async () => {
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockRejectedValueOnce(new Error("job lane damaged"));
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.summary?.turns).toHaveLength(1));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(view.result.current.jobHistory?.status).toBe("degraded"));

    expect(view.result.current.failure).toBe("job-only");
    expect(view.result.current.summary?.turns).toHaveLength(1);
    expect(view.result.current.summary?.jobs).toHaveLength(1);
  });

  test("a failed first load reports a fatal lane and refresh retries it", async () => {
    const load = vi
      .fn(async () => summary())
      .mockRejectedValueOnce(new Error("summary unavailable"))
      .mockResolvedValueOnce(summary());
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.failure).toBe("fatal"));
    expect(view.result.current.summary).toBeNull();

    await act(async () => view.result.current.refresh());
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(view.result.current.summary?.turns).toHaveLength(1));
    expect(view.result.current.jobHistory?.status).toBe("complete");
  });

  test("a loader that throws synchronously reports a fatal lane instead of escaping", async () => {
    const load = vi.fn((_ref: string) => {
      throw new Error("loader exploded");
    });
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(view.result.current.failure).toBe("fatal"));
    expect(view.result.current.jobHistory?.status).toBe("fatal");
  });
});

describe("useRailSummary coalescing", () => {
  test("a burst of job signals becomes one trailing refresh with no overlapping request", async () => {
    const second = deferred<RailSummary>();
    const third = summary({
      jobs: [job({ finishedAt: 2_000, status: "finished" }), job({ jobId: "job_c", startedAt: 6_000 })],
      jobHistory: { status: "complete", revision: 6 },
    });
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockReturnValueOnce(second.promise)
      .mockResolvedValueOnce(third);

    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));

    view.rerender({ ...settled, jobsUpdatedAt: 2 });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    view.rerender({ ...settled, jobsUpdatedAt: 3 });
    view.rerender({ ...settled, jobsUpdatedAt: 3, jobsTreeRevision: 2 });
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () => second.resolve(summary({ jobs: [job()], jobHistory: { status: "complete", revision: 5 } })));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(view.result.current.summary?.jobs).toHaveLength(2));
    expect(view.result.current.jobHistory?.revision).toBe(6);
  });
});

describe("useRailSummary lifecycle", () => {
  test("a ref change fences the old ref's response and loads the new ref", async () => {
    const old = deferred<RailSummary>();
    const fresh = deferred<RailSummary>();
    const load = vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    const cache = new RailSummaryCache();
    const view = renderHook(
      (props: { ref: string }) =>
        useRailSummary({
          ref: props.ref,
          load: (ref) => load(ref),
          jobsUpdatedAt: 1,
          jobsTreeRevision: null,
          hydration: 0,
          cache,
        }),
      { initialProps: { ref: "remote:old" } },
    );
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));

    view.rerender({ ref: "remote:new" });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));

    await act(async () => old.resolve(summary({ jobs: [job({ jobId: "job_old" })] })));
    expect(view.result.current.summary).toBeNull();

    await act(async () => fresh.resolve(summary({ jobs: [job({ jobId: "job_new" })] })));
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_new"));
  });

  test("unmount aborts the open request", async () => {
    const pending = deferred<RailSummary>();
    let signal: AbortSignal | undefined;
    const load = vi.fn((_ref: string, context: { signal: AbortSignal }) => {
      signal = context.signal;
      return pending.promise;
    });
    const view = renderHook(renderSummary(load), { initialProps: settled });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));

    view.unmount();
    expect(signal?.aborted).toBe(true);
  });

  test("a reconnect during a terminal retry clears the spinner instead of freezing it", async () => {
    const retry = deferred<RailSummary>();
    const load = vi
      .fn(async () => summary())
      .mockResolvedValueOnce(summary())
      .mockReturnValueOnce(retry.promise);
    const cache = new RailSummaryCache();
    const render = renderSummary(load, cache);
    const view = renderHook(render, { initialProps: { ...settled, terminal: true } });
    await waitFor(() => expect(view.result.current.summary).not.toBeNull());

    await act(async () => view.result.current.refresh());
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    view.rerender({ ...settled, hydration: 1, terminal: true });

    await act(async () => retry.resolve(summary({ jobs: [job({ jobId: "job_retry" })] })));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    // The pre-reconnect retry was fenced; the terminal cache still stands.
    expect(view.result.current.summary?.jobs[0]?.jobId).toBe("job_a");
  });

  test("a terminal refresh queued behind an in-flight live request survives a later job signal", async () => {
    const liveResponse = deferred<RailSummary>();
    const load = vi
      .fn(async () => summary({ jobHistory: { status: "complete", revision: 4 } }))
      .mockImplementationOnce(async () => summary({ jobHistory: { status: "complete", revision: 4 } }))
      .mockReturnValueOnce(liveResponse.promise)
      .mockResolvedValueOnce(
        summary({
          jobs: [job({ finishedAt: 9_000, exitCode: 0, status: "finished" })],
          jobHistory: { status: "complete", revision: 5 },
        }),
      );
    const cache = new RailSummaryCache();
    const render = renderSummary(load, cache);
    const view = renderHook(render, { initialProps: { ...settled, terminal: true } });
    await waitFor(() => expect(view.result.current.summary).not.toBeNull());

    view.rerender({ ...settled, terminal: false });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    view.rerender({ ...settled, terminal: true });
    view.rerender({ ...settled, jobsUpdatedAt: 2, terminal: true });
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () =>
      liveResponse.resolve(
        summary({ jobs: [job({ jobId: "job_live" })], jobHistory: { status: "complete", revision: 4 } }),
      ),
    );
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(view.result.current.summary?.jobs[0]?.finishedAt).toBe(9_000));

    view.rerender({ ...settled, jobsUpdatedAt: 3, terminal: true });
    expect(load).toHaveBeenCalledTimes(3);
  });
});
