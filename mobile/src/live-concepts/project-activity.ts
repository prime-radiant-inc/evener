// createLiveActivityProjector — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input on the same projector instance → same output.
//
// The projector INSTANCE owns a private scoped registry. Stable keys survive
// hierarchy insertion/reorder/patch. Labels are display-safe inputs only;
// raw diagnostic IDs live only in the private operational map returned
// alongside the view. Duplicate/colliding source IDs are detected and produce
// a documented safe error, never shared/swapped keys.
//
// Activity identity includes stable parent path + kind + rawId. This prevents
// key swaps when entries are reordered across parents. For entries without
// diagnostics, the label is used with a collision-safe occurrence counter.
// Duplicate raw IDs within the same scope produce a safe error.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// --- operational map (snapshot) ----------------------------------------------

export interface ActivityOperationalMap {
  readonly keys: ReadonlyMap<string, string>;
}

// --- projector instance ------------------------------------------------------

export interface LiveActivityProjector {
  project(view: ActivityView): {
    live: LiveActivityView;
    operational: ActivityOperationalMap;
  };
  reset(scope?: string): void;
  dispose(): void;
}

// --- key allocator ------------------------------------------------------------

export type OpaqueKeyAllocator = () => string;

function defaultAllocator(): OpaqueKeyAllocator {
  let n = 0;
  return () => {
    n += 1;
    return `w${n}`;
  };
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

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveActivityProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveActivityProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  // Private scoped registry: maps "scope:parentPath:kind:sourceId" → opaque key.
  const keyRegistry = new Map<string, string>();

  function evictIfNeeded(): void {
    while (keyRegistry.size >= maxReg) {
      const first = keyRegistry.keys().next();
      if (first.done) break;
      keyRegistry.delete(first.value);
    }
  }

  function stableKey(regKey: string): string {
    let k = keyRegistry.get(regKey);
    if (k === undefined) {
      evictIfNeeded();
      k = alloc();
      keyRegistry.set(regKey, k);
    }
    return k;
  }

  // C2: Activity identity includes stable parent path + kind + rawId.
  // Duplicate raw identity in the same scope → documented safe error.
  function projectWorkEntry(
    entry: WorkEntry,
    parentPath: string,
    occurrenceMap: Map<string, number>,
    seenIds: Set<string>,
  ): LiveWorkItem {
    const rawId = entry.diagnostics?.rawId;
    let sourceId: string;

    if (rawId !== undefined && rawId !== "") {
      // C2: Duplicate raw identity within the same parent path → safe error,
      // never share/swap keys. The same rawId under a different parent is a
      // legitimate distinct identity (different parentPath).
      const dupKey = `${parentPath}:${rawId}`;
      if (seenIds.has(dupKey)) {
        throw new Error(
          `Duplicate activity raw ID "${rawId}" under the same parent — cannot assign distinct stable keys`,
        );
      }
      seenIds.add(dupKey);
      sourceId = rawId;
    } else {
      // For entries without diagnostics, use label + occurrence count.
      const occ = occurrenceMap.get(entry.label) ?? 0;
      occurrenceMap.set(entry.label, occ + 1);
      sourceId = `${entry.label}#${occ}`;
    }

    // Identity includes parent path so the same child under different parents
    // gets a different key (no cross-parent aliasing).
    const childPath = `${parentPath}/${entry.kind}:${sourceId}`;
    const regKey = childPath;
    const key = stableKey(regKey);

    const children = (entry.children ?? []).map((c) =>
      projectWorkEntry(c, childPath, occurrenceMap, seenIds),
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
      const seenIds = new Set<string>();
      const opKeys = new Map<string, string>();

      const work = view.work.map((w) => {
        const item = projectWorkEntry(w, "root", occurrenceMap, seenIds);
        const rawId = w.diagnostics?.rawId;
        opKeys.set(item.key, rawId ?? w.label);
        return item;
      });

      return {
        live: {
          tasks: view.tasks.map((t) => ({ status: t.status, count: t.count })),
          work,
          usage: {
            totalTokens: view.usage.totalTokens,
            cost: view.usage.cost,
            contextPressure: view.usage.contextPressure,
            durationMs: view.usage.durationMs,
          },
        },
        operational: {
          keys: opKeys,
        },
      };
    },

    reset(scope?: string): void {
      if (scope === undefined) {
        keyRegistry.clear();
      } else {
        const prefix = `${scope}:`;
        for (const k of keyRegistry.keys()) {
          if (k.startsWith(prefix)) keyRegistry.delete(k);
        }
      }
    },

    dispose(): void {
      keyRegistry.clear();
    },
  };
}
