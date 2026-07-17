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
    <label>inline <input id="inline-dir" type="text" data-settings-dir-input value="/tmp" placeholder="/custom/path"></label>
    <div class="sp-dir-wrap">
      <input id="button-dir" type="text" value="/opt" placeholder="/button/path">
      <button id="button-picker" type="button" data-settings-dir-picker>choose</button>
    </div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/settings",
  });

  const calls = [];
  dom.window.SerfAppwire = {
    completeDirs(prefix) {
      calls.push(prefix);
      const base = String(prefix || "").replace(/\/+$/, "");
      return Promise.resolve({
        results: [
          { path: base + "/accepted", is_git: true },
          { path: base + "/other", is_git: false },
        ],
      });
    },
  };
  dom.window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

  const inlineInput = dom.window.document.getElementById("inline-dir");
  const activeInlineInputListeners = new Set();
  const originalInlineAdd = inlineInput.addEventListener.bind(inlineInput);
  const originalInlineRemove = inlineInput.removeEventListener.bind(inlineInput);
  inlineInput.addEventListener = function(type, listener, options) {
    if (type === "input") activeInlineInputListeners.add(listener);
    return originalInlineAdd(type, listener, options);
  };
  inlineInput.removeEventListener = function(type, listener, options) {
    if (type === "input") activeInlineInputListeners.delete(listener);
    return originalInlineRemove(type, listener, options);
  };

  dom.window.eval(dirPickerSrc);
  dom.window.eval(settingsPickersSrc);
  dom.window.document.dispatchEvent(new dom.window.Event("DOMContentLoaded", { bubbles: true }));

  assert(!inlineInput.hasAttribute("list"), "inline dir input should not receive a datalist list attribute");
  assert(dom.window.document.querySelectorAll("datalist").length === 0,
    "settings dir inputs should not create datalist elements");

  let inlineInputEvents = 0;
  let inlineChangeEvents = 0;
  inlineInput.addEventListener("input", () => { inlineInputEvents++; });
  inlineInput.addEventListener("change", () => { inlineChangeEvents++; });

  inlineInput.focus();
  await new Promise((r) => setTimeout(r, 0));
  assert(dom.window.document.activeElement === inlineInput,
    "focusing inline dir input should keep focus on the original input so typing goes there");
  assert(!dom.window.document.querySelector(".chip-picker-dir"),
    "focusing inline dir input for typing should not open a secondary picker input");
  inlineInput.value = "/typed";
  inlineInput.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 180));
  assert(inlineInput.value === "/typed", "typing should update the original inline dir input");
  assert(dom.window.document.activeElement === inlineInput,
    "typing in inline dir input should keep focus on the original input");
  let picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "typing in inline input should open shared directory suggestions");
  assert(!picker.querySelector(".chip-picker-search"),
    "inline input suggestions should not create a secondary search input");
  assert(calls[0] === "/typed", "inline browser should search using the final path component, got " + calls[0]);
  const listenerCount = activeInlineInputListeners.size;
  inlineInput.value = "/typed-a";
  inlineInput.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 180));
  assert(activeInlineInputListeners.size === listenerCount,
    "replacing inline suggestions should not accumulate input listeners");
  picker = dom.window.document.querySelector(".chip-picker-dir");
  inlineInputEvents = 0;
  inlineChangeEvents = 0;

  picker.querySelector(".chip-picker-dir-row").click();
  await new Promise((r) => setTimeout(r, 0));
  assert(inlineInput.value === "/typed-a/accepted/", "clicking inline directory row should browse into that directory");
  assert(inlineInputEvents === 0, "browsing inline directory rows should not dispatch input events");
  assert(inlineChangeEvents === 0, "browsing inline directory rows should not dispatch change events");
  assert(dom.window.document.querySelector(".chip-picker-dir"), "inline picker should stay open after browsing a row");
  picker.querySelector(".chip-picker-dir-use").click();
  assert(inlineInput.value === "/typed-a/accepted", "accept control should keep browsed path in inline input");
  assert(inlineInputEvents === 1, "accepting inline directory should dispatch one input event");
  assert(inlineChangeEvents === 1, "accepting inline directory should dispatch one change event");
  assert(!dom.window.document.querySelector(".chip-picker-dir"), "inline picker should close after accepting current directory");

  const buttonInput = dom.window.document.getElementById("button-dir");
  const button = dom.window.document.getElementById("button-picker");
  let buttonInputEvents = 0;
  let buttonChangeEvents = 0;
  buttonInput.addEventListener("input", () => { buttonInputEvents++; });
  buttonInput.addEventListener("change", () => { buttonChangeEvents++; });

  button.click();
  await new Promise((r) => setTimeout(r, 0));
  picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "settings dir picker button should open shared .chip-picker-dir");
  assert(calls.includes("/opt/"), "button picker should list children of sibling text input value /opt, calls " + JSON.stringify(calls));
  picker.querySelector(".chip-picker-dir-row").click();
  await new Promise((r) => setTimeout(r, 0));
  assert(buttonInput.value === "/opt", "browsing button directory rows should not write sibling input yet");
  assert(buttonInputEvents === 0, "browsing button directory rows should not dispatch input events");
  assert(buttonChangeEvents === 0, "browsing button directory rows should not dispatch change events");
  picker.querySelector(".chip-picker-dir-use").click();
  assert(buttonInput.value === "/opt/accepted", "accept control should write browsed path to sibling input");
  assert(buttonInputEvents === 1, "accepting button directory should dispatch one input event");
  assert(buttonChangeEvents === 1, "accepting button directory should dispatch one change event");

  console.log("PASS test-settings-dir-picker");
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
