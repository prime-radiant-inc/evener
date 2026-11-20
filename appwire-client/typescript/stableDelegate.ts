export interface StableDelegateState {
  status?: string;
  outcome?: string;
  terminal?: boolean;
}

// A stable delegate's status is its reusable resource lifecycle. Only a
// terminal snapshot describes the last run through outcome; a resumed run can
// retain that old outcome while its current lifecycle is running again.
export function stableDelegateDisplayStatus(delegate: StableDelegateState): string | undefined {
  return delegate.terminal ? (delegate.outcome ?? delegate.status) : delegate.status;
}

/** A working row's non-empty stated intent: the line that stands in for the
 * status word. Empty whenever the row is terminal or states nothing. */
export function delegateStatedIntent(delegate: StableDelegateState & { latestIntent?: string }): string {
  return !delegate.terminal && delegate.latestIntent ? delegate.latestIntent : "";
}

/** The two facts a finalization-drain hold reports: the delegate's own running
 * jobs and the descendant subagents still open below it. Both are one hold
 * sample at the wire level (`holdSequence`), and both mean the same thing to a
 * reader -- the run is parked, and this is what it waits on. */
export interface DelegateWaitFields extends StableDelegateState {
  drainHeldByJobs?: boolean;
  runningJobCount?: number;
  drainHeldBySubagents?: boolean;
  runningSubagentCount?: number;
}

/** How a caller wants a drain wait phrased.
 *
 * `omitSubagents` is for a surface that already renders the waiting subagents
 * expanded beneath the row: naming them again would repeat the same fact one
 * line up. It never brings the frozen pre-drain intent back (see
 * delegateWorkText). */
export interface DelegateWaitOptions {
  omitSubagents?: boolean;
}

/** A working row's live drain wait, or "".
 *
 * The hold gates this, not the counts. A delegate that starts a background
 * shell or a child delegate and keeps working has running work and is not
 * waiting on it; only `drainHeldByJobs` / `drainHeldBySubagents` say the
 * finalization drain is holding the run open, which is the state whose intent
 * and activity clock both stop moving. The counts then only phrase the wait. A
 * row carrying neither fact (a retained read, an older daemon) says nothing,
 * which is not the same claim as "known idle". */
export function delegateWaitText(delegate: DelegateWaitFields, options?: DelegateWaitOptions): string {
  if (delegate.terminal) return "";
  const subagents = !options?.omitSubagents && delegate.drainHeldBySubagents ? (delegate.runningSubagentCount ?? 0) : 0;
  const jobs = delegate.drainHeldByJobs ? (delegate.runningJobCount ?? 0) : 0;
  if (subagents <= 0 && jobs <= 0) return "";
  const subagentText = subagents === 1 ? "Waiting on 1 subagent" : `Waiting on ${subagents} subagents`;
  const jobText = jobs === 1 ? "1 running job" : `${jobs} running jobs`;
  if (subagents > 0 && jobs > 0) return `${subagentText}, ${jobText}`;
  if (subagents > 0) return subagentText;
  return `Waiting on ${jobText}`;
}

// A working row's newest tool intent stands in for the status word; every
// other row state keeps the word the surface passed in. Each client owns its
// own status vocabulary, and this one helper owns the substitution rule, so
// the surfaces cannot drift. The intent is display-only: the status still
// drives sorting, aggregation, and accessibility.
export function delegateWorkText(
  delegate: DelegateWaitFields & { latestIntent?: string },
  status: string,
  options?: DelegateWaitOptions,
): string {
  // The wait outranks both the intent and the status word, but only while the
  // drain actually holds the run: a delegate still working keeps its intent
  // and its status word even with running work of its own.
  const wait = delegateWaitText(delegate, options);
  if (wait !== "") return wait;
  // The caller is rendering the waiting subagents itself, so this row says
  // nothing -- but it must not fall back to the intent or the status word
  // either: the delegate is still parked in its drain, and the intent stopped
  // moving when the drain took the run. Repeating it is the freeze this whole
  // hold exists to replace.
  if (
    !delegate.terminal &&
    options?.omitSubagents &&
    delegate.drainHeldBySubagents &&
    (delegate.runningSubagentCount ?? 0) > 0
  ) {
    return "";
  }
  const intent = delegateStatedIntent(delegate);
  return intent !== "" ? intent : status;
}

