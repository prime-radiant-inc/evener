#!/usr/bin/env node
// pm-workflow drives the hub MCP the way the PM session will: through real
// MCP tool calls over stdio, against a real hub whose provider is fakellm
// (test/e2e/fakellm), so every session it manages runs real daemon turns.
//
// The scenario is the supervising loop the MCP exists for:
// orient → delegate → watch → steer → review → interrupt → report → stop.
//
// Nothing sleeps on a guessed wall clock for state: every wait goes through
// wait_for_activity (that is part of what is being tested) or polls a hub
// read, so the assertions hold however slow the machine is.
//
// Env: EVENER_E2E_RPC (hub /rpc ws url), EVENER_E2E_TOKEN (hub token),
// EVENER_E2E_WORKSPACE (cwd for spawned sessions), EVENER_E2E_MCP (the MCP
// server entry .js to run with node).

import assert from "node:assert/strict";
import process from "node:process";

import { McpClient } from "../mcp-client.mjs";

const rpc = required("EVENER_E2E_RPC");
const token = required("EVENER_E2E_TOKEN");
const workspace = required("EVENER_E2E_WORKSPACE");
const entry = required("EVENER_E2E_MCP");

function required(name) {
  const value = process.env[name];
  if (!value) {
    process.stderr.write(`pm-workflow: set ${name}\n`);
    process.exit(2);
  }
  return value;
}

const client = await McpClient.spawn(["node", entry], {
  env: { ...process.env, EVENER_HUB_RPC_URL: rpc, EVENER_HUB_TOKEN: token },
});

async function call(name, args) {
  const result = await client.callTool(name, args);
  if (result.isError) {
    throw new Error(`${name} failed: ${result.text}`);
  }
  return result.text;
}

function cursorOf(text) {
  // The cursor is "<epoch>:<seq>": the epoch names the server process (a
  // restart mints a new one), the seq its position in that process's stream.
  // since takes the whole string back verbatim; only seq comparisons need it
  // split, so a numeric parse here would silently read the epoch's digits.
  const match = text.match(/activity_cursor: ([0-9a-f]+:\d+)/);
  assert.ok(match, `expected an activity_cursor in:\n${text}`);
  return match[1];
}

function cursorParts(cursor) {
  const match = /^([0-9a-f]+):(\d+)$/.exec(cursor);
  assert.ok(match, `expected an "<epoch>:<seq>" activity_cursor, got "${cursor}"`);
  return { epoch: match[1], seq: Number(match[2]) };
}

