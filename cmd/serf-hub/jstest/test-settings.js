const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/settings-transcript.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <dl class="settings-table" data-transcript-status-form>
      <div class="row editable">
        <dt id="lbl-status-prompt">Prompt Loaded</dt>
        <dd>
          <label class="val-toggle">
            <input type="checkbox" data-transcript-status="promptLoaded" aria-labelledby="lbl-status-prompt">
            <span class="state" aria-hidden="true">OFF</span>
          </label>
        </dd>
      </div>
      <div class="row editable">
        <dt id="lbl-status-hook-normal">Hook exits (normal only)</dt>
        <dd>
          <label class="val-toggle">
            <input type="checkbox" data-transcript-status="hookExitsNormal" aria-labelledby="lbl-status-hook-normal">
            <span class="state" aria-hidden="true">OFF</span>
          </label>
        </dd>
      </div>
    </dl>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });
  const { window } = dom;
  window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
  return window;
}

function change(input) {
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

(function main() {
  const window = makeWindow();
  window.eval(SRC);

  const prompt = window.document.querySelector('[data-transcript-status="promptLoaded"]');
  const normalHooks = window.document.querySelector('[data-transcript-status="hookExitsNormal"]');
  assert(prompt && normalHooks, "test setup missing transcript status controls");
  assert(prompt.checked === false, "prompt loaded should default off");
  assert(prompt.parentElement.querySelector(".state").textContent === "OFF", "default state label should be OFF");

  prompt.checked = true;
  change(prompt);
  const saved = JSON.parse(window.localStorage.getItem("serf-hub.transcript.systemStatus") || "{}");
  assert(saved.promptLoaded === true, "promptLoaded preference was not saved");
  assert(prompt.parentElement.querySelector(".state").textContent === "ON", "changed state label should be ON");
  assert(window.SerfToast.messages.some(m => m.kind === "success"), "settings save should show success toast");

  const restored = makeWindow();
  restored.localStorage.setItem("serf-hub.transcript.systemStatus", JSON.stringify({ hookExitsNormal: true }));
  restored.eval(SRC);
  restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
  const restoredPrompt = restored.document.querySelector('[data-transcript-status="promptLoaded"]');
  const restoredHooks = restored.document.querySelector('[data-transcript-status="hookExitsNormal"]');
  assert(restoredPrompt.checked === false, "missing promptLoaded preference should restore off");
  assert(restoredHooks.checked === true, "saved hookExitsNormal preference should restore on");
  assert(restoredHooks.parentElement.querySelector(".state").textContent === "ON", "restored state label should be ON");

  console.log("PASS — transcript status settings persist and restore");
})();
