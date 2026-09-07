import { describe, expect, it } from "vitest";
import { parseJobLogTail } from "./jobOutput";

const page = { tail: "日本語", totalBytes: 100, retainedStart: 91, truncated: true, hasEarlier: true };

describe("job output byte windows", () => {
  it("preserves server offsets and accepts the omitted false paging flag", () => {
    expect(parseJobLogTail(page)).toEqual(page);
    const { hasEarlier: _, ...withoutEarlier } = page;
    expect(parseJobLogTail(withoutEarlier)).toEqual({ ...page, hasEarlier: false });
    expect(parseJobLogTail({ tail: "", totalBytes: 0, retainedStart: 0, truncated: false })).toEqual({
      tail: "",
      totalBytes: 0,
      retainedStart: 0,
      truncated: false,
      hasEarlier: false,
    });
    expect(parseJobLogTail({ ...page, totalBytes: Number.MAX_SAFE_INTEGER })).not.toBeNull();
  });
  it.each([
    { totalBytes: -1 },
    { totalBytes: 1.5 },
    { totalBytes: Number.NaN },
    { totalBytes: Number.POSITIVE_INFINITY },
    { totalBytes: Number.MAX_SAFE_INTEGER + 1 },
    { retainedStart: -1 },
    { retainedStart: 1.5 },
    { retainedStart: Number.NaN },
    { retainedStart: Number.POSITIVE_INFINITY },
    { retainedStart: Number.MAX_SAFE_INTEGER + 1 },
    { retainedStart: 101 },
    { truncated: undefined },
    { truncated: 0 },
    { truncated: "true" },
    { hasEarlier: null },
    { hasEarlier: 1 },
    { hasEarlier: "false" },
  ])("rejects malformed window bookkeeping %j", (change) => {
    expect(parseJobLogTail({ ...page, ...change })).toBeNull();
  });
  it("rejects arrays even if they carry plausible fields", () => {
    expect(parseJobLogTail(Object.assign([], page))).toBeNull();
  });
});
