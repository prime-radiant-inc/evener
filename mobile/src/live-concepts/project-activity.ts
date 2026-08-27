// createLiveActivityProjector — pure projection from an ActivityView into a
// LiveActivityView for live-concept renderers. No DOM, no network, no clock.
// Same input on the same projector instance → same output.
//
// The projector INSTANCE owns a private scoped registry using nested exact
// Maps at every depth — no delimiter/path composites for hierarchy identity.
// `reset(scope)` deletes the exact scope. Stable keys survive hierarchy
// insertion/reorder/patch. Raw diagnostic IDs and no-ID fallback labels live
// in separate tagged namespaces so rawId "x" can never alias label "x".
// Raw diagnostic IDs are unique scope-wide across all parents and kinds.
// The operational snapshot maps rawId → opaque key (no-ID entries omitted)
// and is returned as a genuinely runtime-immutable wrapper — the backing
// Map lives in a #private field, the instance is Object.frozen, and `size`
// is a getter (non-assignable even via cast/Reflect).
//
// All rejections are transactional: capacity, collision, cycle, and duplicate
// errors commit zero registry entries. The production default allocator uses
// cryptographic entropy (globalThis.crypto.getRandomValues — Web Crypto,
// browser-safe, Node 19+) for a non-derivable, process-unique salt combined
// with an instance counter. No Math.random, no predictable fallback; fails
// closed if secure entropy is unavailable. Traversal is iterative
// (stack-safe) for arbitrary depth. Empty projections allocate/retain no
// new scope/root at any max size.

import type { ActivityView, WorkEntry, WorkTone } from "../services/activity";
import type { DisplayTone, LiveActivityView, LiveWorkItem } from "./model";

// --- typed projector error ---------------------------------------------------

export type ActivityProjectorErrorCode =
  | "capacity"
  | "collision"
  | "cycle"
  | "raw-duplicate"
  | "no-id-duplicate";

export class ActivityProjectorError extends Error {
  readonly code: ActivityProjectorErrorCode;
  constructor(code: ActivityProjectorErrorCode, message: string) {
    super(message);
    this.name = "ActivityProjectorError";
    this.code = code;
  }
}

// --- immutable readonly map wrapper ------------------------------------------
// Genuinely runtime-immutable: the internal Map is a #private field
// inaccessible from outside the class. The instance is Object.frozen so
// casts cannot add shadow properties, and `size` is a getter so casts
// cannot assign it. set/delete/clear do not exist on the wrapper.

class ImmutableReadonlyMap<K, V> implements ReadonlyMap<K, V> {
  #map: Map<K, V>;
  get size(): number {
    return this.#map.size;
  }
  constructor(entries: Iterable<[K, V]>) {
    this.#map = new Map(entries);
    Object.freeze(this);
  }
  get(key: K): V | undefined {
    return this.#map.get(key);
  }
  has(key: K): boolean {
    return this.#map.has(key);
  }
  forEach(
    callback: (value: V, key: K, map: ReadonlyMap<K, V>) => void,
    thisArg?: unknown,
  ): void {
    this.#map.forEach((v, k) => {
      callback.call(thisArg, v, k, this);
    });
  }
  entries(): MapIterator<[K, V]> {
    return this.#map.entries();
  }
  keys(): MapIterator<K> {
    return this.#map.keys();
  }
  values(): MapIterator<V> {
    return this.#map.values();
  }
  [Symbol.iterator](): MapIterator<[K, V]> {
    return this.#map[Symbol.iterator]();
  }
}

// --- operational map (snapshot) ----------------------------------------------
// Direction: raw operational ID → opaque key. No-ID/display-label entries
// are omitted. The wrapper is a genuinely immutable lookup — no registry
// references, no exposed mutation methods.

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
  /** @internal Test-only oracle: number of retained scope roots. */
  _testGetRetainedScopeCount?(): number;
}

// --- key allocator -----------------------------------------------------------

export type OpaqueKeyAllocator = () => string;

// --- entropy seam -------------------------------------------------------------
// Injectable source of cryptographic random bytes. The default uses
// globalThis.crypto.getRandomValues (Web Crypto) — browser-safe and
// available in Node 19+. No predictable fallback; fails closed if
// secure entropy is unavailable.

export type EntropySource = () => Uint8Array;

function generateSalt(entropy?: EntropySource): string {
  const source = entropy ?? defaultEntropy;
  const bytes = source();
  let salt = "";
  for (const b of bytes) {
    salt += b.toString(36).padStart(2, "0");
  }
  return salt;
}

function defaultEntropy(): Uint8Array {
  const crypto = globalThis.crypto;
  if (
    crypto === undefined ||
    typeof crypto.getRandomValues !== "function"
  ) {
    throw new Error(
      "Secure entropy source unavailable for activity projector key generation",
    );
  }
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  return bytes;
}