// waitFor repeatedly waits for activity until the accumulated events match,
// advancing the cursor each round — the exact loop a real PM runs, and the
// honest way to use the tool: every call returns promptly with what arrived,
// and the caller decides when the event it wants has shown up.
async function waitFor(pattern, { refs, groups, since, timeoutSeconds = 30, budgetSeconds = 90 }) {
  let cursor = since;
  const deadline = Date.now() + budgetSeconds * 1000;
  let latest = "";
  for (;;) {
    latest = await call("wait_for_activity", {
      refs,
      groups,
      since: cursor,
      timeout_seconds: timeoutSeconds,
    });
    cursor = cursorOf(latest);
    if (new RegExp(pattern).test(latest)) return { text: latest, cursor };
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for /${pattern}/; last activity:\n${latest}`);
    }
  }
}

// 1. Orient.
const overview = await call("hub_overview", {});
assert.match(overview, /hub: evener-hub .+ \(source local\) at .* — connected/);
assert.match(overview, /daemons:/);
const { epoch, seq } = cursorParts(cursorOf(overview));
assert.equal(seq, 0, "the stream position starts at 0 before any event");
// The catalog read is part of orienting: list_models renders exactly the
// model strings start_session accepts, so the fake provider the workers
// will run must appear there.
const models = await call("list_models", {});
assert.match(models, /- fake\/fake-test-model\b/);
console.log("ok hub_overview");
console.log("ok list_models renders the model start_session accepts");

// 2. Delegate: start a worker on the fake provider. Three held rounds then
// the turn ends, so there is a real turn to watch, steer inside, and read.
const startOut = await call("start_session", {
  cwd: workspace,
  prompt: "Read the notes and index every parser mention.",
  name: "notes-worker",
  model: "fake/fake-test-model",
});
const refMatch = startOut.match(/started (local:[A-Za-z0-9_-]+)/);
assert.ok(refMatch, `expected a session ref in:\n${startOut}`);
const worker = refMatch[1];
console.log(`ok start_session → ${worker}`);

// 3. Watch: the turn must start, and the cursor must advance past 0.
const startWatch = await waitFor("\\[turns\\].*: turn started", { refs: [worker] });
assert.match(startWatch.text, /\[turns\] .*: turn started/);
const cursor = startWatch.cursor;
const watched = cursorParts(cursor);
assert.ok(watched.seq > 0, "the activity cursor advanced past 0");
assert.equal(watched.epoch, epoch, "one server process serves this workflow, so one epoch");
console.log("ok wait_for_activity saw the turn start");

// 4. Steer mid-turn: the worker is inside its held rounds, so auto mode must
// route to steer without the caller knowing the state machine.
const steerOut = await call("send_message", { ref: worker, text: "Also note any lexer TODOs." });
assert.match(steerOut, /steered the running turn/);
console.log("ok send_message auto-steered mid-turn");

// 5. The turn completes (fakellm ends it after its rounds), with the steer
// visible to the model on a later round.
const doneWatch = await waitFor("turn completed", { refs: [worker], groups: ["turns"], since: cursor });
assert.match(doneWatch.text, /turn completed/);
console.log("ok wait_for_activity saw the turn complete");

// 6. Review: the transcript shows the user prompt, the model's tool work,
// and the final message.
const transcript = await call("read_transcript", { ref: worker, turns: 5 });
assert.match(transcript, /user: Read the notes and index every parser mention\./);
assert.match(transcript, /tool: read_file/);
console.log("ok read_transcript shows the work");

// 7. get_session on the worker after its turn: fakellm ends turns with
// communicate(end_turn=true), so evener marks the session awaiting the
// human's next message; the send capability is on.
const detail = await call("get_session", { ref: worker });
assert.match(detail, /state: awaiting/);
assert.match(detail, /you can: send/);
console.log("ok get_session shows the awaiting worker");

// 8. Interrupt: a second worker with more rounds is stopped mid-flight.
const startTwo = await call("start_session", {
  cwd: workspace,
  prompt: "A longer audit task.",
  name: "audit-worker",
  model: "fake/fake-test-model",
});
const refTwo = startTwo.match(/started (local:[A-Za-z0-9_-]+)/)[1];
const watchTwo = await waitFor("turn started", { refs: [refTwo] });
assert.match(watchTwo.text, /turn started/);
const interruptOut = await call("interrupt_session", { ref: refTwo });
assert.match(interruptOut, /interrupted the running turn/);
console.log("ok interrupt_session stopped the second worker mid-turn");

// 9. The fleet view shows both workers.
const fleet = await call("list_sessions", {});
assert.match(fleet, new RegExp(worker));
assert.match(fleet, new RegExp(refTwo));
assert.match(fleet, /notes-worker/);
assert.match(fleet, /audit-worker/);
console.log("ok list_sessions shows the fleet");

// 9b. Rename the second worker: the echo must carry old -> new, and the
// fleet must show the new label — a rename nothing reports is a rename
// that did not happen.
const renameOut = await call("rename_session", { ref: refTwo, name: "audit-worker-retitled" });
assert.match(renameOut, new RegExp(`renamed ${refTwo}: "audit-worker" -> "audit-worker-retitled"`));
const fleetRenamed = await call("list_sessions", {});
assert.match(fleetRenamed, /audit-worker-retitled/);
console.log("ok rename_session retitled the second worker");

// resume_session is deliberately not exercised: reaching it for real needs
// the hub's recovery path to halt a live session, which nothing in this
// fakellm harness can trigger without faking the very state under test.

// 10. Search finds the worker by its prompt.
const search = await call("search_sessions", { query: "parser mention" });
assert.match(search, new RegExp(worker));
console.log("ok search_sessions found the worker");

// 11. Task lists: fakellm never uses a task list, so the tool must say so
// honestly rather than invent rows.
const tasks = await call("list_tasks", { ref: worker });
assert.match(tasks, /no readable task list|empty task list/);
console.log("ok list_tasks is honest about a session without tasks");

// 12. Stop both workers.
for (const ref of [worker, refTwo]) {
  const stopOut = await call("stop_session", { ref });
  assert.match(stopOut, /stopped/);
}

// 13. Honest quiet: a dormant session (no prompt, so nothing ever runs)
// produces no WORK after its spawn settles, so a wait scoped to it and
// the current cursor must report that honestly rather than hanging or
// inventing activity. The wait filters to the turns group on purpose:
// the hub's attention watcher emits idle→idle bookkeeping for a fresh
// session at a timing of its own, and since the attention-fix those
// events genuinely match a wait on the session — the quiet under test is
// "no work happened", not "the hub stayed silent about everything".
// Stopping the workers above would make waits on THEM see their own
// closing events, which is exactly the noise this check must not
// depend on.
const dormantStart = await call("start_session", { cwd: workspace, model: "fake/fake-test-model" });
assert.match(dormantStart, /\(dormant: no prompt, so nothing runs until you send_message\)/);
const dormant = dormantStart.match(/started (local:[A-Za-z0-9_-]+)/)[1];
const quietCursor = cursorOf(dormantStart);
const quiet = await call("wait_for_activity", {
  refs: [dormant],
  since: quietCursor,
  events: ["turns"],
  timeout_seconds: 2,
});
assert.match(quiet, /no matching activity on the 1 named session in the last 2s — nothing is wrong/);
console.log("ok wait_for_activity reports honest quiet");
const dormantStop = await call("stop_session", { ref: dormant });
assert.match(dormantStop, /stopped/);

// ---- Phase 2: a real session uses the MCP as its tools ----
//
// The scripted PM is a genuine evener session spawned with this hub MCP in
// its mcp.json (written by the orchestrator). fakellm answers its rounds
// with hub__ tool calls, the daemon executes them through a real MCP stdio
// connection, and the worker it starts exists on the hub afterwards. This
// is the wiring the PM session kind will actually ship.

const pmStart = await call("start_session", {
  cwd: workspace,
  prompt: "You are the project manager for this fleet. PM-SCRIPTED-DRIVER. Supervise.",
  name: "scripted-pm",
  model: "fake/fake-test-model",
});
const pmRef = pmStart.match(/started (local:[A-Za-z0-9_-]+)/)[1];
console.log(`ok started the scripted PM as a real session → ${pmRef}`);

// A spawned session runs internal setup turns (system, environment) before
// its model turn, and those complete too — so "turn completed" alone can
// fire early. The scripted PM ends its model turn with plain text, which
// leaves the session awaiting the human's next message: that one status
// transition is the deterministic signal the PM is done.
const pmDone = await waitFor("status: awaiting", { refs: [pmRef], groups: ["status"] });
assert.match(pmDone.text, /status: awaiting/);
console.log("ok the scripted PM finished its MCP-driven turn");

// The PM's transcript must show the real MCP tool calls the daemon executed
// on its behalf, and their results.
const pmTranscript = await call("read_transcript", { ref: pmRef, turns: 3, detail: "full" });
assert.match(pmTranscript, /tool: hub__hub_overview/);
// The overview call must have SUCCEEDED for the PM, and the transcript is
// where that shows: its live "connected" line is the unambiguous marker of
// the tool's actual output — the tool name alone cannot tell an error
// result from a working one.
assert.match(pmTranscript, /hub: evener-hub .+ \(source local\) at .* — connected/);
assert.match(pmTranscript, /tool: hub__start_session/);
// The rendered result of the PM's start call must be the success text that
// names the worker's ref — an error result would prove the wiring but not
// the work.
assert.match(pmTranscript, /started local:[A-Za-z0-9_-]+ and started its first turn/);
console.log("ok the PM transcript records the MCP tool calls");

// The worker the PM started through its own MCP tools must exist on the
// hub — the whole point of the exercise.
const fleetAfter = await call("list_sessions", {});
assert.match(fleetAfter, /scripted-worker/);
console.log("ok the PM's hub__start_session created a real worker");

// When the session snapshot carries MCP diagnostics, the hub MCP must read
// as connected in the session that used it.
const pmDetail = await call("get_session", { ref: pmRef });
if (/mcp servers/.test(pmDetail)) {
  assert.match(pmDetail, /hub=connected/);
  console.log("ok the PM session reports the hub MCP connected");
}

// ---- Config-gate smokes: the same server binary, stricter configs ----
//
// The read-only and project-scope gates are configuration promises, so the
// proof is more server instances against the same live hub: tools/list must
// drop the mutating tools under EVENER_HUB_MCP_READONLY=1, and a scope that
// contains no session must refuse to read one.

const readOnly = await McpClient.spawn(["node", entry], {
  env: { ...process.env, EVENER_HUB_RPC_URL: rpc, EVENER_HUB_TOKEN: token, EVENER_HUB_MCP_READONLY: "1" },
});
const readOnlyNames = (await readOnly.listTools()).map((tool) => tool.name);
assert.ok(readOnlyNames.includes("list_models"), "the read-only registry must include list_models");
assert.ok(!readOnlyNames.includes("start_session"), "the read-only registry must not include start_session");
await readOnly.close();
console.log("ok the read-only instance registers only the read tools");

// Every session this harness starts lives in the workspace, so a scope one
// level below it genuinely contains none: the live PM's ref is out of scope
// (a stopped session could fail at thread/read before the gate, so the
// refusal must be proven on a session that is certainly readable).
const scoped = await McpClient.spawn(["node", entry], {
  env: {
    ...process.env,
    EVENER_HUB_RPC_URL: rpc,
    EVENER_HUB_TOKEN: token,
    EVENER_HUB_MCP_PROJECT: `${workspace}/no-sessions-here`,
  },
});
const refusal = await scoped.callTool("get_session", { ref: pmRef });
assert.ok(refusal.isError, "get_session must refuse a ref outside the configured project scope");
assert.match(
  refusal.text,
  /is outside the project scope this server is limited to \(.*no-sessions-here, from EVENER_HUB_MCP_PROJECT\)/,
);
await scoped.close();
console.log("ok the scoped instance refuses an out-of-scope ref");

await client.close();
console.log("PM WORKFLOW PASSED");
