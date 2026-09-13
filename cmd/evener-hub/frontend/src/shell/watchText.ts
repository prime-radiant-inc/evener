// The watch vocabulary the rail and the session panel both speak: cadence, the
// armed wording, the row title, and the gloss that joins them.
//
// Deliberately free of React and of CSS imports. The session panel's row model
// (panes/session/chrome/activityRows.ts) is a pure module that mobile-native
// typechecks as part of its own build, so importing these from the rail's React
// component file dragged every widget stylesheet into that graph - a dependency
// the mobile check rejects, correctly. Keep this file importable from anywhere.
import type { NavigationWatchCadence, NavigationWatchSummary } from "../protocol/types.gen";

// A watch's cadence in the rail's compact shorthand: seconds in, "10m" out.
// Deliberately coarse (no "in 4m", no countdown): the runtime keeps a ticker,
// not a next-fire instant, so there is no honest instant to show - but the
// PERIOD is real, and that is what these labels carry.
export function watchDurationLabel(seconds: number | undefined): string {
  if (seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) return "";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60);
    const rest = Math.round(seconds % 60);
    return rest > 0 ? `${minutes}m${rest}s` : `${minutes}m`;
  }
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.round((seconds % 3600) / 60);
    return minutes > 0 ? `${hours}h${minutes}m` : `${hours}h`;
  }
  const days = Math.floor(seconds / 86400);
  const hours = Math.round((seconds % 86400) / 3600);
  return hours > 0 ? `${days}d${hours}h` : `${days}d`;
}

// One wire cadence row as a phrase a person reads. "after"/"every"/"progress"
// all carry a period; "output"/"events" are conditions with no period at all,
// which is exactly why they read "on ..." instead of "every ...".
export function watchCadenceLabel(cadence: NavigationWatchCadence): string {
  switch (cadence.kind) {
    case "after":
      return `after ${watchDurationLabel(cadence.seconds)}`.trim();
    case "every":
    case "progress":
      return `every ${watchDurationLabel(cadence.seconds)}`.trim();
    case "output":
      return "on output";
    case "events":
      return "on events";
    default:
      // An unrecognized future cadence kind still says SOMETHING honest rather
      // than rendering an empty second line.
      return cadence.kind;
  }
}

// The armed wording, in one place so the rail's gloss and the session panel's
// own watch rows can never disagree about it.
export function watchArmedLabel(active: boolean): string {
  return active ? "armed" : "not armed";
}

// A watch row's second line: what it is waiting on, and whether it is still
// armed. One line per watch, never a countdown - see watchDurationLabel.
export function watchGloss(watch: NavigationWatchSummary): string {
  const parts = (watch.cadence ?? []).map(watchCadenceLabel).filter((label) => label !== "");
  parts.push(watchArmedLabel(watch.active));
  return parts.join(" · ");
}

// The title a watch row shows: the note the watch was armed with - the reason
// a person wrote down. The id is the fallback for a note the wire omitted or
// truncated to nothing, so a row is never blank.
export function watchTitle(watch: NavigationWatchSummary): string {
  return watch.note?.trim() || watch.id;
}
