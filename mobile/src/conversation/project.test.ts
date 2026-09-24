// Fixture-driven tests for the native display-row projection over the
// package's hydrated ThreadModel. Every fixture is a literal Thread wire
// object; no network, no provider credentials, no ambient state. These
// exercise projectConversation's item classification and clustering, the
// forward-compatibility rule (unknown item types must NOT disappear), and
// native's contract on the thread-level fields hydrateThread supplies
// (capabilities, queue, usage/cost, reasoning profile, identity).

import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  AskQuestionRef,
  EvenerThread,
  InputItem,
  ItemModel,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadModel,
  Turn,
  TurnModel,
} from "@evener/appwire-client";
import type {
  ActivityDetail,
  ActivityFamily,
  MobileConversation,
  MobileTimelineItem,
} from "./project";
import { applyNotification, hydrateThread } from "@evener/appwire-client";
import {
  liveAsksFor,
  MAX_ITEM_BYTES,
  projectConversation,
  projectTimeline,
  truncateItem,
  truncateText,
} from "./project";

// The oracle drives the shim exactly as the service does: hydrate the wire
// Thread through the package, then project the display rows.
function projectThread(thread: Thread): MobileConversation {
  return projectConversation(hydrateThread({ thread }, thread.evener.ref, 0));
}

// --- fixture helpers ---------------------------------------------------------

it.each([0, 7])(
  "preserves the typed user transcript entry %s independently of turn and row identity",
  (transcriptEntryIndex) => {
    const projected = projectThread(
      thread([
        turn("turn_m99", [
          item({
            id: "row-without-an-index",
            type: "userMessage",
            turnId: "turn_m99",
            text: "selected input",
            transcriptEntryIndex,
            position: { entry: 800, item: 1 },
          }),
        ]),
      ]),
    );
    expect(projected.items.find((row) => row.kind === "user")).toMatchObject({
      transcriptEntryIndex,
      text: "selected input",
    });
  },
);

it("does not invent a fork entry from turn ids or row positions", () => {
  const projected = projectThread(
    thread([
      turn("turn_7", [
        item({
          id: "user_7",
          type: "userMessage",
          text: "input",
          position: { entry: 800, item: 1 },
        }),
      ]),
    ]),
  );
  expect(projected.items.find((row) => row.kind === "user")).not.toHaveProperty(
    "transcriptEntryIndex",
  );
});

it("does not offer ordinary fork for a human steering notice rendered as a user row", () => {
  const projected = projectThread(
    thread([
      turn("turn_m99", [
        item({
          id: "steering",
          type: "steering",
          source: "user",
          text: "steering input",
          transcriptEntryIndex: 7,
        }),
      ]),
    ]),
  );
  expect(projected.items[0]?.kind).toBe("user");
  expect(projected.items[0]).not.toHaveProperty("transcriptEntryIndex");
});

const ALL_TRUE_CAPS: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  sharedNotes: false,
  queue: true,
  goal: true,
  rename: true,
};

const EMPTY_QUEUE: QueueState = { revision: 0 };

function evenerThread(over: Partial<EvenerThread> = {}): EvenerThread {
  return {
    ref: "ref-1",
    capabilities: ALL_TRUE_CAPS,
    queue: EMPTY_QUEUE,
    ...over,
  };
}

function turn(id: string, items: ThreadItem[], over: Partial<Turn> = {}): Turn {
  return {
    id,
    items,
    itemsView: "default",
    status: "completed",
    ...over,
  };
}

function item(
  over: Partial<ThreadItem> & { id: string; type: string },
): ThreadItem {
  return {
    turnId: "turn-1",
    ...over,
  } as ThreadItem;
}

function thread(turns: Turn[], over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    turns,
    evener: evenerThread(),
    ...over,
  };
}

// Helper to read the kinds of a projected conversation's items.
function kinds(c: MobileConversation): string[] {
  return c.items.map((i) => i.kind);
}

// --- tests -------------------------------------------------------------------

describe("completed question recaps", () => {
  const argumentsJson = JSON.stringify({
    questions: [
      {
        header: "header-alpha",
        question: "prompt-alpha",
        options: [{ label: "option-alpha", detail: "detail-alpha" }],
      },
      {
        header: "header-beta",
        question: "prompt-beta",
        options: [{ label: "option-beta", detail: "detail-beta" }],
      },
    ],
  });
  function recap(overrides: Partial<ThreadItem> = {}, answered = true) {
    const projected = projectThread(
      thread([
        turn("t1", [
          item({
            id: "ask-recap",
            type: "commandExecution",
            toolName: "ask_user",
            status: "completed",
            argumentsJson,
            ...overrides,
          }),
          ...(answered
            ? [
                item({
                  id: "answer-recap",
                  type: "userMessage",
                  text: "opaque-answer",
                }),
              ]
            : []),
        ]),
      ]),
    );
    expect(kinds(projected)).not.toContain("question");
    const activity = projected.items.find((row) => row.kind === "activity");
    expect(activity?.kind).toBe("activity");
    if (activity?.kind !== "activity")
      throw new Error("question recap missing");
    return activity;
  }

  it("retains posted headers and arguments after a later answer", () => {
    const activity = recap();
    expect(activity.state).toBe("completed");
    expect(activity.detail.description).toContain("header-alpha");
    expect(activity.detail.description).toContain("header-beta");
    expect(activity.detail.description).not.toContain("opaque-answer");
    expect(activity.detail.arguments).toBe(argumentsJson);
  });

  it("preserves an authoritative description without adding inferred details", () => {
    expect(
      recap({ description: "authored-description" }).detail.description,
    ).toBe("authored-description");
  });

  it("keeps failed questions non-actionable with their question context and error", () => {
    const activity = recap(
      { error: "opaque-error", output: "opaque-output" },
      false,
    );
    expect(activity.state).toBe("failed");
    expect(activity.detail.description).toContain("header-alpha");
    expect(activity.detail).toMatchObject({
      error: "opaque-error",
      output: "opaque-output",
      arguments: argumentsJson,
    });
  });

  it.each([
    "{",
    "{}",
    JSON.stringify({ questions: [{ header: "unvalidated-header" }] }),
  ])("does not invent context from malformed arguments %s", (malformed) => {
    const activity = recap({ argumentsJson: malformed });
    expect(activity.detail.description).toBeUndefined();
    expect(activity.detail.arguments).toBe(malformed);
  });
});

