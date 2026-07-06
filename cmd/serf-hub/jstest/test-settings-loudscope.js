// loudScope settings control: two-option radio ("asks" default / "all")
// wired through the same data-notif-form change delegate that drives the
// existing checkbox toggles. Mirrors the JSDOM bootstrap in
// test-settings-notifications.js.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/settings-notifications.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function change(input) {
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

async function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <dl class="settings-table" data-notif-form>
      <div class="row editable">
        <dt id="lbl-notif-loudscope">Loud for</dt>
        <dd>
          <label class="val-radio"><input type="radio" name="loud-scope" data-notif-radio="loudScope" value="asks" aria-labelledby="lbl-notif-loudscope"> Questions &amp; errors</label>
          <label class="val-radio"><input type="radio" name="loud-scope" data-notif-radio="loudScope" value="all" aria-labelledby="lbl-notif-loudscope"> Everything needing me</label>
        </dd>
      </div>
    </dl>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/settings/notifications" });
  const { window } = dom;
  window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
  // See test-settings-notifications.js's makeWindow for why this wait
  // matters: jsdom's own DOMContentLoaded fires shortly after construction
  // regardless of when the test triggers it, and eval(SRC) registers a
  // real DOMContentLoaded listener that must not race it.
  if (window.document.readyState !== "complete") {
    await new Promise((resolve) => window.addEventListener("load", resolve, { once: true }));
  }
  return window;
}

(async function main() {
  // --- selecting "all" persists loudScope ---
  const window = await makeWindow();
  window.eval(SRC);
  const changedEvents = [];
  window.document.addEventListener("serf-hub:notifications-changed", (e) => changedEvents.push(e.detail));

  const allRadio = window.document.querySelector('input[value="all"]');
  allRadio.checked = true;
  change(allRadio);
  assert(JSON.parse(window.localStorage.getItem("serf-hub.notifications") || "{}").loudScope === "all", "selecting the 'all' radio must persist loudScope=all");
  assert(changedEvents.some(d => d.key === "loudScope" && d.value === "all"), "loudScope radio dispatches serf-hub:notifications-changed");
  assert(window.SerfToast.messages.some(m => m.kind === "success"), "committed radio shows a success toast");

  // --- restore on (re)load reflects the stored value ---
  const restored = await makeWindow();
  restored.localStorage.setItem("serf-hub.notifications", JSON.stringify({ loudScope: "all" }));
  restored.eval(SRC);
  restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
  assert(restored.document.querySelector('input[value="all"]').checked === true, "restore checks the stored loudScope radio");
  assert(restored.document.querySelector('input[value="asks"]').checked === false, "restore unchecks the non-matching loudScope radio");

  // --- default (no stored prefs at all) reflects to the "asks" radio ---
  const fresh = await makeWindow();
  fresh.eval(SRC);
  fresh.document.dispatchEvent(new fresh.Event("DOMContentLoaded", { bubbles: true }));
  assert(fresh.document.querySelector('input[value="asks"]').checked === true, "default (unset) loudScope reflects to the asks radio");

  console.log("PASS — settings loudScope radio commit and restore");
})();
