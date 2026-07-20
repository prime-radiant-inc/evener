// Fork-from-message (issue #42): every transcript message carries a fork
// affordance. Activating it forks the session at the point where that message
// was entered — for an assistant message, at the user prompt that produced
// it — and stages the original message text in the forked session's composer
// (via the sticky per-session draft) ready for editing. The forked session
// must NOT auto-run the message: forkThread goes out with defer_input and no
// startTurn/turn-start call is made.
"use strict";

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const DRAFTS_SRC = fs.readFileSync(path.resolve(__dirname, "../assets/drafts.js"), "utf8");

// spyOnLocationHref patches the jsdom Location implementation to capture
// navigation targets (same technique as test-renderer-fork-ref.js).
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

function newHarness(sessionId, seed) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="${sessionId}"></header>
    <div id="conversation" data-session-id="${sessionId}" data-state="active"></div>
    <form data-input-form data-session-id="${sessionId}">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://localhost/s/" + sessionId,
  });

  const { window } = dom;
  for (const [k, v] of Object.entries(seed || {})) window.localStorage.setItem(k, v);
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({
    ok: true, json: () => Promise.resolve({}), text: () => Promise.resolve(""),
  });

  window.eval(DRAFTS_SRC);
  require(path.resolve(__dirname, "./load-renderer")).evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  const locationSpy = spyOnLocationHref(window);
  return { window, conv, locationSpy };
}

// makeForkAppwire records forkThread/startTurn calls and resolves forkThread
// with a child session + the server-authoritative original input text.
function makeForkAppwire(calls, originalInput) {
  return {
    forkThread: (sessionId, body) => {
      calls.push({ name: "forkThread", sessionId, body });
      return Promise.resolve({
        ref: "local:01CHILD",
        session_id: "01CHILD",
        original_input: originalInput,
      });
    },
    startTurn: () => {
      calls.push({ name: "startTurn" });
      return Promise.resolve({});
    },
    refForSession: (s) => "local:" + s,
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    tasks: () => Promise.resolve([]),
    readThread: () => Promise.resolve({}),
  };
}

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

async function settle() { await new Promise((r) => setTimeout(r, 30)); }