describe("projectThread", () => {
  it("projects v5 runtime recovery state without hiding saved history", () => {
    const c = projectThread(
      thread([], {
        status: { type: "restartRequired" },
        evener: evenerThread({ resumeRequired: true }),
      }),
    );
    expect(c.status).toEqual({ type: "restartRequired" });
    expect(c.resumeRequired).toBe(true);
    expect(c.items).toEqual([]);
  });

  describe("user text items", () => {
    it("projects a userMessage as a user item", () => {
      const t = thread([
        turn("t1", [
          item({ id: "u1", type: "userMessage", text: "hello world" }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["user"]);
      const u = c.items[0];
      expect(u).toEqual({ kind: "user", id: "u1", text: "hello world" });
    });

    it("treats user text as plain text (no markdown interpretation needed)", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "u1",
            type: "userMessage",
            text: "<script>alert(1)</script>",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(c.items[0]).toEqual({
        kind: "user",
        id: "u1",
        text: "<script>alert(1)</script>",
      });
    });
  });

  describe("assistant text and deltas", () => {
    it("projects a complete agentMessage as a non-streaming assistant item", () => {
      const t = thread([
        turn("t1", [
          item({ id: "a1", type: "agentMessage", text: "# Heading\nbody" }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["assistant"]);
      expect(c.items[0]).toEqual({
        kind: "assistant",
        id: "a1",
        markdown: "# Heading\nbody",
        streaming: false,
      });
    });

    it("projects an agentMessage as streaming while its turn is incomplete", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", text: "partial" })],
          { status: "inProgress" },
        ),
      ]);
      const c = projectThread(t);
      expect(c.items[0]).toEqual({
        kind: "assistant",
        id: "a1",
        markdown: "partial",
        streaming: true,
      });
    });

    // A snapshot never carries in-flight text (the wire's delta field is set
    // only by the subagent preview, and hydrateThread ignores it); a live
    // reducer accumulates it as pendingText chunks, which the row joins onto
    // the settled text.
    it("joins settled text and pending delta chunks into markdown", () => {
      const model = hydrateThread(
        {
          thread: thread([
            turn(
              "t1",
              [item({ id: "a1", type: "agentMessage", text: "final text" })],
              { status: "inProgress" },
            ),
          ]),
        },
        "ref-1",
        0,
      );
      const streamingItem = model.turns[0]?.items[0];
      if (!streamingItem) throw new Error("fixture lost its item");
      streamingItem.pendingText = ["streaming ", "tail"];
      const a = projectConversation(model).items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") {
        expect(a.markdown).toBe("final textstreaming tail");
        expect(a.streaming).toBe(true);
      }
    });

    it("marks an agentMessage streaming for the wire's inProgress turn status", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", delta: "partial" })],
          { status: "inProgress" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") expect(a.streaming).toBe(true);
    });

    it("marks a complete turn's agentMessage as not streaming", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", delta: "partial" })],
          { status: "completed" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") expect(a.streaming).toBe(false);
    });

    // The streaming signal is per-item, not per-turn: an agentMessage that
    // carries its own status decides streaming alone (isActiveItemInModel —
    // the same field the web reads, TurnBlock.tsx's isItemLive), whatever
    // its turn says. A revert to a turn-only check would keep every test
    // above green — an item without its own status inherits the turn's — so
    // these two pin the item-status side of the signal in both directions.
    it("does not stream a completed agentMessage inside an inProgress turn", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", text: "settled", status: "completed" })],
          { status: "inProgress" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") expect(a.streaming).toBe(false);
    });

    it("streams an agentMessage that carries inProgress status itself", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", text: "in flight", status: "inProgress" })],
          { status: "completed" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") expect(a.streaming).toBe(true);
    });
  });

  describe("reasoning items", () => {
    it("projects a reasoning item as a collapsed labeled activity", () => {
      const t = thread([
        turn("t1", [
          item({ id: "r1", type: "reasoning", text: "thinking..." }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity"]);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.id).toBe("r1");
        expect(a.label.toLowerCase()).toContain("reason");
        expect(a.state).toBe("completed");
      }
    });

    it.each(["failed", "interrupted"] as const)(
      "keeps state completed for a reasoning item with wire status %s (web excludes reasoning from hasItemFailure)",
      (status) => {
        const t = thread([
          turn("t1", [
            item({ id: "r1", type: "reasoning", text: "thinking...", status }),
          ]),
        ]);
        const c = projectThread(t);
        const a = c.items[0];
        expect(a?.kind).toBe("activity");
        if (a?.kind === "activity") expect(a.state).toBe("completed");
      },
    );

    it("projects a status-less reasoning item in an in-progress turn as running", () => {
      const t = thread([
        turn("t1", [item({ id: "r1", type: "reasoning", text: "thinking" })], {
          status: "inProgress",
        }),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") expect(a.state).toBe("running");
    });

    // Live reasoning deltas accumulate in reasoningSummaries (reducer.ts's
    // appendReasoningDelta), never in item.text — a mid-stream reread
    // projects a model whose reasoning item has real content in
    // reasoningSummaries but an empty text, which must still show that
    // content instead of an empty row.
    it("projects live reasoning deltas (reasoningSummaries) even when the settled item.text is empty", () => {
      let model = hydrateThread(
        { thread: thread([turn("t1", [], { status: "inProgress" })]) },
        "ref-1",
        0,
      );
      model = applyNotification(
        model,
        {
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: { type: "reasoning", id: "r1", turnId: "t1", status: "inProgress" },
          },
        } as AnyNotification,
        1000,
      );
      model = applyNotification(
        model,
        {
          method: "item/reasoning/summaryTextDelta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "r1", summaryIndex: 0, delta: "thinking hard" },
        } as AnyNotification,
        1001,
      );
      const c = projectConversation(model);
      const a = c.items.find((row) => row.kind === "activity" && row.id === "r1");
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.detail.output).toBe("thinking hard");
      }
    });

    // reducer.ts's wireItemToModel seeds reasoningSummaries from ANY
    // non-empty initial wire text (item/started, or a replayed item on
    // hydrate), and mergeReasoning then keeps that seeded summary across
    // later merges once it's set. A later completion carrying different,
    // authoritative text must not be masked by the stale seeded summary.
    it("shows the completion's authoritative text over a stale seeded reasoningSummaries entry", () => {
      let model = hydrateThread(
        { thread: thread([turn("t1", [], { status: "inProgress" })]) },
        "ref-1",
        0,
      );
      model = applyNotification(
        model,
        {
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: { type: "reasoning", id: "r1", turnId: "t1", status: "inProgress", text: "draft thought" },
          },
        } as AnyNotification,
        1000,
      );
      model = applyNotification(
        model,
        {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: { type: "reasoning", id: "r1", turnId: "t1", status: "completed", text: "final thought" },
          },
        } as AnyNotification,
        1001,
      );
      const c = projectConversation(model);
      const a = c.items.find((row) => row.kind === "activity" && row.id === "r1");
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.detail.output).toBe("final thought");
      }
    });

    // reducer.ts's appendReasoningDelta appends ONLY to reasoningSummaries,
    // never to item.text — so an ACTIVE item whose item/started carried a
    // non-empty partial seed keeps that stale seed in item.text while later
    // deltas grow reasoningSummaries past it. Preferring item.text
    // unconditionally (as a settled item correctly does) loses the
    // streamed growth for an item that is still running.
    it("shows the growing joined summary over a stale partial seed for an ACTIVE item", () => {
      let model = hydrateThread(
        { thread: thread([turn("t1", [], { status: "inProgress" })]) },
        "ref-1",
        0,
      );
      model = applyNotification(
        model,
        {
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: { type: "reasoning", id: "r1", turnId: "t1", status: "inProgress", text: "partial seed" },
          },
        } as AnyNotification,
        1000,
      );
      model = applyNotification(
        model,
        {
          method: "item/reasoning/summaryTextDelta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            itemId: "r1",
            summaryIndex: 0,
            // wireItemToModel already seeded reasoningSummaries[0] from
            // item/started's "partial seed" text; this delta is the
            // CONTINUATION appended after it (appendReasoningDelta), not a
            // restatement, so the joined chunk list reads as one growing
            // whole: "partial seed" + this delta.
            delta: " continues growing well past the seed",
          },
        } as AnyNotification,
        1001,
      );
      const c = projectConversation(model);
      const a = c.items.find((row) => row.kind === "activity" && row.id === "r1");
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.detail.output).toBe("partial seed continues growing well past the seed");
      }
    });
  });

  describe("shell/tool/MCP call items", () => {
    it("projects a commandExecution tool call with status and duration", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            description: "run tests",
            output: "ok",
            status: "completed",
            startedAt: 1000,
            completedAt: 1500,
            durationMs: 500,
            exitCode: 0,
            callId: "call-1",
            argumentsJson: '{"command":"make"}',
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity"]);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.id).toBe("tool1");
        expect(a.label).toBe("shell");
        expect(a.state).toBe("completed");
        expect(a.detail).toEqual({
          description: "run tests",
          arguments: '{"command":"make"}',
          output: "ok",
          error: undefined,
          exitCode: 0,
          durationMs: 500,
          callId: "call-1",
        } satisfies ActivityDetail);
      }
    });

    it("projects a running tool call as running state", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "inProgress",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("running");
    });

    it("projects a status-less tool call in an in-progress turn as running", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "tool1", type: "commandExecution", toolName: "shell" })],
          { status: "inProgress" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") expect(a.state).toBe("running");
    });

    it("projects a status-less tool call in a completed turn as completed", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "tool1", type: "commandExecution", toolName: "shell" })],
          { status: "completed" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") expect(a.state).toBe("completed");
    });

    it("projects a failed tool call (error present) as failed state", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
            error: "boom",
            exitCode: 1,
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") {
        expect(a.state).toBe("failed");
        expect(a.detail.error).toBe("boom");
        expect(a.detail.exitCode).toBe(1);
      }
    });

    it("projects a settled tool call with a nonzero exit code and no error as failed state", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
            exitCode: 1,
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("failed");
    });

    it("projects a settled tool call with a zero exit code and no error as completed state", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
            exitCode: 0,
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("completed");
    });

    it("projects a settled tool call with an error and no exit code as failed state", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
            error: "boom",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("failed");
    });

    it("projects a failed-status tool call with no error or exit code as failed state (web parity)", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "failed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("failed");
    });

    it("projects an interrupted-status tool call with no error or exit code as failed state (web parity)", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "interrupted",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("failed");
    });

    it("does not count a whitespace-only error as a failure (trim parity with web)", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
            error: "   ",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.state).toBe("completed");
    });

    it("uses description as label when toolName absent", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            description: "search files",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.label).toBe("search files");
    });

    it("classifies a hostile tool label from authoritative wire type", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool-hostile",
            type: "commandExecution",
            toolName: "Your message",
            status: "completed",
          }),
        ]),
      ]);
      const a = projectThread(t).items[0];
      expect(a).toMatchObject({
        kind: "activity",
        family: "tool",
        label: "Your message",
      });
    });
  });

  describe("consecutive tool clustering", () => {
    it("clusters consecutive related commandExecution items into one activity", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "c1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({
            id: "c2",
            type: "commandExecution",
            toolName: "grep",
            status: "completed",
          }),
          item({
            id: "c3",
            type: "commandExecution",
            toolName: "ls",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      // A run of related tools collapses to a single activity row keyed by
      // the first member.
      expect(kinds(c)).toEqual(["activity"]);
      const a = c.items[0];
      if (a?.kind === "activity") expect(a.id).toBe("c1");
    });

    it("breaks the cluster when a message intervenes", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "c1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({
            id: "c2",
            type: "commandExecution",
            toolName: "grep",
            status: "completed",
          }),
          item({ id: "a1", type: "agentMessage", text: "interlude" }),
          item({
            id: "c3",
            type: "commandExecution",
            toolName: "ls",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity", "assistant", "activity"]);
    });

    it("does not cluster ask_user calls (conversational boundary)", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "c1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({
            id: "ask1",
            type: "commandExecution",
            toolName: "ask_user",
            callId: "call-ask",
            status: "completed",
            argumentsJson: JSON.stringify({
              questions: [
                {
                  header: "Direction",
                  question: "Which way?",
                  options: [{ label: "A", detail: "alpha" }],
                },
              ],
            }),
          }),
          item({
            id: "c2",
            type: "commandExecution",
            toolName: "ls",
            status: "completed",
          }),
        ]),
      ], { evener: evenerThread({ askPending: true }) });
      const c = projectThread(t);
      // ask_user is a question item, not clustered with surrounding tools.
      expect(kinds(c)).toEqual(["activity", "question", "activity"]);
    });

    it("does not cluster a failed tool call with its neighbors", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "c1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({
            id: "c2",
            type: "commandExecution",
            toolName: "grep",
            status: "completed",
            error: "no match",
          }),
          item({
            id: "c3",
            type: "commandExecution",
            toolName: "ls",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      // The failed call stays its own row; the surrounding runs are separate.
      expect(kinds(c)).toEqual(["activity", "activity", "activity"]);
    });
  });

  describe("steering items", () => {
    it("preserves typed interruption identity independently of the notice prose", () => {
      const projected = projectThread(
        thread([
          turn("t1", [
            item({
              id: "interrupted",
              type: "steering",
              steeringKind: "interrupted",
              text: "opaque body",
            }),
          ]),
        ]),
      );
      expect(projected.items[0]).toMatchObject({
        kind: "notice",
        origin: "steering",
        steeringKind: "interrupted",
        text: "opaque body",
      });
    });
    it("renders human steering as user input with its images", () => {
      const c = projectThread(
        thread([
          turn("t1", [
            item({
              id: "human-steer",
              type: "steering",
              source: "user",
              text: "input-sentinel",
              images: [
                {
                  type: "image",
                  name: "fixture.png",
                  mediaType: "image/png",
                  data: "BASE64",
                },
              ],
            }),
          ]),
        ]),
      );
      expect(kinds(c)).toEqual(["user", "attachments"]);
      expect(c.items[0]).toMatchObject({
        kind: "user",
        id: "human-steer",
        text: "input-sentinel",
      });
      expect(c.items[1]).toMatchObject({
        kind: "attachments",
        items: [{ src: "data:image/png;base64,BASE64", name: "fixture.png" }],
      });
    });

    it("does not turn daemon steering images into user attachments", () => {
      const c = projectThread(
        thread([
          turn("t1", [
            item({
              id: "daemon-steer",
              type: "steering",
              source: "daemon",
              text: "notice-sentinel",
              images: [
                { type: "image", mediaType: "image/png", data: "BASE64" },
              ],
            }),
          ]),
        ]),
      );
      expect(kinds(c)).toEqual(["notice"]);
    });

    it("projects a steering item as a notice with its kind", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "s1",
            type: "steering",
            steeringKind: "loop-detected",
            text: "Loop detected, steering injected",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["notice"]);
      const n = c.items[0];
      if (n?.kind === "notice") {
        expect(n.id).toBe("s1");
        expect(n.tone).toBe("warning");
        expect(n.text).toContain("Loop detected");
      }
    });

    it("projects a steering item without a kind as info tone", () => {
      const t = thread([
        turn("t1", [item({ id: "s1", type: "steering", text: "nudge" })]),
      ]);
      const c = projectThread(t);
      const n = c.items[0];
      if (n?.kind === "notice") expect(n.tone).toBe("info");
    });

    it.each([
      ["loop-detected", "warning"],
      ["ordinary-steering", "informational"],
      [undefined, "informational"],
    ] as const)("classifies %s steering as %s", (steeringKind, family) => {
      const t = thread([
        turn("t1", [
          item({
            id: "s-family",
            type: "steering",
            steeringKind,
            text: "steering text",
          }),
        ]),
      ]);
      expect(projectThread(t).items[0]).toMatchObject({
        kind: "notice",
        origin: "steering",
        family,
      });
    });
  });

  describe("system notices", () => {
    it("projects a systemMessage as a system notice", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "sys1",
            type: "systemMessage",
            eventKind: "compaction",
            text: "Context compacted",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["notice"]);
      const n = c.items[0];
      if (n?.kind === "notice") {
        expect(n.tone).toBe("system");
        expect(n.text).toBe("Context compacted");
      }
    });

    it("projects an error eventKind systemMessage as a warning notice", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "sys1",
            type: "systemMessage",
            eventKind: "error",
            text: "provider down",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const n = c.items[0];
      if (n?.kind === "notice") expect(n.tone).toBe("warning");
    });

    it.each([
      ["error", "warning"],
      ["system_prompt", "hidden-instruction"],
      ["prompt_loaded", "hidden-instruction"],
      ["environment", "system-prelude"],
      ["round_timings", "diagnostic"],
      ["plugin_loaded", "lifecycle"],
      ["skill_activated", "lifecycle"],
      ["hook_completed", "lifecycle"],
      ["context_compaction", "lifecycle"],
      ["compaction", "lifecycle"],
      ["goal_ended", "lifecycle"],
      ["fork_summary", "lifecycle"],
      ["tool_repair", "lifecycle"],
      ["model_switch", "lifecycle"],
      ["notes-context", "lifecycle"],
      ["future-event", "unknown-system"],
      [undefined, "unknown-system"],
    ] as const)("classifies eventKind %s as %s", (eventKind, family) => {
      const t = thread([
        turn("t1", [
          item({
            id: "sys-family",
            type: "systemMessage",
            eventKind: eventKind as ThreadItem["eventKind"],
            text: "source body",
          }),
        ]),
      ]);
      expect(projectThread(t).items[0]).toMatchObject({
        kind: "notice",
        origin: "system",
        family,
      });
    });
  });

  describe("failures (TurnError)", () => {
    it("projects a turn error as a failure item", () => {
      const t = thread(
        [turn("t1", [item({ id: "a1", type: "agentMessage", text: "hi" })])],
        {},
      );
      const firstTurn = t.turns?.[0];
      if (firstTurn) {
        firstTurn.error = {
          message: "the model blew up",
          title: "Provider error",
          hint: "check api key",
        };
      }
      const c = projectThread(t);
      expect(kinds(c)).toContain("failure");
      const f = c.items.find((i) => i.kind === "failure");
      if (f?.kind === "failure") {
        expect(f.title).toBe("Provider error");
        expect(f.detail).toContain("the model blew up");
      }
    });

    it("uses message as title when title absent", () => {
      const t = thread([turn("t1", [])]);
      const firstTurn = t.turns?.[0];
      if (firstTurn) firstTurn.error = { message: "something went wrong" };
      const c = projectThread(t);
      const f = c.items.find((i) => i.kind === "failure");
      if (f?.kind === "failure") expect(f.title).toBe("something went wrong");
    });

    it("keeps identical errors from different turns as distinct stable failure items", () => {
      const t = thread([turn("turn-a", []), turn("turn-b", [])]);
      for (const current of t.turns ?? [])
        current.error = { message: "same provider failure", title: "Provider error" };

      const first = projectThread(t).items.filter((item) => item.kind === "failure");
      const second = projectThread(t).items.filter((item) => item.kind === "failure");
      expect(first.map((item) => item.id)).toEqual(["failure:turn-a", "failure:turn-b"]);
      expect(new Set(first.map((item) => item.id)).size).toBe(2);
      expect(second).toEqual(first);
    });

    it("keeps the failure row id bounded even when the turn's error prose is oversized", () => {
      const huge = "x".repeat(MAX_ITEM_BYTES + 5_000);
      const t = thread([turn("turn-a", [])]);
      const firstTurn = t.turns?.[0];
      if (firstTurn) firstTurn.error = { message: huge, title: huge };

      const f = projectThread(t).items.find((item) => item.kind === "failure");
      if (f?.kind !== "failure") throw new Error("expected a failure row");
      // The id never embeds the error's prose — only the turn it belongs to —
      // so it stays bounded regardless of how large the title gets.
      expect(f.id).toBe("failure:turn-a");
      // The store's display bound (mobile/src/state/conversation.ts) cuts the
      // rendered title separately; applying it here does not disturb the id.
      const bound = (text: string) => truncateText(text, MAX_ITEM_BYTES);
      const rendered = truncateItem(f, bound);
      if (rendered.kind !== "failure") throw new Error("expected a failure row");
      expect(new TextEncoder().encode(rendered.title).length).toBeLessThanOrEqual(MAX_ITEM_BYTES);
      expect(rendered.id).toBe("failure:turn-a");
    });
  });

  describe("images / attachments", () => {
    it("projects user input images as an attachments item", () => {
      const images: InputItem[] = [
        {
          type: "image",
          name: "cat.png",
          mediaType: "image/png",
          url: "http://x/cat.png",
        },
        {
          type: "image",
          name: "dog.jpg",
          mediaType: "image/jpeg",
          data: "BASE64",
        },
      ];
      const t = thread([
        turn("t1", [
          item({ id: "u1", type: "userMessage", text: "look", images }),
        ]),
      ]);
      const c = projectThread(t);
      // user message still projects, plus an attachments item for the images.
      expect(kinds(c)).toEqual(["user", "attachments"]);
      const att = c.items.find((i) => i.kind === "attachments");
      if (att?.kind === "attachments") {
        expect(att.items).toHaveLength(2);
        expect(att.items[0]?.src).toBe("http://x/cat.png");
        expect(att.items[1]?.src).toBe("data:image/jpeg;base64,BASE64");
      }
    });

    it("projects tool output images as an attachments item", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "view",
            status: "completed",
            outputImages: [
              {
                source: "screenshot",
                name: "shot.png",
                url: "http://x/shot.png",
              },
            ],
          }),
        ]),
      ]);
      const c = projectThread(t);
      const att = c.items.find((i) => i.kind === "attachments");
      if (att?.kind === "attachments") {
        expect(att.items).toHaveLength(1);
        expect(att.items[0]?.src).toBe("http://x/shot.png");
        expect(att.items[0]?.name).toBe("shot.png");
      }
    });

    it("places clustered activity attachments after the cluster", () => {
      const c = projectThread(
        thread([
          turn("t1", [
            item({
              id: "tool1",
              type: "commandExecution",
              toolName: "view",
              status: "completed",
              outputImages: [
                { source: "screenshot", name: "one.png", url: "http://x/one" },
              ],
            }),
            item({
              id: "tool2",
              type: "commandExecution",
              toolName: "view",
              status: "completed",
              outputImages: [
                { source: "screenshot", name: "two.png", url: "http://x/two" },
              ],
            }),
          ]),
        ]),
      );

      expect(c.items.map((entry) => entry.kind)).toEqual([
        "activity",
        "attachments",
        "attachments",
      ]);
      const cluster = c.items[0];
      expect(cluster?.kind === "activity" ? cluster.members : undefined).toHaveLength(2);
    });

    it("does not emit an attachments item when images array is empty", () => {
      const t = thread([
        turn("t1", [
          item({ id: "u1", type: "userMessage", text: "hi", images: [] }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["user"]);
    });
  });

  describe("ask_user pending items", () => {
    it("projects a pending ask_user call (after last user message) as a question item", () => {
      const args = JSON.stringify({
        questions: [
          {
            header: "Direction",
            question: "Which way?",
            options: [
              { label: "North", detail: "up", recommended: true },
              { label: "South", detail: "down" },
            ],
            multi_select: false,
            why: "need to know",
          },
          {
            header: "Speed",
            question: "How fast?",
            options: [{ label: "Fast", detail: "quickly" }],
          },
        ],
      });
      const t = thread([
        turn("t1", [
          item({ id: "u1", type: "userMessage", text: "go" }),
          item({
            id: "ask1",
            type: "commandExecution",
            toolName: "ask_user",
            callId: "call-ask",
            status: "completed",
            argumentsJson: args,
          }),
        ]),
      ], { evener: evenerThread({ askPending: true }) });
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["user", "question"]);
      const q = c.items.find((i) => i.kind === "question");
      if (q?.kind === "question") {
        expect(q.questions).toHaveLength(2);
        expect(q.questions.map((question) => question.callId)).toEqual([
          "call-ask",
          "call-ask",
        ]);
        expect(q.questions[0]?.key).toBe("call-ask:0");
        expect(q.questions[1]?.key).toBe("call-ask:1");
        expect(q.questions[0]?.header).toBe("Direction");
        expect(q.questions[0]?.multiSelect).toBe(false);
        expect(q.questions[0]?.options[0]?.recommended).toBe(true);
      }
    });

    it("does not project an ask_user answered by a later user message", () => {
      const args = JSON.stringify({
        questions: [
          {
            header: "H",
            question: "Q?",
            options: [{ label: "A", detail: "d" }],
          },
        ],
      });
      const t = thread([
        turn("t1", [
          item({
            id: "ask1",
            type: "commandExecution",
            toolName: "ask_user",
            callId: "call-ask",
            status: "completed",
            argumentsJson: args,
          }),
          item({
            id: "u1",
            type: "userMessage",
            text: "[answers]\n1. [H] → A",
          }),
        ]),
      ], { evener: evenerThread({ askPending: true }) });
      const c = projectThread(t);
      // A later userMessage resolves the pending ask, so no question item.
      expect(kinds(c)).not.toContain("question");
    });

    it("does not project an errored ask_user as answerable", () => {
      const args = JSON.stringify({
        questions: [
          {
            header: "H",
            question: "Q?",
            options: [{ label: "A", detail: "d" }],
          },
        ],
      });
      const t = thread([
        turn("t1", [
          item({
            id: "ask1",
            type: "commandExecution",
            toolName: "ask_user",
            callId: "call-ask",
            status: "completed",
            error: "denied",
            argumentsJson: args,
          }),
        ]),
      ], { evener: evenerThread({ askPending: true }) });
      const c = projectThread(t);
      expect(kinds(c)).not.toContain("question");
    });

    it("maps askPending from EvenerThread when there are pending ask_user calls", () => {
      const args = JSON.stringify({
        questions: [
          {
            header: "H",
            question: "Q?",
            options: [{ label: "A", detail: "d" }],
          },
        ],
      });
      const t = thread(
        [
          turn("t1", [
            item({
              id: "ask1",
              type: "commandExecution",
              toolName: "ask_user",
              callId: "call-ask",
              status: "completed",
              argumentsJson: args,
            }),
          ]),
        ],
        { evener: evenerThread({ askPending: true }) },
      );
      const c = projectThread(t);
      expect(c.askPending).toBe(true);
    });

    it("askPending is false when no pending ask_user calls exist", () => {
      const t = thread([
        turn("t1", [item({ id: "u1", type: "userMessage", text: "hi" })]),
      ]);
      const c = projectThread(t);
      expect(c.askPending).toBe(false);
    });
  });

  describe("unknown / forward-compatible item type", () => {
    it("projects an unknown item type as a neutral collapsed activity, never disappearing", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "unk1",
            type: "someNewFutureItemType",
            text: "mystery payload",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity"]);
      const a = c.items[0];
      if (a?.kind === "activity") {
        expect(a.id).toBe("unk1");
        expect(a.state).toBe("completed");
        // Label is neutral, not the raw type string leaking as HTML.
        expect(a.label).toBe("Activity");
      }
    });

    it("never exposes raw HTML from an unknown item's text", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "unk1",
            type: "futureThing",
            text: "<img src=x onerror=alert(1)>",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      if (a?.kind === "activity") {
        // The dangerous text is never placed in a label; it lives in detail
        // as plain text the renderer escapes.
        expect(a.label).not.toContain("<img");
        expect(a.detail.output).toBe("<img src=x onerror=alert(1)>");
      }
    });

    it("clusters consecutive unknown items of the same type", () => {
      const t = thread([
        turn("t1", [
          item({ id: "unk1", type: "futureA", text: "a" }),
          item({ id: "unk2", type: "futureA", text: "b" }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity"]);
    });

    // A live "warning" notification is the only way an ItemModel ever carries
    // type "warning" (reducer.ts's case "warning" fold; there is no wire
    // ThreadItem.warning field, so hydrateThread alone can never produce one) -
    // these tests drive that fold directly rather than a static fixture. A
    // warning projects to the same attention row the live applier emits (kind
    // "failure", title as its own field, message+hint joined as detail), not
    // the generic unknown-activity fallback: the row changed kind the moment a
    // reread replaced the live one. Each case asserts the same contract - every
    // non-blank part appears, as title or in the detail - for a different
    // combination of parts present, including the generic "Warning" title when
    // the frame carries none.
    it.each<[string, Record<string, unknown>, string, string[]]>([
      [
        "a message-less frame",
        { title: "Sandbox blocked", hint: "retry later" },
        "Sandbox blocked",
        ["Sandbox blocked", "retry later"],
      ],
      [
        "a message with a hint, no title",
        { message: "disk is nearly full", hint: "retry later" },
        "Warning",
        ["disk is nearly full", "retry later"],
      ],
      [
        "a title together with a message and hint",
        { title: "Sandbox blocked", message: "disk is nearly full", hint: "retry later" },
        "Sandbox blocked",
        ["Sandbox blocked", "disk is nearly full", "retry later"],
      ],
    ])("projects a warning as the live attention row, composing every part (%s)", (_case, warningParams, expectedTitle, expectedSubstrings) => {
      const t = thread([turn("t1", [item({ id: "u1", type: "userMessage", text: "hi" })])], {
        evener: evenerThread({ activeTurnId: "t1" }),
      });
      let model = hydrateThread({ thread: t }, "ref-1", 1000);
      model = applyNotification(
        model,
        {
          method: "warning",
          params: { threadId: "thread-1", ref: "ref-1", ...warningParams },
        } as AnyNotification,
        2000,
      );
      const c = projectConversation(model);
      const row = c.items.find((entry) => entry.id === "item_warning_live_t1_0");
      expect(row?.kind).toBe("failure");
      if (row?.kind === "failure") {
        expect(row.title).toBe(expectedTitle);
        const shown = `${row.title}\n${row.detail}`;
        for (const substring of expectedSubstrings) expect(shown).toContain(substring);
      }
    });

    // item.warning rides an untyped wire param map, so title can be any JSON
    // value at runtime despite ItemModel declaring it string — WarningItem.tsx
    // and transcriptProjector.ts's itemSummary both guard with hasWarningText
    // before touching it. The reducer's fold normalizes a live frame's
    // non-string title away, but a hand-built or future model can still carry
    // one, so warningItem guards it too and must fall back to the generic
    // "Warning" label rather than hand a non-string to a React Native <Copy>
    // child.
    it("falls back to the generic Warning title for a non-string title on the canonical path", () => {
      const t = thread([turn("t1", [item({ id: "u1", type: "userMessage", text: "hi" })])]);
      const base = hydrateThread({ thread: t }, "ref-1", 0);
      const turnModel = base.turns[0]!;
      const model = {
        ...base,
        turns: [
          {
            ...turnModel,
            items: [
              ...turnModel.items,
              {
                id: "warning-raw",
                turnId: "t1",
                type: "warning",
                text: "careful",
                status: "completed",
                warning: { title: 42 as unknown as string, hint: "slow down" },
              },
            ],
          },
        ],
      };
      const row = projectConversation(model).items.find((entry) => entry.id === "warning-raw");
      expect(row).toMatchObject({
        kind: "failure",
        title: "Warning",
        detail: "careful — slow down",
      });
    });
  });

  describe("capability projection", () => {
    it("passes ThreadCapabilities through unchanged", () => {
      const caps: ThreadCapabilities = {
        send: true,
        steer: false,
        interrupt: true,
        compact: false,
        clear: true,
        forkFromTurn: false,
        shutdown: true,
        changeModel: true,
        changeVisionModel: true,
        sharedNotes: false,
        queue: false,
        goal: true,
        rename: false,
      };
      const t = thread([], { evener: evenerThread({ capabilities: caps }) });
      const c = projectThread(t);
      expect(c.capabilities).toEqual(caps);
    });
  });

  // The queue is the wire QueueState itself, as on the package ThreadModel:
  // identity, revision, full texts and previews stay distinct fields, and an
  // absent depth is the reader's to default (queue?.depth ?? 0).
  describe("queue state", () => {
    it("carries queue identity, revision, full texts and previews as the wire sent them", () => {
      const queue: QueueState = {
        revision: 9,
        depth: 2,
        ids: ["entry-a", "entry-b"],
        texts: ["full first", "full second"],
        preview: ["first…", "second…"],
        clientMutationIds: ["send-a", "send-b"],
      };
      const c = projectThread(thread([], { evener: evenerThread({ queue }) }));
      expect(c.queue).toEqual(queue);
    });

    it("carries a bare queue without inventing a depth or previews", () => {
      expect(projectThread(thread([])).queue).toEqual({ revision: 0 });
    });
  });

  describe("usage projection", () => {
    // An absent wire vision model reads as "" — the package ThreadModel's
    // shape (hydrateThread's default), so "off" and a named model stay
    // distinct from unset without a third state.
    it.each([
      [undefined, ""],
      ["", ""],
      ["off", "off"],
      ["provider/model", "provider/model"],
    ])("projects the wire vision model %j as %j", (visionModel, projected) => {
      const t = thread([], {
        evener: evenerThread({ visionModel }),
      });
      expect(projectThread(t).visionModel).toBe(projected);
    });

    it("projects usage and cost from EvenerThread", () => {
      const t = thread([], {
        evener: evenerThread({
          usage: { inputTokens: 100, outputTokens: 200, totalTokens: 300 },
          cost: "0.05",
        }),
      });
      const c = projectThread(t);
      expect(c.usage).toEqual({
        inputTokens: 100,
        outputTokens: 200,
        totalTokens: 300,
      });
      expect(c.cost).toBe("0.05");
    });

    // No token data is null, never a zero-valued object: EvenerThread.Usage
    // uses nil for "no token data", and cost is unknown (not "$0.00").
    it("projects absent usage and cost as null", () => {
      const c = projectThread(thread([]));
      expect(c.usage).toBeNull();
      expect(c.cost).toBeNull();
    });

    it("projects reasoning effort and levels", () => {
      const t = thread([], {
        evener: evenerThread({
          reasoningEffort: "high",
          reasoningEffortLevels: ["low", "medium", "high"],
          supportsReasoning: true,
        }),
      });
      const c = projectThread(t);
      expect(c.reasoningEffort).toBe("high");
      expect(c.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
      expect(c.visionModel).toBe("");
      expect(c.supportsReasoning).toBe(true);
    });

    it("projects an absent reasoning profile as no levels and no support", () => {
      const c = projectThread(thread([]));
      expect(c.reasoningEffort).toBeUndefined();
      expect(c.reasoningEffortLevels).toEqual([]);
      expect(c.supportsReasoning).toBe(false);
    });
  });

  describe("profile / session identity", () => {
    it("carries thread id, name, modelProvider and the whole status", () => {
      const t = thread(
        [turn("t1", [item({ id: "u1", type: "userMessage", text: "hi" })])],
        {
          id: "thread-42",
          name: "My session",
          modelProvider: "openai",
          status: { type: "running", activeFlags: ["generating"] },
        },
      );
      const c = projectThread(t);
      expect(c.threadId).toBe("thread-42");
      expect(c.name).toBe("My session");
      expect(c.modelProvider).toBe("openai");
      expect(c.status).toEqual({
        type: "running",
        activeFlags: ["generating"],
      });
    });

    it("projects an unnamed thread's name as the empty string", () => {
      expect(projectThread(thread([])).name).toBe("");
    });

    it("projects items across multiple turns in order", () => {
      const t = thread([
        turn("t1", [item({ id: "u1", type: "userMessage", text: "first" })]),
        turn("t2", [item({ id: "a1", type: "agentMessage", text: "reply" })]),
        turn("t3", [item({ id: "u2", type: "userMessage", text: "second" })]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["user", "assistant", "user"]);
      expect(c.items.map((i) => (i as { id: string }).id)).toEqual([
        "u1",
        "a1",
        "u2",
      ]);
    });
  });

  // --- activity family discriminator (Task 2A) --------------------------------
  // The durable activity family is carried in MobileTimelineItem independent
  // of the display label. The canonical projection derives it from the wire
  // item's *type* — commandExecution → "tool", reasoning → "reasoning",
  // anything else → "unknown" — never from the label string. A
  // commandExecution whose toolName is "Reasoning" is still family "tool".
  describe("activity family discriminator", () => {
    function familyOf(c: MobileConversation): ActivityFamily | undefined {
      const a = c.items.find((i) => i.kind === "activity");
      return a?.kind === "activity" ? a.family : undefined;
    }

    it("a commandExecution tool call projects family 'tool'", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(c.items[0]?.kind).toBe("activity");
      expect(familyOf(c)).toBe("tool");
    });

    it("a reasoning item projects family 'reasoning'", () => {
      const t = thread([
        turn("t1", [item({ id: "r1", type: "reasoning", text: "thinking" })]),
      ]);
      const c = projectThread(t);
      expect(c.items[0]?.kind).toBe("activity");
      expect(familyOf(c)).toBe("reasoning");
    });

    it("an unknown item type projects family 'unknown'", () => {
      const t = thread([
        turn("t1", [item({ id: "unk1", type: "futureThing", text: "x" })]),
      ]);
      const c = projectThread(t);
      expect(c.items[0]?.kind).toBe("activity");
      expect(familyOf(c)).toBe("unknown");
    });

    // Adversarial: a commandExecution whose toolName is "Reasoning" must stay
    // family "tool", NOT "reasoning". The family follows the wire type, not the
    // label/toolName. callId must be preserved exactly.
    it("a commandExecution named 'Reasoning' stays family 'tool' and preserves callId", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool-reasoning",
            type: "commandExecution",
            toolName: "Reasoning",
            callId: "call-reasoning-1",
            status: "completed",
            argumentsJson: '{"summary":"thinking about it"}',
            output: "thoughts",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        // Family is "tool" — derived from type commandExecution, not toolName.
        expect(a.family).toBe("tool");
        // Label is still the toolName verbatim (display only).
        expect(a.label).toBe("Reasoning");
        // callId preserved exactly for diagnostics disclosure.
        expect(a.detail.callId).toBe("call-reasoning-1");
      }
    });

    // Adversarial: a reasoning item with custom text does not change family.
    it("a reasoning item with custom text stays family 'reasoning'", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "r1",
            type: "reasoning",
            text: "Step 1: analyze\nStep 2: plan",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.family).toBe("reasoning");
        expect(a.label).toBe("Reasoning");
      }
    });

    // Adversarial: the discriminator is independent of the label — a tool and a
    // reasoning item can share a label string but differ in family.
    it("family is independent of label: same label, different family", () => {
      const t = thread([
        turn("t1", [
          // A commandExecution tool named "Reasoning" (label "Reasoning").
          item({
            id: "tool-1",
            type: "commandExecution",
            toolName: "Reasoning",
            status: "completed",
          }),
          // A genuine reasoning item (also label "Reasoning").
          item({ id: "r-1", type: "reasoning", text: "thoughts" }),
        ]),
      ]);
      const c = projectThread(t);
      const activities = c.items.filter((i) => i.kind === "activity");
      expect(activities).toHaveLength(2);
      const [toolItem, reasoningItem] = activities;
      if (toolItem?.kind === "activity" && reasoningItem?.kind === "activity") {
        expect(toolItem.label).toBe("Reasoning");
        expect(reasoningItem.label).toBe("Reasoning");
        // Same label, different family — the discriminator carries the truth.
        expect(toolItem.family).toBe("tool");
        expect(reasoningItem.family).toBe("reasoning");
      }
    });

    // Adversarial: an unknown item whose text looks like reasoning is still
    // family "unknown".
    it("an unknown item with reasoning-like text stays family 'unknown'", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "unk1",
            type: "newReasoningLikeThing",
            text: "I should reason about this carefully",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.family).toBe("unknown");
        expect(a.label).toBe("Activity");
      }
    });

    // Type/serialization: ActivityFamily is a closed type — only tool |
    // reasoning | unknown. A projected family is always one of these three.
    it("every projected activity family is a member of the closed ActivityFamily type", () => {
      const t = thread([
        turn("t1", [
          item({ id: "r1", type: "reasoning", text: "r" }),
          item({
            id: "t1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({ id: "u1", type: "futureThing", text: "u" }),
        ]),
      ]);
      const c = projectThread(t);
      const families = c.items
        .filter(
          (i): i is Extract<MobileTimelineItem, { kind: "activity" }> =>
            i.kind === "activity",
        )
        .map((i) => i.family);
      for (const f of families) {
        // Closed set membership — exhaustive against ActivityFamily.
        expect(["tool", "reasoning", "unknown"]).toContain(f);
      }
    });

    // Serialization/durability: the family survives a JSON round-trip and is
    // carried alongside the canonical projected shape.
    it("family survives JSON serialization round-trip", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "tool1",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-1",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      const roundTrip = JSON.parse(JSON.stringify(c)) as MobileConversation;
      const a = roundTrip.items[0];
      expect(a?.kind).toBe("activity");
      if (a?.kind === "activity") {
        expect(a.family).toBe("tool");
        expect(a.detail.callId).toBe("call-1");
      }
    });

    // Adversarial: clustering preserves family across a merged run. The first
    // member's family (and item) keys the cluster; merged state never loses it.
    it("clustering preserves family across a merged tool run", () => {
      const t = thread([
        turn("t1", [
          item({
            id: "c1",
            type: "commandExecution",
            toolName: "shell",
            status: "completed",
          }),
          item({
            id: "c2",
            type: "commandExecution",
            toolName: "grep",
            status: "completed",
          }),
        ]),
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["activity"]);
      const a = c.items[0];
      if (a?.kind === "activity") {
        expect(a.family).toBe("tool");
        expect(a.id).toBe("c1");
      }
    });
  });
});

