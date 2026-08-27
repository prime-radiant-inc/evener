// createLiveConversationProjector — pure projection from a MobileConversation
// into a LiveConversationView for live-concept renderers. No DOM, no network,
// no clock. Same input → same output.
//
// The projector INSTANCE owns a private registry. Same source item/question/
// option gets the same opaque non-derivable display key and sequenceLabel
// across prepend, insert, reorder, delta, and re-projection. Different
// IDs/namespaces do not collide. Raw ref/session/item/call/question/option
// IDs appear only in the private operational map returned alongside the
// view — never in the serialized view, display key, sequenceLabel, threadKey,
// project, body, or DOM-bound fields.
//
// `threadKey` is opaque (derived from ref, not the raw ref). `project` comes
// only from the supplied display-safe `projectLabel`, never `conv.sessionId`.
// `updatedLabel` passes the authoritative option or null — never fabricated.
// `sequenceLabel` is an opaque stable presentation label allocated on first
// sight, not an array index, and stays stable when older rows prepend.
// `questionKey` links a transcript item to its LiveQuestionView via an opaque
// key; null when the item is not a question-bearing turn.

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

// Centralized truncation limit for the live view, measured in UTF-8 bytes.
const MAX_LIVE_BYTES = 64 * 1024;
const TRUNCATION_MARKER = "… truncated";

const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

// Truncate a string to maxBytes in UTF-8 + marker, ending with "… truncated"
// exactly once. Recognizes a store-capped marker and does not duplicate it.
// Uses TextEncoder for byte-accurate measurement and ensures the result is
// valid Unicode (no split surrogate pairs).
function truncate(text: string): { body: string; truncated: boolean } {
  // If the store already capped the text and appended the marker, recognize
  // it as truncated:true and return as-is (no duplicate marker).
  if (text.endsWith(TRUNCATION_MARKER)) {
    return { body: text, truncated: true };
  }

  const encoded = textEncoder.encode(text);
  if (encoded.length <= MAX_LIVE_BYTES) {
    return { body: text, truncated: false };
  }
  const targetBytes = MAX_LIVE_BYTES - markerBytes.length;
  // Find the last valid UTF-8 code point boundary at or before targetBytes.
  // Scanning backward from targetBytes, a leading byte (not 10xxxxxx) marks
  // the start of a complete code point if the remaining bytes form a valid
  // sequence. This avoids split surrogate pairs and replacement characters.
  let cut = targetBytes;
  while (cut > 0) {
    // A continuation byte starts with 10xxxxxx (0x80–0xBF). Walk backward
    // until we find a leading byte.
    const byte = encoded[cut];
    if (byte === undefined) {
      cut -= 1;
      continue;
    }
    if ((byte & 0xc0) !== 0x80) {
      // Leading byte found at `cut`. Check whether the full sequence starting
      // here fits within targetBytes. If not, cut before it.
      const seqLen = utf8SeqLen(byte);
      if (seqLen !== undefined && cut + seqLen <= targetBytes) {
        break; // complete sequence fits
      }
      // This code point would be split — cut before it.
      break;
    }
    cut -= 1;
  }
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let truncatedBody: string;
  try {
    truncatedBody = decoder.decode(encoded.subarray(0, cut));
  } catch {
    // Fallback: if fatal decoding fails, trim one byte and retry.
    truncatedBody = decoder.decode(encoded.subarray(0, Math.max(0, cut - 1)));
  }
  // If the re-encoded truncated text + marker exceeds maxBytes (due to
  // replacement chars at the boundary), trim further.
  let truncatedBytes = textEncoder.encode(truncatedBody);
  while (
    truncatedBytes.length + markerBytes.length > MAX_LIVE_BYTES &&
    truncatedBody.length > 0
  ) {
    truncatedBody = truncatedBody.slice(0, -1);
    truncatedBytes = textEncoder.encode(truncatedBody);
  }
  return {
    body: truncatedBody + TRUNCATION_MARKER,
    truncated: true,
  };
}

