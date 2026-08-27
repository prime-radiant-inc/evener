// projectLiveActivity — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input → same output.
//
// Maps task groups and nested work (delegates, jobs, watches) without raw IDs,
// commands, paths, prompts, profile IDs, or refs. The live view carries only
// display-safe metadata: tone, title, detail summary — never raw identifiers
// or operational payloads from the RedactedDiagnostic.
//
// Work entry keys are opaque stable private keys derived from a counter, not
// raw labels or IDs. This ensures collision-safe keys and prevents
// operational identifiers from leaking into the DOM.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// Private key generator: produces opaque stable keys that do not expose raw
// labels or IDs. Collision-safe via a monotonically increasing counter.
let keyCounter = 0;
function nextKey(kind: string): string {
  keyCounter += 1;
  return `wk${keyCounter}:${kind}`;
}

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
// Children are preserved recursively.
function projectWorkEntry(entry: WorkEntry): LiveWorkItem {
  const children = (entry.children ?? []).map(projectWorkEntry);
  return {
    key: nextKey(entry.kind),
    kind: entry.kind,
    title: entry.label,
    detail: entry.outputSummary ?? "",
    tone: mapTone(entry.tone),
    children,
  };
}

export function projectLiveActivity(view: ActivityView): LiveActivityView {
  // Reset the key counter at the start of each projection pass to ensure
  // deterministic keys within a single call.
  keyCounter = 0;
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