it("retains every clustered activity member and preserves mixed failure state", () => {
  const projected = projectThread(
    thread([
      turn("t1", [
        item({
          id: "tool-a",
          type: "commandExecution",
          toolName: "shell",
          status: "completed",
          transcriptKey: "key-a",
          position: { entry: 2, item: 0 },
          outputImages: [{ source: "https://example.test/a.png" }],
        }),
        item({
          id: "tool-c",
          type: "commandExecution",
          toolName: "cat",
          status: "inProgress",
          transcriptKey: "key-c",
          position: { entry: 2, item: 2 },
        }),
        item({
          id: "tool-b",
          type: "commandExecution",
          toolName: "grep",
          status: "completed",
          error: "failed",
          transcriptKey: "key-b",
          position: { entry: 2, item: 1 },
        }),
      ]),
    ]),
  );
  const activity = projected.items.find((value) => value.kind === "activity");
  expect(activity?.kind).toBe("activity");
  if (activity?.kind !== "activity") return;
  expect(activity.state).toBe("running");
  expect(activity.members?.map((member) => member.id)).toEqual([
    "tool-a",
    "tool-c",
  ]);
  expect(activity.members?.map((member) => member.position?.item)).toEqual([
    0, 2,
  ]);
  const failed = projected.items.find(
    (value) => value.kind === "activity" && value.id === "tool-b",
  );
  expect(failed).toMatchObject({ kind: "activity", state: "failed" });
  expect(projected.items).toContainEqual(
    expect.objectContaining({
      kind: "attachments",
      id: "tool-a:attachments",
      sourceTranscriptKey: "key-a",
      items: [
        expect.objectContaining({
          id: "tool-a:out:0",
          src: "https://example.test/a.png",
        }),
      ],
    }),
  );
});

