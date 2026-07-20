const { JSDOM } = require("jsdom");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit"></button>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });

  const { window } = dom;
  window.marked = { parse: (t) => String(t || "").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

async function renderSteering(text) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("STEERING_INJECTED", { text });
  await new Promise(r => setTimeout(r, 10));
  return { window, conv };
}

function classifySteering(text) {
  const { window } = newHarness();
  return window.SerfRendererInternal.classifySteering(text);
}

let allPass = true;
async function scenario(name, text, check) {
  const { conv } = await renderSteering(text);
  const result = check(conv);
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  HTML: " + conv.innerHTML);
  }
}

function parserScenario(name, text, check) {
  const summary = classifySteering(text);
  const result = check(summary);
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  summary: " + JSON.stringify(summary));
  }
}

function expectText(el, needle) {
  return el && el.textContent.includes(needle);
}

function expectNotificationCard(conv, tone, needles) {
  const card = conv.querySelector(".notification-card");
  if (!card) return { ok: false, detail: "missing notification card" };
  if (conv.querySelector(".steering .steering-verb") && conv.textContent.includes("steering injected")) return { ok: false, detail: "rendered generic steering" };
  if (!card.classList.contains("notification-card-" + tone)) return { ok: false, detail: "missing " + tone + " tone" };
  for (const needle of needles) {
    if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
  }
  return { ok: true };
}

function expectNotification(summary, expected) {
  if (!summary || summary.kind !== "notification") return { ok: false, detail: "not notification" };
  const n = summary.notification || {};
  for (const [key, value] of Object.entries(expected)) {
    if (key === "attrs") {
      for (const [attr, attrValue] of Object.entries(value)) {
        if (!n.attrs || n.attrs[attr] !== attrValue) return { ok: false, detail: "attr " + attr + " = " + (n.attrs && n.attrs[attr]) };
      }
      continue;
    }
    if (key === "communicate") {
      if (value === null) {
        if (n.communicate !== null) return { ok: false, detail: "communicate should be null" };
        continue;
      }
      for (const [field, fieldValue] of Object.entries(value)) {
        const actual = n.communicate && n.communicate[field];
        if (Array.isArray(fieldValue)) {
          if (JSON.stringify(actual) !== JSON.stringify(fieldValue)) return { ok: false, detail: "communicate." + field + " = " + JSON.stringify(actual) };
        } else if (actual !== fieldValue) {
          return { ok: false, detail: "communicate." + field + " = " + actual };
        }
      }
      continue;
    }
    if (n[key] !== value) return { ok: false, detail: key + " = " + n[key] };
  }
  if (n.rawText !== summary.cleanText) return { ok: false, detail: "rawText not inspectable as cleanText" };
  return { ok: true };
}

