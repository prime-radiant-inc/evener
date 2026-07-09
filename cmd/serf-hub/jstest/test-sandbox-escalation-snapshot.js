// M7 surface-on-entry snapshot (web): a client ENTERING a session whose thread/read
// carries thread.serf.pendingEscalations surfaces the card(s) on hydration, de-duped
// against the live SANDBOX_ESCALATION_REQUESTED notification (no double-render).
const assert = require("assert");
const { JSDOM } = require("jsdom");

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01S"></header>
    <div id="conversation" data-session-id="01S" data-state="active"></div>
    <form data-input-form data-session-id="01S"><textarea class="message-input"></textarea></form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/s/01S",
  });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

  let notify = null;
  window.SerfAppwire = {
    refForSession: (id) => "local:" + id,
    onNotification: (cb) => { notify = cb; return () => {}; },
    onConnectionLost: () => () => {},
    // The entry read carries a pending escalation snapshot for this session.
    readThread: () => Promise.resolve({
      thread: {
        id: "01S", sessionId: "01S", serf: {
          ref: "local:01S",
          pendingEscalations: [
            { escalationId: "esc_snap", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/snapshot/path" },
          ],
        },
        status: { type: "active" }, queue: { depth: 0, preview: [] }, turns: [],
      },
    }),
    eventsFromThread: () => [],
    eventsFromNotification: (method, params) => {
      if (method === "serf/sandbox/escalation/requested") return [["SANDBOX_ESCALATION_REQUESTED", params]];
      return [];
    },
    activeTurnIDFromThread: () => "",
    tasks: () => Promise.resolve([]),
    resolveSandboxEscalation: () => Promise.resolve(),
  };

  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  // Let the readThread hydration resolve.
  await new Promise((r) => setTimeout(r, 40));

  // (1) The snapshot escalation surfaced as a card on entry.
  let cards = window.document.querySelectorAll(".sandbox-escalation");
  assert.strictEqual(cards.length, 1, "entering a session must surface its snapshot escalation, got " + cards.length);
  assert.ok(cards[0].textContent.includes("/snapshot/path"), "snapshot card must show the path");
  assert.ok(/requested by serf/i.test(cards[0].textContent), "snapshot card must be harness-framed");

  // (2) A LIVE notification for the SAME id must NOT double-render (de-dupe).
  assert.ok(notify, "renderer subscribed");
  notify("serf/sandbox/escalation/requested", { escalationId: "esc_snap", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/snapshot/path" });
  cards = window.document.querySelectorAll(".sandbox-escalation");
  assert.strictEqual(cards.length, 1, "a live notification for an already-snapshotted id must not double-render, got " + cards.length);

  // (3) A live notification for a NEW id DOES render a second card.
  notify("serf/sandbox/escalation/requested", { escalationId: "esc_live", mode: "read-only", tool: "write_file", kind: "file_tool", deniedPath: "/live/path" });
  cards = window.document.querySelectorAll(".sandbox-escalation");
  assert.strictEqual(cards.length, 2, "a new live escalation must render its own card, got " + cards.length);

  // (4) RECONNECT re-hydration: a reset (which wipes the DOM, destroying the
  // rendered cards) followed by re-hydration with the still-pending snapshot must
  // RE-RENDER the card. This is the HIGH regression — reset must clear the de-dupe
  // set, else appendSandboxEscalation early-returns and the card vanishes until a
  // full page reload while the daemon stays blocked.
  window.SerfRenderer.resetTranscriptReplay();
  assert.strictEqual(window.document.querySelectorAll(".sandbox-escalation").length, 0, "reset must wipe the rendered cards");
  window.SerfRenderer.surfaceSnapshotEscalations({
    serf: { pendingEscalations: [
      { escalationId: "esc_snap", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/snapshot/path" },
    ] },
  });
  cards = window.document.querySelectorAll(".sandbox-escalation");
  assert.strictEqual(cards.length, 1, "a still-pending escalation must RE-RENDER after a reconnect reset+re-hydrate, got " + cards.length);
  assert.ok(cards[0].textContent.includes("/snapshot/path"), "the re-rendered card must show the path");

  // (5) SETTLED half of the invariant: once the escalation is resolved it is absent
  // from the fresh snapshot, so a reset + re-hydrate must render NO card for it — a
  // settled escalation never reappears.
  window.SerfRenderer.resetTranscriptReplay();
  window.SerfRenderer.surfaceSnapshotEscalations({ serf: { pendingEscalations: [] } });
  cards = window.document.querySelectorAll(".sandbox-escalation");
  assert.strictEqual(cards.length, 0, "a settled escalation (absent from the snapshot) must NOT reappear on re-hydrate, got " + cards.length);

  console.log("PASS test-sandbox-escalation-snapshot.js");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
