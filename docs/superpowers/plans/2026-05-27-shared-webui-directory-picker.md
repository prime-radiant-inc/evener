# Shared Web UI Directory Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every serf-hub web UI path/directory autocomplete use the same picker behavior as the top-screen project directory picker.

**Architecture:** Extract the existing canonical directory picker behavior from `cmd/serf-hub/assets/spawn.js` into a shared `window.SerfDirPicker.open(options)` helper in a new `cmd/serf-hub/assets/dir-picker.js`. Update the top spawn `working_dir` chip and all settings/advanced path controls to call the shared helper, preserving their caller-specific accept behavior through callbacks.

**Tech Stack:** Plain browser JavaScript, JSDOM JavaScript tests, existing serf-hub Appwire `completeDirs` RPC and `/api/dirs` fallback.

---

## File Structure

- Create: `cmd/serf-hub/assets/dir-picker.js`
  - Owns the single directory picker implementation.
  - Exports `window.SerfDirPicker.open(options)`.
  - Contains the canonical popup behavior currently in `spawn.js:openDirPicker`: `.chip-picker-dir`, `.chip-picker-search`, `.chip-picker-results`, git tag rendering, Tab completion, Enter exact-match-vs-literal semantics, initial fetch, click-outside dismissal.

- Modify: `cmd/serf-hub/templates/app.html`
  - Include `/assets/dir-picker.js` before scripts that use it (`settings-pickers.js` and `spawn.js`).

- Modify: `cmd/serf-hub/assets/spawn.js`
  - Replace the body of the local `openDirPicker(chip)` function with a call to `window.SerfDirPicker.open(...)`.
  - Keep top-screen behavior unchanged from the user's perspective.
  - Continue setting `serf-hub.spawn-defaults.global.last-working-dir` when a value is accepted.

- Modify: `cmd/serf-hub/assets/settings-pickers.js`
  - Remove the advanced/settings-only directory picker and datalist implementation.
  - Keep existing selectors:
    - `button[data-settings-dir-picker]`
    - `input[data-settings-dir-input]`
  - Wire both selector families to `window.SerfDirPicker.open(...)`.
  - For settings inputs, accepted values must set `input.value` and dispatch both `input` and `change` events with `{ bubbles: true }`.

- Create: `cmd/serf-hub/jstest/test-dir-picker.js`
  - Unit-test the shared helper directly.

- Modify: `cmd/serf-hub/jstest/test-spawn.js`
  - Load `dir-picker.js` before `spawn.js`.
  - Add/keep assertions that opening the top working-dir chip uses the shared picker behavior.

- Create: `cmd/serf-hub/jstest/test-settings-dir-picker.js`
  - Unit-test `settings-pickers.js` integration for inline settings path inputs and picker buttons.

---

### Task 1: Add the shared directory picker helper and direct tests

**Files:**
- Create: `cmd/serf-hub/assets/dir-picker.js`
- Create: `cmd/serf-hub/jstest/test-dir-picker.js`

- [ ] **Step 1: Write the failing direct helper test**

Create `cmd/serf-hub/jstest/test-dir-picker.js` with this content:

