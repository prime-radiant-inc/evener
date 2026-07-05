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

function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <dl class="settings-table" data-notif-form>
      <div class="row editable">
        <dt id="lbl-notif-title">Title bar count</dt>
        <dd><label class="val-toggle"><input type="checkbox" data-notif="title" aria-labelledby="lbl-notif-title"><span class="state" aria-hidden="true">OFF</span></label></dd>
      </div>
      <div class="row editable">
        <dt id="lbl-notif-os">OS notification</dt>
        <dd><label class="val-toggle"><input type="checkbox" data-notif="os" aria-labelledby="lbl-notif-os"><span class="state" aria-hidden="true">OFF</span></label></dd>
      </div>
    </dl>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/settings/notifications" });
  const { window } = dom;
  window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
  return window;
}

(async function main() {
  // --- simple (non-OS) toggle commits immediately ---
  const window = makeWindow();
  window.eval(SRC);
  const changedEvents = [];
  window.document.addEventListener("serf-hub:notifications-changed", (e) => changedEvents.push(e.detail));

  const title = window.document.querySelector('[data-notif="title"]');
  title.checked = true;
  change(title);
  assert(JSON.parse(window.localStorage.getItem("serf-hub.notifications")).title === true, "title toggle persists to localStorage");
  assert(title.parentElement.querySelector(".state").textContent === "ON", "title toggle updates the ON/OFF label");
  assert(changedEvents.some(d => d.key === "title" && d.value === true), "title toggle dispatches serf-hub:notifications-changed");
  assert(window.SerfToast.messages.some(m => m.kind === "success"), "committed toggle shows a success toast");

  // --- OS toggle: browser grants permission ---
  const grant = makeWindow();
  grant.eval(SRC);
  grant.Notification = { permission: "default", requestPermission: () => Promise.resolve("granted") };
  const os = grant.document.querySelector('[data-notif="os"]');
  os.checked = true;
  change(os);
  await new Promise((r) => setTimeout(r, 0));
  assert(JSON.parse(grant.localStorage.getItem("serf-hub.notifications")).os === true, "OS toggle persists once permission is granted");
  assert(os.checked === true, "OS checkbox stays checked once permission is granted");
  assert(os.parentElement.querySelector(".state").textContent === "ON", "OS toggle label reflects the granted permission");

  // --- OS toggle: browser denies permission ---
  const deny = makeWindow();
  deny.eval(SRC);
  deny.Notification = { permission: "default", requestPermission: () => Promise.resolve("denied") };
  const os2 = deny.document.querySelector('[data-notif="os"]');
  os2.checked = true;
  change(os2);
  await new Promise((r) => setTimeout(r, 0));
  assert(os2.checked === false, "OS checkbox reverts to unchecked when permission is denied");
  assert(!JSON.parse(deny.localStorage.getItem("serf-hub.notifications") || "{}").os, "denied OS toggle does not persist as on");
  assert(os2.parentElement.querySelector(".state").textContent === "OFF", "denied OS toggle label reverts to OFF");
  assert(deny.SerfToast.messages.some(m => m.kind === "warning"), "denied OS permission shows a warning toast");

  // --- restore on (re)load ---
  const restored = makeWindow();
  restored.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true }));
  restored.eval(SRC);
  restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
  assert(restored.document.querySelector('[data-notif="title"]').checked, "restore checks a previously-saved toggle");
  assert(restored.document.querySelector('[data-notif="os"]').checked === false, "restore leaves an unset toggle unchecked");

  console.log("PASS — settings-notifications toggle commit, OS-permission flow, and restore");
})();
