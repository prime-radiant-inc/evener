const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");
const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");
const spawnTemplateSrc = fs.readFileSync(path.resolve(__dirname, "../templates/partials/spawn.html"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const spawnActionsIndex = spawnTemplateSrc.indexOf('<div class="spawn-actions">');
const spawnAdvancedIndex = spawnTemplateSrc.indexOf('<details class="spawn-advanced">');
const spawnAttachRowIndex = spawnTemplateSrc.indexOf('<div class="spawn-attach-row">');
const spawnAttachButtonIndex = spawnTemplateSrc.indexOf('data-attach-trigger');
const composerAttachmentsIndex = spawnTemplateSrc.indexOf('<div class="composer-attachments"');
assert(spawnActionsIndex !== -1, "spawn template should include launch actions");
assert(spawnAdvancedIndex !== -1, "spawn template should include advanced toggle");
assert(spawnAttachRowIndex !== -1, "spawn template should include attach row");
assert(spawnAttachButtonIndex !== -1, "spawn template should include attach button");
assert(composerAttachmentsIndex !== -1, "spawn template should include attachments list after controls");
assert(spawnActionsIndex < spawnAdvancedIndex, "launch button should appear above advanced toggle");
assert(spawnAttachRowIndex < spawnAttachButtonIndex, "attach button should appear inside attach row");
assert(spawnAttachButtonIndex < spawnActionsIndex, "launch button should appear after attach button");
assert(spawnActionsIndex < composerAttachmentsIndex, "launch button should share the attach row above attachments list");

(async function main() {
const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});

dom.window.eval(dirPickerSrc);
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
      <button class="btn btn-chip" type="button" data-chip="harness">
        <span class="chip-value" data-chip-value-harness>serf</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="model">
        <span class="chip-value" data-chip-value-model>(pick a model)</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="branch">
        <span class="chip-value" data-chip-value-branch>(default)</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="working_dir">
        <span class="chip-value" data-chip-value-working-dir>/tmp/project-with-oauth</span>
      </button>
    </div>
    <textarea name="prompt"></textarea>
    <input type="hidden" name="harness" value="serf">
    <input type="hidden" data-harness-option value="serf" data-label="serf">
    <input type="hidden" data-harness-option value="codex" data-label="codex">
    <input type="hidden" name="model" value="">
    <input type="hidden" name="working_dir" value="/tmp/project-with-oauth">
    <input type="hidden" name="branch" value="">
    <input type="hidden" name="access_mode" value="full">
    <input type="hidden" name="agent" value="default">
    <input type="hidden" name="reasoning_effort" value="">
    <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
  </form>
  <a data-recent-prompt="ship the rename"></a>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});

let listModelsCalls = 0;
let listModelsParams = null;
const dirCompletionPrefixes = [];
// The validate-prefilled-model fetch resolves with a list whose serf
// provider enumerates `openai/gpt-5.2`. That keeps the current cwd's
// chip valid while still exercising the sweep against retired models
// like `openai/gpt-5-mini` seeded in other per-project blobs below.
formDom.window.SerfAppwire = {
  listModels(params) {
    listModelsCalls++;
    listModelsParams = params || {};
    return {
      then(resolve) {
        resolve([
          { provider: "openai", model: "gpt-5.2" },
          { provider: "codex", model: "gpt-5.3-codex" },
        ]);
        return { catch() {} };
      },
    };
  },
  listModelsWithDiagnostics(params) {
    listModelsCalls++;
    listModelsParams = params || {};
    return {
      then(resolve) {
        resolve({
          models: [
            { provider: "openai", model: "gpt-5.2" },
            { provider: "codex", model: "gpt-5.3-codex" },
          ],
          diagnostics: [],
        });
        return { catch() {} };
      },
    };
  },
  completeDirs(prefix) {
    dirCompletionPrefixes.push(prefix);
    return Promise.resolve({
      results: [{ path: "/tmp/project-with-oauth", is_git: true }],
    });
  },
};
formDom.window.localStorage.setItem("serf-hub.spawn-defaults.global.model", "openai/gpt-5.2");