```javascript
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function deferred() {
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  return { promise, resolve };
}

(async function main() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="anchor-wrap"><button id="anchor" type="button">dir</button></div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });

  const calls = [];
  let accepted = [];
  dom.window.SerfAppwire = {
    completeDirs(prefix) {
      calls.push(prefix);
      return Promise.resolve({
        results: [
          { path: "/tmp/project", is_git: true },
          { path: "/tmp/plain", is_git: false },
        ],
      });
    },
  };

  dom.window.eval(dirPickerSrc);
  assert(dom.window.SerfDirPicker && typeof dom.window.SerfDirPicker.open === "function",
    "SerfDirPicker.open should be exported");

  const anchor = dom.window.document.getElementById("anchor");
  dom.window.SerfDirPicker.open({
    anchor,
    currentValue: "/tmp",
    placeholder: "/path/to/repo",
    onAccept(value) { accepted.push(value); },
  });

  await new Promise((r) => setTimeout(r, 0));

  const picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "shared picker should render .chip-picker-dir");
  const input = picker.querySelector(".chip-picker-search");
  assert(input && input.value === "/tmp", "picker input should use current value");
  assert(calls[0] === "/tmp", "picker should fetch initial suggestions for current value");
  assert(picker.querySelectorAll(".chip-picker-dir-row").length === 2,
    "picker should render directory rows");
  assert(picker.querySelector(".chip-picker-dir-tag").textContent === "git",
    "picker should render git tag");

  input.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }));
  assert(input.value === "/tmp/project/", "Tab should autocomplete to first result plus slash");

  input.value = "/tmp/project";
  input.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  assert(accepted[0] === "/tmp/project", "Enter on exact match should accept matching suggestion");
  assert(!dom.window.document.querySelector(".chip-picker-dir"), "picker should close after exact accept");

  dom.window.SerfDirPicker.open({
    anchor,
    currentValue: "/custom/literal",
    placeholder: "/path/to/repo",
    onAccept(value) { accepted.push(value); },
  });
  await new Promise((r) => setTimeout(r, 0));
  const picker2 = dom.window.document.querySelector(".chip-picker-dir");
  const input2 = picker2.querySelector(".chip-picker-search");
  input2.value = "/custom/literal";
  input2.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  assert(accepted[1] === "/custom/literal", "Enter on non-exact value should accept typed literal");
  assert(!dom.window.document.querySelector(".chip-picker-dir"), "picker should close after literal accept");

  dom.window.SerfDirPicker.open({
    anchor,
    currentValue: "",
    placeholder: "/path/to/repo",
    onAccept(value) { accepted.push(value); },
  });
  await new Promise((r) => setTimeout(r, 0));
  const picker3 = dom.window.document.querySelector(".chip-picker-dir");
  picker3.querySelector(".chip-picker-dir-row").click();
  assert(accepted[2] === "/tmp/project", "clicking a row should accept that path");

  const slow = deferred();
  dom.window.SerfAppwire.completeDirs = (prefix) => {
    calls.push(prefix);
    return slow.promise;
  };
  dom.window.SerfDirPicker.open({
    anchor,
    currentValue: "/slow",
    placeholder: "/path/to/repo",
    onAccept(value) { accepted.push(value); },
  });
  assert(dom.window.document.querySelectorAll(".chip-picker-dir").length === 1,
    "opening a picker should remove any existing picker first");
  slow.resolve({ results: [] });
  await new Promise((r) => setTimeout(r, 0));
  assert(dom.window.document.querySelector(".empty-state-picker"), "empty result should render empty state");

  console.log("PASS test-dir-picker");
})();
```

