// The pulse meter's data (spec 16.4), the Quiet and May be stuck labels
// (spec 13.1) and the working row's latest tool intent, read from
// evener/activity/read (server addition S5). Both apps decode the read here
// and ask quietState which label a working session shows.
import { isPlainObject } from "./plainObject";
import type { SessionActivity } from "./types.gen";

/** A working session reads Quiet after three minutes without transcript motion. */
export const QUIET_AFTER_MS = 3 * 60_000;
/** A working session reads May be stuck after ten. */
export const STUCK_AFTER_MS = 10 * 60_000;
/** The wire's bound on a session's latest tool intent, in Unicode code points
 * (appwire.MaxIntentRunes): a hub never sends a longer one, so this is the
 * whole of what a row may carry rather than a runaway backstop. Counting code
 * points rather than UTF-16 units is what stops an astral character from
 * measuring as two and smuggling a longer line past the bound. */
export const MAX_INTENT_CODE_POINTS = 200;

/** Whether value is an intent the wire's bound allows: at most
 * MAX_INTENT_CODE_POINTS code points, astral characters counted once. The walk
 * stops at the bound, so a runaway costs no more than the bound plus one step. */
function intentWithinBound(value: string): boolean {
  let points = 0;
  for (let index = 0; index < value.length; ) {
    const code = value.codePointAt(index) ?? 0;
    index += code > 0xffff ? 2 : 1;
    if (++points > MAX_INTENT_CODE_POINTS) return false;
  }
  return true;
}

const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;

// One session entry, key by key as the navigation codec treats a value record:
// the keys this client knows are validated and kept, and any other key is
// dropped. A malformed entry is dropped alone, so one bad row never blanks
// every meter.
function sessionActivity(value: unknown): SessionActivity | null {
  if (!isPlainObject(value)) return null;
  const { ref, minutes, runningSubagents, quietForMs, latestIntent } = value;
  if (typeof ref !== "string" || ref === "" || ref.length > 1024) return null;
  if (!Array.isArray(minutes) || minutes.length === 0 || minutes.length > 60 || !minutes.every(count)) return null;
  if (!count(runningSubagents)) return null;
  if (quietForMs !== undefined && !count(quietForMs)) return null;
  if (latestIntent !== undefined && (typeof latestIntent !== "string" || !intentWithinBound(latestIntent))) return null;
  return {
    ref,
    minutes: [...minutes],
    runningSubagents,
    ...(quietForMs === undefined ? {} : { quietForMs }),
    ...(latestIntent === undefined ? {} : { latestIntent }),
  };
}

/** Decodes an evener/activity/read result. A result that is not a session
 * list throws, so a caller keeps the meters it has rather than drawing them
 * flat. */
export function decodeActivityRead(value: unknown): SessionActivity[] {
  if (!isPlainObject(value) || !Array.isArray(value.sessions)) throw new Error("activity read: invalid response");
  const sessions: SessionActivity[] = [];
  for (const entry of value.sessions) {
    const session = sessionActivity(entry);
    if (session) sessions.push(session);
  }
  return sessions;
}

export type QuietState = "quiet" | "stuck";

/** Whether a working session reads Quiet or May be stuck `sinceReadMs` after
 * the read that returned `activity`, and how long it has been quiet; null when
 * it reads neither. The hub withholds the quiet time while the session is not
 * working or any of its subagents runs, and so does this: an agent waiting on
 * subagents is never quiet or stuck (Jesse's ruling for S5). */
export function quietState(
  activity: SessionActivity,
  sinceReadMs: number,
): { state: QuietState; forMs: number } | null {
  if (activity.quietForMs === undefined || activity.runningSubagents > 0) return null;
  const forMs = activity.quietForMs + Math.max(0, sinceReadMs);
  if (forMs >= STUCK_AFTER_MS) return { state: "stuck", forMs };
  if (forMs >= QUIET_AFTER_MS) return { state: "quiet", forMs };
  return null;
}