it("retains typed system event and sourced hook exit metadata", () => {
  const projected = projectThread(
    thread([
      turn("t1", [
        item({
          id: "hook-1",
          type: "systemMessage",
          eventKind: "hook_completed",
          exitCode: 7,
          text: "hook failed",
        }),
      ]),
    ]),
  );
  const notice = projected.items[0];
  expect(notice).toMatchObject({
    kind: "notice",
    eventKind: "hook_completed",
    exitCode: 7,
  });
});

it("retains distinct tool intent inside compact activity members", () => {
  const projected = projectThread(
    thread([
      turn("t", [
        item({
          id: "a",
          type: "commandExecution",
          toolName: "shell",
          description: "Inspect source",
          status: "completed",
        }),
        item({
          id: "b",
          type: "commandExecution",
          toolName: "shell",
          description: "Run checks",
          status: "completed",
        }),
      ]),
    ]),
  );
  const activity = projected.items.find((value) => value.kind === "activity");
  expect(
    activity?.kind === "activity"
      ? activity.members?.map((member) => member.detail.description)
      : [],
  ).toEqual(["Inspect source", "Run checks"]);
});

it("keeps failures of unknown activity types individually visible", () => {
  const projected = projectThread(
    thread([
      turn("t", [
        item({
          id: "a",
          type: "futureTool",
          error: "first failure",
          status: "failed",
        }),
        item({
          id: "b",
          type: "futureTool",
          error: "second failure",
          status: "failed",
        }),
      ]),
    ]),
  );
  expect(
    projected.items
      .filter((value) => value.kind === "activity")
      .map((value) => value.id),
  ).toEqual(["a", "b"]);
});

