// Anchored chip pickers clamp inside the viewport (tablet band: an anchor
// near the right edge must not push the fixed-width panel off-screen).
const fs = require("fs");
const { JSDOM } = require("jsdom");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

// Behavioral: dir-picker's placeChipPicker clamps left (exported as a test
// seam by this task — see Step 3).
const src = fs.readFileSync(__dirname + "/../assets/dir-picker.js", "utf8");
const dom = new JSDOM(`<!DOCTYPE html><html><body><button id="chip">dir</button></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.matchMedia = (q) => ({ matches: false }); // anchored (non-phone) path
Object.defineProperty(w, "innerWidth", { value: 900, configurable: true });
w.eval(src);
assert(w.SerfDirPicker && typeof w.SerfDirPicker.placeChipPicker === "function",
  "placeChipPicker exported as a test seam");
const anchor = w.document.getElementById("chip");
Object.defineProperty(anchor, "offsetLeft", { value: 880 });
Object.defineProperty(anchor, "offsetTop", { value: 40 });
Object.defineProperty(anchor, "offsetHeight", { value: 30 });
const picker = w.document.createElement("div");
Object.defineProperty(picker, "offsetWidth", { value: 520 });
w.SerfDirPicker.placeChipPicker(picker, anchor);
assert(picker.style.left !== "880px", "left is clamped away from the anchor's raw offset");
assert(parseInt(picker.style.left, 10) <= 900 - 520 - 8, "panel right edge stays inside the viewport");
assert(parseInt(picker.style.left, 10) >= 8, "clamp never pushes past the left edge");

// Mirror: spawn.js carries the same clamp (its placeChipPicker is a copy).
const spawnSrc = fs.readFileSync(__dirname + "/../assets/spawn.js", "utf8");
assert(/maxLeft/.test(spawnSrc) && /offsetWidth/.test(spawnSrc),
  "spawn.js placeChipPicker mirrors the viewport clamp");
console.log("ok picker viewport clamp");
process.exit(0);
