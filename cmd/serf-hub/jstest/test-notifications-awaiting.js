// JSDOM coverage for the active->awaiting alert transition in
// assets/notifications.js (the turn-end "ask_user" case). Mirrors the
// makeWindow scaffolding in test-notifications.js; see that file for the
// broader transition-detection and title/favicon coverage.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/notifications.js", "utf8");

// Tiny deferred Promise — lets a test wait until the module's first
// fetch().then() callback chain has run.
function flush(window) {
  return new Promise((resolve) => window.setTimeout(resolve, 0));
}

// Build a fresh JSDOM window pre-wired with the test-controllable
// globals. `live` is the array returned from the mocked /api/search.
function makeWindow(opts) {
  opts = opts || {};
  const dom = new JSDOM(
    `<!DOCTYPE html><html><head>
      <link rel="icon" href="data:image/svg+xml;utf8,<svg/>">
    </head><body>
      <header class="workspace-header" data-session-id="01TEST">
        <div class="workspace-title-row">
          <div class="workspace-title">
            <span class="title">my session</span>
          </div>
        </div>
      </header>
      <div id="conversation"></div>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" }
  );
  const { window } = dom;

  if (opts.prefs) {
    window.localStorage.setItem("serf-hub.notifications", JSON.stringify(opts.prefs));
  }

  // Mock fetch to return our supplied live list.
  const liveRef = { value: opts.live || [] };
  window.__liveRef = liveRef;
  window.fetch = (url) => {
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ live: liveRef.value, past: [] }),
    });
  };

  // Track Notification constructions.
  const notifications = [];
  function MockNotification(title, opts) {
    notifications.push({ title: title, body: (opts || {}).body });
    this.title = title;
  }
  MockNotification.permission = opts.permission || "granted";
  MockNotification.requestPermission = () => Promise.resolve(MockNotification.permission);
  window.Notification = MockNotification;
  window.__notifications = notifications;

  // Track AudioContext / oscillator usage.
  const audioCalls = [];
  window.AudioContext = function () {
    audioCalls.push("ctor");
    this.createOscillator = () => {
      audioCalls.push("createOscillator");
      return {
        frequency: { value: 0 },
        connect: () => {},
        start: () => audioCalls.push("start"),
        stop: () => audioCalls.push("stop"),
      };
    };
    this.createGain = () => ({
      gain: { value: 0 },
      connect: () => {},
    });
    this.destination = {};
    this.close = () => {};
  };
  window.__audioCalls = audioCalls;

  // Default to unfocused; tests override.
  window.document.hasFocus = () => !!opts.focused;

  return window;
}

function load(window) {
  window.eval(SRC);
}

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

(async function main() {
  // 1. active->awaiting transition fires Notification (the turn-end
  //    "ask_user" case: an agent finishes its turn and starts waiting on
  //    the user, os pref on, unfocused).
  {
    const w = makeWindow({
      prefs: { os: true },
      live: [{ id: "a", state: "active", title: "Sess A" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    w.__liveRef.value = [{ id: "a", state: "awaiting", title: "Sess A" }];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 1,
      "expected 1 notification for active->awaiting, got: " + w.__notifications.length
    );
    assert(
      w.__notifications[0].title === "serf · Sess A",
      "notification title format wrong: " + w.__notifications[0].title
    );
  }

  // 2. awaiting->awaiting (no state change) must NOT fire again.
  {
    const w = makeWindow({
      prefs: { os: true },
      live: [{ id: "a", state: "awaiting", title: "Sess A" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    // Same state on the next poll — not a transition at all.
    w.__liveRef.value = [{ id: "a", state: "awaiting", title: "Sess A" }];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 0,
      "expected 0 notifications for awaiting->awaiting, got: " + w.__notifications.length
    );
  }

  console.log("notifications-awaiting.js tests passed");
  process.exit(0);
})();