// Seed a mix of stored per-project blobs to exercise the init-time
// sweep introduced for kata hnvv:
//   - stale-only blob: only `.model`, retired → whole key removed
//   - mixed blob: `.model` retired but `.working_dir`/`.branch` intact
//   - malformed blob: bare model name → `.model` dropped, rest kept
//   - valid blob: `.model` still offered by the hub → untouched
//   - unknown-provider blob: provider not enumerated → untouched
// Per-project keys live at `serf-hub.spawn-defaults.<workingDir>` — see
// projectKey() in spawn.js. Match that scheme exactly so the sweep
// actually touches them.
const projectKeys = {
  staleOnly: "serf-hub.spawn-defaults./tmp/retired-stale",
  mixed: "serf-hub.spawn-defaults./tmp/retired-mixed",
  malformed: "serf-hub.spawn-defaults./tmp/legacy-bare",
  valid: "serf-hub.spawn-defaults./tmp/still-good",
  unknown: "serf-hub.spawn-defaults./tmp/oauth-anthropic",
};
formDom.window.localStorage.setItem(projectKeys.staleOnly,
  JSON.stringify({ model: "openai/gpt-5-mini" }));
formDom.window.localStorage.setItem(projectKeys.mixed,
  JSON.stringify({ model: "openai/gpt-5-mini", working_dir: "/tmp/retired-mixed", branch: "main" }));
formDom.window.localStorage.setItem(projectKeys.malformed,
  JSON.stringify({ model: "gpt-5-bare", working_dir: "/tmp/legacy-bare" }));
formDom.window.localStorage.setItem(projectKeys.valid,
  JSON.stringify({ model: "openai/gpt-5.2", working_dir: "/tmp/still-good" }));
formDom.window.localStorage.setItem(projectKeys.unknown,
  JSON.stringify({ model: "anthropic/claude-mystery", working_dir: "/tmp/oauth-anthropic" }));
// Unrelated key with a different prefix — the sweep should not touch
// this.
const unrelatedKey = "some-other-app.spawn-defaults./tmp/retired";
formDom.window.localStorage.setItem(unrelatedKey,
  JSON.stringify({ model: "openai/gpt-5-mini" }));
// Global scalar keys: the sweep handles `global.model` explicitly but
// must NOT JSON.parse `global.working_dir` / `global.last-working-dir`
// (those are plain strings, not blobs). Seed `global.working_dir`
// different from the form's server-provided value; loadDefaults must not
// override a pre-filled ?dir value with this global sticky default.
formDom.window.localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/tmp/global-sticky-dir");
formDom.window.localStorage.setItem("serf-hub.spawn-defaults.global.last-working-dir", "/tmp/some-other-dir");

formDom.window.eval(dirPickerSrc);
formDom.window.eval(spawnSrc);
formDom.window.document.dispatchEvent(new formDom.window.Event("DOMContentLoaded", { bubbles: true }));

// Sweep runs inside the listModels promise; the stub above resolves
// synchronously so the asserts can be checked immediately after init.
assert(formDom.window.localStorage.getItem(projectKeys.staleOnly) === null,
  "sweep should remove the entire blob when only the stale .model field remained");
const mixedAfter = JSON.parse(formDom.window.localStorage.getItem(projectKeys.mixed) || "null");
assert(mixedAfter && !("model" in mixedAfter) && mixedAfter.working_dir === "/tmp/retired-mixed" && mixedAfter.branch === "main",
  "sweep should drop stale .model but preserve sibling defaults in mixed blob, got " + JSON.stringify(mixedAfter));
const malformedAfter = JSON.parse(formDom.window.localStorage.getItem(projectKeys.malformed) || "null");
assert(malformedAfter && !("model" in malformedAfter) && malformedAfter.working_dir === "/tmp/legacy-bare",
  "sweep should drop malformed bare-model entry but keep siblings, got " + JSON.stringify(malformedAfter));
const validAfter = JSON.parse(formDom.window.localStorage.getItem(projectKeys.valid) || "null");
assert(validAfter && validAfter.model === "openai/gpt-5.2",
  "sweep should leave valid stored models alone, got " + JSON.stringify(validAfter));
