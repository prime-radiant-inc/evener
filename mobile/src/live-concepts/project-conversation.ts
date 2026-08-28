// createLiveConversationProjector — pure projection from a MobileConversation
// into a LiveConversationView for live-concept renderers. No DOM, no network,
// no clock. Same input on the same projector instance → same output.
//
// The projector INSTANCE owns a private scoped registry. Same source item/
// question/option gets the same opaque non-derivable display key and
// sequenceLabel across prepend, insert, reorder, delta, and re-projection.
// Different IDs/namespaces do not collide. Raw ref/session/item/call/
// question/option IDs appear only in the private operational map returned
// alongside the view — never in the serialized view, display key,
// sequenceLabel, threadKey, project, body, or DOM-bound fields.
//
// C1: All identities use exact nested Maps (TupleRegistry) keyed by string
// tuples — no delimiter/NUL composites anywhere. Question identity includes
// batch callId + question key; option identity includes callId + question key
// + label + detail. Hostile `:` / NUL in any component cannot alias.
//
// I1: Every projection is transactional. All allocations stage in temporary
// registries; only on successful build are they committed. Allocator
// collision, duplicate option/question, capacity overflow, or any validation
// error commits ZERO identities/counters/scopes — the prior registry is
// unchanged and retry works.
//
// I2: Operational map snapshots are genuinely runtime-immutable (FrozenMap).
// Cast/set/delete/clear throw without mutation. No registry refs escape.
//
// I3: Zero-question batch emits NO transcript row and NO question card; no
// private mappings are created.
//
// I4: Oversized UTF-8 content yields exactly one truncation marker even if
// the input already contains the marker one or many times. Byte cap and
// code-point boundary are preserved. A literal trailing marker is ambiguous
// (genuine content may end with it) — the projector does NOT infer
// truncated from the suffix. Set membership in the store's
// truncatedItemIds identifies store truncation; the projector combines
// that with its own oversized-content truncation.

import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type {
  DisplayTone,
  LiveConversationView,
  LiveQuestionView,
  LiveTranscriptItem,
} from "./model";

// --- truncation (I4) ---------------------------------------------------------

const MAX_LIVE_BYTES = 64 * 1024;
const TRUNCATION_MARKER = "… truncated";

const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

function truncateToValidUtf8(encoded: Uint8Array, targetBytes: number): string {
  if (targetBytes <= 0) return "";
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let cut = Math.min(targetBytes, encoded.length);
  while (cut > 0) {
    try {
      return decoder.decode(encoded.subarray(0, cut));
    } catch {
      cut -= 1;
    }
  }
  return "";
}

// If content fits within cap, preserve as-is — the projector returns
// truncated:false. A literal trailing marker is ambiguous: genuine content
// may legitimately end with "… truncated", so the suffix alone does not
// identify store truncation. The caller combines this with the store's
// truncatedItemIds set (authoritative: has(sourceItem.id)). If content
// exceeds cap, strip ALL existing markers so the output has exactly one,
// then truncate and add a single marker, and return truncated:true.

// Build the KMP failure (partial match) table for the marker. failure[i] is
// the length of the longest proper prefix of marker[0..i) that is also a
// suffix of marker[0..i).
function buildKmpFailure(marker: string): number[] {
  const fail = new Array<number>(marker.length).fill(0);
  let k = 0;
  for (let i = 1; i < marker.length; i++) {
    while (k > 0 && marker[i] !== marker[k]) k = fail[k - 1] ?? 0;
    if (marker[i] === marker[k]) k++;
    fail[i] = k;
  }
  return fail;
}

// Linear-time removal of all occurrences of `marker` from `text`, including
// occurrences formed by joining text across a removed marker. Uses a stack
// of (char, kmpState) pairs: each character is pushed once and popped at most
// once, giving O(n) total work. When a full marker match completes, the
// matched characters are popped, and the KMP state of the new stack top
// restores the prior match state so join-created markers are detected
// immediately without rescanning.
function stripMarkersLinear(text: string, marker: string): string {
  if (marker.length === 0) return text;
  const fail = buildKmpFailure(marker);
  // Stack entries: [character, kmpState]. kmpState is the length of the
  // longest prefix of marker that matches ending at this stack position.
  const stack: Array<[string, number]> = [];

  for (const ch of text) {
    // Compute the KMP state for this character based on the current stack top.
    const top = stack.length > 0 ? stack[stack.length - 1] : undefined;
    let state = top ? top[1] : 0;
    while (state > 0 && ch !== marker[state]) state = fail[state - 1] ?? 0;
    if (ch === marker[state]) state++;

    if (state === marker.length) {
      // Full marker matched — pop the marker-length characters off the stack.
      // The marker itself was never fully pushed (we detect at the last char),
      // so we need to pop (marker.length - 1) characters that were pushed as
      // partial matches, plus we don't push this character.
      const popCount = marker.length - 1;
      stack.length -= popCount;
      // After popping, restore the match state from the new stack top (if any).
      // This is the key to detecting join-created markers in O(1) per char.
    } else {
      stack.push([ch, state]);
    }
  }

  return stack.map((entry) => entry[0]).join("");
}

