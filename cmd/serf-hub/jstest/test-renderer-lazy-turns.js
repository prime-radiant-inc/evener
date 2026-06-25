// Lazy transcript loading: when the reader nears the top and older turns
// remain, the renderer pages them in via listTurns and prepends them ABOVE the
// live content without disturbing the in-progress state at the bottom. Verifies
// DOM order, cursor advance, the overlap guard, and that live replay state is
// preserved across the detached older-turn render.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"><span class="status-dot" data-state="active"></span></header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
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
  pass(R.currentMessageId === "live-msg-id", "live currentMessageId preserved across older render");
  pass(R.lastUserText === "LIVE-USER-MESSAGE", "live lastUserText preserved across older render");

  // Head reached → no further fetch.
  R.maybeLoadOlderTurns();
  pass(listCalls === 1, "no fetch once the head is reached");

  if (failures.length) { failures.forEach((f) => console.log(f)); process.exit(1); }
  console.log("PASS: renderer lazily prepends older turns without disturbing live state");
  process.exit(0);
})();