const unknownAfter = JSON.parse(formDom.window.localStorage.getItem(projectKeys.unknown) || "null");
assert(unknownAfter && unknownAfter.model === "anthropic/claude-mystery",
  "sweep should leave models from unenumerated providers alone, got " + JSON.stringify(unknownAfter));
const unrelatedAfter = JSON.parse(formDom.window.localStorage.getItem(unrelatedKey) || "null");
assert(unrelatedAfter && unrelatedAfter.model === "openai/gpt-5-mini",
  "sweep must only match keys with the `serf-hub.spawn-defaults.` prefix, got " + JSON.stringify(unrelatedAfter));
// Global scalar keys must survive the sweep untouched.
assert(formDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.working_dir") === "/tmp/global-sticky-dir",
  "sweep must not touch global.working_dir scalar");
assert(formDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") === "/tmp/some-other-dir",
  "sweep must not touch global.last-working-dir scalar");
assert(formDom.window.document.querySelector('input[name="working_dir"]').value === "/tmp/project-with-oauth",
  "server-provided working_dir must not be overwritten by global sticky default");

const workingDirChip = formDom.window.document.querySelector('button[data-chip="working_dir"]');
assert(workingDirChip, "working_dir chip should exist in spawn chips test fixture");
workingDirChip.click();
await new Promise((r) => setTimeout(r, 0));
assert(formDom.window.document.querySelector(".chip-picker-dir"), "working_dir chip should open shared directory picker");
assert(dirCompletionPrefixes[0] === "/tmp/project-with-oauth",
  "working_dir picker should fetch initial suggestions for current chip value, got " + dirCompletionPrefixes[0]);
formDom.window.document.querySelector(".chip-picker-dir-row").click();
assert(formDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") === "/tmp/project-with-oauth",
  "clicking working_dir suggestion should persist last working directory");

