// Verifies that SerfRenderer.showForkDialog() navigates to the correct session
// URL after a successful fork, honouring the field priority order:
//   json.ref  >  json.child_session_id  >  json.session_id
//
// The three scenarios are each driven by a real click on the .fork-confirm
// button inside a JSDOM harness so that mutations to the navigation expression
// (e.g. wrapping it in `if (false)` or removing a field from the OR chain)
// cause a test failure.
"use strict";

const path = require("path");
const { JSDOM } = require("jsdom");

// spyOnLocationHref patches the jsdom Location implementation to capture
// navigation targets before jsdom's no-op "not implemented" handler discards
// them.  Returns `{ navigations, restore }`.
function spyOnLocationHref(window) {
  const loc = window.location;
  const implSym = Object.getOwnPropertySymbols(loc)
    .find((s) => s.toString() === "Symbol(impl)");
  if (!implSym) throw new Error("jsdom location impl symbol not found — jsdom API changed?");
  const impl = loc[implSym];
  const navigations = [];
  const origNavigate = impl._locationObjectSetterNavigate.bind(impl);
  impl._locationObjectSetterNavigate = (url) => {
    navigations.push("/" + url.path.join("/"));
  };
  return { navigations, restore: () => { impl._locationObjectSetterNavigate = origNavigate; } };
}

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01HOST"></header>
    <div id="conversation" data-session-id="01HOST" data-state="active"></div>
    <form data-input-form data-session-id="01HOST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    // url is required so that relative URLs like /s/foo are resolvable by
    // jsdom's URL parser when the renderer sets window.location.href.
    url: "http://localhost/s/01HOST",
  });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({
    ok: true, json: () => Promise.resolve({}), text: () => Promise.resolve(""),
  });

  require(path.resolve(__dirname, "./load-renderer")).evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  const locationSpy = spyOnLocationHref(window);
  return { window, locationSpy };
}

// makeUserWrap returns the minimal DOM structure showForkDialog() requires:
// a container parent with a userWrap inside, which holds a .pill child.
function makeUserWrap(document, entryIdx) {
  const container = document.createElement("div");
  const userWrap = document.createElement("div");
  userWrap.dataset.entryIdx = String(entryIdx != null ? entryIdx : 1);
  const pill = document.createElement("div");
  pill.className = "pill";
  const textEl = document.createElement("span");
  textEl.className = "user-message-text";
  textEl.textContent = "hello world";
  pill.appendChild(textEl);
  userWrap.appendChild(pill);
  container.appendChild(userWrap);
  document.body.appendChild(container);
  return { container, userWrap };
}

async function clickConfirm(document) {
  const btn = document.querySelector(".fork-confirm");
  if (!btn) throw new Error("no .fork-confirm button found after showForkDialog");
  btn.click();
  // Let the async onclick settle (forkThread promise + microtasks).
  await new Promise((r) => setTimeout(r, 30));
}

function makeForkAppwire(json) {
  return {
    forkThread: () => Promise.resolve(json),
    refForSession: (s) => "ref:" + s,
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    tasks: () => Promise.resolve([]),
    readThread: () => Promise.resolve({}),
  };
}

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

(async () => {
  // -----------------------------------------------------------------------
  // 1. json.ref present → navigate to /s/<ref> (highest priority).
  //    Even when child_session_id and session_id are also present, ref wins.
  // -----------------------------------------------------------------------
  {
    const { window, locationSpy } = newHarness();
    await new Promise((r) => setTimeout(r, 30));

    window.SerfAppwire = makeForkAppwire({
      ref: "remote:abc",
      child_session_id: "cid-1",
      session_id: "sid-1",
    });

    const { container, userWrap } = makeUserWrap(window.document, 3);
    window.SerfRenderer.showForkDialog(userWrap, "original text", "edited text");
    await clickConfirm(window.document);

    const nav = locationSpy.navigations[0];
    pass(
      nav === "/s/" + encodeURIComponent("remote:abc"),
      "json.ref present: expected /s/remote%3Aabc, got " + nav
    );
    container.remove();
  }

  // -----------------------------------------------------------------------
  // 2. json.ref absent, json.child_session_id present → use child_session_id.
  // -----------------------------------------------------------------------
  {
    const { window, locationSpy } = newHarness();
    await new Promise((r) => setTimeout(r, 30));

    window.SerfAppwire = makeForkAppwire({
      child_session_id: "cid-2",
      session_id: "sid-2",
    });

    const { container, userWrap } = makeUserWrap(window.document, 1);
    window.SerfRenderer.showForkDialog(userWrap, "original", "edited");
    await clickConfirm(window.document);

    const nav = locationSpy.navigations[0];
    pass(
      nav === "/s/cid-2",
      "json.child_session_id fallback: expected /s/cid-2, got " + nav
    );
    container.remove();
  }

  // -----------------------------------------------------------------------
  // 3. ref and child_session_id both absent → fall back to session_id.
  // -----------------------------------------------------------------------
  {
    const { window, locationSpy } = newHarness();
    await new Promise((r) => setTimeout(r, 30));

    window.SerfAppwire = makeForkAppwire({ session_id: "sid-3" });

    const { container, userWrap } = makeUserWrap(window.document, 1);
    window.SerfRenderer.showForkDialog(userWrap, "original", "edited");
    await clickConfirm(window.document);

    const nav = locationSpy.navigations[0];
    pass(
      nav === "/s/sid-3",
      "json.session_id fallback: expected /s/sid-3, got " + nav
    );
    container.remove();
  }

  if (failures.length === 0) {
    console.log("PASS: renderer fork navigation preserves source-qualified refs");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
