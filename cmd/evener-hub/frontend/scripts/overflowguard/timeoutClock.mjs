// Test-only timeout clock for a disposable component-browser target. Native
// input, RAF, Date and performance clocks keep running. This deliberately models
// only function timeouts with finite, non-overflow delays, not all HTML timers.
// Self-contained so the browser driver can inject the function without bundling.
export function installTimeoutClock(scope = window) {
  const descriptors = Object.fromEntries(
    ["setTimeout", "clearTimeout"].map((key) => [key, Object.getOwnPropertyDescriptor(scope, key)]),
  );
  const nativeClear = scope.clearTimeout;
  const pending = new Map(),
    history = [];
  let now = 0,
    nextId = -1,
    order = 0,
    advancing = false,
    disposed = false;
  const record = (type, data = {}) => history.push({ type, logicalMs: now, wallMs: scope.performance.now(), ...data });
  const task = () =>
    new Promise((resolve) => {
      const channel = new scope.MessageChannel();
      channel.port1.onmessage = () => {
        channel.port1.close();
        channel.port2.close();
        resolve();
      };
      channel.port2.postMessage(null);
    });
  function setTimeoutControlled(callback, delay = 0, ...args) {
    if (disposed) throw new Error("clock disposed");
    if (typeof callback !== "function") throw new TypeError("timeout clock requires a function callback");
    const requested = Number(delay);
    if (!Number.isFinite(requested) || requested > 2147483647) throw new RangeError("unsupported timeout delay");
    const id = nextId--;
    const timer = { id, callback, args, due: now + Math.max(0, Math.trunc(requested)), order: order++ };
    pending.set(id, timer);
    record("scheduled", { id, requested, due: timer.due });
    return id;
  }
  function clearTimeoutControlled(id) {
    if (pending.delete(Number(id))) record("cancelled", { id: Number(id) });
    else nativeClear.call(scope, id); // Pre-install native timers still cancel.
  }
  scope.setTimeout = setTimeoutControlled;
  scope.clearTimeout = clearTimeoutControlled;
  return {
    get now() {
      return now;
    },
    snapshot() {
      return {
        now,
        pending: [...pending.values()].map(({ id, due, order }) => ({ id, due, order })),
        history: [...history],
      };
    },
    async advanceTo(target) {
      if (disposed || advancing || !Number.isFinite(target) || target < now) throw new Error("invalid clock advance");
      advancing = true;
      let count = 0;
      try {
        for (;;) {
          const timer = [...pending.values()]
            .filter((t) => t.due <= target)
            .sort((a, b) => a.due - b.due || a.order - b.order)[0];
          if (!timer) break;
          if (++count > 1000) throw new Error("timeout callback bound exceeded");
          now = timer.due;
          pending.delete(timer.id);
          record("fired", { id: timer.id });
          timer.callback.apply(scope, timer.args);
          // Yield to actual microtasks and native scheduler tasks, without
          // advancing logical time or executing timers beyond the requested target.
          await task();
        }
        now = target;
      } finally {
        advancing = false;
      }
    },
    dispose() {
      if (advancing) throw new Error("cannot dispose advancing clock");
      const abandoned = [...pending.keys()];
      pending.clear();
      disposed = true;
      const restored = {};
      for (const [key, owned] of [
        ["setTimeout", setTimeoutControlled],
        ["clearTimeout", clearTimeoutControlled],
      ]) {
        restored[key] = scope[key] === owned;
        if (restored[key]) Object.defineProperty(scope, key, descriptors[key]);
      }
      return { abandoned, restored };
    },
  };
}
