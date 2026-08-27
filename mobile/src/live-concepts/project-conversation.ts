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

import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type {
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
  const truncatedBody = decoder.decode(encoded.subarray(0, targetBytes));
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

// Map a mobile timeline item to a live transcript item. Metadata-only: tool
// arguments, output, error, exit codes, and attachment src URLs are never
// included in the body. Activity items show reasoning/tool delta content
// (the detail.output field) in the body, not just "label — state".
function projectItem(item: MobileTimelineItem): LiveTranscriptItem {
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
      };

    case "question": {
      const firstQuestion = item.batch.questions[0];
      const prompt = firstQuestion?.question ?? "";
      return {
        key: nextKey("question"),
        kind: "question",
        label: firstQuestion?.header ?? "Question",
        body: prompt,
        tone: "attention",
        streaming: false,
        truncated: false,
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
      };
    }
  }
}

// Project ask_user question batches into the live questions array. Question
// keys are opaque private keys, not raw call/idx identifiers.
function projectQuestions(
  items: readonly MobileTimelineItem[],
): LiveQuestionView[] {
  const questions: LiveQuestionView[] = [];
  for (const item of items) {
    if (item.kind !== "question") continue;
    for (const q of item.batch.questions) {
      questions.push({
        key: nextKey("q"),
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
  return questions;
}

export function projectLiveConversation(
  conv: MobileConversation,
  ref: string,
): LiveConversationView {
  // Reset the key counter at the start of each projection pass to ensure
  // deterministic keys within a single call.
  keyCounter = 0;
  const items = conv.items.map(projectItem);
  const questions = projectQuestions(conv.items);
  const title = conv.name ?? conv.preview;

  return {
    // threadKey is an opaque key — use the ref but note it is the only
    // operational identifier exposed, and only for thread-level identity
    // (not item-level).
    threadKey: ref,
    title,
    // project is the sessionId — but in the live view we expose it as a
    // display-safe project label, not an operational identifier.
    project: conv.sessionId,
    status: conv.status,
    items,
    questions,
    // olderAvailable is derived from olderCursor: if we have a cursor, more
    // older items are available.
    olderAvailable: false,
  };
}

// Overload that accepts an optional olderCursor to derive olderAvailable.
export function projectLiveConversationWithCursor(
  conv: MobileConversation,
  ref: string,
  olderCursor: string | null,
): LiveConversationView {
  keyCounter = 0;
  const items = conv.items.map(projectItem);
  const questions = projectQuestions(conv.items);
  const title = conv.name ?? conv.preview;

  return {
    threadKey: ref,
    title,
    project: conv.sessionId,
    status: conv.status,
    items,
    questions,
    olderAvailable: olderCursor !== null,
  };
}