function truncate(text: string): { body: string; truncated: boolean } {
  const encoded = textEncoder.encode(text);

  if (encoded.length <= MAX_LIVE_BYTES) {
    // Within cap: body unchanged, projector did not truncate. The store's
    // truncatedItemIds set (passed via options) is the authoritative source
    // for whether this item was truncated — no suffix inference here.
    return { body: text, truncated: false };
  }

  // Oversized: strip ALL existing markers in O(n), then truncate + add
  // exactly one. The linear strip uses a KMP stack-based reducer that
  // removes markers immediately and restores prior match state so
  // join-created markers are detected without rescanning.
  const stripped = stripMarkersLinear(text, TRUNCATION_MARKER);
  const strippedEncoded = textEncoder.encode(stripped);

  const targetBytes = MAX_LIVE_BYTES - markerBytes.length;
  const truncatedContent = truncateToValidUtf8(strippedEncoded, targetBytes);
  return { body: truncatedContent + TRUNCATION_MARKER, truncated: true };
}

// --- tone --------------------------------------------------------------------

function conversationTone(status: string): DisplayTone {
  if (status === "running") return "running";
  if (status === "error" || status === "failed") return "failed";
  if (status === "ready" || status === "idle") return "idle";
  return "unknown";
}

// --- error -------------------------------------------------------------------

export class ProjectionCapacityError extends Error {
  constructor() {
    super("projection capacity exceeded");
    this.name = "ProjectionCapacityError";
  }
}

// --- runtime-immutable map wrapper (I2) --------------------------------------

// A genuinely immutable map: set/delete/clear are defined (so casts to Map
// hit them) but throw without mutating the underlying data. The backing Map
// is hidden behind a #private field — no escape hatch via casts or property
// enumeration.
class FrozenMap<K, V> implements ReadonlyMap<K, V> {
  #map: Map<K, V>;

  constructor(entries: Iterable<[K, V]>) {
    this.#map = new Map(entries);
  }

  get size(): number {
    return this.#map.size;
  }

  get(key: K): V | undefined {
    return this.#map.get(key);
  }

  has(key: K): boolean {
    return this.#map.has(key);
  }

  keys(): MapIterator<K> {
    return this.#map.keys();
  }

  values(): MapIterator<V> {
    return this.#map.values();
  }

  entries(): MapIterator<[K, V]> {
    return this.#map.entries();
  }

  forEach(
    callback: (value: V, key: K, map: ReadonlyMap<K, V>) => void,
    thisArg?: unknown,
  ): void {
    this.#map.forEach((value, key) => {
      callback.call(thisArg, value, key, this);
    });
  }

  [Symbol.iterator](): MapIterator<[K, V]> {
    return this.#map.entries();
  }

  get [Symbol.toStringTag](): string {
    return "FrozenMap";
  }

  // Mutation guards — present so `as Map<K,V>` casts invoke these, not a
  // silent mutation of a real Map. They always throw, never mutate.
  set(_key: K, _value: V): this {
    throw new TypeError("Cannot mutate a frozen map");
  }

  delete(_key: K): boolean {
    throw new TypeError("Cannot mutate a frozen map");
  }

  clear(): void {
    throw new TypeError("Cannot mutate a frozen map");
  }
}

// --- tuple registry (C1: exact nested Maps, no delimiter composites) --------

// A registry of values keyed by exact string tuples, implemented as nested
// Maps. Each dimension is a separate Map key — hostile delimiters or NUL
// in any component cannot alias with another tuple.
class TupleRegistry<V> {
  private root = new Map<string, unknown>();
  private _size = 0;

