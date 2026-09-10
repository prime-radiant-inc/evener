import assert from "node:assert/strict";

// Snapshots are point-in-time reads. Notifications during the final read, a
// connection interruption, or truncated event history prevent a complete
// observation claim; none of them authorizes replaying an action.
export async function observeNotifications(
  hub,
  { methods, decodeNotification, readSnapshot, observe = null, observeDurationMs = 1000, maxEvents = 100 },
) {
  assert.ok(
    Number.isSafeInteger(observeDurationMs) && observeDurationMs >= 0 && observeDurationMs <= 10000,
    "Invalid observation duration.",
  );
  assert.ok(Number.isSafeInteger(maxEvents) && maxEvents > 0 && maxEvents <= 100, "Invalid observation limit.");
  assert.ok(Array.isArray(methods) && methods.length > 0 && methods.every((name) => typeof name === "string"));
  assert.equal(typeof decodeNotification, "function");
  assert.equal(typeof readSnapshot, "function");
  assert.ok(observe === null || typeof observe === "function");
  const selectedMethods = new Set(methods);
  const events = [];
  let eventCount = 0;
  let overflow = false;
  let connectionInterrupted = false;
  let ready = false;
  let listenerError;
  let stopNotifications;
  let stopState;
  let timer;
  try {
    stopNotifications = hub.onNotification((notification) => {
      if (!selectedMethods.has(notification?.method) || listenerError) return;
      try {
        const event = decodeNotification(notification);
        if (event === null) return;
        assert.notEqual(event, undefined, "Notification decoder omitted a result.");
        eventCount += 1;
        if (events.length < maxEvents) events.push(structuredClone(event));
        else overflow = true;
      } catch (error) {
        listenerError = error;
      }
    });
    stopState = hub.onStateChange((state) => {
      if (ready && state !== "ready") connectionInterrupted = true;
      if (state === "ready") ready = true;
    });
    await hub.connect();
    ready = true;
    const initial = await readSnapshot(hub);
    if (listenerError) throw listenerError;
    if (observe) await observe();
    else if (observeDurationMs > 0) {
      await new Promise((resolve) => {
        timer = setTimeout(resolve, observeDurationMs);
      });
    }
    if (listenerError) throw listenerError;
    const countBeforeReadback = eventCount;
    const readback = await readSnapshot(hub);
    if (listenerError) throw listenerError;
    const changedDuringReadback = eventCount !== countBeforeReadback;
    return {
      outcome: overflow || connectionInterrupted || changedDuringReadback ? "uncertain" : "read",
      initial,
      readback,
      events,
      eventCount,
      overflow,
      connectionInterrupted,
      changedDuringReadback,
    };
  } finally {
    clearTimeout(timer);
    stopNotifications?.();
    stopState?.();
  }
}
