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
/** The longest string this client keeps for a session's latest tool intent:
 * twice the 400 UTF-16 units the wire's bound (appwire.MaxIntentRunes, 200
 * runes) can ever cost, so it holds a runaway without ever dropping an intent
 * a hub may legitimately send. */
export const MAX_INTENT_LENGTH = 800;

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
  if (latestIntent !== undefined && (typeof latestIntent !== "string" || latestIntent.length > MAX_INTENT_LENGTH))
    return null;
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