const staleModelDom = new JSDOM(`<!DOCTYPE html><html><body>
  <form data-spawn-form>
    <button class="btn btn-chip" type="button" data-chip="harness"><span class="chip-value" data-chip-value-harness>serf</span></button>
    <button class="btn btn-chip" type="button" data-chip="model"><span class="chip-value" data-chip-value-model>(pick a model)</span></button>
    <textarea name="prompt"></textarea>
    <input type="hidden" name="harness" value="serf">
    <input type="hidden" data-harness-option value="serf" data-label="serf">
    <input type="hidden" name="model" value="">
    <input type="hidden" name="working_dir" value="/tmp/current-stale">
    <input type="hidden" name="branch" value="">
    <input type="hidden" name="access_mode" value="full">
    <input type="hidden" name="agent" value="default">
    <input type="hidden" name="reasoning_effort" value="">
    <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});
staleModelDom.window.SerfAppwire = {
  listModels() {
    return {
      then(resolve) {
        resolve([{ provider: "openai", model: "gpt-5.2" }]);
        return { catch() {} };
      },
    };
  },
};
staleModelDom.window.localStorage.setItem("serf-hub.spawn-defaults.global.model", "openai/gpt-5.2");
staleModelDom.window.localStorage.setItem("serf-hub.spawn-defaults./tmp/current-stale",
  JSON.stringify({ model: "openai/gpt-5-mini", working_dir: "/tmp/current-stale" }));
staleModelDom.window.eval(dirPickerSrc);
staleModelDom.window.eval(spawnSrc);
staleModelDom.window.document.dispatchEvent(new staleModelDom.window.Event("DOMContentLoaded", { bubbles: true }));
assert(staleModelDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.model") === "openai/gpt-5.2",
  "clearing stale per-project model must preserve a different valid global model default");
const staleCurrentAfter = JSON.parse(staleModelDom.window.localStorage.getItem("serf-hub.spawn-defaults./tmp/current-stale") || "null");
assert(staleCurrentAfter && !("model" in staleCurrentAfter) && staleCurrentAfter.working_dir === "/tmp/current-stale",
  "clearing stale per-project model should only drop the matching model, got " + JSON.stringify(staleCurrentAfter));

const modelDisplay = () => formDom.window.document.querySelector("[data-chip-value-model]").textContent.trim();
const modelValue = () => formDom.window.document.querySelector('input[name="model"]').value;
// abbreviateModel strips the "openai/" prefix so the chip shows the short form.
assert(modelDisplay() === "gpt-5.2", "serf spawn should apply stored serf model default (abbreviated)");

// validatePrefilledModel calls listModels at init when the chip has a
// pre-filled value; reset the counter so subsequent picker-open assertions
// measure user-triggered fetches, not init-time validation.
listModelsCalls = 0;
listModelsParams = null;

formDom.window.document.querySelector('button[data-chip="model"]').click();
assert(listModelsCalls === 1, "serf model picker should fetch launch-scoped model list");
assert(listModelsParams.cwd === "/tmp/project-with-oauth", "serf model picker should pass selected working directory");
formDom.window.document.querySelector(".chip-picker").remove();

formDom.window.document.querySelector('button[data-chip="harness"]').click();
formDom.window.document.querySelectorAll(".chip-picker-option")[1].click();
assert(formDom.window.document.querySelector('input[name="harness"]').value === "codex", "harness should switch to codex");
assert(modelValue() === "", "codex harness should clear stale serf model value");
assert(modelDisplay() === "codex default", "codex harness should show codex default model label");

formDom.window.document.querySelector('button[data-chip="model"]').click();
assert(listModelsCalls === 2, "codex model picker should fetch harness-scoped model list");
assert(listModelsParams.harness === "codex", "codex model picker should pass selected harness");
assert(listModelsParams.cwd === "/tmp/project-with-oauth", "codex model picker should pass selected working directory");
assert(
  Array.from(formDom.window.document.querySelectorAll(".chip-picker-model-name")).map(el => el.textContent.trim()).join(",") === "gpt-5.3-codex",
  "codex model picker should offer codex source models",
);
formDom.window.document.querySelector(".chip-picker-model").click();
assert(modelValue() === "gpt-5.3-codex", "codex model picker should submit raw codex model id");
assert(modelDisplay() === "codex/gpt-5.3-codex", "codex model picker should display source/model relationship");

formDom.window.document.querySelector('button[data-chip="harness"]').click();
formDom.window.document.querySelectorAll(".chip-picker-option")[0].click();
assert(formDom.window.document.querySelector('input[name="harness"]').value === "serf", "harness should switch back to serf");
assert(modelValue() === "", "switching back to serf should clear raw codex model value");
assert(modelDisplay() === "(pick a model)", "switching back to serf should show serf model placeholder");

// The picker surfaces launch-check diagnostics so a configured provider that
// failed to list (bad key, network, down) is shown with a reason instead of
// silently vanishing from the model list.
const diagModelDom = new JSDOM(`<!DOCTYPE html><html><body>
  <form data-spawn-form>
    <button class="btn btn-chip" type="button" data-chip="harness"><span class="chip-value" data-chip-value-harness>serf</span></button>
    <button class="btn btn-chip" type="button" data-chip="model"><span class="chip-value" data-chip-value-model>(pick a model)</span></button>
    <textarea name="prompt"></textarea>
    <input type="hidden" name="harness" value="serf">
    <input type="hidden" data-harness-option value="serf" data-label="serf">
    <input type="hidden" name="model" value="">
    <input type="hidden" name="working_dir" value="/tmp/diag-project">
    <input type="hidden" name="branch" value="">
    <input type="hidden" name="access_mode" value="full">
    <input type="hidden" name="agent" value="default">
    <input type="hidden" name="reasoning_effort" value="">
    <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});
