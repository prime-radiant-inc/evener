import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import { toolRendererFor } from "../toolRenderers";
import "./readTranscript";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function call(args: Record<string, unknown>, output?: unknown): ItemModel {
  return item({
    toolName: "read_transcript",
    argumentsJSON: JSON.stringify(args),
    output: output === undefined ? undefined : JSON.stringify(output, null, 2),
  });
}

// Envelope shapes verified against agent/session_tools_transcript.go:
// readMarkdownEnvelope (markdown/job), readOutlineEnvelope (outline, flat - no
// meta block), readRawEnvelope (jsonl).
const MARKDOWN_ENVELOPE = {
  transcript_ref: "local:01KABC",
  format: "markdown",
  content_type: "text/markdown",
  content: "# Session\n\n## Turn 1\nhello\n",
  meta: { turns_total: 120, range: "last:40", turns_rendered: 40, truncated: true, elided_turns: 3 },
};

const OUTLINE_ENVELOPE = {
  transcript_ref: "local:01KABC",
  format: "outline",
  turns_total: 120,
  content: "1. user: hi\n2. assistant: hello\n",
  truncated: false,
  elided_turns: 0,
  hint: "turn numbers here are what range and expand_turn accept",
};

const JOB_ENVELOPE = {
  transcript_ref: "job:01KJOB",
  format: "markdown",
  content_type: "text/markdown",
  content: "# Shell Job 01KJOB\n\n- status: completed\n",
  meta: { turns_total: 1, range: "shell-log", turns_rendered: 1, truncated: false, elided_turns: 0 },
};

// --- registration -------------------------------------------------------

test("read_transcript resolves to its own descriptor, not the raw-output default", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({ transcript_ref: "local:01KABC" }, MARKDOWN_ENVELOPE))).toContain("transcript");
});

test("read_session_transcript shares the descriptor - same envelopes, same arguments", () => {
  expect(toolRendererFor("read_session_transcript")).toBe(toolRendererFor("read_transcript"));
});

test("find_session_transcripts does NOT share it - a search result list is a different shape", () => {
  expect(toolRendererFor("find_session_transcripts")).not.toBe(toolRendererFor("read_transcript"));
});

// --- summary: says what was read, and how much --------------------------

test("summary: a session markdown read names the ref and the rendered turn span", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({ transcript_ref: "local:01KABC" }, MARKDOWN_ENVELOPE))).toBe(
    "Read transcript 01KABC · 40 of 120 turns",
  );
});

test("summary: an outline read reads off the flat envelope's own turns_total", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({ transcript_ref: "local:01KABC", format: "outline" }, OUTLINE_ENVELOPE))).toBe(
    "Read transcript 01KABC · outline of 120 turns",
  );
});

test("summary: a job log read says it read a job's output, not a conversation", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({ transcript_ref: "job:01KJOB" }, JOB_ENVELOPE))).toBe("Read job log 01KJOB");
});

test("summary: an expand_turn read names the turn it expanded", () => {
  const d = toolRendererFor("read_transcript");
  const envelope = {
    ...MARKDOWN_ENVELOPE,
    expansion: {
      expand_turn: 17,
      offset_bytes: 0,
      bytes_returned: 4096,
      total_bytes: 9000,
      representation: "transcript_v2_jsonl",
      encoding: "utf-8",
      data: "{}",
    },
  };
  expect(d.summary(call({ transcript_ref: "local:01KABC", expand_turn: 17 }, envelope))).toBe(
    "Read transcript 01KABC · turn 17 in full",
  );
});

test("summary: the current session (no ref given) says so rather than showing a blank", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({}, { ...MARKDOWN_ENVELOPE, transcript_ref: "" }))).toBe(
    "Read this session's transcript · 40 of 120 turns",
  );
});

test("summary: a live call with no output yet still names its target", () => {
  const d = toolRendererFor("read_transcript");
  expect(d.summary(call({ transcript_ref: "local:01KABC" }))).toBe("Read transcript 01KABC");
});

test("summary: non-JSON output degrades to the bare target rather than inventing a count", () => {
  const d = toolRendererFor("read_transcript");
  const bad = item({
    toolName: "read_transcript",
    argumentsJSON: JSON.stringify({ transcript_ref: "local:01KABC" }),
    output: "not json at all",
  });
  expect(d.summary(bad)).toBe("Read transcript 01KABC");
});

test("summary: an api_log read (read_session_transcript's own source) says which evidence it read", () => {
  const d = toolRendererFor("read_session_transcript");
  const apiLog = item({
    toolName: "read_session_transcript",
    argumentsJSON: JSON.stringify({ transcript_ref: "local:01KABC", source: "api_log" }),
    output: "{}",
  });
  expect(d.summary(apiLog)).toBe("Read API log 01KABC");
});

// --- body: the transcript content, not the envelope ---------------------

test("body renders the envelope's content, not the JSON wrapper", () => {
  const d = toolRendererFor("read_transcript");
  const Body = d.body!;
  const { container } = render(
    <Body item={call({ transcript_ref: "local:01KABC" }, MARKDOWN_ENVELOPE)} live={false} />,
  );
  expect(container.textContent).toContain("## Turn 1");
  expect(container.textContent).not.toContain("content_type");
});

test("body reports an honest truncation from the envelope's own meta", () => {
  const d = toolRendererFor("read_transcript");
  const Body = d.body!;
  render(<Body item={call({ transcript_ref: "local:01KABC" }, MARKDOWN_ENVELOPE)} live={false} />);
  expect(screen.getByTestId("read-transcript-elision").textContent).toContain("3");
});

test("body reports no elision when the read was complete", () => {
  const d = toolRendererFor("read_transcript");
  const Body = d.body!;
  render(<Body item={call({ transcript_ref: "x", format: "outline" }, OUTLINE_ENVELOPE)} live={false} />);
  expect(screen.queryByTestId("read-transcript-elision")).toBe(null);
});

test("body falls back to the raw output text when the envelope isn't parseable JSON", () => {
  const d = toolRendererFor("read_transcript");
  const Body = d.body!;
  const bad = item({ toolName: "read_transcript", output: "plain text fallback" });
  render(<Body item={bad} live={false} />);
  expect(screen.getByText("plain text fallback")).toBeTruthy();
});

test("body renders nothing when there is no output at all", () => {
  const d = toolRendererFor("read_transcript");
  const Body = d.body!;
  const { container } = render(<Body item={item({ toolName: "read_transcript" })} live={false} />);
  expect(container.textContent).toBe("");
});
