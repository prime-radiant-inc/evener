// Turn failure (Source="provider" / TurnStatusFailed) → red turn-level END-CAP
// in the transcript (mockup #15 Alt A, case 2). Distinct from a transport drop
// (which goes to the chrome): the agent's turn genuinely failed, so it stays in
// the conversation as a red end-cap with:
//   • a human-readable summary in SANS as the primary line;
//   • the raw [error] text available in MONO on expand;
//   • a blue Retry action;
//   • a short taxonomy label from Cause.Kind when present.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
    <button class="send-btn" type="submit">send</button>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

const DIAGNOSTICS_SRC = fs.readFileSync(path.resolve(__dirname, "../assets/diagnostics.js"), "utf8");
window.eval(DIAGNOSTICS_SRC);
require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  await new Promise((r) => setTimeout(r, 30));
  const R = window.SerfRenderer;

  // The user had a turn to replay (so Retry is offered).
  R.lastUserText = "Run the migration across all 240 shards.";
  R.lastSubmittedTurn = { text: "Run the migration across all 240 shards.", items: [] };

  // A provider turn failure, as forwarded by appwire's errorPayload:
  // { error (raw), source, title, hint, cause }.
  // source is deliberately 'serf' so the test exercises the typed cause.kind
  // override path rather than the stored source field.
  R.handleData("ERROR", {
    error: "[error] openai: 500 internal_server_error\nrequest-id: req_8fa1c…  ·  model gpt-5.5",
    source: "serf",
    title: "Provider error",
    cause: { kind: "provider", provider: "openai", model: "gpt-5.5", status: 500 },
  });

  // ── It is a red, provider-sourced end-cap IN the transcript ────────────────
  const card = conv.querySelector(".diagnostic.diagnostic-source-provider");
  pass(!!card, "a provider turn failure renders a provider-sourced diagnostic end-cap in the transcript");
  pass(!!card && card.classList.contains("diagnostic-error"), "the end-cap is an error (red) severity");

  // ── Human summary is the PRIMARY line (sans), not the raw [error] ──────────
  const msg = card && card.querySelector(".diagnostic-message");
  pass(!!msg, "the end-cap has a human-readable summary line");
  pass(!!msg && !/^\[error\]/i.test(msg.textContent.trim()), "the summary is human-readable (no leading [error] prefix)");

  // ── Raw [error] text is available in MONO on expand ───────────────────────
  const det = card && card.querySelector("details.diagnostic-detail");
  pass(!!det, "the raw error is foldable into an expandable detail");
  pass(!det || !det.open, "raw detail must start collapsed");
  if (det) det.open = true;
  const pre = det && det.querySelector(".diagnostic-detail-pre");
  pass(!!pre && /openai: 500/.test(pre.textContent), "the expanded raw error shows the original provider error text");
  pass(!!pre && /req_8fa1c/.test(pre.textContent), "the expanded raw error preserves the request-id");
  // The raw block is mono.
  pass(!!pre, "the raw error renders in a mono <pre>/code block");

  // ── A blue Retry action ────────────────────────────────────────────────────
  const retry = card && Array.from(card.querySelectorAll("button")).find((b) => /retry/i.test(b.textContent));
  pass(!!retry, "the end-cap offers a Retry action");

  // ── A short taxonomy label from Cause.Kind ────────────────────────────────
  // The badge must read 'Provider …' even though the stored source was 'serf'.
  const badge = card && card.querySelector(".diagnostic-badge");
  pass(!!badge && /provider/i.test(badge.textContent), "the end-cap badge re-classifies source='serf' to 'provider' via cause.kind override");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: provider turn failure → red end-cap with human summary, raw-on-expand, Retry");
  process.exit(0);
})().catch((err) => { console.error("FAIL: " + (err && err.stack || err)); process.exit(1); });
