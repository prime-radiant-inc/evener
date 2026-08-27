// createLiveActivityProjector — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input on the same projector instance → same output.
//
// The projector INSTANCE owns a private scoped registry keyed by exact scope,
// parent identity, kind, and source identity via nested Maps — no delimiter
// prefix encoding. `reset(scope)` deletes the exact scope. Stable keys
// survive hierarchy insertion/reorder/patch. Labels are display-safe inputs
// only; raw diagnostic IDs live only in the private operational map returned
// alongside the view. Duplicate/cross-kind source IDs are detected and produce
// a generic safe error, never shared/swapped keys.
//
// The production default allocator prefixes keys process-unique via a
// module-level factory counter, so distinct instances never collide. An
// injected allocator that returns duplicate keys is detected and produces a
// generic safe error. Cycle detection traverses with an ancestry set and
// throws a generic cycle error before stack overflow. The registry is bounded
// with safe capacity rejection before mutation (no FIFO eviction):
// identical over-cap rejection leaves existing key stability.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// --- operational map (snapshot) ----------------------------------------------

export interface ActivityOperationalMap {
  readonly keys: ReadonlyMap<string, string>;
}

// --- projector options --------------------------------------------------------

export interface ActivityProjectOptions {
  scope: string;
}

// --- projector instance ------------------------------------------------------

export interface LiveActivityProjector {
  project(
    view: ActivityView,
    options: ActivityProjectOptions,
  ): {
    live: LiveActivityView;
    operational: ActivityOperationalMap;
  };
  reset(scope?: string): void;
  dispose(): void;
}

// --- key allocator ------------------------------------------------------------

export type OpaqueKeyAllocator = () => string;

// Module-level factory counter: each default instance gets a process-unique
// prefix so distinct instances never produce colliding keys.
let factoryCounter = 0;

function defaultAllocator(): OpaqueKeyAllocator {
  const prefix = `w${factoryCounter}_`;
  factoryCounter += 1;
  let n = 0;
  return () => {
    n += 1;
    return `${prefix}${n}`;
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

// Nested Maps: scope → parentPath → kind → sourceId → opaque key.
type SourceMap = Map<string, string>;
type KindMap = Map<string, SourceMap>;
type ParentMap = Map<string, KindMap>;
type ScopeRegistry = Map<string, ParentMap>;

export function createLiveActivityProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveActivityProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  const registry: ScopeRegistry = new Map();
  const allocatedKeys = new Set<string>();
  let registrySize = 0;

  function stableKey(
    scope: string,
    parentPath: string,
    kind: string,
    sourceId: string,
  ): string {
    let scopeMap = registry.get(scope);
    if (scopeMap === undefined) {
      scopeMap = new Map();
      registry.set(scope, scopeMap);
    }
    let parentMap = scopeMap.get(parentPath);
    if (parentMap === undefined) {
      parentMap = new Map();
      scopeMap.set(parentPath, parentMap);
    }
    let kindMap = parentMap.get(kind);
    if (kindMap === undefined) {
      kindMap = new Map();
      parentMap.set(kind, kindMap);
    }
    let key = kindMap.get(sourceId);
    if (key === undefined) {
      // Safe capacity rejection before mutation — no FIFO eviction.
      if (registrySize >= maxReg) {
        throw new Error("Activity projector registry capacity exceeded");
      }
      key = alloc();
      // Validate injected allocator collisions with a generic safe error.
      if (allocatedKeys.has(key)) {
        throw new Error("Activity projector key allocator collision detected");
      }
      allocatedKeys.add(key);
      kindMap.set(sourceId, key);
      registrySize += 1;
    }
    return key;
  }

  function projectWorkEntry(
    entry: WorkEntry,
    scope: string,
    parentPath: string,
    seenRawIds: Map<string, Set<string>>,
    seenNoId: Map<string, Set<string>>,
    ancestry: WeakSet<WorkEntry>,
    opKeys: Map<string, string>,
  ): LiveWorkItem {
    // Cycle detection: if this entry is already in the ancestry path, throw
    // a generic typed cycle error before stack overflow.
    if (ancestry.has(entry)) {
      throw new Error("Activity work entry cycle detected");
    }
    ancestry.add(entry);

    const rawId = entry.diagnostics?.rawId;
    let sourceId: string;

    if (rawId !== undefined && rawId !== "") {
      // Every raw diagnostic ID unique within the same parent across kinds.
      // Duplicate/cross-kind → generic safe error (no raw ID in message).
      let seen = seenRawIds.get(parentPath);
      if (seen === undefined) {
        seen = new Set();
        seenRawIds.set(parentPath, seen);
      }
      if (seen.has(rawId)) {
        throw new Error(
          "Duplicate activity source identity under the same parent — cannot assign distinct stable keys",
        );
      }
      seen.add(rawId);
      sourceId = rawId;
    } else {
      // For no-ID siblings, use kind+label only when unique under exact parent.
      // Indistinguishable duplicates → generic error (no occurrence positions).
      const noIdKey = `${entry.kind}\u0000${entry.label}`;
      let seen = seenNoId.get(parentPath);
      if (seen === undefined) {
        seen = new Set();
        seenNoId.set(parentPath, seen);
      }
      if (seen.has(noIdKey)) {
        throw new Error(
          "Indistinguishable duplicate activity entries under the same parent — cannot assign distinct stable keys",
        );
      }
      seen.add(noIdKey);
      sourceId = entry.label;
    }

    const childPath = `${parentPath}/${entry.kind}:${sourceId}`;
    const key = stableKey(scope, parentPath, entry.kind, sourceId);

    const children = (entry.children ?? []).map((c) =>
      projectWorkEntry(
        c,
        scope,
        childPath,
        seenRawIds,
        seenNoId,
        ancestry,
        opKeys,
      ),
    );

    // Recursively populate operational snapshot for every child, not only
    // top level. Caller mutation cannot change backing registry/past result
    // because opKeys is a fresh Map per projection call.
    opKeys.set(key, rawId ?? entry.label);

    ancestry.delete(entry);

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
    project(
      view: ActivityView,
      opts: ActivityProjectOptions,
    ): {
      live: LiveActivityView;
      operational: ActivityOperationalMap;
    } {
      const scope = opts.scope;
      const seenRawIds = new Map<string, Set<string>>();
      const seenNoId = new Map<string, Set<string>>();
      const ancestry = new WeakSet<WorkEntry>();
      const opKeys = new Map<string, string>();

      const work = view.work.map((w) =>
        projectWorkEntry(
          w,
          scope,
          "root",
          seenRawIds,
          seenNoId,
          ancestry,
          opKeys,
        ),
      );

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
        registry.clear();
        allocatedKeys.clear();
        registrySize = 0;
      } else {
        const scopeMap = registry.get(scope);
        if (scopeMap !== undefined) {
          for (const [, parentMap] of scopeMap) {
            for (const [, kindMap] of parentMap) {
              for (const [, key] of kindMap) {
                allocatedKeys.delete(key);
              }
              registrySize -= kindMap.size;
            }
          }
          registry.delete(scope);
        }
      }
    },

    dispose(): void {
      registry.clear();
      allocatedKeys.clear();
      registrySize = 0;
    },
  };
}
