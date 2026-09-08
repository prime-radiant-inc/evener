import assert from "node:assert/strict";
import { test } from "node:test";
import { observeNotifications } from "./bounded-notifications.mjs";

function fixture() {
  const notifications = new Set();
  const states = new Set();
  const hub = {
    connections: 0,
    connect: async () => {
      hub.connections += 1;
    },
    onNotification: (callback) => {
      notifications.add(callback);
      return () => notifications.delete(callback);
    },
    onStateChange: (callback) => {
      states.add(callback);
      return () => states.delete(callback);
    },
    emit: (value = 1, method = "change") => {
      for (const callback of notifications) callback({ method, params: { value } });
    },
    state: (value) => {
      for (const callback of states) callback(value);
    },
    listenerCount: () => notifications.size + states.size,
  };
  let reads = 0;
  const options = {
    methods: ["change"],
    decodeNotification: ({ params }) => {
      assert.equal(typeof params.value, "number");
      return params.value === -1 ? null : params;
    },
    readSnapshot: async () => ({ revision: ++reads }),
    observeDurationMs: 0,
  };
  return { hub, options };
}

test("subscribes before initial read, reads again without events, and removes listeners", async () => {
  const { hub, options } = fixture();
  let reads = 0;
  options.readSnapshot = async () => {
    if (++reads === 1) hub.emit();
    return { revision: reads };
  };
  const result = await observeNotifications(hub, options);
  assert.equal(result.eventCount, 1);
  assert.deepEqual(result.initial, { revision: 1 });
  assert.deepEqual(result.readback, { revision: 2 });
  assert.equal(result.outcome, "read");
  assert.equal(hub.listenerCount(), 0);
  const quiet = fixture();
  const quietResult = await observeNotifications(quiet.hub, quiet.options);
  assert.equal(quietResult.eventCount, 0);
  assert.deepEqual(quietResult.readback, { revision: 2 });
});

test("filters unrelated methods and sessions before counting", async () => {
  const { hub, options } = fixture();
  options.observe = async () => {
    hub.emit(1, "other");
    hub.emit(-1);
    hub.emit(3);
  };
  const result = await observeNotifications(hub, options);
  assert.equal(result.eventCount, 1);
  assert.deepEqual(result.events, [{ value: 3 }]);
});

test("bounds retention while recording incomplete event history", async () => {
  const { hub, options } = fixture();
  options.maxEvents = 1;
  options.observe = async () => {
    hub.emit(1);
    hub.emit(2);
    hub.emit(3);
  };
  const result = await observeNotifications(hub, options);
  assert.equal(result.eventCount, 3);
  assert.deepEqual(result.events, [{ value: 1 }]);
  assert.equal(result.overflow, true);
  assert.equal(result.outcome, "uncertain");
});

test("reports concurrent readback invalidation without replaying reads", async () => {
  const { hub, options } = fixture();
  let reads = 0;
  options.readSnapshot = async () => {
    if (++reads === 2) hub.emit();
    return { revision: reads };
  };
  const result = await observeNotifications(hub, options);
  assert.equal(result.changedDuringReadback, true);
  assert.equal(result.outcome, "uncertain");
  assert.equal(reads, 2);
});

test("retains disconnect uncertainty after reconnect", async () => {
  const { hub, options } = fixture();
  options.observe = async () => {
    hub.state("reconnecting");
    hub.state("ready");
  };
  const result = await observeNotifications(hub, options);
  assert.equal(result.connectionInterrupted, true);
  assert.equal(result.outcome, "uncertain");
});

test("rejects malformed observed events and cleans up", async () => {
  const { hub, options } = fixture();
  options.observe = async () => hub.emit("invalid");
  await assert.rejects(observeNotifications(hub, options), assert.AssertionError);
  assert.equal(hub.listenerCount(), 0);
});

for (const phase of ["connect", "initial", "observe", "readback"]) {
  test(`removes listeners after ${phase} failure`, async () => {
    const { hub, options } = fixture();
    const fail = () => {
      throw new Error("fixture failure");
    };
    let reads = 0;
    if (phase === "connect") hub.connect = fail;
    if (phase === "observe") options.observe = fail;
    options.readSnapshot = async () => {
      reads += 1;
      if ((phase === "initial" && reads === 1) || (phase === "readback" && reads === 2)) fail();
      return { revision: reads };
    };
    await assert.rejects(observeNotifications(hub, options), /fixture failure/);
    assert.equal(hub.listenerCount(), 0);
  });
}

test("rejects invalid bounds before connection or subscription", async () => {
  for (const bad of [
    { observeDurationMs: -1 },
    { observeDurationMs: 10001 },
    { observeDurationMs: NaN },
    { maxEvents: 0 },
    { maxEvents: 101 },
  ]) {
    const { hub, options } = fixture();
    await assert.rejects(observeNotifications(hub, { ...options, ...bad }), assert.AssertionError);
    assert.equal(hub.connections, 0);
    assert.equal(hub.listenerCount(), 0);
  }
});

test("uses a finite observation timer and removes it after completion", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { hub, options } = fixture();
  let timerScheduled;
  const scheduled = new Promise((resolve) => {
    timerScheduled = resolve;
  });
  const fakeSetTimeout = globalThis.setTimeout;
  t.mock.method(globalThis, "setTimeout", (callback, duration) => {
    const timer = fakeSetTimeout(callback, duration);
    timerScheduled();
    return timer;
  });
  options.observeDurationMs = 250;
  const pending = observeNotifications(hub, options);
  await scheduled;
  hub.emit();
  t.mock.timers.tick(250);
  const result = await pending;
  assert.equal(result.eventCount, 1);
  assert.equal(hub.listenerCount(), 0);
});
