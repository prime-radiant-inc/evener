// Model-switching header chip: picker open/populate, thread/model/set with
// first-slash split, live thread/model/changed updates, run-state disable,
// AppWire error handling, model/list fetch-failure error state, and the
// sidebar resync whitelist (task 7, kata model-switching).
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const modelSwitchSrc = fs.readFileSync(path.resolve(__dirname, "../assets/model-switch.js"), "utf8");
const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const flush = () => new Promise((r) => setTimeout(r, 0));

function workspaceHTML(opts) {
  opts = opts || {};
  const state = opts.state || "idle";
  const activeTurnId = opts.activeTurnId || "";
  const disabledAttr = opts.disabled ? " disabled" : "";
  return `
    <div class="conversation" id="conversation" data-session-id="01SESSION"
         data-active-turn-id="${activeTurnId}" data-state="${state}"></div>
    <div class="composer-model">
      <button type="button" class="composer-model-value" data-model-trigger title="anthropic/claude-opus-4-7"${disabledAttr}>
        <span data-model-display>anthropic/claude-opus-4-7</span><span class="caret">▾</span>
      </button>
      <span class="composer-effort-value" data-effort-display hidden></span>
    </div>`;
}

function makeWindow(html) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>${html}</body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/01SESSION",
  });
  return dom.window;
}

// ---------- (a)/(b) picker opens from model/list, grouped by provider, and
// selecting an entry calls thread/model/set with the first-slash split ----------
async function scenarioOpenAndSelect() {
  const window = makeWindow(workspaceHTML());
  const models = [
    { provider: "anthropic", model: "claude-opus-4-7", display_name: "Opus 4.7" },
    { provider: "anthropic", model: "claude-haiku-4-5", display_name: "Haiku 4.5" },
    { provider: "openrouter", model: "vendor/some-model", display_name: "OR Model" },
  ];
  let setCall = null;
  window.SerfAppwire = {
    listModels: () => Promise.resolve(models),
    setModel: (sessionId, raw) => { setCall = { sessionId, raw }; return Promise.resolve({}); },
    onNotification: () => () => {},
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  window.document.querySelector("[data-model-trigger]").click();
  await flush();

  const providers = Array.from(window.document.querySelectorAll(".chip-picker-provider")).map((p) => p.textContent);
  pass(providers.includes("anthropic") && providers.includes("openrouter"), "picker groups by provider, got " + JSON.stringify(providers));

  const rows = Array.from(window.document.querySelectorAll(".chip-picker-model"));
  pass(rows.some((r) => r.classList.contains("active")), "current model is marked active in the picker");

  // Switch to the openrouter tab and pick the vendor-prefixed model.
  const orTab = Array.from(window.document.querySelectorAll(".chip-picker-provider")).find((p) => p.textContent === "openrouter");
  orTab.click();
  await flush();
  const orRow = window.document.querySelector(".chip-picker-model");
  orRow.click();
  await flush();

  pass(!!setCall, "selecting a model calls setModel");
  pass(setCall.sessionId === "01SESSION", "setModel called with the session id");
  pass(setCall.raw === "openrouter/vendor/some-model", "setModel receives provider/model with remaining slashes intact, got " + (setCall && setCall.raw));
  pass(!window.document.querySelector(".model-switch-picker"), "picker closes after selection");
}

// splitProviderModel (loaded from the real appwire.js) must keep only the
// FIRST slash as the provider boundary — proving (b)'s openrouter claim end
// to end through the real wire-shaping code, not a reimplementation.
class FakeWebSocket {
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.OPEN;
    this.listeners = new Map();
    setTimeout(() => this.dispatch("open", {}), 0);
  }
  addEventListener(name, handler) {
    const handlers = this.listeners.get(name) || [];
    handlers.push(handler);
    this.listeners.set(name, handlers);
  }
  send(raw) {
    const msg = JSON.parse(raw);
    FakeWebSocket.sent.push(msg);
    setTimeout(() => {
      this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: { serverInfo: { name: "fake" } } }) });
    }, 0);
  }
  dispatch(name, event) {
    for (const handler of this.listeners.get(name) || []) handler(event);
  }
}
FakeWebSocket.OPEN = 1;

