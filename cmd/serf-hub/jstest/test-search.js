// JSDOM tests for the search palette's "in-session" scope.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SEARCH_PATH = "../assets/search.js";
const searchSrc = fs.readFileSync(SEARCH_PATH, "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// Common dialog markup used by all scenarios.
function paletteHTML() {
  return `
    <dialog id="search-dialog">
      <input id="search-input" type="text">
      <div id="search-results"></div>
    </dialog>`;
}

function makeDom(extraBody) {
  return new JSDOM(`<!DOCTYPE html><html><body>${paletteHTML()}${extraBody || ""}</body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true });
}

function flush() { return new Promise(r => setTimeout(r, 0)); }

(async function run() {
  // -------- Scenario 1: in-session match wraps query in <mark>. --------
  {
    const dom = makeDom(`
      <div id="conversation">
        <div class="user-message"><div class="user-message-text">fix replay bug</div></div>
        <div class="assistant-message">unrelated reply</div>
      </div>`);
    const { window } = dom;
    // jsdom's HTMLDialogElement may lack showModal; polyfill minimally.
    const dlg = window.document.getElementById("search-dialog");
    if (typeof dlg.showModal !== "function") {
      dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
      dlg.close = function () { this.removeAttribute("open"); this.open = false; };
    }
    // Stub fetch to simulate empty live/past response.
    window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
    window.eval(searchSrc);
    // JSDOM fires DOMContentLoaded asynchronously; wait one tick for init().
    await new Promise(r => setTimeout(r, 30));
    // Trigger init — script evaluated already; document.readyState is "complete".
    const input = window.document.getElementById("search-input");
    dlg.showModal();
    input.value = "replay";
    // Directly invoke search via input event then wait for debounce.
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    await flush();
    const html = window.document.getElementById("search-results").innerHTML;
    pass(/In session/.test(html), "section header 'In session' renders");
    pass(/<mark>replay<\/mark>/.test(html), "matched substring is wrapped in <mark>");
    pass(/turn 1/.test(html), "turn position is shown");
  }

  // -------- Scenario 2: Enter on in-session result scrolls + adds class. --------
  {
    const dom = makeDom(`
      <div id="conversation">
        <div class="user-message">fix replay bug</div>
      </div>`);
    const { window } = dom;
    const dlg = window.document.getElementById("search-dialog");
    if (typeof dlg.showModal !== "function") {
      dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
      dlg.close = function () { this.removeAttribute("open"); this.open = false; };
    }
    window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });

    let scrollCalls = 0;
    const userMsg = window.document.querySelector(".user-message");
    userMsg.scrollIntoView = function () { scrollCalls += 1; };

    window.eval(searchSrc);
    // JSDOM fires DOMContentLoaded asynchronously; wait one tick for init().
    await new Promise(r => setTimeout(r, 30));
    const input = window.document.getElementById("search-input");
    dlg.showModal();
    input.value = "replay";
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    await flush();

    // First (only) item should be the in-session match — active=0 already.
    const enter = new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    input.dispatchEvent(enter);
    pass(scrollCalls === 1, "Enter triggers scrollIntoView on matched element");
    pass(userMsg.classList.contains("search-hit"), "matched element gets .search-hit class");
  }

  // -------- Scenario 2b: Shift+Enter also jumps for in-session. --------
  {
    const dom = makeDom(`
      <div id="conversation">
        <div class="assistant-message">replay later</div>
      </div>`);
    const { window } = dom;
    const dlg = window.document.getElementById("search-dialog");
    if (typeof dlg.showModal !== "function") {
      dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
      dlg.close = function () { this.removeAttribute("open"); this.open = false; };
    }
    window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
    let scrollCalls = 0;
    const msg = window.document.querySelector(".assistant-message");
    msg.scrollIntoView = function () { scrollCalls += 1; };
    window.eval(searchSrc);
    // JSDOM fires DOMContentLoaded asynchronously; wait one tick for init().
    await new Promise(r => setTimeout(r, 30));
    const input = window.document.getElementById("search-input");
    dlg.showModal();
    input.value = "replay";
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    await flush();
    const shiftEnter = new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true });
    input.dispatchEvent(shiftEnter);
    pass(scrollCalls === 1, "Shift+Enter also triggers scrollIntoView for in-session");
    pass(msg.classList.contains("search-hit"), "Shift+Enter adds .search-hit class");
  }

  // -------- Scenario 3: no #conversation → no in-session section. --------
  {
    const dom = makeDom(""); // no conversation in body
    const { window } = dom;
    const dlg = window.document.getElementById("search-dialog");
    if (typeof dlg.showModal !== "function") {
      dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
      dlg.close = function () { this.removeAttribute("open"); this.open = false; };
    }
    window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
    window.eval(searchSrc);
    // JSDOM fires DOMContentLoaded asynchronously; wait one tick for init().
    await new Promise(r => setTimeout(r, 30));
    const input = window.document.getElementById("search-input");
    dlg.showModal();
    input.value = "anything";
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    await flush();
    const html = window.document.getElementById("search-results").innerHTML;
    pass(!/In session/.test(html), "no in-session section without #conversation");
    pass(/no matches in live, past, or this session/.test(html), "empty-state copy uses spec wording");
  }

  // -------- Scenario 4: empty query renders nothing. --------
  {
    const dom = makeDom(`
      <div id="conversation">
        <div class="user-message">hello world</div>
      </div>`);
    const { window } = dom;
    const dlg = window.document.getElementById("search-dialog");
    if (typeof dlg.showModal !== "function") {
      dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
      dlg.close = function () { this.removeAttribute("open"); this.open = false; };
    }
    let fetched = false;
    window.fetch = () => { fetched = true; return Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) }); };
    window.eval(searchSrc);
    // JSDOM fires DOMContentLoaded asynchronously; wait one tick for init().
    await new Promise(r => setTimeout(r, 30));
    const input = window.document.getElementById("search-input");
    dlg.showModal();
    input.value = "";
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    await flush();
    const html = window.document.getElementById("search-results").innerHTML;
    pass(html === "", "empty query renders no results html");
    pass(fetched === false, "empty query does not hit /api/search");
  }

  if (failures.length === 0) {
    console.log("PASS: search palette in-session scope works");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
