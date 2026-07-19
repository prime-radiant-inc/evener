// Lazy transcript loading: when the reader nears the top and older turns
// remain, the renderer pages them in via listTurns and prepends them ABOVE the
// live content without disturbing the in-progress state at the bottom. Verifies
// DOM order, cursor advance, the overlap guard, and that live replay state is
// preserved across the detached older-turn render.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"><span class="status-dot" data-state="active"></span></header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div data-composer-surface>
      <textarea class="message-input"></textarea>
      <button type="submit" class="btn btn-primary send-btn">send</button>
    </div>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));

  // Render a piece of "live" content at the bottom.
  R.handleData("USER_INPUT", { text: "LIVE-USER-MESSAGE", turn: 9 });

  // A sentinel of live replay state that the older-turn render must not clobber.
  R.currentMessageId = "live-msg-id";
  R.lastUserText = "LIVE-USER-MESSAGE";

  // Stub appwire: listTurns returns one older page, eventsFromTurns maps each
  // turn to a USER_INPUT event.
  let listCalls = 0;
  let lastCursorSeen = null;
  window.SerfAppwire = {
    listTurns: (sessionId, cursor, limit) => {
      listCalls++;
      lastCursorSeen = cursor;
      return Promise.resolve({ turns: [{ text: "OLD-USER-MESSAGE", n: 1 }], nextCursor: "" });
    },
    eventsFromTurns: (turns) => turns.map((t) => ["USER_INPUT", { text: t.text, turn: t.n }]),
  };
  R.sessionId = "01TEST";

  // No cursor → no fetch.
  R.olderTurnsCursor = "";
  R.maybeLoadOlderTurns();
  pass(listCalls === 0, "no fetch when there is no older cursor");

  // Cursor set → fetch + prepend.
  // Backdate lastFrameAt to simulate a stalled session; prependOlderTurns must
  // not clobber it (the liveness clock belongs to the live session, not the
  // detached older-turn render).
  const testStartAt = Date.now();
  R.lastFrameAt = testStartAt - 300000;
  R.olderTurnsCursor = "cursor-5";
  R.maybeLoadOlderTurns();
  // Overlap guard: a second near-top scroll while loading must not double-fetch.
  R.maybeLoadOlderTurns();
  await new Promise((r) => setTimeout(r, 20));

  pass(listCalls === 1, "exactly one fetch despite overlapping triggers (got " + listCalls + ")");
  pass(lastCursorSeen === "cursor-5", "fetch used the older cursor");

  const text = conv.textContent;
  const oldAt = text.indexOf("OLD-USER-MESSAGE");
  const liveAt = text.indexOf("LIVE-USER-MESSAGE");
  pass(oldAt >= 0, "older turn rendered into the transcript");
  pass(liveAt >= 0, "live content still present");
  pass(oldAt >= 0 && liveAt >= 0 && oldAt < liveAt, "older turn is prepended ABOVE the live content");

  pass(R.olderTurnsCursor === "", "cursor advances to the reply's nextCursor (head reached)");
  pass(R.loadingOlderTurns === false, "loading flag cleared after the fetch");
  pass(R.lastFrameAt <= testStartAt, "prependOlderTurns does not clobber lastFrameAt (liveness clock preserved across detached older-turn render, got " + R.lastFrameAt + " expected <= " + testStartAt + ")");
  pass(R.currentMessageId === "live-msg-id", "live currentMessageId preserved across older render");
  pass(R.lastUserText === "LIVE-USER-MESSAGE", "live lastUserText preserved across older render");

  // Head reached → no further fetch.
  R.maybeLoadOlderTurns();
  pass(listCalls === 1, "no fetch once the head is reached");

  // ── F1(a): a prepend must not corrupt the LIVE turn's streaming state ──
  // A historical (already-completed) turn's events run through the normal
  // mutating path during the staging render; a historical TURN_COMPLETED with
  // no turnId matches the live finalization guard and clears the live
  // activeTurnId — the live turn's own completion then fails the guard and the
  // message streams forever.
  R.handleData("TURN_STARTED", { turnId: "live-t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "live answer streaming" });
  const liveMsg = conv.querySelector('.assistant-message[data-turn-id="live-t1"]');
  pass(!!liveMsg, "live streaming message exists");
  window.SerfAppwire.eventsFromTurns = () => [
    ["USER_INPUT", { text: "HIST-USER", turn: 2 }],
    ["ASSISTANT_TEXT_START", {}],
    ["ASSISTANT_TEXT_END", { text: "historical answer" }],
    ["TURN_COMPLETED", { turn: { id: "hist-t0", durationMs: 100 } }],
  ];
  R.prependOlderTurns([{ id: "hist-t0" }]);
  pass(R.activeTurnId === "live-t1", "live activeTurnId intact after prepend (got " + JSON.stringify(R.activeTurnId) + ")");
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: " continues" });
  R.handleData("TURN_COMPLETED", { turnId: "live-t1", turn: { id: "live-t1", durationMs: 200 } });
  pass(liveMsg.textContent.includes("live answer streaming continues"), "live message kept streaming across the prepend");
  pass(!liveMsg.classList.contains("streaming"), "live TURN_COMPLETED still finalizes the live message");

  // ── F1(b): a pending live ask_user survives a historical USER_INPUT ──
  // USER_INPUT resolves the whole pending ask set (spec §5.2) — a HISTORICAL
  // user message must not settle the LIVE pending ask or tear down its anchor.
  R.handleData("TURN_STARTED", { turnId: "live-t2" });
  R.handleData("TOOL_CALL_START", { call_id: "ask1", tool_name: "ask_user", arguments_json: JSON.stringify({ questions: [{ header: "Pick", question: "Pick one", options: [{ label: "A" }] }] }) });
  R.handleData("TOOL_CALL_END", { call_id: "ask1", tool_name: "ask_user", output: "ok" });
  const liveAnchor = conv.querySelector("[data-ask-anchor]");
  pass(!!R.pendingAsk && !!liveAnchor, "live ask_user pending with a transcript anchor");
  pass(R.agentQuestionEl === liveAnchor, "anchor recorded as the agent question");
  window.SerfAppwire.eventsFromTurns = () => [["USER_INPUT", { text: "HIST-USER-2", turn: 3 }]];
  R.prependOlderTurns([{ id: "hist-t1" }]);
  pass(!!R.pendingAsk, "ask still pending after a historical USER_INPUT prepend");
  pass(R.pendingAsk && R.pendingAsk.el === liveAnchor, "pending ask keeps its anchor element");
  pass(liveAnchor.isConnected && conv.contains(liveAnchor), "ask anchor still in the live transcript");
  pass(!conv.querySelector(".ask-settled-line"), "no settled line rendered for the live ask");
  pass(R.agentQuestionEl === liveAnchor, "agent question element intact after prepend");

  // ── F1(c): live reasoning survives a historical reasoning prepend ──
  // A historical REASONING_START early-returns on the live reasoningEl, then
  // the historical REASONING_DELTA hijacks the live think body and buffer.
  R.handleData("TURN_STARTED", { turnId: "live-t3" });
  R.handleData("REASONING_START", {});
  R.handleData("REASONING_DELTA", { delta: "live thought" });
  const liveThink = R.reasoningEl;
  pass(!!liveThink, "live reasoning block streaming");
  const liveThinkBody = liveThink.querySelector(".think-body");
  window.SerfAppwire.eventsFromTurns = () => [
    ["REASONING_START", {}],
    ["REASONING_DELTA", { delta: "HIST-THOUGHT" }],
    ["ASSISTANT_TEXT_START", {}],
    ["ASSISTANT_TEXT_END", { text: "hist answer 2" }],
  ];
  R.prependOlderTurns([{ id: "hist-t2" }]);
  pass(R.reasoningEl === liveThink, "live reasoningEl intact after prepend");
  pass(R.reasoningBuf === "live thought", "live reasoningBuf intact (got " + JSON.stringify(R.reasoningBuf) + ")");
  pass(!liveThinkBody.textContent.includes("HIST-THOUGHT"), "historical reasoning did not hijack the live think body");
  R.handleData("REASONING_DELTA", { delta: " continues" });
  pass(liveThinkBody.textContent === "live thought continues", "later live reasoning deltas still render into the live think body");

  // ── F1(d): a historical ask_user in a prepended page must not hijack the
  // LIVE composer. The ask path (handleAskUserAck → appendPendingAskQuestions
  // → renderPendingAskDock / setComposerAskMode, markAgentQuestion →
  // renderNeedsYouDock) mutates the REAL composer form — ask mode on, input
  // hidden+inert, focus stolen into the dock. Field save/restore cannot undo
  // those document-level side effects; the staging replay must suppress them.
  const form = window.document.querySelector("form[data-input-form]");
  const input = form.querySelector(".message-input");
  // Settle the live ask from F1(b) so the composer is back to normal.
  R.handleData("USER_INPUT", { text: "the live answer", turn: 10 });
  pass(!R.pendingAsk, "live ask settled, composer back to normal");
  pass(form.dataset.responseMode !== "ask", "composer in normal mode before the historical ask prepend");
  pass(!input.hidden, "input visible before the historical ask prepend");
  window.SerfAppwire.eventsFromTurns = () => [
    ["TOOL_CALL_START", { call_id: "hask1", tool_name: "ask_user", arguments_json: JSON.stringify({ questions: [{ header: "Hist", question: "Historical?", options: [{ label: "X" }, { label: "Y" }] }] }) }],
    ["TOOL_CALL_END", { call_id: "hask1", tool_name: "ask_user", output: "ok" }],
  ];
  input.focus();
  pass(window.document.activeElement === input, "composer input focused before the historical ask prepend");
  R.prependOlderTurns([{ id: "hist-t3" }]);
  pass(form.dataset.responseMode !== "ask", "historical ask_user does NOT switch the live composer into ask mode");
  pass(!input.hidden, "historical ask_user does NOT hide the live composer input");
  pass(!form.querySelector("[data-ask-response-dock]"), "historical ask_user does NOT build a response dock in the live composer");
  pass(window.document.activeElement === input, "historical ask_user does NOT steal focus from the live composer");
  const nyDock = form.parentNode.querySelector("[data-needs-you-dock]");
  pass(!nyDock || nyDock.hidden, "historical ask_user does NOT light the live needs-you dock");
  pass(R.stagingReplay === false, "stagingReplay cleared after the prepend finally");
  // The historical question still renders into the prepended history itself —
  // only the composer side effects are suppressed.
  pass(!!conv.querySelector("[data-ask-anchor]"), "historical ask anchor still renders into the prepended history");
  // After the restore, the LIVE ask flow must still work end to end.
  R.handleData("TURN_STARTED", { turnId: "live-t4" });
  R.handleData("TOOL_CALL_START", { call_id: "ask2", tool_name: "ask_user", arguments_json: JSON.stringify({ questions: [{ header: "Live", question: "Live?", options: [{ label: "L" }] }] }) });
  R.handleData("TOOL_CALL_END", { call_id: "ask2", tool_name: "ask_user", output: "ok" });
  pass(form.dataset.responseMode === "ask", "live ask flow still activates composer ask mode after a staging replay");
  pass(!!form.querySelector("[data-ask-response-dock]"), "live ask dock still renders after a staging replay");

  // ── F2: a scroll-triggered prepend must not hide the live new-content pill.
  // The staging render runs the normal per-event settle: the detached staging
  // div measures "near bottom", so settleFrame → scrollToBottom →
  // clearNewContentPill zeroes the pill fields. The saved set must cover the
  // WHOLE pill field set — painted count, jump target, and the debounce timer
  // — not just the raw count.
  R.newContentCount = 3;
  R.newContentPaintedCount = 3;
  const jumpTarget = conv.querySelector(".assistant-message");
  R.newContentJumpTarget = jumpTarget;
  R.newContentCountTimer = setTimeout(() => {}, 60000);
  window.SerfAppwire.eventsFromTurns = () => [["USER_INPUT", { text: "HIST-USER-3", turn: 4 }]];
  R.prependOlderTurns([{ id: "hist-t9" }]);
  pass(R.newContentCount === 3, "pill count preserved across prepend (got " + R.newContentCount + ")");
  pass(R.newContentPaintedCount === 3, "pill painted count preserved across prepend (got " + R.newContentPaintedCount + ")");
  pass(R.newContentJumpTarget === jumpTarget, "pill jump target preserved across prepend");
  pass(R.newContentCountTimer !== null, "pill debounce timer survives prepend");
  clearTimeout(R.newContentCountTimer);
  R.newContentCountTimer = null;

  if (failures.length) { failures.forEach((f) => console.log(f)); process.exit(1); }
  console.log("PASS: renderer lazily prepends older turns without disturbing live state");
  process.exit(0);
})();
