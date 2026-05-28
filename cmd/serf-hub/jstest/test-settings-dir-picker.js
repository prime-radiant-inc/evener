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
      return Promise.resolve({
        results: [
          { path: prefix + "/accepted", is_git: true },
          { path: prefix + "/other", is_git: false },
        ],
      });
    },
  };
  dom.window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

  dom.window.eval(dirPickerSrc);
  dom.window.eval(settingsPickersSrc);
  dom.window.document.dispatchEvent(new dom.window.Event("DOMContentLoaded", { bubbles: true }));

  const inlineInput = dom.window.document.getElementById("inline-dir");
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
  assert(calls[0] === "/typed", "inline autocomplete should call completeDirs with current input value, got " + calls[0]);
  inlineInputEvents = 0;
  inlineChangeEvents = 0;

  picker.querySelector(".chip-picker-dir-row").click();
  assert(inlineInput.value === "/typed/accepted", "clicking inline directory row should write accepted path to input");
  assert(inlineInputEvents === 1, "clicking inline directory row should dispatch one input event");
  assert(inlineChangeEvents === 1, "clicking inline directory row should dispatch one change event");
  assert(!dom.window.document.querySelector(".chip-picker-dir"), "inline picker should close after accepting a row");

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
  assert(calls.includes("/opt"), "button picker should call completeDirs with sibling text input value /opt, calls " + JSON.stringify(calls));
  picker.querySelector(".chip-picker-dir-row").click();
  assert(buttonInput.value === "/opt/accepted", "clicking button directory row should write accepted path to sibling input");
  assert(buttonInputEvents === 1, "button directory row should dispatch one input event");
  assert(buttonChangeEvents === 1, "button directory row should dispatch one change event");

  console.log("PASS test-settings-dir-picker");
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
