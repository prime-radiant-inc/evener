// JobLog is the read-only "job:<id>" transcript pane: the job's full command
// above the job's output tail. The command comes from its own read
// (evener/jobs/get); the output from evener/jobs/output. Both are stubbed at
// the store method here - the pane's own effect wiring is what these tests
// pin.

import type { ActivityJob, JobActivityJob } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { chromeStore, resetChromeStoreForTests } from "../../shell/chromeStore";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { JobLog } from "./JobLog";

function job(overrides: Record<string, unknown> = {}): ActivityJob {
  return {
    jobId: "job_x",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: false,
    hasOutput: true,
    description: "shell job",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 0,
    ...overrides,
  } as ActivityJob;
}

// setupJobGet/setupJobOutput spy the store methods (not the module) so the
// pane's fetches stay in-process; each test overrides with mockResolvedValue/
// mockRejectedValue on the returned spy.
function setupJobGet() {
  return vi.spyOn(threadsStore.getState(), "jobGet").mockResolvedValue(job());
}

function setupJobOutput() {
  return vi
    .spyOn(threadsStore.getState(), "jobOutput")
    .mockResolvedValue({ tail: "", totalBytes: 0, retainedStart: 0, truncated: false });
}

let jobGet: ReturnType<typeof setupJobGet>;
let jobOutput: ReturnType<typeof setupJobOutput>;

beforeEach(() => {
  // Both reads only fire once the connection store reports ready; the stubs
  // keep the wire out of the unit test entirely.
  connectionStore.setState({ state: "ready" });
  jobGet = setupJobGet();
  jobOutput = setupJobOutput();
});

afterEach(() => {
  cleanup();
  resetChromeStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  vi.restoreAllMocks();
});

describe("JobLog", () => {
  test("shows the job's full command above its output", async () => {
    jobGet.mockResolvedValue(job({ command: "go test ./... -run Foo -count=1" }));
    jobOutput.mockResolvedValue({ tail: "ok\n", totalBytes: 3, retainedStart: 0, truncated: false });

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect((await screen.findByTestId("joblog-command")).textContent).toBe("go test ./... -run Foo -count=1");
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("ok");
  });

  test("shows the command even when the job has written no output yet", async () => {
    jobGet.mockResolvedValue(job({ command: "make lint", hasOutput: false }));

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect((await screen.findByTestId("joblog-command")).textContent).toBe("make lint");
    expect(await screen.findByText("No output yet")).toBeTruthy();
  });

  test("renders the log with no command line when the job read is unavailable", async () => {
    jobGet.mockRejectedValue(new Error("job not available"));
    jobOutput.mockResolvedValue({ tail: "hello\n", totalBytes: 6, retainedStart: 0, truncated: false });

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect((await screen.findByTestId("joblog-content")).textContent).toContain("hello");
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("shows no command line when the job payload is malformed", async () => {
    // Deliberately violate the declared wire type to exercise runtime validation.
    jobGet.mockResolvedValue({ jobId: "job_x" } as JobActivityJob);
    jobOutput.mockResolvedValue({ tail: "hello\n", totalBytes: 6, retainedStart: 0, truncated: false });

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect(await screen.findByTestId("joblog-content")).toBeTruthy();
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("never shows another job's command while the new job's metadata is in flight", async () => {
    jobGet.mockResolvedValueOnce(job({ jobId: "job_x", command: "first command" }));
    jobOutput.mockResolvedValue({ tail: "one\n", totalBytes: 4, retainedStart: 0, truncated: false });
    const { rerender } = render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("first command");

    // The second job's metadata read stays in flight, the window in which the
    // previous job's command could still be on screen.
    let resolveSecond: (value: JobActivityJob) => void = () => {};
    jobGet.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSecond = resolve;
        }),
    );
    await act(async () => {
      rerender(<JobLog jobRef="job:job_y" parentRef="ref_root" />);
    });
    expect(screen.queryByTestId("joblog-command")).toBeNull();

    await act(async () => {
      resolveSecond(job({ jobId: "job_y", command: "second command" }));
    });
    expect((await screen.findByTestId("joblog-command")).textContent).toBe("second command");
  });
});

test.each(["unavailable", "unnamed"])(
  "%s metadata preserves job identity while real output reads succeed",
  async (kind) => {
    vi.restoreAllMocks();
    const client = new FakeClient();
    client.on("evener/jobs/get", () => {
      if (kind === "unavailable") throw new Error("metadata unavailable");
      return { data: job({ description: "", command: "" }) };
    });
    client.on("evener/jobs/output", () => ({
      data: { tail: "IDENTIFIABLE_OUTPUT", totalBytes: 19, retainedStart: 0, truncated: false },
    }));
    connectionStore.setState({ state: "ready", client });
    render(<JobLog jobRef="job:job_x" parentRef="ref_root" paneId="output-pane" />);
    expect((await screen.findByTestId("joblog-content")).textContent).toContain("IDENTIFIABLE_OUTPUT");
    expect(screen.getByRole("heading", { name: "job_x" })).toBeTruthy();
    expect(chromeStore.getState().paneTitles.get("output-pane")).toBe("job_x");
    expect(client.calls.filter((call) => call.method === "evener/jobs/get").map((call) => call.params)).toEqual([
      { ref: "ref_root", jobId: "job_x" },
    ]);
  },
);