diagModelDom.window.SerfAppwire = {
  listModels() {
    return { then(resolve) { resolve([{ provider: "openai", model: "gpt-5.5" }]); return { catch() {} }; } };
  },
  listModelsWithDiagnostics() {
    return {
      then(resolve) {
        resolve({
          models: [{ provider: "openai", model: "gpt-5.5" }],
          diagnostics: [{ provider: "kimi", message: "list models: HTTP 401", hint: "check credentials" }],
        });
        return { catch() {} };
      },
    };
  },
};
diagModelDom.window.eval(dirPickerSrc);
diagModelDom.window.eval(spawnSrc);
diagModelDom.window.document.dispatchEvent(new diagModelDom.window.Event("DOMContentLoaded", { bubbles: true }));
diagModelDom.window.document.querySelector('button[data-chip="model"]').click();
const diagRows = Array.from(diagModelDom.window.document.querySelectorAll(".chip-picker-diagnostic")).map(el => el.textContent);
assert(diagRows.length === 1, "picker should render one diagnostic row, got " + diagRows.length);
assert(diagRows[0].includes("kimi") && diagRows[0].includes("list models: HTTP 401"),
  "diagnostic row should name the unavailable provider and its reason, got " + JSON.stringify(diagRows[0]));
assert(diagModelDom.window.document.querySelector(".chip-picker-model-name"),
  "picker should still render available models alongside diagnostics");

