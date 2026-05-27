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
  assert(picker.querySelector(".chip-picker-results"), "shared picker should render .chip-picker-results");
  assert(input && input.value === "/tmp", "picker input should use current value");
  assert(calls[0] === "/tmp", "picker should fetch initial suggestions for current value");
  assert(picker.querySelectorAll(".chip-picker-dir-row").length === 2,
    "picker should render directory rows");
  assert(picker.querySelector(".chip-picker-dir-path"), "picker should render directory path text");
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

  const raceOld = deferred();
  const raceNew = deferred();
  dom.window.SerfAppwire.completeDirs = (prefix) => {
    if (prefix === "/race/old") return raceOld.promise;
    if (prefix === "/race/new") return raceNew.promise;
    return Promise.resolve({ results: [] });
  };
  dom.window.SerfDirPicker.open({
    anchor,
    currentValue: "/race/old",
    placeholder: "/path/to/repo",
    onAccept(value) { accepted.push(value); },
  });
  const racePicker = dom.window.document.querySelector(".chip-picker-dir");
  const raceInput = racePicker.querySelector(".chip-picker-search");
  raceInput.value = "/race/new";
  raceInput.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 170));
  raceNew.resolve({ results: [{ path: "/race/newest", is_git: false }] });
  await new Promise((r) => setTimeout(r, 0));
  assert(racePicker.querySelector(".chip-picker-dir-path").textContent === "/race/newest",
    "newer directory results should render first");
  raceOld.resolve({ results: [{ path: "/race/old-stale", is_git: false }] });
  await new Promise((r) => setTimeout(r, 0));
  assert(racePicker.querySelector(".chip-picker-dir-path").textContent === "/race/newest",
    "stale directory results should not overwrite newer results");

  const cleanupDom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="cleanup-wrap"><button id="cleanup-anchor" type="button">dir</button></div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  cleanupDom.window.SerfAppwire = {
    completeDirs() {
      return Promise.resolve({ results: [{ path: "/cleanup/project", is_git: false }] });
    },
  };
  const activeClickListeners = new Set();
  const originalAdd = cleanupDom.window.document.addEventListener.bind(cleanupDom.window.document);
  const originalRemove = cleanupDom.window.document.removeEventListener.bind(cleanupDom.window.document);
  cleanupDom.window.document.addEventListener = function(type, listener, options) {
    if (type === "click") activeClickListeners.add(listener);
    return originalAdd(type, listener, options);
  };
  cleanupDom.window.document.removeEventListener = function(type, listener, options) {
    if (type === "click") activeClickListeners.delete(listener);
    return originalRemove(type, listener, options);
  };
  cleanupDom.window.eval(dirPickerSrc);
  const cleanupAnchor = cleanupDom.window.document.getElementById("cleanup-anchor");
  cleanupDom.window.SerfDirPicker.open({ anchor: cleanupAnchor, currentValue: "", onAccept() {} });
  await new Promise((r) => setTimeout(r, 0));
  assert(activeClickListeners.size === 1, "opening a picker should attach one outside-click listener");
  cleanupDom.window.SerfDirPicker.open({ anchor: cleanupAnchor, currentValue: "", onAccept() {} });
  await new Promise((r) => setTimeout(r, 0));
  assert(activeClickListeners.size === 1,
    "replacing a picker should remove the previous outside-click listener");
  const cleanupInput = cleanupDom.window.document.querySelector(".chip-picker-search");
  cleanupInput.dispatchEvent(new cleanupDom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
  assert(activeClickListeners.size === 0, "Escape dismissal should remove outside-click listener");

  console.log("PASS test-dir-picker");
})();
