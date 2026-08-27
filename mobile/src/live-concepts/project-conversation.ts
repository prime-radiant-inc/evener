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
// `threadKey` is a registry-allocated opaque key (not a hash of ref).
// `project` comes only from the supplied display-safe `projectLabel`, never
// `conv.sessionId`. `updatedLabel` passes the authoritative option or null —
// never fabricated. `sequenceLabel` is an opaque stable presentation label
// allocated on first sight, not an array index, and stays stable when older
// rows prepend. `questionKey` links a transcript item to its LiveQuestionView
// via an opaque key; null when the item is not a question-bearing turn.
//
// The registry uses exact nested Maps keyed separately by raw scope,
// namespace, and source ID — no delimiter-concatenated keys. `reset(scope)`
// deletes the exact scope by Map key; no prefix matching, so a scope that is
// a prefix of another is never confused. The registry is bounded by safe
// rejection (not eviction): before projecting, the projector counts all
// identities the projection would bring; if over max, it throws a generic
// typed ProjectionCapacityError and leaves the prior registry unchanged — no
// current identity loses its key.
//
// The default key allocator is process-unique via a module-level factory
// counter, so two projector instances with default allocators never collide.
// An injected allocator that returns a key already owned by another identity
// throws a generic ProjectionCapacityError. `reset` and `dispose` free
// allocated key state so it can be reused.
//
// Empty/missing question batches emit one explicit question transcript row
// with questionKey:null and a safe generic body. Multi-question batches remain
// one row/card per question. Option identity uses exact nested question scope
// + unique label/detail; indistinguishable duplicates throw a generic safe
// error whose message contains no raw q.key.

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

// --- truncation --------------------------------------------------------------

const MAX_LIVE_BYTES = 64 * 1024;
const TRUNCATION_MARKER = "… truncated";

const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

// Find the largest cut ≤ targetBytes such that encoded[0..cut] decodes as
// valid UTF-8 (no split code point at the boundary).
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