async function scenarioFirstSlashSplitAppwire() {
  FakeWebSocket.sent = [];
  const context = {
    window: { addEventListener() {}, location: { protocol: "http:", host: "127.0.0.1:9180", pathname: "/s/01SESSION" } },
    document: { addEventListener() {}, querySelector() { return null; }, body: { dataset: {} } },
    WebSocket: FakeWebSocket,
    fetch: async () => ({ ok: true, json: async () => ({}) }),
    console,
    setTimeout,
  };
  context.globalThis = context;
  vm.createContext(context);
  vm.runInContext(appwireSrc, context);

  context.window.SerfAppwire.setModel("01SESSION", "openrouter/vendor/some-model");
  await new Promise((r) => setTimeout(r, 20));

  const sent = FakeWebSocket.sent.find((m) => m.method === "thread/model/set");
  pass(!!sent, "setModel sends thread/model/set");
  pass(sent && sent.params.modelProvider === "openrouter", "provider is the instance name (before first slash)");
  pass(sent && sent.params.model === "vendor/some-model", "model keeps remaining slashes, got " + JSON.stringify(sent && sent.params));
}

// ---------- (c) thread/model/changed updates the chip live, no thread re-read,
// and re-keys the cached effort levels ----------
async function scenarioModelChangedNotification() {
  const window = makeWindow(workspaceHTML());
  let handler = null;
  let readThreadCalled = false;
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    onNotification: (h) => { handler = h; return () => {}; },
    refForSession: (id) => "local:" + id,
    readThread: () => { readThreadCalled = true; return Promise.resolve({}); },
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();
  readThreadCalled = false; // init()'s own cold-attach snapshot read doesn't count

  pass(typeof handler === "function", "model-switch.js subscribes to notifications");

  handler("thread/model/changed", {
    threadId: "01SESSION",
    ref: "local:01SESSION",
    modelProvider: "anthropic",
    model: "claude-haiku-4-5",
    reasoningEffortLevels: ["low", "high"],
    supportsReasoning: true,
  });

  const display = window.document.querySelector("[data-model-display]");
  pass(display.textContent.includes("claude-haiku-4-5") || display.textContent === "anthropic/claude-haiku-4-5",
    "chip display updates from the notification, got " + display.textContent);
  pass(!readThreadCalled, "chip update does not trigger a thread re-read");

  const effortState = window.SerfModelSwitch.getEffortState();
  pass(JSON.stringify(effortState.levels) === JSON.stringify(["low", "high"]), "cached effort levels re-key to the new model's levels, got " + JSON.stringify(effortState.levels));
  pass(effortState.supportsReasoning === true, "supportsReasoning re-keys from the notification");
}

// ---------- (task 8 a/d) cold-attach: with NO prior notification, init()
// reads the thread snapshot and renders both the effort chip and the cached
// effort vocabulary from reasoningEffort/reasoningEffortLevels/supportsReasoning
// (never from /api/models or appwire model/list). ----------
async function scenarioColdAttachEffortSnapshot() {
  const window = makeWindow(workspaceHTML());
  let readThreadCalls = 0;
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    onNotification: () => () => {},
    refForSession: (id) => "local:" + id,
    readThread: (sessionId, includeTurns, subscribe) => {
      readThreadCalls++;
      pass(!includeTurns, "cold-attach snapshot read does not request turns");
      pass(!subscribe, "cold-attach snapshot read does not itself subscribe");
      return Promise.resolve({
        thread: {
          serf: {
            reasoningEffort: "high",
            reasoningEffortLevels: ["low", "medium", "high"],
            supportsReasoning: true,
          },
        },
      });
    },
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  pass(readThreadCalls === 1, "init() fetches the thread snapshot exactly once, got " + readThreadCalls);
  const chip = window.document.querySelector("[data-effort-display]");
  pass(!!chip && !chip.hidden, "effort chip is visible after cold-attach snapshot load");
  pass(chip && chip.textContent === "high", "effort chip shows the snapshot's reasoningEffort with no prior notification, got " + (chip && chip.textContent));

  const effortState = window.SerfModelSwitch.getEffortState();
  pass(JSON.stringify(effortState.levels) === JSON.stringify(["low", "medium", "high"]), "cold-attach seeds the effort levels from the snapshot, got " + JSON.stringify(effortState.levels));
  pass(effortState.supportsReasoning === true, "cold-attach seeds supportsReasoning from the snapshot");
}