// Return the number of bytes in a UTF-8 code point given its leading byte,
// or undefined if the byte is a continuation byte or invalid.
function utf8SeqLen(lead: number): number | undefined {
  if ((lead & 0x80) === 0x00) return 1; // 0xxxxxxx
  if ((lead & 0xe0) === 0xc0) return 2; // 110xxxxx
  if ((lead & 0xf0) === 0xe0) return 3; // 1110xxxx
  if ((lead & 0xf8) === 0xf0) return 4; // 11110xxx
  return undefined;
}

// --- tone --------------------------------------------------------------------

// Derive the overall conversation display tone from the conversation status.
function conversationTone(status: string): DisplayTone {
  if (status === "running") return "running";
  if (status === "error" || status === "failed") return "failed";
  if (status === "ready" || status === "idle") return "idle";
  return "unknown";
}

// --- operational map (private) -----------------------------------------------

// The operational map carries raw IDs keyed by their opaque display keys.
// It is returned alongside the view for diagnostic use but is never
// serialized into the DOM.
export interface ConversationOperationalMap {
  // Opaque item key → raw item ID
  readonly itemKeys: Map<string, string>;
  // Opaque question key → raw question key (callId:idx)
  readonly questionKeys: Map<string, string>;
  // Opaque option key → raw option identity (callId:idx:optIdx)
  readonly optionKeys: Map<string, string>;
}

// --- projector options --------------------------------------------------------

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
}

// --- opaque key derivation ---------------------------------------------------

// Each projector instance owns a private registry. Keys are opaque,
// non-derivable, and stable across re-projection. The registry maps a
// namespaced source identity to a stable opaque key and sequence label.
// A per-instance salt ensures different projector instances produce
// different keys for the same source.

