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

function expectText(el, needle) {
  return el && el.textContent.includes(needle);
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

  await scenario("delegate completion notification parses", `<job-notification job_id="job_delegate" event="completed" job_type="delegate" status="completed" reason="" output_bytes="402" transcript_ref="local:delegate">
Job job_delegate completed. Output is available through read_transcript(transcript_ref="local:delegate") if needed.
excerpt:
${delegateEnvelope}
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing notification card" };
    if (conv.querySelector(".steering .steering-verb") && conv.textContent.includes("steering injected")) return { ok: false, detail: "rendered generic steering" };
    if (!card.classList.contains("notification-card-success")) return { ok: false, detail: "missing success tone" };
    for (const needle of ["Job completed", "job_delegate", "delegate", "local:delegate", "DONE", "3fbe7256", "node test passed; go test passed."]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  if (!allPass) process.exit(1);
  console.log("PASS: notification renderer assertions");
  process.exit(0); // renderer pollers keep the event loop alive otherwise
})();
