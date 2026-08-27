// createLiveActivityProjector — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input → same output.
//
// The projector INSTANCE owns a private registry. Stable keys survive
// hierarchy insertion/reorder/patch. Labels are display-safe inputs only;
// raw diagnostic IDs live only in the private operational map returned
// alongside the view. Duplicate/colliding source IDs are detected and produce
// distinct stable display keys.
//
// Work entry keys are opaque stable private keys, not raw labels or IDs.
// This ensures collision-safe keys and prevents operational identifiers from
// leaking into the DOM.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// --- operational map (private) -----------------------------------------------

export interface ActivityOperationalMap {
  // Opaque work key → raw diagnostic ID (from RedactedDiagnostic.rawId).
  // For entries without diagnostics, the raw label is used as the source
  // identity so duplicate labels still get distinct keys.
  readonly keys: Map<string, string>;
}

// --- projector instance ------------------------------------------------------

export interface LiveActivityProjector {
  project(view: ActivityView): {
    live: LiveActivityView;
    operational: ActivityOperationalMap;
  };
}

// --- opaque key derivation ---------------------------------------------------

function makeOpaqueId(): string {
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// --- tone mapping ------------------------------------------------------------

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

// --- projector factory -------------------------------------------------------

export function createLiveActivityProjector(): LiveActivityProjector {
  // Private registry: maps namespace+sourceId → opaque key.
  // The source identity for a work entry is its diagnostic rawId if present,
  // otherwise the label. This means duplicate labels (without diagnostics)
  // still get distinct keys via the per-occurrence counter suffix.
  const keyRegistry = new Map<string, string>();
  const operationalKeys = new Map<string, string>();

  const salt = makeOpaqueId();
  let keyCounter = 0;

  // Allocate or retrieve a stable opaque key for a source identity.
  function stableKey(namespace: string, sourceId: string): string {
    const regKey = `${namespace}:${sourceId}`;
    let k = keyRegistry.get(regKey);
    if (k === undefined) {
      keyCounter += 1;
      k = `${salt}${keyCounter.toString(36)}`;
      keyRegistry.set(regKey, k);
    }
    return k;
  }

  // Project a WorkEntry into a LiveWorkItem. The source identity for key
  // stability is the diagnostic rawId if available, otherwise the label.
  // Duplicate labels without diagnostics are disambiguated by an occurrence
  // counter passed from the caller.
  function projectWorkEntry(
    entry: WorkEntry,
    occurrenceMap: Map<string, number>,
  ): LiveWorkItem {
    // Determine source identity: prefer diagnostic rawId, fall back to label.
    const rawId = entry.diagnostics?.rawId;
    let sourceId: string;
    let operationalId: string;
    if (rawId !== undefined && rawId !== "") {
      sourceId = rawId;
      operationalId = rawId;
    } else {
      // For entries without diagnostics, use label + occurrence count to
      // distinguish duplicate labels.
      const occ = occurrenceMap.get(entry.label) ?? 0;
      occurrenceMap.set(entry.label, occ + 1);
      sourceId = `${entry.label}#${occ}`;
      operationalId = entry.label;
    }

    const key = stableKey(entry.kind, sourceId);
    operationalKeys.set(key, operationalId);

    const children = (entry.children ?? []).map((c) =>
      projectWorkEntry(c, occurrenceMap),
    );

    return {
      key,
      kind: entry.kind,
      title: entry.label,
      detail: entry.outputSummary ?? "",
      tone: mapTone(entry.tone),
      children,
    };
  }

  return {
    project(view: ActivityView): {
      live: LiveActivityView;
      operational: ActivityOperationalMap;
    } {
      const occurrenceMap = new Map<string, number>();
      return {
        live: {
          tasks: view.tasks.map((t) => ({ status: t.status, count: t.count })),
          work: view.work.map((w) => projectWorkEntry(w, occurrenceMap)),
          usage: {
            totalTokens: view.usage.totalTokens,
            cost: view.usage.cost,
            contextPressure: view.usage.contextPressure,
            durationMs: view.usage.durationMs,
          },
        },
        operational: {
          keys: operationalKeys,
        },
      };
    },
  };
}
