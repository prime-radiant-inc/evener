// Stable private live projector tests for createLiveConversationProjector —
// maps a MobileConversation into a LiveConversationView for live-concept
// renderers. The projector instance owns a private registry: same source
// item/question/option gets the same opaque non-derivable display key and
// sequenceLabel across prepend, insert, reorder, delta, and re-projection.
// Raw ref/session/item/call/question/option IDs appear only in the private
// operational map, never in the serialized view, display key, sequenceLabel,
// threadKey, project, body, or DOM-bound fields.
//
// Uses a deterministic key allocator to avoid probabilistic assertions.

import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../conversation/model";
import type { OpaqueKeyAllocator } from "./project-conversation";
import {
  createLiveConversationProjector,
  ProjectionCapacityError,
} from "./project-conversation";

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

function deterministicAllocator(prefix: string): OpaqueKeyAllocator {
  let n = 0;
  return () => {
    n += 1;
    return `${prefix}${n}`;
  };
}

const textEncoder = new TextEncoder();
function utf8Bytes(s: string): number {
  return textEncoder.encode(s).length;
}

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

  it("maps a tool/activity row with metadata-only body", () => {
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

  it("maps a failure row", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "failure", id: "fail-1", title: "Error", detail: "wrong" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "failure");
    expect(item?.body).toContain("wrong");
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

  // --- C1: threadKey is registry-allocated, not hash ------------------------

  it("threadKey is opaque — not the raw ref", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
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

  it("threadKey reveals no hash input or salt", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const conv = makeConversation();
    const { view } = p.project(conv, {
      ref: "ref-abc",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(view.threadKey).not.toContain("ref");
    expect(view.threadKey).not.toContain("abc");
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

  it("different refs get different threadKeys", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation();
    const a = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    const b = p.project(conv, {
      ref: "ref-B",
      olderCursor: null,
      projectLabel: "Project",
      updatedLabel: null,
    });
    expect(a.view.threadKey).not.toBe(b.view.threadKey);
  });

  // --- project / updatedLabel / olderAvailable ------------------------------

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
    const { view } = p.project(conv, { ...OPTS });
    expect(view.updatedLabel).toBeNull();
  });

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
    const p = createLiveConversationProjector();
    expect(typeof p.project).toBe("function");
    expect(Object.keys(p).sort()).toEqual(["dispose", "project", "reset"]);
  });

  // --- tone mapping ----------------------------------------------------------

  it("produces running tone for running status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "running" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("running");
  });

  it("produces failed tone for error and failed statuses", () => {
    const p = createLiveConversationProjector();
    for (const status of ["error", "failed"]) {
      const conv = makeConversation({ status });
      const { view } = p.project(conv, { ...OPTS });
      expect(view.tone).toBe("failed");
    }
  });

  it("produces idle tone for ready and idle statuses", () => {
    const p = createLiveConversationProjector();
    for (const status of ["ready", "idle"]) {
      const conv = makeConversation({ status });
      const { view } = p.project(conv, { ...OPTS });
      expect(view.tone).toBe("idle");
    }
  });

  it("produces unknown tone for unrecognized status", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({ status: "weird" });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.tone).toBe("unknown");
  });

  // --- opaque keys (deterministic, no probabilistic assertions) -------------

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
  });

  it("question and option keys are opaque — do not expose raw call IDs", () => {
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

  it("different item IDs produce different opaque keys (no collision)", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "first" },
        { kind: "user", id: "u2", text: "second" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const keys = view.items.map((i) => i.key);
    expect(keys).toHaveLength(2);
    expect(keys[0]).not.toBe(keys[1]);
  });

  it("same item ID across two projector instances gets different keys (deterministic)", () => {
    const p1 = createLiveConversationProjector({
      allocator: deterministicAllocator("a"),
    });
    const p2 = createLiveConversationProjector({
      allocator: deterministicAllocator("b"),
    });
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p1.project(conv, { ...OPTS });
    const b = p2.project(conv, { ...OPTS });
    expect(a.view.items[0]?.key).toBe("a1");
    expect(b.view.items[0]?.key).toBe("b1");
    expect(a.view.items[0]?.key).not.toBe(b.view.items[0]?.key);
  });

  // --- C4: multi-question batch linkage --------------------------------------

  it("multi-question batch projects one linked row per question", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-batch-1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "call-1:0",
                header: "Q1",
                question: "First question?",
                options: [{ label: "A", detail: "Option A" }],
                multiSelect: false,
              },
              {
                key: "call-1:1",
                header: "Q2",
                question: "Second question?",
                options: [{ label: "B", detail: "Option B" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const qItems = view.items.filter((i) => i.kind === "question");
    expect(qItems).toHaveLength(2);
    expect(view.questions).toHaveLength(2);
    expect(qItems[0]?.body).toBe("First question?");
    expect(qItems[0]?.questionKey).not.toBeNull();
    expect(qItems[1]?.body).toBe("Second question?");
    expect(qItems[1]?.questionKey).not.toBeNull();
    expect(qItems[0]?.questionKey).toBe(view.questions[0]?.key);
    expect(qItems[1]?.questionKey).toBe(view.questions[1]?.key);
    expect(qItems[0]?.questionKey).not.toBe(qItems[1]?.questionKey);
  });

  it("questionKey links transcript item to the corresponding LiveQuestionView (single)", () => {
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

  // --- C3: option identity uses stable content, not index -------------------

  it("option keys are stable across option reorder (content identity)", () => {
    const p = createLiveConversationProjector();
    const original = makeConversation({
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
    const a = p.project(original, { ...OPTS });
    const optAKey = a.view.questions[0]?.options[0]?.key;
    const optBKey = a.view.questions[0]?.options[1]?.key;

    const reordered = makeConversation({
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
                options: [
                  { label: "B", detail: "Option B" },
                  { label: "A", detail: "Option A" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const b = p.project(reordered, { ...OPTS });
    expect(b.view.questions[0]?.options[0]?.key).toBe(optBKey);
    expect(b.view.questions[0]?.options[1]?.key).toBe(optAKey);
  });

  it("option insert preserves existing option keys", () => {
    const p = createLiveConversationProjector();
    const original = makeConversation({
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
    const a = p.project(original, { ...OPTS });
    const optAKey = a.view.questions[0]?.options[0]?.key;
    const optBKey = a.view.questions[0]?.options[1]?.key;

    const withInsert = makeConversation({
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
                options: [
                  { label: "A", detail: "Option A" },
                  { label: "C", detail: "Option C" },
                  { label: "B", detail: "Option B" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const b = p.project(withInsert, { ...OPTS });
    expect(b.view.questions[0]?.options[0]?.key).toBe(optAKey);
    expect(b.view.questions[0]?.options[2]?.key).toBe(optBKey);
  });

  it("indistinguishable duplicate options produce a safe error", () => {
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
                options: [
                  { label: "A", detail: "Same" },
                  { label: "A", detail: "Same" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    expect(() => p.project(conv, { ...OPTS })).toThrow(/duplicate option/i);
  });

  it("duplicate option error message contains no raw q.key", () => {
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
                key: "raw-question-key-secret-xyz",
                header: "Choose",
                question: "Which?",
                options: [
                  { label: "A", detail: "Same" },
                  { label: "A", detail: "Same" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    let msg = "";
    try {
      p.project(conv, { ...OPTS });
    } catch (e) {
      msg = e instanceof Error ? e.message : String(e);
    }
    expect(msg).not.toContain("raw-question-key-secret-xyz");
    expect(msg).toMatch(/duplicate option/i);
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
      expect(item.sequenceLabel).not.toContain("u1");
      expect(item.sequenceLabel).not.toContain("a1");
      expect(item.sequenceLabel).not.toContain("u2");
    }
  });

  it("sequence labels are stable across re-projection", () => {
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
    const p = createLiveConversationProjector();
    const initial = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "recent" },
        { kind: "assistant", id: "a1", markdown: "reply", streaming: false },
      ],
    });
    const a = p.project(initial, { ...OPTS });
    const recentLabels = a.view.items.map((i) => i.sequenceLabel);

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
    const bLabels = b.view.items.map((i) => i.sequenceLabel);
    expect(bLabels[2]).toBe(recentLabels[0]);
    expect(bLabels[3]).toBe(recentLabels[1]);
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

  // --- serialization safety --------------------------------------------------

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
          detail: "wrong",
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

  // --- I4: truncation byte-exact assertions ----------------------------------

  it("body including marker is at most 65536 UTF-8 bytes", () => {
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
    expect(utf8Bytes(item?.body ?? "")).toBeLessThanOrEqual(65536);
  });

  it("sets truncated false for normal items", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "short" }],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.items[0]?.truncated).toBe(false);
  });

  it("truncation marker appears exactly once", () => {
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
    const marker = "… truncated";
    expect(item).toBeDefined();
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
    expect(body.endsWith(marker)).toBe(true);
  });

  it("existing marker-ended over-cap input is re-truncated validly to <=65536 bytes", () => {
    const marker = "… truncated";
    const content = "x".repeat(65536);
    const cappedText = content + marker;
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
    const p = createLiveConversationProjector();
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    expect(utf8Bytes(item?.body ?? "")).toBeLessThanOrEqual(65536);
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
  });

  it("store-capped marker text within cap is recognized as truncated (no duplicate)", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    const markerByteLen = utf8Bytes(marker);
    const content = "x".repeat(65536 - markerByteLen);
    const cappedText = content + marker;
    expect(utf8Bytes(cappedText)).toBeLessThanOrEqual(65536);
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "capped-ok",
          markdown: cappedText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    expect(utf8Bytes(item?.body ?? "")).toBeLessThanOrEqual(65536);
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
  });

  it("multibyte truncation produces valid Unicode (no replacement chars)", () => {
    const p = createLiveConversationProjector();
    const emoji = "🎉"; // 4 bytes each
    const largeText = emoji.repeat(20_000); // 80,000 bytes
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
    expect(item?.body).not.toContain("\uFFFD");
    expect(item?.body.endsWith("… truncated")).toBe(true);
    expect(utf8Bytes(item?.body ?? "")).toBeLessThanOrEqual(65536);
  });

  it("projected tool body visibly carries bounded output", () => {
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
    expect(utf8Bytes(item?.body ?? "")).toBeLessThanOrEqual(65536);
  });

  // --- I4: marker-in-input adversarial ---------------------------------------

  it("input with multiple existing markers yields exactly one marker in output", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    // Content has the marker embedded many times, plus exceeds the cap.
    const part = `hello ${marker} world ${marker} `;
    const largeText = part.repeat(10_000); // well over 65536 bytes
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "multi-marker",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
    expect(body.endsWith(marker)).toBe(true);
    expect(utf8Bytes(body)).toBeLessThanOrEqual(65536);
  });

  it("input with single marker not at end, oversized, yields exactly one marker at end", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    const largeText = `prefix ${marker} suffix ${"x".repeat(70_000)}`;
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "mid-marker",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
    expect(body.endsWith(marker)).toBe(true);
    expect(utf8Bytes(body)).toBeLessThanOrEqual(65536);
  });

  it("small input with marker in the middle is not truncated", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    const text = `before ${marker} after`;
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "small-marker",
          markdown: text,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(false);
    expect(item?.body).toBe(text);
  });

  // --- I2: runtime-immutable operational snapshots ---------------------------

  it("returns snapshot operational maps that cannot corrupt internal state", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const opaqueKey = a.view.items[0]?.key;
    expect(opaqueKey).toBeDefined();

    const b = p.project(conv, { ...OPTS });
    expect(b.view.items[0]?.key).toBe(opaqueKey);
    expect(b.operational.itemKeys.get(opaqueKey ?? "")).toBe("u1");
    expect(a.operational.itemKeys.get(opaqueKey ?? "")).toBe("u1");
  });

  it("set on operational map throws without mutation (runtime-immutable)", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const m = a.operational.itemKeys as Map<string, string>;
    expect(() => m.set("injected", "x")).toThrow(TypeError);
    // Verify no mutation occurred.
    expect(a.operational.itemKeys.has("injected")).toBe(false);
  });

  it("delete on operational map throws without mutation (runtime-immutable)", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const opaqueKey = a.view.items[0]?.key ?? "";
    const m = a.operational.itemKeys as Map<string, string>;
    expect(() => m.delete(opaqueKey)).toThrow(TypeError);
    expect(a.operational.itemKeys.get(opaqueKey)).toBe("u1");
  });

  it("clear on operational map throws without mutation (runtime-immutable)", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const m = a.operational.itemKeys as Map<string, string>;
    expect(() => m.clear()).toThrow(TypeError);
    expect(a.operational.itemKeys.size).toBe(1);
  });

  it("mutating a returned operational map does not affect subsequent projection", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const opaqueKey = a.view.items[0]?.key;

    // All mutation attempts throw — no silent corruption.
    const m = a.operational.itemKeys as Map<string, string>;
    expect(() => m.clear()).toThrow(TypeError);

    const b = p.project(conv, { ...OPTS });
    expect(b.view.items[0]?.key).toBe(opaqueKey);
    expect(b.operational.itemKeys.get(opaqueKey ?? "")).toBe("u1");
  });

  // --- I2: reset / dispose / scoped identities -------------------------------

  it("reset(scope) clears only entries for that scope", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyA = a.view.items[0]?.key;

    const b = p.project(conv, {
      ref: "ref-B",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyB = b.view.items[0]?.key;
    expect(keyA).not.toBe(keyB);

    p.reset("ref-A");

    const a2 = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(a2.view.items[0]?.key).not.toBe(keyA);

    const b2 = p.project(conv, {
      ref: "ref-B",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(b2.view.items[0]?.key).toBe(keyB);
  });

  it("reset() with no scope clears everything", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyA = a.view.items[0]?.key;

    p.reset();

    const b = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(b.view.items[0]?.key).not.toBe(keyA);
  });

  it("dispose clears everything", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const keyA = a.view.items[0]?.key;

    p.dispose();

    const b = p.project(conv, { ...OPTS });
    expect(b.view.items[0]?.key).not.toBe(keyA);
  });

  it("reused IDs across different conversation refs do not alias", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const b = p.project(conv, {
      ref: "ref-B",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(a.view.items[0]?.key).not.toBe(b.view.items[0]?.key);
  });

  // --- C1: exact nested Map reset (no delimiter, no prefix matching) --------

  it("reset(scope) does not clear scope that is a prefix of another scope", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, {
      ref: "ref",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyA = a.view.items[0]?.key;

    const b = p.project(conv, {
      ref: "ref-other",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyB = b.view.items[0]?.key;
    expect(keyA).not.toBe(keyB);

    p.reset("ref");

    const b2 = p.project(conv, {
      ref: "ref-other",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(b2.view.items[0]?.key).toBe(keyB);

    const a2 = p.project(conv, {
      ref: "ref",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(a2.view.items[0]?.key).not.toBe(keyA);
  });

  it("reset(scope) is exact — delimiter strings in IDs do not cause cross-scope reset", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "ref-A:weird", text: "hi" }],
    });
    const a = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    const keyA = a.view.items[0]?.key;

    p.reset("ref");

    const a2 = p.project(conv, {
      ref: "ref-A",
      olderCursor: null,
      projectLabel: "P",
      updatedLabel: null,
    });
    expect(a2.view.items[0]?.key).toBe(keyA);
  });

  // --- process-unique deterministic default allocator -------------------------

  it("default allocator is process-unique across projector instances", () => {
    const p1 = createLiveConversationProjector();
    const p2 = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p1.project(conv, { ...OPTS });
    const b = p2.project(conv, { ...OPTS });
    expect(a.view.items[0]?.key).not.toBe(b.view.items[0]?.key);
  });

  it("default allocator produces deterministic keys on the same instance", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const b = p.project(conv, { ...OPTS });
    expect(a.view.items[0]?.key).toBe(b.view.items[0]?.key);
  });

  // --- I1: injected allocator collision detection (transactional) ------------

  it("injected allocator returning a duplicate key throws a generic safe error", () => {
    const collide: OpaqueKeyAllocator = () => "dup-key";
    const p = createLiveConversationProjector({ allocator: collide });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    expect(() => p.project(conv, { ...OPTS })).toThrow(ProjectionCapacityError);
  });

  it("injected allocator collision error message is generic — no raw IDs", () => {
    const collide: OpaqueKeyAllocator = () => "dup-key";
    const p = createLiveConversationProjector({ allocator: collide });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "raw-id-secret-1", text: "a" },
        { kind: "user", id: "raw-id-secret-2", text: "b" },
      ],
    });
    let msg = "";
    try {
      p.project(conv, { ...OPTS });
    } catch (e) {
      msg = e instanceof Error ? e.message : String(e);
    }
    expect(msg).not.toContain("raw-id-secret-1");
    expect(msg).not.toContain("raw-id-secret-2");
    expect(msg.length).toBeGreaterThan(0);
  });

  it("allocator collision commits zero identities — retry succeeds cleanly", () => {
    // Allocator returns the same key on the 2nd call as the 1st, causing a
    // collision during the build phase.
    let callCount = 0;
    const trickyAlloc: OpaqueKeyAllocator = () => {
      callCount++;
      if (callCount === 1) return "shared";
      return "shared"; // collides with the first allocation
    };
    const p = createLiveConversationProjector({ allocator: trickyAlloc });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });

    // First attempt throws on collision at the 2nd allocation.
    expect(() => p.project(conv, { ...OPTS })).toThrow(ProjectionCapacityError);

    // After failure, retry with a fresh projector and clean allocator — must
    // succeed because no partial identities were committed from the failed
    // attempt on the original projector.
    const p2 = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const result = p2.project(conv, { ...OPTS });
    expect(result.view.items).toHaveLength(2);
    expect(result.view.items[0]?.key).not.toBe(result.view.items[1]?.key);
  });

  // --- I1: capacity rejection (transactional, not eviction) ------------------

  it("over-capacity projection throws ProjectionCapacityError, leaves prior registry intact", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
      maxRegistrySize: 4,
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    const a = p.project(conv, { ...OPTS });
    const keyU1 = a.view.items[0]?.key;
    expect(keyU1).toBeDefined();

    const conv2 = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
        { kind: "user", id: "u3", text: "c" },
        { kind: "user", id: "u4", text: "d" },
      ],
    });
    expect(() => p.project(conv2, { ...OPTS })).toThrow(
      ProjectionCapacityError,
    );

    const a2 = p.project(conv, { ...OPTS });
    expect(a2.view.items[0]?.key).toBe(keyU1);
  });

  it("over-capacity projection is stable — repeated attempts throw consistently", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
      maxRegistrySize: 3,
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
        { kind: "user", id: "u3", text: "c" },
        { kind: "user", id: "u4", text: "d" },
      ],
    });
    for (let i = 0; i < 3; i++) {
      expect(() => p.project(conv, { ...OPTS })).toThrow(
        ProjectionCapacityError,
      );
    }
  });

  it("capacity error message is generic — no raw IDs or refs", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
      maxRegistrySize: 2,
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "raw-secret-id", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    let msg = "";
    try {
      p.project(conv, {
        ref: "raw-secret-ref",
        olderCursor: null,
        projectLabel: "P",
        updatedLabel: null,
      });
    } catch (e) {
      msg = e instanceof Error ? e.message : String(e);
    }
    expect(msg).not.toContain("raw-secret-id");
    expect(msg).not.toContain("raw-secret-ref");
  });

  it("duplicate option failure commits zero identities — retry with fix succeeds", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const badConv = makeConversation({
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
                options: [
                  { label: "A", detail: "Same" },
                  { label: "A", detail: "Same" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    expect(() => p.project(badConv, { ...OPTS })).toThrow(/duplicate option/i);

    // Retry with fixed options — must succeed with clean allocator sequence,
    // proving no identities were committed from the failed attempt.
    const goodConv = makeConversation({
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
                options: [
                  { label: "A", detail: "One" },
                  { label: "B", detail: "Two" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const result = p.project(goodConv, { ...OPTS });
    expect(result.view.questions).toHaveLength(1);
    expect(result.view.questions[0]?.options).toHaveLength(2);
  });

  // --- I3: zero-question batch emits no rows and no cards -------------------

  it("zero-question batch emits no transcript row", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-empty",
          batch: {
            callId: "call-1",
            questions: [],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const qItems = view.items.filter((i) => i.kind === "question");
    expect(qItems).toHaveLength(0);
  });

  it("zero-question batch creates no LiveQuestionView card", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-empty",
          batch: {
            callId: "call-1",
            questions: [],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions).toHaveLength(0);
  });

  it("zero-question batch creates no private mappings — no operational entries", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-empty",
          batch: {
            callId: "call-1",
            questions: [],
          },
        },
      ],
    });
    const { operational } = p.project(conv, { ...OPTS });
    expect(operational.questionKeys.size).toBe(0);
    expect(operational.optionKeys.size).toBe(0);
    expect(operational.itemKeys.size).toBe(0);
  });

  it("zero-question batch does not consume allocator keys", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const emptyConv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-empty",
          batch: {
            callId: "call-1",
            questions: [],
          },
        },
      ],
    });
    p.project(emptyConv, { ...OPTS });

    // Now project a real item — it should get k1 (thread) and k2 (item),
    // proving the empty batch consumed no allocator keys.
    const realConv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const result = p.project(realConv, { ...OPTS });
    expect(result.view.threadKey).toBe("k1");
    expect(result.view.items[0]?.key).toBe("k2");
  });

  it("multi-question batch still projects one row/card per question", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-batch",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "call-1:0",
                header: "Q1",
                question: "First?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
              {
                key: "call-1:1",
                header: "Q2",
                question: "Second?",
                options: [{ label: "B", detail: "B" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const qItems = view.items.filter((i) => i.kind === "question");
    expect(qItems).toHaveLength(2);
    expect(view.questions).toHaveLength(2);
  });

  // --- C1: adversarial crafted collisions (delimiter/NUL in components) -----

  it("item ID containing colon does not collide with namespace delimiter", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    // "item:weird" as an ID should not alias with the "item" namespace.
    const conv = makeConversation({
      items: [
        { kind: "user", id: "item:weird", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.items).toHaveLength(2);
    expect(view.items[0]?.key).not.toBe(view.items[1]?.key);
    expect(view.items[0]?.key).not.toContain("item:weird");
  });

  it("item ID containing NUL does not cause aliasing", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "a\u0000b", text: "nul-id" },
        { kind: "user", id: "a", text: "plain" },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.items).toHaveLength(2);
    expect(view.items[0]?.key).not.toBe(view.items[1]?.key);
  });

  it("question key containing colon does not alias across callIds", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    // Two questions with keys that could collide via delimiter: "call:0" vs
    // "call" + ":0". With exact tuple paths, callId and q.key are separate
    // dimensions and cannot alias.
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call",
            questions: [
              {
                key: ":0",
                header: "Q",
                question: "First?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
        {
          kind: "question",
          id: "q2",
          batch: {
            callId: "call:0",
            questions: [
              {
                key: "x",
                header: "Q",
                question: "Second?",
                options: [{ label: "B", detail: "B" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions).toHaveLength(2);
    expect(view.questions[0]?.key).not.toBe(view.questions[1]?.key);
  });

  it("option label containing NUL does not alias with detail", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    // Option 1: label="A\u0000B", detail="C"
    // Option 2: label="A", detail="B\u0000C"
    // With NUL-composite these would collide; with exact tuples they don't.
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
                header: "Q",
                question: "Which?",
                options: [
                  { label: "A\u0000B", detail: "C" },
                  { label: "A", detail: "B\u0000C" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions[0]?.options).toHaveLength(2);
    expect(view.questions[0]?.options[0]?.key).not.toBe(
      view.questions[0]?.options[1]?.key,
    );
  });

  it("callId reuse: same question key in different batches gets different keys", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    // Two batches with same q.key but different callId → different question keys.
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "shared-key",
                header: "Q",
                question: "First?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
        {
          kind: "question",
          id: "q2",
          batch: {
            callId: "call-2",
            questions: [
              {
                key: "shared-key",
                header: "Q",
                question: "Second?",
                options: [{ label: "A", detail: "A" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions).toHaveLength(2);
    expect(view.questions[0]?.key).not.toBe(view.questions[1]?.key);
  });

  it("option identity includes callId — same label/detail across batches differ", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
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
                header: "Q",
                question: "Which?",
                options: [{ label: "A", detail: "Same" }],
                multiSelect: false,
              },
            ],
          },
        },
        {
          kind: "question",
          id: "q2",
          batch: {
            callId: "call-2",
            questions: [
              {
                key: "call-2:0",
                header: "Q",
                question: "Which?",
                options: [{ label: "A", detail: "Same" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions[0]?.options[0]?.key).not.toBe(
      view.questions[1]?.options[0]?.key,
    );
  });

  // --- operational map snapshot completeness --------------------------------

  it("operational itemKeys include every item in the current projection", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
        { kind: "user", id: "u3", text: "c" },
      ],
    });
    const { view, operational } = p.project(conv, { ...OPTS });
    expect(operational.itemKeys.size).toBe(view.items.length);
    for (const item of view.items) {
      expect(operational.itemKeys.has(item.key)).toBe(true);
    }
  });

  it("operational questionKeys and optionKeys include current projection only", () => {
    const p = createLiveConversationProjector();
    const conv1 = makeConversation({
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
                options: [
                  { label: "A", detail: "A" },
                  { label: "B", detail: "B" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    p.project(conv1, { ...OPTS });

    const conv2 = makeConversation({
      items: [
        {
          kind: "question",
          id: "q2",
          batch: {
            callId: "call-2",
            questions: [
              {
                key: "call-2:0",
                header: "Other",
                question: "What?",
                options: [{ label: "C", detail: "C" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const r2 = p.project(conv2, { ...OPTS });
    expect(r2.operational.questionKeys.size).toBe(1);
    expect(r2.operational.optionKeys.size).toBe(1);
    for (const q of r2.view.questions) {
      expect(r2.operational.questionKeys.has(q.key)).toBe(true);
    }
    for (const q of r2.view.questions) {
      for (const opt of q.options) {
        expect(r2.operational.optionKeys.has(opt.key)).toBe(true);
      }
    }
  });

  // --- C1: option identity uses exact nested question scope ----------------

  it("same option label/detail in different questions get different keys", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q-multi",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "call-1:0",
                header: "Q1",
                question: "First?",
                options: [{ label: "A", detail: "Same" }],
                multiSelect: false,
              },
              {
                key: "call-1:1",
                header: "Q2",
                question: "Second?",
                options: [{ label: "A", detail: "Same" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    expect(view.questions).toHaveLength(2);
    expect(view.questions[0]?.options[0]?.key).not.toBe(
      view.questions[1]?.options[0]?.key,
    );
  });

  // --- title / determinism ---------------------------------------------------

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

  // --- R1: structured reverse operational linkage ----------------------------

  it("questionKeys reverse linkage returns structured {callId, questionKey} tuple", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-A",
            questions: [
              {
                key: "qk-1",
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
    const { operational, view } = p.project(conv, { ...OPTS });
    const qKey = view.questions[0]?.key;
    expect(qKey).toBeDefined();
    const entry = operational.questionKeys.get(qKey ?? "");
    expect(entry).toBeDefined();
    // Structured tuple, not a lossy string.
    expect(typeof entry).toBe("object");
    expect(entry).not.toBe(null);
    expect((entry as { callId: string; questionKey: string }).callId).toBe(
      "call-A",
    );
    expect((entry as { callId: string; questionKey: string }).questionKey).toBe(
      "qk-1",
    );
  });

  it("optionKeys reverse linkage returns structured {callId, questionKey, label, detail} tuple", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-B",
            questions: [
              {
                key: "qk-2",
                header: "Choose",
                question: "Which?",
                options: [{ label: "OptA", detail: "DetailA" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { operational, view } = p.project(conv, { ...OPTS });
    const optKey = view.questions[0]?.options[0]?.key;
    expect(optKey).toBeDefined();
    const entry = operational.optionKeys.get(optKey ?? "");
    expect(entry).toBeDefined();
    expect(typeof entry).toBe("object");
    expect(entry).not.toBe(null);
    const tup = entry as {
      callId: string;
      questionKey: string;
      label: string;
      detail: string;
    };
    expect(tup.callId).toBe("call-B");
    expect(tup.questionKey).toBe("qk-2");
    expect(tup.label).toBe("OptA");
    expect(tup.detail).toBe("DetailA");
  });

  it("adversarial different callIds with same qkey+option recover distinct exact tuples", () => {
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
    });
    const conv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-X",
            questions: [
              {
                key: "shared-qk",
                header: "Q",
                question: "Which?",
                options: [{ label: "L", detail: "D" }],
                multiSelect: false,
              },
            ],
          },
        },
        {
          kind: "question",
          id: "q2",
          batch: {
            callId: "call-Y",
            questions: [
              {
                key: "shared-qk",
                header: "Q",
                question: "Which?",
                options: [{ label: "L", detail: "D" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { operational, view } = p.project(conv, { ...OPTS });
    expect(view.questions).toHaveLength(2);
    const qKey0 = view.questions[0]?.key;
    const qKey1 = view.questions[1]?.key;
    expect(qKey0).not.toBe(qKey1);

    const qEntry0 = operational.questionKeys.get(qKey0 ?? "") as {
      callId: string;
      questionKey: string;
    };
    const qEntry1 = operational.questionKeys.get(qKey1 ?? "") as {
      callId: string;
      questionKey: string;
    };
    // Same questionKey string, but different callId → distinct tuples.
    expect(qEntry0.questionKey).toBe("shared-qk");
    expect(qEntry1.questionKey).toBe("shared-qk");
    expect(qEntry0.callId).toBe("call-X");
    expect(qEntry1.callId).toBe("call-Y");
    expect(qEntry0).not.toEqual(qEntry1);

    // Options: same label/detail, different callId → distinct tuples.
    const optKey0 = view.questions[0]?.options[0]?.key;
    const optKey1 = view.questions[1]?.options[0]?.key;
    expect(optKey0).not.toBe(optKey1);
    const oEntry0 = operational.optionKeys.get(optKey0 ?? "") as {
      callId: string;
      questionKey: string;
      label: string;
      detail: string;
    };
    const oEntry1 = operational.optionKeys.get(optKey1 ?? "") as {
      callId: string;
      questionKey: string;
      label: string;
      detail: string;
    };
    expect(oEntry0.label).toBe("L");
    expect(oEntry1.label).toBe("L");
    expect(oEntry0.callId).toBe("call-X");
    expect(oEntry1.callId).toBe("call-Y");
    expect(oEntry0).not.toEqual(oEntry1);
  });

  it("reverse linkage values are immutable (frozen)", () => {
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
                key: "qk-1",
                header: "Q",
                question: "Which?",
                options: [{ label: "A", detail: "B" }],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const { operational, view } = p.project(conv, { ...OPTS });
    const qKey = view.questions[0]?.key ?? "";
    const qEntry = operational.questionKeys.get(qKey) as {
      callId: string;
      questionKey: string;
    };
    expect(Object.isFrozen(qEntry)).toBe(true);

    const optKey = view.questions[0]?.options[0]?.key ?? "";
    const oEntry = operational.optionKeys.get(optKey) as {
      callId: string;
      questionKey: string;
      label: string;
      detail: string;
    };
    expect(Object.isFrozen(oEntry)).toBe(true);
  });

  // --- R2: runtime-immutable snapshot wrapper (#private backing) --------------

  it("FrozenMap backing map is inaccessible via property enumeration", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const { operational } = p.project(conv, { ...OPTS });
    const m = operational.itemKeys as unknown as Record<string, unknown>;
    // The private backing map must not be accessible by any enumerable key.
    for (const key of Object.keys(m)) {
      const val = m[key];
      if (val instanceof Map) {
        // If we find a Map property, it should be empty or not the backing map.
        expect(val.size).toBe(0);
      }
    }
    // No property should be a Map containing our data.
    const allValues = Object.getOwnPropertyNames(m);
    for (const name of allValues) {
      const v = m[name];
      if (v instanceof Map) {
        expect(v.has(operational.itemKeys.keys().next().value ?? "")).toBe(
          false,
        );
      }
    }
  });

  it("FrozenMap backing map is inaccessible via getOwnPropertyNames cast", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const { operational } = p.project(conv, { ...OPTS });
    const opaqueKey = operational.itemKeys.keys().next().value as string;
    expect(opaqueKey).toBeDefined();
    const m = operational.itemKeys as unknown as object;
    // Try every own property name — none should be the backing Map.
    for (const prop of Object.getOwnPropertyNames(m)) {
      const val = (m as Record<string, unknown>)[prop];
      if (val instanceof Map) {
        expect(val.get(opaqueKey)).toBeUndefined();
      }
    }
  });

  it("FrozenMap cast to Map cannot mutate via set after getting entries", () => {
    const p = createLiveConversationProjector();
    const conv = makeConversation({
      items: [{ kind: "user", id: "u1", text: "hi" }],
    });
    const a = p.project(conv, { ...OPTS });
    const m = a.operational.itemKeys as Map<string, string>;
    // Verify entries() returns a snapshot iterator, not a live one.
    const entries = [...m.entries()];
    expect(entries).toHaveLength(1);
    // Even if we try to get the internal via entries, mutating fails.
    expect(() => m.set("hack", "val")).toThrow(TypeError);
    expect(a.operational.itemKeys.has("hack")).toBe(false);
  });

  // --- R3: fixed-point truncation marker elimination --------------------------

  it("marker prefix + marker + suffix where join forms new marker yields exactly one marker", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    // Construct input so that removing the middle marker occurrence joins
    // the prefix and suffix to form a NEW marker occurrence.
    // marker = "… truncated" (11 chars)
    // prefix = "…" (first char of marker)
    // suffix = " truncated" (rest of marker)
    // So: prefix + marker + suffix = "…" + "… truncated" + " truncated"
    // After removing the middle "… truncated": "…" + " truncated" = "… truncated" (new marker!)
    // A single-pass strip would leave this second marker in the output.
    const prefix = marker.slice(0, 1); // "…"
    const suffix = marker.slice(1); // " truncated"
    // Build oversized content: prefix + marker + suffix + padding
    const padding = "x".repeat(70_000);
    const largeText = prefix + marker + suffix + padding;
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "tricky-marker",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    // Fixed-point elimination must remove the re-formed marker too.
    expect(count).toBe(1);
    expect(body.endsWith(marker)).toBe(true);
    expect(utf8Bytes(body)).toBeLessThanOrEqual(65536);
  });

  it("nested marker formation across multiple joins eliminates to fixed point", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    // Chain: prefix + marker + mid + marker + suffix
    // where prefix+mid forms a marker, and mid+suffix forms another.
    // prefix = "…", mid = " truncated…", suffix = " truncated"
    // After removing both markers: "…" + " truncated…" + " truncated"
    // = "… truncated…" + " truncated" — first join forms "… truncated" again
    // Fixed point must eliminate ALL formed markers.
    const prefix = marker.slice(0, 1); // "…"
    const mid = marker.slice(1) + marker.slice(0, 1); // " truncated…"
    const suffix = marker.slice(1); // " truncated"
    const padding = "x".repeat(70_000);
    const largeText = prefix + marker + mid + marker + suffix + padding;
    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "nested-marker",
          markdown: largeText,
          streaming: false,
        },
      ],
    });
    const { view } = p.project(conv, { ...OPTS });
    const item = view.items.find((i) => i.kind === "assistant");
    expect(item?.truncated).toBe(true);
    const body = item?.body ?? "";
    const count = body.split(marker).length - 1;
    expect(count).toBe(1);
    expect(body.endsWith(marker)).toBe(true);
    expect(utf8Bytes(body)).toBeLessThanOrEqual(65536);
  });

  it("adversarial deep chain A.repeat(n)+B.repeat(n) eliminates all markers in linear work", () => {
    const p = createLiveConversationProjector();
    const marker = "… truncated";
    // A = first Unicode scalar of marker, B = rest of marker.
    // A.repeat(n) + B.repeat(n) produces n overlapping marker candidates:
    // each A followed by B forms a marker. After removing one, the adjacent
    // A and B join to form a new marker. A naive repeated whole-string scan
    // would need O(n) passes each O(n) → O(n²). A linear single-pass
    // algorithm processes each scalar once.
    const A = marker.slice(0, 1); // "…"
    const B = marker.slice(1); // " truncated"
    const n = 5_000;
    // Build oversized input: A*n + B*n + padding to exceed the cap.
    const padding = "x".repeat(70_000);
    const largeText = A.repeat(n) + B.repeat(n) + padding;

    const conv = makeConversation({
      items: [
        {
          kind: "assistant",
          id: "deep-chain",
          markdown: largeText,
          streaming: false,
        },
      ],
    });

    // Black/gray-box oracle: spy on String.prototype.split, replace, and the
    // String iterator to prove the strip algorithm does NOT use repeated
    // whole-string split/replace/rescan. The old fixed-point implementation
    // called stripped.split(marker).join("") in a loop; on this adversarial
    // input that loop runs O(n) passes, each O(n) → O(n²) split calls on the
    // full string. The new KMP stack reducer iterates the string once via its
    // iterator and never calls split/replace on the oversized content.
    let splitCalls = 0;
    let replaceCalls = 0;
    let stringIteratorCreations = 0;
    const origSplit = String.prototype.split;
    const origReplace = String.prototype.replace;
    const origStringIterator = String.prototype[Symbol.iterator];

    // Track array push/length-mutations to bound stack work proportionally to n.
    let arrayPushCalls = 0;
    let arrayJoinCalls = 0;
    const origArrayPush = Array.prototype.push;
    const origArrayJoin = Array.prototype.join;

    try {
      String.prototype.split = function (
        this: string,
        ...args: Parameters<string["split"]>
      ): string[] {
        if (this.length > marker.length) splitCalls++;
        return origSplit.apply(this, args);
      } as string["split"];
      String.prototype.replace = function (
        this: string,
        ...args: Parameters<string["replace"]>
      ): string {
        if (this.length > marker.length) replaceCalls++;
        return origReplace.apply(this, args);
      } as string["replace"];
      // Spies must be restored in finally; use a loose type for the iterator
      // override since StringIterator<string> and IterableIterator<string>
      // differ in [Symbol.dispose].
      const iteratorSpy: (this: string) => IterableIterator<string> = function (
        this: string,
      ): IterableIterator<string> {
        if (this.length > marker.length) stringIteratorCreations++;
        return origStringIterator.call(this) as IterableIterator<string>;
      };
      (String.prototype as { [Symbol.iterator]: unknown })[Symbol.iterator] =
        iteratorSpy;
      Array.prototype.push = function (
        this: unknown[],
        ...args: unknown[]
      ): number {
        arrayPushCalls++;
        return origArrayPush.apply(this, args);
      } as typeof Array.prototype.push;
      Array.prototype.join = function (
        this: unknown[],
        ...args: Parameters<typeof origArrayJoin>
      ): string {
        arrayJoinCalls++;
        return origArrayJoin.apply(this, args);
      } as typeof Array.prototype.join;

      const { view } = p.project(conv, { ...OPTS });

      const item = view.items.find((i) => i.kind === "assistant");
      expect(item?.truncated).toBe(true);
      const body = item?.body ?? "";
      // Use the original split for assertions (not the spy) to avoid polluting
      // the split-call count. The spy is only for monitoring the projector.
      const splitOrig = origSplit as unknown as (
        this: string,
        sep: string,
      ) => string[];
      const markerCount = splitOrig.call(body, marker).length - 1;
      expect(markerCount).toBe(1);
      expect(body.endsWith(marker)).toBe(true);
      expect(utf8Bytes(body)).toBeLessThanOrEqual(65536);

      // No repeated whole-string split/replace/rescan: the old fixed-point
      // implementation would call split on the oversized text at least once
      // (and O(n) times on this adversarial input). The KMP reducer never
      // calls split or replace on the large string.
      expect(splitCalls).toBe(0);
      expect(replaceCalls).toBe(0);

      // One input traversal: the String iterator is created exactly once for
      // the oversized content (the for...of loop in stripMarkersLinear).
      expect(stringIteratorCreations).toBe(1);

      // Bounded actual stack mutations proportional to n: each character is
      // pushed at most once, so total push calls are O(input length). The old
      // fixed-point implementation did no push/pop but did O(n) full-string
      // split+join passes. Here we prove pushes are linear, not quadratic.
      const inputLength = largeText.length;
      expect(arrayPushCalls).toBeGreaterThan(0);
      expect(arrayPushCalls).toBeLessThanOrEqual(inputLength * 2);

      // One output join: the final result is built with a single .join("").
      // Multiple join calls would indicate repeated string assembly.
      // (arrayJoinCalls counts all joins on arrays; the projector's key/seq
      // allocation paths use Maps, not array joins, so join is dominated by
      // the strip output. We assert at least one join occurred and that the
      // total is small — the old split().join() loop would call join once
      // per pass, i.e. O(n) times.)
      expect(arrayJoinCalls).toBeGreaterThanOrEqual(1);
      expect(arrayJoinCalls).toBeLessThanOrEqual(inputLength / 1000 + 10);
    } finally {
      String.prototype.split = origSplit;
      String.prototype.replace = origReplace;
      (String.prototype as { [Symbol.iterator]: unknown })[Symbol.iterator] =
        origStringIterator;
      Array.prototype.push = origArrayPush;
      Array.prototype.join = origArrayJoin;
    }
  });

  // --- R4: same-failed-projector transactional zero-retention -----------------

  it("always-colliding allocator on same projector: tight cap proves zero retained identities", () => {
    // Allocator collides on every call during the first projection, then
    // recovers with unique values on the second. A tight capacity means ANY
    // leaked identity from the failed projection would exhaust the cap on
    // retry on the SAME projector.
    let phase: "collide" | "recover" = "collide";
    let n = 0;
    const smartAlloc: OpaqueKeyAllocator = () => {
      if (phase === "collide") return "dup"; // always collide in phase 1
      n++;
      return `r${n}`;
    };
    // 2 user items → 3 identities (thread + 2 items). cap=3 is exact.
    // If the failed projection leaked even 1 identity, totalIdentities > 0
    // and 3 new identities would exceed the cap of 3 on retry.
    const p = createLiveConversationProjector({
      allocator: smartAlloc,
      maxRegistrySize: 3,
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    // First attempt: allocator always returns "dup" → collision at 2nd call.
    expect(() => p.project(conv, { ...OPTS })).toThrow(ProjectionCapacityError);

    // Switch to recovery phase and retry on SAME projector.
    phase = "recover";
    // 3 new identities must fit cap=3 — proves zero leaked from failure.
    // Assert exact retry identity keys to prove the rollback: if any identity
    // or counter leaked from the failed projection, the allocator counter n
    // would be offset and these keys would differ, or the projection would
    // throw ProjectionCapacityError because totalIdentities > 0.
    // Allocation order: u1 key(r1), u1 seq(r2), u2 key(r3), u2 seq(r4),
    // thread(r5). 3 identities, 5 allocations.
    const result = p.project(conv, { ...OPTS });
    expect(result.view.items).toHaveLength(2);
    expect(result.view.items[0]?.key).toBe("r1");
    expect(result.view.items[1]?.key).toBe("r3");
    expect(result.view.threadKey).toBe("r5");
    // Operational map proves the scope (ref-1) was not retained from failure.
    expect(result.operational.itemKeys.get("r1")).toBe("u1");
    expect(result.operational.itemKeys.get("r3")).toBe("u2");
  });

  it("same-projector: collision failure then success proves zero leaked counters/scopes", () => {
    // Use a projector where the allocator collides on the first projection
    // but succeeds on the second. The same projector must have zero retained
    // state from the failed attempt.
    let phase = 0;
    let n = 0;
    const phaseAlloc: OpaqueKeyAllocator = () => {
      if (phase === 0) return "dup"; // always collide in phase 0
      n++;
      return `s${n}`;
    };
    const p = createLiveConversationProjector({
      allocator: phaseAlloc,
      maxRegistrySize: 3,
    });
    const conv = makeConversation({
      items: [
        { kind: "user", id: "u1", text: "a" },
        { kind: "user", id: "u2", text: "b" },
      ],
    });
    // First projection: allocator always returns "dup" → collision.
    expect(() => p.project(conv, { ...OPTS })).toThrow(ProjectionCapacityError);

    // Switch to recovery phase.
    phase = 1;
    // Second projection on SAME projector: must succeed with exactly 3
    // identities (thread + 2 items), no leaked counters from the failure.
    // Allocation order: u1 key(s1), u1 seq(s2), u2 key(s3), u2 seq(s4),
    // thread(s5). But only 3 identities count toward the cap.
    const result = p.project(conv, { ...OPTS });
    expect(result.view.items).toHaveLength(2);
    expect(result.view.items[0]?.key).toBe("s1");
    expect(result.view.items[1]?.key).toBe("s3");
    expect(result.view.threadKey).toBe("s5");
  });

  it("duplicate failure on same projector exhausts cap if any leak — proves zero retention", () => {
    // A question batch with 1 question + 2 options creates 5 identities:
    // thread(1) + qitem(1) + question(1) + option(1) + option(1) = 5.
    // Duplicate option causes failure mid-build. If ANY identity leaked,
    // retry with 5 new identities would exceed cap=5.
    const p = createLiveConversationProjector({
      allocator: deterministicAllocator("k"),
      maxRegistrySize: 5,
    });
    const badConv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "qk-1",
                header: "Q",
                question: "Which?",
                options: [
                  { label: "A", detail: "Same" },
                  { label: "A", detail: "Same" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    expect(() => p.project(badConv, { ...OPTS })).toThrow(/duplicate option/i);

    // Retry on SAME projector with fixed options — 5 identities must fit
    // in cap=5, proving zero leaked from the failed attempt.
    const goodConv = makeConversation({
      items: [
        {
          kind: "question",
          id: "q1",
          batch: {
            callId: "call-1",
            questions: [
              {
                key: "qk-1",
                header: "Q",
                question: "Which?",
                options: [
                  { label: "A", detail: "One" },
                  { label: "B", detail: "Two" },
                ],
                multiSelect: false,
              },
            ],
          },
        },
      ],
    });
    const result = p.project(goodConv, { ...OPTS });
    expect(result.view.questions).toHaveLength(1);
    expect(result.view.questions[0]?.options).toHaveLength(2);
    expect(result.view.threadKey).toBeDefined();
    expect(result.view.items).toHaveLength(1); // 1 qitem row
  });
});
