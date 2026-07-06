// Per-turn metadata badge: appwire forwards the full turn object on
// turn/completed, and the renderer stamps data-turn-id on the assistant
// message that closes a turn, then attaches a .turn-meta badge (duration,
// tokens, cost) once the turn/completed notification lands.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── appwire mapping: turn/completed carries the full turn object ────────────
{
  const appwireSrc = fs.readFileSync(require("path").resolve(__dirname, "../assets/appwire.js"), "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only" });
  dom.window.eval(appwireSrc);
  const turn = {
    id: "turn_1",
    status: "completed",
    durationMs: 4200,
    usage: { inputTokens: 100, outputTokens: 50, totalTokens: 150 },
    cost: "~$0.01",
  };
  const out = dom.window.SerfAppwire.eventsFromNotification("turn/completed", { turnId: "turn_1", turn });
  const completed = out.find(([kind]) => kind === "TURN_COMPLETED");
  pass(!!completed, "turn/completed notification should map to a TURN_COMPLETED event, got " + JSON.stringify(out));
  pass(!!completed && completed[1].turnId === "turn_1", "TURN_COMPLETED event should carry the turnId");
  pass(!!completed && completed[1].turn === turn, "TURN_COMPLETED event should carry the full turn object (not just turnId)");
}

// ── renderer: stamps data-turn-id and attaches the .turn-meta badge ─────────
{
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  const send = (name, data) => window.SerfRenderer.handleData(name, data);

  (async () => {
    await new Promise((r) => setTimeout(r, 20));

    // A turn starts, the assistant streams a reply, then turn/completed lands
    // carrying the turn's usage/cost/duration.
    send("TURN_STARTED", { turnId: "turn_1" });
    send("ASSISTANT_TEXT_START", {});
    send("ASSISTANT_TEXT_DELTA", { delta: "the answer" });
    send("ASSISTANT_TEXT_END", { text: "the answer" });

    const msg = conv.querySelector(".assistant-message");
    pass(!!msg, "assistant message should render");
    pass(!!msg && msg.dataset.turnId === "turn_1", "assistant message that closes the turn should be stamped with data-turn-id, got " + (msg && msg.dataset.turnId));

    send("TURN_COMPLETED", {
      turnId: "turn_1",
      turn: {
        id: "turn_1",
        status: "completed",
        durationMs: 4200,
        usage: { inputTokens: 100, outputTokens: 50, totalTokens: 150 },
        cost: "~$0.01",
      },
    });

    const meta = msg && msg.querySelector(".turn-meta");
    pass(!!meta, "assistant message should gain a .turn-meta badge after turn/completed");
    const text = meta ? meta.textContent : "";
    // formatToolDuration(4200) === "4.2s" — reuse the existing duration-format
    // convention (see renderer-format.js) rather than inventing new text.
    pass(/4\.2s/.test(text), "badge text should include the formatted duration, got: " + text);
    pass(/↑100 ↓50/.test(text), "badge text should include the token counts, got: " + text);
    pass(/~\$0\.01/.test(text), "badge text should include the cost, got: " + text);

    const costEl = meta && meta.querySelector(".cost");
    pass(!!costEl, "badge should contain a nested .cost child span (a later phase's CSS gating depends on it)");
    pass(!!costEl && costEl.textContent === "~$0.01", "the .cost child span should hold the cost text, got: " + (costEl && costEl.textContent));

    // Show-cost off: the CSS gate only hides the .cost span itself, so the
    // separator and the tooltip must be built to not leak the cost figure.
    window.document.body.dataset.showCost = "false";
    send("TURN_STARTED", { turnId: "turn_2" });
    send("ASSISTANT_TEXT_START", {});
    send("ASSISTANT_TEXT_DELTA", { delta: "another answer" });
    send("ASSISTANT_TEXT_END", { text: "another answer" });
    send("TURN_COMPLETED", {
      turnId: "turn_2",
      turn: {
        id: "turn_2",
        status: "completed",
        durationMs: 4200,
        usage: { inputTokens: 100, outputTokens: 50, totalTokens: 150 },
        cost: "~$0.01",
      },
    });

    const msgs = conv.querySelectorAll(".assistant-message");
    const msg2 = msgs[msgs.length - 1];
    const meta2 = msg2 && msg2.querySelector(".turn-meta");
    pass(!!meta2, "second assistant message should gain a .turn-meta badge after turn/completed");
    pass(!!meta2 && !meta2.title.includes("$"), "badge title should not leak the cost figure when Show-cost is off, got: " + (meta2 && meta2.title));
    const costEl2 = meta2 && meta2.querySelector(".cost");
    pass(!!costEl2, "badge should still contain a .cost child span when Show-cost is off (CSS still gates its display)");
    // jsdom doesn't apply the CSS display:none gate, so textContent alone
    // can't reveal a dangling separator — check the DOM structure directly:
    // no "· " text node should precede .cost when cost is hidden.
    const prevNode = costEl2 && costEl2.previousSibling;
    pass(
      !(prevNode && prevNode.nodeType === 3 && prevNode.textContent === " · "),
      "no leading '· ' separator text node should precede .cost when Show-cost is off, got prev node text: " + JSON.stringify(prevNode && prevNode.textContent)
    );

    if (failures.length) {
      for (const f of failures) console.error(f);
      process.exit(1);
    }
    console.log("PASS: per-turn duration/tokens/cost badge attaches on turn/completed");
    // init() starts a recurring task-badge poll (setInterval) that keeps the
    // event loop alive; exit explicitly on success like the sibling tests.
    process.exit(0);
  })();
}
