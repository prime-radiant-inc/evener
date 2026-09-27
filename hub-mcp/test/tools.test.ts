import assert from "node:assert/strict";
import { test } from "node:test";

import { ActivityWatcher } from "../src/events.js";
import { TOOLS, type ToolContext } from "../src/tools.js";
import { FakeHub } from "./fakeHub.js";
import { makeThread } from "./sessions.test.js";

function ctxFor(hub: FakeHub): ToolContext {
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  return {
    port: hub,
    watcher,
    config: { url: "ws://fake/rpc", token: "t", tokenSource: "test" },
    shutdown: new AbortController(),
  };
}

test("list_sessions renders one row per session and reports the cursor", async () => {
  const hub = new FakeHub();
  hub.on("thread/list", () => ({
    data: [makeThread(), makeThread({ id: "thread-b", evener: { ...makeThread().evener, ref: "local:b" } })],
  }));
  const out = await TOOLS.list_sessions.run(ctxFor(hub), {});
  assert.match(out, /local:a \| idle/);
  assert.match(out, /local:b \| idle/);
  assert.match(out, /activity_cursor: 0/);
});

test("list_sessions says so, and suggests search, when nothing matches", async () => {
  const hub = new FakeHub();
  hub.on("thread/list", () => ({ data: [] }));
  const out = await TOOLS.list_sessions.run(ctxFor(hub), { search: "parser" });
  assert.match(out, /no sessions match the search "parser"/);
  assert.match(out, /search_sessions/);
});

test("start_session sends the prompt as its first input and subscribes to the new session", async () => {
  const hub = new FakeHub();
  const spawned = makeThread({ id: "thread-new", evener: { ...makeThread().evener, ref: "local:new" } });
  hub.on("thread/start", () => ({ thread: spawned, turn: { id: "turn_1", itemsView: "full", status: "in_progress" } }));
  hub.on("thread/read", () => ({ thread: spawned }));
  hub.on("evener/thread/name/set", () => ({}));
  const out = await TOOLS.start_session.run(ctxFor(hub), {
    cwd: "/tmp/project",
    prompt: "write the parser tests",
    name: "parser-tests",
    max_subagent_depth: 0,
  });
  assert.match(out, /started local:new and started its first turn/);
  const [start] = hub.callsOf("thread/start");
  assert.ok(start);
  assert.equal(start.cwd, "/tmp/project");
  assert.deepEqual(start.input, [{ type: "text", text: "write the parser tests" }]);
  assert.deepEqual(start.launchOverrides, { maxSubagentDepth: 0 });
  const [naming] = hub.callsOf("evener/thread/name/set");
  assert.ok(naming);
  assert.deepEqual(naming, { ref: "local:new", name: "parser-tests" });
  assert.ok(
    hub.calls.some((c) => c.method === "thread/read"),
    "the new session is subscribed",
  );
});

test("start_session without a prompt reports a dormant session", async () => {
  const hub = new FakeHub();
  hub.on("thread/start", () => ({ thread: makeThread({ evener: { ...makeThread().evener, ref: "local:dormant" } }) }));
  hub.on("thread/read", () => ({ thread: makeThread() }));
  const out = await TOOLS.start_session.run(ctxFor(hub), { cwd: "/tmp/p" });
  assert.match(out, /started local:dormant \(dormant: no prompt, so nothing runs until you send_message\)/);
});

test("wait_for_activity subscribes named refs and returns their events", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  const ctx = ctxFor(hub);
  const waiting = TOOLS.wait_for_activity.run(ctx, { refs: ["local:a"], timeout_seconds: 5 });
  hub.emit({ method: "turn/completed", params: { ref: "local:a", turn: { status: "completed" } } } as never);
  const out = await waiting;
  assert.match(out, /\[turns\] local:a: turn completed/);
  assert.match(out, /activity_cursor: 1/);
});

test("wait_for_activity times out honestly with guidance", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  const out = await TOOLS.wait_for_activity.run(ctxFor(hub), { refs: ["local:a"], timeout_seconds: 1 });
  assert.match(out, /no matching activity/);
  assert.match(out, /get_session shows whether the session is idle, errored, or waiting on a human/);
});

test("interrupt refuses sessions that are not running a turn", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({
      status: { type: "idle" },
      evener: { ...makeThread().evener, capabilities: { ...makeThread().evener.capabilities, interrupt: false } },
    }),
  }));
  try {
    await TOOLS.interrupt_session.run(ctxFor(hub), { ref: "local:a" });
    assert.fail("expected a refusal");
  } catch (err) {
    assert.match(err instanceof Error ? err.message : String(err), /cannot be interrupted right now \(state idle\)/);
  }
});

test("stop_session escalates to forceStop only when asked", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  hub.on("thread/shutdown", () => ({}));
  await TOOLS.stop_session.run(ctxFor(hub), { ref: "local:a" });
  assert.equal(hub.callsOf("thread/shutdown").length, 1);
  assert.equal(hub.callsOf("evener/thread/forceStop").length, 0);

  hub.on("evener/thread/forceStop", () => ({}));
  await TOOLS.stop_session.run(ctxFor(hub), { ref: "local:a", force: true });
  assert.equal(hub.callsOf("evener/thread/forceStop").length, 1);
});

test("list_tasks renders the task rows with statuses", async () => {
  const hub = new FakeHub();
  hub.on("evener/tasks/list", () => ({
    data: [
      { id: 1, type: "implement", description: "write the parser", prompt: "write it", status: "done" },
      { id: 2, type: "verify", description: "run make test", prompt: "run it", status: "in_progress", depends_on: [1] },
    ],
  }));
  const out = await TOOLS.list_tasks.run(ctxFor(hub), { ref: "local:a" });
  assert.match(out, /local:a — 1\/2 done/);
  assert.match(out, /1\. \[done\] write the parser/);
  assert.match(out, /2\. \[in_progress\] run make test \(after 1\)/);
});

test("search_sessions splits live and past results", async () => {
  const hub = new FakeHub();
  hub.on("evener/search", () => ({
    live: [{ id: "a", title: "parser fix", project: "evener", state: "active", age: "2m ago", ref: "local:a" }],
    past: [{ id: "b", title: "old parser work", project: "evener", state: "ended", age: "3d ago", ref: "local:b" }],
  }));
  const out = await TOOLS.search_sessions.run(ctxFor(hub), { query: "parser" });
  assert.match(out, /live: local:a \| active \| parser fix/);
  assert.match(out, /past: local:b \| ended \| old parser work/);
});
