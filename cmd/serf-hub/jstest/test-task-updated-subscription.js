// serf/task/updated: (1) appwire.js maps the wire notification to a
// TASKS_CHANGED client event, and (2) the renderer's handleData subscribes
// to that event and updates the task-status row immediately from the pushed
// total/done, without waiting for the next 5s poll. Copy T3 (later, not this
// task) retires startTaskBadgePoller once this event-driven path has landed
// — this test does not assert its absence.
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

// --- 1. Wire mapping: appwire.js's eventsFromNotification ------------------
{
  const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
    runScripts: "outside-only",
    url: "http://127.0.0.1:9180/s/01S",
  });
  dom.window.eval(appwireSrc);

  const out = dom.window.SerfAppwire.eventsFromNotification("serf/task/updated", { threadId: "01S", total: 3, done: 1 });
  // Manual comparison, not assert.deepStrictEqual: the JSDOM realm's Array/
  // Object constructors differ by identity from this (Node) realm's, which
  // trips deepStrictEqual's prototype check even when the shapes match.
  assert.strictEqual(out.length, 1, "serf/task/updated must map to exactly one event, got " + JSON.stringify(out));
  assert.strictEqual(out[0][0], "TASKS_CHANGED", "serf/task/updated must map to TASKS_CHANGED, got " + JSON.stringify(out[0]));
  assert.strictEqual(out[0][1].total, 3, "TASKS_CHANGED must carry total, got " + JSON.stringify(out[0][1]));
  assert.strictEqual(out[0][1].done, 1, "TASKS_CHANGED must carry done, got " + JSON.stringify(out[0][1]));
}

// --- 2. Renderer subscription: handleData's TASKS_CHANGED case -------------
(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button type="button" class="status-item tasks-status" data-tasks-trigger title="task list"><span class="status-key">tasks</span><span class="status-value" data-task-status-text>loading…</span></button>
    </div>
    <header class="workspace-header" data-session-id="01S"></header>
    <div id="conversation" data-session-id="01S" data-state="active"></div>
    <form data-input-form data-session-id="01S">
      <textarea class="message-input"></textarea>
    </form>
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
    readThread: () => Promise.resolve({
      thread: {
        id: "01S",
        sessionId: "01S",
        serf: { ref: "local:01S" },
        status: { type: "active" },
        queue: { depth: 0, preview: [] },
        turns: [],
      },
    }),
    eventsFromThread: () => [],
    eventsFromNotification: (method, params) => {
      if (method === "serf/task/updated") {
        return [["TASKS_CHANGED", { total: params.total, done: params.done }]];
      }
      return [];
    },
    activeTurnIDFromThread: () => "",
    tasks: () => Promise.resolve([]),
  };

  require("./load-renderer").evalRenderer(window);

  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  await new Promise((resolve) => setTimeout(resolve, 30));

  assert.ok(notify, "renderer did not subscribe to AppWire notifications");

  // Assert synchronously, in the same tick as notify(): handleData's
  // TASKS_CHANGED case updates the badge immediately (synchronously) and
  // only THEN kicks off the tasks() refetch as a microtask. Our tasks()
  // stub resolves to [], which (correctly) blanks the badge back to "no
  // tasks" once that microtask runs — awaiting here would race exactly
  // that harmless-but-overwriting refetch, so the assertion must happen
  // before it, not after.
  notify("serf/task/updated", { threadId: "01S", total: 3, done: 1 });

  const textEl = window.document.querySelector("[data-task-status-text]");
  assert.ok(textEl, "task-status-text element must exist");
  assert.ok(/1\/3/.test(textEl.textContent),
    "serf/task/updated must refresh the task-status row immediately, got " + JSON.stringify(textEl.textContent));

  console.log("PASS test-task-updated-subscription.js");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
