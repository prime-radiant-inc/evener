// EVENER_HUB_MCP_PROJECT enforcement: a scoped server refuses, filters, and
// never echoes. Every ref-naming tool must refuse sessions outside the
// configured scope, naming the scope (the operator's own configuration) but
// never the out-of-scope session's paths (another project's data).

import assert from "node:assert/strict";
import { test } from "node:test";

import { ActivityWatcher } from "../src/events.js";
import type { ToolContext } from "../src/tools.js";
import { TOOLS } from "../src/tools.js";
import { FakeHub } from "./fakeHub.js";
import { makeThread } from "./sessions.test.js";

const SCOPE = "/home/jesse/git/evener";

function ctxFor(hub: FakeHub, projectScope?: string): ToolContext {
  const watcher = new ActivityWatcher(hub);
  watcher.start();
  return {
    port: hub,
    watcher,
    config: { url: "ws://fake/rpc", token: "t", tokenSource: "test", ...(projectScope ? { projectScope } : {}) },
    shutdown: new AbortController(),
  };
}

function outOfScopeThread(): ReturnType<typeof makeThread> {
  return makeThread({ cwd: "/elsewhere/repo", projectPath: "/elsewhere/repo" });
}

test("a scoped get_session refuses an out-of-scope ref, naming the scope but not the session's paths", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: outOfScopeThread() }));
  await assert.rejects(
    () => TOOLS.get_session.run(ctxFor(hub, SCOPE), { ref: "local:a" }),
    (err: unknown) => {
      const message = err instanceof Error ? err.message : String(err);
      assert.match(message, /local:a is outside the project scope this server is limited to/);
      assert.match(message, /\/home\/jesse\/git\/evener/, "the refusal names the configured scope");
      assert.doesNotMatch(message, /\/elsewhere/, "the session's own paths are withheld");
      return true;
    },
  );
});

test("a scoped get_session serves refs whose project and cwd lie inside the scope", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({ cwd: `${SCOPE}/worktrees/w1`, projectPath: SCOPE }),
  }));
  const out = await TOOLS.get_session.run(ctxFor(hub, SCOPE), { ref: "local:a" });
  assert.match(out, /local:a — /);
});

test("read_transcript scope-prechecks with a metadata read before any transcript work", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: outOfScopeThread() }));
  hub.on("thread/turns/list", () => ({ data: [] }));
  await assert.rejects(
    () => TOOLS.read_transcript.run(ctxFor(hub, SCOPE), { ref: "local:a" }),
    /outside the project scope/,
  );
  const reads = hub.callsOf("thread/read");
  assert.equal(reads.length, 1, "the precheck is exactly one read");
  assert.equal(reads[0]?.includeTurns, false, "the precheck is a metadata read, not a transcript fetch");
  assert.equal(hub.callsOf("thread/turns/list").length, 0);
});

test("list_tasks scope-prechecks the same way", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: outOfScopeThread() }));
  hub.on("evener/tasks/list", () => ({ data: [] }));
  await assert.rejects(() => TOOLS.list_tasks.run(ctxFor(hub, SCOPE), { ref: "local:a" }), /outside the project scope/);
  assert.equal(hub.callsOf("evener/tasks/list").length, 0);
});

test("the mutation tools refuse out-of-scope refs before any mutation", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({
      status: { type: "active" },
      cwd: "/elsewhere/repo",
      projectPath: "/elsewhere/repo",
      evener: { ...makeThread().evener, resumeRequired: true },
    }),
  }));
  const cases = [
    ["send_message", { ref: "local:a", text: "hi" }],
    ["interrupt_session", { ref: "local:a" }],
    ["stop_session", { ref: "local:a" }],
    ["clear_queue", { ref: "local:a" }],
    ["rename_session", { ref: "local:a", name: "n" }],
    ["resume_session", { ref: "local:a" }],
  ] as const;
  for (const [name, args] of cases) {
    await assert.rejects(
      () => TOOLS[name].run(ctxFor(hub, SCOPE), args),
      (err: unknown) => {
        assert.match(err instanceof Error ? err.message : String(err), /outside the project scope/);
        return true;
      },
    );
  }
  assert.equal(hub.calls.length, cases.length, "each tool made exactly its scope-precheck read");
  assert.ok(
    hub.calls.every((c) => c.method === "thread/read"),
    "no tool reached a mutation",
  );
});

