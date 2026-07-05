const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/settings-shell.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header">
      <div class="workspace-title">
        <button type="button" class="btn-ghost settings-nav-back" hidden aria-label="Back to settings">‹ Settings</button>
        <span class="title" data-settings-section="theme">theme</span>
      </div>
    </header>
    <nav class="settings-nav" aria-label="Settings sections">
      <div class="settings-nav-filter">
        <input type="search" class="val-input" data-settings-nav-filter placeholder="Filter settings…">
      </div>
      <a class="settings-nav-link" href="/settings/general">General</a>
      <a class="settings-nav-link" href="/settings/theme">Theme</a>
      <div class="settings-nav-section">Agents &amp; models</div>
      <a class="settings-nav-link" href="/settings/agents">Agents</a>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/settings/theme" });
  return dom.window;
}

(function main() {
  const window = makeWindow();
  window.eval(SRC);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  // --- nav filter ---
  const filterInput = window.document.querySelector("[data-settings-nav-filter]");
  const sectionHeader = window.document.querySelector(".settings-nav-section");
  assert(![...window.document.querySelectorAll(".settings-nav-link")].some(a => a.hidden), "no links hidden before filtering");

  filterInput.value = "agents";
  filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
  assert(window.document.querySelector('a[href="/settings/general"]').hidden, "General hides when filtering for 'agents'");
  assert(!window.document.querySelector('a[href="/settings/agents"]').hidden, "Agents stays visible when filtering for 'agents'");
  assert(!sectionHeader.hidden, "'Agents & models' header stays visible: it has a visible child link");

  filterInput.value = "nomatch";
  filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
  assert(sectionHeader.hidden, "'Agents & models' header hides when every child link is hidden");

  filterInput.value = "";
  filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
  assert(![...window.document.querySelectorAll(".settings-nav-link")].some(a => a.hidden), "clearing the filter re-shows every link");

  // --- back button / syncPane ---
  const back = window.document.querySelector(".settings-nav-back");
  assert(back.hasAttribute("hidden") === false, "back button becomes visible on load: an Active section title is present");

  back.click();
  assert(back.hasAttribute("hidden"), "clicking back hides the back button");
  assert(window.document.body.dataset.settingsPane === "nav", "clicking back flips settingsPane to nav");

  console.log("PASS — settings-nav filter and phone back-button wiring");
})();