// --- what the live model carries into the rows (D23c-2b) ---------------------

it("projects a hand-stamped warning item as the live attention row", () => {
  // A warning only ever reaches the model through the reducer's own
  // `case "warning"` fold, which stamps ItemModel.warning beside the text —
  // warnings are not transcript-persisted, so no snapshot carries one. This
  // fixture hand-stamps the untyped warning map, exercising warningItem's
  // direct model reading beside the live-fold fixtures in the cluster above.
  const model = hydrateThread(
    { thread: thread([turn("t", [item({ id: "item_warning_live_t_0", type: "warning", text: "Retrying the provider", status: "completed" })])]) },
    "ref-1",
    0,
  );
  const warning = model.turns[0]?.items[0];
  if (!warning) throw new Error("expected the warning item");
  warning.warning = { title: "Provider warning", hint: "attempt 2 of 3" };
  expect(projectConversation(model).items).toMatchObject([
    {
      kind: "failure",
      id: "item_warning_live_t_0",
      title: "Provider warning",
      detail: "Retrying the provider — attempt 2 of 3",
    },
  ]);
});

// A warning with nothing in it is not a row. The web's own warning renderer
// returns null for exactly this case (WarningItem: no title, no text, no
// hint), and a blank critical notice on the phone is a red herring with no
// content to explain itself.
it("drops a warning that carries nothing at all", () => {
  const projected = projectThread(
    thread([turn("t", [item({ id: "w", type: "warning", text: "", status: "completed" })])]),
  );
  expect(projected.items).toEqual([]);
});

