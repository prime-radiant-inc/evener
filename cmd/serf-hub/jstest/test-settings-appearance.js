const fs = require("fs");
const { JSDOM } = require("jsdom");

const THEME_SRC = fs.readFileSync("../assets/theme.js", "utf8");
const SRC = fs.readFileSync("../assets/settings-appearance.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function change(input) {
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="val-radio-group" data-theme-picker>
      <label class="val-radio"><input type="radio" name="theme" value="system"></label>
      <label class="val-radio"><input type="radio" name="theme" value="dark"></label>
      <label class="val-radio"><input type="radio" name="theme" value="light"></label>
    </div>
    <div class="val-radio-group" data-phone-density-picker>
      <label class="val-radio"><input type="radio" name="phone-density" value="compact"></label>
      <label class="val-radio"><input type="radio" name="phone-density" value="comfortable"></label>
    </div>
    <div class="val-radio-group" data-sidebar-mode-picker>
      <label class="val-radio"><input type="radio" name="sidebar-mode" value="auto"></label>
      <label class="val-radio"><input type="radio" name="sidebar-mode" value="pane"></label>
      <label class="val-radio"><input type="radio" name="sidebar-mode" value="rail"></label>
    </div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });
  const { window } = dom;
  window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
  return window;
}

(function main() {
  const window = makeWindow();
  window.eval(THEME_SRC);
  window.eval(SRC);

  // --- theme ---
  const dark = window.document.querySelector('input[name="theme"][value="dark"]');
  dark.checked = true;
  change(dark);
  assert(window.document.documentElement.getAttribute("data-theme") === "dark", "setting theme=dark applies data-theme");
  assert(window.localStorage.getItem("serf-hub.theme") === "dark", "theme choice persists to localStorage");
  assert(window.SerfToast.messages.some(m => m.message === "Theme: dark"), "theme change toasts the new value");

  const system = window.document.querySelector('input[name="theme"][value="system"]');
  system.checked = true;
  change(system);
  assert(window.document.documentElement.getAttribute("data-theme") === null, "theme=system removes data-theme");
  assert(window.localStorage.getItem("serf-hub.theme") === null, "theme=system clears the stored preference");

  // --- phone density ---
  const comfortable = window.document.querySelector('input[name="phone-density"][value="comfortable"]');
  comfortable.checked = true;
  change(comfortable);
  assert(window.localStorage.getItem("serf-hub.phone-density") === "comfortable", "phone-density choice persists");
  assert(window.document.body.dataset.phoneDensity === "comfortable", "phone-density applies to body dataset immediately");

  // --- sidebar mode: delegates to window.SerfSidebar.applySidebarMode when present ---
  window.SerfSidebar = { applied: [], applySidebarMode(v) { this.applied.push(v); } };
  const rail = window.document.querySelector('input[name="sidebar-mode"][value="rail"]');
  rail.checked = true;
  change(rail);
  assert(window.SerfSidebar.applied.length === 1 && window.SerfSidebar.applied[0] === "rail", "sidebar-mode=rail delegates to SerfSidebar.applySidebarMode");

  const auto = window.document.querySelector('input[name="sidebar-mode"][value="auto"]');
  auto.checked = true;
  change(auto);
  assert(window.SerfSidebar.applied.length === 2 && window.SerfSidebar.applied[1] === "auto", "sidebar-mode=auto delegates to SerfSidebar.applySidebarMode");
  assert(window.localStorage.getItem("serf-hub.sidebar.rail") === null, "delegated sidebar-mode does not write localStorage directly");
  delete window.SerfSidebar;

  // --- sidebar mode fallback (no SerfSidebar): persists the raw value ---
  const pane = window.document.querySelector('input[name="sidebar-mode"][value="pane"]');
  pane.checked = true;
  change(pane);
  assert(window.localStorage.getItem("serf-hub.sidebar.rail") === "pane", "sidebar-mode=pane falls back to localStorage without SerfSidebar");

  // --- restore on (re)load ---
  const restored = makeWindow();
  restored.localStorage.setItem("serf-hub.theme", "light");
  restored.localStorage.setItem("serf-hub.phone-density", "comfortable");
  restored.localStorage.setItem("serf-hub.sidebar.rail", "rail");
  restored.eval(THEME_SRC);
  restored.eval(SRC);
  restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
  assert(restored.document.querySelector('input[name="theme"][value="light"]').checked, "restore checks the stored theme radio");
  assert(restored.document.querySelector('input[name="phone-density"][value="comfortable"]').checked, "restore checks the stored phone-density radio");
  assert(restored.document.querySelector('input[name="sidebar-mode"][value="rail"]').checked, "restore checks the stored sidebar-mode radio");

  // --- restore defaults to auto when no pref is stored ---
  const fresh = makeWindow();
  fresh.eval(THEME_SRC);
  fresh.eval(SRC);
  fresh.document.dispatchEvent(new fresh.Event("DOMContentLoaded", { bubbles: true }));
  assert(fresh.document.querySelector('input[name="sidebar-mode"][value="auto"]').checked, "restore checks auto when no sidebar-mode pref is stored");

  // --- the "apply stored value on load" IIFE runs before DOMContentLoaded ---
  const preset = makeWindow();
  preset.localStorage.setItem("serf-hub.phone-density", "comfortable");
  preset.eval(THEME_SRC);
  preset.eval(SRC);
  assert(preset.document.body.dataset.phoneDensity === "comfortable", "phone-density applies on script load, before DOMContentLoaded fires");
  assert(!("sidebarRail" in preset.document.body.dataset), "sidebar-mode no longer writes the rail attribute on script load (sidebar.js owns it)");

  console.log("PASS — settings-appearance theme/phone-density/sidebar-mode commit, restore, and on-load apply");
})();
