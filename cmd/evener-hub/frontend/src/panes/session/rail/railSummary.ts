// The rail summary contract: one bounded projection of a thread's job lane,
// plus the by-ref cache the rail keeps across mounts.
//
// A summary is never presumed complete. `jobHistory` carries the projection's
// own completeness and revision so the rail can show an incomplete lane
// instead of rendering it as full history.

export type RailJobHistoryStatus = "complete" | "incomplete" | "degraded" | "fatal";

export interface RailJobHistory {
  status: RailJobHistoryStatus;
  /** The projection revision these jobs were read at, or null when the
   * source cannot supply one. A lower revision never replaces a higher one. */
  revision: number | null;
}

export interface RailJobInterval {
  jobId: string;
  startedAt: number;
  /** null while the job is still running. */
  finishedAt: number | null;
  exitCode?: number;
  status: string;
}

/** The cheap per-turn tuple: no turn text, just what the rail draws. */
export interface RailSummaryTurn {
  /** The wire turn id, when the source carries one. The model merge dedupes a
   * live turn against this id; a summary without ids falls back to the start
   * timestamp. */
  turnId?: string;
  startedAt: number;
  userInput: boolean;
  steering: boolean;
  error: boolean;
  in: number;
  out: number;
  resultBytes: number;
}

export interface RailSummary {
  turns: readonly RailSummaryTurn[];
  jobs: readonly RailJobInterval[];
  jobHistory: RailJobHistory;
}

export interface RailSummaryLoadContext {
  signal: AbortSignal;
}

export type RailSummaryLoader = (ref: string, context: RailSummaryLoadContext) => Promise<RailSummary>;

/** What a cached summary was loaded under, so a remount can tell whether the
 * live job signals moved while it was away. */
export interface RailSummaryCacheEntry {
  summary: RailSummary;
  /** The thread's job signal key at load time (jobsUpdatedAt + jobsTreeRevision). */
  signal: string;
  /** The thread store's hydration count at load time (reconnect and resync). */
  hydration: number;
  /** Whether the ref was an ended session when this entry was loaded. A
   * terminal mount must not freeze an entry captured while the session was
   * still live. */
  terminal: boolean;
  /** A resolved status that outlived the summary's own, such as a job-only
   * refresh failure. Absent when the summary's status is still the truth. */
  jobHistory?: RailJobHistory;
}

/** One entry per thread ref, shared by every rail mount. */
export class RailSummaryCache {
  readonly #entries = new Map<string, RailSummaryCacheEntry>();

  get(ref: string): RailSummaryCacheEntry | undefined {
    return this.#entries.get(ref);
  }

  set(ref: string, entry: RailSummaryCacheEntry): void {
    this.#entries.set(ref, entry);
  }

  clear(): void {
    this.#entries.clear();
  }
}

export const railSummaryCache = new RailSummaryCache();
