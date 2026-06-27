// When the proposed working directory doesn't exist, the spawn form must prompt
// (inline) and create it on confirm before spawning — not fail in the daemon.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");
const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const flush = () => new Promise((r) => setTimeout(r, 0));

// A spawn form with the real nested attach-row/actions structure so the inline
// confirm (which anchors above .spawn-attach-row) inserts correctly.
function buildForm(missingDir) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form data-spawn-form>
      <textarea name="prompt">do the thing</textarea>
      <div class="spawn-attach-row">
        <button type="button" data-attach-trigger>attach</button>
        <div class="spawn-actions">
          <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
        </div>
      </div>
      <input type="hidden" name="harness" value="serf">
      <input type="hidden" data-harness-option value="serf" data-label="serf">
      <input type="hidden" name="model" value="">
      <input type="hidden" name="working_dir" value="${missingDir}">
      <input type="hidden" name="branch" value="">
      <input type="hidden" name="access_mode" value="full">
      <input type="hidden" name="agent" value="default">
      <input type="hidden" name="reasoning_effort" value="">
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/new" });
  return dom;
}

// fetch stub: /api/path/validate?kind=dir reports the dir is missing; record
// whether /api/dirs/create is POSTed (and with what path).
function installFetch(window, missingDir, createResult) {
  const calls = { validate: 0, create: 0, createPath: null };
  window.fetch = (url, opts) => {
    const u = String(url);
    if (u.indexOf("/api/path/validate") === 0) {
      calls.validate++;
      return Promise.resolve({
        json: () => Promise.resolve({ path: missingDir, valid: false, error: "stat " + missingDir + ": no such file or directory" }),
      });
    }
    if (u === "/api/dirs/create") {
      calls.create++;
      calls.createPath = JSON.parse(opts.body).path;
      return Promise.resolve(createResult);
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  };
  return calls;
}

(async () => {
  const MISSING = "/tmp/serf-spawn-newdir-xyz";

  // --- Confirm path: prompt shown, create called, spawn proceeds. ---
  {
    const dom = buildForm(MISSING);
    const { window } = dom;
    const calls = installFetch(window, MISSING, { ok: true, json: () => Promise.resolve({ path: MISSING, created: true }) });
    let startCalled = false;
    window.SerfAppwire = { listModels() { return { then(res) { res([]); return { catch() {} }; } }; }, startThread() { startCalled = true; return Promise.reject(new Error("stop after dir create")); } };
    window.eval(dirPickerSrc);
    window.eval(spawnSrc);
    window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    window.document.querySelector("[data-spawn-form]").dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
    await flush();

    const confirmEl = window.document.querySelector("[data-spawn-confirm]");
    pass(confirmEl, "a missing working dir must show the inline create-confirm prompt");
    pass(confirmEl && confirmEl.textContent.includes(MISSING), "confirm prompt should name the directory");
    pass(!startCalled, "spawn must NOT start before the user confirms creating the dir");

    const createBtn = confirmEl && Array.from(confirmEl.querySelectorAll("button")).find((b) => /create/i.test(b.textContent));
    pass(createBtn, "confirm prompt should offer a create button");
    createBtn.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
    await flush();
    await flush();

    pass(calls.create === 1, "confirming should POST /api/dirs/create exactly once, got " + calls.create);
    pass(calls.createPath === MISSING, "create should target the proposed dir, got " + calls.createPath);
    pass(startCalled, "spawn should proceed after the directory is created");
    pass(!window.document.querySelector("[data-spawn-confirm]"), "confirm prompt should be removed after acting");
  }

  // --- Cancel path: no create, no spawn. ---
  {
    const dom = buildForm(MISSING);
    const { window } = dom;
    const calls = installFetch(window, MISSING, { ok: true, json: () => Promise.resolve({ created: true }) });
    let startCalled = false;
    window.SerfAppwire = { listModels() { return { then(res) { res([]); return { catch() {} }; } }; }, startThread() { startCalled = true; return Promise.reject(new Error("should not start")); } };
    window.eval(dirPickerSrc);
    window.eval(spawnSrc);
    window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    window.document.querySelector("[data-spawn-form]").dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
    await flush();

    const confirmEl = window.document.querySelector("[data-spawn-confirm]");
    pass(confirmEl, "cancel path: prompt should still appear");
    const cancelBtn = confirmEl && Array.from(confirmEl.querySelectorAll("button")).find((b) => /cancel/i.test(b.textContent));
    pass(cancelBtn, "confirm prompt should offer a cancel button");
    cancelBtn.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
    await flush();
    await flush();

    pass(calls.create === 0, "cancelling must NOT create the directory");
    pass(!startCalled, "cancelling must NOT spawn");
    pass(!window.document.querySelector("[data-spawn-confirm]"), "prompt should be dismissed on cancel");
  }

  if (failures.length === 0) {
    console.log("PASS: spawn create-missing-directory prompt");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((err) => { console.error(err && err.stack ? err.stack : err); process.exit(1); });
