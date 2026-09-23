#!/usr/bin/env node
import { spawn } from "node:child_process";
// settingshostguard — the settings UI's live acceptance driver for the
// multi-host remote-admin work (component 07b): drives the PRODUCTION hub web
// app (the hub's embedded frontend/dist) in real headless Chrome against a real
// hub's /auth/<token> URL, with a REMOTE host selected, and asserts that each
// host-scoped settings pane renders THAT HOST's own data - never the
// controller's - then performs one write through the UI (AGENTS.md Save).
//
// Like scripts/skillguard/run.mjs, this driver never starts Vite: the hub's
// embedded production frontend IS the app under test and --url is the hub's own
// /auth/<token> URL, so the page and the hub share one origin (the hub's
// WebSocket Accept refuses a cross-origin upgrade, so a same-origin page is what
// lets the app open /rpc at all).
//
// The host-side proof (the write landed in the disposable host root, and the
// host's real files are byte-identical) lives in the Go owner
// (cmd/evener-hub/app_host_settings_ui_e2e_test.go), not here: this driver's
// own echo only proves what the browser rendered.
//
// Fixture vocabulary is supplied by the Go owner as a JSON file (--expect) so
// this driver carries no hardcoded host/controller values; every sentinel it
// requires and every controller value it must NOT see comes from that file.
//
// Options:
//   --url URL            the hub's /auth/<token> URL to open first (required)
//   --artifact-dir DIR   screenshots and result JSON (required)
//   --host NAME          the remote host name the Go owner registered (required)
//   --expect FILE        JSON fixture of seeded host/controller values (required)
//   --help               show this help
import { appendFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { connectPage, devtoolsHttpURL, evaluate, navigateTo, waitForHttp } from "../browserGuardCdp.mjs";
import {
  chromeProfileEnvironment,
  chromeProfileIsolationArgs,
  createBrowserProcessCleanup,
  describeBrowserStartupFailure,
  findChrome,
  parseChromeDevToolsAnnouncement,
  requestBrowserClose,
} from "../browserGuardProcess.mjs";

const PROFILE_PREFIX = "settingshostguard-chrome-";

// makeChromeProfileDir creates the browser profile under a SHORT root.
//
// Chrome puts its process-singleton socket at
// <user-data-dir>/com.google.Chrome.<id>/SingletonSocket, and a unix socket path
// is capped at about 108 bytes. Taking the ambient temp dir on trust is not safe
// here: a nested TMPDIR - a sandbox's own scratch directory, or the temp root a
// test harness hands its children - pushes that path past the cap, and Chrome
// aborts before DevTools is ready. The harness would then report it honestly as
// an environment problem, but it is this layout that caused it, so prefer the
// conventional short temp root and fall back to the ambient one only when the
// short root is missing or unwritable.
function makeChromeProfileDir() {
  // Leaves room for Chrome's own ~35-byte singleton suffix under the 108-byte cap.
  const budget = 60;
  const roots = process.platform === "win32" ? [tmpdir()] : ["/tmp", tmpdir()];
  for (const root of [...new Set(roots)]) {
    if (root.length + PROFILE_PREFIX.length + 6 > budget) continue;
    try {
      return mkdtempSync(path.join(root, PROFILE_PREFIX));
    } catch {
      // Unwritable or absent: try the next root.
    }
  }
  return mkdtempSync(path.join(tmpdir(), PROFILE_PREFIX));
}

const DEVTOOLS_ANNOUNCEMENT_PREFIX = "DevTools listening on ";
const CHILD_EXIT_GRACE_MS = 2_000;
const VIEWPORT = { width: 1440, height: 1000 };

// The settings panes the harness visits, in tour order. "project" is not a
// settings-nav row (it needs ?cwd=) but is a valid dispatch target; it is
// visited only when the fixture supplies a cwd.
const PANES = [
  "credentials",
  "agents-md",
  "launch-evener",
  "inrepo",
  "project",
  "plugins-manager",
  "plugins",
  "skills",
  "mcp",
];

// The app shell marker: the rail brand renders once AppShell has mounted, so a
// settings deep link is booted exactly when it is present.
const APP_SHELL_EXPR = `(() => document.querySelector("[data-testid='rail-brand']") !== null ? true : null)()`;
// The settings pane's own content region (Settings.tsx). Scoping the text
// probes to it keeps the always-present settings nav (whose labels name
// sections, not data) out of the assertions.
const SETTINGS_CONTENT_EXPR = `(() => document.querySelector("[data-testid='settings-content']") !== null ? true : null)()`;
const SETTINGS_TEXT_EXPR = `(() => { const el = document.querySelector("[data-testid='settings-content']"); return (el ?? document.body).innerText; })()`;

// hostSelectExpr locates the settings route's shared host picker by its
// ACCESSIBLE NAME, not by an id or testid: the FormRow label is "Host" and the
// Select is associated with it, so el.labels carries it. There is deliberately
// no data-testid on this control (HostPicker.tsx); if this probe ever stops
// finding it, that is a finding about the delivered UI, not a reason to add
// one here.
const HOST_SELECT_EXPR = `(() => {
  const selects = [...document.querySelectorAll("select")];
  return selects.find((s) => {
    const labels = [...(s.labels ?? [])].map((l) => (l.textContent ?? "").trim());
    return labels.includes("Host");
  }) ?? null;
})()`;

// HOST_SELECT_PRESENT_EXPR is that same probe reduced to a serializable boolean.
// waitPage evaluates with returnByValue, and a DOM ELEMENT does not survive that
// transfer - CDP omits `value` for a node - so an element-returning probe is
// `undefined` on every poll and can never satisfy the wait, however long it
// polls: it reports "the picker never appeared" while the picker is on screen.
// Presence waits therefore use this form; the element itself is only ever
// consumed INSIDE the page (selectHost's own expression), never returned.
const HOST_SELECT_PRESENT_EXPR = `(() => (${HOST_SELECT_EXPR}) !== null ? true : null)()`;

// labeledInputExpr locates a text input by the FormRow label that names it, the
// same way HOST_SELECT_EXPR locates the Host picker: the launch form's text
// fields take their accessible name from a <label htmlFor>, and widgets/input
// emits no aria-label of its own, so an [aria-label] probe would find nothing.
// Probing by label association is what the product actually renders; if this
// ever stops finding a field, that is a finding about the delivered UI, not a
// reason to add a testid here.
function labeledInputExpr(label) {
  return `(() => {
    const inputs = [...document.querySelectorAll("input")];
    return inputs.find((i) => {
      const labels = [...(i.labels ?? [])].map((l) => (l.textContent ?? "").trim());
      return labels.includes(${JSON.stringify(label)});
    }) ?? null;
  })()`;
}

function usage() {
  return [
    "Usage: node scripts/settingshostguard/run.mjs --url URL --artifact-dir DIR --host NAME --expect FILE",
    "",
    "Drives the production evener-hub settings UI in headless Chrome against a real",
    "hub URL with a remote host selected, asserting each host-scoped pane renders",
    "that host's own data and performing one write (AGENTS.md Save) through the UI.",
    "",
    "Options:",
    "  --url URL            the hub's /auth/<token> URL to open first (required)",
    "  --artifact-dir DIR   directory for screenshots and the result JSON (required)",
    "  --host NAME          the remote host name the Go owner registered (required)",
    "  --expect FILE        JSON fixture of seeded host/controller values (required)",
    "  --help               show this help",
  ].join("\n");
}

function parseArgs(argv) {
  const out = { help: false, values: new Map() };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") {
      out.help = true;
      return out;
    }
    const eq = arg.indexOf("=");
    if (arg.startsWith("--") && eq > 0) {
      out.values.set(arg.slice(2, eq), arg.slice(eq + 1));
      continue;
    }
    if (arg.startsWith("--")) {
      const value = argv[i + 1];
      if (value === undefined || value.startsWith("--")) {
        throw new Error(`${arg} requires a value`);
      }
      out.values.set(arg.slice(2), value);
      i++;
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }
  return out;
}

