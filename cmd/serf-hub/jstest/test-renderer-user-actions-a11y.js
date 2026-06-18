// Pass 7 a11y: the per-user-message "copy" and "edit" actions must be real
// <button> elements, not clickable <span>s. A bare <span onclick> is not
// keyboard-focusable or operable, so keyboard-only users can never reach it.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve(""),
});
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

async function run() {
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("USER_INPUT", { text: "do the thing" });
  await new Promise(r => setTimeout(r, 10));

  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  const actions = conv.querySelector(".user-message-actions");
  pass(!!actions, "user message should carry an actions row");

  const copy = actions && actions.querySelector(".action.copy");
  const edit = actions && actions.querySelector(".action.edit");
  pass(!!copy, "actions row should have a copy control");
  pass(!!edit, "actions row should have an edit control");

  // The core a11y requirement: both controls are real, focusable buttons.
  pass(copy && copy.tagName === "BUTTON", "copy action must be a <button>, got " + (copy && copy.tagName));
  pass(edit && edit.tagName === "BUTTON", "edit action must be a <button>, got " + (edit && edit.tagName));
  // Buttons declared type=button so they never submit the surrounding form.
  pass(copy && copy.getAttribute("type") === "button", "copy button must be type=button");
  pass(edit && edit.getAttribute("type") === "button", "edit button must be type=button");

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  }
  for (const f of failures) console.log(f);
  process.exit(1);
}

run();
