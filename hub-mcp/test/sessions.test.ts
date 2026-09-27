import assert from "node:assert/strict";
import { test } from "node:test";

import type { Thread } from "@evener/appwire-client";

import { normalizeRef, pathInside, sessionDetail, sessionRow, threadInScope } from "../src/sessions.js";

const NOW = 1_800_000_000_000;
const SCOPE = "/home/jesse/git/evener";

export function makeThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thread-a",
    sessionId: "a",
    preview: "fix the parser",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet",
    // Thread wire timestamps are epoch seconds (hubcore.UnixSeconds).
    createdAt: (NOW - 3600_000) / 1000,
    updatedAt: (NOW - 120_000) / 1000,
    status: { type: "idle" },
    cwd: "/home/jesse/git/evener",
    projectPath: "/home/jesse/git/evener",
    cliVersion: "test",
    source: "local",
    evener: {
      ref: "local:a",
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: false,
        queue: true,
        goal: true,
        sharedNotes: false,
        rename: true,
      },
      queue: { revision: 1, depth: 0 },
    },
    ...overrides,
  };
}

test("normalizeRef assumes the local source for bare session ids", () => {
  assert.equal(normalizeRef("01ABC"), "local:01ABC");
  assert.equal(normalizeRef(" local:01ABC "), "local:01ABC");
  assert.equal(normalizeRef("host:h:01"), "host:h:01");
});

test("pathInside requires containment, never a shared prefix", () => {
  assert.ok(pathInside(SCOPE, SCOPE));
  assert.ok(pathInside(SCOPE, `${SCOPE}/`));
  assert.ok(pathInside(SCOPE, `${SCOPE}/worktrees/w1`));
  assert.ok(!pathInside(SCOPE, "/home/jesse/git/evener-other"));
  assert.ok(!pathInside(SCOPE, "/elsewhere"));
});

test("threadInScope requires every path the thread names to be inside the scope", () => {
  assert.ok(threadInScope(makeThread(), SCOPE));
  assert.ok(
    threadInScope(makeThread({ cwd: `${SCOPE}/worktrees/w1`, projectPath: SCOPE }), SCOPE),
    "a linked worktree inside the project is in scope",
  );
  assert.ok(!threadInScope(makeThread({ cwd: "/elsewhere/repo" }), SCOPE), "cwd outside puts the session out");
  assert.ok(
    !threadInScope(makeThread({ cwd: SCOPE, projectPath: "/elsewhere/repo" }), SCOPE),
    "projectPath outside puts the session out even when the cwd is inside",
  );
});

test("a session row carries ref, state, name, project, model, and age", () => {
  const row = sessionRow(makeThread(), NOW);
  assert.match(row, /local:a \| idle \| fix the parser/);
  assert.match(row, /\/home\/jesse\/git\/evener/);
  assert.match(row, /anthropic\/claude-sonnet/);
  assert.match(row, /2m ago/);
});

test("queue depth and task progress surface in the row", () => {
  const row = sessionRow(
    makeThread({
      name: "worker",
      status: { type: "active", activeFlags: ["askPending"] },
      evener: {
        ...makeThread().evener,
        queue: { revision: 2, depth: 2 },
        tasks: { total: 4, done: 1 },
      },
    }),
    NOW,
  );
  assert.match(row, /active \[askPending\]/);
  assert.match(row, /\| worker, queue 2, tasks 1\/4/);
});

test("detail names only what this MCP can do, lists UI-only moves, and flags resume-required sessions", () => {
  const detail = sessionDetail(
    makeThread({
      evener: {
        ...makeThread().evener,
        resumeRequired: true,
        capabilities: { ...makeThread().evener.capabilities, send: false, steer: false },
      },
    }),
    NOW,
  );
  assert.match(detail, /resume required: resume it with resume_session/);
  assert.match(detail, /you can: resume, queue, interrupt, clear-queue, stop, rename/);
  assert.doesNotMatch(detail, /you can: [^\n]*compact/);
  assert.match(detail, /hub UI only: compact, fork, change-model, set-goal/);
});

test("you can: lists rename only when the capability is set, and resume only when required", () => {
  const plain = sessionDetail(makeThread(), NOW);
  assert.match(plain, /you can: send, steer, queue, interrupt, clear-queue, stop, rename/);
  assert.doesNotMatch(plain, /resume/, "a session not waiting on a resume does not offer one");

  const noRename = sessionDetail(
    makeThread({
      evener: { ...makeThread().evener, capabilities: { ...makeThread().evener.capabilities, rename: false } },
    }),
    NOW,
  );
  assert.doesNotMatch(noRename, /you can: [^\n]*rename/);
});

test("detail carries failed tool calls and ask-pending, and nil failedToolCalls renders nothing", () => {
  const flagged = sessionDetail(
    makeThread({
      evener: { ...makeThread().evener, failedToolCalls: 3, askPending: true },
    }),
    NOW,
  );
  assert.match(flagged, /failed tool calls: 3/);
  assert.match(flagged, /waiting on a human answer/);

  const counted = sessionDetail(makeThread({ evener: { ...makeThread().evener, failedToolCalls: 0 } }), NOW);
  assert.match(counted, /failed tool calls: 0/, "a real zero is good news, not unknown");

  const unknown = sessionDetail(makeThread(), NOW);
  assert.doesNotMatch(unknown, /failed tool calls/, "nil means nobody counted — never render a fabricated 0");
});

test("the row flags ask-pending sessions", () => {
  const row = sessionRow(makeThread({ evener: { ...makeThread().evener, askPending: true } }), NOW);
  assert.match(row, /, ask pending/);
});

test("detail reports context pressure and cost when present", () => {
  const detail = sessionDetail(
    makeThread({
      evener: {
        ...makeThread().evener,
        contextUsed: 90_000,
        contextWindow: 200_000,
        usage: { inputTokens: 90_000, outputTokens: 4_000, cacheReadTokens: 210_000 },
        cost: "~$1.40",
        turnCount: 12,
      },
    }),
    NOW,
  );
  assert.match(detail, /context: 90k of 200k tokens \(45% used\)/);
  assert.match(detail, /turns completed: 12, in 90k, out 4k, cache 210k, cost ~\$1\.40/);
});
