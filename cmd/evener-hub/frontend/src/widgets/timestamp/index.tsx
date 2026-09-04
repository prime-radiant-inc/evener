// A relative-time label whose absolute instant is one hover away.
//
// Built on the platform's own Intl formatters (RelativeTimeFormat for the
// visible relative text, DateTimeFormat for the absolute hover) — no third-
// party date dependency, matching the project's "prefer standard defaults"
// stance. The locale is pinned to "en" so output is deterministic across
// environments (CI and dev alike); the existing transcript formatters
// (messages/format.ts) are likewise en-style by construction.
//
// Pure render, no internal clock — mirrors Cadence and Loader: the caller
// owns "now" (the session's shared 3s tick via useSessionNow, or a fixed
// value in tests), so a component with no timer cannot fake or drift liveness
// between renders.
import { requireClass } from "../internal/requireClass";
import styles from "./timestamp.module.css";

export interface TimestampProps {
  /** Epoch-ms instant the event occurred. */
  value: number;
  /** Epoch-ms "current" instant the relative time is measured against.
   * Prop-driven, never Date.now() internally — same pure-render rule as
   * Cadence (src/widgets/cadence/index.tsx) and Loader. */
  now: number;
}

const CLASS = {
  time: requireClass(styles.time, "timestamp.module.css", "time"),
};

// One formatter per shape, module-singleton — Intl construction is not cheap,
// and the relative formatter is called on every render of every visible
// timestamp. Pinned locale keeps the snapshot deterministic.
const RELATIVE = new Intl.RelativeTimeFormat("en", { style: "narrow", numeric: "always" });

const ABS_SAME_DAY = new Intl.DateTimeFormat("en", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const ABS_OTHER_DAY = new Intl.DateTimeFormat("en", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

// Pick the largest unit whose magnitude is >= 1, rounding to that unit so
// "90s" reads "2m ago" rather than "90s ago" (a 90-second-old step is a minute
// and a half, not ninety seconds — the same rounding every "time ago" UI does).
// Below 10s the step is effectively current, so it collapses to "now" rather
// than "0s ago"/"9s ago" noise. Future (clock-skew) values also collapse to
// "now": a timestamp slightly ahead of the clock is not "in 4 seconds", it is
// just now.
function relativeLabel(diffMs: number): string {
  if (diffMs < 10_000) return "now";
  const sec = Math.round(diffMs / 1000);
  if (sec < 60) return RELATIVE.format(-sec, "second");
  const min = Math.round(sec / 60);
  if (min < 60) return RELATIVE.format(-min, "minute");
  const hr = Math.round(min / 60);
  if (hr < 24) return RELATIVE.format(-hr, "hour");
  const day = Math.round(hr / 24);
  if (day < 7) return RELATIVE.format(-day, "day");
  const wk = Math.round(day / 7);
  return RELATIVE.format(-wk, "week");
}

// Adaptive absolute: time-only when the event shares the reference day,
// date+time once it crosses a calendar boundary. `toDateString` is the
// cheapest same-calendar-day test and is timezone-correct for both operands.
function absoluteLabel(value: number, now: number): string {
  const sameDay = new Date(value).toDateString() === new Date(now).toDateString();
  return (sameDay ? ABS_SAME_DAY : ABS_OTHER_DAY).format(value);
}

/** A relative time (e.g. "5m ago") whose absolute instant ("13:53:45", or
 * "Aug 20, 09:41:02" once it crosses a day boundary) is the native
 * `<time title>` hover tooltip. The `dateTime` attribute carries the ISO
 * instant for assistive tech and copy/paste. Pure render of `value`/`now` —
 * no timers, no Date.now(). */
export function Timestamp({ value, now }: TimestampProps) {
  if (Number.isNaN(value) || Number.isNaN(now)) return null;
  const diff = now - value;
  return (
    <time className={CLASS.time} dateTime={new Date(value).toISOString()} title={absoluteLabel(value, now)}>
      {relativeLabel(diff)}
    </time>
  );
}
