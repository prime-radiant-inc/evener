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

(async function main() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form id="settings" data-launch-settings-root data-launch-settings-layer="global">
      <div data-launch-settings-groups></div>
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/settings/launch-serf",
  });

  const validateCalls = [];
  dom.window.SerfAppwire = {
    request() { return Promise.resolve({}); },
    validatePath(value, kind) {
      validateCalls.push({ value, kind });
      if (value.includes("missing")) {
        return Promise.resolve({ valid: false, error: "missing path: " + value });
      }
      return Promise.resolve({ valid: true, path: value.replace("/raw/", "/canonical/") });
    },
  };
  dom.window.eval(src);

  const schema = [
    {
      field: "agent",
      wireField: "agent",
      label: "Agent",
      group: "Model",
      kind: "select",
      choices: [{ value: "serf", label: "Serf" }, { value: "codex", label: "Codex" }],
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
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
      field: "reasoning_effort",
      wireField: "reasoningEffort",
      label: "Reasoning effort",
      group: "Model",
      kind: "select",
      choices: [{ value: "medium", label: "medium" }],
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_mode",
      wireField: "systemPromptMode",
      label: "System prompt source",
      group: "Prompts",
      kind: "radio",
      choices: [{ value: "", label: "Serf default" }, { value: "file", label: "Pick a file" }, { value: "inline", label: "Fill in text" }],
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_file",
      wireField: "systemPromptFile",
      label: "System prompt file",
      group: "Prompts",
      kind: "path",
      pathKind: "file",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_text",
      wireField: "systemPromptText",
      label: "System prompt text",
      group: "Prompts",
      kind: "multilineText",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_append_mode",
      wireField: "systemPromptAppendMode",
      label: "Append to system prompt",
      group: "Prompts",
      kind: "radio",
      choices: [{ value: "", label: "Do not append anything" }, { value: "file", label: "Pick a file" }, { value: "inline", label: "Fill in text" }],
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_append_file",
      wireField: "systemPromptAppendFile",
      label: "Append file",
      group: "Prompts",
      kind: "path",
      pathKind: "file",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "system_prompt_append_text",
      wireField: "systemPromptAppendText",
      label: "Append text",
      group: "Prompts",
      kind: "multilineText",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "plugin_dirs",
      wireField: "pluginDirs",
      label: "Plugin directories",
      group: "Resources",
      kind: "pathList",
      pathKind: "dir",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
      driverSupport: { serf: true },
    },
    {
      field: "model_fallbacks",
      wireField: "modelFallbacks",
      label: "Model fallbacks",
      group: "Resources",
      kind: "modelList",
      defaultableLayers: ["global", "project"],
      perLaunch: true,
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
    current: {
      model: "openai/gpt-5",
      pluginDirs: ["/missing/plugin", "/raw/plugin"],
      modelFallbacks: [],
      traceFile: "/tmp/trace.out",
      env: { FOO: "bar" },
    },
    includeEnvFallbacks: false,
  });

  assert(root.querySelector("[data-launch-settings-groups]"), "settings groups root should remain present");
  assert(root.querySelector('[data-launch-wire-field="model"]'), "model control should render");
  assert(root.querySelector('[data-launch-wire-field="traceFile"]').dataset.launchPathKind === "outputFile",
    "pathKind should stay on rendered path control");
  assert(root.querySelector('[data-launch-wire-field="traceFile"]').value === "/tmp/trace.out",
    "path current value should populate");
  assert(root.querySelectorAll('[data-launch-wire-field="systemPromptMode"]').length === 3,
    "system prompt source should render as a composite radio group");
  assert(root.querySelectorAll('[data-launch-option="system_prompt_file"]').length === 0,
    "system prompt file should not render as a standalone row");
  const promptFile = root.querySelector('input[data-launch-wire-field="systemPromptFile"]');
  const promptText = root.querySelector('textarea[data-launch-wire-field="systemPromptText"]');
  assert(promptFile && promptText,
    "system prompt composite should include the file input and textarea");
  assert(root.textContent.includes("Serf default"),
    "system prompt composite should use the schema label for the default option");
  assert(root.textContent.includes("Do not append anything"),
    "append composite should use the schema label for the default option");
  promptFile.value = "/missing/prompt.md";
  promptText.value = "inline prompt";
  const defaultPromptOut = dom.window.LaunchConfigControls.collect(root);
  assert(!Object.prototype.hasOwnProperty.call(defaultPromptOut, "systemPromptFile") &&
    !Object.prototype.hasOwnProperty.call(defaultPromptOut, "systemPromptText"),
    "collect should ignore inactive system prompt dependent controls");
  root.querySelector('input[data-launch-wire-field="systemPromptMode"][value="inline"]').checked = true;
  const inlinePromptOut = dom.window.LaunchConfigControls.collect(root);
  assert(inlinePromptOut.systemPromptMode === "inline" && inlinePromptOut.systemPromptText === "inline prompt" &&
    !Object.prototype.hasOwnProperty.call(inlinePromptOut, "systemPromptFile"),
    "collect should include only the active inline system prompt value");
  root.querySelector('input[data-launch-wire-field="systemPromptMode"][value="file"]').checked = true;
  const inactiveValid = await dom.window.LaunchConfigControls.validate(root);
  assert(!inactiveValid, "validate should check the active system prompt file input");
  promptFile.value = "";
  promptFile.setCustomValidity("");
  delete promptFile.dataset.launchInvalid;
  root.querySelector('input[data-launch-wire-field="systemPromptMode"][value="inline"]').checked = true;
  const modelFallbackWrap = root.querySelector('[data-launch-kind="modelList"][data-launch-wire-field="modelFallbacks"]');
  assert(modelFallbackWrap.querySelector("[data-launch-explicit-empty]").checked,
    "explicit empty modelFallbacks should render no-fallbacks affordance as checked");
  assert(!root.textContent.includes("SERF_MODEL"), "settings controls must not render env fallback values");
  assert(!root.querySelector("[data-launch-env-fallback]"), "settings controls must not expose env fallback nodes");

  const invalid = await dom.window.LaunchConfigControls.validate(root);
  assert(!invalid, "validate should block invalid pathList entries");
  assert(validateCalls.some(c => c.value === "/missing/plugin" && c.kind === "dir"),
    "pathList validation should use schema pathKind");
  const pluginWrap = root.querySelector('[data-launch-kind="pathList"][data-launch-wire-field="pluginDirs"]');
  const pluginError = pluginWrap.querySelector("[data-launch-validation-error]");
  assert(pluginError && !pluginError.hidden && pluginError.textContent.includes("missing path"),
    "pathList validation should render inline error");

  pluginWrap.querySelector('[data-value="/missing/plugin"]').remove();
  const valid = await dom.window.LaunchConfigControls.validate(root);
  assert(valid, "validate should pass after invalid pathList entry is removed");
  assert(validateCalls.some(c => c.value === "/tmp/trace.out" && c.kind === "output-file"),
    "outputFile scalar validation should map to output-file");
  assert(pluginWrap.querySelector('[data-value="/canonical/plugin"]'),
    "valid pathList entries should update to canonical validated path");

  const pendingPath = pluginWrap.querySelector(".settings-add-row input");
  const addPath = pluginWrap.querySelector(".settings-add-row button");
  pendingPath.value = "/missing/pending";
  addPath.click();
  await new Promise(resolve => dom.window.setTimeout(resolve, 0));
  assert(!pendingPath.checkValidity(), "invalid Add click should leave pending path input invalid");
  assert(!root.checkValidity(), "stale invalid pending path should block native form validation before it is cleared");
  assert(pluginError && !pluginError.hidden && pluginError.textContent.includes("missing path"),
    "invalid Add click should keep inline validation visible");
  pendingPath.value = "";
  pendingPath.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  assert(pendingPath.checkValidity(), "clearing pending path input should clear stale custom validity");
  assert(root.checkValidity(), "cleared pending path input should not block native form validation");
  assert(pluginError.hidden, "clearing pending path input should clear inline validation");
  pendingPath.value = "/raw/pending";
  pendingPath.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  assert(pendingPath.checkValidity(), "editing pending path input should clear stale custom validity");

  const envError = new Error('env key "OPENAI_API_KEY" looks like a credential; route through serf/auth/apiKey/set');
  assert(dom.window.LaunchConfigControls.showBackendError(root, envError),
    "credential env backend errors should be recognized");
  const envWrap = root.querySelector('[data-launch-kind="envMap"][data-launch-wire-field="env"]');
  const envInline = envWrap.querySelector("[data-launch-validation-error]");
  assert(envInline && !envInline.hidden && envInline.textContent.includes("OPENAI_API_KEY"),
    "credential env backend errors should render inline near env control");

  const out = dom.window.LaunchConfigControls.collect(root);
  assert(out.model === "openai/gpt-5", "collect should include populated model");
  assert(out.traceFile === "/tmp/trace.out", "collect should include output-file field with wire name");
  assert(out.pluginDirs && out.pluginDirs[0] === "/canonical/plugin", "collect should include canonicalized path list");
  assert(Array.isArray(out.modelFallbacks) && out.modelFallbacks.length === 0,
    "collect should preserve explicit empty modelFallbacks");
  assert(out.env && out.env.FOO === "bar", "collect should include env map values");

  const inheritRoot = dom.window.document.createElement("form");
  inheritRoot.dataset.launchSettingsRoot = "";
  inheritRoot.dataset.launchSettingsLayer = "global";
  dom.window.document.body.appendChild(inheritRoot);
  dom.window.LaunchConfigControls.render(inheritRoot, {
    mode: "settings",
    layer: "global",
    options: schema,
    current: {},
    includeEnvFallbacks: false,
  });
  const inheritOut = dom.window.LaunchConfigControls.collect(inheritRoot);
  assert(!Object.prototype.hasOwnProperty.call(inheritOut, "modelFallbacks"),
    "collect should omit unset modelFallbacks so settings inherit defaults");

  const spawnRoot = dom.window.document.createElement("form");
  spawnRoot.dataset.launchAdvancedRoot = "";
  dom.window.document.body.appendChild(spawnRoot);
  dom.window.LaunchConfigControls.render(spawnRoot, {
    mode: "spawn",
    options: schema,
    current: { modelFallbacks: [] },
    includeEnvFallbacks: false,
  });
  assert(!spawnRoot.querySelector('[data-launch-wire-field="agent"]'),
    "spawn advanced controls should omit top-pane agent");
  assert(!spawnRoot.querySelector('[data-launch-wire-field="model"]'),
    "spawn advanced controls should omit top-pane model");
  assert(!spawnRoot.querySelector('[data-launch-wire-field="reasoningEffort"]'),
    "spawn advanced controls should omit top-pane reasoning effort");
  assert(!spawnRoot.querySelector("[data-launch-explicit-empty]"),
    "spawn controls should not render the settings-only no-fallbacks affordance");
  const spawnOut = dom.window.LaunchConfigControls.collect(spawnRoot);
  assert(!Object.prototype.hasOwnProperty.call(spawnOut, "modelFallbacks"),
    "spawn collect with no fallback rows should keep existing empty-list omission behavior");
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
