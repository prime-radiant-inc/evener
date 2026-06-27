// On a phone the chip selectors (model / path / etc.) dock as a full-width
// bottom sheet with a dimming scrim, instead of a fixed-width desktop panel
// anchored under the chip. Guards placeChipPicker + addPickerScrim in
// dir-picker.js (the model picker in spawn.js shares the same helpers).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

function harness(isPhone) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="spawn-chips">
      <button class="btn-chip" data-chip="working_dir"><span class="chip-value">(pick a directory)</span></button>
    </div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const { window } = dom;
  window.matchMedia = (q) => ({
    matches: isPhone && /max-width:\s*767px/.test(q),
    media: q, onchange: null,
    addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false; },
  });
  window.SerfAppwire = { completeDirs: () => Promise.resolve({ results: [{ path: "/home/jesse", is_git: false }] }) };
  window.eval(dirPickerSrc);
  return window;
}

(async () => {
  // --- Phone: bottom sheet + scrim, inline position cleared. ---
  {
    const window = harness(true);
    window.SerfDirPicker.open({ anchor: window.document.querySelector('[data-chip="working_dir"]'), currentValue: "", placeholder: "/path/to/repo", onAccept() {} });
    await flush();

    const picker = window.document.querySelector(".chip-picker");
    pass(picker, "picker opens");
    pass(picker && picker.classList.contains("chip-picker-sheet"), "on phone the picker is a bottom sheet (.chip-picker-sheet)");
    pass(picker && picker.style.position === "", "inline absolute position is cleared so the CSS fixed sheet wins");
    pass(picker && picker.style.top === "" && picker.style.left === "", "inline top/left are cleared on phone");
    pass(window.document.querySelector(".chip-picker-scrim"), "a dimming scrim is added behind the sheet");

    // Closing the picker removes the scrim (MutationObserver cleanup).
    picker.remove();
    await flush();
    pass(!window.document.querySelector(".chip-picker-scrim"), "scrim is removed when the picker closes");
  }

  // --- Desktop: anchored panel, no sheet, no scrim. ---
  {
    const window = harness(false);
    window.SerfDirPicker.open({ anchor: window.document.querySelector('[data-chip="working_dir"]'), currentValue: "", onAccept() {} });
    await flush();

    const picker = window.document.querySelector(".chip-picker");
    pass(picker && !picker.classList.contains("chip-picker-sheet"), "desktop picker is NOT a bottom sheet");
    pass(!window.document.querySelector(".chip-picker-scrim"), "desktop picker has no scrim");
  }

  if (failures.length === 0) {
    console.log("PASS: mobile picker bottom sheet");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
