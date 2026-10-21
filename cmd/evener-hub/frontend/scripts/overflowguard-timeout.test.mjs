import assert from "node:assert/strict";
import test from "node:test";
import { installTimeoutClock } from "./overflowguard/timeoutClock.mjs";

function realm() {
  return { setTimeout, clearTimeout, performance, Date, requestAnimationFrame: () => {}, MessageChannel };
}

function setup(t) {
  const scope = realm();
  const clock = installTimeoutClock(scope);
  t.after(() => clock.dispose());
  return { scope, clock };
}

test("timeouts invoke the actual callback with arguments and the window receiver at their deadline", async (t) => {
  const { scope, clock } = setup(t);
  const seen = [];
  scope.setTimeout(
    function (...args) {
      seen.push({ receiver: this, args, now: clock.now });
    },
    10,
    42,
    null,
  );
  await clock.advanceTo(9);
  assert.deepEqual(seen, []);
  await clock.advanceTo(10);
  assert.deepEqual(seen, [{ receiver: scope, args: [42, null], now: 10 }]);
  await clock.advanceTo(11);
  assert.equal(seen.length, 1);
  assert.equal(clock.snapshot().history.find((event) => event.type === "scheduled").requested, 10);
});

test("clear cancels controlled timers and delegates pre-install native handles", async (t) => {
  const scope = realm();
  let nativeCalls = 0;
  const nativeHandle = scope.setTimeout(() => nativeCalls++, 0);
  const clock = installTimeoutClock(scope);
  t.after(() => clock.dispose());
  scope.clearTimeout(nativeHandle);
  let calls = 0;
  const handle = scope.setTimeout(() => calls++, 3);
  scope.clearTimeout(handle);
  scope.clearTimeout(handle);
  await clock.advanceTo(4);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(calls, 0);
  assert.equal(nativeCalls, 0);
  assert.equal(clock.snapshot().pending.length, 0);
});

test("advances order deadlines and FIFO, flush async-scheduled due work, and preserve later timers", async (t) => {
  const { scope, clock } = setup(t);
  const seen = [];
  scope.setTimeout(() => seen.push("last"), 30);
  scope.setTimeout(() => {
    seen.push("first");
    Promise.resolve().then(() => scope.setTimeout(() => seen.push("microtask"), 0));
    const channel = new MessageChannel();
    channel.port1.onmessage = () => {
      scope.setTimeout(() => seen.push("task"), 0);
      channel.port1.close();
      channel.port2.close();
    };
    channel.port2.postMessage(null);
    scope.setTimeout(() => seen.push("future"), 20);
  }, 10);
  scope.setTimeout(() => seen.push("second"), 10);
  scope.setTimeout(() => seen.push("early"), 5);
  await clock.advanceTo(10);
  assert.deepEqual(seen, ["early", "first", "second", "microtask", "task"]);
  assert.deepEqual(
    clock.snapshot().pending.map((timer) => timer.due),
    [30, 30],
  );
  await clock.advanceTo(30);
  assert.deepEqual(seen.slice(-2), ["last", "future"]);
});

test("callbacks can cancel later callbacks in the same advance", async (t) => {
  const { scope, clock } = setup(t);
  let called = false;
  let later;
  scope.setTimeout(() => scope.clearTimeout(later), 2);
  later = scope.setTimeout(() => {
    called = true;
  }, 3);
  await clock.advanceTo(4);
  assert.equal(called, false);
});

test("dispose restores exact descriptors and leaves native clocks untouched", async () => {
  const scope = realm();
  Object.defineProperty(scope, "setTimeout", { enumerable: false });
  const before = Object.getOwnPropertyDescriptors(scope);
  const clock = installTimeoutClock(scope);
  for (const key of ["performance", "Date", "requestAnimationFrame", "MessageChannel"]) {
    assert.deepEqual(Object.getOwnPropertyDescriptor(scope, key), before[key]);
  }
  scope.setTimeout(() => assert.fail(), 2);
  const disposed = clock.dispose();
  assert.equal(disposed.abandoned.length, 1);
  assert.deepEqual(disposed.restored, { setTimeout: true, clearTimeout: true });
  assert.deepEqual(Object.getOwnPropertyDescriptors(scope), before);
  assert.equal(clock.snapshot().pending.length, 0);
  await assert.rejects(clock.advanceTo(2), Error);
});

test("dispose never overwrites wrappers installed by a later owner", () => {
  const scope = realm();
  const clock = installTimeoutClock(scope);
  const laterSet = () => 123,
    laterClear = () => {};
  scope.setTimeout = laterSet;
  scope.clearTimeout = laterClear;
  assert.deepEqual(clock.dispose().restored, { setTimeout: false, clearTimeout: false });
  assert.equal(scope.setTimeout, laterSet);
  assert.equal(scope.clearTimeout, laterClear);
});

test("concurrent, backwards and invalid advances fail without losing future timers", async (t) => {
  const { scope, clock } = setup(t);
  let futureCalls = 0;
  scope.setTimeout(() => futureCalls++, 4);
  scope.setTimeout(() => {}, 1);
  const running = clock.advanceTo(2);
  await assert.rejects(clock.advanceTo(3), Error);
  assert.throws(() => clock.dispose(), Error);
  await running;
  for (const target of [1, NaN, Infinity]) await assert.rejects(clock.advanceTo(target), Error);
  assert.equal(clock.now, 2);
  assert.equal(futureCalls, 0);
  assert.equal(clock.snapshot().pending[0].due, 4);
  await clock.advanceTo(4);
  assert.equal(futureCalls, 1);
});

test("callback exceptions reject the advance and do not execute later timers", async (t) => {
  const { scope, clock } = setup(t);
  const failure = new Error();
  let laterCalls = 0;
  scope.setTimeout(() => {
    throw failure;
  }, 1);
  scope.setTimeout(() => laterCalls++, 2);
  await assert.rejects(clock.advanceTo(3), (error) => error === failure);
  assert.equal(laterCalls, 0);
  assert.equal(clock.now, 1);
  await clock.advanceTo(2);
  assert.equal(laterCalls, 1);
});

test("a runaway is bounded and unsupported timeout forms fail explicitly", async (t) => {
  const { scope, clock } = setup(t);
  let calls = 0;
  const repeat = () => {
    calls++;
    scope.setTimeout(repeat, 0);
  };
  scope.setTimeout(repeat, 0);
  await assert.rejects(clock.advanceTo(1), Error);
  assert.equal(calls, 1000);
  assert.equal(clock.now, 0);
  assert.throws(() => scope.setTimeout("return 1", 0), TypeError);
  for (const delay of [NaN, Infinity, 2147483648]) assert.throws(() => scope.setTimeout(() => {}, delay), RangeError);
});
