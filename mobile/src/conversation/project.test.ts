// Fixture-driven tests for the AppWire-to-mobile thread projection.
// Every fixture is a literal Thread wire object; no network, no provider
// credentials, no ambient state. These exercise projectThread's item
// classification, clustering, capability/queue/usage projection, and the
// forward-compatibility rule (unknown item types must NOT disappear).

import { describe, expect, it } from "vitest";
import type {
  EvenerThread,
  InputItem,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  ActivityDetail,
  ActivityFamily,
  MobileConversation,
  MobileTimelineItem,
} from "./model";
import { projectThread } from "./project";

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS: ThreadCapabilities = {
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

describe("projectThread", () => {
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

    it("projects an agentMessage with a delta as streaming while incomplete", () => {
      const t = thread([
        turn(
          "t1",
          [item({ id: "a1", type: "agentMessage", delta: "partial" })],
          { status: "running" },
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

    it("joins text and delta into markdown when both present", () => {
      const t = thread([
        turn(
          "t1",
          [
            item({
              id: "a1",
              type: "agentMessage",
              text: "final text",
              delta: "streaming tail",
            }),
          ],
          { status: "running" },
        ),
      ]);
      const c = projectThread(t);
      const a = c.items[0];
      expect(a?.kind).toBe("assistant");
      if (a?.kind === "assistant") {
        expect(a.markdown).toBe("final textstreaming tail");
        expect(a.streaming).toBe(true);
      }
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
      ]);
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
      ]);
      const c = projectThread(t);
      expect(kinds(c)).toEqual(["user", "question"]);
      const q = c.items.find((i) => i.kind === "question");
      if (q?.kind === "question") {
        expect(q.batch.callId).toBe("call-ask");
        expect(q.batch.questions).toHaveLength(2);
        expect(q.batch.questions[0]?.key).toBe("call-ask:0");
        expect(q.batch.questions[1]?.key).toBe("call-ask:1");
        expect(q.batch.questions[0]?.header).toBe("Direction");
        expect(q.batch.questions[0]?.multiSelect).toBe(false);
        expect(q.batch.questions[0]?.options[0]?.recommended).toBe(true);
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
      ]);
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
      ]);
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
  });

  describe("capability projection", () => {
    it("maps ThreadCapabilities booleans 1:1 to MobileCapabilities", () => {
      const caps: ThreadCapabilities = {
        send: true,
        steer: false,
        interrupt: true,
        compact: false,
        clear: true,
        forkFromTurn: false,
        shutdown: true,
        changeModel: true,
        queue: false,
        goal: true,
        rename: false,
      };
      const t = thread([], { evener: evenerThread({ capabilities: caps }) });
      const c = projectThread(t);
      expect(c.capabilities).toEqual(caps);
    });
  });

  describe("queue state", () => {
    it("projects an empty queue as depth 0", () => {
      const t = thread([]);
      const c = projectThread(t);
      expect(c.queue).toEqual({ depth: 0, preview: [] });
    });

    it("projects queue depth and previews", () => {
      const queue: QueueState = {
        revision: 3,
        depth: 2,
        texts: ["first queued", "second queued"],
        preview: ["first queued"],
      };
      const t = thread([], { evener: evenerThread({ queue }) });
      const c = projectThread(t);
      expect(c.queue).toEqual({ depth: 2, preview: ["first queued"] });
    });

    it("falls back to texts when preview absent", () => {
      const queue: QueueState = {
        revision: 1,
        depth: 2,
        texts: ["a", "b"],
      };
      const t = thread([], { evener: evenerThread({ queue }) });
      const c = projectThread(t);
      expect(c.queue.depth).toBe(2);
      expect(c.queue.preview).toEqual(["a", "b"]);
    });
  });

  describe("usage projection", () => {
    it("projects usage from EvenerThread", () => {
      const t = thread([], {
        evener: evenerThread({
          usage: { inputTokens: 100, outputTokens: 200, totalTokens: 300 },
          cost: "0.05",
          contextUsed: 5000,
          contextWindow: 200000,
          contextRemaining: 195000,
          contextPressure: 0.025,
        }),
      });
      const c = projectThread(t);
      expect(c.usage).toEqual({
        inputTokens: 100,
        outputTokens: 200,
        cacheReadTokens: undefined,
        totalTokens: 300,
        cost: "0.05",
        contextUsed: 5000,
        contextWindow: 200000,
        contextRemaining: 195000,
        contextPressure: 0.025,
      });
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
      expect(c.supportsReasoning).toBe(true);
    });
  });

  describe("profile / session identity", () => {
    it("carries thread id, sessionId, name, preview, modelProvider, status", () => {
      const t = thread(
        [turn("t1", [item({ id: "u1", type: "userMessage", text: "hi" })])],
        {
          id: "thread-42",
          sessionId: "sess-9",
          name: "My session",
          preview: "hi",
          modelProvider: "openai",
          status: { type: "running", activeFlags: ["generating"] },
        },
      );
      const c = projectThread(t);
      expect(c.id).toBe("thread-42");
      expect(c.sessionId).toBe("sess-9");
      expect(c.name).toBe("My session");
      expect(c.preview).toBe("hi");
      expect(c.modelProvider).toBe("openai");
      expect(c.status).toBe("running");
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
