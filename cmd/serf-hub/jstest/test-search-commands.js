// JSDOM tests for the search palette's command mode.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SEARCH_PATH = "../assets/search.js";
const searchSrc = fs.readFileSync(SEARCH_PATH, "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function paletteHTML() {
  return `
    <dialog id="search-dialog">
      <div class="search-dialog-inner">
        <header class="search-dialog-header">
          <span class="search-icon">🔍</span>
          <input id="search-input" type="text" placeholder="search live + past sessions">
          <span class="search-dialog-hint">esc</span>
        </header>
        <div id="search-results" class="search-results"></div>
      </div>
    </dialog>`;
}

function makeDom(extraBody, opts) {
  const url = (opts && opts.url) || "http://localhost/";
  return new JSDOM(`<!DOCTYPE html><html><body>${paletteHTML()}${extraBody || ""}</body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: url });
}

async function loadAndOpen(dom) {
  const { window } = dom;
  const dlg = window.document.getElementById("search-dialog");
  if (typeof dlg.showModal !== "function") {
    dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
    dlg.close = function () { this.removeAttribute("open"); this.open = false; };
  }
  // Default fetch stub; tests can replace.
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
  window.eval(searchSrc);
  await new Promise(r => setTimeout(r, 30)); // let DOMContentLoaded fire
  return { window: window, dialog: dlg, input: window.document.getElementById("search-input"), results: window.document.getElementById("search-results") };
}

function tick(ms) { return new Promise(r => setTimeout(r, ms)); }

(async function run() {
  // -------- Scenario 1: "/" enters command-filter mode without fetching --------
  {
    const dom = makeDom("");
    let fetchCalls = 0;
    const ctx = await loadAndOpen(dom);
    ctx.window.fetch = () => { fetchCalls += 1; return Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) }); };
    ctx.window.SerfSearch.open();
    ctx.input.value = "/";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    const html = ctx.results.innerHTML;
    pass(/Commands/.test(html), 'leading "/" renders Commands section');
    pass(fetchCalls === 0, 'leading "/" does not call /api/search');
  }

  // -------- Scenario 2: "/comp" filters to the compact command --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/comp";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    const html = ctx.results.innerHTML;
    pass(/Compact transcript/.test(html), '"/comp" matches Compact transcript');
    pass(!/Interrupt model call/.test(html), '"/comp" does not show Interrupt');
  }

  // -------- Scenario 3: Backspacing "/" returns to search mode --------
  {
    const dom = makeDom("");
    let fetchCalls = 0;
    const ctx = await loadAndOpen(dom);
    ctx.window.fetch = () => { fetchCalls += 1; return Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) }); };
    ctx.window.SerfSearch.open();
    ctx.input.value = "/comp";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(/Commands/.test(ctx.results.innerHTML), "starts in command mode");
    ctx.input.value = "";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(220); // wait past 150ms debounce
    pass(ctx.results.innerHTML === "", "empty query clears results (back to search mode)");
    pass(fetchCalls === 0, "empty query does not fetch");
  }

  // -------- Scenario 4: Esc from command-filter closes --------
  {
    const dom = makeDom("");
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(ctx.dialog.open === true, "dialog open before Esc");
    ctx.window.document.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await tick(10);
    pass(ctx.dialog.open === false, "Esc closes the dialog from command-filter mode");
  }

  // -------- Scenario 5: Session command absent on home, present on live session --------
  {
    // Home page (no #conversation)
    const homeDom = makeDom("", { url: "http://localhost/" });
    const home = await loadAndOpen(homeDom);
    home.window.SerfSearch.open();
    home.input.value = "/";
    home.input.dispatchEvent(new home.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(!/Compact transcript/.test(home.results.innerHTML), "compact hidden on home page");
    pass(/New session/.test(home.results.innerHTML), "global commands present on home page");
  }
  {
    // Live session
    const liveDom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const live = await loadAndOpen(liveDom);
    live.window.SerfSearch.open();
    live.input.value = "/";
    live.input.dispatchEvent(new live.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(/Compact transcript/.test(live.results.innerHTML), "compact shown on live session");
  }

  // -------- Scenario 6: Session command absent when state=ended; ended-ok remains --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="ended"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(!/Compact transcript/.test(ctx.results.innerHTML), "compact hidden when state=ended");
    pass(/Copy session ID/.test(ctx.results.innerHTML), "copy-id (ended-ok) shown when ended");
    pass(/Toggle tasks panel/.test(ctx.results.innerHTML), "tasks (ended-ok) shown when ended");
  }

  // -------- Scenario 7: Argless command POSTs to the right endpoint and closes --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    let postedPath = null;
    let postedMethod = null;
    ctx.window.fetch = (url, opts) => {
      postedPath = url;
      postedMethod = (opts && opts.method) || "GET";
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    };
    ctx.window.SerfSearch.open();
    ctx.input.value = "/compact";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await tick(10);
    pass(postedPath === "/s/01S/compact", "compact POSTs to /s/<id>/compact (got " + postedPath + ")");
    pass(postedMethod === "POST", "compact uses POST");
    pass(ctx.dialog.open === false, "dialog closes after argless run");
  }

  // -------- Scenario 8: Enum-args command transitions to args mode, lists items, Esc returns to filter --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    // Provide /models response so the model command's enum source resolves.
    ctx.window.fetch = (url) => {
      if (url === "/models") {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({
          models: [
            { id: "claude-opus-4-7", display_name: "Opus 4.7" },
            { id: "claude-sonnet-4-6", display_name: "Sonnet 4.6" },
          ]
        })});
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    };
    ctx.window.SerfSearch.open();
    ctx.input.value = "/model";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(/Switch model/.test(ctx.results.innerHTML), "command-filter shows Switch model");
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await tick(20); // let the model fetch resolve
    const pillEl = ctx.window.document.querySelector(".search-cmd-pill");
    pass(pillEl && !pillEl.hidden, "args-mode pill shown");
    pass(/Opus 4\.7/.test(ctx.results.innerHTML), "model list rendered");

    // Esc from args mode returns to filter, not closed.
    ctx.window.document.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await tick(20);
    pass(ctx.dialog.open === true, "Esc from args mode does NOT close the dialog");
    pass(pillEl.hidden === true, "args-mode pill hidden after Esc");
    pass(/Commands/.test(ctx.results.innerHTML), "Esc from args returns to command-filter");
  }

  // -------- Scenario 9: Selecting an enum item POSTs to /s/<id>/model --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    let postedPath = null;
    let postedBody = null;
    ctx.window.fetch = (url, opts) => {
      if (url === "/models") {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({
          models: [{ id: "claude-opus-4-7", display_name: "Opus 4.7" }]
        })});
      }
      postedPath = url;
      postedBody = opts && opts.body;
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    };
    ctx.window.SerfSearch.open();
    ctx.input.value = "/model";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await tick(30);
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await tick(10);
    pass(postedPath === "/s/01S/model", "selecting a model POSTs to /s/<id>/model (got " + postedPath + ")");
    pass(/claude-opus-4-7/.test(String(postedBody)), "POST body contains chosen model id");
    pass(ctx.dialog.open === false, "dialog closes after selecting a model");
  }

  // -------- Scenario 10: openWith("/") opens and seeds the query --------
  {
    const dom = makeDom("");
    const ctx = await loadAndOpen(dom);
    pass(ctx.dialog.open === false, "dialog initially closed");
    ctx.window.SerfSearch.openWith("/");
    await tick(20);
    pass(ctx.dialog.open === true, "openWith opens the dialog");
    pass(ctx.input.value === "/", "openWith seeds the input with the given query");
    pass(/Commands/.test(ctx.results.innerHTML), "openWith with / enters command-filter mode");
  }

  if (failures.length === 0) {
    console.log("PASS: command palette covers filter, args, scope, openWith");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