  get(path: readonly string[]): V | undefined {
    let node: Map<string, unknown> | undefined = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      node = node.get(key) as Map<string, unknown> | undefined;
      if (node === undefined) return undefined;
    }
    const last = path[path.length - 1] as string;
    return node?.get(last) as V | undefined;
  }

  has(path: readonly string[]): boolean {
    let node: Map<string, unknown> | undefined = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      node = node.get(key) as Map<string, unknown> | undefined;
      if (node === undefined) return false;
    }
    const last = path[path.length - 1] as string;
    return node?.has(last) ?? false;
  }

  set(path: readonly string[], val: V): boolean {
    let node: Map<string, unknown> = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      let next = node.get(key) as Map<string, unknown> | undefined;
      if (next === undefined) {
        next = new Map<string, unknown>();
        node.set(key, next);
      }
      node = next;
    }
    const last = path[path.length - 1] as string;
    const isNew = !node.has(last);
    if (isNew) this._size++;
    node.set(last, val);
    return isNew;
  }

  // Delete all entries under a given scope (first path element). Returns
  // freed values for allocated-key cleanup.
  deleteScope(scope: string): V[] {
    const values: V[] = [];
    const collect = (node: Map<string, unknown>) => {
      for (const [, v] of node) {
        if (v instanceof Map) {
          collect(v as Map<string, unknown>);
        } else {
          values.push(v as V);
          this._size--;
        }
      }
    };
    const sub = this.root.get(scope);
    if (sub !== undefined) {
      collect(sub as Map<string, unknown>);
      this.root.delete(scope);
    }
    return values;
  }

  clear(): void {
    this.root.clear();
    this._size = 0;
  }

  get size(): number {
    return this._size;
  }

  *entries(): IterableIterator<[string[], V]> {
    const walk = function* (
      node: Map<string, unknown>,
      prefix: string[],
    ): IterableIterator<[string[], V]> {
      for (const [k, v] of node) {
        const p = [...prefix, k];
        if (v instanceof Map) {
          yield* walk(v as Map<string, unknown>, p);
        } else {
          yield [p, v as V];
        }
      }
    };
    yield* walk(this.root, []);
  }
}

// --- operational map (snapshot) ----------------------------------------------

export interface QuestionLink {
  readonly callId: string;
  readonly questionKey: string;
}

export interface OptionLink {
  readonly callId: string;
  readonly questionKey: string;
  readonly label: string;
  readonly detail: string;
}

export interface ConversationOperationalMap {
  readonly itemKeys: ReadonlyMap<string, string>;
  readonly questionKeys: ReadonlyMap<string, QuestionLink>;
  readonly optionKeys: ReadonlyMap<string, OptionLink>;
}

// --- projector options -------------------------------------------------------

export interface ConversationProjectOptions {
  ref: string;
  olderCursor: string | null;
  projectLabel: string;
  updatedLabel: string | null;
  // Plan3 host projection: authoritative set of item IDs the store has
  // truncated (frozen). The projector marks an item truncated:true when
  // the projector itself truncates oversized content OR the store set
  // marks the item's ID as truncated. This replaces suffix inference.
  truncatedItemIds: ReadonlySet<string>;
}

// --- projector instance ------------------------------------------------------

export interface LiveConversationProjector {
  project(
    conv: MobileConversation,
    options: ConversationProjectOptions,
  ): { view: LiveConversationView; operational: ConversationOperationalMap };
  reset(scope?: string): void;
  dispose(): void;
}

// --- key allocator -----------------------------------------------------------

export type OpaqueKeyAllocator = () => string;

let moduleAllocatorCounter = 0;

function defaultAllocator(): OpaqueKeyAllocator {
  const prefix = `p${moduleAllocatorCounter++}`;
  let n = 0;
  return () => `${prefix}-${++n}`;
}

