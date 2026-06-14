// Verifies the retry-after-partial reset path on the web renderer:
//   - the appwire layer maps item/agentMessage/reset → ASSISTANT_TEXT_RESET
//   - the renderer discards the in-progress assistant message on reset, so a
//     retried model call's output replaces rather than appends to the partial.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── appwire mapping: item/agentMessage/reset → ASSISTANT_TEXT_RESET ──────────
{
  const appwireSrc = fs.readFileSync("../assets/appwire.js", "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only" });
  dom.window.eval(appwireSrc);
  const out = dom.window.SerfAppwire.eventsFromNotification("item/agentMessage/reset", { itemId: "assistant_1" });
  pass(out.length === 1 && out[0][0] === "ASSISTANT_TEXT_RESET", "reset notification should map to one ASSISTANT_TEXT_RESET event, got " + JSON.stringify(out));
  pass(out[0] && out[0][1] && out[0][1].itemId === "assistant_1", "reset event should carry the itemId");
}

// ── renderer: ASSISTANT_TEXT_RESET discards the in-progress message ──────────
{
  const rendererSrc = fs.readFileSync("../assets/renderer.js", "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  window.eval(rendererSrc);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  const send = (name, data) => window.SerfRenderer.handleData(name, data);

  (async () => {
    await new Promise(r => setTimeout(r, 20));

    // Attempt 1 streams a partial reply, then the stream fails and retries.
    send("ASSISTANT_TEXT_START", {});
    send("ASSISTANT_TEXT_DELTA", { delta: "partial answe" });
    pass(conv.querySelectorAll(".assistant-message").length === 1, "partial stream should render an in-progress assistant message");

    send("ASSISTANT_TEXT_RESET", { itemId: "assistant_1" });
    pass(conv.querySelectorAll(".assistant-message").length === 0, "reset should discard the in-progress assistant message");

    // Attempt 2 streams the full reply to completion.
    send("ASSISTANT_TEXT_START", {});
    send("ASSISTANT_TEXT_DELTA", { delta: "final answer" });
    send("ASSISTANT_TEXT_END", { text: "final answer" });

    const assistants = conv.querySelectorAll(".assistant-message");
    pass(assistants.length === 1, "expected exactly one assistant message after retry, got " + assistants.length);
    pass(assistants.length === 1 && assistants[0].textContent === "final answer", "retry output should replace the partial, got: " + (assistants[0] && assistants[0].textContent));

    if (failures.length) {
      for (const f of failures) console.error(f);
      process.exit(1);
    }
    console.log("PASS: renderer assistant-text reset (retry-after-partial)");
  })();
}
