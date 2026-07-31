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
    expect(rows![0].endedAt).toBeUndefined();
    expect(rows![0].exitCode).toBeUndefined();
  });

  it("returns null for null data (old daemon capability gap)", () => {
    expect(parseJobListData(null)).toBeNull();
    expect(parseJobListData(undefined)).toBeNull();
    expect(parseJobListData({})).toBeNull();
  });

  it("returns an empty list for a real empty list", () => {
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
    expect(rows![0].jobId).toBe("job_3");
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
