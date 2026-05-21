const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const src = fs.readFileSync(path.resolve(__dirname, "../assets/launchconfig.js"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <form id="settings" data-launch-settings-root data-launch-settings-layer="global">
    <div data-launch-settings-groups></div>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/settings/launch-serf",
});

dom.window.SerfAppwire = { request() { return Promise.resolve({}); } };
dom.window.eval(src);

const schema = [
  {
    field: "model",
    wireField: "model",
    label: "Model",
    group: "Model",
    kind: "modelPicker",
    defaultableLayers: ["global", "project"],
    perLaunch: true,
    envFallback: { name: "SERF_MODEL" },
    driverSupport: { serf: true },
  },
  {
    field: "trace_file",
    wireField: "traceFile",
    label: "Trace file",
    group: "Debug Logging",
    kind: "path",
    pathKind: "outputFile",
    defaultableLayers: ["global", "project", "launch"],
    perLaunch: true,
    driverSupport: { serf: true },
  },
  {
    field: "env",
    wireField: "env",
    label: "Environment variables",
    group: "Environment",
    kind: "envMap",
    defaultableLayers: ["global", "project"],
    perLaunch: true,
    driverSupport: { serf: true },
  },
];

const root = dom.window.document.getElementById("settings");
dom.window.LaunchConfigControls.render(root, {
  mode: "settings",
  layer: "global",
  options: schema,
  current: { model: "openai/gpt-5", traceFile: "/tmp/trace.out", env: { FOO: "bar" } },
  includeEnvFallbacks: false,
});

assert(root.querySelector("[data-launch-settings-groups]"), "settings groups root should remain present");
assert(root.querySelector('[data-launch-wire-field="model"]'), "model control should render");
assert(root.querySelector('[data-launch-wire-field="traceFile"]').dataset.launchPathKind === "outputFile",
  "pathKind should stay on rendered path control");
assert(root.querySelector('[data-launch-wire-field="traceFile"]').value === "/tmp/trace.out",
  "path current value should populate");
assert(!root.textContent.includes("SERF_MODEL"), "settings controls must not render env fallback values");
assert(!root.querySelector("[data-launch-env-fallback]"), "settings controls must not expose env fallback nodes");

const out = dom.window.LaunchConfigControls.collect(root);
assert(out.model === "openai/gpt-5", "collect should include populated model");
assert(out.traceFile === "/tmp/trace.out", "collect should include output-file field with wire name");
assert(out.env && out.env.FOO === "bar", "collect should include env map values");