- [ ] **Step 2: Run the new test to verify it fails because the helper does not exist**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-dir-picker.js
```

Expected: FAIL or file-read error for missing `../assets/dir-picker.js`.

- [ ] **Step 3: Implement `dir-picker.js` by extracting the canonical behavior**

Create `cmd/serf-hub/assets/dir-picker.js` with this content:

```javascript
// dir-picker.js — shared directory picker used by all serf-hub web UI path controls.
(function (global) {
  "use strict";

  function completeDirs(prefix) {
    if (global.SerfAppwire && typeof global.SerfAppwire.completeDirs === "function") {
      return global.SerfAppwire.completeDirs(prefix);
    }
    return fetch("/api/dirs?prefix=" + encodeURIComponent(prefix || ""), {
      credentials: "same-origin",
    }).then((r) => r.json());
  }

  function removeExisting() {
    const existing = document.querySelector(".chip-picker");
    if (existing) existing.remove();
  }

  function dismissOnOutsideClick(picker) {
    setTimeout(() => {
      const offClick = (e) => {
        if (!picker.contains(e.target)) {
          picker.remove();
          document.removeEventListener("click", offClick);
        }
      };
      document.addEventListener("click", offClick);
    }, 0);
  }

  function normalizedResults(data) {
    const list = data && Array.isArray(data.results) ? data.results : [];
    return list.map((item) => {
      if (typeof item === "string") return { path: item, is_git: false };
      return { path: item.path || "", is_git: !!item.is_git };
    }).filter((item) => item.path);
  }

  function openDirPicker(options) {
    options = options || {};
    const anchor = options.anchor;
    if (!anchor || !anchor.parentNode) return null;

    removeExisting();

    const picker = document.createElement("div");
    picker.className = "chip-picker chip-picker-dir";

    const input = document.createElement("input");
    input.className = "chip-picker-search";
    input.placeholder = options.placeholder || "/path/to/repo";
    input.value = options.currentValue || "";
    picker.appendChild(input);

    const results = document.createElement("div");
    results.className = "chip-picker-results";
    picker.appendChild(results);

    let timer = null;

    function accept(value) {
      const path = String(value || "");
      if (!path.trim()) return;
      if (typeof options.onAccept === "function") options.onAccept(path);
      picker.remove();
    }

    function fetchDirs(prefix) {
      const dirsPromise = completeDirs(prefix);
      dirsPromise.then((data) => {
        results.innerHTML = "";
        const list = normalizedResults(data);
        if (list.length === 0) {
          const empty = document.createElement("div");
          empty.className = "empty-state empty-state-picker";
          empty.innerHTML = '<p class="empty-state-body">No matching directories</p>';
          results.appendChild(empty);
          return;
        }
        list.forEach((r) => {
          const el = document.createElement("div");
          el.className = "chip-picker-dir-row";
          const path = document.createElement("span");
          path.className = "chip-picker-dir-path";
          path.textContent = r.path;
          el.appendChild(path);
          if (r.is_git) {
            const tag = document.createElement("span");
            tag.className = "chip-picker-dir-tag";
            tag.textContent = "git";
            el.appendChild(tag);
          }
          el.addEventListener("click", () => accept(r.path));
          results.appendChild(el);
        });
      }).catch(() => {});
    }

    input.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => fetchDirs(input.value), 150);
    });

    // Tab autocompletes to first result + "/". Enter prefers an exact
    // match and otherwise commits the typed literal so the UI does not
    // silently choose the wrong directory.
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        const typed = input.value;
        if (!typed.trim()) return;
        const rows = results.querySelectorAll(".chip-picker-dir-row");
        let exact = null;
        for (const row of rows) {
          const p = row.querySelector(".chip-picker-dir-path");
          if (p && p.textContent === typed) { exact = row; break; }
        }
        if (exact) exact.click();
        else accept(typed);
      } else if (e.key === "Tab") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-path");
        if (first) input.value = first.textContent + "/";
      } else if (e.key === "Escape") {
        e.preventDefault();
        picker.remove();
      }
    });

    anchor.parentNode.style.position = "relative";
    anchor.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    picker.style.top = (anchor.offsetTop + anchor.offsetHeight + 4) + "px";
    picker.style.left = anchor.offsetLeft + "px";
    picker.style.zIndex = "50";
    if (options.minWidth) picker.style.minWidth = options.minWidth;

    input.focus();
    fetchDirs(input.value);
    dismissOnOutsideClick(picker);
    return picker;
  }

  global.SerfDirPicker = {
    open: openDirPicker,
  };
})(window);
```

- [ ] **Step 4: Run the direct helper test to verify it passes**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-dir-picker.js
```

Expected: `PASS test-dir-picker`.

- [ ] **Step 5: Commit Task 1**

Run:

```bash
git add cmd/serf-hub/assets/dir-picker.js cmd/serf-hub/jstest/test-dir-picker.js
git commit -m "feat: add shared hub directory picker"
```

