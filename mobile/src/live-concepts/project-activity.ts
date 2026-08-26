// projectLiveActivity — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input → same output.
//
// Maps task groups and nested work (delegates, jobs, watches) without raw IDs,
// commands, paths, prompts, profile IDs, or refs. The live view carries only
// display-safe metadata: tone, title, detail summary — never raw identifiers
// or operational payloads from the RedactedDiagnostic.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// Map an ActivityView work tone to a live display tone.
function mapTone(tone: WorkTone): DisplayTone {
  switch (tone) {
    case "running":
      return "running";
    case "failed":
      return "failed";
    case "terminal":
      return "success";
    case "idle":
      return "idle";
    default:
      return "unknown";
  }
}

// Project a WorkEntry (with possible RedactedDiagnostic) into a LiveWorkItem
// that carries NO raw identifiers, commands, paths, profile IDs, or refs.
function projectWorkEntry(entry: WorkEntry): LiveWorkItem {
  const children = (entry.children ?? []).map(projectWorkEntry);
  return {
    key: entry.label, // Use label as the display key, not the raw ID
    kind: entry.kind,
    title: entry.label,
    detail: entry.outputSummary ?? "",
    tone: mapTone(entry.tone),
    children,
  };
}

export function projectLiveActivity(view: ActivityView): LiveActivityView {
  return {
    tasks: view.tasks.map((t) => ({ status: t.status, count: t.count })),
    work: view.work.map(projectWorkEntry),
    usage: {
      totalTokens: view.usage.totalTokens,
      cost: view.usage.cost,
      contextPressure: view.usage.contextPressure,
      durationMs: view.usage.durationMs,
    },
  };
}
