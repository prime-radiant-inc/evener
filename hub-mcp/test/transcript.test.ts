import assert from "node:assert/strict";
import { test } from "node:test";

import type { ThreadItem, Turn } from "@evener/appwire-client";

import { renderTranscript } from "../src/transcript.js";

function turn(items: ThreadItem[], overrides: Partial<Turn> = {}): Turn {
  return { id: "turn_1", itemsView: "full", status: "completed", items, ...overrides };
}

test("outline renders messages and one line per tool call, hiding thinking", () => {
  const text = renderTranscript(
    [
      turn([
        { type: "userMessage", id: "i1", text: "ship it" },
        { type: "reasoning", id: "i2", text: "let me think" },
        { type: "agentMessage", id: "i3", text: "on it" },
        {
          type: "commandExecution",
          id: "i4",
          toolName: "shell",
          argumentsJson: '{"command":"git status"}',
          durationMs: 250,
        },
      ]),
    ],
    "outline",
  );
  assert.match(text, /turn turn_1 — completed/);
  assert.match(text, /user: ship it/);
  assert.match(text, /assistant: on it/);
  assert.doesNotMatch(text, /let me think/);
  assert.match(text, /tool: shell git status \(250ms\)/);
});

test("full detail keeps tool output and thinking, and failures name the error", () => {
  const text = renderTranscript(
    [
      turn(
        [
          {
            type: "commandExecution",
            id: "i1",
            toolName: "shell",
            argumentsJson: '{"command":"make test"}',
            output: "ok\nok",
            durationMs: 2_000,
          },
          {
            type: "commandExecution",
            id: "i2",
            toolName: "grep",
            argumentsJson: '{"pattern":"x"}',
            error: "no match",
            exitCode: 1,
          },
        ],
        { status: "failed", error: { message: "provider unhealthy after 2 stream failures", hint: "check key" } },
      ),
    ],
    "full",
  );
  assert.match(text, /output: ok\nok/);
  assert.match(text, /error: no match/);
  assert.match(text, /exit 1/);
  assert.match(text, /turn failed: provider unhealthy after 2 stream failures \(hint: check key\)/);
});

test("steering items distinguish human sends from daemon steering", () => {
  const text = renderTranscript(
    [
      turn([
        { type: "steering", id: "i1", source: "user", text: "stop and write a haiku" },
        { type: "steering", id: "i2", steeringKind: "task-nudge", text: "keep going" },
      ]),
    ],
    "outline",
  );
  assert.match(text, /user \(steer\): stop and write a haiku/);
  assert.match(text, /steering\/task-nudge: keep going/);
});

test("truncation is honest about what it cut", () => {
  const long = "x".repeat(700);
  const text = renderTranscript([turn([{ type: "agentMessage", id: "i1", text: long }])], "outline");
  assert.match(text, /\[\+\d+ chars\]/);
});