---

### Task 2: Load the shared helper and migrate the top spawn picker

**Files:**
- Modify: `cmd/serf-hub/templates/app.html`
- Modify: `cmd/serf-hub/assets/spawn.js`
- Modify: `cmd/serf-hub/jstest/test-spawn.js`

- [ ] **Step 1: Write the failing spawn integration test update**

In `cmd/serf-hub/jstest/test-spawn.js`, add this near the existing source reads at the top:

```javascript
const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");
```

Before every `window.eval(spawnSrc)` call in the file, add:

```javascript
window.eval(dirPickerSrc);
```

For variables named `formDom.window` and `staleModelDom.window`, use the exact window object:

```javascript
formDom.window.eval(dirPickerSrc);
staleModelDom.window.eval(dirPickerSrc);
```

After the existing model picker assertions around the first form DOM, add this test block:

```javascript
let dirCalls = [];
formDom.window.SerfAppwire.completeDirs = (prefix) => {
  dirCalls.push(prefix);
  return Promise.resolve({ results: [{ path: "/tmp/project-with-oauth", is_git: true }] });
};

const dirChip = formDom.window.document.createElement("button");
dirChip.className = "btn btn-chip";
dirChip.type = "button";
dirChip.dataset.chip = "working_dir";
dirChip.innerHTML = '<span class="chip-value" data-chip-value-working_dir>/tmp/project-with-oauth</span>';
formDom.window.document.getElementById("spawn-chips").appendChild(dirChip);
dirChip.click();
await new Promise((r) => setTimeout(r, 0));
assert(formDom.window.document.querySelector(".chip-picker-dir"),
  "working directory chip should open the shared directory picker");
assert(dirCalls[0] === "/tmp/project-with-oauth",
  "working directory chip should fetch suggestions for the current chip value");
formDom.window.document.querySelector(".chip-picker-dir-row").click();
assert(formDom.window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") === "/tmp/project-with-oauth",
  "working directory chip should remember accepted directory");
```

If `test-spawn.js` is not already inside an async function where this block is inserted, wrap the assertions in the existing asynchronous section or use a promise chain. Do not add top-level `await` unless the file already runs as an ES module.

- [ ] **Step 2: Run the updated spawn test to verify it fails before migration**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-spawn.js
```

Expected: failure until `spawn.js` calls the shared helper or test loading is corrected.

- [ ] **Step 3: Include `dir-picker.js` in the app template**

In `cmd/serf-hub/templates/app.html`, locate the script includes for assets. Add the shared helper before both `settings-pickers.js` and `spawn.js`:

```html
<script src="/assets/dir-picker.js"></script>
```

The final order must ensure `window.SerfDirPicker` exists before `settings-pickers.js` and `spawn.js` execute. A correct local ordering is:

```html
<script src="/assets/appwire.js"></script>
<script src="/assets/dir-picker.js"></script>
<script src="/assets/settings-pickers.js"></script>
<script src="/assets/spawn.js"></script>
```

Do not reorder unrelated scripts unless required by the current template structure.

- [ ] **Step 4: Replace `spawn.js` local directory picker body with shared helper call**

In `cmd/serf-hub/assets/spawn.js`, replace the full `openDirPicker(chip)` function body with:

```javascript
  function openDirPicker(chip) {
    const display = chip.querySelector(".chip-value");
    const current = display && display.textContent.trim() === "(pick a directory)"
      ? ""
      : (display ? display.textContent.trim() : "");
    const fallback = window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") || "";
    if (!window.SerfDirPicker || typeof window.SerfDirPicker.open !== "function") return;
    window.SerfDirPicker.open({
      anchor: chip,
      currentValue: current || fallback,
      placeholder: "/path/to/repo",
      onAccept(value) {
        setChipValue("working_dir", value);
        window.localStorage.setItem("serf-hub.spawn-defaults.global.last-working-dir", value);
      },
    });
  }
