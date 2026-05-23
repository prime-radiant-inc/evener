// Verify the SerfFocusTrap helper: on activate, stores activeElement as the
// restore target; on Tab, focus cycles through focusable elements inside the
// trap; on Shift+Tab cycles backwards; on deactivate, restores focus.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const TRAP_PATH = "../assets/focus-trap.js";
const trapSrc = fs.readFileSync(TRAP_PATH, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <button id="outside-before">before</button>
  <button id="trigger">open</button>
  <aside id="panel">
    <button id="first">first</button>
    <input id="middle" type="text">
    <button id="last">last</button>
  </aside>
  <button id="outside-after">after</button>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.eval(trapSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(typeof window.SerfFocusTrap === "object", "SerfFocusTrap should exist on window");
pass(typeof window.SerfFocusTrap.activate === "function", "SerfFocusTrap.activate should be a function");
pass(typeof window.SerfFocusTrap.deactivate === "function", "SerfFocusTrap.deactivate should be a function");

const doc = window.document;
const trigger = doc.getElementById("trigger");
const panel = doc.getElementById("panel");
const first = doc.getElementById("first");
const middle = doc.getElementById("middle");
const last = doc.getElementById("last");
const outsideBefore = doc.getElementById("outside-before");
const outsideAfter = doc.getElementById("outside-after");

// --- 1. activate captures activeElement and focuses first focusable in trap.
trigger.focus();
pass(doc.activeElement === trigger, "trigger should hold focus before activate");

const handle = window.SerfFocusTrap.activate(panel, trigger);
pass(handle && typeof handle === "object", "activate should return a handle object");
pass(doc.activeElement === first, "after activate, focus should land on first focusable inside panel, got " + (doc.activeElement && doc.activeElement.id));

// --- 2. Sibling root-level elements should have `inert` applied.
pass(outsideBefore.hasAttribute("inert"), "outside-before should have inert during trap");
pass(outsideAfter.hasAttribute("inert"), "outside-after should have inert during trap");
pass(!panel.hasAttribute("inert"), "panel itself should NOT have inert during trap");

// --- 3. Tab from last cycles forward to first.
last.focus();
const tabEvent = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(tabEvent);
pass(doc.activeElement === first, "Tab from last should cycle to first, got " + (doc.activeElement && doc.activeElement.id));

// --- 4. Shift+Tab from first cycles backward to last.
first.focus();
const shiftTab = new window.KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true });
panel.dispatchEvent(shiftTab);
pass(doc.activeElement === last, "Shift+Tab from first should cycle to last, got " + (doc.activeElement && doc.activeElement.id));

// --- 5. Tab in the middle of the trap leaves the browser to do its default
//       forward step (we shouldn't preventDefault). middle -> last via the
//       browser's natural Tab traversal isn't simulated by JSDOM, so just
//       assert the helper didn't preventDefault when not at an edge.
middle.focus();
const midTab = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(midTab);
pass(!midTab.defaultPrevented, "Tab in middle of trap should NOT preventDefault");

// --- 6. deactivate restores focus to trigger and removes inert.
window.SerfFocusTrap.deactivate(handle);
pass(doc.activeElement === trigger, "deactivate should restore focus to trigger, got " + (doc.activeElement && doc.activeElement.id));
pass(!outsideBefore.hasAttribute("inert"), "outside-before should no longer have inert after deactivate");
pass(!outsideAfter.hasAttribute("inert"), "outside-after should no longer have inert after deactivate");

// --- 7. After deactivate the Tab handler is unbound (cycling no longer fires).
last.focus();
const postTab = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(postTab);
pass(doc.activeElement === last, "after deactivate, Tab should not cycle (handler unbound), got " + (doc.activeElement && doc.activeElement.id));

if (failures.length === 0) {
  console.log("PASS: focus-trap activate, cycle, deactivate");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