// ---------- (task 8 b) level-list semantics port spawn.js:1608-1633 ----------
async function scenarioEffortLevelListSemantics() {
  const window = makeWindow(workspaceHTML());
  let handler = null;
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    onNotification: (h) => { handler = h; return () => {}; },
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  // Known non-reasoning model: supportsReasoning === false => NO options.
  handler("thread/model/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", modelProvider: "openrouter", model: "no-reasoning-model",
    reasoningEffortLevels: [], supportsReasoning: false,
  });
  pass(JSON.stringify(window.SerfModelSwitch.effortLevels()) === "[]", "supportsReasoning === false yields a known-empty ladder, got " + JSON.stringify(window.SerfModelSwitch.effortLevels()));

  // Unknown model: absent/empty levels but supportsReasoning true => fall
  // back to the full vocabulary (same fallback list as spawn.js:1605).
  handler("thread/model/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", modelProvider: "custom", model: "unknown-model",
    reasoningEffortLevels: [], supportsReasoning: true,
  });
  pass(JSON.stringify(window.SerfModelSwitch.effortLevels()) === JSON.stringify(["minimal", "low", "medium", "high"]),
    "absent ladder on an unknown-but-reasoning model falls back to the full vocabulary, got " + JSON.stringify(window.SerfModelSwitch.effortLevels()));

  // Known model with an explicit ladder: exactly that ladder, nothing more.
  handler("thread/model/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", modelProvider: "anthropic", model: "claude-haiku-4-5",
    reasoningEffortLevels: ["low", "high"], supportsReasoning: true,
  });
  pass(JSON.stringify(window.SerfModelSwitch.effortLevels()) === JSON.stringify(["low", "high"]),
    "a known model lists exactly its own levels, got " + JSON.stringify(window.SerfModelSwitch.effortLevels()));
}

// ---------- (task 8 c) after thread/model/changed the search.js palette
// effort picker re-keys to the new model's levels, not a hardcoded vocabulary ----------
async function scenarioPaletteEffortReKeys() {
  const searchSrc = fs.readFileSync(path.resolve(__dirname, "../assets/search.js"), "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <dialog id="search-dialog"><div class="search-dialog-inner"><header class="search-dialog-header">
    <input id="search-input" type="text"></header><div id="search-results"></div></div></dialog>
    ${workspaceHTML()}
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/01SESSION" });
  const { window } = dom;
  const dlg = window.document.getElementById("search-dialog");
  dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
  dlg.close = function () { this.removeAttribute("open"); this.open = false; };
  let handler = null;
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    setReasoningEffort: () => Promise.resolve({}),
    onNotification: (h) => { handler = h; return () => {}; },
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.eval(searchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  handler("thread/model/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", modelProvider: "anthropic", model: "claude-haiku-4-5",
    reasoningEffortLevels: ["low", "high"], supportsReasoning: true,
  });

  const commands = window.SerfSearch._commands();
  const effortCmd = commands.find((c) => c.id === "reasoning-effort");
  const options = await Promise.resolve(effortCmd.args.source());
  const ids = options.map((o) => o.id).filter((id) => id !== "");
  pass(JSON.stringify(ids) === JSON.stringify(["low", "high"]),
    "the live effort picker lists exactly the current model's levels (G8), got " + JSON.stringify(ids));
}

// ---------- (task 8 d) thread/reasoning-effort/changed updates the chip live ----------
async function scenarioReasoningEffortChangedUpdatesChip() {
  const window = makeWindow(workspaceHTML());
  let handler = null;
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    onNotification: (h) => { handler = h; return () => {}; },
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  handler("thread/reasoning-effort/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", reasoningEffort: "xhigh",
  });

  const chip = window.document.querySelector("[data-effort-display]");
  pass(chip && chip.textContent === "xhigh", "effort chip updates from thread/reasoning-effort/changed, got " + (chip && chip.textContent));
  pass(chip && !chip.hidden, "effort chip is visible once an effort value is known");
}

