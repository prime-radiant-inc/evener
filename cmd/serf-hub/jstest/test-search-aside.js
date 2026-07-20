// JSDOM tests for the search palette's /aside command: fork the current
// session at its tip into a side thread and open it.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

// thread-state.js defines window.SerfThreadState; production loads it first
// (templates/app.html), so prepend it here.
const threadStateSrc = fs.readFileSync(path.resolve(__dirname, "../assets/thread-state.js"), "utf8");
const searchSrc = threadStateSrc + "\n" + fs.readFileSync(path.resolve(__dirname, "../assets/search.js"), "utf8");

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
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
  window.eval(searchSrc);
  await new Promise(r => setTimeout(r, 30)); // let DOMContentLoaded fire
  return { window: window, dialog: dlg, input: window.document.getElementById("search-input"), results: window.document.getElementById("search-results") };
}

function tick(ms) { return new Promise(r => setTimeout(r, ms)); }

async function runAside(ctx) {
  ctx.window.SerfSearch.open();
  ctx.input.value = "/aside";
  ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
  await tick(20);
  ctx.input.dispatchEvent(new ctx.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  await tick(30);
}

(async function run() {
  // -------- Scenario 1: "/aside" filters to the Aside command --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    ctx.window.SerfSearch.open();
    ctx.input.value = "/asi";
    ctx.input.dispatchEvent(new ctx.window.Event("input", { bubbles: true }));
    await tick(20);
    pass(/Aside/.test(ctx.results.innerHTML), '"/asi" matches the Aside command');
  }

  // -------- Scenario 2: appwire path forks at tip and opens the side thread --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    const calls = [];
    const navs = [];
    let sidebarRefresh = 0;
    ctx.window.SerfAppwire = {
      asideThread: (sessionId) => {
        calls.push(sessionId);
        return Promise.resolve({ ref: "local:child1", session_id: "child1" });
      },
    };
    ctx.window.htmx = { trigger: () => { sidebarRefresh += 1; } };
    ctx.window.SerfSearch.Nav.go = (url) => { navs.push(url); };
    await runAside(ctx);
    pass(calls.length === 1 && calls[0] === "01S", "asideThread called once with current session id (got " + JSON.stringify(calls) + ")");
    pass(navs.length === 1 && navs[0] === "/s/child1", "navigates to the aside child session (got " + JSON.stringify(navs) + ")");
    pass(sidebarRefresh === 1, "sidebar refresh triggered so the side thread shows up");
    pass(ctx.dialog.open === false, "dialog closes after aside succeeds");
  }

  // -------- Scenario 3: REST fallback posts to /s/<id>/aside --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    const fetches = [];
    const navs = [];
    ctx.window.fetch = (url, opts) => {
      fetches.push({ url: url, method: opts && opts.method });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ child_session_id: "child2" }) });
    };
    ctx.window.SerfSearch.Nav.go = (url) => { navs.push(url); };
    await runAside(ctx);
    pass(fetches.length === 1 && fetches[0].url === "/s/01S/aside" && fetches[0].method === "POST",
      "REST fallback posts to /s/01S/aside (got " + JSON.stringify(fetches) + ")");
    pass(navs.length === 1 && navs[0] === "/s/child2", "REST fallback navigates to the child session (got " + JSON.stringify(navs) + ")");
  }

  // -------- Scenario 4: failure keeps the palette open with an inline error --------
  {
    const dom = makeDom(`<div id="conversation" data-session-id="01S" data-state="live"></div>`,
      { url: "http://localhost/s/01S" });
    const ctx = await loadAndOpen(dom);
    const navs = [];
    ctx.window.SerfAppwire = {
      asideThread: () => Promise.reject(new Error("aside is only supported for local serf threads")),
    };
    ctx.window.SerfSearch.Nav.go = (url) => { navs.push(url); };
    await runAside(ctx);
    pass(navs.length === 0, "failed aside does not navigate");
    pass(ctx.dialog.open === true, "dialog stays open after aside failure");
    pass(/aside is only supported for local serf threads/.test(ctx.results.innerHTML),
      "inline palette error shows the failure (got " + JSON.stringify(ctx.results.textContent) + ")");
  }

  if (failures.length) {
    console.error(failures.join("\n"));
    process.exit(1);
  }
  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });
