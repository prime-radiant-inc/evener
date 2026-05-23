// Verify chip-overflow.js caps visible chips at 4 (sorted by data-chip-modified)
// and inserts a "+N more" expand button that reveals the rest when clicked.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/chip-overflow.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div id="spawn-chips" data-chip-overflow-host>
    <button class="chip" data-chip="a" data-chip-modified="1000"></button>
    <button class="chip" data-chip="b" data-chip-modified="2000"></button>
    <button class="chip" data-chip="c" data-chip-modified="3000"></button>
    <button class="chip" data-chip="d" data-chip-modified="4000"></button>
    <button class="chip" data-chip="e" data-chip-modified="5000"></button>
    <button class="chip" data-chip="f" data-chip-modified="6000"></button>
  </div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.eval(SRC);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// chip-overflow.js applies on DOMContentLoaded / immediately.
const chips = window.document.querySelectorAll(".chip");
const hidden = Array.from(chips).filter((c) => c.hidden);
pass(hidden.length === 2, "expected 2 chips hidden, got " + hidden.length);
// The two oldest (a=1000, b=2000) should be hidden.
const hiddenIds = hidden.map((c) => c.dataset.chip).sort().join(",");
pass(hiddenIds === "a,b", "expected oldest chips hidden, got " + hiddenIds);

const more = window.document.querySelector(".chip-overflow-more");
pass(more !== null, "+N more chip should exist");
pass(more && more.textContent.includes("+2"), "more chip should say +2, got " + (more && more.textContent));

// Click to expand.
more.click();
const stillHidden = Array.from(window.document.querySelectorAll(".chip")).filter((c) => c.hidden);
pass(stillHidden.length === 0, "after click no chip should be hidden");
pass(window.document.querySelector(".chip-overflow-more") === null, "more chip should be removed after expand");

if (failures.length === 0) {
  console.log("PASS: chip overflow caps + expand");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}