function makeOpaqueId(): string {
  // Generate a random 8-char hex salt segment. This makes keys non-derivable
  // from the source identity — an observer cannot recover the raw ID.
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// Simple stable string hash (FNV-1a) to derive an opaque threadKey from the
// raw ref without exposing it. The hash is non-reversible and combined with
// the instance salt.
function opaqueHash(input: string, salt: string): string {
  let h = 0x811c9dc5;
  const s = salt + input;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0");
}

// --- projector factory -------------------------------------------------------

export function createLiveConversationProjector(): LiveConversationProjector {
  // Private registry: maps namespace+sourceId → opaque key.
  // Namespacing prevents collisions between items with the same raw ID but
  // different kinds (e.g. a question item and an activity item that happen
  // to share an ID).
  const keyRegistry = new Map<string, string>();
  const seqRegistry = new Map<string, string>();
  const operationalItemKeys = new Map<string, string>();
  const operationalQuestionKeys = new Map<string, string>();
  const operationalOptionKeys = new Map<string, string>();

  // Per-instance salt for opaque key derivation.
  const salt = makeOpaqueId();
  let keyCounter = 0;
  let seqCounter = 0;

  // Allocate or retrieve a stable opaque key for a source identity.
  function stableKey(namespace: string, sourceId: string): string {
    const regKey = `${namespace}:${sourceId}`;
    let k = keyRegistry.get(regKey);
    if (k === undefined) {
      keyCounter += 1;
      // Opaque key: salt + counter, no raw ID embedded.
      k = `${salt}${keyCounter.toString(36)}`;
      keyRegistry.set(regKey, k);
    }
    return k;
  }

  // Allocate or retrieve a stable opaque sequence label.
  function stableSeq(namespace: string, sourceId: string): string {
    const regKey = `${namespace}:${sourceId}`;
    let s = seqRegistry.get(regKey);
    if (s === undefined) {
      seqCounter += 1;
      // Opaque sequence label: allocated on first sight, not array index.
      s = `${salt.slice(0, 4)}s${seqCounter.toString(36)}`;
      seqRegistry.set(regKey, s);
    }
    return s;
  }

  function projectItem(
    item: MobileTimelineItem,
    questionKeyMap: Map<string, string>,
  ): LiveTranscriptItem {
    const seq = stableSeq("item", item.id);

    switch (item.kind) {
      case "user":
        return {
          key: stableKey("item", item.id),
          kind: "user",
          label: "You",
          body: item.text,
          tone: "idle",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: seq,
        };

      case "assistant": {
        const { body, truncated } = truncate(item.markdown);
        return {
          key: stableKey("item", item.id),
          kind: "assistant",
          label: "Assistant",
          body,
          tone: item.streaming ? "running" : "idle",
          streaming: item.streaming,
          truncated,
          questionKey: null,
          sequenceLabel: seq,
        };
      }

      case "activity": {
        const outputText = item.detail.output ?? "";
        const { body, truncated } =
          outputText.length > 0
            ? truncate(outputText)
            : { body: "", truncated: false };
        return {
          key: stableKey("item", item.id),
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
          sequenceLabel: seq,
        };
      }

      case "notice":
        return {
          key: stableKey("item", item.id),
          kind: "user",
          label: "Notice",
          body: item.text,
          tone: item.tone === "warning" ? "attention" : "idle",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: seq,
        };

      case "question": {
        const firstQuestion = item.batch.questions[0];
        const prompt = firstQuestion?.question ?? "";
        // Link this transcript item to its question key via the opaque key.
        const qKey = questionKeyMap.get(item.id) ?? null;
        return {
          key: stableKey("item", item.id),
          kind: "question",
          label: firstQuestion?.header ?? "Question",
          body: prompt,
          tone: "attention",
          streaming: false,
          truncated: false,
          questionKey: qKey,
          sequenceLabel: seq,
        };
      }

      case "failure":
        return {
          key: stableKey("item", item.id),
          kind: "failure",
          label: item.title,
          body: item.detail,
          tone: "failed",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: seq,
        };

      case "attachments": {
        const names = item.items.map((a) => a.name ?? "attachment").join(", ");
        return {
          key: stableKey("item", item.id),
          kind: "attachment",
          label: "Attachments",
          body: names,
          tone: "idle",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: seq,
        };
      }
    }
  }

  function projectQuestions(items: readonly MobileTimelineItem[]): {
    questions: LiveQuestionView[];
    keyMap: Map<string, string>;
  } {
    const questions: LiveQuestionView[] = [];
    const keyMap = new Map<string, string>(); // item.id -> opaque question key

    for (const item of items) {
      if (item.kind !== "question") continue;
      for (let qi = 0; qi < item.batch.questions.length; qi++) {
        const q = item.batch.questions[qi];
        if (q === undefined) continue;
        const qNamespace = "question";
        const qSource = q.key; // stable re-derivable source identity
        const qKey = stableKey(qNamespace, qSource);
        keyMap.set(item.id, qKey);
        operationalQuestionKeys.set(qKey, qSource);

        questions.push({
          key: qKey,
          header: q.header,
          prompt: q.question,
          options: q.options.map((o, oi) => {
            const optSource = `${q.key}:${oi}`;
            const optKey = stableKey("option", optSource);
            operationalOptionKeys.set(optKey, optSource);
            return {
              key: optKey,
              label: o.label,
              detail: o.detail,
            };
          }),
          multiple: q.multiSelect,
        });
      }
    }
    return { questions, keyMap };
  }

  return {
    project(
      conv: MobileConversation,
      options: ConversationProjectOptions,
    ): { view: LiveConversationView; operational: ConversationOperationalMap } {
      const { ref, olderCursor, projectLabel, updatedLabel } = options;

      // Pre-compute question keys first so items can link to them.
      const { questions, keyMap } = projectQuestions(conv.items);

      // Project items with the question key context.
      const items = conv.items.map((item) => {
        const projected = projectItem(item, keyMap);
        // Record the operational mapping for every item.
        operationalItemKeys.set(projected.key, item.id);
        return projected;
      });

      const title = conv.name ?? conv.preview;
      const threadKey = opaqueHash(ref, salt);

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
          itemKeys: operationalItemKeys,
          questionKeys: operationalQuestionKeys,
          optionKeys: operationalOptionKeys,
        },
      };
    },
  };
}
