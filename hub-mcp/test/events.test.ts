import assert from "node:assert/strict";
import { test } from "node:test";

import type { AnyNotification } from "@evener/appwire-client";

import { ActivityWatcher, classify } from "../src/events.js";
import { FakeHub } from "./fakeHub.js";

function note(method: string, params: Record<string, unknown>): AnyNotification {
  return { method, params } as unknown as AnyNotification;
}

/** epochOf splits an "<epoch>:<seq>" cursor so tests can rebuild same-epoch cursors. */
function epochOf(cursor: string): string {
  const epoch = cursor.split(":")[0];
  assert.ok(epoch, `cursor ${cursor} must be <epoch>:<seq>`);
  return epoch;
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

// AttentionEntry.ID on the wire is the BARE session id (hubcore fills it from
// the session id, no "local:" prefix), so classify must normalize it to the
// ref form every other notification carries — or the attention group can
// never match a wait keyed on local:<id>.
test("attention changes normalize their bare session ids to refs", () => {
  const events = classify(
    note("evener/attention/changed", {
      changed: [
        { threadId: "a", title: "parser fix", level: "needs_you", prevLevel: "working", askPending: true },
        { threadId: "b", title: "docs", level: "working", prevLevel: "working" },
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
  const second = events[1];
  assert.ok(second);
  assert.equal(second.ref, "local:b");
});

test("attention events match a wait keyed on the local: ref", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: { id: "local:a", evener: { ref: "local:a", capabilities: {}, queue: {} } } }));
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  await watcher.readAndSubscribe("local:a");
  const pending = watcher.wait({ refs: ["local:a"], timeoutMs: 2000 });
  hub.emit(
    note("evener/attention/changed", {
      changed: [{ threadId: "a", title: "parser fix", level: "needs_you", prevLevel: "working" }],
      summary: { needsYou: 1, error: 0, working: 0 },
    }),
  );
  const result = await pending;
  assert.equal(result.events.length, 1);
  assert.equal(result.events[0]?.group, "attention");
});

test("a watcher buffers events and wait returns them by cursor", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  const first = watcher.wait({ timeoutMs: 0 });
  assert.equal((await first).events.length, 0);
  assert.match((await first).cursor, /:0$/);

  hub.emit(note("turn/started", { ref: "local:a" }));
  hub.emit(note("thread/status/changed", { ref: "local:b", status: { type: "idle" } }));
  hub.emit(note("item/agentMessage/delta", { ref: "local:a", delta: "noise" }));

  const result = await watcher.wait({ since: (await first).cursor, timeoutMs: 10 });
  assert.equal(result.events.length, 2);
  assert.equal(result.timedOut, false);
  assert.equal(result.cursor, `${epochOf((await first).cursor)}:2`);

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

// A restart of the hub MCP server is a new process, so its seq counter starts
// over. The cursor therefore names its own epoch; a since from any other epoch
// (or the bare-number format an older process printed) means the stream
// restarted and events since that cursor were lost — the wait starts from now
// and says so.
test("a cursor from a foreign epoch starts from now and reports the restart", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  hub.emit(note("turn/started", { ref: "local:a" }));
  const epoch = epochOf(watcher.cursor());

  const same = await watcher.wait({ since: `${epoch}:0`, timeoutMs: 0 });
  assert.equal(same.events.length, 1);
  assert.equal(same.restarted, false);

  const foreign = await watcher.wait({ since: "some-old-epoch:0", timeoutMs: 0 });
  assert.equal(foreign.events.length, 0);
  assert.equal(foreign.restarted, true);
  assert.equal(foreign.cursor, watcher.cursor());

  const legacy = await watcher.wait({ since: "15", timeoutMs: 0 });
  assert.equal(legacy.events.length, 0);
  assert.equal(legacy.restarted, true);
});

// collect() keeps only the newest `limit` matches; when older matches were
// dropped the result must count them instead of pretending nothing else
// happened.
test("matches past the limit are counted as skipped", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  for (let i = 0; i < 4; i++) hub.emit(note("turn/started", { ref: "local:a" }));
  const epoch = epochOf(watcher.cursor());
  const result = await watcher.wait({ since: `${epoch}:0`, limit: 2, timeoutMs: 0 });
  assert.equal(result.events.length, 2);
  assert.equal(result.events[0]?.seq, 3);
  assert.equal(result.events[1]?.seq, 4);
  assert.equal(result.skipped, 2);
  assert.equal(result.restarted, false);
});

// The ring buffer evicts the oldest events under load. A same-epoch cursor
// older than the oldest retained event may have had matching events evicted
// in between — the wait must disclose that instead of answering "all quiet".
test("a same-epoch cursor older than the buffer is flagged as possibly evicted", async () => {
  const hub = new FakeHub();
  const watcher = new ActivityWatcher(hub, { capacity: 3 });
  watcher.start();
  for (let i = 0; i < 5; i++) hub.emit(note("turn/started", { ref: "local:a" }));
  const epoch = epochOf(watcher.cursor());

  const stale = await watcher.wait({ since: `${epoch}:0`, limit: 10, timeoutMs: 0 });
  assert.equal(stale.events.length, 3);
  assert.equal(stale.evicted, true);

  const fresh = await watcher.wait({ since: `${epoch}:4`, limit: 10, timeoutMs: 0 });
  assert.equal(fresh.events.length, 1);
  assert.equal(fresh.evicted, false);
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