function check(condition, message) {
  if (!condition) throw new Error(message);
}

class Driver {
  constructor({ url, artifactDir, host, expect }) {
    this.url = url;
    this.artifactDir = artifactDir;
    this.host = host;
    this.expect = expect;
    this.origin = new URL(url).origin;
    this.failures = [];
    this.panes = [];
    this.chromeBinary = null;
    this.chromeArgv = [];
    this.chromeStderr = "";
    this.page = null;
    this.lifecycle = null;
    this.profileDir = null;
    this.chrome = null;
    this.endpoint = null;
  }

  async start() {
    this.chromeBinary = findChrome();
    this.profileDir = makeChromeProfileDir();
    this.lifecycle = createBrowserProcessCleanup({ profileDir: this.profileDir });
    this.chromeArgv = [
      "--headless=new",
      "--disable-gpu",
      ...chromeProfileIsolationArgs(),
      "--remote-debugging-port=0",
      `--user-data-dir=${this.profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      `--window-size=${VIEWPORT.width},${VIEWPORT.height}`,
      "about:blank",
    ];
    this.chrome = spawn(this.chromeBinary, this.chromeArgv, {
      stdio: ["ignore", "ignore", "pipe"],
      env: chromeProfileEnvironment(this.profileDir),
      detached: process.platform !== "win32",
    });
    const chromePgid = process.platform !== "win32" && Number.isInteger(this.chrome.pid) ? this.chrome.pid : null;
    this.lifecycle.addChild(this.chrome, {
      processGroupId: chromePgid,
      gracefulClose: () => requestBrowserClose(this.endpoint),
    });
    let lineBuffer = "";
    const announced = new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`chrome never announced its DevTools endpoint after ${CHILD_EXIT_GRACE_MS * 5}ms`)),
        CHILD_EXIT_GRACE_MS * 5,
      );
      const finish = (fn, value) => {
        clearTimeout(timer);
        fn(value);
      };
      this.chrome.once("exit", (code, signal) => {
        finish(
          reject,
          new Error(`chrome exited before DevTools readiness (code ${code ?? "?"}, signal ${signal ?? "none"})`),
        );
      });
      this.chrome.once("error", (error) => finish(reject, error));
      this.chrome.stderr.on("data", (chunk) => {
        this.chromeStderr += chunk;
        lineBuffer += chunk.toString();
        const lines = lineBuffer.split(/\r\n|\r|\n/);
        lineBuffer = lines.pop() ?? "";
        for (const line of lines) {
          if (!line.startsWith(DEVTOOLS_ANNOUNCEMENT_PREFIX)) continue;
          try {
            const endpoint = parseChromeDevToolsAnnouncement(line);
            if (endpoint) finish(resolve, endpoint);
          } catch (error) {
            finish(reject, error);
          }
        }
      });
    });
    this.endpoint = await announced;
    await waitForHttp(devtoolsHttpURL(this.endpoint, "/json/version"), "chrome devtools endpoint");
    this.page = await connectPage(this.endpoint);
    await this.page.send("Page.enable").catch(() => {});
    await this.page.send("Runtime.enable").catch(() => {});
    await this.page.send("Network.enable").catch(() => {});
    // Authenticate once: the /auth/<token> URL sets the hub's session cookie
    // and redirects into the app. Every later same-origin navigation carries it.
    await navigateTo(this.page, this.url);
  }

  // assertBuiltShell fails with a clear message when the hub is serving the
  // placeholder/half-built 503 rather than the SPA, then waits for the app
  // shell. It is deliberately separate from start(): a placeholder dist is a
  // fact about the HUB, not a browser-startup fault, so it must not be reported
  // through the browser-startup description.
  async assertBuiltShell() {
    const landed = await evaluate(this.send, "document.body ? document.body.innerText : ''").catch(() => "");
    if (typeof landed === "string" && landed.includes("web app not built")) {
      throw new Error(
        "the hub is serving the placeholder dist (webnext.go's 503), not the built SPA; " +
          "build the frontend first (`make build-web`, or `npm run build` in cmd/evener-hub/frontend) and rebuild the hub, then rerun",
      );
    }
    await this.waitPage(APP_SHELL_EXPR, { timeoutMs: 30000, label: "app shell (rail brand)" });
  }

  get send() {
    return this.page.send;
  }

  async stop() {
    try {
      this.page?.close();
    } catch {}
    if (this.lifecycle) await this.lifecycle.cleanup();
  }

  async screenshot(name) {
    try {
      const response = await this.send("Page.captureScreenshot", { format: "png" });
      writeFileSync(path.join(this.artifactDir, `${name}.png`), Buffer.from(response.result.data, "base64"));
    } catch (error) {
      appendFileSync(path.join(this.artifactDir, "screenshots.log"), `${name}: ${error}\n`);
    }
  }

  // waitPage polls one expression until it is truthy, throwing on timeout with
  // the settings text the page actually showed.
  async waitPage(exprSource, { timeoutMs = 15000, label } = {}) {
    const deadline = Date.now() + timeoutMs;
    let last = null;
    for (;;) {
      last = await evaluate(this.send, exprSource).catch(() => null);
      if (last !== null && last !== undefined && last !== false) return last;
      if (Date.now() > deadline) {
        const text = await evaluate(this.send, SETTINGS_TEXT_EXPR).catch(() => "<unreadable>");
        throw new Error(`${label ?? exprSource} did not hold within ${timeoutMs}ms; settings text was:\n${text}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }

  // settingsText is the visible text of the settings content region.
  settingsText() {
    return evaluate(this.send, SETTINGS_TEXT_EXPR);
  }

  // settingsTextWithValues is settingsText plus the current value of every form
  // control inside the region. innerText does not include an input's or a
  // textarea's value, so a negative assertion made on innerText alone cannot
  // see a controller value that a pane rendered inside a field - which is
  // exactly the mistake those assertions exist to catch.
  //
  // The join separator is written `\\n` because this string is a template
  // literal: a bare `\n` here becomes a REAL newline in the expression the page
  // receives, which splits the string literal across two lines and the page
  // answers with a SyntaxError instead of a value.
  settingsTextWithValues() {
    return evaluate(
      this.send,
      `(() => {
        const el = document.querySelector("[data-testid='settings-content']");
        if (el === null) return document.body.innerText;
        const values = [...el.querySelectorAll("input, textarea, select")].map((c) => c.value ?? "");
        return [el.innerText, ...values].join("\\n");
      })()`,
    );
  }

  // hostSelectState reads the "Host" picker's presence and current value. The
  // value names the host the route selected, so it is the per-pane proof that
  // the route carried the selection into this pane.
  hostSelectState() {
    return evaluate(
      this.send,
      `(() => { const s = ${HOST_SELECT_EXPR}; return s === null ? null : { value: s.value, options: [...s.options].map((o) => ({ value: o.value, label: o.textContent })) }; })()`,
    );
  }

  // selectHost drives the picker the way a user does: it first waits for the
  // option to exist (the picker fetches the host registry asynchronously on
  // mount, so a remote option appears only once that resolves - setting the
  // value before then would silently leave the select on its previous option),
  // then sets the native select's value and dispatches the change event React's
  // onChange listens to. The app then rewrites the route (selectHost ->
  // navigate), so the URL carries it.
  async selectHost(value) {
    await this.waitPage(
      `(() => { const s = ${HOST_SELECT_EXPR}; return s !== null && [...s.options].some((o) => o.value === ${JSON.stringify(value)}) ? true : null; })()`,
      { label: `Host picker offers an option for ${JSON.stringify(value)}` },
    );
    const ok = await evaluate(
      this.send,
      `(() => {
        const s = ${HOST_SELECT_EXPR};
        if (s === null) return false;
        s.value = ${JSON.stringify(value)};
        s.dispatchEvent(new Event("change", { bubbles: true }));
        return true;
      })()`,
    );
    check(
      ok,
      `the settings Host picker (accessible name "Host") was not found; it is required to select the remote host`,
    );
  }

  // clickSettingsButton clicks the first visible, enabled button in the
  // settings content whose accessible text (textContent or aria-label) is
  // exactly `text`.
  async clickSettingsButton(text) {
    const boxes = await evaluate(
      this.send,
      `(() => {
        const root = document.querySelector("[data-testid='settings-content']");
        if (root === null) return [];
        const matches = [...root.querySelectorAll("button")].filter((b) => {
          const label = (b.getAttribute("aria-label") ?? "").trim() || b.textContent.trim();
          return label === ${JSON.stringify(text)};
        });
        return matches.map((b) => {
          b.scrollIntoView({ block: "center" });
          const r = b.getBoundingClientRect();
          return { x: r.x + r.width / 2, y: r.y + r.height / 2, disabled: b.disabled };
        });
      })()`,
    );
    check(boxes && boxes.length > 0, `no button labeled ${JSON.stringify(text)} in the settings content`);
    check(!boxes[0].disabled, `button ${JSON.stringify(text)} is disabled`);
    await this.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: boxes[0].x, y: boxes[0].y });
    await this.send("Input.dispatchMouseEvent", {
      type: "mousePressed",
      x: boxes[0].x,
      y: boxes[0].y,
      button: "left",
      clickCount: 1,
    });
    await this.send("Input.dispatchMouseEvent", {
      type: "mouseReleased",
      x: boxes[0].x,
      y: boxes[0].y,
      button: "left",
      clickCount: 1,
    });
  }

  // buttonEnabled reports whether the settings button with this accessible
  // text exists and is enabled.
  buttonState(text) {
    return evaluate(
      this.send,
      `(() => {
        const root = document.querySelector("[data-testid='settings-content']");
        if (root === null) return null;
        const b = [...root.querySelectorAll("button")].find((btn) => {
          const label = (btn.getAttribute("aria-label") ?? "").trim() || btn.textContent.trim();
          return label === ${JSON.stringify(text)};
        });
        return b === undefined ? null : { disabled: b.disabled };
      })()`,
    );
  }

  // setTextareaValue writes a controlled React textarea the way the browser
  // does: through the native value setter, then an input event.
  setTextareaValue(ariaLabel, value) {
    return evaluate(
      this.send,
      `(() => {
        const el = document.querySelector("textarea[aria-label=" + JSON.stringify(${JSON.stringify(ariaLabel)}) + "]");
        if (el === null) return null;
        const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set;
        setter.call(el, ${JSON.stringify(value)});
        el.dispatchEvent(new Event("input", { bubbles: true }));
        return el.value;
      })()`,
    );
  }

  textareaValue(ariaLabel) {
    return evaluate(
      this.send,
      `(() => { const el = document.querySelector("textarea[aria-label=" + JSON.stringify(${JSON.stringify(ariaLabel)}) + "]"); return el === null ? null : el.value; })()`,
    );
  }

  inputValue(ariaLabel) {
    return evaluate(
      this.send,
      `(() => { const el = document.querySelector("input[aria-label=" + JSON.stringify(${JSON.stringify(ariaLabel)}) + "]"); return el === null ? null : el.value; })()`,
    );
  }

  settingsURL(section, { cwd = null } = {}) {
    const params = new URLSearchParams();
    params.set("host", this.host);
    if (cwd !== null) params.set("cwd", cwd);
    return `${this.origin}/settings/${section}?${params.toString()}`;
  }

  // openSettingsURL navigates to a settings URL and asserts the Host picker it
  // renders names `expectHostValue`. The picker's value is the per-pane proof
  // that the route carried the selection into this pane.
  async openSettingsURL(url, { expectHostValue = this.host } = {}) {
    // Plain navigation, then waitPage: the settings pane is a lazy route whose
    // content mounts only after the SPA boots and its client connects, so a
    // boot expression evaluated once at the load event would always miss it.
    await navigateTo(this.page, url);
    await this.waitPage(SETTINGS_CONTENT_EXPR, { label: `settings content at ${url}` });
    // The rail is replaced by the settings pane on this route; rail-brand is a
    // marker of the session shell, not of settings, so it is not asserted here.
    const select = await this.waitPage(HOST_SELECT_PRESENT_EXPR, { label: `Host picker at ${url}` }).then(() =>
      this.hostSelectState(),
    );
    check(
      select !== null && select.value === expectHostValue,
      `${url}: the Host picker value = ${JSON.stringify(select?.value)}, want ${JSON.stringify(expectHostValue)}`,
    );
    return select;
  }

  async openPane(section, { cwd = null } = {}) {
    return this.openSettingsURL(this.settingsURL(section, { cwd }));
  }
}

