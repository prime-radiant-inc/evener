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

console.log("PASS: diagnostics taxonomy and render component");
