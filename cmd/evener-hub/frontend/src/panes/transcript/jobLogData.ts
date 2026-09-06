export interface JobLogTail {
  tail: string;
  totalBytes: number;
  retainedStart: number;
  truncated: boolean;
  // Absent on daemons that predate paging: treated as false, which keeps the
  // old truncation-note-only behavior instead of offering pages that would
  // come back as duplicates of the tail.
  hasEarlier: boolean;
}

// evener/jobs/output's data field crosses the wire untyped (unknown on the
// generated JobsOutputResponse); validate the appwire.JobOutputTail shape
// before trusting it rather than casting.
export function parseJobLogTail(data: unknown): JobLogTail | null {
  if (typeof data !== "object" || data === null) return null;
  const raw = data as Record<string, unknown>;
  if (typeof raw.tail !== "string") return null;
  if (typeof raw.totalBytes !== "number" || typeof raw.retainedStart !== "number") return null;
  return {
    tail: raw.tail,
    totalBytes: raw.totalBytes,
    retainedStart: raw.retainedStart,
    truncated: raw.truncated === true,
    hasEarlier: raw.hasEarlier === true,
  };
}
