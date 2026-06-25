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
  window.marked = { parse: (t) => t };
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

  if (!allPass) process.exit(1);
  console.log("PASS: notification renderer assertions");
  process.exit(0); // renderer pollers keep the event loop alive otherwise
})();
