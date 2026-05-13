const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});

dom.window.eval(spawnSrc);

assert(dom.window.SerfSpawn, "spawn helpers were not exported");
assert(
  dom.window.SerfSpawn.sessionPath({ ref: "local:01LOCAL", session_id: "01LOCAL" }) === "/s/01LOCAL",
  "local spawn should preserve bare session route",
);
assert(
  dom.window.SerfSpawn.sessionPath({ ref: "local:01LOCAL", session_id: "local:01LOCAL" }) === "/s/01LOCAL",
  "local spawn should canonicalize local-ref session IDs to bare session routes",
);
assert(
  dom.window.SerfSpawn.sessionPath({ ref: "codex:th_codex", session_id: "th_codex" }) === "/s/codex%3Ath_codex",
  "remote spawn should navigate by canonical ref",
);

const formDom = new JSDOM(`<!DOCTYPE html><html><body>
  <form data-spawn-form>
    <div id="spawn-chips">
      <button class="chip" type="button" data-chip="harness">
        <span class="chip-value" data-chip-value-harness>serf</span>
      </button>
      <button class="chip" type="button" data-chip="model">
        <span class="chip-value" data-chip-value-model>(pick a model)</span>
      </button>
      <button class="chip" type="button" data-chip="branch">
        <span class="chip-value" data-chip-value-branch>(default)</span>
      </button>
    </div>
    <textarea name="prompt"></textarea>
    <input type="hidden" name="harness" value="serf">
    <input type="hidden" data-harness-option value="serf" data-label="serf">
    <input type="hidden" data-harness-option value="codex" data-label="codex">
    <input type="hidden" name="model" value="">
    <input type="hidden" name="working_dir" value="">
    <input type="hidden" name="branch" value="">
    <input type="hidden" name="access_mode" value="full">
    <input type="hidden" name="agent" value="default">
    <input type="hidden" name="reasoning_effort" value="">
    <button class="spawn-btn" type="submit">spawn</button>
  </form>
  <a data-recent-prompt="ship the rename"></a>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});

let listModelsCalls = 0;
let listModelsParams = null;
formDom.window.SerfAppwire = {
  listModels(params) {
    listModelsCalls++;
    listModelsParams = params || {};
    return {
      then(resolve) {
        resolve([{ provider: "codex", model: "gpt-5.3-codex" }]);
        return { catch() {} };
      },
    };
  },
};
formDom.window.localStorage.setItem("serf-hub.spawn-defaults.global.model", "openai/gpt-5.2");
formDom.window.eval(spawnSrc);
formDom.window.document.dispatchEvent(new formDom.window.Event("DOMContentLoaded", { bubbles: true }));

const modelDisplay = () => formDom.window.document.querySelector("[data-chip-value-model]").textContent.trim();
const modelValue = () => formDom.window.document.querySelector('input[name="model"]').value;
assert(modelDisplay() === "openai/gpt-5.2", "serf spawn should apply stored serf model default");

formDom.window.document.querySelector('button[data-chip="harness"]').click();
formDom.window.document.querySelectorAll(".chip-picker-option")[1].click();
assert(formDom.window.document.querySelector('input[name="harness"]').value === "codex", "harness should switch to codex");
assert(modelValue() === "", "codex harness should clear stale serf model value");
assert(modelDisplay() === "codex default", "codex harness should show codex default model label");

formDom.window.document.querySelector('button[data-chip="model"]').click();
assert(listModelsCalls === 1, "codex model picker should fetch harness-scoped model list");
assert(listModelsParams.harness === "codex", "codex model picker should pass selected harness");
assert(
  Array.from(formDom.window.document.querySelectorAll(".chip-picker-model-name")).map(el => el.textContent.trim()).join(",") === "gpt-5.3-codex",
  "codex model picker should offer codex source models",
);
formDom.window.document.querySelector(".chip-picker-model").click();
assert(modelValue() === "gpt-5.3-codex", "codex model picker should submit raw codex model id");
assert(modelDisplay() === "codex/gpt-5.3-codex", "codex model picker should display source/model relationship");

let promptCalled = false;
formDom.window.prompt = () => {
  promptCalled = true;
  return "prompt-dialog-branch";
};
formDom.window.document.querySelector('button[data-chip="branch"]').click();
assert(!promptCalled, "branch picker should not call window.prompt");
const branchInput = formDom.window.document.querySelector(".chip-picker-search");
assert(branchInput, "branch picker should render an inline input");
branchInput.value = "feature/no-dialog";
branchInput.dispatchEvent(new formDom.window.KeyboardEvent("keydown", {
  key: "Enter",
  bubbles: true,
  cancelable: true,
}));
assert(formDom.window.document.querySelector('input[name="branch"]').value === "feature/no-dialog", "branch input should set hidden branch");
assert(
  formDom.window.document.querySelector("[data-chip-value-branch]").textContent.trim() === "feature/no-dialog",
  "branch input should update chip text",
);

formDom.window.document.querySelector("[data-recent-prompt]").click();
assert(
  formDom.window.document.querySelector('textarea[name="prompt"]').value === "ship the rename",
  "recent prompt should prefill the prompt textarea",
);

let alertCalled = false;
formDom.window.alert = () => { alertCalled = true; };
formDom.window.SerfAppwire = null;
let sentSpawnBody = null;
formDom.window.fetch = (_url, opts) => {
  sentSpawnBody = JSON.parse(opts.body);
  return Promise.resolve({
    ok: false,
    text: () => Promise.resolve(JSON.stringify({
      error: "start codex app-server: no such file or directory",
      code: -32014,
      serfErrorInfo: "hubLaunch",
    })),
  });
};
formDom.window.document.querySelector("[data-spawn-form]").dispatchEvent(new formDom.window.Event("submit", {
  bubbles: true,
  cancelable: true,
}));

setTimeout(() => {
  assert(sentSpawnBody.prompt === "ship the rename", "spawn request should send prompt field");
  assert(!Object.prototype.hasOwnProperty.call(sentSpawnBody, "task"), "spawn request should not send legacy task field");
  assert(!alertCalled, "spawn failure should not call window.alert");
  const diagnostic = formDom.window.document.querySelector('[role="alert"]');
  assert(diagnostic, "spawn failure should render an in-page diagnostic");
  assert(
    diagnostic.textContent.includes("start codex app-server: no such file or directory"),
    "spawn diagnostic should show structured error message, got " + diagnostic.textContent,
  );
  const spawnButton = formDom.window.document.querySelector(".spawn-btn");
  assert(!spawnButton.disabled, "spawn button should be re-enabled after failed spawn");
  console.log("PASS: spawn navigation and harness-aware model defaults");
}, 0);
