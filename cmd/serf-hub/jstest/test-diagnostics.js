// Node-only coverage for assets/diagnostics.js. This intentionally avoids
// JSDOM so the diagnostic taxonomy can be tested without installing browser
// test dependencies.
const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/diagnostics.js", "utf8");

class Element {
  constructor(tag) {
    this.tag = tag;
    this.className = "";
    this.attributes = {};
    this.children = [];
    this._text = "";
    this._listeners = {};
  }

  set textContent(value) {
    this._text = String(value || "");
    this.children = [];
  }

  get textContent() {
    return this._text + this.children.map((child) => child.textContent).join("");
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  addEventListener(event, handler) {
    if (!this._listeners[event]) this._listeners[event] = [];
    this._listeners[event].push(handler);
  }

  dispatchEvent(event) {
    const handlers = this._listeners[event.type] || [];
    for (const h of handlers) h(event);
  }

  querySelectorByClass(className) {
    if (this.className.split(/\s+/).includes(className)) return this;
    for (const child of this.children) {
      const found = child.querySelectorByClass(className);
      if (found) return found;
    }
    return null;
  }
}

function load() {
  const context = {
    window: {},
    document: {
      createElement: (tag) => new Element(tag),
    },
  };
  context.globalThis = context;
  vm.createContext(context);
  vm.runInContext(SRC, context);
  return context.window.SerfDiagnostics;
}

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const diagnostics = load();

const serf = diagnostics.classify({
  severity: "error",
  message: "[error] configuration error: unknown provider: openrouter",
});
assert(serf.source === "serf", "unknown provider should classify as serf, got " + serf.source);
assert(serf.title === "Serf configuration error", "unexpected serf title: " + serf.title);
assert(!serf.message.includes("[error]"), "message should be cleaned: " + serf.message);
assert(serf.hint.includes("Hub launched Serf"), "serf hint should name hub launch config: " + serf.hint);

const provider = diagnostics.classify({
  severity: "error",
  message: "openai error (status=401): invalid API key",
});
assert(provider.source === "provider", "provider HTTP failure should classify as provider");
assert(provider.title === "Provider error", "unexpected provider title: " + provider.title);

const hub = diagnostics.classify({
  severity: "error",
  message: "daemon spawn timed out: process exited before rendezvous",
});
assert(hub.source === "hub", "spawn/rendezvous failure should classify as hub");

const rendered = diagnostics.render({
  severity: "error",
  message: "configuration error: unknown provider: openrouter",
});
assert(
  rendered.className.includes("diagnostic-source-serf"),
  "rendered component should include source class: " + rendered.className
);
assert(
  rendered.attributes.role === "alert",
  "error diagnostic should be role=alert, got " + rendered.attributes.role
);
assert(
  rendered.querySelectorByClass("diagnostic-badge").textContent === "Serf error",
  "badge text wrong: " + rendered.querySelectorByClass("diagnostic-badge").textContent
);
assert(
  rendered.textContent.includes("Hub launched Serf"),
  "rendered hint missing: " + rendered.textContent
);

// render() with action buttons (Retry turn).
let retryClicked = false;
const withActions = diagnostics.render(
  { severity: "error", source: "provider", message: "stream ended without finish event" },
  [{ label: "Retry turn", onclick: function() { retryClicked = true; } }]
);
assert(
  withActions.className.includes("diagnostic-source-provider"),
  "action card should be source-provider: " + withActions.className
);
const actionsEl = withActions.querySelectorByClass("diagnostic-actions");
assert(actionsEl !== null, "diagnostic-actions element should exist");
const btnEl = actionsEl.querySelectorByClass("diagnostic-action-btn");
assert(btnEl !== null, "diagnostic-action-btn should exist");
assert(btnEl._text === "Retry turn", "button label wrong: " + btnEl._text);

// Simulate click via dispatchEvent — verify onclick is wired through addEventListener.
btnEl.dispatchEvent({ type: "click" });
assert(retryClicked, "onclick should fire when button is clicked");

// render() with no actions — no diagnostic-actions element.
const noActions = diagnostics.render({ severity: "error", source: "provider", message: "some error" });
const noActionsEl = noActions.querySelectorByClass("diagnostic-actions");
assert(noActionsEl === null, "no diagnostic-actions when no actions passed");

// render() with actions array on the input object itself.
const inlineActions = diagnostics.render({
  severity: "error",
  source: "hub",
  message: "hub error",
  actions: [{ label: "Dismiss", onclick: function() {} }],
});
const inlineActionsEl = inlineActions.querySelectorByClass("diagnostic-actions");
assert(inlineActionsEl !== null, "inline actions on input should render diagnostic-actions");

// kata 96pr: stored source="serf" should be re-classified by message pattern
// when the message clearly matches a provider or hub failure. Legacy
// transcripts emitted before commit 05203e0 broadened isProviderFailure to
// cover stream-truncation patterns persisted source="serf" forever; the
// renderer must override on read so the e465 Reconnect/Retry button surfaces.

// Legacy stream-truncation diagnostic stored as serf → reclassify to provider.
const legacyStreamTrunc = diagnostics.classify({
  source: "serf",
  message: "stream ended without finish event",
});
assert(legacyStreamTrunc.source === "provider",
  "legacy serf-source stream truncation should reclassify to provider, got " + legacyStreamTrunc.source);
assert(legacyStreamTrunc.title === "Provider error",
  "reclassified diagnostic should use provider title, got " + legacyStreamTrunc.title);
assert(legacyStreamTrunc.hint.includes("model provider"),
  "reclassified diagnostic should use provider hint, got " + legacyStreamTrunc.hint);

// Real serf-source configuration error should stay as serf.
const realSerfConfig = diagnostics.classify({
  source: "serf",
  message: "no model: use --model provider/model",
});
assert(realSerfConfig.source === "serf",
  "real serf configuration error should stay serf, got " + realSerfConfig.source);
assert(realSerfConfig.title === "Serf configuration error",
  "real serf configuration error should keep serf title, got " + realSerfConfig.title);

// Non-serf stored sources are honored unchanged (no override of provider).
const explicitProvider = diagnostics.classify({
  source: "provider",
  message: "stream ended without finish event",
});
assert(explicitProvider.source === "provider",
  "explicit provider source should be honored, got " + explicitProvider.source);

// Heuristic fallback (no stored source) still classifies stream truncation as provider.
const heuristicProvider = diagnostics.classify({
  message: "stream ended without finish event",
});
assert(heuristicProvider.source === "provider",
  "missing source should still heuristically classify as provider, got " + heuristicProvider.source);

// Legacy hub failure stored as serf → reclassify to hub.
const legacyHub = diagnostics.classify({
  source: "serf",
  message: "rendezvous timeout",
});
assert(legacyHub.source === "hub",
  "legacy serf-source rendezvous failure should reclassify to hub, got " + legacyHub.source);
assert(legacyHub.title === "Hub error",
  "reclassified hub diagnostic should use hub title, got " + legacyHub.title);

console.log("PASS: diagnostics taxonomy and render component");