// --- projector factory -------------------------------------------------------

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveConversationProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveConversationProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  // C1: Exact nested Maps keyed by string tuples. No delimiter concatenation.
  const keyRegistry = new TupleRegistry<string>();
  const seqRegistry = new TupleRegistry<string>();

  // All allocator-returned strings currently in use (for collision detection).
  const allocatedKeys = new Set<string>();

  // Total identity count (entries in keyRegistry only; seqRegistry tracks
  // the same identity tuples for items but is not double-counted).
  let totalIdentities = 0;

  // --- preflight: count new identities using exact tuple paths (C1) --------

  function countNewIdentities(
    items: readonly MobileTimelineItem[],
    scope: string,
  ): number {
    const seen = new TupleRegistry<true>();
    let newCount = 0;

    function check(path: string[]): void {
      if (seen.has(path)) return;
      seen.set(path, true);
      if (!keyRegistry.has(path)) newCount += 1;
    }

    check([scope, "thread"]);
    for (const item of items) {
      if (item.kind === "question") {
        // I3: empty batch → no identities.
        if (item.batch.questions.length === 0) continue;
        const callId = item.batch.callId;
        for (const q of item.batch.questions) {
          if (q === undefined) continue;
          check([scope, "qitem", callId, q.key]);
          check([scope, "question", callId, q.key]);
          for (const o of q.options) {
            check([scope, "option", callId, q.key, o.label, o.detail]);
          }
        }
      } else {
        check([scope, "item", item.id]);
      }
    }
    return newCount;
  }

  return {
    project(
      conv: MobileConversation,
      opts: ConversationProjectOptions,
    ): { view: LiveConversationView; operational: ConversationOperationalMap } {
      const { ref, olderCursor, projectLabel, updatedLabel } = opts;
      const truncatedItemIds = opts.truncatedItemIds;
      const scope = ref;

      // --- I1: Preflight capacity check (before any allocation) -------------
      const newCount = countNewIdentities(conv.items, scope);
      if (totalIdentities + newCount > maxReg) {
        throw new ProjectionCapacityError();
      }

      // --- I1: Build phase — stage all allocations transactionally ----------
      // Staging registries are local; only committed on success. If any error
      // occurs, staging is discarded and zero identities are committed.
      const stagedKeys = new TupleRegistry<string>();
      const stagedSeqs = new TupleRegistry<string>();
      const stagedAllocated = new Set<string>();

      function stageKey(path: string[]): string {
        let k = keyRegistry.get(path);
        if (k !== undefined) return k;

        k = stagedKeys.get(path);
        if (k !== undefined) return k;

        k = alloc();
        if (allocatedKeys.has(k) || stagedAllocated.has(k)) {
          throw new ProjectionCapacityError();
        }
        stagedAllocated.add(k);
        stagedKeys.set(path, k);
        return k;
      }

      function stageSeq(path: string[]): string {
        let s = seqRegistry.get(path);
        if (s !== undefined) return s;

        s = stagedSeqs.get(path);
        if (s !== undefined) return s;

        s = alloc();
        if (allocatedKeys.has(s) || stagedAllocated.has(s)) {
          throw new ProjectionCapacityError();
        }
        stagedAllocated.add(s);
        stagedSeqs.set(path, s);
        return s;
      }

      // --- Build question views and key mappings (C1: callId in identity) ---
      const questions: LiveQuestionView[] = [];
      const keyMap = new Map<string, Map<string, string>>(); // callId → q.key → qKey
      const opQuestionKeys = new Map<string, QuestionLink>();
      const opOptionKeys = new Map<string, OptionLink>();
      const seenQuestions = new Set<string>();

      for (const item of conv.items) {
        if (item.kind !== "question") continue;
        // I3: zero-question batch → no card, no mappings.
        if (item.batch.questions.length === 0) continue;
        const callId = item.batch.callId;

        for (const q of item.batch.questions) {
          if (q === undefined) continue;

          // Detect duplicate question (same callId + q.key).
          const qId = JSON.stringify([callId, q.key]);
          if (seenQuestions.has(qId)) {
            throw new Error("Indistinguishable duplicate question");
          }
          seenQuestions.add(qId);

          const qKey = stageKey([scope, "question", callId, q.key]);
          if (!keyMap.has(callId)) keyMap.set(callId, new Map());
          keyMap.get(callId)?.set(q.key, qKey);
          opQuestionKeys.set(
            qKey,
            Object.freeze({ callId, questionKey: q.key }),
          );

          // Detect duplicate options within this question (exact label+detail).
          const seenOptions = new Set<string>();
          const options = q.options.map((o) => {
            const optId = JSON.stringify([o.label, o.detail]);
            if (seenOptions.has(optId)) {
              throw new Error("Indistinguishable duplicate option");
            }
            seenOptions.add(optId);
            const optKey = stageKey([
              scope,
              "option",
              callId,
              q.key,
              o.label,
              o.detail,
            ]);
            opOptionKeys.set(
              optKey,
              Object.freeze({
                callId,
                questionKey: q.key,
                label: o.label,
                detail: o.detail,
              }),
            );
            return { key: optKey, label: o.label, detail: o.detail };
          });

          questions.push({
            key: qKey,
            header: q.header,
            prompt: q.question,
            options,
            multiple: q.multiSelect,
          });
        }
      }

      // --- Build transcript items (C1: exact paths, I3: empty batch) --------
      const opItemKeys = new Map<string, string>();
      const items: LiveTranscriptItem[] = [];

      for (const item of conv.items) {
        const rows: LiveTranscriptItem[] = [];

        switch (item.kind) {
          case "user":
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "user",
              label: "You",
              body: item.text,
              tone: "idle",
              streaming: false,
              truncated: false,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;

          case "assistant": {
            const { body, truncated } = truncate(item.markdown);
            // Plan3: truncated is projector-actually-truncated OR the store
            // marked this item's ID as truncated (authoritative freeze set).
            const isTruncated = truncated || truncatedItemIds.has(item.id);
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "assistant",
              label: "Assistant",
              body,
              tone: item.streaming ? "running" : "idle",
              streaming: item.streaming,
              truncated: isTruncated,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;
          }

          case "activity": {
            const outputText = item.detail.output ?? "";
            const { body, truncated } =
              outputText.length > 0
                ? truncate(outputText)
                : { body: "", truncated: false };
            // Plan3: truncated is projector-actually-truncated OR the store
            // marked this item's ID as truncated (authoritative freeze set).
            const isTruncated = truncated || truncatedItemIds.has(item.id);
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "tool",
              label: item.label,
              body,
              tone:
                item.state === "running"
                  ? "running"
                  : item.state === "failed"
                    ? "failed"
                    : "success",
              streaming: item.state === "running",
              truncated: isTruncated,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;
          }

          case "notice":
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "user",
              label: "Notice",
              body: item.text,
              tone: item.tone === "warning" ? "attention" : "idle",
              streaming: false,
              truncated: false,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;

          case "question": {
            // I3: zero-question batch → no transcript rows.
            if (item.batch.questions.length === 0) break;
            const callId = item.batch.callId;
            for (const q of item.batch.questions) {
              if (q === undefined) continue;
              const qKey = keyMap.get(callId)?.get(q.key) ?? null;
              rows.push({
                key: stageKey([scope, "qitem", callId, q.key]),
                kind: "question",
                label: q.header,
                body: q.question,
                tone: "attention",
                streaming: false,
                truncated: false,
                questionKey: qKey,
                sequenceLabel: stageSeq([scope, "qitem", callId, q.key]),
              });
            }
            break;
          }

          case "failure":
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "failure",
              label: item.title,
              body: item.detail,
              tone: "failed",
              streaming: false,
              truncated: false,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;

          case "attachments": {
            const names = item.items
              .map((a) => a.name ?? "attachment")
              .join(", ");
            rows.push({
              key: stageKey([scope, "item", item.id]),
              kind: "attachment",
              label: "Attachments",
              body: names,
              tone: "idle",
              streaming: false,
              truncated: false,
              questionKey: null,
              sequenceLabel: stageSeq([scope, "item", item.id]),
            });
            break;
          }
        }

        for (const row of rows) {
          opItemKeys.set(row.key, item.id);
          items.push(row);
        }
      }

      // Thread key (C1: exact path [scope, "thread"]).
      const threadKey = stageKey([scope, "thread"]);

      // --- I1: Commit — write staging to main registries (atomic) -----------
      for (const [path, key] of stagedKeys.entries()) {
        keyRegistry.set(path, key);
        allocatedKeys.add(key);
      }
      for (const [path, seq] of stagedSeqs.entries()) {
        seqRegistry.set(path, seq);
        allocatedKeys.add(seq);
      }
      totalIdentities += stagedKeys.size;

      const title = conv.name ?? conv.preview;

      return {
        view: {
          threadKey,
          title,
          project: projectLabel,
          status: conv.status,
          items,
          questions,
          olderAvailable: olderCursor !== null,
          tone: conversationTone(conv.status),
          updatedLabel,
        },
        operational: {
          itemKeys: new FrozenMap(opItemKeys),
          questionKeys: new FrozenMap(opQuestionKeys),
          optionKeys: new FrozenMap(opOptionKeys),
        },
      };
    },

    reset(scope?: string): void {
      if (scope === undefined) {
        keyRegistry.clear();
        seqRegistry.clear();
        allocatedKeys.clear();
        totalIdentities = 0;
      } else {
        for (const k of keyRegistry.deleteScope(scope)) {
          allocatedKeys.delete(k);
        }
        for (const s of seqRegistry.deleteScope(scope)) {
          allocatedKeys.delete(s);
        }
        totalIdentities = keyRegistry.size;
      }
    },

    dispose(): void {
      keyRegistry.clear();
      seqRegistry.clear();
      allocatedKeys.clear();
      totalIdentities = 0;
    },
  };
}
