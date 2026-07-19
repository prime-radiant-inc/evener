// Issue #24: user-sent steering messages (the steer button, or queued user
// input drained as steering) must render as user messages — not as the
// system-looking collapsible "↻ steering injected" divider.
//
// The daemon marks user-originated steering with source:"user" on the
// serf/steering/injected notification and on hydrated steering thread items;
// appwire.js must thread that marker through both mapping paths and
// renderer.js must branch on it.
//
// Covers:
//   A. live notification mapping carries source through
//   B. renderer renders source:"user" steering via appendUserMessage (bubble
//      + images), not the steering divider
//   C. renderer keeps the divider treatment for system steering (no source)
//   D. hydration (eventsFromThread) carries item.source through
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve(""),
});
window.eval(appwireSrc);
window.SerfAppwire.tasks = () => Promise.resolve([]);
require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

async function run() {
  await new Promise((r) => setTimeout(r, 30));

  // ── A. live notification mapping carries source through ──────────────
  const liveUser = window.SerfAppwire.eventsFromNotification("serf/steering/injected", {
    text: "please focus on the tests",
    source: "user",
    images: [{ url: "data:image/png;base64,aW1n", name: "shot.png" }],
  });
  pass(liveUser.length === 1 && liveUser[0][0] === "STEERING_INJECTED", "live mapping should emit one STEERING_INJECTED, got " + JSON.stringify(liveUser));
  pass(liveUser[0] && liveUser[0][1].source === "user", "live mapping must carry source=user, got " + JSON.stringify(liveUser[0] && liveUser[0][1]));

  const liveSystem = window.SerfAppwire.eventsFromNotification("serf/steering/injected", {
    text: "loop detection: you appear to be stuck",
  });
  pass(liveSystem[0] && !liveSystem[0][1].source, "system steering should carry no source, got " + JSON.stringify(liveSystem[0] && liveSystem[0][1]));

  // ── B/C. renderer branches on source ─────────────────────────────────
  window.SerfRenderer.handleData("SESSION_START", { model: "test", profile: "test", session_id: "01TEST" });
  window.SerfRenderer.handleData("USER_INPUT", { text: "fix the flaky test", images: [] });
  for (const [kind, data] of liveUser) window.SerfRenderer.handleData(kind, data);
  for (const [kind, data] of liveSystem) window.SerfRenderer.handleData(kind, data);
  await new Promise((r) => setTimeout(r, 10));

  const users = conv.querySelectorAll(".user-message");
  pass(users.length === 2, "expected 2 user messages (input + user steering), got " + users.length);
  const steerBubble = users[1];
  pass(steerBubble && steerBubble.textContent.includes("please focus on the tests"), "user steering bubble should carry the steering text");
  const tag = steerBubble && steerBubble.querySelector(".user-message-tag");
  pass(tag && tag.textContent === "You", "user steering bubble should carry the 'You' tag");
  const thumb = steerBubble && steerBubble.querySelector(".user-image-thumb");
  pass(thumb && thumb.getAttribute("src") === "data:image/png;base64,aW1n", "user steering image should render as a thumbnail");

  const dividers = conv.querySelectorAll("details.steering");
  pass(dividers.length === 1, "expected exactly 1 steering divider (the system one), got " + dividers.length);
  pass(dividers[0] && dividers[0].textContent.includes("stuck"), "the surviving divider should be the system steering, got " + (dividers[0] && dividers[0].textContent));
  pass(users[1] && !users[1].textContent.includes("stuck"), "system steering must not render inside a user bubble");

  // ── D. hydration mapping carries item.source through ─────────────────
  const thread = {
    id: "01TEST",
    turns: [{
      id: "turn_1",
      status: "completed",
      items: [
        { type: "userMessage", id: "item_user_0", text: "fix the flaky test", status: "completed" },
        { type: "steering", id: "item_steering_1", text: "please focus on the tests", source: "user", status: "completed" },
        { type: "steering", id: "item_steering_2", text: "<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>", status: "completed" },
      ],
    }],
  };
  const hydEvents = window.SerfAppwire.eventsFromThread(thread);
  const hydSteering = hydEvents.filter(([kind]) => kind === "STEERING_INJECTED");
  pass(hydSteering.length === 2, "hydration should emit 2 STEERING_INJECTED events, got " + hydSteering.length);
  pass(hydSteering[0] && hydSteering[0][1].source === "user", "hydrated user steering must carry source=user, got " + JSON.stringify(hydSteering[0] && hydSteering[0][1]));
  pass(hydSteering[1] && !hydSteering[1][1].source, "hydrated system steering must carry no source, got " + JSON.stringify(hydSteering[1] && hydSteering[1][1]));

  // Hydrated user steering renders the same way as live (the renderer
  // consumes the same event shape on replay).
  for (const [kind, data] of hydSteering) window.SerfRenderer.handleData(kind, data);
  await new Promise((r) => setTimeout(r, 10));
  const usersAfter = conv.querySelectorAll(".user-message");
  pass(usersAfter.length === 3, "expected 3 user messages after hydrated user steering, got " + usersAfter.length);
  // The hydrated SYSTEM-REMINDER nudge is system steering: it keeps the
  // divider treatment, joining the live "stuck" divider.
  const dividersAfter = conv.querySelectorAll("details.steering");
  pass(dividersAfter.length === 2, "expected 2 steering dividers after hydration (stuck + system nudge), got " + dividersAfter.length);

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: user-sent steering renders as a user message (live + hydration); system steering keeps the divider");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
