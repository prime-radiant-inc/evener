// Live session titles are daemon truth. A generated/renamed title notification
// must update both the open workspace header and the sidebar tree without a page
// reload.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");

function makeWindow() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <aside id="sidebar"></aside>
    <header class="workspace-header" data-session-id="01TITLE">
      <div class="workspace-title"><span id="workspace-session-title" class="title">please fix the appwire title plumbing because the sidebar uses this whole prompt</span></div>
    </header>
    <div id="conversation" data-session-id="01TITLE" data-state="idle"></div>
    <form data-input-form data-session-id="01TITLE"><textarea class="message-input"></textarea></form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/s/01TITLE",
  });
  const { window } = dom;
  window.marked = { parse: (t) => String(t || "") };
  window.SerfIcons = {
    idle: "i", working: "w", warning: "!", error: "x", ended: "e",
    questionWaiting: "?", yourMove: "y", favorite: "*",
  };
  window.fetchCalls = [];
  window.fetch = (url) => {
    window.fetchCalls.push(String(url));
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({
        live: [],
        projects: [{
          key: "project:/tmp/title",
          name: "title",
          sessions: [{
            row_id: "project:/tmp/title:01TITLE",
            session_id: "01TITLE",
            ref: "local:01TITLE",
            title: "Fix appwire titles",
            state: "idle",
            updated_at: new Date().toISOString(),
          }],
          default_expanded: true,
        }],
        archived_projects: [], test_runs: [], needs_you: [], favorites: [],
      }),
    });
  };
  window.eval(appwireSrc);
  require("./load-renderer").evalRenderer(window);
  window.eval(sidebarSrc);
  window.SerfRenderer.init(window.document.getElementById("conversation"));
  return window;
}

async function settle(ms = 30) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

(async () => {
  const window = makeWindow();
  await settle(50);

  const failures = [];
  const pass = (condition, message) => { if (!condition) failures.push("FAIL: " + message); };

  let events = window.SerfAppwire.eventsFromNotification("serf/thread/name/changed", {
    ref: "local:01TITLE",
    threadId: "01TITLE",
    name: "Fix appwire titles",
    source: "prompt",
  });
  pass(events.some(([kind]) => kind === "THREAD_TITLE_CHANGED"), "appwire should translate name-change notification into THREAD_TITLE_CHANGED");
  for (const [kind, data] of events) window.SerfRenderer.handleData(kind, data);

  const title = window.document.getElementById("workspace-session-title");
  pass(title && title.textContent === "Fix appwire titles", "workspace title should update without reload, got: " + (title && title.textContent));

  const before = window.fetchCalls.length;
  for (const handler of window.SerfAppwire._testNotificationHandlers || []) {
    handler("serf/thread/name/changed", { ref: "local:01TITLE", threadId: "01TITLE", name: "Fix appwire titles" });
  }
  await settle(2100);
  pass(window.fetchCalls.length > before, "sidebar should schedule /api/tree resync on name-change notification");
  const sidebarTitle = window.document.querySelector("#sidebar .sb-row .title");
  pass(sidebarTitle && sidebarTitle.textContent === "Fix appwire titles", "sidebar title should update without reload, got: " + (sidebarTitle && sidebarTitle.textContent));

  if (failures.length) {
    console.log(window.document.body.innerHTML);
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }
  console.log("PASS: live title notifications update workspace and sidebar");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
