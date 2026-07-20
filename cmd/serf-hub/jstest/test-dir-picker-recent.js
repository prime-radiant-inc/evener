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

function makeDom(appwire) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="anchor-wrap"><button id="anchor" type="button">dir</button></div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  dom.window.SerfAppwire = appwire;
  dom.window.eval(dirPickerSrc);
  return dom;
}

(async function main() {
  // The 15 most recent projects prepopulate the picker on an empty open
  // (issue #35), most-recent first, above the browse listing.
  const recent = Array.from({ length: 15 }, (_, i) => "/home/jesse/proj-" + String(i).padStart(2, "0"));
  let recentCalls = 0;
  const accepted = [];
  const dom = makeDom({
    completeDirs() {
      return Promise.resolve({ results: [{ path: "/home/jesse/other", is_git: false }] });
    },
    recentProjects() {
      recentCalls++;
      return Promise.resolve(recent);
    },
  });
  const anchor = dom.window.document.getElementById("anchor");
  dom.window.SerfDirPicker.open({ anchor, currentValue: "", onAccept(v) { accepted.push(v); } });
  await new Promise((r) => setTimeout(r, 0));

  const picker = dom.window.document.querySelector(".chip-picker-dir");
  assert(picker, "picker should render");
  assert(recentCalls === 1, "recentProjects should be fetched once on an empty open, got " + recentCalls);
  const results = picker.querySelector(".chip-picker-results");
  const header = results.querySelector(".chip-picker-dir-recent-header");
  assert(header, "recent projects section header should render");
  assert(results.firstElementChild === header, "recent projects section should sit above the browse listing");
  const rows = picker.querySelectorAll(".chip-picker-dir-recent");
  assert(rows.length === 15, "picker should render the 15 most recent projects as options, got " + rows.length);
  assert(rows[0].dataset.recentPath === recent[0], "first recent option should be the most recently used project");
  assert(rows[14].dataset.recentPath === recent[14], "recent options should keep the server's recency order");
  assert(picker.querySelector('[data-dir-path="/home/jesse/other"]'),
    "browse rows should still render below the recent projects");

  rows[0].click();
  await new Promise((r) => setTimeout(r, 0));
  assert(accepted[0] === recent[0], "clicking a recent project option should accept that path");
  assert(!dom.window.document.querySelector(".chip-picker-dir"), "picker should close after accepting a recent project");

  // Typing a query swaps the recent options for search results.
  dom.window.SerfDirPicker.open({ anchor, currentValue: "", onAccept(v) { accepted.push(v); } });
  await new Promise((r) => setTimeout(r, 0));
  const picker2 = dom.window.document.querySelector(".chip-picker-dir");
  const input2 = picker2.querySelector(".chip-picker-search");
  input2.value = "/home/jesse/ot";
  input2.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 170));
  await new Promise((r) => setTimeout(r, 0));
  assert(picker2.querySelectorAll(".chip-picker-dir-recent").length === 0,
    "typing a query should replace the recent options with search results");
  assert(picker2.querySelector('[data-dir-path="/home/jesse/other"]'),
    "search results should still render after typing");
  input2.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));

  // The initial listing shows recents even when the picker opens on an
  // existing value (the spawn flow passes the last-used dir); browsing into
  // a directory swaps back to plain results.
  recentCalls = 0;
  dom.window.SerfDirPicker.open({ anchor, currentValue: "/home/jesse", onAccept(v) { accepted.push(v); } });
  await new Promise((r) => setTimeout(r, 0));
  const pickerInit = dom.window.document.querySelector(".chip-picker-dir");
  assert(recentCalls === 1, "initial listing on an existing value should fetch recents, got " + recentCalls);
  assert(pickerInit.querySelectorAll(".chip-picker-dir-recent").length === 15,
    "initial listing on an existing value should show the recent options");
  pickerInit.querySelector('[data-dir-path="/home/jesse/other"]').click();
  await new Promise((r) => setTimeout(r, 0));
  assert(dom.window.document.querySelectorAll(".chip-picker-dir-recent").length === 0,
    "browsing into a directory should replace the recent options");
  const pickerEsc = dom.window.document.querySelector(".chip-picker-dir");
  pickerEsc.querySelector(".chip-picker-search").dispatchEvent(
    new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));

  // An older hub client without recentProjects must not break the picker.
  const dom2 = makeDom({
    completeDirs() {
      return Promise.resolve({ results: [{ path: "/home/jesse/other", is_git: false }] });
    },
  });
  const anchor2 = dom2.window.document.getElementById("anchor");
  dom2.window.SerfDirPicker.open({ anchor: anchor2, currentValue: "", onAccept() {} });
  await new Promise((r) => setTimeout(r, 0));
  const picker3 = dom2.window.document.querySelector(".chip-picker-dir");
  assert(picker3, "picker should render without recentProjects support");
  assert(picker3.querySelectorAll(".chip-picker-dir-recent").length === 0,
    "no recent section without recentProjects support");
  assert(picker3.querySelector('[data-dir-path="/home/jesse/other"]'),
    "browse rows should still render without recentProjects support");

  // Layout contract: recent rows put the path in the SHRINKING column
  // (minmax(0, 1fr), where it ellipsizes rtl) and the basename in the
  // natural-width column — the shared minmax(0,1fr)/auto grid starves the
  // name to zero width on long unbroken paths (delegate worktree ids) and
  // wraps it one character per line.
  {
    const css = require("fs").readFileSync(require("path").resolve(__dirname, "../assets/style.css"), "utf8");
    const m = /\.chip-picker-dir-recent\s*\{([^{}]*)\}/.exec(css);
    assert(m && /grid-template-columns:\s*auto\s+minmax\(0,\s*1fr\)/.test(m[1]),
      ".chip-picker-dir-recent must override the row grid to auto minmax(0, 1fr) so paths ellipsize instead of starving names");
  }

  console.log("PASS test-dir-picker-recent");
})();
