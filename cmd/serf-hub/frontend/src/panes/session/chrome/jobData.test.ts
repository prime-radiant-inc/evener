import { describe, expect, it } from "vitest";
import { parseJobListData, parseJobOutputData } from "./jobData";

describe("parseJobListData", () => {
  it("parses a full shell row", () => {
    const rows = parseJobListData([
      {
        jobId: "job_1",
        type: "shell",
        status: "completed",
        reason: "",
        description: "run tests",
        command: "go test ./...",
        background: true,
        startedAt: "2026-07-31T12:00:00Z",
        endedAt: "2026-07-31T12:01:00Z",
        exitCode: 0,
        outputBytes: 123,
        hasOutput: true,
      },
    ]);
    expect(rows).toHaveLength(1);
    expect(rows![0]).toMatchObject({ jobId: "job_1", status: "completed", exitCode: 0, hasOutput: true });
  });

  it("omits optional fields when absent", () => {
    const rows = parseJobListData([
      {
        jobId: "job_2",
        type: "delegate",
        status: "running",
        description: "scout",
        background: true,
        startedAt: "2026-07-31T12:00:00Z",
        outputBytes: 0,
        hasOutput: false,
      },
    ]);
    expect(rows![0]?.endedAt).toBeUndefined();
    expect(rows![0]?.exitCode).toBeUndefined();
  });

  it("returns null for null data (old daemon capability gap)", () => {
    expect(parseJobListData(null)).toBeNull();
    expect(parseJobListData(undefined)).toBeNull();
    expect(parseJobListData({})).toBeNull();
  });

  it("returns an empty list for a real empty list", () => {
    expect(parseJobListData([])).toEqual([]);
  });

  // agent/jobs_panel.go's JobSummary.Status is a plain Go string, so a
  // daemon newer than this bundle can send a status JobStatus has never
  // heard of. Dropping the row would hide a job that really ran, and
  // rewriting the status to a known one would misreport it: the row is kept
  // and the wire's own word for it is carried through untouched.
  it("keeps a row whose status is outside the known set, verbatim", () => {
    const rows = parseJobListData([
      {
        jobId: "job_4",
        type: "shell",
        status: "quarantined",
        description: "unknown state",
        background: false,
        startedAt: "2026-07-31T12:00:00Z",
        outputBytes: 0,
        hasOutput: false,
      },
    ]);
    expect(rows).toHaveLength(1);
    expect(rows![0]?.status).toBe("quarantined");
  });

  // Every optional field on the wire is `omitempty`, so an empty string is
  // the same absence a missing key is - never a value worth rendering.
  it("drops empty-string optional fields to undefined, matching omitempty", () => {
    const rows = parseJobListData([
      {
        jobId: "job_5",
        type: "shell",
        status: "completed",
        reason: "",
        description: "run tests",
        command: "",
        task: "",
        background: false,
        startedAt: "2026-07-31T12:00:00Z",
        endedAt: "",
        outputBytes: 0,
        hasOutput: false,
      },
    ]);
    expect(rows![0]?.reason).toBeUndefined();
    expect(rows![0]?.command).toBeUndefined();
    expect(rows![0]?.task).toBeUndefined();
    expect(rows![0]?.endedAt).toBeUndefined();
  });

  // A known limit, pinned rather than fixed: dropping unusable rows one at a
  // time means an array of nothing but garbage is indistinguishable from a
  // session that genuinely ran no jobs, and the panel says "No jobs yet" for
  // both. Only a non-array (`null` from an old daemon) is reported as a
  // capability gap.
  it("parses an all-malformed array to the same [] a real empty list gives", () => {
    expect(parseJobListData(["garbage", 5, null, {}])).toEqual([]);
    expect(parseJobListData([])).toEqual([]);
  });

  it("drops malformed entries but keeps parseable ones", () => {
    const rows = parseJobListData([
      "garbage",
      {
        jobId: "job_3",
        type: "shell",
        status: "running",
        description: "ok",
        background: false,
        startedAt: "2026-07-31T12:00:00Z",
        outputBytes: 0,
        hasOutput: false,
      },
    ]);
    expect(rows).toHaveLength(1);
    expect(rows![0]?.jobId).toBe("job_3");
  });
});

describe("parseJobOutputData", () => {
  it("parses a tail payload", () => {
    const out = parseJobOutputData({ tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true });
    expect(out).toEqual({ tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true });
  });

  it("returns null for uninterpretable data", () => {
    expect(parseJobOutputData(null)).toBeNull();
    expect(parseJobOutputData({ tail: 5 })).toBeNull();
  });
});