// The effort chip offers the SELECTED model's reasoning-effort levels (per
// model), not a static list, plus (default)/none.
const effortDom = new JSDOM(`<!DOCTYPE html><html><body>
  <form data-spawn-form>
    <button class="btn btn-chip" type="button" data-chip="harness"><span class="chip-value" data-chip-value-harness>serf</span></button>
    <button class="btn btn-chip" type="button" data-chip="model"><span class="chip-value" data-chip-value-model>claude-opus-4-6</span></button>
    <button class="btn btn-chip" type="button" data-chip="reasoning_effort"><span class="chip-value" data-chip-value-reasoning_effort>(default)</span></button>
    <textarea name="prompt"></textarea>
    <input type="hidden" name="harness" value="serf">
    <input type="hidden" data-harness-option value="serf" data-label="serf">
    <input type="hidden" name="model" value="anthropic/claude-opus-4-6">
    <input type="hidden" name="reasoning_effort" value="">
    <input type="hidden" name="working_dir" value="/tmp/effort">
    <input type="hidden" name="branch" value="">
    <input type="hidden" name="access_mode" value="full">
    <input type="hidden" name="agent" value="default">
    <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/new",
});
effortDom.window.SerfAppwire = {
  listModels() {
    return { then(resolve) { resolve([{ provider: "anthropic", model: "claude-opus-4-6" }]); return { catch() {} }; } };
  },
  listModelsWithDiagnostics() {
    return {
      then(resolve) {
        resolve({
          models: [{ provider: "anthropic", model: "claude-opus-4-6", reasoning_effort_levels: ["low", "medium", "high", "max"] }],
          diagnostics: [],
        });
        return { catch() {} };
      },
    };
  },
};
effortDom.window.eval(dirPickerSrc);
effortDom.window.eval(spawnSrc);
effortDom.window.document.dispatchEvent(new effortDom.window.Event("DOMContentLoaded", { bubbles: true }));
effortDom.window.document.querySelector('button[data-chip="reasoning_effort"]').click();
const effortOptions = Array.from(effortDom.window.document.querySelectorAll(".chip-picker .chip-picker-option")).map(el => el.textContent);
assert(effortOptions.join(",") === "(default),low,medium,high,max,none",
  "effort picker should list the model's levels + default/none, got " + JSON.stringify(effortOptions));
assert(!effortOptions.includes("minimal"),
  "effort picker should be per-model (claude-opus-4-6 has no 'minimal'), got " + JSON.stringify(effortOptions));
Array.from(effortDom.window.document.querySelectorAll(".chip-picker-option")).find(el => el.textContent === "max").click();
assert(effortDom.window.document.querySelector('input[name="reasoning_effort"]').value === "max",
  "selecting an effort level should set the hidden reasoning_effort input");
assert(effortDom.window.document.querySelector("[data-chip-value-reasoning_effort]").textContent === "max",
  "selecting an effort level should update the chip display");

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

let advancedModelStartBody = null;
formDom.window.SerfAppwire = {
  startThread(body) {
    advancedModelStartBody = body;
    return Promise.reject(new Error("advanced model test stop"));
  },
};
formDom.window.document.querySelector('input[name="model"]').value = "openai/chip-model";
const advancedModelInput = formDom.window.document.createElement("input");
advancedModelInput.type = "hidden";
advancedModelInput.dataset.launchWireField = "model";
advancedModelInput.dataset.launchKind = "modelPicker";
advancedModelInput.value = "openai/advanced-model";
formDom.window.document.querySelector("[data-spawn-form]").appendChild(advancedModelInput);
formDom.window.document.querySelector('textarea[name="prompt"]').value = "use advanced model";
formDom.window.document.querySelector("[data-spawn-form]").dispatchEvent(new formDom.window.Event("submit", {
  bubbles: true,
  cancelable: true,
}));
assert(advancedModelStartBody && advancedModelStartBody.model === "openai/advanced-model",
  "advanced schema model should win over chip model in appwire start payload");
assert(advancedModelStartBody.launch_overrides && advancedModelStartBody.launch_overrides.model === "openai/advanced-model",
  "advanced schema model should remain present in launch overrides");
assert(formDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.model") === "openai/chip-model",
  "advanced per-launch model must not be persisted as sticky chip default");
advancedModelInput.remove();
formDom.window.document.querySelector('input[name="model"]').value = "";

// Submitting with an empty prompt should be blocked by the defensive
// guard and surface an in-page diagnostic rather than firing a request.
let blockedFetchCalled = false;
formDom.window.fetch = () => {
  blockedFetchCalled = true;
  return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
};
const savedAppwire = formDom.window.SerfAppwire;
formDom.window.SerfAppwire = null;
formDom.window.document.querySelector('textarea[name="prompt"]').value = "   \n  ";
formDom.window.document.querySelector("[data-spawn-form]").dispatchEvent(new formDom.window.Event("submit", {
  bubbles: true,
  cancelable: true,
}));
assert(!blockedFetchCalled, "empty/whitespace-only prompt should not trigger a spawn request");
const emptyDiagnostic = formDom.window.document.querySelector("[data-spawn-error]");
assert(emptyDiagnostic, "empty-prompt submit should render an in-page diagnostic");
assert(
  emptyDiagnostic.textContent.toLowerCase().includes("prompt is empty"),
  "empty-prompt diagnostic should explain the issue, got " + emptyDiagnostic.textContent,
);

let imageOnlySpawnBody = null;
formDom.window.fetch = (_url, opts) => {
  imageOnlySpawnBody = JSON.parse(opts.body);
  return Promise.resolve({ ok: false, text: () => Promise.resolve("image-only test stop") });
};
const pathListWrapper = formDom.window.document.createElement("div");
pathListWrapper.dataset.launchPathKind = "dir";
pathListWrapper.dataset.launchWireField = "skillsDirs";
formDom.window.document.querySelector("[data-spawn-form]").appendChild(pathListWrapper);
formDom.window.document.querySelector('textarea[name="prompt"]').value = "   \n  ";
formDom.window.document.querySelector("[data-spawn-form]").__composerPasteState = {
  items: [{ type: "image", mediaType: "image/png", data: new Uint8Array([1, 2, 3]), name: "shot.png" }],
};
formDom.window.document.querySelector("[data-spawn-form]").dispatchEvent(new formDom.window.Event("submit", {
  bubbles: true,
  cancelable: true,
}));
assert(imageOnlySpawnBody && imageOnlySpawnBody.prompt === "   \n  ",
  "image-only spawn should submit even with whitespace prompt and path-list wrapper hooks");
assert(imageOnlySpawnBody.items && imageOnlySpawnBody.items.length === 1,
  "image-only spawn should include image items");
pathListWrapper.remove();
formDom.window.document.querySelector("[data-spawn-form]").__composerPasteState = { items: [] };
formDom.window.SerfAppwire = savedAppwire;

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
      serf_error_info: "hubLaunch",
    })),
  });
};
formDom.window.document.querySelector("[data-spawn-form]").dispatchEvent(new formDom.window.Event("submit", {
  bubbles: true,
  cancelable: true,
}));

await new Promise((r) => setTimeout(r, 0));
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
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