```

This deliberately keeps the existing top-screen behavior:
- Uses current chip value unless the placeholder is shown.
- Falls back to `serf-hub.spawn-defaults.global.last-working-dir`.
- Accepting any path updates the working-dir chip.
- Accepting any path updates `last-working-dir`.

- [ ] **Step 5: Run the spawn integration test**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-spawn.js
```

Expected: existing test output remains passing and the new shared-picker assertions pass.

- [ ] **Step 6: Commit Task 2**

Run:

```bash
git add cmd/serf-hub/templates/app.html cmd/serf-hub/assets/spawn.js cmd/serf-hub/jstest/test-spawn.js
git commit -m "refactor: use shared directory picker for spawn cwd"
```

---

### Task 3: Migrate advanced/settings path inputs to the shared picker

**Files:**
- Modify: `cmd/serf-hub/assets/settings-pickers.js`
- Create: `cmd/serf-hub/jstest/test-settings-dir-picker.js`

- [ ] **Step 1: Write the failing settings picker integration test**

Create `cmd/serf-hub/jstest/test-settings-dir-picker.js` with this content:

```javascript
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dirPickerSrc = fs.readFileSync(path.resolve(__dirname, "../assets/dir-picker.js"), "utf8");
const settingsPickersSrc = fs.readFileSync(path.resolve(__dirname, "../assets/settings-pickers.js"), "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

(async function main() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="root">
      <div class="sp-dir-wrap">
        <input id="inline-dir" type="text" data-settings-dir-input value="/tmp">
        <button id="pick-dir" type="button" data-settings-dir-picker>pick</button>
      </div>
      <div class="sp-dir-wrap">
        <input id="button-dir" type="text" value="/opt">
        <button id="button-pick" type="button" data-settings-dir-picker>pick</button>
      </div>
      <button id="model" type="button" data-settings-model-picker></button>
    </div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/settings/plugins",
  });

  dom.window.fetch = (url) => {
    if (String(url).includes("/api/models")) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
    if (String(url).includes("/api/dirs")) return Promise.resolve({ ok: true, json: () => Promise.resolve({ results: [] }) });
    return Promise.reject(new Error("unexpected fetch " + url));
  };

  const dirCalls = [];
  dom.window.SerfAppwire = {
    completeDirs(prefix) {
      dirCalls.push(prefix);
      return Promise.resolve({ results: [{ path: prefix === "/opt" ? "/opt/shared" : "/tmp/shared", is_git: false }] });
    },
  };

  dom.window.eval(dirPickerSrc);
  dom.window.eval(settingsPickersSrc);
  dom.window.document.dispatchEvent(new dom.window.Event("DOMContentLoaded", { bubbles: true }));

  const inlineInput = dom.window.document.getElementById("inline-dir");
  assert(!inlineInput.getAttribute("list"), "settings dir input should not get browser datalist autocomplete");
  assert(!dom.window.document.querySelector("datalist"), "settings picker should not create datalist elements");

  let inputEvents = 0;
  let changeEvents = 0;
  inlineInput.addEventListener("input", () => { inputEvents++; });
  inlineInput.addEventListener("change", () => { changeEvents++; });

  inlineInput.dispatchEvent(new dom.window.Event("focus", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 0));
  let picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "focusing an inline settings dir input should open shared picker");
  assert(dirCalls[0] === "/tmp", "inline settings dir picker should fetch for input value");
  picker.querySelector(".chip-picker-dir-row").click();
  assert(inlineInput.value === "/tmp/shared", "inline settings dir picker should write accepted value");
  assert(inputEvents === 1, "inline settings dir picker should dispatch input event");
  assert(changeEvents === 1, "inline settings dir picker should dispatch change event");

  const buttonInput = dom.window.document.getElementById("button-dir");
  dom.window.document.getElementById("button-pick").click();
  await new Promise((r) => setTimeout(r, 0));
  picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "settings dir picker button should open shared picker");
  assert(dirCalls.includes("/opt"), "settings dir picker button should fetch for sibling input value");
  picker.querySelector(".chip-picker-dir-row").click();
  assert(buttonInput.value === "/opt/shared", "settings dir picker button should write accepted value");

  console.log("PASS test-settings-dir-picker");
})();
```

