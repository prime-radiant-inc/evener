// projectLiveConversation — pure projection from a MobileConversation into a
// LiveConversationView for live-concept renderers. No DOM, no network, no
// clock. Same input → same output.
//
// Maps user, assistant, tool, question, failure, and attachment rows to the
// LiveTranscriptItem union with metadata-only bodies: raw arguments, output,
// error, exit codes, and source URLs never leak into the live view.
//
// View IDs are opaque stable private keys, not raw item/ref/session/question
// IDs. The projection maintains a private operational-key map internally and
// returns display keys that are stable but do not expose operational
// identifiers in the DOM.
//
// `sequenceLabel` is an opaque stable adapter output used only for ordering
// and stable disclosure identity across re-projections. It is not a
// user-facing ID. `questionKey` links a transcript item to its
// LiveQuestionView; null when the item is not a question-bearing turn.
// `updatedLabel` is null when no authoritative timestamp exists; the
// projection never fabricates one. `tone` reflects the overall conversation
// display tone derived from the conversation status.

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

// Centralized truncation limit for the live view, measured in UTF-8 bytes.
const MAX_LIVE_BYTES = 64 * 1024;
const TRUNCATION_MARKER = "… truncated";

const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

// Truncate a string to maxBytes in UTF-8 + marker, ending with "… truncated"
// exactly once. Uses TextEncoder for byte-accurate measurement and ensures
// the result is valid Unicode (no split surrogate pairs).
function truncate(text: string): { body: string; truncated: boolean } {
  const encoded = textEncoder.encode(text);
  if (encoded.length <= MAX_LIVE_BYTES) {
    return { body: text, truncated: false };
  }
  const targetBytes = MAX_LIVE_BYTES - markerBytes.length;
  const decoder = new TextDecoder("utf-8", { fatal: false });
  let truncatedBody = decoder.decode(encoded.subarray(0, targetBytes));
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

// Private key generator: produces opaque stable keys that do not expose raw
// operational IDs in the DOM. The key is derived from a counter + the item
// kind, ensuring stability within a single projection pass and uniqueness
// across items.
let keyCounter = 0;
function nextKey(kind: string): string {
  keyCounter += 1;
  return `k${keyCounter}:${kind}`;
}

// Opaque stable sequence label generator: produces a stable label for a given
// source item that is consistent across re-projections of the same input. It
// uses the item's index in the items array, which is deterministic for a given
// MobileConversation. The label is opaque — not the raw item ID.
function sequenceLabelFor(index: number): string {
  return `s${index}`;
}

// Derive the overall conversation display tone from the conversation status.
function conversationTone(status: string): DisplayTone {
  if (status === "running") return "running";
  if (status === "error" || status === "failed") return "failed";
  if (status === "ready" || status === "idle") return "idle";
  return "unknown";
}

// Context for projecting items, carrying question keys for linking.
interface ProjectionContext {
  questionKeys: Map<string, string>; // item.id -> question key
  itemIndex: number;
}

// Map a mobile timeline item to a live transcript item. Metadata-only: tool
// arguments, output, error, exit codes, and attachment src URLs are never
// included in the body. Activity items show reasoning/tool delta content
// (the detail.output field) in the body, not just "label — state".
function projectItem(
  item: MobileTimelineItem,
  ctx: ProjectionContext,
): LiveTranscriptItem {
  const seq = sequenceLabelFor(ctx.itemIndex);
  ctx.itemIndex += 1;

  switch (item.kind) {
    case "user":
      return {
        key: nextKey("user"),
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
        key: nextKey("assistant"),
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
      // Show the reasoning/tool delta content (detail.output) in the body
      // when available, rather than just "label — state". This makes
      // streaming reasoning-summary and tool-output deltas visible in the
      // live conversation view.
      const outputText = item.detail.output ?? "";
      const { body, truncated } =
        outputText.length > 0
          ? truncate(outputText)
          : { body: "", truncated: false };
      return {
        key: nextKey("tool"),
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
        key: nextKey("notice"),
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
      // Link this transcript item to its question key.
      const qKey = ctx.questionKeys.get(item.id) ?? null;
      return {
        key: nextKey("question"),
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
        key: nextKey("failure"),
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
      // Metadata-only: count + names, never src URLs.
      const names = item.items.map((a) => a.name ?? "attachment").join(", ");
      return {
        key: nextKey("attachment"),
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

// Pre-compute question keys for each question item so that transcript items
// can link to them. Returns a map of item.id -> question key. Also returns
// the LiveQuestionView array.
function projectQuestions(items: readonly MobileTimelineItem[]): {
  questions: LiveQuestionView[];
  keyMap: Map<string, string>;
} {
  const questions: LiveQuestionView[] = [];
  const keyMap = new Map<string, string>();
  for (const item of items) {
    if (item.kind !== "question") continue;
    for (const q of item.batch.questions) {
      const qKey = nextKey("q");
      keyMap.set(item.id, qKey);
      questions.push({
        key: qKey,
        header: q.header,
        prompt: q.question,
        options: q.options.map((o) => ({
          key: nextKey("opt"),
          label: o.label,
          detail: o.detail,
        })),
        multiple: q.multiSelect,
      });
    }
  }
  return { questions, keyMap };
}

// Build the projection context (question key map) and project all items.
function buildProjection(
  conv: MobileConversation,
  ref: string,
  olderCursor: string | null,
): LiveConversationView {
  // Reset the key counter at the start of each projection pass to ensure
  // deterministic keys within a single call.
  keyCounter = 0;

  // Pre-compute question keys first so items can link to them.
  const { questions, keyMap } = projectQuestions(conv.items);

  // Project items with the question key context.
  const ctx: ProjectionContext = { questionKeys: keyMap, itemIndex: 0 };
  const items = conv.items.map((item) => projectItem(item, ctx));

  const title = conv.name ?? conv.preview;

  return {
    threadKey: ref,
    title,
    project: conv.sessionId,
    status: conv.status,
    items,
    questions,
    olderAvailable: olderCursor !== null,
    tone: conversationTone(conv.status),
    // updatedLabel is null when no authoritative timestamp exists. The
    // projection never fabricates a timestamp to fill this field.
    updatedLabel: null,
  };
}

export function projectLiveConversation(
  conv: MobileConversation,
  ref: string,
): LiveConversationView {
  return buildProjection(conv, ref, null);
}

// Overload that accepts an optional olderCursor to derive olderAvailable.
export function projectLiveConversationWithCursor(
  conv: MobileConversation,
  ref: string,
  olderCursor: string | null,
): LiveConversationView {
  return buildProjection(conv, ref, olderCursor);
}