let factoryCounter = 0;

function defaultAllocator(entropy?: EntropySource): OpaqueKeyAllocator {
  const salt = generateSalt(entropy);
  const id = factoryCounter;
  factoryCounter += 1;
  let n = 0;
  return () => {
    n += 1;
    return `${salt}_${id}_${n}`;
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

// --- hierarchy node (nested exact Maps, no delimiter strings) ----------------

interface HierarchyNode {
  // kind → namespace → sourceId → opaque key
  entries: Map<string, Map<string, Map<string, string>>>;
  // kind → namespace → sourceId → child node
  children: Map<string, Map<string, Map<string, HierarchyNode>>>;
}

function newHierarchyNode(): HierarchyNode {
  return { entries: new Map(), children: new Map() };
}

// Look up an existing key at a parent node (read-only, creates nothing).
function lookupKey(
  parentNode: HierarchyNode | undefined,
  kind: string,
  ns: string,
  sourceId: string,
): string | undefined {
  if (parentNode === undefined) return undefined;
  const nsMap = parentNode.entries.get(kind);
  if (nsMap === undefined) return undefined;
  const srcMap = nsMap.get(ns);
  if (srcMap === undefined) return undefined;
  return srcMap.get(sourceId);
}

// Look up an existing child node (read-only, creates nothing).
function lookupChild(
  parentNode: HierarchyNode | undefined,
  kind: string,
  ns: string,
  sourceId: string,
): HierarchyNode | undefined {
  if (parentNode === undefined) return undefined;
  const nsMap = parentNode.children.get(kind);
  if (nsMap === undefined) return undefined;
  const srcMap = nsMap.get(ns);
  if (srcMap === undefined) return undefined;
  return srcMap.get(sourceId);
}

// Get-or-create a child node (mutation, only during commit).
function getOrCreateChild(
  parentNode: HierarchyNode,
  kind: string,
  ns: string,
  sourceId: string,
): HierarchyNode {
  let nsMap = parentNode.children.get(kind);
  if (nsMap === undefined) {
    nsMap = new Map();
    parentNode.children.set(kind, nsMap);
  }
  let srcMap = nsMap.get(ns);
  if (srcMap === undefined) {
    srcMap = new Map();
    nsMap.set(ns, srcMap);
  }
  let child = srcMap.get(sourceId);
  if (child === undefined) {
    child = newHierarchyNode();
    srcMap.set(sourceId, child);
  }
  return child;
}

// Set a key at a parent node (mutation, only during commit).
function setKey(
  parentNode: HierarchyNode,
  kind: string,
  ns: string,
  sourceId: string,
  key: string,
): void {
  let nsMap = parentNode.entries.get(kind);
  if (nsMap === undefined) {
    nsMap = new Map();
    parentNode.entries.set(kind, nsMap);
  }
  let srcMap = nsMap.get(ns);
  if (srcMap === undefined) {
    srcMap = new Map();
    nsMap.set(ns, srcMap);
  }
  srcMap.set(sourceId, key);
}

// Iteratively collect all keys in a scope's hierarchy tree (for reset).
function collectAllKeys(root: HierarchyNode): string[] {
  const keys: string[] = [];
  const stack: HierarchyNode[] = [root];
  while (stack.length > 0) {
    const node = stack.pop();
    if (node === undefined) break;
    for (const [, nsMap] of node.entries) {
      for (const [, srcMap] of nsMap) {
        for (const [, key] of srcMap) {
          keys.push(key);
        }
      }
    }
    for (const [, nsMap] of node.children) {
      for (const [, srcMap] of nsMap) {
        for (const [, child] of srcMap) {
          stack.push(child);
        }
      }
    }
  }
  return keys;
}

// --- collected entry (validation phase) -------------------------------------

interface CollectedEntry {
  entry: WorkEntry;
  entryKind: string;
  ns: string; // "raw" or "label"
  sourceId: string;
  rawId: string | null;
  existingKey: string | undefined;
  parentIndex: number; // -1 for root
  // The parent node in the EXISTING registry (read-only, from before this
  // projection). May be undefined if the scope or path doesn't exist yet.
  existingParentNode: HierarchyNode | undefined;
  // The child node in the EXISTING registry for this entry's children to
  // look up against (read-only).
  existingChildNode: HierarchyNode | undefined;
  childIndices: number[];
}

// --- projector factory -------------------------------------------------------

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveActivityProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
  entropy?: EntropySource;
}): LiveActivityProjector {
  const alloc = options?.allocator ?? defaultAllocator(options?.entropy);
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  const registry = new Map<string, HierarchyNode>();
  const allocatedKeys = new Set<string>();
  let registrySize = 0;

  return {
    project(
      view: ActivityView,
      opts: ActivityProjectOptions,
    ): {
      live: LiveActivityView;
      operational: ActivityOperationalMap;
    } {
      const scope = opts.scope;
      const scopeRoot = registry.get(scope);
      const seenRawIds = new Set<string>(); // scope-wide uniqueness
      // per-parent: parentIndex → kind → Set<label> (exact nested sets,
      // no NUL-delimited composite strings)
      const seenNoId = new Map<number, Map<string, Set<string>>>();
      const collected: CollectedEntry[] = [];
      const topLevelIndices: number[] = [];

      // --- Pass 1: iterative traversal, validate, collect -------------------
      // Stack-safe for arbitrary depth. Each frame carries a direct reference
      // to the parent's existing hierarchy node (O(1) lookup, no path
      // reconstruction). Cycle detection uses a path Set maintained across
      // visit/leave frames.

      type Frame =
        | {
            type: "enter";
            entry: WorkEntry;
            parentIndex: number;
            // The parent's existing hierarchy node (for read-only lookup).
            parentExistingNode: HierarchyNode | undefined;
          }
        | { type: "leave"; entry: WorkEntry; index: number };

      const stack: Frame[] = [];
      for (let i = view.work.length - 1; i >= 0; i--) {
        const entry = view.work[i];
        if (entry !== undefined) {
          stack.push({
            type: "enter",
            entry,
            parentIndex: -1,
            parentExistingNode: scopeRoot,
          });
        }
      }

      const pathSet = new Set<WorkEntry>();

      while (stack.length > 0) {
        const frame = stack.pop();
        if (frame === undefined) break;

        if (frame.type === "enter") {
          // Cycle detection: entry already on current path → cycle error.
          if (pathSet.has(frame.entry)) {
            throw new ActivityProjectorError(
              "cycle",
              "Activity work entry cycle detected",
            );
          }
          pathSet.add(frame.entry);

          const rawId = frame.entry.diagnostics?.rawId;
          const effectiveRawId =
            rawId !== undefined && rawId !== "" ? rawId : null;
          const hasRawId = effectiveRawId !== null;
          const ns = hasRawId ? "raw" : "label";
          const sourceId = hasRawId ? effectiveRawId : frame.entry.label;

          // Raw ID scope-wide uniqueness (across all parents and kinds).
          if (hasRawId) {
            if (seenRawIds.has(effectiveRawId)) {
              throw new ActivityProjectorError(
                "raw-duplicate",
                "Raw diagnostic ID duplicate — cannot assign distinct stable keys",
              );
            }
            seenRawIds.add(effectiveRawId);
          } else {
            // No-ID per-parent duplicate check (kind+label under same parent).
            // Uses exact nested kind → Set<label> maps — no NUL composites.
            let kindMap = seenNoId.get(frame.parentIndex);
            if (kindMap === undefined) {
              kindMap = new Map();
              seenNoId.set(frame.parentIndex, kindMap);
            }
            let labelSet = kindMap.get(frame.entry.kind);
            if (labelSet === undefined) {
              labelSet = new Set();
              kindMap.set(frame.entry.kind, labelSet);
            }
            if (labelSet.has(frame.entry.label)) {
              throw new ActivityProjectorError(
                "no-id-duplicate",
                "Indistinguishable duplicate activity entries — cannot assign distinct stable keys",
              );
            }
            labelSet.add(frame.entry.label);
          }

          // Look up existing key (read-only, O(1) via direct parent node ref).
          const existingKey = lookupKey(
            frame.parentExistingNode,
            frame.entry.kind,
            ns,
            sourceId,
          );

          // Look up the existing child node for this entry's children.
          const existingChildNode = lookupChild(
            frame.parentExistingNode,
            frame.entry.kind,
            ns,
            sourceId,
          );

          const index = collected.length;
          collected.push({
            entry: frame.entry,
            entryKind: frame.entry.kind,
            ns,
            sourceId,
            rawId: effectiveRawId,
            existingKey,
            parentIndex: frame.parentIndex,
            existingParentNode: frame.parentExistingNode,
            existingChildNode,
            childIndices: [],
          });

          if (frame.parentIndex < 0) {
            topLevelIndices.push(index);
          } else {
            const parent = collected[frame.parentIndex];
            if (parent !== undefined) {
              parent.childIndices.push(index);
            }
          }

          // Push leave frame, then children (reverse for correct order).
          stack.push({ type: "leave", entry: frame.entry, index });
          const childEntries = frame.entry.children ?? [];
          for (let i = childEntries.length - 1; i >= 0; i--) {
            const child = childEntries[i];
            if (child !== undefined) {
              stack.push({
                type: "enter",
                entry: child,
                parentIndex: index,
                parentExistingNode: existingChildNode,
              });
            }
          }
        } else {
          // Leave: remove from path set.
          pathSet.delete(frame.entry);
        }
      }

      // --- Capacity check (before any mutation) -----------------------------

      const newEntryCount = collected.reduce(
        (count, ce) => count + (ce.existingKey === undefined ? 1 : 0),
        0,
      );
      if (registrySize + newEntryCount > maxReg) {
        throw new ActivityProjectorError(
          "capacity",
          "Activity projector registry capacity exceeded",
        );
      }

      // --- Pass 2: allocate keys (no registry mutation) ---------------------
      // If a collision is detected, roll back all keys allocated in this pass.

      const newKeys = new Map<number, string>(); // collected index → key
      for (const [i, ce] of collected.entries()) {
        if (ce.existingKey !== undefined) continue;
        const key = alloc();
        if (allocatedKeys.has(key)) {
          // Collision: roll back this pass's allocations.
          for (const k of newKeys.values()) {
            allocatedKeys.delete(k);
          }
          throw new ActivityProjectorError(
            "collision",
            "Activity projector key allocator collision detected",
          );
        }
        allocatedKeys.add(key);
        newKeys.set(i, key);
      }

      // --- Pass 3: commit to registry (can't fail — capacity/collision checked)
      // Only create a scope root if there are entries to commit. Empty
      // projections must not allocate or retain any new scope/root.

      if (collected.length > 0) {
        let commitRoot = registry.get(scope);
        if (commitRoot === undefined) {
          commitRoot = newHierarchyNode();
          registry.set(scope, commitRoot);
        }
        // Process in order: parents are always committed before children.
        // For each entry, the commit-time parent node is:
        //   - commitRoot if parentIndex < 0 (root entry)
        //   - the parent's commit-time child node otherwise
        const commitChildNodes: HierarchyNode[] = [];
        for (const [i, ce] of collected.entries()) {
          const parentIdx = ce.parentIndex;
          const commitParent: HierarchyNode =
            parentIdx < 0
              ? commitRoot
              : commitChildNodes[parentIdx] ?? commitRoot;
          // Get or create the child node for this entry under its parent.
          const childNode = getOrCreateChild(
            commitParent,
            ce.entryKind,
            ce.ns,
            ce.sourceId,
          );
          commitChildNodes.push(childNode);
          // Set the key if this is a new entry.
          if (ce.existingKey === undefined) {
            const key = newKeys.get(i);
            if (key !== undefined) {
              setKey(commitParent, ce.entryKind, ce.ns, ce.sourceId, key);
              registrySize += 1;
            }
          }
        }
      }

      // --- Pass 4: build LiveWorkItem tree and operational map --------------

      const builtItems: (LiveWorkItem | undefined)[] = new Array(
        collected.length,
      );
      const opEntries: [string, string][] = []; // rawId → key

      for (let i = collected.length - 1; i >= 0; i--) {
        const ce = collected[i];
        if (ce === undefined) continue;
        const key = ce.existingKey ?? newKeys.get(i);
        if (key === undefined) continue;
        const children = ce.childIndices
          .map((idx) => builtItems[idx])
          .filter((item): item is LiveWorkItem => item !== undefined);
        builtItems[i] = {
          key,
          kind: ce.entry.kind,
          title: ce.entry.label,
          detail: ce.entry.outputSummary ?? "",
          tone: mapTone(ce.entry.tone),
          children,
        };
        if (ce.rawId !== null) {
          opEntries.push([ce.rawId, key]);
        }
      }

      const topLevel = topLevelIndices
        .map((idx) => builtItems[idx])
        .filter((item): item is LiveWorkItem => item !== undefined);
      const opMap = new ImmutableReadonlyMap<string, string>(opEntries);
      const operational: ActivityOperationalMap = { keys: opMap };
      Object.freeze(operational);

      return {
        live: {
          tasks: view.tasks.map((t) => ({ status: t.status, count: t.count })),
          work: topLevel,
          usage: {
            totalTokens: view.usage.totalTokens,
            cost: view.usage.cost,
            contextPressure: view.usage.contextPressure,
            durationMs: view.usage.durationMs,
          },
        },
        operational,
      };
    },

    reset(scope?: string): void {
      if (scope === undefined) {
        registry.clear();
        allocatedKeys.clear();
        registrySize = 0;
      } else {
        const scopeRoot = registry.get(scope);
        if (scopeRoot !== undefined) {
          const removedKeys = collectAllKeys(scopeRoot);
          for (const key of removedKeys) {
            allocatedKeys.delete(key);
          }
          registrySize -= removedKeys.length;
          registry.delete(scope);
        }
      }
    },

    dispose(): void {
      registry.clear();
      allocatedKeys.clear();
      registrySize = 0;
    },

    _testGetRetainedScopeCount(): number {
      return registry.size;
    },
  };
}