- [ ] **Step 2: Run the new settings integration test to verify it fails before migration**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-settings-dir-picker.js
```

Expected: failure because current `settings-pickers.js` creates a datalist for `data-settings-dir-input` and has its own `buildDirPicker`.

- [ ] **Step 3: Replace settings-only directory picker code with shared helper wiring**

In `cmd/serf-hub/assets/settings-pickers.js`:

1. Remove the old `buildDirPicker(anchorBtn, input)` function.
2. Remove the old datalist-based `wireDirInput(input)` function.
3. Add these helper functions in their place:

```javascript
  // ---------- dir picker ----------

  function writeDirInput(input, value) {
    input.value = value || "";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function openSharedDirPicker(anchor, input) {
    if (!anchor || !input) return;
    if (!window.SerfDirPicker || typeof window.SerfDirPicker.open !== "function") return;
    window.SerfDirPicker.open({
      anchor,
      currentValue: input.value || "",
      placeholder: input.placeholder || "/path/to/repo",
      minWidth: "360px",
      onAccept(value) {
        writeDirInput(input, value);
      },
    });
  }

  function wireDirInput(input) {
    if (input.__spDirInit) return;
    input.__spDirInit = true;
    input.addEventListener("focus", () => openSharedDirPicker(input, input));
    input.addEventListener("keydown", (e) => {
      if ((e.key === "ArrowDown" || e.key === "Enter") && !document.querySelector(".chip-picker-dir")) {
        e.preventDefault();
        openSharedDirPicker(input, input);
      }
    });
  }
```

Keep `initSettingsPickers(root)` selectors unchanged except that the picker button click handler should now call:

```javascript
        openSharedDirPicker(btn, input);
```

The updated button section should be:

```javascript
    root.querySelectorAll("button[data-settings-dir-picker]").forEach(btn => {
      if (btn.__spInit) return;
      btn.__spInit = true;
      const container = btn.closest(".sp-dir-wrap");
      if (!container) return;
      const input = container.querySelector("input[type=text]");
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        openSharedDirPicker(btn, input);
      });
    });
```

- [ ] **Step 4: Run the settings integration test**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-settings-dir-picker.js
```

Expected: `PASS test-settings-dir-picker`.

- [ ] **Step 5: Commit Task 3**

Run:

```bash
git add cmd/serf-hub/assets/settings-pickers.js cmd/serf-hub/jstest/test-settings-dir-picker.js
git commit -m "refactor: use shared directory picker in settings"
```

---

### Task 4: Confirm generated launch path controls use the migrated settings wiring

**Files:**
- Modify: `cmd/serf-hub/jstest/test-launchconfig-controls.js`

- [ ] **Step 1: Add an assertion that generated path controls keep the settings-dir marker**

In `cmd/serf-hub/jstest/test-launchconfig-controls.js`, after the existing trace file assertions:

```javascript
  assert(root.querySelector('[data-launch-wire-field="traceFile"]').dataset.settingsDirInput === "true",
    "generated path controls should opt into the shared settings directory picker");
```

After the `pluginWrap` declaration, add:

```javascript
  assert(pluginWrap.querySelector("input[data-settings-dir-input]"),
    "generated pathList controls should opt into the shared settings directory picker");
```

These assertions avoid testing `settings-pickers.js` twice while protecting the contract between generated launch controls and shared settings picker wiring.