// requireContains asserts the host-scoped pane showed the host's seeded value
// and never the controller's. `what` names the pane field for the failure.
function requireHostValue(text, hostValue, controllerValue, what) {
  check(
    typeof text === "string" && text.includes(hostValue),
    `${what}: the pane did not render the host's seeded value ${JSON.stringify(hostValue)}; text was:\n${text}`,
  );
  if (controllerValue !== undefined && controllerValue !== null) {
    check(
      !text.includes(controllerValue),
      `${what}: the pane rendered the CONTROLLER's value ${JSON.stringify(controllerValue)} while a remote host was selected; text was:\n${text}`,
    );
  }
}

async function runPane(driver, section) {
  const expect = driver.expect;
  const record = { section, ok: false, probe: "", evidence: {} };
  switch (section) {
    case "credentials": {
      // The first pane also exercises the product's own selection gesture: the
      // pane is opened on the LOCAL default with no ?host=, and the Host picker
      // (located by its accessible name) is then driven to the remote host. The
      // route must carry the picked host.
      await driver.openSettingsURL(`${driver.origin}/settings/credentials`, { expectHostValue: "local" });
      await driver.selectHost(driver.host);
      await driver.waitPage(
        `(() => new URLSearchParams(window.location.search).get("host") === ${JSON.stringify(driver.host)} ? true : null)()`,
        { label: "route carries the picked host" },
      );
      const regionLabel = `Providers on ${driver.host}`;
      await driver.waitPage(
        `(() => document.querySelector("[aria-label=" + JSON.stringify(${JSON.stringify(regionLabel)}) + "]") !== null ? true : null)()`,
        { label: `remote credentials region "${regionLabel}"` },
      );
      // The region appears with the pane, but its ROWS come from the selected
      // host over evener/host/request, so the listing arrives after the region
      // does. Read the region only once the host's own instance is in it: every
      // other pane's assertion already waits this way, and reading the instant
      // the region exists asserts against an empty listing.
      const text = await driver.waitPage(
        `(() => {
          const el = document.querySelector("[aria-label=" + JSON.stringify(${JSON.stringify(regionLabel)}) + "]");
          if (el === null) return null;
          return el.innerText.includes(${JSON.stringify(expect.credentials.hostText)}) ? el.innerText : null;
        })()`,
        { label: `the remote pane to list the host's own instance ${JSON.stringify(expect.credentials.hostText)}` },
      );
      requireHostValue(text, expect.credentials.hostText, expect.credentials.controllerText, "credentials");
      record.probe = `Host picker (accessible name "Host") -> option value "${driver.host}"; remote section [aria-label="${regionLabel}"]`;
      record.evidence = { regionText: text };
      record.ok = true;
      return record;
    }
    case "agents-md": {
      const c = expect.agentsMd;
      await driver.openPane(section);
      await driver.waitPage(
        `document.querySelector("textarea[aria-label='AGENTS.md contents']") !== null ? true : null`,
        {
          label: "AGENTS.md editor",
        },
      );
      const loaded = await driver.waitPage(
        `(() => { const el = document.querySelector("textarea[aria-label='AGENTS.md contents']"); return el === null ? null : (el.value.includes(${JSON.stringify(c.hostContent)}) ? el.value : null); })()`,
        { label: "host AGENTS.md content loaded" },
      );
      const text = await driver.settingsTextWithValues();
      check(
        !text.includes(c.controllerContent),
        `agents-md: the controller's AGENTS.md content ${JSON.stringify(c.controllerContent)} appeared while a remote host was selected`,
      );
      // The write: edit the host's file through the UI and Save.
      const typed = await driver.setTextareaValue("AGENTS.md contents", c.writeContent);
      check(typed === c.writeContent, `agents-md: setting the editor value returned ${JSON.stringify(typed)}`);
      await driver.clickSettingsButton("Save");
      await driver.waitPage(
        `(() => { const root = document.querySelector("[data-testid='settings-content']"); if (root === null) return null;
          const b = [...root.querySelectorAll("button")].find((btn) => btn.textContent.trim() === "Save");
          return b !== undefined && b.disabled ? true : null; })()`,
        { timeoutMs: 30000, label: "Save round-tripped (Save button disabled again)" },
      );
      const echoed = await driver.textareaValue("AGENTS.md contents");
      check(
        echoed === c.writeContent,
        `agents-md: after Save the editor showed ${JSON.stringify(echoed)}, want the written value ${JSON.stringify(c.writeContent)}`,
      );
      record.probe = `textarea [aria-label="AGENTS.md contents"] (loaded host content, then the UI write) + button "Save"`;
      record.evidence = { loaded, written: echoed };
      record.write = { pane: section, value: c.writeContent };
      record.ok = true;
      return record;
    }
    case "launch-evener": {
      const c = expect.launchAgent;
      await driver.openPane(section);
      const value = await driver.waitPage(
        `(() => { const el = ${labeledInputExpr("Agent")}; return el === null ? null : (el.value.includes(${JSON.stringify(c.host)}) ? el.value : null); })()`,
        { label: "host launch Agent value" },
      );
      const text = await driver.settingsTextWithValues();
      check(
        !text.includes(c.controller),
        `launch-evener: the controller's launch Agent ${JSON.stringify(c.controller)} appeared while a remote host was selected`,
      );
      record.probe = `input labeled "Agent" (global launch layer)`;
      record.evidence = { agent: value };
      record.ok = true;
      return record;
    }
    case "inrepo": {
      await driver.openPane(section);
      const text = await driver.settingsText();
      check(
        typeof text === "string" && text.length > 0,
        "inrepo: the pane rendered no content with a remote host selected",
      );
      record.probe =
        "Host picker value + pane body (structural: the in-repo pane needs a host-side repo/cwd to show a seeded value)";
      record.evidence = { textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    case "project": {
      const cwd = expect.projectCwd ?? null;
      await driver.openPane(section, { cwd });
      const text = await driver.settingsText();
      check(
        typeof text === "string" && text.length > 0,
        "project: the pane rendered no content with a remote host selected",
      );
      record.probe = `Host picker value + pane body at /settings/project?cwd=${cwd ?? "<none>"}`;
      record.evidence = { cwd, textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    case "plugins-manager": {
      await driver.openPane(section);
      await driver.waitPage(
        `(() => { const root = document.querySelector("[data-testid='settings-content']"); return root !== null && root.innerText.includes("Browse") ? true : null; })()`,
        { label: "plugins-manager segmented control" },
      );
      const text = await driver.settingsText();
      check(
        !/Failed to load/.test(text),
        `plugins-manager: the pane rendered a load error with a remote host selected; text was:\n${text}`,
      );
      record.probe =
        "segmented control (Installed / Browse / Marketplaces) + pane body (structural: the host plugin/marketplace catalog is not file-seedable here)";
      record.evidence = { textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    case "plugins": {
      const c = expect.pluginsDir;
      await driver.openPane(section);
      const text = await driver.waitPage(
        `(() => { const el = document.querySelector("[data-testid='settings-content']"); return el !== null && el.innerText.includes(${JSON.stringify(c.host)}) ? el.innerText : null; })()`,
        { label: "host plugin directory" },
      );
      requireHostValue(text, c.host, c.controller, "plugins");
      record.probe = "plugin directory list (global launch layer plugin_dirs)";
      record.evidence = { textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    case "skills": {
      const c = expect.skillsDir;
      await driver.openPane(section);
      const text = await driver.waitPage(
        `(() => { const el = document.querySelector("[data-testid='settings-content']"); return el !== null && el.innerText.includes(${JSON.stringify(c.host)}) ? el.innerText : null; })()`,
        { label: "host skill directory" },
      );
      requireHostValue(text, c.host, c.controller, "skills");
      record.probe = "skill directory list (global launch layer skills_dirs)";
      record.evidence = { textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    case "mcp": {
      const c = expect.mcpConfig;
      await driver.openPane(section);
      const text = await driver.waitPage(
        `(() => { const el = document.querySelector("[data-testid='settings-content']"); return el !== null && el.innerText.includes(${JSON.stringify(c.host)}) ? el.innerText : null; })()`,
        { label: "host MCP config path" },
      );
      requireHostValue(text, c.host, c.controller, "mcp");
      record.probe = "MCP config-file list (global launch layer mcp_configs)";
      record.evidence = { textExcerpt: text.slice(0, 400) };
      record.ok = true;
      return record;
    }
    default:
      throw new Error(`unknown pane ${section}`);
  }
}

async function runPanes(driver) {
  for (const section of PANES) {
    try {
      const record = await runPane(driver, section);
      driver.panes.push(record);
      await driver.screenshot(`pane-${section}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      driver.failures.push({ section, message });
      driver.panes.push({ section, ok: false, probe: "", evidence: {}, error: message });
      await driver.screenshot(`pane-${section}-failure`);
      throw error;
    }
  }
}

function writeResult(driver) {
  const write = driver.panes.find((p) => p.write)?.write ?? null;
  const result = {
    ok: driver.failures.length === 0 && driver.panes.length === PANES.length,
    host: driver.host,
    url: driver.url,
    at: new Date().toISOString(),
    panes: driver.panes,
    write,
    failures: driver.failures,
  };
  writeFileSync(path.join(driver.artifactDir, "result.json"), `${JSON.stringify(result, null, 2)}\n`);
  return result;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  for (const name of ["url", "artifact-dir", "host", "expect"]) {
    if (!args.values.get(name)) {
      console.error(`missing --${name}\n\n${usage()}`);
      process.exitCode = 1;
      return;
    }
  }
  const artifactDir = args.values.get("artifact-dir");
  mkdirSync(artifactDir, { recursive: true });
  const expect = JSON.parse(readFileSync(args.values.get("expect"), "utf8"));
  const driver = new Driver({
    url: args.values.get("url"),
    artifactDir,
    host: args.values.get("host"),
    expect,
  });
  const signaled = { cleanup: false };
  const handleSignal = (code) => {
    if (signaled.cleanup) process.exit(code);
    signaled.cleanup = true;
    void (async () => {
      try {
        await driver.stop();
      } catch {}
      process.exit(code);
    })();
  };
  process.on("SIGTERM", () => handleSignal(143));
  process.on("SIGINT", () => handleSignal(130));
  try {
    try {
      await driver.start();
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({
          error,
          subsystem: "chrome",
          chromeBinary: driver.chromeBinary,
          chromeArgv: driver.chromeArgv,
          chromeStderr: driver.chromeStderr,
        }),
      );
    }
    // Outside the startup wrapper: a placeholder dist is the hub's fault, not
    // the browser's, and must read as its own failure.
    await driver.assertBuiltShell();
    await runPanes(driver);
    const result = writeResult(driver);
    console.log(
      `settingshostguard ok: ${result.panes.length} host-scoped panes rendered the selected host's data; write landed via AGENTS.md Save (artifacts at ${artifactDir})`,
    );
  } catch (error) {
    console.error(`settingshostguard FAIL: ${error instanceof Error ? error.message : String(error)}`);
    writeResult(driver);
    process.exitCode = 1;
  } finally {
    await driver.stop();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
