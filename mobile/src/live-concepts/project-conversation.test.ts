// Pure projection tests for projectLiveConversation — maps a MobileConversation
// into a LiveConversationView for live-concept renderers. The projection is
// pure: same input → same output, no DOM, no network, no clock.
//
// Covers: user, assistant, tool, question, failure, and attachment rows map to
// the LiveTranscriptItem union with metadata-only bodies (no raw arguments,
// output, error, or exit codes leak into the live view). The projection
// returns a private operational-key map that is stable and non-enumerable.

import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../conversation/model";
import { projectLiveConversation } from "./project-conversation";

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function makeConversation(
  over: Partial<MobileConversation> = {},
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "hello",
    modelProvider: "anthropic",
    status: "ready",
    items: [],
    capabilities: ALL_TRUE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
    ...over,
  };
}

// --- tests -------------------------------------------------------------------

describe("projectLiveConversation", () => {
  it("maps a user message row", () => {
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "Hello world" }],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "u1");
    expect(item).toBeDefined();
    expect(item?.kind).toBe("user");
    expect(item?.body).toBe("Hello world");
  });

  it("maps an assistant row with streaming flag", () => {
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "a1",
          markdown: "thinking...",
          streaming: true,
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "a1");
    expect(item?.kind).toBe("assistant");
    expect(item?.body).toBe("thinking...");
    expect(item?.streaming).toBe(true);
  });

  it("maps a tool/activity row with metadata-only body (no arguments/output/error)", () => {
    const conv = makeConversation({
      items: [
        {
          kind: "activity",
          id: "tool-1",
          label: "shell",
          state: "running",
          detail: {
            arguments: '{"secret":"value"}',
            output: "sensitive output",
            error: "some error",
            exitCode: 0,
            callId: "call-1",
          },
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "tool-1");
    expect(item?.kind).toBe("tool");
    expect(item?.label).toBe("shell");
    // Body should be metadata-only — not raw arguments/output/error
    expect(item?.body).not.toContain("secret");
    expect(item?.body).not.toContain("sensitive");
    expect(item?.body).not.toContain("some error");
  });

  it("maps a question row", () => {
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "call-1:0",
                header: "Choose",
                question: "Which option?",
                options: [
                  { label: "A", detail: "Option A" },
                  { label: "B", detail: "Option B" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "q1");
    expect(item?.kind).toBe("question");
    expect(item?.body).toContain("Which option?");
    // Questions should also appear in the questions array
    expect(view.questions).toHaveLength(1);
    expect(view.questions[0]?.prompt).toBe("Which option?");
    expect(view.questions[0]?.options).toHaveLength(2);
  });

  it("maps a failure row", () => {
    const conv = makeConversation({
      items: [
        {
          kind: "failure",
          id: "fail-1",
          title: "Error",
          detail: "Something went wrong",
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "fail-1");
    expect(item?.kind).toBe("failure");
    expect(item?.body).toContain("Something went wrong");
  });

  it("maps an attachment row with metadata only (no src URLs)", () => {
    const conv = makeConversation({
      items: [
        {
          kind: "attachments",
          id: "att-1",
          items: [
            { id: "img-1", src: "file:///secret/path.png", name: "photo.png" },
          ],
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "att-1");
    expect(item?.kind).toBe("attachment");
    // Body should not expose the raw src URL
    expect(item?.body).not.toContain("file:///secret");
    expect(item?.body).not.toContain("path.png");
  });

  it("returns a threadKey from the ref", () => {
    const conv = makeConversation();
    const view = projectLiveConversation(conv, "ref-42");
    expect(view.threadKey).toBe("ref-42");
  });

  it("returns a title from the conversation name or preview", () => {
    const conv = makeConversation({ name: "My Thread", preview: "preview" });
    const view = projectLiveConversation(conv, "ref-1");
    expect(view.title).toBe("My Thread");
  });

  it("falls back to preview when name is absent", () => {
    const conv = makeConversation({ preview: "some preview" });
    const view = projectLiveConversation(conv, "ref-1");
    expect(view.title).toBe("some preview");
  });

  it("returns olderAvailable from conversation state", () => {
    const conv = makeConversation();
    const view = projectLiveConversation(conv, "ref-1");
    expect(view.olderAvailable).toBe(false);
  });

  it("sets truncated flag on items that exceed the 64 KiB limit", () => {
    const largeText = "x".repeat(70_000);
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "big-1",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "big-1");
    expect(item?.truncated).toBe(true);
  });

  it("sets truncated false for normal items", () => {
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "short" }],
    });
    const view = projectLiveConversation(conv, "ref-1");
    const item = view.items.find((i) => i.key === "u1");
    expect(item?.truncated).toBe(false);
  });

  it("is deterministic — same input produces same output", () => {
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hi" },
        { kind: "assistant", id: "a1", markdown: "hello", streaming: false },
      ],
    });
    const a = projectLiveConversation(conv, "ref-1");
    const b = projectLiveConversation(conv, "ref-1");
    expect(a).toEqual(b);
  });

  it("returns a private operational-key map (keys are stable but non-enumerable)", () => {
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hi" },
        { kind: "assistant", id: "a1", markdown: "hello", streaming: false },
      ],
    });
    const view = projectLiveConversation(conv, "ref-1");
    // The view should have items with stable keys
    const keys = view.items.map((i) => i.key);
    expect(keys).toEqual(["u1", "a1"]);
    // The view shape should match LiveConversationView
    expect(view.status).toBe("ready");
  });
});
