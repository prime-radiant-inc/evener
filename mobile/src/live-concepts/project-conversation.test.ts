// Stable private live projector tests for createLiveConversationProjector —
// maps a MobileConversation into a LiveConversationView for live-concept
// renderers. The projector instance owns a private registry: same source
// item/question/option gets the same opaque non-derivable display key and
// sequenceLabel across prepend, insert, reorder, delta, and re-projection.
// Raw ref/session/item/call/question/option IDs appear only in the private
// operational map, never in the serialized view, display key, sequenceLabel,
// threadKey, project, body, or DOM-bound fields.

import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../conversation/model";
import type { LiveConversationView, LiveTranscriptItem } from "./model";
import { createLiveConversationProjector } from "./project-conversation";

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
    sessionId: "session-secret-1",
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

const OPTS = {
  ref: "ref-1",
  olderCursor: null,
  projectLabel: "My Project",
  updatedLabel: null,
} as const;

// --- tests -------------------------------------------------------------------

describe("createLiveConversationProjector", () => {
  it("maps a user message row", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "Hello world" }],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "user");
    expect(item).toBeDefined();
    expect(item?.body).toBe("Hello world");
  });

  it("maps an assistant row with streaming flag", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.body).toBe("thinking...");
    expect(item?.streaming).toBe(true);
  });

  it("maps a tool/activity row with metadata-only body (no arguments/error)", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "tool");
    expect(item?.label).toBe("shell");
    expect(item?.body).not.toContain("secret");
    expect(item?.body).not.toContain("some error");
    expect(item?.body).toContain("sensitive output");
  });

  it("maps a question row", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "question");
    expect(item?.body).toContain("Which option?");
    expect(view.questions).toHaveLength(1);
    expect(view.questions[0]?.prompt).toBe("Which option?");
    expect(view.questions[0]?.options).toHaveLength(2);
  });

  it("maps a failure row", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "failure");
    expect(item?.body).toContain("Something went wrong");
  });

  it("maps an attachment row with metadata only (no src URLs)", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "attachment");
    expect(item?.body).not.toContain("file:///secret");
    expect(item?.body).not.toContain("path.png");
  });

  // --- threadKey / project / updatedLabel ------------------------------------

  it("threadKey is opaque — not the raw ref", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const { view } = p.project(conv, {
      ref: "raw-ref-secret-123",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(view.threadKey).not.toBe("raw-ref-secret-123");
    expect(typeof view.threadKey).toBe("string");
    expect(view.threadKey.length).toBeGreaterThan(0);
  });

  it("threadKey is stable across re-projection of the same ref", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const a = p.project(conv, {
      ref: "ref-X",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    const b = p.project(conv, {
      ref: "ref-X",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(a.view.threadKey).toBe(b.view.threadKey);
  });

  it("project comes from projectLabel, never conv.sessionId", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ sessionId: "session-secret-1" });
    const { view } = p.project(conv, {
      ref: "ref-1",
      olderCursor: null,
      projectLabel: "Display Project Name",
      updatedLabel: null,
    });
    expect(view.project).toBe("Display Project Name");
    expect(view.project).not.toBe("session-secret-1");
  });

  it("updatedLabel passes authoritative option or null", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const { view: withLabel } = p.project(conv, {
      ref: "ref-1",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: "2 minutes ago",
    });
    expect(withLabel.updatedLabel).toBe("2 minutes ago");

    const { view: nullLabel } = p.project(conv, {
      ref: "ref-1",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(nullLabel.updatedLabel).toBeNull();
  });

  it("updatedLabel is never fabricated", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const { view } = p.project(conv, {
      ref: "ref-1",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(view.updatedLabel).toBeNull();
  });

  // --- olderAvailable --------------------------------------------------------

  it("olderAvailable is exactly olderCursor !== null", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const { view: withCursor } = p.project(conv, {
      ref: "ref-1",
      olderCursor: "page-1",
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(withCursor.olderAvailable).toBe(true);

    const { view: nullCursor } = p.project(conv, {
      ref: "ref-1",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(nullCursor.olderAvailable).toBe(false);
  });

  it("does not expose alternate APIs that hardcode false", () => {
    // The projector instance has only project(); no hardcode-false convenience.
    const p = createLiveConversationProjector();
    expect(typeof p.project).toBe("function");
    expect(Object.keys(p)).toEqual(["project"]);
  });

  // --- tone mapping ----------------------------------------------------------

  it("produces running tone for running status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "running" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("running");
  });

  it("produces failed tone for error status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "error" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("failed");
  });

  it("produces failed tone for failed status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "failed" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("failed");
  });

  it("produces idle tone for ready status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "ready" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("idle");
  });

  it("produces idle tone for idle status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "idle" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("idle");
  });

  it("produces unknown tone for unrecognized status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "weird-status" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("unknown");
  });

  // --- opaque keys -----------------------------------------------------------

  it("returns opaque private keys that do not expose raw operational IDs", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "sensitive-user-id-123", text: "hi" },
        {
          kind: "assistant",
          id: "secret-agent-id-456",
          markdown: "hello",
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const keys = view.items.map((i) => i.key);
    expect(keys).not.toContain("sensitive-user-id-123");
    expect(keys).not.toContain("secret-agent-id-456");
    for (const key of keys) {
      expect(typeof key).toBe("string");
      expect(key.length).toBeGreaterThan(0);
    }
  });

  it("question keys are opaque — do not expose raw call IDs", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-secret-789",
            questions: [
              {
                key: "call-secret-789:0",
                header: "Choose",
                question: "Which?",
                options: [{ label: "A", detail: "Option A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    for (const q of view.questions) {
      expect(q.key).not.toContain("call-secret-789");
      for (const opt of q.options) {
        expect(opt.key).not.toContain("call-secret-789");
      }
    }
  });

  it("questionKey links transcript item to the corresponding LiveQuestionView", () => {
    const p = createLiveConversationProjector();
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
                question: "Which?",
                options: [{ label: "A", detail: "Option A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "question");
    expect(item?.questionKey).not.toBeNull();
    expect(view.questions.some((q) => q.key === item?.questionKey)).toBe(true);
  });

  it("questionKey is null on non-question items", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.items[0]?.questionKey).toBeNull();
  });

  // --- sequence labels: stable across prepend/reorder/reprojection ----------

  it("produces opaque stable sequenceLabel on every item", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "first" },
        { kind: "assistant", id: "a1", markdown: "second", streaming: false },
        { kind: "user", id: "u2", text: "third" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    for (const item of view.items) {
      expect(item.sequenceLabel).toBeDefined();
      expect(typeof item.sequenceLabel).toBe("string");
      expect(item.sequenceLabel.length).toBeGreaterThan(0);
    }
    for (const item of view.items) {
      expect(item.sequenceLabel).not.toContain("u1");
      expect(item.sequenceLabel).not.toContain("a1");
      expect(item.sequenceLabel).not.toContain("u2");
    }
  });

  it("sequence labels are stable across re-projection of identical input", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "first" },
        { kind: "assistant", id: "a1", markdown: "second", streaming: false },
      ],
    });
    const a = p.project(conv, { ...OPTS });
    const b = p.project(conv, { ...OPTS });
    expect(a.view.items.map((i) => i.sequenceLabel)).toEqual(
      b.view.items.map((i) => i.sequenceLabel),
    );
  });

  it("sequence labels stay stable when older rows prepend", () => {
    // First projection: two items
    const p = createLiveConversationProjector();
    const initial = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "recent" },
        { kind: "assistant", id: "a1", markdown: "reply", streaming: false },
      ],
    });
    const a = p.project(initial, { ...OPTS });
    const recentLabels = a.view.items.map((i) => i.sequenceLabel);

    // Second projection: older items prepended before the same two items
    const withOlder = makeConversation({
      items: [
        { kind: "user", id: "old-1", text: "older message" },
        {
          kind: "assistant",
          id: "old-2",
          markdown: "older reply",
          streaming: false,
        },
        { kind: "user", id: "u1", text: "recent" },
        { kind: "assistant", id: "a1", markdown: "reply", streaming: false },
      ],
    });
    const b = p.project(withOlder, {
      ref: "ref-1",
      olderCursor: "page-1",
      projectLabel: "Project",
      updatedLabel: null,
    });
    // The two recent items should have the SAME sequence labels as before,
    // even though they are now at positions 2 and 3 instead of 0 and 1.
    const bLabels = b.view.items.map((i) => i.sequenceLabel);
    expect(bLabels[2]).toBe(recentLabels[0]);
    expect(bLabels[3]).toBe(recentLabels[1]);
    // The prepended older items should have their own new labels
    expect(bLabels[0]).not.toBe(recentLabels[0]);
    expect(bLabels[1]).not.toBe(recentLabels[1]);
  });

  it("item keys are stable across prepend of older items", () => {
    const p = createLiveConversationProjector();
    const initial = makeConversation({
      items: [{ kind: "user", id: "u1", text: "recent" }],
    });
    const a = p.project(initial, { ...OPTS });
    const recentKey = a.view.items[0]?.key;

    const withOlder = makeConversation({
      items: [
        { kind: "user", id: "old-1", text: "older" },
        { kind: "user", id: "u1", text: "recent" },
      ],
    });
    const b = p.project(withOlder, {
      ref: "ref-1",
      olderCursor: "page-1",
      projectLabel: "Project",
      updatedLabel: null,
    });
    // The recent item should have the same key despite being at index 1 now
    expect(b.view.items[1]?.key).toBe(recentKey);
  });

  it("question keys are stable across re-projection and prepend", () => {
    const p = createLiveConversationProjector();
    const initial = makeConversation({
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
                question: "Which?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const a = p.project(initial, { ...OPTS });
    const qKey = a.view.questions[0]?.key;
    const optKey = a.view.questions[0]?.options[0]?.key;

    const withOlder = makeConversation({
      items: [
        { kind: "user", id: "old-1", text: "older" },
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "call-1:0",
                header: "Choose",
                question: "Which?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const b = p.project(withOlder, {
      ref: "ref-1",
      olderCursor: "page-1",
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(b.view.questions[0]?.key).toBe(qKey);
    expect(b.view.questions[0]?.options[0]?.key).toBe(optKey);
  });

  // --- serialization safety: raw IDs never leak into the view ---------------

  it("serialized view JSON contains no raw operational strings", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      id: "thread-secret-1",
      sessionId: "session-secret-1",
      items: [
        { kind: "user", id: "user-secret-id", text: "hi" },
        {
          kind: "assistant",
          id: "agent-secret-id",
          markdown: "hello",
          streaming: false,
        },
        {
          kind: "activity",
          id: "tool-secret-id",
          label: "shell",
          state: "running",
          detail: {
            arguments: '{"secret":"value"}',
            output: "output text",
            error: "error text",
            exitCode: 0,
            callId: "call-secret-id",
          },
        },
        {
          kind: "question",
          id: "question-secret-id",
          batch: {
            callId: "call-secret-id",
            questions: [
              {
                key: "call-secret-id:0",
                header: "Choose",
                question: "Which?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
        {
          kind: "failure",
          id: "fail-secret-id",
          title: "Error",
          detail: "Something went wrong",
        },
        {
          kind: "attachments",
          id: "att-secret-id",
          items: [
            {
              id: "img-secret-id",
              src: "file:///secret/path.png",
              name: "x.png",
            },
          ],
        },
      ],
    });
    const { view } = p.project(conv, {
      ref: "ref-secret-1",
      olderCursor: null,
      projectLabel: "Display Project",
      updatedLabel: null,
    });
    const json = JSON.stringify(view);
    // None of the raw operational identifiers should appear in the serialized view
    expect(json).not.toContain("session-secret-1");
    expect(json).not.toContain("user-secret-id");
    expect(json).not.toContain("agent-secret-id");
    expect(json).not.toContain("tool-secret-id");
    expect(json).not.toContain("call-secret-id");
    expect(json).not.toContain("question-secret-id");
    expect(json).not.toContain("fail-secret-id");
    expect(json).not.toContain("att-secret-id");
    expect(json).not.toContain("img-secret-id");
    expect(json).not.toContain("ref-secret-1");
    expect(json).not.toContain("file:///secret");
    expect(json).not.toContain('"secret":"value"');
  });

  // --- truncation: marker-aware, multibyte, valid Unicode --------------------

  it("sets truncated flag on items that exceed the 64 KiB UTF-8 limit", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
  });

  it("sets truncated false for normal items", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "short" }],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "user");
    expect(item?.truncated).toBe(false);
  });

  it("truncation marker appears exactly once and body is valid Unicode", () => {
    const p = createLiveConversationProjector();
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
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    const marker = "… truncated";
    expect(item).toBeDefined();
    const count = item!.body.split(marker).length - 1;
    expect(count).toBe(1);
    // Body should end with the marker
    expect(item!.body.endsWith(marker)).toBe(true);
  });

  it("recognizes store-capped marker text as truncated:true and does not duplicate marker", () => {
    // If the store already capped the text and appended the marker, the
    // projector recognizes it as truncated:true and does NOT append again.
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    const cappedText = "x".repeat(64 * 1024 - marker.length) + marker;
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "capped-1",
          markdown: cappedText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    // Marker appears exactly once — not duplicated
    expect(item).toBeDefined();
    const count = item!.body.split(marker).length - 1;
    expect(count).toBe(1);
  });

  it("multibyte truncation produces valid Unicode (no split surrogate pairs)", () => {
    const p = createLiveConversationProjector();
    // Build text with multibyte characters that would split mid-codepoint
    // at a naive byte boundary. Each emoji is 4 UTF-8 bytes.
    const emoji = "🎉"; // U+1F389, 4 bytes in UTF-8
    const largeText = emoji.repeat(20_000); // 80,000 bytes > 64 KiB
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "emoji-1",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    // The body should be valid Unicode — no replacement characters from
    // split multibyte sequences at the boundary.
    expect(item?.body).not.toContain("\uFFFD");
    expect(item?.body.endsWith("… truncated")).toBe(true);
  });

  it("projected tool/reasoning body visibly carries bounded output", () => {
    const p = createLiveConversationProjector();
    const largeOutput = "y".repeat(70_000);
    const conv = makeConversation({
      items: [
        {
          kind: "activity",
          id: "tool-1",
          label: "shell",
          state: "completed",
          detail: { output: largeOutput },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "tool");
    expect(item?.truncated).toBe(true);
    expect(item?.body.length).toBeGreaterThan(0);
    expect(item?.body.endsWith("… truncated")).toBe(true);
  });

  // --- operational map -------------------------------------------------------

  it("returns an operational map alongside the view", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const { view, operational } = p.project(conv, { ...OPTS });
    expect(view).toBeDefined();
    expect(operational).toBeDefined();
    // The operational map should map the opaque key back to the raw item ID
    const opaqueKey = view.items[0]?.key;
    expect(opaqueKey).toBeDefined();
    expect(operational.itemKeys.get(opaqueKey!)).toBe("u1");
  });

  it("operational map contains raw question and option IDs", () => {
    const p = createLiveConversationProjector();
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
                question: "Which?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view, operational } = p.project(conv, { ...OPTS });
    const qKey = view.questions[0]?.key;
    expect(qKey).toBeDefined();
    expect(operational.questionKeys.get(qKey!)).toBe("call-1:0");
    const optKey = view.questions[0]?.options[0]?.key;
    expect(optKey).toBeDefined();
    expect(operational.optionKeys.get(optKey!)).toBe("call-1:0:0");
  });

  // --- re-projection / patch stability ---------------------------------------

  it("re-projecting identical input produces identical keys and sequence labels", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hi" },
        { kind: "assistant", id: "a1", markdown: "hello", streaming: false },
      ],
    });
    const a = p.project(conv, { ...OPTS });
    const b = p.project(conv, { ...OPTS });
    expect(a.view).toEqual(b.view);
  });

  it("re-projecting patched input keeps stable keys for unchanged items", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hello" },
        { kind: "assistant", id: "a1", markdown: "reply", streaming: true },
      ],
    });
    const a = p.project(conv, { ...OPTS });
    const userKey = a.view.items[0]?.key;
    const assistantKey = a.view.items[1]?.key;

    // Patch: assistant streaming changes to false (delta update)
    const patched = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hello" },
        { kind: "assistant", id: "a1", markdown: "reply", streaming: false },
      ],
    });
    const b = p.project(patched, { ...OPTS });
    expect(b.view.items[0]?.key).toBe(userKey);
    expect(b.view.items[1]?.key).toBe(assistantKey);
  });

  it("inserting a question before existing items preserves their keys", () => {
    const p = createLiveConversationProjector();
    const initial = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "first" },
        { kind: "assistant", id: "a1", markdown: "second", streaming: false },
      ],
    });
    const a = p.project(initial, { ...OPTS });
    const userKey = a.view.items[0]?.key;
    const assistantKey = a.view.items[1]?.key;

    const withQuestion = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-new",
          batch: {
            callId: "call-new",
            questions: [
              {
                key: "call-new:0",
                header: "Choose",
                question: "Which?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
        { kind: "user", id: "u1", text: "first" },
        { kind: "assistant", id: "a1", markdown: "second", streaming: false },
      ],
    });
    const b = p.project(withQuestion, { ...OPTS });
    // The user and assistant items should have the same keys despite being
    // shifted by one position due to the inserted question.
    expect(b.view.items[1]?.key).toBe(userKey);
    expect(b.view.items[2]?.key).toBe(assistantKey);
  });

  // --- different IDs don't collide ------------------------------------------

  it("different item IDs produce different opaque keys (no collision)", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "first" },
        { kind: "user", id: "u2", text: "second" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const keys = view.items.map((i) => i.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("same item ID across two projector instances gets different keys", () => {
    const p1 = createLiveConversationProjector();
    const p2 = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p1.project(conv, { ...OPTS });
    const b = p2.project(conv, { ...OPTS });
    expect(a.view.items[0]?.key).not.toBe(b.view.items[0]?.key);
  });

  // --- title -----------------------------------------------------------------

  it("returns a title from the conversation name or preview", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ name: "My Thread", preview: "preview" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.title).toBe("My Thread");
  });

  it("falls back to preview when name is absent", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ preview: "some preview" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.title).toBe("some preview");
  });

  // --- determinism within a single instance ----------------------------------

  it("is deterministic — same input on same instance produces same output", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "hi" },
        { kind: "assistant", id: "a1", markdown: "hello", streaming: false },
      ],
    });
    const a = p.project(conv, { ...OPTS });
    const b = p.project(conv, { ...OPTS });
    expect(a.view).toEqual(b.view);
  });
});