// Whitespace title/hint join into nothing (they never reach the raw-frame
// fallback below), but a whitespace-only `message` is itself a message-less
// frame by the reducer's own EffectiveMessage-equivalent (reducer.ts's
// warningMessage: trims to "", same as absent) — so the reducer already
// folded item.text to the bounded raw frame (rawWarningFrame) before this
// projection ever sees it, and the row renders that, not nothing. The title
// and hint reach the model only through the reducer's own live `warning`
// fold, so the fixture folds one.
it.each([
  ["a whitespace title", { title: "   " }],
  ["a whitespace hint", { hint: "\n\t" }],
  ["whitespace everywhere", { title: " ", hint: "  " }],
])("renders the raw frame for a live warning whose message/title/hint are all whitespace (%s)", (_case, warning) => {
  const params = { threadId: "thread-1", ref: "ref-1", message: "  ", ...warning };
  const model = applyNotification(
    hydrateThread(
      {
        thread: thread([turn("t", [], { status: "inProgress" })], {
          evener: evenerThread({ activeTurnId: "t" }),
        }),
      },
      "ref-1",
      0,
    ),
    { method: "warning", params } as never,
    1000,
  );
  expect(projectConversation(model).items).toEqual([
    expect.objectContaining({ kind: "failure", title: "Warning", detail: JSON.stringify(params) }),
  ]);
});

