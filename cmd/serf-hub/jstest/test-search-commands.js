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

  // -------- Scenario 2b: fuzzy command matching supports abbreviations --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/cm";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    const html = ctx.results.innerHTML;
    pass(/Compact transcript/.test(html), '"/cm" fuzzy-matches Compact transcript');
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

  // -------- Scenario 5b: /project names the actual sidebar-reveal behavior --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/project";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(/Reveal session's project in sidebar/.test(ctx.results.innerHTML), '"/project" labels sidebar reveal behavior');
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
    // Provide /api/models response so the model command's enum source resolves.
    ctx.window.fetch = (url) => {
      if (url === "/api/models") {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([
          { provider: "anthropic", model: "claude-opus-4-7", display_name: "Opus 4.7" },
          { provider: "anthropic", model: "claude-sonnet-4-6", display_name: "Sonnet 4.6" },
        ])});
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
    // The user typed "/model" to get into args mode. After backing out
    // they should see that same filter restored, not a generic "/".
    pass(ctx.input.value === "/model", "Esc from args restores pre-args filter (got " + JSON.stringify(ctx.input.value) + ")");
  }

  // -------- Scenario 9: Selecting an enum item POSTs to /s/<id>/model --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    let postedPath = null;
    let postedBody = null;
    ctx.window.fetch = (url, opts) => {
      if (url === "/api/models") {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([
          { provider: "anthropic", model: "claude-opus-4-7", display_name: "Opus 4.7" },
        ])});
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
    pass(/anthropic\/claude-opus-4-7/.test(String(postedBody)), "POST body contains provider/model id");
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

  // -------- Scenario 10b: ARIA roles and active-descendant tracking --------
  {
    const dom = makeDom("");
    const ctx = await loadAndOpen(dom);
    // Add ARIA to test markup that paletteHTML() leaves bare.
    ctx.results.setAttribute("role", "listbox");
    ctx.input.setAttribute("role", "combobox");
    ctx.input.setAttribute("aria-controls", "search-results");
    ctx.window.SerfSearch.open();
    ctx.input.value = "/";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    const rows = ctx.results.querySelectorAll(".search-row");
    pass(rows.length > 0, "command-filter renders rows");
    pass(rows[0].getAttribute("role") === "option", "row has role=option");
    pass(rows[0].getAttribute("aria-selected") === "true", "active row has aria-selected=true");
    pass(ctx.input.getAttribute("aria-activedescendant") === rows[0].id, "input aria-activedescendant points at active row");
    // Move down; assert active-descendant follows.
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
    await tick(10);
    pass(ctx.input.getAttribute("aria-activedescendant") === rows[1].id, "ArrowDown moves aria-activedescendant");
  }

  // -------- Scenario 10c: argless commands persist into Recent --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    ctx.window.SerfSearch.open();
    ctx.input.value = "/compact";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await tick(20);

    ctx.window.SerfSearch.openWith("/");
    await tick(20);
    const html = ctx.results.innerHTML;
    pass(/Recent/.test(html), "recent section is shown after running an argless command");
    pass(html.indexOf("Recent") < html.indexOf("Commands"), "recent section appears above commands");
    pass(/Compact transcript/.test(html), "recent section includes Compact transcript");
    pass(/compact/.test(ctx.window.localStorage.getItem("serf.search.recentCommands") || ""), "recent command persisted");
  }

  // -------- Scenario 11: Every command dispatches its declared side effect --------
  // One row per registered command. Build a fresh JSDOM per case so fetches,
  // clicks, and navigations don't leak.
  await commandSweep();

  if (failures.length === 0) {
    console.log("PASS: command palette covers filter, args, scope, openWith, dispatch");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();

async function commandSweep() {
  const cases = [
    { name: "new", page: "home", query: "/new s",
      expect: (c) => assertCS(c, c.calls.navigations.slice(-1)[0] === "/new", "navigated to /new") },
    { name: "spawn", page: "home", query: "/spawn", argEntry: "do the thing",
      expect: (c) => assertCS(c, c.calls.navigations.slice(-1)[0] === "/new?prompt=do%20the%20thing", "navigated to /new?prompt=<encoded>") },
    { name: "settings", page: "home", query: "/settings",
      expect: (c) => assertCS(c, c.calls.navigations.slice(-1)[0] === "/settings", "navigated to /settings") },
    { name: "theme", page: "home", query: "/theme", argEntry: "light",
      expect: (c) => {
        assertCS(c, c.document.body.classList.contains("light-theme"), "body has light-theme class");
        assertCS(c, !c.document.body.classList.contains("dark-theme"), "body no longer has dark-theme class");
        let stored = null;
        try { stored = c.window.localStorage.getItem("serf-hub.theme"); } catch (_) {}
        assertCS(c, stored === "light", "theme persisted to localStorage (got " + stored + ")");
      } },
    { name: "dashboard", page: "home", query: "/dashboard",
      expect: (c) => assertCS(c, c.calls.navigations.slice(-1)[0] === "/", "navigated to /") },
    { name: "search", page: "home", query: "/search", expectStaysOpen: true,
      expect: (c) => assertCS(c, c.input.value === "", "search command clears input") },
    { name: "help", page: "home", query: "/help", expectStaysOpen: true,
      expect: (c) => {
        assertCS(c, /Keyboard shortcuts/.test(c.results.innerHTML), "help renders Keyboard shortcuts section");
        assertCS(c, /⌘K/.test(c.results.innerHTML), "help mentions ⌘K");
      } },
    { name: "compact", page: "session", query: "/compact",
      expect: (c) => assertCS(c, sawFetchCS(c, "POST", "/s/01S/compact"), "POST /s/01S/compact") },
    { name: "interrupt", page: "session", query: "/interrupt",
      expect: (c) => assertCS(c, sawFetchCS(c, "POST", "/s/01S/interrupt"), "POST /s/01S/interrupt") },
    { name: "clear", page: "session", query: "/clear",
      expect: (c) => assertCS(c, sawFetchCS(c, "POST", "/s/01S/clear"), "POST /s/01S/clear") },
    { name: "shutdown", page: "session", query: "/shutdown", confirmAnswer: false,
      expect: (c) => {
        assertCS(c, c.calls.confirms === 0, "shutdown does not ask confirm()");
        assertCS(c, sawFetchCS(c, "POST", "/s/01S/shutdown"), "POST /s/01S/shutdown");
      } },
    { name: "model", page: "session", query: "/model", argEntry: "opus",
      modelsResponse: [{ provider: "anthropic", model: "claude-opus-4-7", display_name: "Opus 4.7" }],
      expect: (c) => {
        const hit = c.calls.fetches.find(f => f.url === "/s/01S/model");
        assertCS(c, !!hit, "POST /s/01S/model");
        const body = String(hit && hit.opts && hit.opts.body || "");
        assertCS(c, body.indexOf("anthropic/claude-opus-4-7") >= 0, "body carries provider/model id (got " + body + ")");
      } },
    { name: "steer", page: "session", query: "/steer", argEntry: "less rambling",
      expect: (c) => {
        const hit = c.calls.fetches.find(f => f.url === "/s/01S/steer");
        assertCS(c, !!hit, "POST /s/01S/steer");
        assertCS(c, /less rambling/.test(String(hit && hit.opts && hit.opts.body)), "body carries steer text");
      } },
    { name: "copy-id", page: "session", query: "/copy",
      expect: (c) => assertCS(c, c.calls.clipboardWrites.slice(-1)[0] === "01S", "wrote session ID to clipboard") },
    { name: "tasks", page: "session", query: "/tasks",
      expect: (c) => assertCS(c, c.calls.panelClicks.tasks === 1, "tasks trigger clicked") },
    { name: "status", page: "session", query: "/status",
      expect: (c) => assertCS(c, c.calls.panelClicks.details === 1, "details trigger clicked") },
    { name: "project", page: "session", query: "/project", withSidebarLink: true,
      expect: (c) => {
        const section = c.document.querySelector("[data-project-key]");
        assertCS(c, section && !section.classList.contains("collapsed"), "project section uncollapsed");
        assertCS(c, c.calls.scrolledSections.includes("demo"), "project section scrolled into view");
      } },
  ];
  for (const tc of cases) await runCaseCS(tc);
}

function assertCS(ctx, cond, msg) {
  if (!cond) failures.push("FAIL [" + ctx.caseName + "]: " + msg);
}
function sawFetchCS(ctx, method, url) {
  return ctx.calls.fetches.some(f => f.url === url && (f.opts && f.opts.method) === method);
}

async function runCaseCS(tc) {
  let extraBody = "";
  let url = "http://localhost/";
  if (tc.page === "session") {
    extraBody = `<div id="conversation" data-session-id="01S" data-state="live">`;
    if (tc.withUserTurns) tc.withUserTurns.forEach(t => { extraBody += `<div class="user-message">${t}</div>`; });
    extraBody += `</div>`;
    extraBody += `<button data-tasks-trigger></button><button data-details-trigger></button>`;
    if (tc.withSidebarLink) {
      extraBody += `<nav class="sidebar"><section class="sidebar-section" data-project-key="demo"><a href="/s/01S">my live session</a></section></nav>`;
    }
    url = "http://localhost/s/01S";
  }
  const dom = makeDom(extraBody, { url: url });
  const { window } = dom;
  const dlg = window.document.getElementById("search-dialog");
  if (typeof dlg.showModal !== "function") {
    dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
    dlg.close = function () { this.removeAttribute("open"); this.open = false; };
  }

  const calls = {
    navigations: [],
    fetches: [],
    clipboardWrites: [],
    confirms: 0,
    panelClicks: { tasks: 0, details: 0 },
    scrolledSections: [],
  };

  window.fetch = (u, opts) => {
    if (u === "/api/models") {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(tc.modelsResponse || []) });
    }
    calls.fetches.push({ url: u, opts: opts || {} });
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  };
  window.confirm = () => { calls.confirms += 1; return tc.confirmAnswer === undefined ? true : tc.confirmAnswer; };
  Object.defineProperty(window.navigator, "clipboard", {
    configurable: true,
    value: { writeText: (s) => { calls.clipboardWrites.push(s); return Promise.resolve(); } },
  });
  window.document.querySelectorAll("[data-project-key]").forEach(sec => {
    sec.scrollIntoView = function () { calls.scrolledSections.push(this.getAttribute("data-project-key")); };
  });
  const tasksBtn = window.document.querySelector("[data-tasks-trigger]");
  if (tasksBtn) tasksBtn.click = () => { calls.panelClicks.tasks += 1; };
  const detailsBtn = window.document.querySelector("[data-details-trigger]");
  if (detailsBtn) detailsBtn.click = () => { calls.panelClicks.details += 1; };

  window.eval(searchSrc);
  await new Promise(r => setTimeout(r, 30));
  const input = window.document.getElementById("search-input");
  const results = window.document.getElementById("search-results");

  // JSDOM's Location.assign is non-configurable; production routes through
  // window.SerfSearch.Nav.go so tests can capture targets.
  if (window.SerfSearch && window.SerfSearch.Nav) {
    window.SerfSearch.Nav.go = (u) => { calls.navigations.push(u); };
  }

  const ctx = { caseName: tc.name, window: window, document: window.document, dialog: dlg, input: input, results: results, calls: calls };

  window.SerfSearch.open();
  input.value = tc.query;
  input.dispatchEvent(new window.Event("input", { bubbles: true }));
  await new Promise(r => setTimeout(r, 20));
  input.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 30));

  if (tc.argEntry !== undefined) {
    input.value = tc.argEntry;
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 30));
    input.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    await new Promise(r => setTimeout(r, 20));
  }

  tc.expect(ctx);

  if (!tc.expectStaysOpen) {
    assertCS(ctx, dlg.open === false, "dialog closed after run");
  } else {
    assertCS(ctx, dlg.open === true, "dialog stays open");
  }
}
