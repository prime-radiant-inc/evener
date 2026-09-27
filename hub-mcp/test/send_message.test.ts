import assert from "node:assert/strict";
import { test } from "node:test";

import { WireError } from "@evener/appwire-client";

import { ActivityWatcher } from "../src/events.js";
import type { ToolContext } from "../src/tools.js";
import { TOOLS } from "../src/tools.js";
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

function wireError(info: string, message: string): WireError {
  const err = new WireError(message, -32000, { evenerErrorInfo: info });
  return err;
}

async function runSend(hub: FakeHub, args: Record<string, unknown>): Promise<string | { error: string }> {
  const ctx = ctxFor(hub);
  try {
    return await TOOLS.send_message.run(ctx, args);
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

function outcome(out: string | { error: string }): string {
  return typeof out === "string" ? out : out.error;
}

test("auto mode starts a turn on an idle session", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  hub.on("turn/start", () => ({ turn: { id: "turn_2", itemsView: "full", status: "in_progress" }, receipt: {} }));
  const out = await runSend(hub, { ref: "local:a", text: "next: write tests" });
  assert.match(outcome(out), /started turn turn_2 on local:a/);
  const [start] = hub.callsOf("turn/start");
  assert.ok(start);
  assert.equal(start.text, undefined);
  assert.deepEqual((start.input as Array<{ type: string; text: string }>)[0], {
    type: "text",
    text: "next: write tests",
  });
  assert.ok(start.clientMutationId, "mutations carry a fresh clientMutationId");
  assert.equal(start.expectedInstanceId, "thread-a", "expectedInstanceId falls back to thread id");
});

test("auto mode steers a busy session", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({ status: { type: "active" }, evener: { ...makeThread().evener, instanceId: "inst-7" } }),
  }));
  hub.on("turn/steer", () => ({ receipt: {} }));
  const out = await runSend(hub, { ref: "local:a", text: "pivot: use sqlite" });
  assert.match(outcome(out), /steered the running turn/);
  const [steer] = hub.callsOf("turn/steer");
  assert.ok(steer);
  assert.equal(steer.expectedInstanceId, "inst-7", "the CAS id is the live instance id when present");
});

test("auto mode falls back to queueing when steer is unavailable", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({
      status: { type: "active" },
      evener: {
        ...makeThread().evener,
        queue: { revision: 1, depth: 2 },
        capabilities: { ...makeThread().evener.capabilities, steer: false },
      },
    }),
  }));
  hub.on("turn/queue", () => ({ receipt: {} }));
  const out = await runSend(hub, { ref: "local:a", text: "when you finish, run make vet" });
  assert.match(outcome(out), /queued on local:a \(position 3\)/);
});

test("explicit steer on an idle session is an explained error", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  const out = await runSend(hub, { ref: "local:a", text: "hi", mode: "steer" });
  assert.match(outcome(out), /is idle, not mid-turn; there is nothing to steer/);
  assert.equal(hub.callsOf("turn/steer").length, 0);
});

test("explicit steer respects the steer capability, the gate auto applies", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({
      status: { type: "active" },
      evener: { ...makeThread().evener, capabilities: { ...makeThread().evener.capabilities, steer: false } },
    }),
  }));
  const out = await runSend(hub, { ref: "local:a", text: "hi", mode: "steer" });
  assert.match(outcome(out), /does not support steering while busy \(state active\); queue instead/);
  assert.equal(hub.callsOf("turn/steer").length, 0);
});

test("explicit queue on an idle session is an explained error, like auto's gating", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  const out = await runSend(hub, { ref: "local:a", text: "hi", mode: "queue" });
  assert.match(outcome(out), /is idle, not mid-turn; there is nothing to queue behind — use mode "start" \(or auto\)/);
  assert.equal(hub.callsOf("turn/queue").length, 0);
});

test("start on a session that cannot accept turns is an explained error", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({
    thread: makeThread({
      status: { type: "ended" },
      evener: { ...makeThread().evener, capabilities: { ...makeThread().evener.capabilities, send: false } },
    }),
  }));
  const out = await runSend(hub, { ref: "local:a", text: "hi", mode: "start" });
  assert.match(outcome(out), /does not accept new turns right now \(state ended\)/);
});

test("a restart conflict retries once against the new instance", async () => {
  const hub = new FakeHub();
  let reads = 0;
  hub.on("thread/read", () => {
    reads += 1;
    return { thread: makeThread({ evener: { ...makeThread().evener, instanceId: `inst-${reads}` } }) };
  });
  let starts = 0;
  hub.on("turn/start", () => {
    starts += 1;
    if (starts === 1) throw wireError("conflict", "session instance changed");
    return { turn: { id: "turn_3", itemsView: "full", status: "in_progress" }, receipt: {} };
  });
  const out = await runSend(hub, { ref: "local:a", text: "again" });
  assert.match(outcome(out), /started turn turn_3/);
  const [, second] = hub.callsOf("turn/start");
  assert.ok(second);
  assert.equal(second.expectedInstanceId, "inst-2", "the retry re-read and used the new instance id");
});

test("an unknown mutation outcome is reported as unknown, never retried", async () => {
  const hub = new FakeHub();
  hub.on("thread/read", () => ({ thread: makeThread() }));
  hub.on("turn/start", () => {
    throw wireError("mutationOutcomeUnknown", "connection lost during turn/start");
  });
  const out = await runSend(hub, { ref: "local:a", text: "maybe" });
  assert.match(outcome(out), /fate is unknown/);
  assert.match(outcome(out), /do not blindly resend/);
  assert.equal(hub.callsOf("turn/start").length, 1, "an uncertain outcome is never retried");
});

test("empty text is refused before any hub call", async () => {
  const hub = new FakeHub();
  const out = await runSend(hub, { ref: "local:a", text: "   " });
  assert.match(outcome(out), /requires non-empty text/);
  assert.equal(hub.calls.length, 0);
});
