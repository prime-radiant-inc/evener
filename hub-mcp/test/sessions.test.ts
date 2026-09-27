import assert from "node:assert/strict";
import { test } from "node:test";

import type { Thread } from "@evener/appwire-client";

import { normalizeRef, sessionDetail, sessionRow } from "../src/sessions.js";

const NOW = 1_800_000_000_000;

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

test("detail names what you may do and flags resume-required sessions", () => {
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
  assert.match(detail, /resume required before actions/);
  assert.match(detail, /you can: queue, interrupt, clear-queue, compact, fork, change-model, stop, rename, set-goal/);
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
