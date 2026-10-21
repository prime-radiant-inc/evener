// External WebSocket fault injection shared by the browser and native journeys.
// Every delivered response still comes from the real hub. Holds are confined to
// the transport boundary: an RPC request still reaches the hub, only its
// response (or an independent notification) is withheld from the page.
export function installRetryWireFault() {
  const Original = globalThis.WebSocket;
  const state = {
    mode: "normal",
    armed: false,
    requests: [],
    dropped: 0,
    connections: 0,
    // Per-method RPC responses and independent notifications the driver has
    // asked this page to withhold. Nothing is fabricated: release delivers the
    // hub's own frame unchanged.
    heldResponses: [],
    heldNotifications: [],
    responseHolds: [],
    notificationHolds: [],
    failNext: {},
    failed: 0,
    methodCounts: {},
  };
  // A reload installs a fresh fault instance before the app boots. Config the
  // driver wrote to localStorage survives the navigation; it is consumed once
  // so later loads start clean.
  try {
    const config = JSON.parse(globalThis.localStorage?.getItem("__retryWireConfig") ?? "null");
    if (config) {
      state.responseHolds = Array.isArray(config.responseHolds) ? [...config.responseHolds] : [];
      state.notificationHolds = Array.isArray(config.notificationHolds) ? [...config.notificationHolds] : [];
      state.failNext = config.failNext && typeof config.failNext === "object" ? { ...config.failNext } : {};
      globalThis.localStorage.removeItem("__retryWireConfig");
    }
  } catch {
    // Storage may be unavailable; a missing config just means no holds.
  }
  state.holdResponses = (...methods) => {
    for (const method of methods) if (!state.responseHolds.includes(method)) state.responseHolds.push(method);
  };
  state.holdNotifications = (...methods) => {
    for (const method of methods) if (!state.notificationHolds.includes(method)) state.notificationHolds.push(method);
  };
  state.failNextResponse = (method, count = 1) => {
    state.failNext[method] = (state.failNext[method] ?? 0) + count;
  };
  state.clearHolds = () => {
    state.responseHolds = [];
    state.notificationHolds = [];
    state.heldResponses = [];
    state.heldNotifications = [];
  };
  // With no method arguments every held frame is released. Named methods
  // release only their own frames, so acceptance and the independent history
  // reads/events that would prove the continuation can be observed separately.
  state.releaseResponses = (...methods) => {
    const wanted = new Set(methods);
    const keep = [];
    for (const held of state.heldResponses) {
      if (wanted.size === 0 || wanted.has(held.method)) held.listener({ data: held.data });
      else keep.push(held);
    }
    state.heldResponses = keep;
    state.responseHolds = wanted.size === 0 ? [] : state.responseHolds.filter((method) => !wanted.has(method));
  };
  state.releaseNotifications = (...methods) => {
    const wanted = new Set(methods);
    const keep = [];
    for (const held of state.heldNotifications) {
      if (wanted.size === 0 || wanted.has(held.method)) held.listener({ data: held.data });
      else keep.push(held);
    }
    state.heldNotifications = keep;
    state.notificationHolds = wanted.size === 0 ? [] : state.notificationHolds.filter((method) => !wanted.has(method));
  };
  globalThis.__retryWire = state;
  globalThis.WebSocket = class extends Original {
    constructor(...args) {
      super(...args);
      // The method of every in-flight request, so a response is attributable
      // without inspecting its result.
      this.requestMethods = new Map();
      this.addEventListener("open", () => {
        state.connections++;
      });
    }
    set onmessage(listener) {
      super.onmessage =
        listener === null
          ? null
          : (event) => {
              const message = JSON.parse(String(event.data));
              if (message.id != null) {
                const method = this.requestMethods.get(message.id);
                this.requestMethods.delete(message.id);
                const failNext = method === undefined ? 0 : (state.failNext[method] ?? 0);
                if (failNext > 0) {
                  state.failNext[method] = failNext - 1;
                  state.failed++;
                  listener({
                    data: JSON.stringify({
                      id: message.id,
                      error: {
                        message: "retry journey transient authoritative read failure",
                        code: "fixture_transient",
                      },
                    }),
                  });
                  return;
                }
                if (method !== undefined && state.responseHolds.includes(method)) {
                  state.heldResponses.push({ method, listener, data: String(event.data) });
                  return;
                }
              }
              if (message.id == null && message.method && state.notificationHolds.includes(message.method)) {
                state.heldNotifications.push({ method: message.method, listener, data: String(event.data) });
                return;
              }
              if (this.lostReplyID !== undefined && message.id === this.lostReplyID && message.result) {
                this.lostReplyID = undefined;
                state.dropped++;
                this.close();
                return;
              }
              listener(event);
            };
    }
    get onmessage() {
      return super.onmessage;
    }
    send(data) {
      const message = JSON.parse(String(data));
      if (message.id != null && message.method) {
        this.requestMethods.set(message.id, message.method);
        state.methodCounts[message.method] = (state.methodCounts[message.method] ?? 0) + 1;
      }
      if (message.method === "turn/start" || message.method === "turn/queue") {
        state.requests.push({ method: message.method, params: message.params });
        if (state.armed) {
          state.armed = false;
          if (state.mode === "lost-reply") this.lostReplyID = message.id;
          if (state.mode === "refused") {
            super.send(
              JSON.stringify({
                ...message,
                params: { ...message.params, expectedInstanceId: "fixture-wrong-instance" },
              }),
            );
            return;
          }
        }
      }
      super.send(data);
    }
  };
  return state;
}
