// JobLog is the read-only "job:<id>" transcript pane: the job's full command
// above the job's output tail. The command comes from its own read
// (evener/jobs/get); the output from evener/jobs/output. Both are stubbed at
// the store method here - the pane's own effect wiring is what these tests
// pin.

import type { ActivityJob } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
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
  return vi.spyOn(threadsStore.getState(), "jobGet").mockResolvedValue(null);
}

function setupJobOutput() {
  return vi
    .spyOn(threadsStore.getState(), "jobOutput")
    .mockResolvedValue({ tail: "", totalBytes: 0, retainedStart: 0 });
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
  connectionStore.setState({ state: "idle", client: null });
  vi.restoreAllMocks();
});

describe("JobLog", () => {
  test("shows the job's full command above its output", async () => {
    jobGet.mockResolvedValue(job({ command: "go test ./... -run Foo -count=1" }));
    jobOutput.mockResolvedValue({ tail: "ok\n", totalBytes: 3, retainedStart: 0 });

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
    jobOutput.mockResolvedValue({ tail: "hello\n", totalBytes: 6, retainedStart: 0 });

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect((await screen.findByTestId("joblog-content")).textContent).toContain("hello");
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });

  test("shows no command line when the job payload is malformed", async () => {
    jobGet.mockResolvedValue({ jobId: "job_x" });
    jobOutput.mockResolvedValue({ tail: "hello\n", totalBytes: 6, retainedStart: 0 });

    render(<JobLog jobRef="job:job_x" parentRef="ref_root" />);

    expect(await screen.findByTestId("joblog-content")).toBeTruthy();
    expect(screen.queryByTestId("joblog-command")).toBeNull();
  });
});