(async () => {
  const delegateEnvelope = JSON.stringify({
    message: "Status: DONE\nCommit hash(es): 3fbe7256\nOne-line test summary: node test passed; go test passed.\nConcerns: None",
    data: {
      status: "DONE",
      commit_hashes: ["3fbe7256"],
      test_summary: "node test passed; go test passed.",
      concerns: []
    },
    artifacts: []
  });

  const delegateNotification = `<job-notification job_id="job_delegate" event="completed" job_type="delegate" status="completed" reason="" output_bytes="402" transcript_ref="local:delegate">
Job job_delegate completed. Output is available through read_transcript(transcript_ref="local:delegate") if needed.
excerpt:
${delegateEnvelope}
</job-notification>`;

  await scenario("delegate completion notification parses", delegateNotification, (conv) =>
    expectNotificationCard(conv, "success", ["Job completed", "job_delegate", "delegate", "local:delegate", "DONE", "3fbe7256", "node test passed; go test passed."]));

  const exhaustedNotification = `<job-notification job_id="job_exhausted" event="completed" job_type="delegate" status="exhausted" reason="tool_round_budget_exhausted" output_bytes="42" transcript_ref="local:child-exhausted">
Job job_exhausted exhausted. Output is available through read_transcript(transcript_ref="local:child-exhausted") if needed.
</job-notification>`;
  parserScenario("exhausted notification is terminal non-success", exhaustedNotification, (summary) => {
    const n = summary && summary.notification;
    if (!n) return { ok: false, detail: "not notification" };
    if (n.tone === "success" || n.tone === "neutral") return { ok: false, detail: "exhausted tone = " + n.tone };
    if (!n.attrs || n.attrs.status !== "exhausted" || n.attrs.reason !== "tool_round_budget_exhausted") {
      return { ok: false, detail: "raw exhaustion metadata was not retained" };
    }
    return { ok: true };
  });

  await scenario("raw notification remains inspectable", delegateNotification, (conv) => {
    const pre = conv.querySelector(".notification-card-raw pre");
    if (!pre) return { ok: false, detail: "missing raw notification pre" };
    if (pre.textContent !== delegateNotification) return { ok: false, detail: "raw notification text changed" };
    return { ok: true };
  });

  await scenario("completed job recedes: neutral done-glyph, no chip wall, demoted metadata", delegateNotification, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing card" };
    // The wall of bordered key/value chips is gone (one containment device).
    if (card.querySelector(".notification-card-chip") || card.querySelector(".notification-card-meta")) {
      return { ok: false, detail: "chip wall should be removed" };
    }
    // Done recedes: a neutral done icon, not a coloured/filled dot.
    const glyph = card.querySelector(".notification-card-glyph");
    if (!glyph || !glyph.querySelector("svg")) return { ok: false, detail: "completed job should show a neutral done icon, got " + (glyph && glyph.innerHTML) };
    if (glyph.textContent.includes("✓")) return { ok: false, detail: "completed job should not use the literal ✓ glyph" };
    // The job kind is demoted to a quiet secondary; the raw id is not echoed as boilerplate prose.
    const sub = card.querySelector(".notification-card-sub");
    if (!sub || !expectText(sub, "delegate")) return { ok: false, detail: "job kind should appear on the demoted secondary" };
    if (card.querySelector(".notification-card-prose")) return { ok: false, detail: "job boilerplate prose should be suppressed" };
    return { ok: true };
  });

  const watchNotification = `<job-notification event="watch" status="watch" watch_id="watch_01" transcript_ref="local:watch">
Watch watch_01 triggered for output match.
excerpt:
non-json watch excerpt
</job-notification>`;
  parserScenario("watch notification parses as warning with non-json excerpt", watchNotification, (summary) =>
    expectNotification(summary, {
      type: "watch",
      title: "Watch triggered",
      tone: "warning",
      excerpt: "non-json watch excerpt",
      communicate: null,
      attrs: { event: "watch", watch_id: "watch_01", transcript_ref: "local:watch" }
    }));
  await scenario("watch notification renders minimal warning card", watchNotification, (conv) =>
    expectNotificationCard(conv, "warning", ["Watch triggered", "watch_01", "local:watch"]));

  const watchSendEnvelope = JSON.stringify({
    message: "Status: DELIVERED\nConcerns: routed to observer",
    data: { status: "DELIVERED", concerns: ["observer still running"] },
    artifacts: []
  });
  const watchSendNotification = `<job-notification event="watch_send" status="sent" watch_id="watch_02" delegate_id="dlg_abc" transcript_ref="local:watch-send">
Watch watch_02 delivered to observer delegate dlg_abc.
excerpt:
${watchSendEnvelope}
</job-notification>`;
  parserScenario("watch-send notification parses concerns and warning tone", watchSendNotification, (summary) =>
    expectNotification(summary, {
      type: "watch-send",
      title: "Watch delivered",
      tone: "warning",
      communicate: { status: "DELIVERED", concerns: ["observer still running"] },
      attrs: { event: "watch_send", watch_id: "watch_02", delegate_id: "dlg_abc" }
    }));
  await scenario("watch-send notification renders minimal warning card", watchSendNotification, (conv) =>
    expectNotificationCard(conv, "warning", ["Watch delivered", "watch_02", "dlg_abc", "DELIVERED", "observer still running"]));

  await scenario("communicate concerns surface as a tidy facts list", watchSendNotification, (conv) => {
    const facts = conv.querySelector(".notification-card-facts");
    if (!facts) return { ok: false, detail: "missing facts list" };
    if (!expectText(facts, "concerns") || !expectText(facts, "observer still running")) {
      return { ok: false, detail: "concerns should render in the facts list" };
    }
    return { ok: true };
  });

  const observerEnvelope = JSON.stringify({
    message: "Status: DONE\nOne-line test summary: observer callback delivered.",
    data: { status: "DONE", test_summary: "observer callback delivered.", concerns: [] },
    artifacts: []
  });
  const observerNotification = `Observer callback:
message: Observer sidecar called back to parent
output: ${observerEnvelope}`;
  parserScenario("observer callback coerces success tone to warning", observerNotification, (summary) =>
    expectNotification(summary, {
      type: "observer-callback",
      title: "Observer callback",
      tone: "warning",
      prose: "Observer sidecar called back to parent",
      communicate: { status: "DONE", testSummary: "observer callback delivered.", concerns: [] }
    }));
  await scenario("observer callback renders minimal warning card", observerNotification, (conv) =>
    expectNotificationCard(conv, "warning", ["Observer callback", "Observer sidecar called back", "DONE", "observer callback delivered."]));

  const malformedNotification = `<job-notification job_id="job_malformed" event="completed" status="completed" transcript_ref="local:malformed">
Job job_malformed completed with a malformed excerpt.
excerpt:
<script>window.__bad = true</script>
</job-notification>`;
  parserScenario("malformed excerpt remains raw inspectable text", malformedNotification, (summary) =>
    expectNotification(summary, {
      type: "job",
      tone: "success",
      excerpt: "<script>window.__bad = true</script>",
      communicate: null,
      attrs: { job_id: "job_malformed", transcript_ref: "local:malformed" }
    }));
  await scenario("malformed excerpt is not injected as HTML", malformedNotification, (conv) => {
    const cardResult = expectNotificationCard(conv, "success", ["Job completed", "job_malformed", "local:malformed"]);
    if (!cardResult.ok) return cardResult;
    if (conv.querySelector("script")) return { ok: false, detail: "malformed excerpt injected as script HTML" };
    return { ok: true };
  });

  const errorNotification = `<job-notification job_id="job_error" event="completed" job_type="shell" status="completed" exit_code="2" transcript_ref="local:error">
Job job_error completed with a nonzero exit code.
excerpt:
not-json error output
</job-notification>`;
  parserScenario("nonzero exit code gives error tone", errorNotification, (summary) =>
    expectNotification(summary, {
      type: "job",
      title: "Job completed",
      tone: "error",
      excerpt: "not-json error output",
      communicate: null,
      attrs: { job_id: "job_error", exit_code: "2", transcript_ref: "local:error" }
    }));
  await scenario("nonzero exit code renders minimal error card", errorNotification, (conv) =>
    expectNotificationCard(conv, "error", ["Job completed", "job_error", "shell", "2", "local:error"]));

  await scenario("outer failure overrides successful communicate status", `<job-notification job_id="job_failed_done" event="completed" job_type="delegate" status="failed" reason="" output_bytes="12" transcript_ref="local:failed-done">
Job job_failed_done failed. Output is available through read_transcript(transcript_ref="local:failed-done") if needed.
excerpt:
{"data":{"status":"DONE"}}
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-error");
    if (!card) return { ok: false, detail: "missing error card" };
    if (!expectText(card, "Job failed")) return { ok: false, detail: "missing failed title" };
    if (!expectText(card, "DONE")) return { ok: false, detail: "missing communicate status" };
    return { ok: true };
  });

  await scenario("shell failure notification shows failure metadata", `<job-notification job_id="job_shell" event="failed" job_type="shell" status="failed" reason="exit_nonzero" output_bytes="128" exit_code="2" transcript_ref="job:job_shell">
Job job_shell failed. Output is available through read_transcript(transcript_ref="job:job_shell") if needed.
excerpt:
command failed on line 1
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-error");
    if (!card) return { ok: false, detail: "missing error card" };
    // The failure signal (kind, exit code, reason) surfaces on the card; the
    // byte count is plumbing and stays inspectable in the raw disclosure.
    for (const needle of ["Job failed", "job_shell", "shell", "exit_nonzero", "exit 2", "job:job_shell", "command failed on line 1"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    const sub = card.querySelector(".notification-card-sub");
    if (!sub || !expectText(sub, "exit 2")) return { ok: false, detail: "exit code should surface on the demoted secondary line" };
    const raw = card.querySelector(".notification-card-raw pre");
    if (!raw || raw.textContent.indexOf("output_bytes=\"128\"") === -1) return { ok: false, detail: "byte count should remain inspectable in raw" };
    return { ok: true };
  });

  await scenario("watch event notification renders as watch card", `<job-notification job_id="" event="watch" job_type="watch" status="watch" reason="event: ASSISTANT_TEXT_END" output_bytes="0">
Watch event triggered: event: ASSISTANT_TEXT_END.
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-warning");
    if (!card) return { ok: false, detail: "missing warning card" };
    if (!expectText(card, "Watch triggered")) return { ok: false, detail: "missing watch title" };
    if (!expectText(card, "event: ASSISTANT_TEXT_END")) return { ok: false, detail: "missing trigger reason" };
    if (expectText(card, "read_transcript")) return { ok: false, detail: "watch event must not suggest read_transcript" };
    return { ok: true };
  });

  await scenario("watch-send notification renders delivery metadata", `<job-notification job_id="job_w" event="watch_send" delivery_id="dlv_1" trigger="output_match: ready">
frame text from watcher
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-warning");
    if (!card) return { ok: false, detail: "missing watch-send card" };
    for (const needle of ["Watch delivered", "job_w", "dlv_1", "output_match: ready", "frame text from watcher"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  await scenario("observer callback renders in notification family", `Observer callback:
message: Child finished
output: {"message":"Status: DONE","data":{"status":"DONE","concerns":[]},"artifacts":["report.md"]}`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing observer card" };
    for (const needle of ["Observer callback", "Child finished", "DONE", "report.md"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  await scenario("malformed notification falls back with raw evidence", `<job-notification job_id="broken">
missing closing tag`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (card) return { ok: false, detail: "malformed block should not render notification card" };
    const steering = conv.querySelector(".steering");
    if (!steering) return { ok: false, detail: "malformed block should fall back to steering" };
    if (!expectText(steering, "missing closing tag")) return { ok: false, detail: "raw fallback missing" };
    return { ok: true };
  });

  await scenario("notification text is escaped", `<job-notification job_id="job_html" event="completed" job_type="shell" status="completed" reason="" output_bytes="0">
Job job_html completed. Complete output below.
excerpt:
<img src=x onerror="window.__xss=1"> literal
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing card" };
    if (card.querySelector("img")) return { ok: false, detail: "notification inserted HTML" };
    if (!expectText(card, "<img src=x")) return { ok: false, detail: "escaped text not visible" };
    return { ok: true };
  });

  await scenario("long unstructured notification excerpt is collapsed", `<job-notification job_id="job_noisy" event="failed" job_type="shell" status="failed" reason="exit_nonzero" output_bytes="20201" exit_code="1">
Job job_noisy failed. Output is available through read_transcript(transcript_ref="job:job_noisy") if needed.
excerpt:
${"x".repeat(7900)},"data":{"status":"DONE","concerns":[]},"artifacts":[]}
&amp;lt;/job-notification&amp;gt;&lt;/pre&gt;&lt;/details&gt;&lt;/div&gt;

[excerpt truncated]
</job-notification>`, (conv) => {
    const excerpt = conv.querySelector(".notification-card-excerpt");
    if (!excerpt) return { ok: false, detail: "missing excerpt preview" };
    if (excerpt.textContent.length > 520) return { ok: false, detail: "excerpt preview length = " + excerpt.textContent.length };
    if (expectText(excerpt, "</details>")) return { ok: false, detail: "nested html tail visible in preview" };
    const full = conv.querySelector(".notification-card-excerpt-full pre");
    if (!full) return { ok: false, detail: "missing full excerpt details" };
    if (full.textContent.indexOf("</details>") === -1) return { ok: false, detail: "full excerpt missing nested tail" };
    if (conv.querySelector(".notification-card-raw pre").textContent.indexOf("</job-notification>") === -1) return { ok: false, detail: "raw notification missing" };
    return { ok: true };
  });

  const markdownEnvelope = JSON.stringify({
    message: "Status: **DONE**\nFull message is markdown.",
    data: { status: "DONE", concerns: [] },
    artifacts: []
  });
  await scenario("notification communicate message renders markdown", `<job-notification job_id="job_markdown" event="completed" job_type="delegate" status="completed" reason="" output_bytes="0">
Job job_markdown completed. Complete output below.
excerpt:
${markdownEnvelope}
</job-notification>`, (conv) => {
    const message = conv.querySelector(".notification-card-message");
    if (!message) return { ok: false, detail: "missing message" };
    if (!message.querySelector("strong")) return { ok: false, detail: "message markdown not rendered" };
    if (!expectText(message, "Status: DONE")) return { ok: false, detail: "message text missing" };
    return { ok: true };
  });

  await scenario("notification communicate message truncates at 8k", `<job-notification job_id="job_long" event="completed" job_type="delegate" status="completed" reason="" output_bytes="0">
Job job_long completed. Complete output below.
excerpt:
${JSON.stringify({ message: "x".repeat(8200), data: { status: "DONE", concerns: [] }, artifacts: [] })}
</job-notification>`, (conv) => {
    const message = conv.querySelector(".notification-card-message");
    if (!message) return { ok: false, detail: "missing message" };
    if (message.textContent.length !== 8000) return { ok: false, detail: "message length = " + message.textContent.length };
    return { ok: true };
  });

  // Issue #36: a notification turn can carry several <job-notification> blocks
  // joined by newlines (the daemon drains every pending notification into one
  // reminder). Each block must parse and render individually — a greedy
  // per-message match aggregates them into one notification.
  const multiNotification = `<job-notification job_id="job_033sO5k8TeGZmH2lGJT6BK" event="watch" job_type="watch" status="watch" reason="output_match: jstest: all tests passed" output_bytes="0">
Job job_033sO5k8TeGZmH2lGJT6BK watch. Output is available through read_transcript(transcript_ref="job:job_033sO5k8TeGZmH2lGJT6BK") if needed.
</job-notification>
<job-notification job_id="job_033sNxu2eUcKPnizpWBQER" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="85" exit_code="0">
Job job_033sNxu2eUcKPnizpWBQER completed. Complete output below.
excerpt:
PASS: composer drafts are sticky per session (localStorage)
jstest: all tests passed

</job-notification>
<job-notification job_id="job_033sO5k8TeGZmH2lGJT6BK" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="473" exit_code="0">
Job job_033sO5k8TeGZmH2lGJT6BK completed. Complete output below.
excerpt:
PASS: composer drafts are sticky per session (localStorage)
jstest: all tests passed
990534ea hub: drafts guard against cross-session text leaks (#21)

</job-notification>`;

  parserScenario("multi-block notification turn parses each block individually", multiNotification, (summary) => {
    if (!summary || summary.kind !== "notification") return { ok: false, detail: "not notification" };
    const list = summary.notifications;
    if (!Array.isArray(list) || list.length !== 3) return { ok: false, detail: "notifications length = " + (list && list.length) };
    const expect = [
      { job_id: "job_033sO5k8TeGZmH2lGJT6BK", event: "watch", status: "watch", tone: "warning" },
      { job_id: "job_033sNxu2eUcKPnizpWBQER", event: "completed", status: "completed", tone: "success" },
      { job_id: "job_033sO5k8TeGZmH2lGJT6BK", event: "completed", status: "completed", tone: "success" },
    ];
    for (let i = 0; i < expect.length; i++) {
      const n = list[i];
      for (const [key, value] of Object.entries(expect[i])) {
        const actual = key === "tone" ? n.tone : (n.attrs && n.attrs[key]);
        if (actual !== value) return { ok: false, detail: "block " + i + " " + key + " = " + actual };
      }
    }
    // Each block keeps only its own raw text: the first block must not span
    // into the third block's excerpt.
    if (list[0].rawText.includes("990534ea")) return { ok: false, detail: "first block aggregated later blocks" };
    if (!list[2].excerpt.includes("990534ea hub: drafts guard against cross-session text leaks (#21)")) {
      return { ok: false, detail: "third block lost its own excerpt" };
    }
    return { ok: true };
  });

  await scenario("multi-block notification turn renders one card per block", multiNotification, (conv) => {
    const cards = conv.querySelectorAll(".notification-card");
    if (cards.length !== 3) return { ok: false, detail: "card count = " + cards.length };
    const watchCards = conv.querySelectorAll('.notification-card-warning[data-job-id="job_033sO5k8TeGZmH2lGJT6BK"]');
    if (watchCards.length !== 1) return { ok: false, detail: "watch card count = " + watchCards.length };
    // A watch frame carrying a concrete job_id renders "Job watch" (only a
    // job-less watch event titles "Watch triggered").
    if (!expectText(watchCards[0], "Job watch")) return { ok: false, detail: "watch card missing title" };
    if (!expectText(watchCards[0], "output_match: jstest: all tests passed")) return { ok: false, detail: "watch card missing trigger reason" };
    const doneCards = conv.querySelectorAll('.notification-card-success[data-job-id="job_033sO5k8TeGZmH2lGJT6BK"]');
    if (doneCards.length !== 1) return { ok: false, detail: "completed card count = " + doneCards.length };
    if (!expectText(doneCards[0], "990534ea")) return { ok: false, detail: "completed card missing its excerpt" };
    const other = conv.querySelector('.notification-card-success[data-job-id="job_033sNxu2eUcKPnizpWBQER"]');
    if (!other) return { ok: false, detail: "missing card for job_033sNxu2eUcKPnizpWBQER" };
    // Each card's raw disclosure shows only its own block.
    const raw = cards[0].querySelector(".notification-card-raw pre");
    if (!raw || raw.textContent.includes("990534ea")) return { ok: false, detail: "first card raw aggregated later blocks" };
    return { ok: true };
  });

  await scenario("blocks parse individually with arbitrary text between and around them",
    "lead-in note\n" + watchNotification + "\narbitrary middle text\n" + errorNotification + "\ntrailing note", (conv) => {
      const cards = conv.querySelectorAll(".notification-card");
      if (cards.length !== 2) return { ok: false, detail: "card count = " + cards.length };
      if (!expectText(cards[0], "Watch triggered")) return { ok: false, detail: "first card is not the watch notification" };
      if (!expectText(cards[1], "Job completed")) return { ok: false, detail: "second card is not the completion notification" };
      // The interstitial text is preserved, not swallowed into a notification.
      if (!conv.textContent.includes("arbitrary middle text")) return { ok: false, detail: "interstitial text was lost" };
      return { ok: true };
    });

  if (!allPass) process.exit(1);
  console.log("PASS: notification renderer assertions");
  process.exit(0); // renderer pollers keep the event loop alive otherwise
})();
