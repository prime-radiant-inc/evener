// Regression tests for composer textarea shortcuts in normal workspaces vs side-pane iframes.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const SETTINGS_DISPLAY_SRC = fs.readFileSync(path.resolve(__dirname, "../assets/settings-display.js"), "utf8");

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
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
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

(function testEnterToSendModeOffPreservesExistingBehavior() {
  const dom = makeDOM(false);
  const { window } = dom;
  window.localStorage.setItem("serf-hub.composer", JSON.stringify({ enterToSend: false }));
  const ta = window.document.querySelector(".message-input");
  const form = window.document.querySelector("form[data-input-form]");
  const steer = window.document.querySelector("[data-steer-trigger]");
  let submitCount = 0;
  let steerCount = 0;
  form.requestSubmit = () => { submitCount++; };
  steer.addEventListener("click", () => { steerCount++; });

  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
  pass(submitCount === 1, "enter-to-send off: Cmd+Enter should request submit, got " + submitCount);

  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true }));
  pass(steerCount === 1, "enter-to-send off: Shift+Enter should click steer, got " + steerCount);
})();

(function testEnterToSendModeSubmitsOnBareEnterAndNewlinesOnShiftEnter() {
  const dom = makeDOM(false);
  const { window } = dom;
  window.localStorage.setItem("serf-hub.composer", JSON.stringify({ enterToSend: true }));
  const ta = window.document.querySelector(".message-input");
  const form = window.document.querySelector("form[data-input-form]");
  const steer = window.document.querySelector("[data-steer-trigger]");
  let submitCount = 0;
  let steerCount = 0;
  form.requestSubmit = () => { submitCount++; };
  steer.addEventListener("click", () => { steerCount++; });

  const bareEnter = new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
  ta.dispatchEvent(bareEnter);
  pass(submitCount === 1, "enter-to-send on: bare Enter should request submit, got " + submitCount);

  const shiftEnter = new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true });
  ta.dispatchEvent(shiftEnter);
  pass(steerCount === 0, "enter-to-send on: Shift+Enter should NOT click steer, got " + steerCount);
  pass(shiftEnter.defaultPrevented === false, "enter-to-send on: Shift+Enter should leave the default (newline insertion) unprevented");
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

(function testComposerKeybindHintsReflectEnterToSendMode() {
  const dom = makeDOM(false);
  const { window } = dom;
  window.eval(SETTINGS_DISPLAY_SRC);
  const sendKbd = window.document.querySelector(".send-btn kbd");
  const steerKbd = window.document.querySelector("[data-steer-trigger] kbd");

  // enterToSend OFF (default, absent from localStorage): steer keeps Shift+Enter,
  // send keeps Cmd+Enter.
  sendKbd.textContent = "";
  steerKbd.textContent = "";
  window.SerfSettingsDisplay.applyComposerKeybindHints();
  pass(sendKbd.textContent === "⌘↵", "enter-to-send off: send-btn kbd should show ⌘↵, got " + JSON.stringify(sendKbd.textContent));
  pass(steerKbd.textContent === "⇧↵", "enter-to-send off: steer kbd should show ⇧↵, got " + JSON.stringify(steerKbd.textContent));

  // enterToSend ON: bare Enter sends, so the steer kbd hint is hidden and the
  // send kbd hint collapses to a bare ↵.
  window.localStorage.setItem("serf-hub.composer", JSON.stringify({ enterToSend: true }));
  window.SerfSettingsDisplay.applyComposerKeybindHints();
  pass(sendKbd.textContent === "↵", "enter-to-send on: send-btn kbd should show ↵, got " + JSON.stringify(sendKbd.textContent));
  pass(steerKbd.textContent === "", "enter-to-send on: steer kbd should be hidden, got " + JSON.stringify(steerKbd.textContent));
})();

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: composer shortcuts respect pane iframe mode");
process.exit(0);
