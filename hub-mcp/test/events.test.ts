import assert from "node:assert/strict";
import { test } from "node:test";

import type { AnyNotification } from "@evener/appwire-client";

import { ActivityWatcher, classify } from "../src/events.js";
import { FakeHub } from "./fakeHub.js";

function note(method: string, params: Record<string, unknown>): AnyNotification {
  return { method, params } as unknown as AnyNotification;
}

test("turn completion carries status, duration, cost, and failure", () => {
  const events = classify(
    note("turn/completed", {
      ref: "local:a",
      turn: { status: "completed", durationMs: 61_000, cost: "~$0.12", error: undefined },
    }),
  );
  const event = events[0];
  assert.ok(event);
  assert.equal(event.ref, "local:a");
  assert.equal(event.group, "turns");
  assert.match(event.summary, /turn completed \(completed, 1m01s, ~\$0\.12\)/);
  const failedEvents = classify(
    note("turn/completed", { ref: "local:a", turn: { status: "failed", error: { message: "provider unhealthy" } } }),
  );
  const failed = failedEvents[0];
  assert.ok(failed);
  assert.match(failed.summary, /failed: provider unhealthy/);
});

test("token deltas are never events", () => {
  assert.equal(classify(note("item/agentMessage/delta", { ref: "local:a", delta: "x" })).length, 0);
  assert.equal(classify(note("item/toolOutput/delta", { ref: "local:a", delta: "x" })).length, 0);
});

test("attention changes fan out one event per changed session", () => {
  const events = classify(
    note("evener/attention/changed", {
      changed: [
        { threadId: "local:a", title: "parser fix", level: "needs_you", prevLevel: "working", askPending: true },
        { threadId: "local:b", title: "docs", level: "working", prevLevel: "working" },
      ],
      summary: { needsYou: 1, error: 0, working: 1 },
    }),
  );
  assert.equal(events.length, 2);
  const first = events[0];
  assert.ok(first);
  assert.equal(first.ref, "local:a");
  assert.match(first.summary, /working → needs_you: parser fix \(waiting on a human answer\)/);
  assert.equal(first.group, "attention");
});

test("a watcher buffers events and wait returns them by cursor", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  const first = watcher.wait({ timeoutMs: 0 });
  assert.equal((await first).events.length, 0);

  hub.emit(note("turn/started", { ref: "local:a" }));
  hub.emit(note("thread/status/changed", { ref: "local:b", status: { type: "idle" } }));
  hub.emit(note("item/agentMessage/delta", { ref: "local:a", delta: "noise" }));

  const result = await watcher.wait({ since: 0, timeoutMs: 10 });
  assert.equal(result.events.length, 2);
  assert.equal(result.timedOut, false);
  assert.equal(result.cursor, 2);

  const next = await watcher.wait({ since: result.cursor, timeoutMs: 1 });
  assert.equal(next.events.length, 0);
  assert.equal(next.timedOut, true);
});

test("wait blocks until a matching event arrives, filtered by ref and group", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  const pending = watcher.wait({ refs: ["local:a"], groups: ["turns"], timeoutMs: 2000 });
  hub.emit(note("thread/status/changed", { ref: "local:b", status: { type: "idle" } }));
  hub.emit(note("thread/queueChanged", { ref: "local:a", queue: { depth: 1, preview: ["hi"] } }));
  hub.emit(note("turn/started", { ref: "local:a" }));
  const result = await pending;
  assert.equal(result.events.length, 1);
  const first = result.events[0];
  assert.ok(first);
  assert.equal(first.method, "turn/started");
});

test("readAndSubscribe subscribes and the ready hook resubscribes after a reconnect", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: { id: "local:a", evener: { ref: "local:a", capabilities: {}, queue: {} } } }));
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  await watcher.readAndSubscribe("local:a");
  assert.deepEqual(watcher.subscribedRefs(), ["local:a"]);

  hub.fireReady();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(hub.callsOf("thread/read").length, 2, "ready must re-issue the subscription read");
});