// Encode first; body including marker is guaranteed ≤ MAX_LIVE_BYTES UTF-8
// bytes. Recognizes a store-capped marker and does not duplicate it. If the
// marker-ended content exceeds the cap, it is re-truncated validly.
function truncate(text: string): { body: string; truncated: boolean } {
  const encoded = textEncoder.encode(text);

  // Check if the store already capped the text and appended the marker.
  if (text.endsWith(TRUNCATION_MARKER)) {
    const contentText = text.slice(0, text.length - TRUNCATION_MARKER.length);
    const contentEncoded = textEncoder.encode(contentText);
    if (contentEncoded.length + markerBytes.length <= MAX_LIVE_BYTES) {
      return { body: text, truncated: true };
    }
    // Content + marker exceeds cap — re-truncate the content validly.
    const targetBytes = MAX_LIVE_BYTES - markerBytes.length;
    const truncatedContent = truncateToValidUtf8(contentEncoded, targetBytes);
    return { body: truncatedContent + TRUNCATION_MARKER, truncated: true };
  }

  if (encoded.length <= MAX_LIVE_BYTES) {
    return { body: text, truncated: false };
  }

  const targetBytes = MAX_LIVE_BYTES - markerBytes.length;
  const truncatedContent = truncateToValidUtf8(encoded, targetBytes);
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

// Generic typed projection-capacity error. Used for both capacity overflow
// and allocator collision. The message is intentionally generic — it never
// contains raw scope/namespace/source IDs or allocator-returned keys.
export class ProjectionCapacityError extends Error {
  constructor() {
    super("projection capacity exceeded");
    this.name = "ProjectionCapacityError";
  }
}

// --- operational map (snapshot) ----------------------------------------------

export interface ConversationOperationalMap {
  readonly itemKeys: ReadonlyMap<string, string>;
  readonly questionKeys: ReadonlyMap<string, string>;
  readonly optionKeys: ReadonlyMap<string, string>;
}

// --- projector options -------------------------------------------------------

export interface ConversationProjectOptions {
  ref: string;
  olderCursor: string | null;
  projectLabel: string;
  updatedLabel: string | null;
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

// Module-level counter for process-unique default allocator prefixes.
// Two projector instances with the default allocator never collide because
// each gets a distinct prefix.
let moduleAllocatorCounter = 0;

function defaultAllocator(): OpaqueKeyAllocator {
  const prefix = `p${moduleAllocatorCounter++}`;
  let n = 0;
  return () => `${prefix}-${++n}`;
}

// --- nested registry ---------------------------------------------------------
// scope → namespace → sourceId → allocated value
// Exact Map-key lookup; no delimiter concatenation, no prefix matching.

type NestedMap<V> = Map<string, Map<string, Map<string, V>>>;

function nestedGet<V>(
  reg: NestedMap<V>,
  scope: string,
  ns: string,
  id: string,
): V | undefined {
  return reg.get(scope)?.get(ns)?.get(id);
}

function nestedSet<V>(
  reg: NestedMap<V>,
  scope: string,
  ns: string,
  id: string,
  val: V,
): void {
  let nsMap = reg.get(scope);
  if (!nsMap) {
    nsMap = new Map();
    reg.set(scope, nsMap);
  }
  let idMap = nsMap.get(ns);
  if (!idMap) {
    idMap = new Map();
    nsMap.set(ns, idMap);
  }
  idMap.set(id, val);
}

function nestedHas<V>(
  reg: NestedMap<V>,
  scope: string,
  ns: string,
  id: string,
): boolean {
  return reg.get(scope)?.get(ns)?.has(id) ?? false;
}

// Remove a scope entirely and return all allocated values stored under it.
function nestedDeleteScope<V>(reg: NestedMap<V>, scope: string): V[] {
  const nsMap = reg.get(scope);
  if (!nsMap) return [];
  const values: V[] = [];
  for (const [, idMap] of nsMap) {
    for (const [, val] of idMap) {
      values.push(val);
    }
  }
  reg.delete(scope);
  return values;
}

// Count total entries across all scopes (for one registry).
function nestedCount<V>(reg: NestedMap<V>): number {
  let count = 0;
  for (const [, nsMap] of reg) {
    for (const [, idMap] of nsMap) {
      count += idMap.size;
    }
  }
  return count;
}

// --- projector factory -------------------------------------------------------

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveConversationProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveConversationProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  // Private scoped registries: nested Maps keyed by scope → namespace → id.
  const keyRegistry: NestedMap<string> = new Map();
  const seqRegistry: NestedMap<string> = new Map();

  // All allocator-returned strings currently in use (for collision detection).
  // Freed on reset/dispose so keys can be reused after their scope is cleared.
  const allocatedKeys = new Set<string>();

  // Total identity count (entries in keyRegistry only; seqRegistry tracks the
  // same identity tuples for items but is not double-counted).
  let totalIdentities = 0;

  function allocate(): string {
    const k = alloc();
    if (allocatedKeys.has(k)) {
      throw new ProjectionCapacityError();
    }
    allocatedKeys.add(k);
    return k;
  }

  function stableKey(
    scope: string,
    namespace: string,
    sourceId: string,
  ): string {
    let k = nestedGet(keyRegistry, scope, namespace, sourceId);
    if (k === undefined) {
      k = allocate();
      nestedSet(keyRegistry, scope, namespace, sourceId, k);
      totalIdentities += 1;
    }
    return k;
  }

  function stableSeq(
    scope: string,
    namespace: string,
    sourceId: string,
  ): string {
    let s = nestedGet(seqRegistry, scope, namespace, sourceId);
    if (s === undefined) {
      s = allocate();
      nestedSet(seqRegistry, scope, namespace, sourceId, s);
    }
    return s;
  }

  // Pre-projection identity count: walk all items, collect unique identity
  // tuples, count how many are NOT already registered. If the total would
  // exceed max, the caller throws before touching the registry.
  function countNewIdentities(
    items: readonly MobileTimelineItem[],
    scope: string,
  ): number {
    const seen = new Set<string>();
    let newCount = 0;

    function check(ns: string, id: string): void {
      const tuple = `${ns}\u0000${id}`;
      if (seen.has(tuple)) return;
      seen.add(tuple);
      if (!nestedHas(keyRegistry, scope, ns, id)) newCount += 1;
    }

    check("thread", scope);
    for (const item of items) {
      if (item.kind === "question") {
        if (item.batch.questions.length === 0) {
          check("item", item.id);
        } else {
          for (const q of item.batch.questions) {
            if (q === undefined) continue;
            check("item", `${item.id}:${q.key}`);
            check("question", q.key);
            for (const o of q.options) {
              check("option", `${q.key}:${o.label}\u0000${o.detail}`);
            }
          }
        }
      } else {
        check("item", item.id);
      }
    }
    return newCount;
  }

  function projectItem(
    item: MobileTimelineItem,
    scope: string,
    questionKeyMap: Map<string, string>,
  ): LiveTranscriptItem[] {
    switch (item.kind) {
      case "user":
        return [
          {
            key: stableKey(scope, "item", item.id),
            kind: "user",
            label: "You",
            body: item.text,
            tone: "idle",
            streaming: false,
            truncated: false,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];

      case "assistant": {
        const { body, truncated } = truncate(item.markdown);
        return [
          {
            key: stableKey(scope, "item", item.id),
            kind: "assistant",
            label: "Assistant",
            body,
            tone: item.streaming ? "running" : "idle",
            streaming: item.streaming,
            truncated,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];
      }

      case "activity": {
        const outputText = item.detail.output ?? "";
        const { body, truncated } =
          outputText.length > 0
            ? truncate(outputText)
            : { body: "", truncated: false };
        return [
          {
            key: stableKey(scope, "item", item.id),
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
            truncated,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];
      }

      case "notice":
        return [
          {
            key: stableKey(scope, "item", item.id),
            kind: "user",
            label: "Notice",
            body: item.text,
            tone: item.tone === "warning" ? "attention" : "idle",
            streaming: false,
            truncated: false,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];

      case "question": {
        // Empty/missing question batch: one explicit row, questionKey:null,
        // safe generic body. No LiveQuestionView card is created.
        if (item.batch.questions.length === 0) {
          return [
            {
              key: stableKey(scope, "item", item.id),
              kind: "question",
              label: "Question",
              body: "No questions available.",
              tone: "idle",
              streaming: false,
              truncated: false,
              questionKey: null,
              sequenceLabel: stableSeq(scope, "item", item.id),
            },
          ];
        }

        // C4: Project one linked transcript question row per question in the
        // batch, never first-body/last-key mismatch. Each row links to its
        // own questionKey.
        const rows: LiveTranscriptItem[] = [];
        for (const q of item.batch.questions) {
          if (q === undefined) continue;
          const qKey = questionKeyMap.get(q.key) ?? null;
          const sourceId = `${item.id}:${q.key}`;
          rows.push({
            key: stableKey(scope, "item", sourceId),
            kind: "question",
            label: q.header,
            body: q.question,
            tone: "attention",
            streaming: false,
            truncated: false,
            questionKey: qKey,
            sequenceLabel: stableSeq(scope, "item", sourceId),
          });
        }
        return rows;
      }

      case "failure":
        return [
          {
            key: stableKey(scope, "item", item.id),
            kind: "failure",
            label: item.title,
            body: item.detail,
            tone: "failed",
            streaming: false,
            truncated: false,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];

      case "attachments": {
        const names = item.items.map((a) => a.name ?? "attachment").join(", ");
        return [
          {
            key: stableKey(scope, "item", item.id),
            kind: "attachment",
            label: "Attachments",
            body: names,
            tone: "idle",
            streaming: false,
            truncated: false,
            questionKey: null,
            sequenceLabel: stableSeq(scope, "item", item.id),
          },
        ];
      }
    }
  }

  function projectQuestions(
    items: readonly MobileTimelineItem[],
    scope: string,
  ): {
    questions: LiveQuestionView[];
    keyMap: Map<string, string>;
    opQuestionKeys: Map<string, string>;
    opOptionKeys: Map<string, string>;
  } {
    const questions: LiveQuestionView[] = [];
    const keyMap = new Map<string, string>(); // q.key → opaque question key
    const opQuestionKeys = new Map<string, string>();
    const opOptionKeys = new Map<string, string>();

    for (const item of items) {
      if (item.kind !== "question") continue;
      if (item.batch.questions.length === 0) continue; // empty batch: no card
      for (const q of item.batch.questions) {
        if (q === undefined) continue;
        const qKey = stableKey(scope, "question", q.key);
        keyMap.set(q.key, qKey);
        opQuestionKeys.set(qKey, q.key);

        // C3: Option identity uses stable content identity (label+detail),
        // not array index. Detect indistinguishable duplicates and safe-error.
        // The error message is generic — no raw q.key in it.
        const seenOptions = new Set<string>();
        const options = q.options.map((o) => {
          const optIdentity = `${o.label}\u0000${o.detail}`;
          if (seenOptions.has(optIdentity)) {
            // Generic safe error: says "duplicate option" but never leaks the
            // raw q.key or any internal identifier.
            throw new Error("Indistinguishable duplicate option");
          }
          seenOptions.add(optIdentity);
          const optKey = stableKey(scope, "option", `${q.key}:${optIdentity}`);
          opOptionKeys.set(optKey, optIdentity);
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
    return { questions, keyMap, opQuestionKeys, opOptionKeys };
  }

  return {
    project(
      conv: MobileConversation,
      opts: ConversationProjectOptions,
    ): { view: LiveConversationView; operational: ConversationOperationalMap } {
      const { ref, olderCursor, projectLabel, updatedLabel } = opts;
      const scope = ref;

      // Capacity check: count new identities this projection would bring.
      // If total would exceed max, throw before touching the registry.
      const newCount = countNewIdentities(conv.items, scope);
      if (totalIdentities + newCount > maxReg) {
        throw new ProjectionCapacityError();
      }

      // Pre-compute question keys first so items can link to them.
      const { questions, keyMap, opQuestionKeys, opOptionKeys } =
        projectQuestions(conv.items, scope);

      // Project items (question items may expand to multiple rows).
      const opItemKeys = new Map<string, string>();
      const items: LiveTranscriptItem[] = [];
      for (const item of conv.items) {
        const rows = projectItem(item, scope, keyMap);
        for (const row of rows) {
          opItemKeys.set(row.key, item.id);
          items.push(row);
        }
      }

      const title = conv.name ?? conv.preview;
      // C1: threadKey is a registry-allocated opaque key, not a hash.
      const threadKey = stableKey(scope, "thread", scope);

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
          itemKeys: opItemKeys,
          questionKeys: opQuestionKeys,
          optionKeys: opOptionKeys,
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
        // Free allocated keys for this scope from both registries.
        for (const k of nestedDeleteScope(keyRegistry, scope)) {
          allocatedKeys.delete(k);
        }
        for (const s of nestedDeleteScope(seqRegistry, scope)) {
          allocatedKeys.delete(s);
        }
        // Recount total identities from keyRegistry (accurate, avoids drift).
        totalIdentities = nestedCount(keyRegistry);
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
