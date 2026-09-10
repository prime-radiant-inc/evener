import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { clientFromEnvironment } from "./connection.mjs";

if (process.env.EVENER_EXAMPLE_CREATE_SESSION !== "1")
  throw new Error(
    "Set EVENER_EXAMPLE_CREATE_SESSION=1 on an isolated test hub; this creates a session and starts/stops a turn.",
  );
const provider = process.env.EVENER_MODEL_PROVIDER;
const model = process.env.EVENER_MODEL;
if (!provider || !model) throw new Error("Set EVENER_MODEL_PROVIDER and EVENER_MODEL from the hub catalog.");
const { hub, cwd } = clientFromEnvironment();
const failures = [];
let ref;
let instanceId;
let creationAttempted = false;
let startAttempted = false;
let finished = false;
let stopObserving = () => {};

// Notifications can precede responses. Retain lifecycle metadata only for the
// example's fresh session, without buffering transcript or provider payloads.
function observeTurns(ref) {
  const seen = new Map();
  const waiters = new Set();
  stopObserving = hub.onNotification((event) => {
    if (!["turn/started", "turn/completed"].includes(event.method) || event.params.ref !== ref) return;
    const turn = event.params.turn;
    seen.set(`${event.method}:${turn.id}`, { id: turn.id, status: turn.status });
    for (const check of waiters) check();
  });
  return (method, turnId) =>
    new Promise((resolve, reject) => {
      const key = `${method}:${turnId}`;
      const timer = setTimeout(() => {
        waiters.delete(check);
        reject(new Error(`Did not observe ${method} for ${turnId}.`));
      }, 30000);
      function check() {
        if (!seen.has(key)) return;
        clearTimeout(timer);
        waiters.delete(check);
        resolve(seen.get(key));
      }
      waiters.add(check);
      check();
    });
}
function checkReceipt(receipt, mutationId, threadId, projectionState) {
  assert.equal(receipt.clientMutationId, mutationId);
  assert.equal(receipt.threadId, threadId);
  assert.equal(receipt.instanceId, instanceId);
  assert.ok(receipt.turnId);
  assert.ok(["applied", "replayed"].includes(receipt.disposition));
  assert.equal(receipt.projectionState, projectionState);
}
try {
  await hub.connect();
  const catalog = await hub.request("model/list", { cwd });
  assert.ok(
    catalog.data.some((item) => item.provider === provider && item.model === model),
    "Requested model is absent from this project's catalog.",
  );
  // Empty input requires an empty-task-capable harness. thread/start has no
  // mutation ID: a lost reply must never cause an automatic second creation.
  creationAttempted = true;
  const created = await hub.request("thread/start", { cwd, modelProvider: provider, model });
  ref = created.thread.evener.ref;
  console.log(JSON.stringify({ createdRef: ref }));
  const observed = observeTurns(ref);
  const opened = await hub.request("thread/read", { ref, includeTurns: true, subscribe: true });
  instanceId = opened.thread.evener.instanceId;
  assert.ok(instanceId);
  assert.equal(opened.thread.evener.capabilities.send, true);
  const mutationId = randomUUID();
  const prompt = `AppWire lifecycle fixture ${mutationId}`;
  startAttempted = true;
  const started = await hub.request("turn/start", {
    ref,
    expectedInstanceId: instanceId,
    clientMutationId: mutationId,
    input: [{ type: "text", text: prompt }],
  });
  checkReceipt(started.receipt, mutationId, opened.thread.id, "pending");
  assert.equal(started.receipt.turnId, started.turn.id);
  await observed("turn/started", started.turn.id);
  const active = await hub.request("thread/read", { ref, includeTurns: false, subscribe: false });
  assert.equal(active.thread.evener.instanceId, instanceId);
  assert.equal(
    active.thread.evener.capabilities.interrupt,
    true,
    "Use a scripted provider that holds the turn open until interrupted.",
  );
  const stopId = randomUUID();
  const stopped = await hub.request("turn/interrupt", {
    ref,
    expectedInstanceId: instanceId,
    clientMutationId: stopId,
  });
  checkReceipt(stopped.receipt, stopId, opened.thread.id, "reflected");
  assert.equal(stopped.receipt.turnId, started.turn.id);
  const completed = await observed("turn/completed", started.turn.id);
  assert.equal(completed.status, "interrupted");
  finished = true;
  const readback = await hub.request("thread/read", { ref, includeTurns: true, subscribe: false });
  assert.equal(readback.thread.evener.instanceId, instanceId);
  const turn = readback.thread.turns.find((turn) => turn.id === started.turn.id);
  assert.equal(turn?.status, "interrupted");
  assert.ok(turn.items.some((item) => item.type === "userMessage" && item.text === prompt));
  assert.equal(readback.thread.evener.capabilities.send, true);
  console.log(
    JSON.stringify({
      ref,
      turnId: turn.id,
      status: turn.status,
      verified: [
        "start receipt",
        "turn/started",
        "interrupt receipt",
        "turn/completed",
        "input readback",
        "idle send capability",
      ],
    }),
  );
} catch (error) {
  failures.push(error);
  if (creationAttempted && !ref)
    console.error("Creation may be unconfirmed. Inspect the isolated hub; do not rerun blindly.");
} finally {
  // Cleanup is limited to the example's fresh session and original instance.
  // Preserve the session for inspection; never replay Start or delete history.
  if (ref && instanceId && startAttempted && !finished) {
    try {
      const { thread } = await hub.request("thread/read", { ref, includeTurns: false, subscribe: false });
      if (thread.evener.instanceId === instanceId && thread.evener.capabilities.interrupt)
        await hub.request("turn/interrupt", { ref, expectedInstanceId: instanceId, clientMutationId: randomUUID() });
    } catch (error) {
      failures.push(error);
    }
  }
  if (ref) {
    try {
      await hub.request("thread/unsubscribe", { ref });
    } catch (error) {
      failures.push(error);
    }
  }
  stopObserving();
  hub.close();
}
if (failures.length)
  throw new AggregateError(failures, "Session lifecycle example failed; inspect the isolated session.");
