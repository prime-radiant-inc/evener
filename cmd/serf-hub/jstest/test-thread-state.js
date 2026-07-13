// JSDOM test pinning window.SerfThreadState.isBusy — the single definition of
// the thread busy signal shared by the composer controls (renderer.js), the
// header model chip (model-switch.js), and the command palette (search.js).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const threadStateSrc = fs.readFileSync(path.resolve(__dirname, "../assets/thread-state.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", { runScripts: "outside-only" });
dom.window.eval(threadStateSrc);
const isBusy = dom.window.SerfThreadState.isBusy;

// Busy requires BOTH an active state AND a set turn id.
pass(isBusy("active", "turn-1") === true, "active + turn id => busy");
pass(isBusy("active", "") === false, "active but no turn id => not busy");
pass(isBusy("idle", "turn-1") === false, "turn id but not active => not busy");
pass(isBusy("awaiting", "turn-1") === false, "awaiting (your-move) is not busy even with a turn id");
pass(isBusy("", "") === false, "empty/empty => not busy");
// Falsy turn ids (null/undefined from missing DOM attributes) are not busy.
pass(isBusy("active", null) === false, "null turn id => not busy");
pass(isBusy("active", undefined) === false, "undefined turn id => not busy");

if (failures.length) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("PASS: thread-state isBusy semantics");
