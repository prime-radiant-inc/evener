// Stale-overlap guard: two overlapping readThread completions must not wedge
// hydration. A superseded hydration H1 (abandoned by a reconnect that started
// H2) resolving AFTER H2 completed must return before ANY state/DOM mutation —
// otherwise it resets H2's valid transcript, replays into it, and the
// mid-chunk staleness abort leaves hydrationInProgress stuck true forever
// (which then no-ops refreshTranscriptStatusVisibility / maybeLoadOlderTurns).
// The stale .catch path must likewise not clear the NEW stream's state.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <div id="input-status" class="input-status">
    <span class="status-item liveness-inline" data-liveness hidden></span>
  </div>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (text) => text };
window.fetch = () => Promise.resolve({
  ok: true,
  json: () => Promise.resolve([]),
  text: () => Promise.resolve(""),
});

// Deferred readThread queue: each connectAppwire call captures one deferral.
const deferrals = [];
window.SerfAppwire = {
  tasks: () => new Promise(() => {}),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: () => () => {},
  onConnectionLost: () => () => {},
  readThread: () => new Promise((resolve, reject) => deferrals.push({ resolve, reject })),
  eventsFromThread: (thread) => thread.testEvents || [],
  eventsFromNotification: () => [],
};

require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
const conversation = window.document.getElementById("conversation");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const userEvents = (prefix, count) => {
  const events = [];
  for (let i = 0; i < count; i++) events.push(["USER_INPUT", { text: prefix + i }]);
  return events;
};
const threadWith = (sessionId, events) => ({
  thread: {
    id: sessionId,
    sessionId,
    status: "inProgress",
    serf: { ref: "local:" + sessionId, activeTurnId: "turn_active" },
    turns: [],
    testEvents: events,
  },
});
const userMessageTexts = (el) =>
  Array.from(el.querySelectorAll(".user-message"))
    .map((m) => { const t = m.querySelector(".user-message-text"); return (t || m).textContent; });
const waitFor = async (cond, timeoutMs) => {
  const deadline = Date.now() + (timeoutMs || 5000);
  while (!cond()) {
    if (Date.now() > deadline) return false;
    await new Promise((r) => setTimeout(r, 5));
  }
  return true;
};
const pump = async (ms) => new Promise((r) => setTimeout(r, ms || 30));