// ---------- (d) trigger disabled + palette refusal while Status.Type=="active" ----------
async function scenarioRunStateDisable() {
  const window = makeWindow(workspaceHTML({ state: "idle", activeTurnId: "" }));
  let handler = null;
  window.SerfAppwire = {
    listModels: () => Promise.resolve([]),
    setModel: () => Promise.resolve({}),
    onNotification: (h) => { handler = h; return () => {}; },
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  const trigger = window.document.querySelector("[data-model-trigger]");
  pass(!trigger.disabled, "trigger starts enabled while idle");

  handler("turn/started", { turnId: "T1", ref: "local:01SESSION" });
  handler("thread/status/changed", { status: { type: "active" }, ref: "local:01SESSION" });
  pass(trigger.disabled, "trigger disables once Status.Type==active and ActiveTurnID is set");

  trigger.click();
  await flush();
  pass(!window.document.querySelector(".model-switch-picker"), "disabled trigger click does not open the picker");

  handler("turn/completed", { turnId: "T1", ref: "local:01SESSION" });
  pass(!trigger.disabled, "trigger re-enables once the turn completes");
}

// (d, continued) — the palette "Switch model" action also refuses while busy.
async function scenarioPaletteRefusesWhileBusy() {
  const searchSrc = fs.readFileSync(path.resolve(__dirname, "../assets/search.js"), "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <dialog id="search-dialog"><div class="search-dialog-inner"><header class="search-dialog-header">
    <input id="search-input" type="text"></header><div id="search-results"></div></div></dialog>
    <div class="conversation" id="conversation" data-session-id="01SESSION" data-active-turn-id="T1" data-state="active"></div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/01SESSION" });
  const { window } = dom;
  const dlg = window.document.getElementById("search-dialog");
  dlg.showModal = function () { this.setAttribute("open", ""); this.open = true; };
  dlg.close = function () { this.removeAttribute("open"); this.open = false; };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ live: [], past: [] }) });
  window.eval(searchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  const commands = window.SerfSearch && window.SerfSearch._commands ? window.SerfSearch._commands() : null;
  pass(!!commands, "search.js exposes its command registry for testing");
  const modelCmd = commands && commands.find((c) => c.id === "model");
  pass(!!modelCmd, "the model palette command exists");
  const result = modelCmd.args.run({ sessionId: "01SESSION" }, { id: "anthropic/claude-haiku-4-5", label: "haiku" });
  const resolved = await Promise.resolve(result);
  pass(resolved && resolved.paletteBlocked === true, "palette model command refuses while a turn is active, got " + JSON.stringify(resolved));
}