(async () => {
  // -----------------------------------------------------------------------
  // 1. User message: fork action renders in the actions row; clicking it
  //    forks with defer_input at the message's transcript entry index, stages
  //    the server-returned original text as the child session's draft, and
  //    navigates to the fork — without any startTurn (no auto-run).
  // -----------------------------------------------------------------------
  {
    const { window, conv, locationSpy } = newHarness("01HOST");
    await settle();

    const calls = [];
    window.SerfAppwire = makeForkAppwire(calls, "hello world");

    window.SerfRenderer.handleData("USER_INPUT", { text: "hello world", turn: 3 });
    await settle();

    const forkBtn = conv.querySelector(".user-message .user-message-actions .action.fork");
    pass(!!forkBtn, "user message should render a fork action");
    pass(forkBtn && forkBtn.tagName === "BUTTON", "fork action must be a <button>, got " + (forkBtn && forkBtn.tagName));
    pass(forkBtn && forkBtn.getAttribute("type") === "button", "fork button must be type=button");
    if (forkBtn) forkBtn.click();
    await settle();

    const forkCalls = calls.filter((c) => c.name === "forkThread");
    pass(forkCalls.length === 1, "expected exactly one forkThread call, got " + forkCalls.length);
    if (forkCalls.length) {
      pass(forkCalls[0].body.turn === 3, "forkThread body.turn should be the message entry index 3, got " + JSON.stringify(forkCalls[0].body));
      pass(forkCalls[0].body.defer_input === true, "forkThread body.defer_input should be true, got " + JSON.stringify(forkCalls[0].body));
      pass(!forkCalls[0].body.edited_message, "forkThread must not send edited_message in the defer flow, got " + JSON.stringify(forkCalls[0].body));
    }
    pass(calls.filter((c) => c.name === "startTurn").length === 0, "fork must not auto-run the message (no startTurn)");
    pass(
      window.localStorage.getItem("serf-hub.draft.01CHILD") === "hello world",
      "child session draft should hold the original message, got " +
        JSON.stringify(window.localStorage.getItem("serf-hub.draft.01CHILD"))
    );
    const nav = locationSpy.navigations[0];
    pass(nav === "/s/" + encodeURIComponent("local:01CHILD"), "expected navigation to the fork child, got " + nav);
  }

  // -----------------------------------------------------------------------
  // 2. Assistant message: the fork action forks at the USER PROMPT that
  //    produced the reply (retry semantics), staging that prompt's text.
  // -----------------------------------------------------------------------
  {
    const { window, conv, locationSpy } = newHarness("01HOST");
    await settle();

    const calls = [];
    window.SerfAppwire = makeForkAppwire(calls, "original prompt");

    window.SerfRenderer.handleData("USER_INPUT", { text: "original prompt", turn: 1 });
    window.SerfRenderer.handleData("ASSISTANT_TEXT_START", {});
    window.SerfRenderer.handleData("ASSISTANT_TEXT_END", { text: "the reply" });
    await settle();

    const forkBtn = conv.querySelector(".assistant-message .msg-fork");
    pass(!!forkBtn, "assistant message should render a fork action");
    if (forkBtn) forkBtn.click();
    await settle();

    const forkCalls = calls.filter((c) => c.name === "forkThread");
    pass(forkCalls.length === 1, "expected exactly one forkThread call, got " + forkCalls.length);
    if (forkCalls.length) {
      pass(forkCalls[0].body.turn === 1, "assistant fork should target the producing user prompt's entry index 1, got " + JSON.stringify(forkCalls[0].body));
      pass(forkCalls[0].body.defer_input === true, "forkThread body.defer_input should be true, got " + JSON.stringify(forkCalls[0].body));
    }
    pass(
      window.localStorage.getItem("serf-hub.draft.01CHILD") === "original prompt",
      "child session draft should hold the producing prompt, got " +
        JSON.stringify(window.localStorage.getItem("serf-hub.draft.01CHILD"))
    );
    const nav = locationSpy.navigations[0];
    pass(nav === "/s/" + encodeURIComponent("local:01CHILD"), "expected navigation to the fork child, got " + nav);
  }

  // -----------------------------------------------------------------------
  // 3. SerfDrafts.writeFor: stores a draft for an arbitrary session id (the
  //    not-yet-visited fork child) and removes it for blank content.
  // -----------------------------------------------------------------------
  {
    const { window } = newHarness("01HOST");
    pass(typeof window.SerfDrafts.writeFor === "function", "SerfDrafts.writeFor must exist");
    if (typeof window.SerfDrafts.writeFor === "function") {
      window.SerfDrafts.writeFor("01OTHER", "staged text");
      pass(
        window.localStorage.getItem("serf-hub.draft.01OTHER") === "staged text",
        "writeFor should store under the target session key, got " +
          JSON.stringify(window.localStorage.getItem("serf-hub.draft.01OTHER"))
      );
      window.SerfDrafts.writeFor("01OTHER", "   ");
      pass(
        window.localStorage.getItem("serf-hub.draft.01OTHER") === null,
        "writeFor with blank text should remove the draft"
      );
    }
  }

  // -----------------------------------------------------------------------
  // 4. Opening the fork child restores the staged draft into the composer,
  //    ready for editing — and does NOT submit it.
  // -----------------------------------------------------------------------
  {
    const { window } = newHarness("01CHILD", { "serf-hub.draft.01CHILD": "original prompt" });
    await settle();
    const ta = window.document.querySelector("form[data-input-form] .message-input");
    pass(ta.value === "original prompt", "child composer should be pre-populated with the original message, got " + JSON.stringify(ta.value));
  }

  // -----------------------------------------------------------------------
  // 5. Local echo + server-echo correction: the fork button must read the
  //    entry index AT CLICK TIME. The optimistic local echo binds an inferred
  //    index; promoteLocalUserMessage later corrects wrap.dataset.entryIdx to
  //    the server-authoritative turn. Clicking after the correction must fork
  //    at the corrected index, never the stale inferred one.
  // -----------------------------------------------------------------------
  {
    const { window, conv } = newHarness("01HOST");
    await settle();

    const calls = [];
    window.SerfAppwire = makeForkAppwire(calls, "follow-up");

    // An existing transcript message, then an optimistic local echo whose
    // entry index is only inferred (entryIndex 3 -> inferred 4).
    window.SerfRenderer.handleData("USER_INPUT", { text: "first", turn: 3 });
    window.SerfRenderer.appendLocalUserMessage("follow-up", [], "tid-9", 1);
    await settle();

    const localWrap = conv.querySelector('.user-message[data-local-echo="true"]');
    pass(!!localWrap, "local echo should render before promotion");
    pass(localWrap && localWrap.dataset.entryIdx === "4", "local echo starts at the inferred index 4, got " + (localWrap && localWrap.dataset.entryIdx));

    // The server echo corrects the entry index to the authoritative turn 7.
    const promoted = window.SerfRenderer.promoteLocalUserMessage({ text: "follow-up", turn: 7, turnId: "tid-9", images: [] });
    pass(promoted === true, "promoteLocalUserMessage should promote the local echo");
    pass(localWrap && localWrap.dataset.entryIdx === "7", "promotion corrects dataset.entryIdx to 7, got " + (localWrap && localWrap.dataset.entryIdx));

    const forkBtn = localWrap && localWrap.querySelector(".user-message-actions .action.fork");
    pass(!!forkBtn, "local echo should render a fork action");
    if (forkBtn) forkBtn.click();
    await settle();

    const forkCalls = calls.filter((c) => c.name === "forkThread");
    pass(forkCalls.length === 1, "expected exactly one forkThread call, got " + forkCalls.length);
    if (forkCalls.length) {
      pass(
        forkCalls[0].body.turn === 7,
        "click after dataset correction must fork at the corrected index 7, not the stale inferred 4, got " + JSON.stringify(forkCalls[0].body)
      );
      pass(forkCalls[0].body.defer_input === true, "forkThread body.defer_input should be true, got " + JSON.stringify(forkCalls[0].body));
    }
  }

  if (failures.length === 0) {
    console.log("PASS: fork-from-message stages the original message in the fork composer's draft");
    process.exit(0);
  }
  for (const f of failures) console.log(f);
  process.exit(1);
})();