- [ ] **Step 2: Run the launch config controls test**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-launchconfig-controls.js
```

Expected: PASS.

- [ ] **Step 3: Commit Task 4**

Run:

```bash
git add cmd/serf-hub/jstest/test-launchconfig-controls.js
git commit -m "test: cover launch path picker wiring"
```

---

### Task 5: Full verification and cleanup

**Files:**
- Verify all touched files.

- [ ] **Step 1: Search for obsolete datalist directory autocomplete code**

Run:

```bash
rg "datalist|setAttribute\(\"list\"|data-settings-dir-input.*datalist|buildDirPicker|/api/dirs" cmd/serf-hub/assets cmd/serf-hub/templates cmd/serf-hub/jstest
```

Expected:
- No `setAttribute("list"` in `settings-pickers.js`.
- No `buildDirPicker` in `settings-pickers.js`.
- `/api/dirs` may remain in `dir-picker.js` and `appwire.js` only.
- Test references to “datalist” are allowed only where asserting no datalist is created.

- [ ] **Step 2: Run all JavaScript web UI tests**

Run:

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: every `test-*.js` exits successfully. If `NODE_PATH` is missing in the local environment, run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules ./run-all.sh
```

- [ ] **Step 3: Run targeted Go tests for hub web/static routing**

Run:

```bash
go test ./cmd/serf-hub -run 'Test.*(Web|Launch|Path|RPC|Config)' 
```

Expected: PASS. This should catch missing embedded asset/template issues if `app.html` or asset embedding expectations changed.

- [ ] **Step 4: Run the full hub package tests if targeted tests pass**

Run:

```bash
go test ./cmd/serf-hub
```

Expected: PASS. If a known live/real integration test is skipped by default, leave it skipped; do not weaken tests.

- [ ] **Step 5: Inspect git diff**

Run:

```bash
git diff -- cmd/serf-hub/assets/dir-picker.js cmd/serf-hub/assets/settings-pickers.js cmd/serf-hub/assets/spawn.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-dir-picker.js cmd/serf-hub/jstest/test-settings-dir-picker.js cmd/serf-hub/jstest/test-spawn.js cmd/serf-hub/jstest/test-launchconfig-controls.js
```

Expected:
- One shared implementation in `dir-picker.js`.
- `spawn.js` delegates top working-dir chip to `window.SerfDirPicker.open`.
- `settings-pickers.js` delegates inline and button directory controls to `window.SerfDirPicker.open`.
- No browser datalist implementation remains for settings directory inputs.
- Tests cover the shared helper, spawn integration, settings integration, and generated launch control markers.

- [ ] **Step 6: Final commit if any verification-only fixes were needed**

If Task 5 required fixes after the prior commits, commit them:

```bash
git add cmd/serf-hub/assets cmd/serf-hub/templates cmd/serf-hub/jstest
git commit -m "test: verify shared directory picker integration"
```

If there were no additional changes, do not create an empty commit.

---

## Acceptance Criteria

- The top-screen project directory picker still behaves the same:
  - Opens `.chip-picker-dir`.
  - Fetches directory completions from `SerfAppwire.completeDirs` or `/api/dirs` fallback.
  - Renders directory rows and git tags.
  - Tab autocompletes to first suggestion plus `/`.
  - Enter selects an exact matching suggestion, otherwise accepts typed literal.
  - Accepted value updates `working_dir` and `serf-hub.spawn-defaults.global.last-working-dir`.

- Advanced/settings path controls use the same shared picker:
  - No settings path input uses browser datalist autocomplete.
  - `data-settings-dir-input` opens the shared picker from the input.
  - `data-settings-dir-picker` buttons open the shared picker for the sibling text input.
  - Accepted values dispatch `input` and `change` events so existing validation and save logic continues to run.

- Generated launch config path controls remain wired through `data-settings-dir-input`.

- `cmd/serf-hub/jstest/run-all.sh` passes.

- `go test ./cmd/serf-hub` passes or any failure is investigated and explained with concrete evidence.