// ---------- (e) AppWire error renders a notice; chip stays unchanged ----------
async function scenarioSetModelError() {
  const window = makeWindow(workspaceHTML());
  let toastMessage = null;
  window.SerfToast = { show: (msg, kind) => { toastMessage = { msg, kind }; } };
  window.SerfAppwire = {
    listModels: () => Promise.resolve([{ provider: "anthropic", model: "claude-haiku-4-5", display_name: "Haiku" }]),
    setModel: () => Promise.reject(Object.assign(new Error("model not launchable: missing credentials"), {})),
    onNotification: () => () => {},
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  window.document.querySelector("[data-model-trigger]").click();
  await flush();
  window.document.querySelector(".chip-picker-model").click();
  await flush();

  pass(toastMessage && /missing credentials/.test(toastMessage.msg), "server error message surfaces as a notice, got " + JSON.stringify(toastMessage));
  pass(toastMessage && toastMessage.kind === "error", "notice is an error-kind toast");
  const display = window.document.querySelector("[data-model-display]");
  pass(display.textContent === "anthropic/claude-opus-4-7", "chip is unchanged after a failed set, got " + display.textContent);
}

// ---------- (f) failed model/list fetch renders an error state, not empty ----------
async function scenarioModelListFetchError() {
  const window = makeWindow(workspaceHTML());
  window.SerfAppwire = {
    listModels: () => Promise.reject(new Error("network down")),
    setModel: () => Promise.resolve({}),
    onNotification: () => () => {},
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  window.document.querySelector("[data-model-trigger]").click();
  await flush();

  const errEl = window.document.querySelector(".chip-picker-error");
  pass(!!errEl, "a failed model/list fetch renders a visible error state in the picker");
  pass(window.document.querySelectorAll(".chip-picker-model").length === 0, "no silent empty model list is rendered");
}

// ---------- (g) thread/model/changed is in the sidebar resync whitelist ----------
function scenarioSidebarQualifying() {
  const match = sidebarSrc.match(/var QUALIFYING = \{([^}]*)\}/);
  pass(!!match, "sidebar.js still defines a QUALIFYING literal");
  pass(!!match && /["']?thread\/model\/changed["']?\s*:\s*1/.test(match[1]), "thread/model/changed is added to the sidebar resync whitelist");
}

// ---------- eventsFromNotification maps the two new notifications ----------
function scenarioEventsFromNotificationMapping() {
  const context = {
    window: { addEventListener() {}, location: { protocol: "http:", host: "127.0.0.1:9180", pathname: "/s/01SESSION" } },
    document: { addEventListener() {}, querySelector() { return null; }, body: { dataset: {} } },
    WebSocket: function () {},
    fetch: async () => ({ ok: true, json: async () => ({}) }),
    console,
  };
  context.globalThis = context;
  vm.createContext(context);
  vm.runInContext(appwireSrc, context);

  const modelEvents = context.window.SerfAppwire.eventsFromNotification("thread/model/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", modelProvider: "anthropic", model: "claude-haiku-4-5",
    reasoningEffortLevels: ["low", "high"], supportsReasoning: true,
  });
  pass(modelEvents.length === 1 && modelEvents[0][0] === "MODEL_CHANGED", "thread/model/changed maps to a MODEL_CHANGED event, got " + JSON.stringify(modelEvents));
  pass(modelEvents[0][1].model === "claude-haiku-4-5", "MODEL_CHANGED carries the model field");

  const effortEvents = context.window.SerfAppwire.eventsFromNotification("thread/reasoning-effort/changed", {
    threadId: "01SESSION", ref: "local:01SESSION", reasoningEffort: "high",
  });
  pass(effortEvents.length === 1 && effortEvents[0][0] === "REASONING_EFFORT_CHANGED", "thread/reasoning-effort/changed maps to a REASONING_EFFORT_CHANGED event, got " + JSON.stringify(effortEvents));
  pass(effortEvents[0][1].reasoningEffort === "high", "REASONING_EFFORT_CHANGED carries the reasoningEffort field");
}

// ---------- (h) htmx:afterSwap resyncs busy state + picker/cache to the
// freshly server-rendered NEW session, instead of staying keyed to the OLD
// session's stale busy state ----------
async function scenarioHtmxAfterSwapResync() {
  // Start on a BUSY session (mid-turn): trigger should be disabled.
  const window = makeWindow(workspaceHTML({ state: "active", activeTurnId: "T1" }));
  let listModelsCalls = 0;
  window.SerfAppwire = {
    listModels: () => { listModelsCalls++; return Promise.resolve([{ provider: "anthropic", model: "claude-haiku-4-5", display_name: "Haiku" }]); },
    setModel: () => Promise.resolve({}),
    onNotification: () => () => {},
    refForSession: (id) => "local:" + id,
  };
  window.eval(modelSwitchSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  let trigger = window.document.querySelector("[data-model-trigger]");
  pass(trigger.disabled, "trigger starts disabled on the busy session");

  // Navigate (sidebar htmx swap) to an IDLE session — simulate htmx replacing
  // #workspace/#conversation with the new session's server-rendered DOM.
  window.document.body.innerHTML = workspaceHTML({ state: "idle", activeTurnId: "" });
  window.document.body.dispatchEvent(new window.Event("htmx:afterSwap", { bubbles: true }));
  await flush();

  trigger = window.document.querySelector("[data-model-trigger]");
  pass(!trigger.disabled, "after swapping to an idle session, trigger resyncs to enabled");

  // Open the picker on the idle session, then swap to a BUSY session — the
  // picker must close rather than staying open over stale content, and the
  // model cache must be dropped so the new session's picker refetches.
  trigger.click();
  await flush();
  pass(!!window.document.querySelector(".model-switch-picker"), "picker opens on the idle session");
  const callsBeforeSwap = listModelsCalls;

  window.document.body.innerHTML = workspaceHTML({ state: "active", activeTurnId: "T2" });
  window.document.body.dispatchEvent(new window.Event("htmx:afterSwap", { bubbles: true }));
  await flush();

  pass(!window.document.querySelector(".model-switch-picker"), "picker closes across the swap to a busy session");
  trigger = window.document.querySelector("[data-model-trigger]");
  pass(trigger.disabled, "after swapping to a busy session, trigger resyncs to disabled");

  // Swap back to idle and open the picker again: cache must have been
  // cleared by the swap, so listModels is called again (not served stale).
  window.document.body.innerHTML = workspaceHTML({ state: "idle", activeTurnId: "" });
  window.document.body.dispatchEvent(new window.Event("htmx:afterSwap", { bubbles: true }));
  await flush();
  trigger = window.document.querySelector("[data-model-trigger]");
  trigger.click();
  await flush();
  pass(listModelsCalls > callsBeforeSwap, "model cache is cleared across the swap, so the picker refetches on next open");
}

(async () => {
  await scenarioOpenAndSelect();
  await scenarioFirstSlashSplitAppwire();
  await scenarioModelChangedNotification();
  await scenarioColdAttachEffortSnapshot();
  await scenarioEffortLevelListSemantics();
  await scenarioPaletteEffortReKeys();
  await scenarioReasoningEffortChangedUpdatesChip();
  await scenarioRunStateDisable();
  await scenarioPaletteRefusesWhileBusy();
  await scenarioSetModelError();
  await scenarioModelListFetchError();
  await scenarioHtmxAfterSwapResync();
  scenarioSidebarQualifying();
  scenarioEventsFromNotificationMapping();

  if (failures.length === 0) {
    console.log("PASS: model-switch header chip picker, live updates, run-state disable");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