it("carries a warning that has no title of its own", () => {
  const projected = projectThread(
    thread([
      turn("t", [
        item({ id: "w", type: "warning", text: "something happened", status: "completed" }),
      ]),
    ]),
  );
  expect(projected.items).toMatchObject([
    { kind: "failure", title: "Warning", detail: "something happened" },
  ]);
});

it("reads a reasoning row from the per-summaryIndex chunks the model keeps", () => {
  const model = hydrateThread(
    { thread: thread([turn("t", [item({ id: "r", type: "reasoning", text: "seed", status: "inProgress" })], { status: "inProgress" })]) },
    "ref-1",
    0,
  );
  const reasoning = model.turns[0]?.items[0];
  if (!reasoning) throw new Error("expected the reasoning item");
  // The reducer accumulates one chunk list per summary index; the row joins
  // them in order, one paragraph each.
  reasoning.reasoningSummaries = [["first ", "thought"], ["second thought"]];
  expect(projectConversation(model).items).toMatchObject([
    {
      kind: "activity",
      family: "reasoning",
      state: "running",
      detail: { output: "first thought\n\nsecond thought" },
    },
  ]);
});

it("falls back to a reasoning item's own text when no chunks were kept", () => {
  const projected = projectThread(
    thread([
      turn("t", [item({ id: "r", type: "reasoning", status: "completed" })]),
    ]),
  );
  expect(projected.items).toMatchObject([
    { kind: "activity", family: "reasoning", detail: { output: "" } },
  ]);
});