(async () => {
  // ── (a) stale .then after the newer hydration completed ─────────────────
  // H1 starts (300 events — enough to cross a chunk boundary so the abandoned
  // replay would suspend mid-chunk), then a reconnect starts H2 (3 events).
  R.init(conversation);
  pass(deferrals.length === 1, "first connect issued one readThread");
  R.connectAppwire(); // reconnect: H1's stream is superseded
  pass(deferrals.length === 2, "reconnect issued a second readThread");
  const h1 = deferrals[0];
  const h2 = deferrals[1];
  const streamAfterH2Start = R.liveStream;

  // H2 (newer) resolves and completes fully.
  h2.resolve(threadWith("01TEST", userEvents("h2-event-", 3)));
  pass(await waitFor(() => R.appwireHydrated === true, 30000), "H2 hydration completed");
  pass(R.hydrationInProgress === false, "hydrationInProgress cleared after H2");
  pass(userMessageTexts(conversation).length === 3, "transcript holds exactly H2's events");

  // Now the STALE H1 resolves. Without the entry guard it would reset H2's
  // transcript, replay 150 events, then abort mid-chunk on the LATER liveStream
  // check — leaving hydrationInProgress stuck true forever.
  h1.resolve(threadWith("01TEST", userEvents("h1-event-", 300)));
  await pump(60); // let any (buggy) chunked replay run several slices
  pass(R.hydrationInProgress === false,
    "stale H1 completion did not wedge hydrationInProgress (would no-op status refresh + older-turns paging forever)");
  pass(R.appwireHydrated === true, "stale H1 completion did not clear appwireHydrated");
  pass(R.liveStream === streamAfterH2Start, "stale H1 completion did not touch the live stream");
  const textsAfterStale = userMessageTexts(conversation);
  pass(textsAfterStale.length === 3,
    "stale H1 did not reset/re-render the transcript (got " + textsAfterStale.length + " messages)");
  pass(textsAfterStale.every((t, i) => t === "h2-event-" + i),
    "transcript is still exactly H2's events");

  // ── (b) stale .catch must not clear the new stream's state ───────────────
  R.connectAppwire(); // H3
  R.connectAppwire(); // H4 supersedes H3
  pass(deferrals.length === 4, "two more readThreads issued");
  const h3 = deferrals[2];
  const h4 = deferrals[3];
  h4.resolve(threadWith("01TEST", userEvents("h4-event-", 2)));
  pass(await waitFor(() => R.appwireHydrated === true && userMessageTexts(conversation).length === 2, 30000),
    "H4 hydration completed");
  const streamAfterH4 = R.liveStream;
  pass(!!streamAfterH4, "live stream present after H4");

  h3.reject(new Error("stale transport failure"));
  await pump(60);
  pass(R.liveStream === streamAfterH4,
    "stale H3 rejection did NOT clear the new stream (clearAppwireStream would kill live delivery)");
  pass(R.appwireHydrated === true, "stale H3 rejection left appwireHydrated true");
  pass(R.hydrationInProgress === false, "stale H3 rejection left hydrationInProgress false");
  pass(!window.document.getElementById("connection-banner"),
    "stale H3 rejection raised no connection banner over a healthy stream");
  pass(userMessageTexts(conversation).every((t, i) => t === "h4-event-" + i),
    "transcript is still exactly H4's events after the stale rejection");

  // ── (c) a stale H1 aborting mid-replay must not clobber H2's settle suppression ──
  // H1 begins its chunked replay; H2 starts at H1's first yield (superseding
  // the stream) and suspends mid-replay itself; H1 then resumes, detects its
  // stale token, and returns. H1's finally must clear suppressScrollSettle ONLY
  // while H1 still owns the stream — clearing it under H2's mid-replay would
  // re-enable the per-event settle work (the O(N²) path) and replay-time
  // scroll side effects for H2's remaining chunks.
  // Instrument suppressScrollSettle so a true→false transition landing while
  // H2 owns the replay (hydrationInProgress true, outside a transcript reset)
  // is caught regardless of how the chunk timers interleave.
  let sssValue = R.suppressScrollSettle;
  let staleClobber = false;
  const origReset = R.resetTranscriptReplay.bind(R);
  let inReset = false;
  R.resetTranscriptReplay = (...args) => {
    inReset = true;
    try { return origReset(...args); } finally { inReset = false; }
  };
  Object.defineProperty(R, "suppressScrollSettle", {
    configurable: true,
    get() { return sssValue; },
    set(v) {
      if (v === false && sssValue === true && R.hydrationInProgress === true && !inReset) {
        staleClobber = true;
      }
      sssValue = v;
    },
  });

  R.connectAppwire(); // H1 (init is idempotent per element — reconnect path starts it)
  pass(deferrals.length === 5, "(c) connect issued a fresh readThread");
  const hc1 = deferrals[4];
  hc1.resolve(threadWith("01TEST", userEvents("hc1-event-", 300))); // 2 chunks → yields once
  await Promise.resolve(); // H1's .then ran its first slice and suspended at the yield
  pass(R.hydrationInProgress === true && R.suppressScrollSettle === true,
    "(c) H1 mid-replay holds hydrationInProgress and suppressScrollSettle");

  R.connectAppwire(); // H2 supersedes H1's stream at H1's first yield
  pass(deferrals.length === 6, "(c) reconnect issued H2's readThread");
  const hc2 = deferrals[5];
  const streamH2 = R.liveStream;
  hc2.resolve(threadWith("01TEST", userEvents("hc2-event-", 600))); // 4 chunks → H2 yields too
  await Promise.resolve(); // H2 ran its first slice and suspended mid-replay
  pass(R.hydrationInProgress === true && R.suppressScrollSettle === true,
    "(c) H2 mid-replay holds both flags");

  // Let every pending chunk timer fire: H1 resumes first, detects the stale
  // token, and returns — its finally runs while H2 is still mid-replay.
  pass(await waitFor(() => R.appwireHydrated === true && userMessageTexts(conversation).length === 600, 30000),
    "(c) H2 completed its replay after H1's stale abort");
  pass(!staleClobber,
    "(c) H1's stale finally did NOT clear suppressScrollSettle under H2's mid-replay ownership");
  pass(R.liveStream === streamH2, "(c) H2 still owns the stream after H1's abort");
  pass(R.hydrationInProgress === false, "(c) H2 cleared hydrationInProgress on completion");
  pass(R.suppressScrollSettle === false, "(c) H2 cleared suppressScrollSettle on completion");
  pass(userMessageTexts(conversation).every((t, i) => t === "hc2-event-" + i),
    "(c) transcript is exactly H2's events");

  if (failures.length) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: stale overlapping readThread completion/rejection is guarded before any state mutation");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
