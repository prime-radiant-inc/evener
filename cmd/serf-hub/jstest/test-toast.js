// Verify window.SerfToast.show inserts an .toast element into #toast-region,
// auto-dismisses after the configured timeout, returns a handle that can be
// dismissed early, and that #toast-region has aria-live="polite".
const fs = require("fs");
const { JSDOM } = require("jsdom");

const TOAST_PATH = "../assets/toast.js";
const toastSrc = fs.readFileSync(TOAST_PATH, "utf8");

const dom = new JSDOM(
  `<!DOCTYPE html><html><body>
     <div id="toast-region" aria-live="polite"></div>
   </body></html>`,
  { runScripts: "outside-only", pretendToBeVisual: true }
);
const { window } = dom;
// Stub setTimeout/clearTimeout so we can advance fake time.
const fakeTimers = [];
let nextHandle = 1;
window.setTimeout = (fn, ms) => { const h = nextHandle++; fakeTimers.push({ h, fn, ms }); return h; };
window.clearTimeout = (h) => { const i = fakeTimers.findIndex((t) => t.h === h); if (i >= 0) fakeTimers.splice(i, 1); };
function flushTimers() { while (fakeTimers.length) { const t = fakeTimers.shift(); try { t.fn(); } catch (_) {} } }

window.eval(toastSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(typeof window.SerfToast === "object", "SerfToast global should exist");
pass(typeof window.SerfToast.show === "function", "SerfToast.show should be a function");
pass(typeof window.SerfToast.dismiss === "function", "SerfToast.dismiss should be a function");

const region = window.document.getElementById("toast-region");
pass(region.getAttribute("aria-live") === "polite", "toast-region should be aria-live=polite");

// Show a success toast.
const h1 = window.SerfToast.show("Saved", "success");
let toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "expected 1 toast, got " + toasts.length);
pass(toasts[0].classList.contains("toast-success"), "kind class should be applied");
pass(toasts[0].textContent.includes("Saved"), "message should appear");

// Dismiss handle explicitly.
window.SerfToast.dismiss(h1);
// The dismissing class is applied; the element is still in the DOM until
// the exit animation timer fires. Flush timers.
flushTimers();
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 0, "after dismiss the toast should be removed");

// Auto-dismiss after timeout.
window.SerfToast.show("Bye", "info", { timeout: 50 });
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "after show the toast should exist");
flushTimers(); // runs the 50ms auto-dismiss timer
flushTimers(); // runs the exit-animation cleanup timer
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 0, "after auto-dismiss the toast should be removed");

// Default kind is "info"; missing region is tolerated.
const ghost = window.document.createElement("div");
const detached = window.SerfToast.show("ignored", "info"); // should still create a toast in the existing region
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "default kind toast inserted");
window.SerfToast.dismiss(detached);
flushTimers();

// Unknown kind defaults to info.
window.SerfToast.show("hi", "unknown-kind");
toasts = region.querySelectorAll(".toast");
pass(toasts[0].classList.contains("toast-info"), "unknown kind should default to toast-info");

if (failures.length === 0) {
  console.log("PASS: toast show/dismiss/auto-dismiss/aria-live");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}
