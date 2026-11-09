import { durationParts } from "./displayFormat";

// Timestamp formatting for task rows (spec: docs/superpowers/specs/
// 2026-08-09-task-list-ui-design.md). The wire's ISO strings stay strings in
// TaskRow; these two functions are the only place they become display text.
// Ages floor to the largest whole unit, like the rail and the Board.
// Both tolerate invalid input by returning the raw string - a malformed
// timestamp is a display detail, never a reason to blank a task row. Shared
// by the web tasks panel and native's tasks sheet and delegate details.

export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  const { count, unit } = durationParts(now.getTime() - t.getTime());
  if (unit === "second") return "now"; // covers the future-skew case too
  return `${count}${unit[0]} ago`;
}

// Built once: a formatter with an options object is not served from the
// engine's cache, and absoluteTime runs once or more per visible task row.
const ABSOLUTE_FORMAT = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

export function absoluteTime(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  return ABSOLUTE_FORMAT.format(t);
}
