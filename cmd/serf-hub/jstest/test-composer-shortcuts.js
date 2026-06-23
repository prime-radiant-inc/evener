// Regression tests for composer textarea shortcuts in normal workspaces vs side-pane iframes.
const { JSDOM } = require("jsdom");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function makeDOM(isPane) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01SHORT"></header>
    <div id="conversation" data-session-id="01SHORT" data-active-turn-id="turn_1" data-state="active"></div>
    <form class="workspace-input" data-input-form data-session-id="01SHORT">
      <textarea class="message-input" rows="1"></textarea>
      <button type="button" data-steer-trigger data-capability-steer="true">steer <kbd>⇧↵</kbd></button>
      <button type="submit" class="send-btn" data-capability-send="true" data-capability-queue="true">send <kbd>⌘↵</kbd></button>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, status: 202, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  window.SerfRenderer.isInPane = () => isPane;
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return dom;
}

(function testMainWorkspaceShortcutsStillSubmitAndSteer() {
  const dom = makeDOM(false);
  const { window } = dom;
  const ta = window.document.querySelector(".message-input");
  const form = window.document.querySelector("form[data-input-form]");
  const steer = window.document.querySelector("[data-steer-trigger]");
  let submitCount = 0;
  let steerCount = 0;
  form.requestSubmit = () => { submitCount++; };
  steer.addEventListener("click", () => { steerCount++; });

  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
  pass(submitCount === 1, "main workspace Cmd+Enter should request submit, got " + submitCount);

  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true }));
  pass(steerCount === 1, "main workspace Shift+Enter should click steer, got " + steerCount);
})();

(function testPaneIframeShortcutsAreIgnored() {
  const dom = makeDOM(true);
  const { window } = dom;
  const ta = window.document.querySelector(".message-input");
  const form = window.document.querySelector("form[data-input-form]");
  const steer = window.document.querySelector("[data-steer-trigger]");
  let submitCount = 0;
  let steerCount = 0;
  form.requestSubmit = () => { submitCount++; };
  steer.addEventListener("click", () => { steerCount++; });

  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }));
  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true }));

  pass(submitCount === 0, "pane iframe Enter shortcuts should not submit, got " + submitCount);
  pass(steerCount === 0, "pane iframe Shift+Enter should not steer, got " + steerCount);
})();

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: composer shortcuts respect pane iframe mode");
process.exit(0);
