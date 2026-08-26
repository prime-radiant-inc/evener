// projectLiveConversation — pure projection from a MobileConversation into a
// LiveConversationView for live-concept renderers. No DOM, no network, no
// clock. Same input → same output.
//
// Maps user, assistant, tool, question, failure, and attachment rows to the
// LiveTranscriptItem union with metadata-only bodies: raw arguments, output,
// error, exit codes, and source URLs never leak into the live view. The
// projection returns a private operational-key map (stable item keys derived
// from the mobile item id).

import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type {
  LiveConversationView,
  LiveQuestionView,
  LiveTranscriptItem,
} from "./model";

// Centralized truncation limit for the live view.
const MAX_LIVE_TEXT = 64 * 1024;
const TRUNCATION_MARKER = "… truncated";

function truncate(text: string): { body: string; truncated: boolean } {
  if (text.length <= MAX_LIVE_TEXT) return { body: text, truncated: false };
  return {
    body:
      text.slice(0, MAX_LIVE_TEXT - TRUNCATION_MARKER.length) +
      TRUNCATION_MARKER,
    truncated: true,
  };
}

// Map a mobile timeline item to a live transcript item. Metadata-only: tool
// arguments, output, error, exit codes, and attachment src URLs are never
// included in the body.
function projectItem(item: MobileTimelineItem): LiveTranscriptItem {
  const key = item.id;

  switch (item.kind) {
    case "user":
      return {
        key,
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
        key,
        kind: "assistant",
        label: "Assistant",
        body,
        tone: item.streaming ? "running" : "idle",
        streaming: item.streaming,
        truncated,
      };
    }

    case "activity": {
      // Metadata-only body: use label + state, never raw arguments/output/error.
      const stateLabel =
        item.state === "running"
          ? "Running"
          : item.state === "failed"
            ? "Failed"
            : "Completed";
      const body = `${item.label} — ${stateLabel}`;
      return {
        key,
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
        truncated: false,
      };
    }

    case "notice":
      return {
        key,
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
        key,
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
        key,
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
        key,
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

// Project ask_user question batches into the live questions array.
function projectQuestions(
  items: readonly MobileTimelineItem[],
): LiveQuestionView[] {
  const questions: LiveQuestionView[] = [];
  for (const item of items) {
    if (item.kind !== "question") continue;
    for (const q of item.batch.questions) {
      questions.push({
        key: q.key,
        header: q.header,
        prompt: q.question,
        options: q.options.map((o) => ({
          key: o.label,
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
    olderAvailable: false,
  };
}