// A question row on screen and the wire's askPending must agree: #1731 round
// 4 made ThreadModel.askPending the single source for "is anything pending",
// and liveAskQuestions (which askQuestionsByCall/liveAsksFor call) now gates
// its whole item scan on it, so an answerable ask_user call in the
// transcript renders as a question row if and only if askPending is true.
it("a question row on screen implies askPending is true — the wire is the single source", () => {
  const askArgs =
    '{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}';
  const askItem = item({
    id: "ask-1",
    type: "commandExecution",
    toolName: "ask_user",
    status: "completed",
    argumentsJson: askArgs,
  });
  const pending = projectThread(
    thread([turn("t", [askItem])], { evener: evenerThread({ askPending: true }) }),
  );
  expect(pending.items.some((row) => row.kind === "question")).toBe(true);
  expect(pending.askPending).toBe(true);

  // The identical transcript with the wire saying nothing is pending renders
  // no question row at all — the item scan never runs.
  const resolved = projectThread(
    thread([turn("t", [askItem])], { evener: evenerThread({ askPending: false }) }),
  );
  expect(resolved.items.some((row) => row.kind === "question")).toBe(false);
});

// liveAsksFor memoizes askQuestionsByCall's scan by model.turns alone (its
// own doc comment: "no memory of its own... reuses ONE scan for every
// caller that shares that exact array"). A frame that changes ONLY
// askPending (a status frame with no item of its own,
// conversation.ts's changesRows) hands back the SAME turns reference with a
// different askPending — the memo must not serve the stale answer it cached
// under the old flag. liveAsksFor reads only turns and askPending, so a
// minimal ThreadModel-shaped fixture stands in for the rest (this file's own
// thread()/turn() build the WIRE shape hydrateThread consumes, not this).
it("liveAsksFor re-derives when askPending changes even though turns did not", () => {
  const askArgs =
    '{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}';
  const turns: TurnModel[] = [
    {
      id: "t",
      status: "completed",
      items: [
        {
          id: "ask-1",
          turnId: "t",
          type: "commandExecution",
          toolName: "ask_user",
          status: "completed",
          callId: "call-1",
          argumentsJSON: askArgs,
        } as ItemModel,
      ],
    },
  ];
  const notAsking = { turns, askPending: false } as unknown as ThreadModel;
  expect(liveAsksFor(notAsking).size).toBe(0);
  // Same turns reference, askPending now true.
  const asking = { ...notAsking, askPending: true };
  expect(asking.turns).toBe(notAsking.turns);
  expect(liveAsksFor(asking).size).toBe(1);
  // And back to false again re-derives to empty rather than serving the
  // "asking" answer just cached under the same turns reference.
  const resolvedAgain = { ...notAsking, askPending: false };
  expect(liveAsksFor(resolvedAgain).size).toBe(0);
});

it("renders no question row when the ask arguments do not parse", () => {
  const projected = projectThread(
    thread(
      [
        turn("t", [
          item({
            id: "ask-bad",
            type: "commandExecution",
            toolName: "ask_user",
            status: "completed",
            argumentsJson: "{{not valid json",
          }),
        ]),
      ],
      { evener: evenerThread({ askPending: true }) },
    ),
  );
  expect(projected.items.some((row) => row.kind === "question")).toBe(false);
});

// --- rowsForTurn's cache (turnRowCache), directly: model-level fixtures ------
// (TurnModel/ItemModel, not the wire-level Thread fixtures the rest of this
// file uses) because the point is object IDENTITY across two projectTimeline
// calls, which hydrateThread never preserves — every hydrate call builds
// fresh turn objects. Local builders, not shared, mirroring
// deriveAskQuestions.test.ts's own precedent for this same reason.

describe("rowsForTurn's per-turn cache", () => {
  function askItem(callId: string): ItemModel {
    return {
      id: `item_${callId}`,
      turnId: "t1",
      type: "commandExecution",
      toolName: "ask_user",
      callId,
      status: "completed",
      argumentsJSON: JSON.stringify({
        questions: [{ header: "H", question: "Q", options: [{ label: "A", detail: "d" }] }],
      }),
    } as ItemModel;
  }

  function askRefs(callId: string): AskQuestionRef[] {
    return [
      {
        key: `${callId}:0`,
        callId,
        header: "H",
        question: "Q",
        options: [{ label: "A", detail: "d" }],
        multiSelect: false,
      },
    ];
  }

  // The cache is keyed on the turn object (a WeakMap), but a turn's own
  // question row depends on `asks` too — whether ITS OWN ask_user calls are
  // still answerable (project.ts's TurnRows.askState comment: "the calls this
  // turn consumed and whether they were answerable, so the rows are reused
  // only while that still holds"). Two projectTimeline calls over the SAME
  // turn object, with call_1 answerable the first time and resolved the
  // second, must not reuse the first call's question row.
  it("does not reuse a cached question row once the turn's own ask becomes resolved", () => {
    const sharedTurn: TurnModel = { id: "t1", status: "completed", items: [askItem("call_1")] } as TurnModel;

    const answerable = new Map<string, AskQuestionRef[]>([["call_1", askRefs("call_1")]]);
    const firstPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, answerable);
    expect(firstPass.some((row) => row.kind === "question")).toBe(true);

    const resolved = new Map<string, AskQuestionRef[]>(); // call_1 no longer answerable
    const secondPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, resolved);
    expect(secondPass.some((row) => row.kind === "question")).toBe(false);
  });

  // The inverse: a genuinely unrelated `asks` map (no entry the turn's own
  // askState even names) is exactly the case the cache SHOULD reuse for —
  // measured here so the cache's own hit path stays covered, not just its
  // invalidation path.
  it("reuses the cached row set when the turn's own asks are unaffected", () => {
    const sharedTurn: TurnModel = { id: "t1", status: "completed", items: [askItem("call_1")] } as TurnModel;

    const asksA = new Map<string, AskQuestionRef[]>([["call_1", askRefs("call_1")]]);
    const firstPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asksA);

    // A different Map instance, unrelated call included, but call_1 reads
    // exactly the same as before — the cache must still answer "answerable".
    const asksB = new Map<string, AskQuestionRef[]>([
      ["call_1", askRefs("call_1")],
      ["call_unrelated", askRefs("call_unrelated")],
    ]);
    const secondPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asksB);
    expect(secondPass).toEqual(firstPass);
  });
});
