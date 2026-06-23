// Context compaction as a visible, inspectable lifecycle event (mockup #17
// Alt A). The projector carries the structured before/after numbers under
// item.raw.compaction; the web must:
//   • appwire: forward item.raw onto the SYSTEM_MESSAGE payload so the
//     structured numbers reach the renderer (not only the prose text);
//   • renderer: draw the compaction event as a quiet neutral one-liner that
//     EXPANDS to show the REAL before/after math (EstTokensBefore→After,
//     TurnsBefore→After, Layer) from those numbers — never a silent rug-pull.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <main id="workspace">
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form class="workspace-input" data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit">send</button>
    </form>
  </main>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
window.eval(appwireSrc);
require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── appwire forwards the structured raw onto SYSTEM_MESSAGE ──────────────────
const rawObj = {
  compaction: {
    layer: "L4",
    turns_before: 42,
    turns_after: 8,
    est_tokens_before: 120000,
    est_tokens_after: 23000,
  },
};
const evs = window.SerfAppwire.eventsFromNotification("item/completed", {
  item: {
    type: "systemMessage",
    id: "c1",
    description: "Context compaction",
    text: "Layer: L4\nTurns: 42 -> 8\nEstimated tokens: 120000 -> 23000",
    status: "completed",
    raw: rawObj,
  },
});
const sys = evs.find((e) => e[0] === "SYSTEM_MESSAGE");
pass(!!sys, "compaction item maps to a SYSTEM_MESSAGE event (got " + JSON.stringify(evs) + ")");
pass(!!sys && sys[1] && sys[1].raw && sys[1].raw.compaction,
  "SYSTEM_MESSAGE carries item.raw.compaction (got " + JSON.stringify(sys && sys[1]) + ")");

// A normal systemMessage with no raw must not invent a raw payload.
const plainEvs = window.SerfAppwire.eventsFromNotification("item/completed", {
  item: { type: "systemMessage", id: "s2", description: "Skill activated", text: "Activated skill: x", status: "completed" },
});
const plainSys = plainEvs.find((e) => e[0] === "SYSTEM_MESSAGE");
pass(!!plainSys && !plainSys[1].raw, "an ordinary systemMessage carries no raw (got " + JSON.stringify(plainSys && plainSys[1]) + ")");

async function waitForRendererReady(renderer) {
  for (let i = 0; i < 100; i++) {
    if (renderer.descriptionsReady && !renderer.eventBuffer) return;
    await new Promise((r) => setTimeout(r, 5));
  }
}

(async () => {
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  const R = window.SerfRenderer;
  await waitForRendererReady(R); // flush the cold-load buffer deterministically

  // ── renderer: compaction is a quiet, expandable lifecycle line ─────────────
  R.handleData("SYSTEM_MESSAGE", {
    title: "Context compaction",
    text: "Layer: L4\nTurns: 42 -> 8\nEstimated tokens: 120000 -> 23000",
    raw: rawObj,
  });

  const line = conv.querySelector(".context-compaction-line");
  pass(!!line, "compaction renders as a dedicated .context-compaction-line");
  // It must be a disclosure that can EXPAND (a <details> or an open-toggle).
  const expandable = line && (line.tagName.toLowerCase() === "details" || line.querySelector("details, [data-compaction-toggle]"));
  pass(!!expandable, "the compaction line is expandable (a disclosure)");

  // The collapsed summary is quiet, reads as a done event, and is glyph-paired.
  pass(!!line && /compact/i.test(line.textContent), "summary reads as a compaction (done) event");

  // Expand and assert the REAL before/after numbers are shown (token + turn math).
  const det = line && (line.tagName.toLowerCase() === "details" ? line : line.querySelector("details"));
  if (det) det.open = true;
  const body = line && line.textContent;
  pass(!!body && /42/.test(body) && /8/.test(body), "expanded body shows turns before(42) -> after(8)");
  pass(!!body && /120/.test(body) && /23/.test(body), "expanded body shows tokens before(120k) -> after(23k)");
  pass(!!body && /L4/.test(body), "expanded body shows the compaction layer");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: context compaction is an inspectable before/after lifecycle event");
  process.exit(0);
})();
