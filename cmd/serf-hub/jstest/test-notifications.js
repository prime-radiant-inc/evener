// JSDOM coverage for assets/notifications.js. Loads the module into a
// JSDOM window with a mocked fetch, Notification, and AudioContext, then
// exercises the title-count, transition-detection, focus-skip, and
// settings-event re-init paths.
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
  // Clean any previous state from earlier evals just in case.
  window.eval(SRC);
}

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

(async function main() {
  // 1. title:true with one awaiting session yields "(1) serf — …".
  {
    const w = makeWindow({ prefs: { title: true }, live: [{ id: "a", state: "awaiting" }] });
    load(w);
    await flush(w);
    await flush(w);
    assert(
      w.document.title === "(1) serf — my session",
      "title prefix expected, got: " + JSON.stringify(w.document.title)
    );
  }

  // 2. title:false → no count, plain title.
  {
    const w = makeWindow({ prefs: { title: false }, live: [{ id: "a", state: "awaiting" }] });
    load(w);
    await flush(w);
    await flush(w);
    assert(
      w.document.title.indexOf("(") === -1,
      "no count expected, got: " + JSON.stringify(w.document.title)
    );
  }

  // 3. idle→awaiting transition fires Notification (os pref on, unfocused).
  {
    const w = makeWindow({
      prefs: { os: true },
      live: [{ id: "a", state: "idle", title: "Sess A" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    // Now flip state and trigger another poll.
    w.__liveRef.value = [{ id: "a", state: "awaiting", title: "Sess A" }];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 1,
      "expected 1 notification, got: " + w.__notifications.length
    );
    assert(
      w.__notifications[0].title === "serf · Sess A",
      "notification title format wrong: " + w.__notifications[0].title
    );
  }

  // 4. Sound: same transition with sound pref on plays a tone.
  {
    const w = makeWindow({
      prefs: { sound: true },
      live: [{ id: "a", state: "processing" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    w.__liveRef.value = [{ id: "a", state: "errored" }];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__audioCalls.indexOf("createOscillator") !== -1,
      "expected oscillator created, got: " + JSON.stringify(w.__audioCalls)
    );
    assert(
      w.__audioCalls.indexOf("start") !== -1,
      "expected oscillator start, got: " + JSON.stringify(w.__audioCalls)
    );
  }

  // 5. document.hasFocus()===true skips OS notification on transition.
  {
    const w = makeWindow({
      prefs: { os: true },
      focused: true,
      live: [{ id: "a", state: "idle" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    w.__liveRef.value = [{ id: "a", state: "awaiting" }];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 0,
      "expected no notification while focused, got: " + w.__notifications.length
    );
  }

  // 6. serf-hub:notifications-changed event causes a re-poll (apply title).
  {
    const w = makeWindow({ prefs: { title: false }, live: [{ id: "a", state: "awaiting" }] });
    load(w);
    await flush(w);
    await flush(w);
    const before = w.document.title;
    assert(before.indexOf("(") === -1, "baseline should be no count: " + before);
    // Flip pref + dispatch event.
    w.localStorage.setItem(
      "serf-hub.notifications",
      JSON.stringify({ title: true })
    );
    w.document.dispatchEvent(new w.CustomEvent("serf-hub:notifications-changed"));
    await flush(w);
    await flush(w);
    assert(
      w.document.title.indexOf("(1) serf — ") === 0,
      "after event title should have count, got: " + w.document.title
    );
  }

  // 7. First-poll seeding: a session already in `awaiting` at init time
  //    must NOT fire a notification when the very next poll returns the
  //    same state. This is the failure mode kata #28 calls out — opening
  //    the hub on a long-idle awaiting session would otherwise spam an OS
  //    notification on every reload.
  {
    const w = makeWindow({
      prefs: { os: true, sound: true },
      live: [{ id: "a", state: "awaiting", title: "Already Awaiting" }],
    });
    load(w);
    await flush(w);
    await flush(w);
    // Same state on the next poll.
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 0,
      "expected 0 notifications when init found session already awaiting; got: " +
        w.__notifications.length
    );
    assert(
      w.__audioCalls.indexOf("createOscillator") === -1,
      "expected no tone; got: " + JSON.stringify(w.__audioCalls)
    );
  }

  // 8. Multiple existing awaiting sessions, then a new idle session
  //    transitions to awaiting — only the new transition fires.
  {
    const w = makeWindow({
      prefs: { os: true },
      live: [
        { id: "a", state: "awaiting", title: "Was Awaiting" },
        { id: "b", state: "idle", title: "Was Idle" },
      ],
    });
    load(w);
    await flush(w);
    await flush(w);
    // a stays awaiting (no fire), b idle->awaiting (fire).
    w.__liveRef.value = [
      { id: "a", state: "awaiting", title: "Was Awaiting" },
      { id: "b", state: "awaiting", title: "Was Idle" },
    ];
    await w.serfHubNotifications.poll();
    await flush(w);
    assert(
      w.__notifications.length === 1,
      "expected exactly 1 notification (only b's transition), got: " +
        w.__notifications.length
    );
    assert(
      (w.__notifications[0].title || "").indexOf("Was Idle") !== -1,
      "expected b to fire, got: " + w.__notifications[0].title
    );
  }

  console.log("notifications.js tests passed");
  process.exit(0);
})();
