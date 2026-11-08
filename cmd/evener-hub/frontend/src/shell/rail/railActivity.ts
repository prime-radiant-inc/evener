// The rail's read of the hub's activity pulse (evener/activity/read), and the
// one place a working row's status words are decided. The native Board settles
// a working row through the same data in the same order
// (mobile-native/src/board/attention.ts): running subagents first, then quiet
// or stuck, then the session's latest tool intent, then the job it is running,
// then the bare state word (#3637).
//
// The card this feeds mounts only while it is open (FloatingBubblePortal
// renders nothing when hidden), so the read runs only while someone is
// looking: once on open, then every RAIL_ACTIVITY_POLL_MS. A read older than
// two intervals is treated as no read at all - the Board's staleness rule - so
// a hub that has stopped answering cannot make a row read quiet or stuck from
// the age of its last answer alone. A method-not-found answer means the hub
// predates the read; polling stops for good and the card keeps the row's own
// facts. The same method-not-found handling and staleness arithmetic live in
// mobile-native's ActivityPoll, kept separate because this read is one
// session's and lives exactly as long as one card.
import {
  compactDuration,
  decodeActivityRead,
  isMethodNotFound,
  quietState,
  type SessionActivity,
} from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { connectionStore } from "../../stores/connection";
import { STATUS_LABEL } from "../../widgets";
import type { RailSession } from "./railNodes";

export const RAIL_ACTIVITY_POLL_MS = 10_000;
// Two intervals, not one: a single missed tick gets room to self-heal before
// the row falls back.
export const RAIL_ACTIVITY_STALE_AFTER_MS = 2 * RAIL_ACTIVITY_POLL_MS;

export interface RailActivityRead {
  activity: SessionActivity;
  msSinceRead: number;
}

/** Whether a read taken msSinceRead milliseconds ago is still trustworthy. */
export function isFreshActivityRead(msSinceRead: number): boolean {
  return msSinceRead < RAIL_ACTIVITY_STALE_AFTER_MS;
}

/** One session's latest pulse while its card is open, or null while the read
 * is absent, stale, or unsupported by this hub. */
export function useSessionActivityRead(ref: string, active: boolean): RailActivityRead | null {
  const [read, setRead] = useState<{ activity: SessionActivity; at: number } | null>(null);

  useEffect(() => {
    if (!active) return undefined;
    let requestId = 0;
    let pending = false;
    let unsupported = false;
    let timer: ReturnType<typeof setInterval> | undefined;

    const stopTimer = () => {
      if (timer === undefined) return;
      clearInterval(timer);
      timer = undefined;
    };

    const poll = () => {
      // One read in flight per card: a hub slower than the interval must not
      // accumulate requests whose answers are all discarded.
      if (unsupported || pending || document.visibilityState !== "visible") return;
      const { client, state } = connectionStore.getState();
      if (state !== "ready" || !client) return;
      const id = ++requestId;
      pending = true;
      client
        .request("evener/activity/read", { refs: [ref] })
        .then((response) => {
          if (id !== requestId) return;
          pending = false;
          let sessions: SessionActivity[];
          try {
            sessions = decodeActivityRead(response);
          } catch {
            return; // an unreadable response keeps the previous read rather than blanking it
          }
          const activity = sessions.find((session) => session.ref === ref);
          if (!activity) {
            setRead(null); // the hub withheld this entry: the row's own fallback applies
            return;
          }
          setRead({ activity, at: Date.now() });
        })
        .catch((error: unknown) => {
          if (id !== requestId) return;
          pending = false;
          if (!isMethodNotFound(error)) return;
          unsupported = true;
          stopTimer();
        });
    };

    const start = () => {
      if (timer !== undefined || unsupported) return;
      poll();
      timer = setInterval(poll, RAIL_ACTIVITY_POLL_MS);
    };

    const onVisibilityChange = () => {
      // No timer runs in a hidden tab; showing the tab reads at once.
      if (document.visibilityState === "visible") start();
      else stopTimer();
    };

    start();
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      requestId++; // retire any answer in flight
      stopTimer();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [ref, active]);

  if (!active || read === null || read.activity.ref !== ref) return null;
  const msSinceRead = Math.max(0, Date.now() - read.at);
  if (!isFreshActivityRead(msSinceRead)) return null;
  return { activity: read.activity, msSinceRead };
}

export type RailStatusKind = "not-started" | "failed" | "state" | "intent" | "subagents" | "quiet" | "stuck" | "job";

export interface RailStatus {
  kind: RailStatusKind;
  text: string;
}

function waitingOnSubagents(count: number): string {
  return `Waiting on ${count} subagent${count === 1 ? "" : "s"}`;
}

/** A working session's status words: the Board's precedence, with the row's
 * own facts standing in when the hub predates the activity read. Without a
 * read the card keeps the state word it has always shown (the rail's
 * pre-#3637 behavior) rather than inventing subagent or quiet counts from a
 * tally the row already prints elsewhere; a running job is the one fact the
 * row itself still names. */
export function workingStatus(session: RailSession, read: RailActivityRead | null): RailStatus {
  if (read) {
    const { activity, msSinceRead } = read;
    if (activity.runningSubagents > 0) {
      return { kind: "subagents", text: waitingOnSubagents(activity.runningSubagents) };
    }
    const quiet = quietState(activity, msSinceRead);
    if (quiet?.state === "stuck") {
      return { kind: "stuck", text: `May be stuck · no updates for ${compactDuration(quiet.forMs)}` };
    }
    if (quiet?.state === "quiet") return { kind: "quiet", text: `Quiet ${compactDuration(quiet.forMs)}` };
    if (activity.latestIntent) return { kind: "intent", text: activity.latestIntent };
  }
  if (session.running_job_command) return { kind: "job", text: `Running ${session.running_job_command}` };
  return { kind: "state", text: STATUS_LABEL.working };
}