/** time.RFC3339Nano, the layout internal/appprojector parses activity
 * timestamps with. Date.parse is looser and keeps only milliseconds, so the
 * comparison tests this shape and reads the fraction itself. */
const activityTimestamp = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|([+-])(\d{2}):(\d{2}))$/;
/** The whole seconds since the epoch (offset applied) and the fractional
 * nanoseconds of an RFC3339Nano activity timestamp, or null when the value is
 * not one -- so a value only Date.parse accepts never orders against a real
 * one, and two values that differ below a millisecond order exactly as the
 * projector's time.Time does. */
export function activityInstant(value: string): { seconds: number; nanos: number } | null {
  const match = activityTimestamp.exec(value);
  if (!match) return null;
  const [, year, month, day, hour, minute, second, fraction, , sign, offsetHour, offsetMinute] = match;
  const monthValue = Number(month);
  const dayValue = Number(day);
  if (monthValue < 1 || monthValue > 12 || dayValue < 1 || dayValue > 31) return null;
  // Go's time.RFC3339Nano rejects a leap second ("second out of range"), so
  // accepting one would order a value the projector refuses.
  if (Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) return null;
  if (sign !== undefined && (Number(offsetHour) > 23 || Number(offsetMinute) > 59)) return null;
  const yearValue = Number(year);
  const utc = Date.UTC(yearValue, monthValue - 1, dayValue, Number(hour), Number(minute), Number(second));
  const at = new Date(utc);
  // Date.UTC rolls a calendar-invalid day (31 February) into the next month,
  // which the projector's parser rejects outright.
  if (at.getUTCFullYear() !== yearValue || at.getUTCMonth() !== monthValue - 1 || at.getUTCDate() !== dayValue) {
    return null;
  }
  const offsetSeconds =
    sign === undefined ? 0 : (sign === "-" ? -1 : 1) * (Number(offsetHour) * 3600 + Number(offsetMinute) * 60);
  return { seconds: utc / 1000 - offsetSeconds, nanos: Number((fraction ?? "").padEnd(9, "0").slice(0, 9)) };
}

/** Mirrors internal/appprojector.delegateActivityAfter: a blank candidate never
 * wins, a blank current loses to any non-blank candidate, and otherwise the
 * candidate wins only when both values parse and it is strictly later. */
export function activityAfter(candidate: string | undefined, current: string | undefined): boolean {
  if (!candidate || candidate.trim() === "") return false;
  if (!current || current.trim() === "") return true;
  const candidateAt = activityInstant(candidate);
  const currentAt = activityInstant(current);
  if (!candidateAt || !currentAt) return false;
  if (candidateAt.seconds !== currentAt.seconds) return candidateAt.seconds > currentAt.seconds;
  return candidateAt.nanos > currentAt.nanos;
}
/** The projector's join for one delegate: `incomingWins` picks the snapshot
 * fields, while latestActivityAt is the independent maximum of the two -- a
 * lower revision may advance it, a higher one never moves it backward. The
 * other side's activity is taken only when it is strictly later, exactly as
 * internal/appprojector.mergeAppwireDelegateInfo moves it, so a pushed row and
 * the same row read back agree. */
export function joinDelegateState<T extends { latestActivityAt?: string | undefined }>(
  incoming: T,
  current: T,
  incomingWins: boolean,
): T {
  const winner = incomingWins ? incoming : current;
  const other = incomingWins ? current : incoming;
  const activity = activityAfter(other.latestActivityAt, winner.latestActivityAt)
    ? other.latestActivityAt
    : winner.latestActivityAt;
  return activity === winner.latestActivityAt ? winner : { ...winner, latestActivityAt: activity };
}
/** A missing/NaN revision is the zero value; an unset read row must not beat a
 * real frame, and two unset rows compare equal. */
export function revisionOf(value: number | undefined): number {
  return typeof value === "number" && !Number.isNaN(value) ? value : 0;
}