test("wait_for_activity treats an out-of-scope named ref exactly like a subscribe failure", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", (params) => {
    if ((params as { ref: string }).ref === "local:outside") return { thread: outOfScopeThread() };
    return { thread: makeThread() };
  });
  const waiting = TOOLS.wait_for_activity.run(ctxFor(hub, SCOPE), {
    refs: ["local:a", "local:outside"],
    timeout_seconds: 5,
  });
  hub.emit({ method: "turn/started", params: { ref: "local:a" } } as never);
  const out = await waiting;
  assert.match(out, /could not watch local:outside: .*outside the project scope/);
  assert.match(out, /could not watch local:outside: .*\/home\/jesse\/git\/evener/);
  assert.match(out, /\[turns\] local:a: turn started/);
});

test("a wait whose every named ref is out of scope says nothing was waited on", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: outOfScopeThread() }));
  const out = await TOOLS.wait_for_activity.run(ctxFor(hub, SCOPE), { refs: ["local:far"], timeout_seconds: 1 });
  assert.match(out, /could not watch local:far: .*outside the project scope/);
  assert.match(out, /no matching activity — none of the named refs could be watched/);
});

test("list_sessions filters rows to the scope and says so", async () => {
  const hub = new FakeHub();
  hub.on("thread/list", () => ({
    data: [
      makeThread(),
      makeThread({
        id: "thread-x",
        cwd: "/elsewhere/repo",
        projectPath: "/elsewhere/repo",
        evener: { ...makeThread().evener, ref: "local:x" },
      }),
    ],
  }));
  const out = await TOOLS.list_sessions.run(ctxFor(hub, SCOPE), {});
  assert.match(out, /local:a \| idle/);
  assert.doesNotMatch(out, /local:x/);
  assert.match(out, /project scope: \/home\/jesse\/git\/evener — rows for sessions outside it are hidden/);
});

test("search_sessions filters live and past rows by their project field", async () => {
  const hub = new FakeHub();
  hub.on("evener/search", () => ({
    live: [
      { id: "a", title: "in scope", project: "evener", state: "active", age: "2m ago", ref: "local:a" },
      { id: "c", title: "elsewhere", project: "other-repo", state: "active", age: "1m ago", ref: "local:c" },
    ],
    past: [
      { id: "b", title: "old parser work", project: "evener", state: "ended", age: "3d ago", ref: "local:b" },
      { id: "d", title: "old elsewhere", project: "other-repo", state: "ended", age: "5d ago", ref: "local:d" },
    ],
  }));
  const out = await TOOLS.search_sessions.run(ctxFor(hub, SCOPE), { query: "parser" });
  assert.match(out, /live: local:a \| active \| in scope/);
  assert.doesNotMatch(out, /local:c/);
  assert.match(out, /past: local:b \| ended \| old parser work/);
  assert.doesNotMatch(out, /local:d/);
  assert.match(out, /project scope: \/home\/jesse\/git\/evener/);
});

test("start_session refuses a cwd outside the scope", async () => {
  const hub = new FakeHub();
  hub.on("thread/start", () => ({ thread: makeThread(), turn: {} }));
  const out = await TOOLS.start_session.run(ctxFor(hub, SCOPE), { cwd: "/elsewhere/x", prompt: "go" });
  assert.match(out, /outside the project scope/);
  assert.match(out, /\/home\/jesse\/git\/evener/);
  assert.equal(hub.callsOf("thread/start").length, 0);
});

test("start_session serves a cwd inside the scope", async () => {
  const hub = new FakeHub();
  hub.on("thread/start", () => ({
    thread: makeThread({ cwd: `${SCOPE}/sub` }),
    turn: { id: "turn_1", itemsView: "full", status: "in_progress" },
  }));
  hub.on("thread/read", () => ({ thread: makeThread({ cwd: `${SCOPE}/sub` }) }));
  const out = await TOOLS.start_session.run(ctxFor(hub, SCOPE), { cwd: `${SCOPE}/sub`, prompt: "go" });
  assert.match(out, /started local:a/);
});

test("hub_overview prints the scope line and omits recent projects when scoped", async () => {
  const hub = new FakeHub();
  hub.on("evener/daemon/list", () => ({ daemons: [], defaultTimeoutMillis: 1000 }));
  hub.on("evener/projects/recent", () => ({ data: ["/elsewhere/repo"] }));
  const out = await TOOLS.hub_overview.run(ctxFor(hub, SCOPE), {});
  assert.match(out, /project scope: \/home\/jesse\/git\/evener/);
  assert.match(out, /EVENER_HUB_MCP_PROJECT/);
  assert.doesNotMatch(out, /recent project directories/);
  assert.equal(hub.callsOf("evener/projects/recent").length, 0, "a scoped overview never fetches the directory list");

  const unscoped = await TOOLS.hub_overview.run(ctxFor(hub), {});
  assert.match(unscoped, /recent project directories/);
  assert.equal(hub.callsOf("evener/projects/recent").length, 1);
});
