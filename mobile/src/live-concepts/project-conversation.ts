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
// The projector exposes `reset(scope)` to clear entries for a specific
// conversation ref and `dispose()` to clear everything. The registry is
// bounded with safe overflow eviction. A deterministic key allocator can be
// injected for tests.

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

// --- key allocator ------------------------------------------------------------

export type OpaqueKeyAllocator = () => string;

function defaultAllocator(): OpaqueKeyAllocator {
  let n = 0;
  return () => {
    n += 1;
    return `k${n}`;
  };
}

// --- projector factory -------------------------------------------------------

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveConversationProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveConversationProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxReg = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;

  // Private scoped registries: maps "scope:namespace:sourceId" → opaque key/seq.
  const keyRegistry = new Map<string, string>();
  const seqRegistry = new Map<string, string>();

  function evictIfNeeded(): void {
    while (keyRegistry.size >= maxReg) {
      const first = keyRegistry.keys().next();
      if (first.done) break;
      keyRegistry.delete(first.value);
    }
    while (seqRegistry.size >= maxReg) {
      const first = seqRegistry.keys().next();
      if (first.done) break;
      seqRegistry.delete(first.value);
    }
  }

  function stableKey(
    scope: string,
    namespace: string,
    sourceId: string,
  ): string {
    const regKey = `${scope}:${namespace}:${sourceId}`;
    let k = keyRegistry.get(regKey);
    if (k === undefined) {
      evictIfNeeded();
      k = alloc();
      keyRegistry.set(regKey, k);
    }
    return k;
  }

  function stableSeq(
    scope: string,
    namespace: string,
    sourceId: string,
  ): string {
    const regKey = `${scope}:${namespace}:${sourceId}`;
    let s = seqRegistry.get(regKey);
    if (s === undefined) {
      evictIfNeeded();
      s = alloc();
      seqRegistry.set(regKey, s);
    }
    return s;
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
      for (const q of item.batch.questions) {
        if (q === undefined) continue;
        const qKey = stableKey(scope, "question", q.key);
        keyMap.set(q.key, qKey);
        opQuestionKeys.set(qKey, q.key);

        // C3: Option identity uses stable content identity (label+detail),
        // not array index. Detect indistinguishable duplicates and safe-error.
        const seenOptions = new Set<string>();
        const options = q.options.map((o) => {
          const optIdentity = `${o.label}\u0000${o.detail}`;
          if (seenOptions.has(optIdentity)) {
            throw new Error(
              `Indistinguishable duplicate option (label="${o.label}", detail="${o.detail}") in question "${q.key}" — cannot assign distinct stable keys`,
            );
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
      } else {
        const prefix = `${scope}:`;
        for (const k of keyRegistry.keys()) {
          if (k.startsWith(prefix)) keyRegistry.delete(k);
        }
        for (const k of seqRegistry.keys()) {
          if (k.startsWith(prefix)) seqRegistry.delete(k);
        }
      }
    },

    dispose(): void {
      keyRegistry.clear();
      seqRegistry.clear();
    },
  };
}
