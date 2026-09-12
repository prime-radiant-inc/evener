#!/usr/bin/env node
// skillguard — the sixth browser guard: drives the PRODUCTION hub web app
// (AppShell → Session → Composer) in headless Chrome against a REAL hub URL
// served by a REAL evener-hub WebServer with REAL `evener serve` daemons
// behind it. jsdom cannot exercise a live AppWire session, native IndexedDB
// durable mutations, or the composer's chip state machines — that is exactly
// what this guard owns.
//
// Unlike the five layout/scroll guards, this driver never starts Vite: the
// hub's embedded production frontend is the app under test, and --url is the
// hub's real /auth/<token> URL. All model output is scripted at the daemon's
// external provider adapter (see cmd/evener/skill_browser_helper_test.go); the
// driver's only fixture channels are:
//   --url            the hub's auth URL to navigate first
//   --artifact-dir   where failing-run screenshots and state dumps are kept
//   --control-path   fixture IPC JSONL to the daemon helper (hold/release)
//   --milestone-path JSONL the driver appends browser milestones to; the Go
//                    owner (TestSkillComposerBrowser) verifies them against
//                    the daemons' actual provider requests and transcripts.
//
// Deterministic: no credentials, no network beyond the loopback hub, no
// shared dev server.
import { appendFileSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { mkdtempSync } from "node:fs";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import {
  chromeProfileEnvironment,
  chromeProfileIsolationArgs,
  createBrowserProcessCleanup,
  describeBrowserStartupFailure,
  findChrome,
  parseChromeDevToolsAnnouncement,
  requestBrowserClose,
} from "../browserGuardProcess.mjs";
import { connectPage, createStartupDeadline, devtoolsHttpURL, evaluate, navigateTo, waitForHttp } from "../browserGuardCdp.mjs";

const FRONTEND = path.resolve(path.dirname(import.meta.url), "..", "..");
const PROFILE_PREFIX = "skillguard-chrome-";
const DEVTOOLS_ANNOUNCEMENT_PREFIX = "DevTools listening on ";
const CHILD_EXIT_GRACE_MS = 2_000;

// Fixture vocabulary, shared with cmd/evener-hub/skill_composer_browser_test.go.
// The Go owner asserts these exact strings in the daemons' request logs and
// transcripts, so they are deliberately opaque: no natural-language copy is
// pinned, only sentinel data crossing the plumbing boundaries.
const SKILL_NAME = "pkg:probe";
const SKILL_TOKEN = "probe";
const SKILL_MENU_ROW = "/pkg:probe";
const CHIP_REMOVE_PREFIX = "Remove skill pkg:probe";
const REPLY_TEXT = "skillguard turn complete";
const PROSE = {
  canonical: "PROSE_ALPHA_14a run the fixture check on the gamma channel",
  draft: "PROSE_DRAFT_14b staged for the switch",
  queueTurn: "PROSE_QTURN_14c open a long turn for the queue",
  queue1: "PROSE_QUEUE_14c first pass",
  queue2: "PROSE_QUEUE_14c second pass",
  attachment: "PROSE_ATTACH_14d inspect the attached image",
  steerTurn: "PROSE_STEER_TURN_14e open a long turn for steering",
  steer: "PROSE_STEER_14e redirect the running turn",
  capabilityLoss: "PROSE_CAPLOSS_14f aimed at a lost capability",
  failTurn: "PROSE_FAIL_TURN_14g open a long turn for the failing claim",
  fail: "PROSE_FAIL_14h request the missing source",
  delay: "PROSE_DELAY_14i submitted then edited while held",
  delayExtra: "PROSE_DELAY_EXTRA_14i typed after the submit",
  transport: "PROSE_NET_14j submitted while offline",
};

const VIEWPORT = { width: 1440, height: 1000 };

function usage() {
  return [
    "Usage: node scripts/skillguard/run.mjs --url URL --artifact-dir DIR --control-path FILE --milestone-path FILE --session-a REF --session-b REF",
    "",
    "Drives the production evener-hub web app in headless Chrome against a real",
    "hub URL, proving the composer's canonical skill selection behavior end to end.",
    "",
    "Options:",
    "  --url URL            the hub's /auth/<token> URL to open first (required)",
    "  --artifact-dir DIR   directory for failing-run screenshots and dumps (required)",
    "  --control-path FILE  fixture IPC JSONL written to the daemon helper: hold/release (required)",
    "  --milestone-path FILE JSONL file this driver appends browser milestones to (required)",
    "  --session-a REF      the hub session ref of the control-path daemon's own session (required)",
    "  --session-b REF      the hub session ref of the second daemon's session (required)",
    "  --help                show this help",
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

class Driver {
  constructor({ url, artifactDir, controlPath, milestonePath }) {
    this.url = url;
    this.artifactDir = artifactDir;
    this.controlPath = controlPath;
    this.milestonePath = milestonePath;
    this.failures = [];
    this.chromeBinary = null;
    this.chromeArgv = [];
    this.chromeStderr = "";
    this.page = null;
    this.lifecycle = null;
    this.profileDir = null;
    this.chrome = null;
    this.endpoint = null;
  }

  milestone(name, detail = {}) {
    this.milestoneCount = (this.milestoneCount ?? 0) + 1;
    appendFileSync(this.milestonePath, `${JSON.stringify({ milestone: name, at: new Date().toISOString(), detail })}\n`);
  }

  control(command) {
    appendFileSync(this.controlPath, `${JSON.stringify({ command })}\n`);
  }

  async start() {
    this.chromeBinary = findChrome();
    this.profileDir = mkdtempSync(path.join(tmpdir(), PROFILE_PREFIX));
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
        finish(reject, new Error(`chrome exited before DevTools readiness (code ${code ?? "?"}, signal ${signal ?? "none"})`));
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

  // ---- native input ----

  async typeText(text) {
    for (const char of text) {
      await this.send("Input.dispatchKeyEvent", {
        type: "keyDown",
        key: char,
        text: char,
        unmodifiedText: char,
      });
      await this.send("Input.dispatchKeyEvent", { type: "keyUp", key: char });
    }
  }

  async press(key, modifiers = 0) {
    const codes = {
      Enter: { code: "Enter", keyCode: 13 },
      Tab: { code: "Tab", keyCode: 9 },
      Escape: { code: "Escape", keyCode: 27 },
      Backspace: { code: "Backspace", keyCode: 8 },
      a: { code: "KeyA", keyCode: 65 },
    }[key];
    await this.send("Input.dispatchKeyEvent", {
      type: "keyDown",
      key,
      code: codes?.code ?? key,
      windowsVirtualKeyCode: codes?.keyCode ?? 0,
      nativeVirtualKeyCode: codes?.keyCode ?? 0,
      modifiers,
    });
    await this.send("Input.dispatchKeyEvent", {
      type: "keyUp",
      key,
      code: codes?.code ?? key,
      windowsVirtualKeyCode: codes?.keyCode ?? 0,
      nativeVirtualKeyCode: codes?.keyCode ?? 0,
      modifiers,
    });
  }

  async elementBox(selector) {
    return evaluate(
      this.send,
      `(() => {
        const el = document.querySelector(${JSON.stringify(selector)});
        if (!el) return null;
        el.scrollIntoView({ block: "center", inline: "center" });
        const r = el.getBoundingClientRect();
        return { x: r.x + r.width / 2, y: r.y + r.height / 2, w: r.width, h: r.height };
      })()`,
    );
  }

  async click(selector, { scopedTo } = {}) {
    const full = scopedTo ? `${scopedTo} ${selector}` : selector;
    const box = await this.elementBox(full);
    if (!box) throw new Error(`click: no element matches ${full}`);
    await this.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: box.x, y: box.y });
    await this.send("Input.dispatchMouseEvent", { type: "mousePressed", x: box.x, y: box.y, button: "left", clickCount: 1 });
    await this.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: box.x, y: box.y, button: "left", clickCount: 1 });
  }

  async clickAt(x, y) {
    await this.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
    await this.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  }

  // clickByText finds a visible button by its accessible text — some controls
  // (the queue strip's buttons) carry no testid.
  async clickByText(text) {
    const boxes = await evaluate(
      this.send,
      `(() => { const matches = [...document.querySelectorAll("button")].filter((b) => b.textContent.trim() === ${JSON.stringify(text)} || (b.getAttribute("aria-label") ?? "").trim() === ${JSON.stringify(text)});
        return matches.map((b) => { b.scrollIntoView({ block: "center" }); const r = b.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2, disabled: b.disabled }; }); })()`,
    );
    if (!boxes || boxes.length === 0) throw new Error(`clickByText: no button labeled ${JSON.stringify(text)}`);
    if (boxes[0].disabled) throw new Error(`clickByText: button ${JSON.stringify(text)} is disabled`);
    await this.clickAt(boxes[0].x, boxes[0].y);
  }

  // ---- page state (each returns plain JSON values) ----

  composerSelector(ref) {
    return `[data-composer="${ref}"]`;
  }

  // The composer's chip/tile rows render in the pane's footer, OUTSIDE the
  // [data-composer] wrapper (that wrapper is only the hold-hints anchor
  // around the input card). The session pane is the PARENT of the scaffold
  // body carrying data-pane-scaffold="session:<ref>" (PaneScaffold renders
  // header/body/footer inside one pane div) - scope to that pane so the
  // state reads the chips this session actually rendered.
  paneScopeExpr(ref) {
    return `document.querySelector("[data-pane-scaffold='session:${ref}']")?.parentElement`;
  }

  async waitPage(exprSource, { timeoutMs = 15000, label }) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const value = await evaluate(this.send, exprSource).catch(() => null);
      if (value !== null && value !== undefined && value !== false) return value;
      if (Date.now() > deadline) {
        // Toasts auto-dismiss; capture whatever the app is complaining about
        // at the moment of the timeout, and keep the LAST toast a composer
        // action produced around for the waits that outlive it.
        const toast = await evaluate(this.send, this.toastExpr()).catch(() => "");
        const seen = toast || this.lastToast ? `; toast: ${toast || this.lastToast}` : "";
        throw new Error(`timed out after ${timeoutMs}ms waiting for ${label ?? exprSource}${seen}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 80));
    }
  }

  composerStateExpr(ref) {
    return `(() => {
      const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
      const pane = ${this.paneScopeExpr(ref)};
      if (!root || !pane) return null;
      const textarea = root.querySelector("textarea");
      const chips = [...pane.querySelectorAll("[data-testid='composer-skill-chip']")];
      return {
        text: textarea ? textarea.value : null,
        placeholder: textarea ? textarea.placeholder : null,
        chips: chips.map((chip) => chip.textContent),
        removeLabels: [...pane.querySelectorAll("[data-testid='composer-skill-chip'] button")].map((b) => b.getAttribute("aria-label")),
        tiles: pane.querySelectorAll("[data-testid='attachment-tile']").length,
        submitDisabled: root.querySelector("[data-testid='composer-submit']")?.disabled ?? null,
        steerVisible: root.querySelector("[data-testid='composer-steer']") !== null,
      };
    })()`;
  }

  async composerState(ref) {
    return evaluate(this.send, this.composerStateExpr(ref));
  }

  railRowsExpr() {
    return `(() => ({
      rows: [...document.querySelectorAll("[data-session-ref]")].map((el) => ({ ref: el.dataset.sessionRef, text: el.textContent.slice(0, 80) })),
    }))()`;
  }

  queueStripExpr() {
    return `(() => {
      const header = [...document.querySelectorAll("h3")].find((h) => h.textContent.startsWith("Queued messages"));
      if (!header) return null;
      const strip = header.closest("section");
      const rows = [...strip.querySelectorAll("li")].map((li) => ({
        text: li.textContent,
        buttons: [...li.querySelectorAll("button")].map((b) => b.getAttribute("aria-label") ?? b.textContent),
      }));
      return { header: header.textContent, rows };
    })()`;
  }

  toastExpr() {
    return `(() => document.querySelector("section[aria-label='Notifications']")?.textContent ?? "")()`;
  }

  turnFailureExpr() {
    return `(() => {
      const caps = [...document.querySelectorAll("[data-testid='turn-failure']")];
      return caps.length > 0 ? caps.map((cap) => cap.textContent.slice(0, 200)) : null;
    })()`;
  }

  transcriptTextExpr() {
    return `(() => document.body.innerText.length + "|" + document.body.innerText.slice(-4000))()`;
  }

  durableRecordsExpr() {
    return `(async () => {
      const openDB = (name) => new Promise((resolve, reject) => {
        const request = indexedDB.open(name);
        request.addEventListener("success", () => resolve(request.result), { once: true });
        request.addEventListener("error", () => reject(request.error), { once: true });
      });
      const readAll = (database, store) => new Promise((resolve, reject) => {
        try {
          const tx = database.transaction(store, "readonly");
          const request = tx.objectStore(store).getAll();
          request.addEventListener("success", () => resolve(request.result), { once: true });
          request.addEventListener("error", () => reject(request.error), { once: true });
        } catch (error) { reject(error); }
      });
      const strip = (records) => (records ?? []).map((record) => ({
        clientMutationId: record.clientMutationId,
        method: record.method,
        state: record.state,
        recoveryKind: record.recoveryKind,
        recoveryReason: record.recoveryReason,
        composerText: record.composerText,
        targetRef: record.targetRef,
        input: record.payload?.input,
      }));
      const database = await openDB("evener-mutation-outbox");
      const out = {};
      for (const store of ["outbox", "optimistic", "recovery"]) {
        try { out[store] = strip(await readAll(database, store)); } catch { out[store] = "unreadable"; }
      }
      database.close();
      return out;
    })()`;
  }

  draftStorageExpr() {
    return `(() => {
      const out = [];
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i);
        if (key.startsWith("evener.composer.draft.")) out.push({ key, value: localStorage.getItem(key) });
      }
      return out.length > 0 ? out : [];
    })()`;
  }

  async screenshot(name) {
    try {
      const response = await this.send("Page.captureScreenshot", { format: "png" });
      writeFileSync(path.join(this.artifactDir, `${name}.png`), Buffer.from(response.result.data, "base64"));
    } catch (error) {
      appendFileSync(path.join(this.artifactDir, "screenshots.log"), `${name}: ${error}\n`);
    }
  }

  async dumpState(name) {
    const files = ["composer", "rail", "queue", "toast", "durable", "drafts", "transcript"].map((kind) => {
      const exprs = {
        composer: this.composerStateExpr(this.sessionA),
        rail: this.railRowsExpr(),
        queue: this.queueStripExpr(),
        toast: this.toastExpr(),
        durable: this.durableRecordsExpr(),
        drafts: this.draftStorageExpr(),
        transcript: this.transcriptTextExpr(),
      };
      return evaluate(this.send, exprs[kind]).catch((error) => `EVAL ERROR: ${error}`);
    });
    const [composer, rail, queue, toast, durable, drafts, transcript] = await Promise.all(files);
    appendFileSync(
      path.join(this.artifactDir, `${name}.dump.json`),
      JSON.stringify({ at: new Date().toISOString(), composer, rail, queue, toast, durable, drafts, transcript }, null, 2),
    );
  }

  // ---- composer gestures ----

  async openSession(ref) {
    await this.waitPage(
      `(() => document.querySelector("[data-session-ref='${ref}']") !== null ? true : null)()`,
      { label: `rail row for ${ref}` },
    );
    await this.click(`[data-session-ref="${ref}"]`);
    await this.waitPage(
      `(() => document.querySelector(${JSON.stringify(this.composerSelector(ref))}) !== null ? true : null)()`,
      { label: `composer for ${ref}` },
    );
    await this.waitPage(
      `(() => { const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))}); const ta = root && root.querySelector("textarea"); return ta && !ta.hidden ? true : null; })()`,
      { label: `visible textarea for ${ref}` },
    );
  }

  async focusComposer(ref) {
    await this.click(`${this.composerSelector(ref)} textarea`);
    // A click lands wherever the box's center is; with existing text that can
    // be mid-string. The caret belongs at the END for every gesture this
    // driver makes (typing always appends), so set it after the focus click.
    await evaluate(
      this.send,
      `(() => { const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
        const ta = root && root.querySelector("textarea"); if (!ta) return null;
        ta.setSelectionRange(ta.value.length, ta.value.length); return true; })()`,
    );
  }

  async selectSkillChip(ref) {
    // Every selection starts from a focused composer: after e.g. a chip
    // remove, focus sits on the removed chip's button and typed keys would
    // never reach the textarea.
    await this.focusComposer(ref);
    // Type the completion token as its own trailing token (a leading space —
    // a mid-word slash never opens the menu); the inline slash menu opens
    // with real matches.
    await this.typeText(` /${SKILL_TOKEN}`);
    await this.waitPage(
      `(() => { const menu = document.querySelector("[data-testid='composer-slash-menu']"); if (!menu) return null;
        return [...menu.querySelectorAll("button")].some((b) => b.textContent.includes(${JSON.stringify(SKILL_MENU_ROW)})) ? true : null; })()`,
      { label: `slash menu row ${SKILL_MENU_ROW}` },
    );
    // Click the skill's own row (native mouse events), not a keyboard commit,
    // so the scenario never depends on highlight ordering.
    const rowBox = await this.elementBox(
      `[data-testid='composer-slash-menu'] button`,
    );
    if (!rowBox) throw new Error("slash menu row vanished before click");
    const rows = await evaluate(
      this.send,
      `(() => { const menu = document.querySelector("[data-testid='composer-slash-menu']");
        return [...menu.querySelectorAll("button")].filter((b) => b.textContent.includes(${JSON.stringify(SKILL_MENU_ROW)})).map((b) => { const r = b.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; }); })()`,
    );
    if (!rows || rows.length === 0) throw new Error(`no ${SKILL_MENU_ROW} row in slash menu`);
    await this.clickAt(rows[0].x, rows[0].y);
    await this.waitPage(
      `(() => { const pane = ${this.paneScopeExpr(ref)};
        const chips = pane ? [...pane.querySelectorAll("[data-testid='composer-skill-chip']")] : [];
        return chips.some((c) => c.textContent.includes(${JSON.stringify(SKILL_NAME)})) ? chips.map((c) => c.textContent) : null; })()`,
      { label: `chip ${SKILL_NAME}` },
    );
    await this.waitPage(
      `(() => document.querySelector("[data-testid='composer-slash-menu']") === null ? true : null)()`,
      { label: "slash menu closed after selection" },
    );
    // Chip selection removes ONLY the completion token, so the leading space
    // that made it a token is still in the text; delete it with a real
    // Backspace so the composer holds exactly the prose every later
    // assertion compares against.
    await this.press("Backspace");
    await this.waitPage(
      `(() => { const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
        const ta = root && root.querySelector("textarea"); return ta && !ta.value.endsWith(" ") ? true : null; })()`,
      { label: "trailing completion space removed" },
    );
  }

  async removeSkillChip(ref) {
    const labels = await evaluate(
      this.send,
      `(() => { const pane = ${this.paneScopeExpr(ref)};
        const buttons = pane ? [...pane.querySelectorAll("[data-testid='composer-skill-chip'] button")].filter((b) => (b.getAttribute("aria-label") ?? "").startsWith(${JSON.stringify(CHIP_REMOVE_PREFIX)})) : [];
        return buttons.map((b) => { const r = b.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2, label: b.getAttribute("aria-label") }; }); })()`,
    );
    if (!labels || labels.length === 0) throw new Error("no chip remove button");
    await this.clickAt(labels[0].x, labels[0].y);
    await this.waitPage(
      `(() => { const pane = ${this.paneScopeExpr(ref)};
        const chips = pane ? [...pane.querySelectorAll("[data-testid='composer-skill-chip']")] : [];
        return chips.some((c) => c.textContent.includes(${JSON.stringify(SKILL_NAME)})) ? null : true; })()`,
      { label: "chip removed" },
    );
    return labels[0].label;
  }

  async clickSubmit(ref) {
    await this.clickComposerAction(ref, "composer-submit");
  }

  async clickSteer(ref) {
    await this.clickComposerAction(ref, "composer-steer");
  }

  // A composer action click is lost when a re-render lands between the mouse
  // press and release (no click event fires) — the roster refresh after a
  // daemon death re-renders the shell exactly then. So each attempt is
  // verified by an OBSERVABLE effect (composer state changed, the button went
  // disabled, or a toast answered) and re-clicked when nothing happened. A
  // re-click while the first landed is harmless: actionPending disables the
  // button synchronously (submitAction sets busy state before its first
  // await), so the duplicate press hits a disabled control.
  async clickComposerAction(ref, testId, { attempts = 4 } = {}) {
    const before = await this.composerState(ref);
    // A refusal toast from an EARLIER action can still be on screen when this
    // click starts; only a toast that CHANGED counts as this click's effect.
    const beforeToast = await evaluate(this.send, this.toastExpr()).catch(() => "");
    const effectExpr = `(() => {
      const state = ${this.composerStateExpr(ref)};
      if (!state) return null;
      if (state.text !== ${JSON.stringify(before.text)}) return true;
      if (state.chips.length !== ${JSON.stringify(before.chips.length)}) return true;
      if (state.tiles !== ${JSON.stringify(before.tiles)}) return true;
      if (state.submitDisabled === true) return true;
      const toast = document.querySelector("section[aria-label='Notifications']");
      if (toast && toast.textContent.trim() !== "" && toast.textContent !== ${JSON.stringify(beforeToast)}) return true;
      return null;
    })()`;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      try {
        await this.click(`[data-testid='${testId}']`, { scopedTo: this.composerSelector(ref) });
      } catch (clickError) {
        // The control VANISHING usually means an EARLIER attempt's click just
        // landed (e.g. a steer that took longer than one poll window to
        // acknowledge, unmounting the button): give the effect one final
        // observation window before treating this as a failure.
        const landed = await this.waitPage(effectExpr, {
          timeoutMs: 3000,
          label: `${testId} effect after the control vanished (attempt ${attempt}/${attempts})`,
        }).catch(() => false);
        if (landed) {
          this.lastToast = await evaluate(this.send, this.toastExpr()).catch(() => "");
          return;
        }
        throw clickError;
      }
      const landed = await this.waitPage(effectExpr, {
        timeoutMs: attempt === attempts ? 1500 : 1000,
        label: `${testId} click to take effect (attempt ${attempt}/${attempts})`,
      }).catch(() => false);
      if (landed) {
        this.lastToast = await evaluate(this.send, this.toastExpr()).catch(() => "");
        return;
      }
    }
    // Diagnose a click that never took: what covers the button, where it sits,
    // and what the app said — the follow-up waits only ever show a symptom.
    const diagnosis = await evaluate(
      this.send,
      `(() => { const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
        const btn = root && root.querySelector("[data-testid=" + ${JSON.stringify(`'${testId}'`)} + "]");
        if (!btn) return "button not in DOM";
        const r = btn.getBoundingClientRect();
        const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
        const toast = document.querySelector("section[aria-label='Notifications']");
        return JSON.stringify({
          rect: { x: r.x, y: r.y, w: r.width, h: r.height },
          disabled: btn.disabled,
          hit: hit === btn ? "button" : (hit ? hit.tagName + "." + hit.className + " " + (hit.getAttribute("data-testid") ?? "") : "nothing"),
          toast: toast ? toast.textContent : "",
        }); })()`,
    ).catch((error) => `diagnosis failed: ${error}`);
    throw new Error(`${testId} click never took effect for ${ref}: ${diagnosis}`);
  }

  async waitForComposerCleared(ref, { timeoutMs = 15000 } = {}) {
    await this.waitPage(
      `(() => { const state = ${this.composerStateExpr(ref)}; return state && state.text === "" && state.chips.length === 0 ? true : null; })()`,
      { timeoutMs, label: `composer cleared for ${ref}` },
    );
  }

  async waitForActiveTurn(ref, { timeoutMs = 15000 } = {}) {
    await this.waitPage(
      `(() => { const state = ${this.composerStateExpr(ref)}; return state && state.steerVisible ? true : null; })()`,
      { timeoutMs, label: `active turn (Steer visible) for ${ref}` },
    );
  }

  // Every scripted turn replies with the SAME sentinel, and the transcript
  // VIRTUALIZES its rows — older reply rows unmount as new ones render, so
  // counting occurrences in body.innerText is a moving window that can never
  // observe a turn that completed. Instead, snapshot WHICH turn-block ids
  // already carry the reply and wait for a turn-block that carries it and was
  // NOT in that snapshot. The baseline must be captured BEFORE the
  // submit/release that triggers the reply.
  async replyBaseline(text = REPLY_TEXT) {
    const ids = await evaluate(
      this.send,
      `(() => [...document.querySelectorAll("[data-testid='turn-block']")]
        .filter((el) => (el.textContent ?? "").includes(${JSON.stringify(text)}))
        .map((el) => el.getAttribute("data-turn-id")))()`,
    ).catch(() => []);
    return Array.isArray(ids) ? ids : [];
  }

  async waitForReply(text, baselineIds, { timeoutMs = 25000 } = {}) {
    if (!Array.isArray(baselineIds)) {
      throw new Error("waitForReply needs a replyBaseline() captured before the triggering submit/release");
    }
    await this.waitPage(
      `(() => { const baseline = new Set(${JSON.stringify(baselineIds)});
        return [...document.querySelectorAll("[data-testid='turn-block']")]
          .some((el) => (el.textContent ?? "").includes(${JSON.stringify(text)}) && !baseline.has(el.getAttribute("data-turn-id"))) ? true : null; })()`,
      { timeoutMs, label: `a further reply "${text}" (${baselineIds.length} earlier reply turns)` },
    );
  }
}

function check(condition, message) {
  if (!condition) throw new Error(message);
}

async function runScenarios(driver) {
  // ---- prelude: auth + app shell ----
  await navigateTo(driver.page, driver.url);
  await driver.waitPage(`(() => document.querySelector("[data-testid='rail-brand']") !== null ? true : null)()`, {
    timeoutMs: 30000,
    label: "app shell (rail brand)",
  });
  const rows = await driver.waitPage(driver.railRowsExpr(), { timeoutMs: 30000, label: "rail rows" });
  check(rows.rows.length >= 2, `expected two live sessions in the rail, found ${rows.rows.length}`);
  // The control path targets helper alpha's OWN daemon, and rail order is not
  // start order: pin each session to the ref the Go owner derived from the
  // daemon's rendezvous entry, so every hold/release lands on the daemon that
  // actually owns the driven session.
  const rowRefs = new Set(rows.rows.map((row) => row.ref));
  check(rowRefs.has(driver.pinnedSessionA), `rail has no session ${driver.pinnedSessionA}: ${JSON.stringify(rows.rows)}`);
  check(rowRefs.has(driver.pinnedSessionB), `rail has no session ${driver.pinnedSessionB}: ${JSON.stringify(rows.rows)}`);
  driver.sessionA = driver.pinnedSessionA;
  driver.sessionB = driver.pinnedSessionB;
  driver.milestone("sessions-visible", { sessionA: driver.sessionA, sessionB: driver.sessionB, rows: rows.rows });

  // ---- scenario: canonical selection ----
  await driver.openSession(driver.sessionA);
  driver.milestone("composer-mounted", { ref: driver.sessionA });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.canonical);
  await driver.selectSkillChip(driver.sessionA);
  let state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 1 && state.chips[0].includes(SKILL_NAME), `chip missing after selection: ${JSON.stringify(state)}`);
  check(state.text === PROSE.canonical, `selection changed the prose: ${JSON.stringify(state.text)}`);
  driver.milestone("chip-added", state);
  const removeLabel = await driver.removeSkillChip(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 0, "chip not removed");
  check(state.text === PROSE.canonical, "removal changed the prose");
  driver.milestone("chip-removed", { ...state, removeLabel });
  await driver.selectSkillChip(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 1, "chip not re-selected");
  driver.milestone("chip-reselected", state);
  driver.milestone("chip-labels", {
    chipText: state.chips,
    removeLabels: state.removeLabels,
    prose: state.text,
  });
  // The durable outbox record is TRANSIENT — removed at the daemon's
  // turn/start ACK, the same event that clears the composer — so this read
  // races it and may legitimately capture an already-drained outbox. The
  // durable-evidence assertions that cannot race live in the transcript's
  // skill_state record and the transport scenario's stalled-transport
  // capture; a record the driver did catch must still carry prose and skill.
  const canonicalBaseline = await driver.replyBaseline();
  await driver.clickSubmit(driver.sessionA);
  const durable = await evaluate(driver.send, driver.durableRecordsExpr()).catch(() => ({}));
  driver.milestone("durable-mutation", durable);
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("submitted-canonical", { ref: driver.sessionA, prose: PROSE.canonical });
  driver.milestone("draft-after-commit", await evaluate(driver.send, driver.draftStorageExpr()));
  await driver.waitForReply(REPLY_TEXT, canonicalBaseline);

  // ---- scenario: draft thread-switch / remount ----
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.draft);
  await driver.selectSkillChip(driver.sessionA);
  driver.milestone("draft-staged", await driver.composerState(driver.sessionA));
  await driver.openSession(driver.sessionB);
  driver.milestone("thread-switched", { ref: driver.sessionB });
  const bState = await driver.composerState(driver.sessionB);
  check(bState.text === "", `session B composer not fresh: ${JSON.stringify(bState)}`);
  await driver.openSession(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.text === PROSE.draft, `draft text did not survive the switch: ${JSON.stringify(state)}`);
  check(state.chips.some((c) => c.includes(SKILL_NAME)), `draft chips did not survive the switch: ${JSON.stringify(state)}`);
  driver.milestone("draft-remounted", { ...state, storage: await evaluate(driver.send, driver.draftStorageExpr()) });
  // Clear the draft for the next scenario: select all + remove the chip.
  await driver.focusComposer(driver.sessionA);
  await driver.press("a", 2); // Ctrl+A
  await driver.press("Backspace");
  await driver.removeSkillChip(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.text === "" && state.chips.length === 0, `draft not cleared: ${JSON.stringify(state)}`);
  driver.milestone("draft-cleared", state);

  // ---- scenario: queue edit / return / drain ----
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.queueTurn);
  await driver.clickSubmit(driver.sessionA);
  // The daemon's ACK clears the composer; wait for it before typing again —
  // clearIfUnchanged deliberately keeps a draft that was edited before the
  // ACK landed, so typing too early would strand the queue prose.
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  driver.milestone("hold-turn-started", { prose: PROSE.queueTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.queue1);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA);
  let queue = await driver.waitPage(
    `(() => { const strip = ${driver.queueStripExpr()}; return strip && strip.rows.some((row) => row.text.includes(${JSON.stringify(PROSE.queue1)})) ? strip : null; })()`,
    { label: "queue strip with first pass" },
  );
  check(
    queue.rows.some((row) => row.text.includes(PROSE.queue1)),
    `queue strip missing ${PROSE.queue1}: ${JSON.stringify(queue)}`,
  );
  driver.milestone("queued", {
    rows: queue.rows,
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  // Edit the queued entry: its text returns to the composer and the entry
  // leaves the queue (the durable edit is a text-only recompose — the chip is
  // re-staged by the user, which is the documented edit contract).
  await driver.click("button[aria-label='Edit message']");
  await driver.waitPage(
    `(() => { const state = ${driver.composerStateExpr(driver.sessionA)}; return state && state.text.includes(${JSON.stringify(PROSE.queue1)}) ? true : null; })()`,
    { label: "queued text returned to composer" },
  );
  driver.milestone("queue-returned", await driver.composerState(driver.sessionA));
  await driver.press("a", 2);
  await driver.typeText(PROSE.queue2);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA);
  queue = await driver.waitPage(
    `(() => { const strip = ${driver.queueStripExpr()}; return strip && strip.rows.some((row) => row.text.includes(${JSON.stringify(PROSE.queue2)})) ? strip : null; })()`,
    { label: "queue strip with second pass" },
  );
  check(
    queue.rows.some((row) => row.text.includes(PROSE.queue2)),
    `queue strip missing ${PROSE.queue2}: ${JSON.stringify(queue)}`,
  );
  driver.milestone("requeued", {
    rows: queue.rows,
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  await driver.clickByText("Steer queue now");
  // The drain commits durably before the release below lets the held turn
  // finish; the queue strip empties once the daemon has folded the queue into
  // the running turn.
  await driver.waitPage(
    `(() => { const strip = ${driver.queueStripExpr()}; return strip === null || strip.rows.length === 0 ? true : null; })()`,
    { timeoutMs: 30000, label: "queue drained" },
  );
  driver.milestone("drain-committed", {
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  const drainBaseline = await driver.replyBaseline();
  driver.control("release");
  await driver.waitForReply(REPLY_TEXT, drainBaseline);
  driver.milestone("drain-released", {});
}

async function runScenariosPart2(driver) {
  // ---- scenario: selected steering ----
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.steerTurn);
  await driver.clickSubmit(driver.sessionA);
  // Wait for the ACK's composer clear before typing the steer prose (same
  // clearIfUnchanged race as the queue scenario's hold turn).
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  // Steer only once the long turn's input is committed to the visible
  // transcript: steering before the daemon records the input folds the two
  // texts into ONE user message (interrupt semantics), which is a different
  // shape than the one this scenario's provider assertions describe.
  await driver.waitPage(
    `(() => [...document.querySelectorAll("[data-testid='turn-block']")].some((el) => (el.textContent ?? "").includes(${JSON.stringify(PROSE.steerTurn)})) ? true : null)()`,
    { label: "steered turn's input visible in the transcript" },
  );
  driver.milestone("steer-turn-started", { prose: PROSE.steerTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.steer);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSteer(driver.sessionA);
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("steered", { prose: PROSE.steer });
  const steerBaseline = await driver.replyBaseline();
  driver.control("release");
  await driver.waitForReply(REPLY_TEXT, steerBaseline);
  driver.milestone("steer-released", {});

  // ---- scenario: attachment preservation ----
  const imagePath = await writeFixtureImage(driver.artifactDir);
  await driver.setFocusFileInput(driver.sessionA, imagePath);
  await driver.waitPage(
    `(() => { const state = ${driver.composerStateExpr(driver.sessionA)}; return state && state.tiles > 0 ? state : null; })()`,
    { label: "attachment tile" },
  );
  // The tile renders its "still processing" placeholder until the PNG
  // re-encode lands (useAttachments settles it asynchronously); submitting
  // while it is pending is refused with "Image attachment is still
  // processing". Wait for the decoded thumbnail before typing on.
  await driver.waitPage(
    `(() => { const pane = ${driver.paneScopeExpr(driver.sessionA)};
      const tiles = pane ? [...pane.querySelectorAll("[data-testid='attachment-tile']")] : [];
      return tiles.length > 0 && tiles.every((t) => t.querySelector("button[aria-label^='View ']") !== null) ? true : null; })()`,
    { timeoutMs: 30000, label: "attachment decoded (no longer processing)" },
  );
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.attachment);
  await driver.selectSkillChip(driver.sessionA);
  await driver.removeSkillChip(driver.sessionA);
  await driver.selectSkillChip(driver.sessionA);
  const attachState = await driver.composerState(driver.sessionA);
  check(attachState.tiles === 1, `attachment tile lost across chip edits: ${JSON.stringify(attachState)}`);
  check(attachState.text.includes("[image 1]"), `attachment anchor missing: ${JSON.stringify(attachState.text)}`);
  driver.milestone("attachment-preserved", attachState);
  const attachBaseline = await driver.replyBaseline();
  await driver.clickSubmit(driver.sessionA);
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("attachment-submitted", {
    prose: PROSE.attachment,
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  await driver.waitForReply(REPLY_TEXT, attachBaseline);

  // ---- scenario: capability loss ----
  await driver.openSession(driver.sessionB);
  await driver.focusComposer(driver.sessionB);
  await driver.typeText(PROSE.capabilityLoss);
  await driver.selectSkillChip(driver.sessionB);
  const staged = await driver.composerState(driver.sessionB);
  driver.milestone("caploss-staged", staged);
  // The Go owner shuts helper B down when it sees that milestone, then
  // refreshes the real roster. The pane re-renders the thread as ended; the
  // composer collapses to its follow-up invitation.
  await driver.waitPage(
    `(() => { const state = ${driver.composerStateExpr(driver.sessionB)}; return state && state.placeholder === "Send a follow-up…" ? true : null; })()`,
    { timeoutMs: 30000, label: "session B rendered as ended" },
  );
  driver.milestone("caploss-ended", await driver.composerState(driver.sessionB));
  await driver.focusComposer(driver.sessionB);
  await driver.clickSubmit(driver.sessionB);
  // The refusal keeps the draft: text and chips stay, and NOTHING durable is
  // written for this mutation.
  const refused = await driver.composerState(driver.sessionB);
  check(refused.text.includes(PROSE.capabilityLoss), `draft text lost on refusal: ${JSON.stringify(refused)}`);
  check(refused.chips.some((c) => c.includes(SKILL_NAME)), `draft chip lost on refusal: ${JSON.stringify(refused)}`);
  const toast = await evaluate(driver.send, driver.toastExpr());
  const durableB = await evaluate(driver.send, driver.durableRecordsExpr());
  driver.milestone("caploss-refused", { ...refused, toast, durable: durableB });
  // Clean the staged draft so later IndexedDB reads stay unambiguous.
  await driver.focusComposer(driver.sessionB);
  await driver.press("a", 2);
  await driver.press("Backspace");
  await driver.removeSkillChip(driver.sessionB);
  // The refusal toast renders OVER the composer card and swallows clicks
  // aimed at its buttons; wait for it to dismiss before any later scenario
  // drives the composer again.
  await driver.waitPage(
    `(() => { const toast = document.querySelector("section[aria-label='Notifications']"); return !toast || toast.textContent.trim() === "" ? true : null; })()`,
    { timeoutMs: 20000, label: "refusal toast dismissed" },
  );

  // ---- scenario: failed activation + explicit retry ----
  await driver.openSession(driver.sessionA);
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.failTurn);
  await driver.clickSubmit(driver.sessionA);
  // Wait for the ACK's composer clear before typing the failing claim (same
  // clearIfUnchanged race as every other held turn-start).
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  driver.milestone("fail-turn-started", { prose: PROSE.failTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.fail);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA);
  const queue = await driver.waitPage(
    `(() => { const strip = ${driver.queueStripExpr()}; return strip && strip.rows.some((row) => row.text.includes(${JSON.stringify(PROSE.fail)})) ? strip : null; })()`,
    { label: "queue strip with failing request" },
  );
  check(queue.rows.some((row) => row.text.includes(PROSE.fail)), `queue missing ${PROSE.fail}: ${JSON.stringify(queue)}`);
  driver.milestone("fail-queued", {
    rows: queue.rows,
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  // The Go owner now deletes the skill source, releases the held turn, and
  // restores the source once the failed input is durably recorded. The
  // driver's next observable event is the visible turn-failure end cap.
  await driver.waitPage(driver.turnFailureExpr(), { timeoutMs: 45000, label: "visible failed input" });
  driver.milestone("fail-observed", { failure: await evaluate(driver.send, driver.turnFailureExpr()) });
  // Explicit retry: the failed input kept the names and prose for correction;
  // the user re-composes the same request and sends it again.
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.fail);
  await driver.selectSkillChip(driver.sessionA);
  const retryBaseline = await driver.replyBaseline();
  await driver.clickSubmit(driver.sessionA);
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForReply(REPLY_TEXT, retryBaseline);
  driver.milestone("fail-retried", { prose: PROSE.fail });

  // ---- scenario: delayed accepted-send vs newer chip edit ----
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.delay);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA);
  // The submit was ACCEPTED — the composer clears at the daemon's ACK; wait
  // for it so the newer-draft typing below starts from an empty composer.
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  driver.milestone("delay-submitted", { prose: PROSE.delay });
  // The submit was ACCEPTED (the composer cleared at the daemon's ACK) while
  // the turn is still held at the provider. The user starts a NEWER draft:
  // extra prose plus a chip, then removes that chip. The delayed commit
  // notification that arrives when the held turn finally completes must not
  // clobber this newer draft nor resurrect its removed chip.
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(` ${PROSE.delayExtra}`);
  await driver.selectSkillChip(driver.sessionA);
  const editedState = await driver.composerState(driver.sessionA);
  check(editedState.text.includes(PROSE.delayExtra), `newer draft lost the typed edit: ${JSON.stringify(editedState)}`);
  driver.milestone("delay-edited", editedState);
  await driver.removeSkillChip(driver.sessionA);
  const delayBaseline = await driver.replyBaseline();
  driver.control("release");
  await driver.waitForReply(REPLY_TEXT, delayBaseline);
  const keptState = await driver.composerState(driver.sessionA);
  check(keptState.text.includes(PROSE.delayExtra), `delayed commit clobbered the newer draft: ${JSON.stringify(keptState)}`);
  check(keptState.chips.length === 0, `delayed commit restored the removed chip: ${JSON.stringify(keptState)}`);
  driver.milestone("delay-commit-kept", keptState);
  await driver.focusComposer(driver.sessionA);
  await driver.press("a", 2);
  await driver.press("Backspace");

  // ---- scenario: transport loss + recovery ----
  const netBaseline = await driver.replyBaseline();
  await driver.send("Network.emulateNetworkConditions", { offline: true, latency: 0, downloadThroughput: 0, uploadThroughput: 0 });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(PROSE.transport);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA);
  // With the transport stalled the mutation cannot be acknowledged, but the
  // durable outbox has already persisted it — input text AND skill item — so
  // nothing is lost. (A RECOVERY row only ever appears for failed or orphaned
  // mutations, never a merely stalled one, so the durable record is the
  // observable.)
  const offlineDurable = await driver.waitPage(
    `(async () => { const durable = await ${driver.durableRecordsExpr()};
      return durable && durable.outbox.concat(durable.recovery).some((record) =>
        JSON.stringify(record.input).includes(${JSON.stringify(PROSE.transport)}) &&
        JSON.stringify(record.input).includes("skill")) ? durable : null; })()`,
    { timeoutMs: 20000, label: "offline submit persisted in the durable outbox" },
  );
  const offlineState = await driver.composerState(driver.sessionA);
  driver.milestone("net-failed-kept", { durable: offlineDurable, composer: offlineState });
  await driver.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  // Connectivity restored: the outbox retries (the window's online event and
  // the heartbeat's reconnect both wake it) and the same durable mutation is
  // delivered exactly once.
  await driver.waitForReply(REPLY_TEXT, netBaseline, { timeoutMs: 45000 });
  const onlineDurable = await evaluate(driver.send, driver.durableRecordsExpr());
  driver.milestone("net-restored", { durable: onlineDurable });
}

// Minimal 1x1 PNG fixture (a transparent pixel), written into the artifact
// directory so DOM.setFileInputFiles can hand the browser a real local file.
async function writeFixtureImage(dir) {
  const pngBase64 =
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
  const filePath = path.join(dir, "skillguard-fixture.png");
  writeFileSync(filePath, Buffer.from(pngBase64, "base64"));
  return filePath;
}

// setFocusFileInput points DevTools' file picker at the composer's hidden
// input[type=file] — the browser then fires the real change event.
async function setFocusFileInputImpl(driver, ref, filePath) {
  const document = await driver.send("DOM.getDocument", { depth: -1 });
  const rootId = document.result.root.nodeId;
  const found = await driver.send("DOM.querySelector", {
    nodeId: rootId,
    selector: `${driver.composerSelector(ref)} input[type=file]`,
  });
  const nodeId = found.result?.nodeId;
  if (!nodeId) throw new Error("composer file input not found in the DOM");
  await driver.send("DOM.setFileInputFiles", { files: [filePath], nodeId });
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  const need = ["url", "artifact-dir", "control-path", "milestone-path", "session-a", "session-b"];
  for (const name of need) {
    if (!args.values.get(name)) {
      console.error(`missing --${name}\n\n${usage()}`);
      process.exitCode = 1;
      return;
    }
  }
  mkdirSync(args.values.get("artifact-dir"), { recursive: true });
  const driver = new Driver({
    url: args.values.get("url"),
    artifactDir: args.values.get("artifact-dir"),
    controlPath: args.values.get("control-path"),
    milestonePath: args.values.get("milestone-path"),
  });
  driver.setFocusFileInput = (ref, filePath) => setFocusFileInputImpl(driver, ref, filePath);
  // Pinned session refs (see runScenarios' sessions-visible block): the
  // control path targets helper alpha's own daemon, and rail row order is not
  // helper start order.
  driver.pinnedSessionA = args.values.get("session-a");
  driver.pinnedSessionB = args.values.get("session-b");
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
    await runScenarios(driver);
    await runScenariosPart2(driver);
    driver.milestone("done", {});
    console.log(`skillguard ok: production composer driven through the real hub (${driver.milestoneCount ?? 0} milestones at ${driver.milestonePath})`);
  } catch (error) {
    console.error(`skillguard FAIL: ${error instanceof Error ? error.message : String(error)}`);
    try {
      await driver.screenshot("skillguard-failure");
      await driver.dumpState("skillguard-failure");
    } catch {}
    process.exitCode = 1;
  } finally {
    await driver.stop();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
