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
import { pathToFileURL } from "node:url";
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
import { ReactionBudget } from "./budgets.mjs";

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
// What composerState reports for the one selected skill chip.
const SKILL_CHIPS = [`/${SKILL_NAME}`];
const EDITOR = "[contenteditable='true'][role='textbox'][aria-label='Message']";
const TWO_SKILLS = "Run /skill-1 and then /skill-2";
const inlineText = (text) => `${text} /${SKILL_NAME}`;

// The payload a send is expected to carry. Most of this guard's sends go out
// with the skill chip attached and nothing staged, so that is the default and
// a site that differs says so.
function draft(text, { chips = SKILL_CHIPS, tiles = 0 } = {}) {
  return { text: chips === SKILL_CHIPS ? inlineText(text) : text, chips, tiles };
}
const REPLY_TEXT = "skillguard turn complete";
const CHIP_REMOVE_PREFIX = "Remove skill pkg:probe";
const PROSE = {
  canonical: "PROSE_ALPHA_14a run the fixture check on the gamma channel",
  draft: "PROSE_DRAFT_14b staged for the switch",
  queueTurn: "PROSE_QTURN_14c open a long turn for the queue",
  queue1: "PROSE_QUEUE_14c first pass",
  queue2: "PROSE_QUEUE_14c second pass",
  attachment: "PROSE_ATTACH_14d inspect the attached image",
  steerTurn: "PROSE_STEER_TURN_14e open a long turn for steering",
  steer: "PROSE_STEER_14e redirect the running turn",
  failTurn: "PROSE_FAIL_TURN_14g open a long turn for the failing claim",
  fail: "PROSE_FAIL_14h request the missing source",
  delay: "PROSE_DELAY_14i submitted then edited while held",
  delayExtra: "PROSE_DELAY_EXTRA_14i typed after the submit",
  transport: "PROSE_NET_14j submitted while offline",
};

const VIEWPORT = { width: 1440, height: 1000 };

// The queue strip's heading. The strip's root is a plain <section> with no
// testid, so both the presence check and the row dump locate it by this
// header; one constant keeps the two from drifting.
const QUEUED_MESSAGES_HEADER = "Queued messages";

// The steered turn's input is rendered only once the daemon's own turn/start
// push has crossed the hub and React has committed it into the virtualized
// transcript -- a full round trip that is independent of (and later than) the
// submit ACK that cleared the composer and showed Steer. That is the slowest
// hop in the scenario, so it gets a budget with real headroom (and an env
// override for triage) instead of waitPage's bare 15s default. It is still a
// hard bound: an input that never lands as a turn still fails.
const STEERED_TURN_INPUT_TIMEOUT_MS = envMillis("SKILLGUARD_STEERED_TURN_TIMEOUT_MS", 60_000);

// A hold captures the NEXT provider request, so it may only be armed once the
// previous turn is GENUINELY over. The session's busy predicate
// (appwire-client's isTurnActive) is `status.type === "active" &&
// activeTurnId`, and the daemon can report not-busy in the gap between a
// turn's last leg completing and the next leg -- a drained queue's steering
// message -- being dispatched. A single not-busy poll would arm the hold into
// that gap, the hold would steal the pending leg, that turn would never end,
// and the next submit would silently route to the client queue. Every turn-end
// barrier therefore requires the idle reading to SETTLE for this long before
// it releases. Overridable for triage.
const TURN_IDLE_SETTLE_MS = envMillis("SKILLGUARD_TURN_IDLE_SETTLE_MS", 3_000);

// The retry budget for the turn-id baseline read (turnIds). A baseline is
// captured immediately before the release that would dispatch the leg it
// proves, so it is normally one round trip; under load a poll can reject while
// the page is busy, and a rejected read must be retried rather than read as
// "no turns yet". The budget is deliberately longer than the wire's own 30s
// bound on a single Runtime.evaluate, so a stalled call is retried too instead
// of failing the scenario; it is still a hard bound, and a read that never
// comes back fails with the last reading that stood in for a baseline.
const TURN_BASELINE_RETRY_MS = envMillis("SKILLGUARD_TURN_BASELINE_TIMEOUT_MS", 45_000);

// selectAll feeds a replacement edit -- the queue journey selects the draft a
// queued entry returned and types over it -- so a caret the editor left at the
// end of the text instead of the whole selection would send the next typeText
// into the wrong place. Setting a DOM range is how a user selects text, and the
// editor adopts it asynchronously, so selectAll confirms the editor actually
// holds the whole-text selection before returning. The confirmation waits for
// the editor to settle (a selection a render has not reset yet reads as held
// once) and re-applies it a bounded number of times; an editor that will not
// hold it fails loudly rather than being typed into.
const SELECT_ALL_ATTEMPTS = 4;

// envMillis reads a millisecond budget from the environment, defaulting when
// unset or BLANK and refusing anything else non-numeric rather than silently
// treating it as NaN (which would disable a timeout entirely). Blank is not
// merely "": Number(" ") and Number("\t") are both 0 -- finite and
// non-negative -- so a whitespace-only value used to sail through the checks
// below and silently select 0. For SKILLGUARD_TURN_IDLE_SETTLE_MS that
// disables the settle barrier outright, which is the load-dependent flakiness
// this budget exists to remove. A blank value therefore takes the DEFAULT, the
// same as an unset one.
function envMillis(name, fallback) {
  const raw = process.env[name];
  if (raw === undefined || raw.trim() === "") return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value) || value < 0) {
    throw new Error(`${name} must be a non-negative number of milliseconds, got ${JSON.stringify(raw)}`);
  }
  return value;
}

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

// Read the actual rendered editor, including hard breaks. Ignore only PM's
// non-content trailing caret BR; inline atom labels remain ordinary text here.
function editorText(root) {
  const read = (node) => {
    if (node.nodeType === Node.TEXT_NODE) return node.textContent;
    if (node.nodeName === "BR") return node.classList.contains("ProseMirror-trailingBreak") ? "" : "\n";
    return [...node.childNodes].map(read).join("");
  };
  return [...root.childNodes].map((node, index) =>
    (index > 0 && node.nodeName === "P" ? "\n" : "") + read(node)).join("");
}

// Offset selection for single-line editing cases. Never place a caret inside
// an atom: its complete label contributes to plain-text offsets, but its DOM
// boundary is the only selectable location.
function selectEditorRange(editor, start, end) {
  const positions = new Map([[0, [editor, 0]]]);
  let offset = 0;
  const walk = (node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      for (let i = 0; i <= node.length; i++) positions.set(offset + i, [node, i]);
      offset += node.length;
    } else if (node.nodeType === Node.ELEMENT_NODE && node.matches("[data-testid='composer-skill-chip']")) {
      const index = [...node.parentNode.childNodes].indexOf(node);
      positions.set(offset, [node.parentNode, index]);
      offset += node.textContent.length;
      positions.set(offset, [node.parentNode, index + 1]);
    } else {
      for (const child of node.childNodes) walk(child);
    }
  };
  walk(editor);
  if (!positions.has(start) || !positions.has(end)) throw new Error(`invalid editor boundary ${start}-${end}`);
  editor.focus();
  const range = document.createRange();
  range.setStart(...positions.get(start)); range.setEnd(...positions.get(end));
  const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
}

export class Driver {
  constructor({ url, artifactDir, controlPath, milestonePath }) {
    this.url = url;
    this.artifactDir = artifactDir;
    this.controlPath = controlPath;
    this.milestonePath = milestonePath;
    // How slow THIS machine has proven to be: every wait's budget is sized
    // from the slowest reaction a completed wait has already observed, so a
    // loaded runner gets a proportional hang tripwire instead of the fixed
    // floor (see budgets.mjs).
    this.reactions = new ReactionBudget();
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
    // SKILLGUARD_CPU_THROTTLE=N slows the page's main thread N-fold through
    // Chrome's own emulation, the way a loaded CI runner does, so a
    // load-dependent guard failure can be reproduced on an idle host.
    const throttle = Number(process.env.SKILLGUARD_CPU_THROTTLE ?? "");
    if (throttle > 1) {
      await this.page.send("Emulation.setCPUThrottlingRate", { rate: throttle });
      this.milestone("cpu-throttled", { rate: throttle });
    }
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

  editorExpr(ref) {
    return `document.querySelector(${JSON.stringify(this.composerSelector(ref))})?.querySelector(${JSON.stringify(EDITOR)})`;
  }

  composerEditStateExpr(ref) {
    return `(() => {
      const editor = ${this.editorExpr(ref)};
      if (!editor) return null;
      const selection = window.getSelection();
      const offset = (node, at) => {
        if (!node || !editor.contains(node)) return null;
        const range = document.createRange();
        range.selectNodeContents(editor); range.setEnd(node, at);
        return (${editorText.toString()})(range.cloneContents()).length;
      };
      const anchor = offset(selection.anchorNode, selection.anchorOffset);
      const focus = offset(selection.focusNode, selection.focusOffset);
      return { value: (${editorText.toString()})(editor),
        start: anchor === null || focus === null ? null : Math.min(anchor, focus),
        end: anchor === null || focus === null ? null : Math.max(anchor, focus),
        focused: document.activeElement === editor };
    })()`;
  }

  // settleComposer waits until one session's editor stops changing under it,
  // and returns the state it settled on. Two reads 80ms apart that agree is
  // the signal that the last render has landed; it is not a promise that no
  // further one is coming, which is why every caller re-checks afterwards.
  // Focus is part of that state: a focus change during the wait is a change.
  async settleComposer(ref, { timeoutMs = 5000 } = {}) {
    const deadline = Date.now() + timeoutMs;
    let previous = null;
    for (;;) {
      const now = await evaluate(this.send, this.composerEditStateExpr(ref));
      if (!now) throw new Error(`settleComposer(${ref}): no composer editor`);
      const key = JSON.stringify(now);
      if (key === previous) return now;
      previous = key;
      if (Date.now() > deadline) {
        throw new Error(`settleComposer(${ref}): composer still changing after ${timeoutMs}ms (${key})`);
      }
      await new Promise((resolve) => setTimeout(resolve, 80));
    }
  }

  // Use Chrome's editing path, not value setters or synthetic input events.
  // A render that loses native input or moves its caret is a product failure:
  // do not repair/retype it in the driver and hide that failure.
  async typeText(ref, text) {
    const before = await evaluate(this.send, this.composerEditStateExpr(ref));
    check(before?.focused, `typeText(${ref}): editor is not focused`);
    check(before.start !== null && before.end !== null, `typeText(${ref}): selection is outside the editor`);
    const want = before.value.slice(0, before.start) + text + before.value.slice(before.end);
    const caret = before.start + text.length;
    await this.send("Input.insertText", { text });
    try {
      await this.waitPage(`(() => { const state = ${this.composerEditStateExpr(ref)};
        return state && state.value === ${JSON.stringify(want)} && state.start === ${caret} && state.end === ${caret} ? state : null; })()`,
        { label: `native edit ${JSON.stringify(want)} with caret ${caret}` });
    } catch (error) {
      const after = await evaluate(this.send, this.composerEditStateExpr(ref));
      throw new Error(`${error.message}; native state ${JSON.stringify({ before, after })}`, { cause: error });
    }
  }

  // typeTextAgainstAtom delivers a run that lands directly against a skill
  // atom's label, which the editor answers by separating the two with a space
  // so the reference stays whole. The oracle is that separated result, caret
  // included - not the raw insertion the browser handed over.
  async typeTextAgainstAtom(ref, text, side) {
    const before = await evaluate(this.send, this.composerEditStateExpr(ref));
    check(before?.focused, `typeTextAgainstAtom(${ref}): editor is not focused`);
    check(before.start !== null && before.end !== null, `typeTextAgainstAtom(${ref}): selection is outside the editor`);
    const inserted = side === "before" ? `${text} ` : ` ${text}`;
    const want = before.value.slice(0, before.start) + inserted + before.value.slice(before.end);
    const caret = before.start + inserted.length;
    await this.send("Input.insertText", { text });
    try {
      await this.waitPage(`(() => { const state = ${this.composerEditStateExpr(ref)};
        return state && state.value === ${JSON.stringify(want)} && state.start === ${caret} && state.end === ${caret} ? state : null; })()`,
        { label: `separated edit ${JSON.stringify(want)} with caret ${caret}` });
    } catch (error) {
      const after = await evaluate(this.send, this.composerEditStateExpr(ref));
      throw new Error(`${error.message}; native state ${JSON.stringify({ before, after })}`, { cause: error });
    }
  }

  async selectRange(ref, start, end = start) {
    const selected = await evaluate(this.send, `(async () => {
      const editor = ${this.editorExpr(ref)};
      if (!editor) return false;
      const changed = new Promise((resolve) => document.addEventListener("selectionchange", resolve, { once: true }));
      (${selectEditorRange.toString()})(editor, ${start}, ${end});
      await changed;
      return true;
    })()`);
    check(selected, `selectRange(${ref}): editor missing`);
  }

  async moveCaret(ref, key) {
    const changed = evaluate(this.send, `new Promise((resolve) => document.addEventListener("selectionchange", () => resolve(true), { once: true }))`);
    await this.press(ref, key);
    await changed;
  }

  async selectAll(ref) {
    for (let attempt = 1; attempt <= SELECT_ALL_ATTEMPTS; attempt++) {
      const state = await this.composerEditState(ref);
      check(state, `selectAll(${ref}): editor missing`);
      await this.selectRange(ref, 0, state.value.length);
      // settleComposer returns the state the editor stopped changing on, so a
      // selection a render is about to reset does not read as held.
      const settled = await this.settleComposer(ref);
      // Compared against the SETTLED text, not the length read before the
      // range was placed: a draft that grew mid-flight would otherwise leave
      // the selection covering only the old prefix and still read as held,
      // and the next typeText would replace the prefix and strand the tail.
      if (settled.start === 0 && settled.end === settled.value.length) return;
      if (attempt < SELECT_ALL_ATTEMPTS) {
        console.error(
          `skillguard: ${ref}: a render reset the selection after selectAll; re-selecting (attempt ${attempt}/${SELECT_ALL_ATTEMPTS})`,
        );
      }
    }
    throw new Error(
      `selectAll(${ref}): the editor would not hold the whole-text selection (${SELECT_ALL_ATTEMPTS} attempts)`,
    );
  }

  // composerEditState reads one session's editor state: its text, its selection
  // as serialized-text offsets, and whether it holds focus.
  async composerEditState(ref) {
    return evaluate(this.send, this.composerEditStateExpr(ref));
  }

  // #1669: macOS Chrome does not hand a page the OS clipboard without a
  // permission the guard's browser profile does not grant, so the copy/paste
  // milestone cannot use the real chords there. It drives the same two events
  // the browser would fire, each carrying a real DataTransfer, so the editor's
  // own clipboard serializer (copy) and paste importer stay under test while
  // only the OS clipboard itself is out of the loop. copySelection returns the
  // text/plain the editor's clipboardTextSerializer wrote into that
  // DataTransfer, which is the payload a real Ctrl+C would have put on the
  // clipboard.
  async copySelection(ref) {
    return evaluate(this.send, `(() => {
      const editor = ${this.editorExpr(ref)};
      if (!editor) return null;
      const dt = new DataTransfer();
      editor.dispatchEvent(new ClipboardEvent("copy", { clipboardData: dt, bubbles: true, cancelable: true }));
      return dt.getData("text/plain");
    })()`);
  }

  // pasteText delivers `text` the way a real Ctrl+V would: a `paste` event
  // carrying a DataTransfer with a text/plain payload, dispatched at the
  // editor so its own paste handler imports it. The edit it causes is awaited
  // by the caller's waitPage, exactly as the native chord was.
  async pasteText(ref, text) {
    return evaluate(this.send, `(() => {
      const editor = ${this.editorExpr(ref)};
      if (!editor) return null;
      const dt = new DataTransfer();
      dt.setData("text/plain", ${JSON.stringify(text)});
      return editor.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
    })()`);
  }

  // press sends a real key to one session's composer. The CDP event goes to
  // whatever the page has focused, which is not necessarily the editor this
  // scenario is driving, so focus is put back on that composer first -- in the
  // page, immediately before the key, rather than trusted from whatever ran
  // last. A Backspace delivered to the wrong element deletes the wrong draft.
  async press(ref, key, modifiers = 0) {
    const focused = await evaluate(
      this.send,
      `(() => {
        const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
        const ta = root && root.querySelector(${JSON.stringify(EDITOR)});
        if (!ta) return { error: "no composer contenteditable" };
        const already = document.activeElement === ta;
        if (!already) ta.focus();
        return { restored: !already, focused: document.activeElement === ta }; })()`,
    );
    if (!focused || focused.error) {
      throw new Error(`press(${ref}, ${key}): ${focused ? focused.error : "no result from the page (navigated or disconnected?)"}`);
    }
    if (!focused.focused) throw new Error(`press(${ref}, ${key}): the composer would not take focus`);
    if (focused.restored) console.error(`skillguard: ${ref}: focus was elsewhere before ${key}; restored it`);
    const codes = {
      Enter: { code: "Enter", keyCode: 13 },
      Tab: { code: "Tab", keyCode: 9 },
      Escape: { code: "Escape", keyCode: 27 },
      Backspace: { code: "Backspace", keyCode: 8 },
      Delete: { code: "Delete", keyCode: 46 },
      ArrowLeft: { code: "ArrowLeft", keyCode: 37 },
      ArrowRight: { code: "ArrowRight", keyCode: 39 },
      z: { code: "KeyZ", keyCode: 90 },
      y: { code: "KeyY", keyCode: 89 },
      c: { code: "KeyC", keyCode: 67 },
      v: { code: "KeyV", keyCode: 86 },
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

  // waitPage resolves on the FIRST observation of a non-null/false value. That
  // is right for a condition that only ever moves one way, but wrong for one
  // the app can flip back -- a status that can briefly read idle between a
  // turn's legs. `settleMs` requires the condition to hold CONTINUOUSLY for
  // that long (sampled once per poll) before the wait resolves, so a transient
  // reading cannot release it. Every caller that gates an IRREVERSIBLE action
  // (arming a hold, a submit that routes differently while busy) passes it.
  //
  // An evaluate that could not run -- a CDP hiccup, a navigation in flight --
  // is NOT a reading, in either direction. It must not release the wait (the
  // null check is what stops that) and it must not RESET the settle timer
  // either: under load a single failed poll in the middle of a settle window
  // used to throw the whole hold away and start it over, which is exactly the
  // kind of load-dependent behaviour this barrier exists to remove. Only a
  // reading that CAME BACK, including one that answers "not yet", ends a hold.
  //
  // Error time is not idle time either. A failed poll is unobserved wall
  // clock, so it cannot stand as proof that the condition held across it: the
  // settle window is SHIFTED FORWARD by that span -- the successful idle time
  // already accumulated is kept (the window does not restart, which is what a
  // hiccup used to cost), while the unobserved span itself is excluded from the
  // proof. Each failure contributes only the span since the PREVIOUS POLL
  // ATTEMPT, never since the last success: a run of failures must account every
  // interval exactly once, or the deadline would outrun wall clock (a failed
  // poll would push it further away than the clock advanced) and the window
  // start would be shoved past the current poll -- a wait that can neither
  // release nor fail.
  //
  // The deadline is refunded that span, because a proof cannot complete inside
  // a budget the failure ate. The refund is bounded by the settle window it
  // protects: once unobserved time has cost as much as the whole proof needs,
  // the deadline stops moving forward, so an evaluate that never comes back
  // fails the wait (at the caller's budget plus at most one settle window)
  // instead of extending it forever.
  //
  // The settle window itself is part of the budget the caller asked for, so
  // the deadline is extended once, to `settleMs` after the condition is first
  // held. Without it a condition that first holds inside the last settleMs of
  // the budget could never release -- a guaranteed timeout at exactly the
  // loaded moment the wait was widened to survive. It is accounted for ONCE:
  // re-extending on every hold would let a flapping condition postpone the
  // deadline indefinitely, and a wait that cannot fail is worse than a slow one.
  async waitPage(exprSource, { timeoutMs = 15000, label, settleMs = 0 } = {}) {
    const startedAt = Date.now();
    // The caller's timeoutMs is the FLOOR of a hang tripwire, not a fixed
    // budget: a run that has already proven this machine slow widens it (up to
    // the ceiling), so a correct-but-slow reaction under load is not read as a
    // wedged page. The wait is still released ONLY by the awaited condition --
    // the budget decides only how long silence is tolerated (see budgets.mjs).
    const budgetMs = this.reactions.deadline(timeoutMs);
    let deadline = startedAt + budgetMs;
    // A completed wait is the driver's observation of how slow the app is on
    // this machine. The settle window is a proof barrier, not a reaction, so a
    // hold counts from when the condition FIRST held, not from when it settled.
    const settled = (value) => {
      this.reactions.observe((heldSince ?? Date.now()) - startedAt);
      return value;
    };
    let heldSince = null;
    let settleAccounted = false;
    // When the previous poll ATTEMPT landed, success or failure: the span a
    // failure excludes from the proof is measured from here.
    let readAt = startedAt;
    // How much unobserved time may still be refunded to the deadline.
    let refundLeft = settleMs;
    for (;;) {
      let errored = false;
      const value = await evaluate(this.send, exprSource).catch(() => {
        errored = true;
        return null;
      });
      const now = Date.now();
      if (errored) {
        const unobserved = now - readAt;
        if (heldSince !== null) {
          heldSince += unobserved;
          const refund = Math.min(unobserved, refundLeft);
          refundLeft -= refund;
          deadline += refund;
        }
      }
      readAt = now;
      const held = !errored && value !== null && value !== undefined && value !== false;
      if (held) {
        if (settleMs <= 0) return settled(value);
        if (heldSince === null) {
          heldSince = now;
          if (!settleAccounted) {
            settleAccounted = true;
            deadline = Math.max(deadline, now + settleMs);
          }
        }
        if (now - heldSince >= settleMs) return settled(value);
      } else if (!errored) {
        heldSince = null;
      }
      if (now > deadline) {
        // Toasts auto-dismiss; capture whatever the app is complaining about
        // at the moment of the timeout, and keep the LAST toast a composer
        // action produced around for the waits that outlive it.
        const toast = await evaluate(this.send, this.toastExpr()).catch(() => "");
        const seen = toast || this.lastToast ? `; toast: ${toast || this.lastToast}` : "";
        const settle = settleMs > 0 ? ` and hold for ${settleMs}ms (last held: ${held ? `yes, ${now - (heldSince ?? now)}ms` : "no"})` : "";
        throw new Error(
          `timed out after ${budgetMs}ms (waited ${now - startedAt}ms) waiting for ${label ?? exprSource}${settle}${seen}`,
        );
      }
      await new Promise((resolve) => setTimeout(resolve, 80));
    }
  }

  composerStateExpr(ref) {
    return `(() => {
      const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))});
      const pane = ${this.paneScopeExpr(ref)};
      if (!root || !pane) return null;
      const editor = root.querySelector(${JSON.stringify(EDITOR)});
      const chips = editor ? [...editor.querySelectorAll("[data-testid='composer-skill-chip']")] : [];
      return {
        text: editor ? (${editorText.toString()})(editor) : null,
        placeholder: editor ? editor.getAttribute("data-placeholder") : null,
        chips: chips.map((chip) => chip.textContent),
        chipDetails: chips.map((chip) => ({ text: chip.textContent, title: chip.title, label: chip.getAttribute("aria-label"), editable: chip.getAttribute("contenteditable") })),
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

  // The rail element mounts before the hub's session list arrives, and waitPage
  // resolves on any non-null value -- so returning a bare rows object made the
  // caller's count check race the first render: the check saw an empty rail,
  // and the failure dump caught both rows present moments later. atLeast makes
  // the wait resolve only once that many rows are on screen. Callers that want
  // a snapshot whatever the count (the failure dump) leave it at 0.
  railRowsExpr({ atLeast = 0 } = {}) {
    return `(() => {
      const rows = [...document.querySelectorAll("[data-session-ref]")].map((el) => ({ ref: el.dataset.sessionRef, text: el.textContent.slice(0, 80) }));
      return rows.length >= ${atLeast} ? { rows } : null;
    })()`;
  }

  // The root a reading belongs to: the whole document by default (the waits
  // that ask "is the app's queue empty?" are about the app, and the rail is not
  // inside any session's pane), or one session's own pane when a `ref` is
  // passed. A FAILURE DIAGNOSIS passes one: with more than one pane mounted, a
  // document-wide sweep answers with whichever session happened to render a
  // match, which is exactly how a failure in this session gets diagnosed using
  // another session's turns or queue.
  paneOrDocumentExpr(ref = null) {
    return ref === null || ref === undefined ? `document` : this.paneScopeExpr(ref);
  }

  queueStripExpr(ref = null) {
    return `(() => {
      const root = ${this.paneOrDocumentExpr(ref)};
      if (!root) return null;
      const header = [...root.querySelectorAll("h3")].find((h) => h.textContent.startsWith(${JSON.stringify(QUEUED_MESSAGES_HEADER)}));
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

  // Every rendered turn-block's text, truncated per block. Used by failure
  // reporting: a wait on a turn-block says only that nothing matched, while
  // this says what the transcript actually contained -- in the session the
  // report is about when it is given a ref (see paneOrDocumentExpr).
  transcriptBlocksExpr(ref = null) {
    return `(() => { const root = ${this.paneOrDocumentExpr(ref)};
      if (!root) return null;
      return [...root.querySelectorAll("[data-testid='turn-block']")].map((el) => (el.textContent ?? "").slice(0, 240)); })()`;
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
      `(() => { const root = document.querySelector(${JSON.stringify(this.composerSelector(ref))}); const ta = root && root.querySelector(${JSON.stringify(EDITOR)}); return ta && !ta.hidden ? true : null; })()`,
      { label: `visible contenteditable for ${ref}` },
    );
  }

  async focusComposer(ref) {
    await this.click(`${this.composerSelector(ref)} ${EDITOR}`);
    const state = await this.composerState(ref);
    await this.selectRange(ref, state.text.length);
  }

  async selectSkillChip(ref) {
    await this.focusComposer(ref);
    const state = await this.composerState(ref);
    await this.typeText(ref, `${state.text.endsWith(" ") ? "" : " "}/${SKILL_TOKEN}`);
    await this.completeSkill(ref, SKILL_NAME);
    // Completion at the end adds a separator. Remove only that character,
    // leaving the canonical atom in its original sentence position.
    await this.press(ref, "Backspace");
  }

  async completeSkill(ref, name) {
    const row = `/${name}`;
    await this.waitPage(
      `(() => { const menu = document.querySelector("[data-testid='composer-slash-menu']"); if (!menu) return null;
        return [...menu.querySelectorAll("button")].some((b) => b.textContent.includes(${JSON.stringify(row)})) ? true : null; })()`,
      { label: `slash menu row ${row}` },
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
        return [...menu.querySelectorAll("button")].filter((b) => b.textContent.includes(${JSON.stringify(row)})).map((b) => { const r = b.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; }); })()`,
    );
    if (!rows || rows.length === 0) throw new Error(`no ${row} row in slash menu`);
    await this.clickAt(rows[0].x, rows[0].y);
    await this.waitPage(
      `(() => { const pane = ${this.paneScopeExpr(ref)};
        const chips = pane ? [...pane.querySelectorAll("[data-testid='composer-skill-chip']")] : [];
        return chips.some((c) => c.textContent.includes(${JSON.stringify(name)})) ? chips.map((c) => c.textContent) : null; })()`,
      { label: `chip ${name}` },
    );
    await this.waitPage(
      `(() => document.querySelector("[data-testid='composer-slash-menu']") === null ? true : null)()`,
      { label: "slash menu closed after selection" },
    );
  }

  async removeSkillChip(ref) {
    const before = await this.composerState(ref);
    const at = before.text.indexOf(`/${SKILL_NAME}`);
    check(at >= 0, "no inline skill atom to remove");
    await this.selectRange(ref, at + SKILL_NAME.length + 1);
    await this.press(ref, "Backspace");
    await this.assertComposerDraft(ref, "atomic Backspace", {
      text: before.text.slice(0, at) + before.text.slice(at + SKILL_NAME.length + 1),
      chips: before.chips.filter((chip) => chip !== `/${SKILL_NAME}`), tiles: before.tiles,
    });
  }

  // One staged attachment tile, removed through its own control. The tile's
  // buttons carry no testid beyond the tile itself (AttachmentTile renders
  // `View <name>` and `Remove <name>`), so the remove button is the one whose
  // label starts with "Remove ". The tile count is re-read between removals:
  // each click takes one tile, and the caller's loop ends when none are left.
  async removeAttachmentTile(ref) {
    const labels = await evaluate(
      this.send,
      `(() => { const pane = ${this.paneScopeExpr(ref)};
        const tiles = pane ? [...pane.querySelectorAll("[data-testid='attachment-tile']")] : [];
        const buttons = tiles
          .map((tile) => [...tile.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") ?? "").startsWith("Remove ")))
          .filter(Boolean);
        return buttons.map((b) => { const r = b.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2, label: b.getAttribute("aria-label") }; }); })()`,
    );
    if (!labels || labels.length === 0) throw new Error("no attachment remove button");
    await this.clickAt(labels[0].x, labels[0].y);
    return labels[0].label;
  }

  // Every action that SENDS the composer's draft states the draft it means to
  // send, and the composer is held to it first. A draft that arrived
  // corrupted used to be discovered much later, as a wait timing out on a
  // transcript line that could not appear; named here, the failure says which
  // characters are wrong and stops at the action that would have carried it.
  //
  // THREE actions send it, not two. Submit and steer are the obvious pair.
  // The queue strip's "Steer queue now" is the third: turn/drainAsSteer
  // "atomically appends the composer's current text/attachments (if any) to
  // the input queue, then drains the whole queue into the active turn as one
  // steering message" (stores/threads.ts:157), so a stray draft rides along
  // with rows the scenario meant to send alone. Only "Edit message" sends
  // nothing -- it returns a row TO the composer.
  // The whole payload, not just the prose: a send carries the composer's text,
  // its skill chips and its staged attachments, so an unexpected chip or a
  // stray tile is as wrong as a scrambled character and was as invisible.
  // placeholder, submitDisabled and steerVisible are not compared -- they
  // describe the control, not what it sends.
  async assertComposerDraft(ref, action, expect) {
    if (!expect || typeof expect.text !== "string" || !Array.isArray(expect.chips) || typeof expect.tiles !== "number") {
      throw new Error(`${action}(${ref}): pass the {text, chips, tiles} this action means to send`);
    }
    const state = await this.composerState(ref);
    if (!state) throw new Error(`${action}(${ref}): no composer to send from`);
    const sent = { text: state.text, chips: state.chips, tiles: state.tiles };
    const want = { text: expect.text, chips: expect.chips, tiles: expect.tiles };
    if (JSON.stringify(sent) !== JSON.stringify(want)) {
      throw new Error(`${action}(${ref}): composer holds ${JSON.stringify(sent)}, expected ${JSON.stringify(want)}`);
    }
  }

  async sendComposerDraft(ref, testId, action, expect) {
    await this.assertComposerDraft(ref, action, expect);
    await this.clickComposerAction(ref, testId);
  }

  async clickSubmit(ref, expect) {
    await this.sendComposerDraft(ref, "composer-submit", "clickSubmit", expect);
  }

  async clickSteer(ref, expect) {
    await this.sendComposerDraft(ref, "composer-steer", "clickSteer", expect);
  }

  // A composer action click is lost when a re-render lands between the mouse
  // press and release (no click event fires) — any store update that
  // re-renders the shell at that moment is enough. So each attempt is
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
      // A disabled button is evidence only of a TRANSITION — the click's own
      // actionPending — never a resting state: a click that did nothing while
      // the button sat disabled for an unrelated reason must not pass.
      if (state.submitDisabled === true && ${JSON.stringify(before.submitDisabled)} === false) return true;
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

  // A POSITIVE turn-end barrier. Every hold in the choreography is armed only
  // after this barrier, because a hold captures the NEXT provider request —
  // arming it while the previous scenario's turn still has an undispatched leg
  // (a drain's steering leg, which the daemon only sends after the held first
  // leg returns) would steal that leg: the captured request never completes,
  // the turn never ends, and the next submit silently routes to the
  // client-side queue (observed on a loaded box as exactly that: the steered
  // input sitting in the queue strip and the input-visibility wait timing
  // out).
  //
  // `steerVisible === false` alone is NOT an authoritative idle condition: it
  // is the DERIVED busy predicate (Composer.tsx's busy = isTurnActive(
  // status.type, activeTurnId) = status.type === "active" && activeTurnId), so
  // it is ALSO false when the wire reports an ACTIVE session with no
  // activeTurnId — which is exactly what a session holding queued/steering
  // work reports, and precisely where the drain's undispatched leg lives. The
  // barrier therefore gates on the session's own published state instead of
  // that derived flag:
  //   1. the session's RAW wire status, which Cadence renders straight onto its
  //      aria-label ("Working" ⟺ status.type === "active"); the barrier only
  //      releases when the session is demonstrably not working; and
  //   2. queue depth — the status row's queue indicator renders only while
  //      queueDepth > 0, so its absence is an empty WIRE queue; and
  //   3. the client-side pending-work surfaces, because (2) covers only what
  //      the wire already knows: an optimistic/pending queue row that has not
  //      been acknowledged yet, an accepted-but-unreflected send/steer/drain,
  //      or a durable outbox record still in flight are all invisible to
  //      queueDepth while they exist. Those are exactly the states PendingChips
  //      (send/steer/drain entries) and the "Queued messages" strip (queue
  //      rows, plus recovery/blocked records) render, from the same durable
  //      outbox and authoritative pending-mutation projections the durable
  //      reads use, so either one being on screen blocks the release -- and
  //      the stores those surfaces are projected from are read directly too,
  //      because a render lands a commit after the durable write and a poll
  //      can land in that gap; and
  //   4. the session's DURABLE recovery records, because (3)'s strip is not
  //      authoritative for them: QueueStrip filters out the record its own
  //      composer has taken ownership of (activeRecoveryId), so a failed
  //      request the composer has restored as a draft renders NOTHING in the
  //      strip -- and the barrier would release over a composer that is about
  //      to resend it. The store is the record's own storage and is not
  //      filtered by who owns it, so "no strip" plus "no record" is what the
  //      release means. (A record that cannot be read is not a release: the
  //      reading is retried and only times out.)
  // A session still working, or still holding any pending work, cannot release
  // the barrier even though its busy flag reads false.
  //
  // The idle reading must still SETTLE (waitPage's settleMs) before it
  // releases: a single not-idle-looking poll can land in the gap between the
  // previous turn's last leg completing and the next leg being dispatched,
  // which widens under load. The settle is a BACKSTOP, not the guarantee: every
  // turn whose last leg is a daemon-dispatched continuation (a drained queue
  // row, an interrupt steer) is additionally held to that leg's own reply by
  // waitForContinuationReply before this barrier runs, so no load-induced gap
  // can outlive the release of that wait. Reply waits cannot substitute on
  // their own: such a turn produces a reply PER leg, and the first leg's reply
  // arrives while the continuation leg is still pending.
  //
  // The session's own recovery records are read from the DURABLE store that
  // owns them, not from a render of them: QueueStrip hides the record its
  // composer has taken ownership of (activeRecoveryId), so the strip is
  // blind to a failed request that has already been restored into the
  // composer. True is returned only when the store was read AND holds no such
  // record for this session: an unreadable store (null) and a pending record
  // (false) both keep the barrier from releasing.
  noPendingRecoveryExpr(ref) {
    return `(async () => {
      const durable = await ${this.durableRecordsExpr()};
      if (!durable || !Array.isArray(durable.recovery)) return null;
      return durable.recovery.some(
        (record) => record.targetRef === ${JSON.stringify(ref)} && record.method !== "notes/human/set",
      )
        ? false
        : true;
    })()`;
  }

  // The same reading for the OTHER two durable stores the pending surfaces are
  // projected from. PendingChips and the queue strip render send/steer/drain
  // and queue entries out of the outbox and the accepted-mutation store, but
  // they render them a commit LATER than the durable write: the projection is
  // another IndexedDB round trip plus a React commit, so a poll can see a
  // quiet pane while the durable record for the session is already there. The
  // barrier must not release into that gap -- the next hold would capture the
  // request this record is about to dispatch. True only when both stores were
  // read AND hold no record for this session; an unreadable store (null) and a
  // pending record (false) both keep the barrier from releasing.
  //
  // `notes/human/set` is the one mutation kind this app never treats as
  // pending turn work (QueueStrip and Composer's fresh-recovery effect both
  // filter it out by that exact method), so it is excluded here for the same
  // reason the recovery read excludes it.
  noPendingTurnRecordExpr(ref) {
    return `(async () => {
      const durable = await ${this.durableRecordsExpr()};
      if (!durable || !Array.isArray(durable.outbox) || !Array.isArray(durable.optimistic)) return null;
      return [...durable.outbox, ...durable.optimistic].some(
        (record) => record.targetRef === ${JSON.stringify(ref)} && record.method !== "notes/human/set",
      )
        ? false
        : true;
    })()`;
  }

  // One expression, so every reading of "this session is idle" is the same
  // reading: the barrier below waits on it, and nothing else has a private
  // copy that could drift from it.
  turnIdleExpr(ref) {
    return `(async () => {
      const pane = ${this.paneScopeExpr(ref)};
      if (!pane) return null;
      const state = ${this.composerStateExpr(ref)};
      if (!state || state.steerVisible !== false) return null;
      const cadence = pane.querySelector("[data-testid='pane-cadence-slot'] [role='img']");
      if (!cadence || cadence.getAttribute("aria-label") === "Working") return null;
      if (pane.querySelector("[data-testid='status-row-queue']") !== null) return null;
      if (pane.querySelector("[data-testid='pending-chips']") !== null) return null;
      if ([...pane.querySelectorAll("h3")].some((h) => h.textContent.startsWith(${JSON.stringify(QUEUED_MESSAGES_HEADER)}))) return null;
      if ((await ${this.noPendingRecoveryExpr(ref)}) !== true) return null;
      if ((await ${this.noPendingTurnRecordExpr(ref)}) !== true) return null;
      return true;
    })()`;
  }

  async waitForTurnIdle(ref, { timeoutMs = 30000 } = {}) {
    await this.waitPage(
      this.turnIdleExpr(ref),
      {
        timeoutMs,
        settleMs: TURN_IDLE_SETTLE_MS,
        label: `previous turn ended and stayed idle for ${ref}`,
      },
    );
  }

  // clearComposerDraft takes one session's composer to the state a fresh
  // re-composition starts from, and holds it there against the app's own
  // persistence until the session's durable recovery store is EMPTY.
  //
  // Why it exists: a failed request can come back to the composer instead of
  // resting in the strip. Composer's fresh-recovery effect restores a rejected
  // record into an EMPTY composer and takes ownership of it, and QueueStrip
  // then hides the record (activeRecoveryId) -- so the strip is empty AND the
  // composer holds the failed draft, and a retry that types without looking
  // appends to it instead of composing it. Clearing is not a workaround for
  // that shape: it is the app's own reading of it.
  // queueRecoveryPersistence discards the record once the composer's text,
  // attachments and selections are all empty (Composer.tsx), so an emptied
  // composer is what "this recovery is spent" means to the app -- and the
  // record LEAVING THE DURABLE STORE is the signal that nothing can restore
  // the draft again, because the restore effect can only fire while a record
  // exists. Waiting for the store, rather than for a quiet poll window, is what
  // makes the state the retry types into final.
  //
  // ATTACHMENTS COUNT. The discard predicate requires the staged attachments
  // to be empty as well as the text and the selections, so a tile left behind
  // keeps the record -- and with it the restorable draft -- alive forever, and
  // the wait below could never see the store empty: it would time out after
  // its whole budget. Removing the tiles is therefore part of emptying the
  // composer, not a convenience.
  async clearComposerDraft(ref, { timeoutMs = 20000 } = {}) {
    const settled = await this.settleComposer(ref);
    if (settled.value !== "") {
      await this.focusComposer(ref);
      await this.selectAll(ref);
      await this.press(ref, "Backspace");
    }
    // Chips go through their own remove buttons, one at a time: the selection
    // list is what the record's persistence reads, and a chip left behind
    // keeps both the record and its restorable draft alive.
    for (let attempt = 0; attempt < 8; attempt++) {
      const chips = await evaluate(
        this.send,
        `(() => { const pane = ${this.paneScopeExpr(ref)};
          return pane
            ? [...pane.querySelectorAll("[data-testid='composer-skill-chip'] button")].filter((b) =>
                (b.getAttribute("aria-label") ?? "").startsWith(${JSON.stringify(CHIP_REMOVE_PREFIX)})).length
            : 0; })()`,
      );
      if (!chips) break;
      await this.removeSkillChip(ref);
    }
    // Attachment tiles go the same way, and for the same reason: each tile
    // carries its own remove button (AttachmentTile's `Remove <name>`), and
    // the record's discard predicate reads the staged attachment list, not the
    // rendered tiles.
    for (let attempt = 0; attempt < 8; attempt++) {
      const tiles = await evaluate(
        this.send,
        `(() => { const pane = ${this.paneScopeExpr(ref)};
          return pane
            ? [...pane.querySelectorAll("[data-testid='attachment-tile'] button")].filter((b) =>
                (b.getAttribute("aria-label") ?? "").startsWith("Remove ")).length
            : 0; })()`,
      );
      if (!tiles) break;
      await this.removeAttachmentTile(ref);
    }
    await this.waitPage(this.composerDrainedExpr(ref), {
      timeoutMs,
      label: `composer cleared (text, chips and tiles) and no pending recovery for ${ref}`,
    });
  }

  // The emptied composer, as ONE reading: text, skill chips AND attachment
  // tiles all empty, with no durable recovery record left to restore a draft
  // from. Named separately from its one wait so the state it means is
  // inspectable on its own -- the wait, a failure report and a test all read
  // the same predicate.
  composerDrainedExpr(ref) {
    return `(async () => {
      const state = ${this.composerStateExpr(ref)};
      if (!state || state.text !== "" || state.chips.length > 0 || state.tiles !== 0) return null;
      return (await ${this.noPendingRecoveryExpr(ref)}) === true ? true : null;
    })()`;
  }

  // The turn-block ids this session's pane currently renders. Captured before
  // the release that will dispatch a continuation leg, this is the baseline
  // that tells waitForContinuationReply which turns existed while that leg was
  // still undispatched -- see it for why that baseline is the wait's proof.
  //
  // Every transcript read is scoped to the session's own pane. Turn ids are
  // unique per session, not per document, and a session pane is not the only
  // place a turn block can be rendered (the stack host mounts every pane;
  // measured on this desktop host, opening session B unmounts session A, so
  // the collision below is latent rather than everyday). A document-wide sweep
  // can be satisfied by another session's pane, which would make this baseline
  // wrong in the direction that HEALS a false pass (ids from the other pane
  // make real turns look pre-existing) or a timeout (the continuation's own
  // block is treated as pre-existing).
  turnIdsExpr(ref = this.sessionA) {
    return `(() => {
      const pane = ${this.paneScopeExpr(ref)};
      if (!pane) return null;
      return [...pane.querySelectorAll("[data-testid='turn-block']")].map((el) => el.getAttribute("data-turn-id"));
    })()`;
  }

  // turnIds is a BASELINE, and a baseline that failed to read is not an empty
  // one. `[]` claims "no turns existed", which is the reading that lets
  // waitForContinuationReply release on a turn that was already running -- the
  // exact false pass the baseline exists to prevent. A missing pane, a null
  // result or a page error therefore fails here, BEFORE the release that would
  // dispatch the continuation leg it is meant to prove.
  //
  // A TRANSIENT failure is not that verdict. A single evaluate rejection under
  // load used to hard-fail the scenario this barrier exists to stabilize, so
  // an unsuccessful read is retried until it comes back or the budget expires;
  // only then does it fail, naming the last reading (or the last error) that
  // stood in for a baseline.
  async readTurnIdBaseline(exprSource, label, { timeoutMs = TURN_BASELINE_RETRY_MS } = {}) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      let last = null;
      const ids = await evaluate(this.send, exprSource).catch((error) => {
        last = `read failed: ${error}`;
        return null;
      });
      if (Array.isArray(ids)) return ids;
      if (last === null) last = `got ${JSON.stringify(ids)}`;
      if (Date.now() > deadline) {
        throw new Error(`${label}: no turn ids read after ${timeoutMs}ms (${last})`);
      }
      await new Promise((resolve) => setTimeout(resolve, 80));
    }
  }

  async turnIds(ref = this.sessionA) {
    return this.readTurnIdBaseline(this.turnIdsExpr(ref), `turnIds(${ref})`);
  }

  // waitForContinuationReply is the POSITIVE end-of-turn proof for a turn whose
  // last leg is a daemon-dispatched continuation: a drained queue row folded
  // into the running turn, or an interrupt steer. The daemon dispatches that
  // leg only AFTER the leg before it returns, and no wire event names the
  // dispatch, so an idle-looking status alone can be read in the gap before it
  // (a gap that widens with load). What the wire does publish, in order, is the
  // continuation's OWN turn boundary and then the continuation input itself: a
  // steering item opens no turn (`EventSteeringInjected` projects to a bare
  // notification), so the daemon announces `EventTurnStarted` for the turn that
  // will carry it BEFORE the injection, precisely so that content is not
  // attributed to the turn before it (agent/session_lifecycle.go's
  // acceptNotificationInput and acceptSteeringCarrierInput both say so at the
  // emit site; injectDrainedSteering consumes the message only after). Two
  // orderings follow, and this wait holds the continuation to both:
  //
  //   1. The continuation text lands in a turn block that did NOT exist when
  //      the release was issued, because the turn it belongs to is opened at
  //      that boundary. `turnsBefore` is `turnIds()` captured before the
  //      release; a turn block whose id is in it is a turn that was already
  //      running, so nothing in it can be proof that the continuation leg ran.
  //      The held leg's own reply lives in exactly such a block -- verified
  //      frame by frame on a live run: the block that first receives the
  //      continuation text appears with ZERO replies in it, while the held
  //      leg's reply is already rendered in the block before it.
  //   2. Inside that new turn, the continuation's own input is followed by a
  //      reply. TurnBlock renders one turn's items in wire order, and the reply
  //      to the continuation can only be recorded after the daemon dispatched
  //      that leg, so a sentinel reply AFTER this text is a reply to it.
  //
  // What this wait must NOT be is "some turn block holds the text and the
  // sentinel reply" -- the shape it used to have. Every scripted leg answers
  // with the SAME sentinel, so that check is satisfied by any earlier reply
  // sharing a block with the continuation text, and it reads as a completion
  // proof only because this app currently renders the continuation in a block
  // of its own. That is a layout fact, not a published ordering: had the
  // continuation been folded into the running turn's block -- the failure the
  // review named, where the held leg's reply is already there when the steering
  // item is injected -- the old check would have released on the HELD leg's
  // reply, before the continuation leg's request had even been dispatched. With
  // the turn identity and the item order both required, that same page state
  // fails the wait loudly instead of passing it.
  //
  // `text` is the continuation input the scenario submitted.
  //
  // The whole check runs inside the TARGET SESSION'S PANE. Turn ids are
  // unique per session, not per document, so a document-wide sweep can be
  // satisfied by a turn block rendered anywhere else -- above all another
  // session's pane (identical scripted text is the norm here, since every leg
  // answers with the same sentinel). That either confirms a pass this session
  // never produced or hides the continuation behind an id the baseline
  // collected from a pane the release never touched.
  async waitForContinuationReply(text, turnsBefore, { timeoutMs = 30000, ref = this.sessionA } = {}) {
    if (!Array.isArray(turnsBefore)) {
      throw new Error("waitForContinuationReply needs the turnIds() captured before the release that dispatches the continuation");
    }
    const expr = `(() => {
      const pane = ${this.paneScopeExpr(ref)};
      if (!pane) return null;
      const before = new Set(${JSON.stringify(turnsBefore)});
      return [...pane.querySelectorAll("[data-testid='turn-block']")].some((el) => {
        const id = el.getAttribute("data-turn-id");
        if (!id || before.has(id)) return false;
        const block = el.textContent ?? "";
        const at = block.indexOf(${JSON.stringify(text)});
        if (at < 0) return false;
        // The reply must follow the continuation TEXT, not merely follow its
        // first character: slice from the end of the run itself ('at + 1'
        // scanned the continuation's own tail, harmless only while the two
        // sentinels stay disjoint -- a continuation whose text ever carried
        // the reply sentinel would release on the input's own text, before the
        // continuation leg's reply existed, which is exactly the premature
        // release this wait was rewritten to prevent).
        return block.slice(at + ${text.length}).includes(${JSON.stringify(REPLY_TEXT)});
      }) ? true : null; })()`;
    try {
      await this.waitPage(expr, {
        timeoutMs,
        label: `the continuation leg carrying ${JSON.stringify(text)} to complete in its own turn`,
      });
    } catch (error) {
      throw await this.dumpPageState(ref, error);
    }
  }

  // clickQueueStripControl applies the composer-action lesson (see
  // clickComposerAction's own hazard comment) to the queue strip's
  // un-testid'd controls: the strip re-renders as its durable records settle,
  // so a single-shot click — or the element query itself — racing that
  // re-render fails outright. Each click is verified by an OBSERVED effect
  // and retried; a re-click while the first landed is harmless because the
  // strip's own busy state disables the control synchronously.
  async clickQueueStripControl({ locate, effectExpr, label, attempts = 4 }) {
    for (let attempt = 1; attempt <= attempts; attempt++) {
      try {
        await locate();
      } catch (clickError) {
        // The control vanishing is not yet a failure: the click may have just
        // landed (the strip updated), or a re-render may be remounting the
        // row. Only the effect decides.
        const landed = await this.waitPage(effectExpr, {
          timeoutMs: 2000,
          label: `${label} effect after the control vanished (attempt ${attempt}/${attempts})`,
        }).catch(() => false);
        if (landed) return;
        if (attempt === attempts) throw clickError;
        continue;
      }
      const landed = await this.waitPage(effectExpr, {
        timeoutMs: attempt === attempts ? 3000 : 1500,
        label: `${label} click to take effect (attempt ${attempt}/${attempts})`,
      }).catch(() => false);
      if (landed) return;
      if (attempt < attempts) console.error(`skillguard: ${label}: click ${attempt} had no observed effect; re-clicking`);
    }
    const diagnosis = await evaluate(
      this.send,
      `(() => { const strip = ${this.queueStripExpr()}; return strip ? JSON.stringify(strip) : "queue strip not rendered"; })()`,
    ).catch((error) => `diagnosis failed: ${error}`);
    throw new Error(`${label} click never took effect: ${diagnosis}`);
  }

  // turnTextsExpr evaluates to one session's mounted transcript text per turn:
  // the pane's turn-blocks that share a data-turn-id, concatenated, because a
  // turn can render as several segments (TranscriptBody's turnRow). Scoped to
  // the pane because turn ids are session-local and two sessions are mounted.
  // Only the rows near the end are mounted, and which ones changes with the
  // footer's height, so a wait must ask about a turn's own content, never
  // about which rows exist.
  turnTextsExpr(ref) {
    return `(() => { const byTurn = new Map();
      const pane = ${this.paneScopeExpr(ref)};
      for (const el of pane ? pane.querySelectorAll("[data-testid='turn-block']") : []) {
        const id = el.getAttribute("data-turn-id");
        byTurn.set(id, (byTurn.get(id) ?? "") + (el.textContent ?? ""));
      }
      return [...byTurn.values()]; })()`;
  }

  // Every scripted turn replies with the SAME sentinel, so a reply is only
  // evidence of THIS turn when it sits in the turn that carries the submitted
  // prose; a baseline of mounted reply rows cannot tell a new reply from an
  // older row mounting back in.
  async waitForReply(ref, prose, { timeoutMs = 25000 } = {}) {
    await this.waitPage(
      `(() => ${this.turnTextsExpr(ref)}.some((text) => text.includes(${JSON.stringify(prose)}) && text.includes(${JSON.stringify(REPLY_TEXT)})) ? true : null)()`,
      { timeoutMs, label: `the reply to ${JSON.stringify(prose)} in ${ref}` },
    );
  }

  // dumpPageState is the failure report both transcript waits owe on a timeout:
  // this condition is fed by a daemon push, so the useful question when it fails
  // is not "how long did we wait" but "which of a late render, a queue-routed
  // submit, or a wrong-session pane happened" -- and that is only answerable
  // from the page, not the label. It RETURNS the caller's error with what the
  // transcript, the queue strip and the composer actually showed appended, and
  // the caller throws it: a report that threw on its own left the caller
  // depending on that throw to fail its wait, so a dump that ever returned
  // normally would have turned the timeout into a silent pass. Returning makes
  // the propagation total -- the caller has the error in hand and rethrows it
  // on every path.
  //
  // `ref` names the session the dump is ABOUT: two composers can be mounted,
  // so a dump that assumed one of them would answer about the wrong session,
  // and every read here is scoped to that session's own pane for the same
  // reason (with more than one pane mounted, a document-wide sweep reports
  // whichever session rendered a match -- which is how a failure in this
  // session gets diagnosed using another session's turns or queue).
  async dumpPageState(ref, error) {
    const [blocks, blockItems, queue, composer] = await Promise.all([
      evaluate(this.send, this.transcriptBlocksExpr(ref)).catch((e) => `turn-block read failed: ${e}`),
      evaluate(this.send, this.transcriptBlockItemsExpr(ref)).catch((e) => `turn-block items read failed: ${e}`),
      evaluate(this.send, this.queueStripExpr(ref)).catch((e) => `queue read failed: ${e}`),
      this.composerState(ref).catch((e) => `composer read failed: ${e}`),
    ]);
    return new Error(
      `${error instanceof Error ? error.message : String(error)}\n` +
        `  transcript turn-blocks: ${JSON.stringify(blocks)}\n` +
        `  turn-block items (id: testids): ${JSON.stringify(blockItems)}\n` +
        `  queue strip: ${JSON.stringify(queue)}\n` +
        `  composer: ${JSON.stringify(composer)}`,
    );
  }

  // transcriptBlockItemsExpr lists, per turn block in the pane, the turn id and
  // the data-testid of every descendant that carries one -- which turn opened
  // and which items landed in it, the question a continuation-leg failure asks.
  transcriptBlockItemsExpr(ref) {
    return `(() => {
      const pane = ${this.paneScopeExpr(ref)};
      if (!pane) return null;
      return [...pane.querySelectorAll("[data-testid='turn-block']")].map((el) => ({
        id: el.getAttribute("data-turn-id"),
        items: [...el.querySelectorAll("[data-testid]")].map((n) => n.getAttribute("data-testid")),
      }));
    })()`;
  }

  // waitForTranscriptInput waits for `text` to appear in a rendered
  // turn-block. On timeout it dumps what the transcript, the queue strip and
  // the composer actually showed: this condition is fed by a daemon push, so
  // the useful question when it fails is not "how long did we wait" but
  // "which of a late render, a queue-routed submit, or a wrong-session pane
  // happened" — and that is only answerable from the page, not the label.
  // `ref` names the SESSION whose pane is searched and whose composer the
  // timeout dump reads; it defaults to sessionA (this helper's current call
  // site) so a reuse for sessionB asks about B, not A. The search is scoped to
  // that pane for the same reason the continuation wait is: the scripted text
  // is identical across panes by construction, so a document-wide sweep can be
  // satisfied by a turn in the other mounted session while the session under
  // test has rendered nothing. It reads the pane's turns the way every other
  // transcript wait does (turnTextsExpr): one turn's segments are concatenated,
  // so a text the virtualized transcript split across two rows still matches.
  async waitForTranscriptInput(text, { timeoutMs = STEERED_TURN_INPUT_TIMEOUT_MS, ref = this.sessionA } = {}) {
    try {
      await this.waitPage(
        `(() => ${this.turnTextsExpr(ref)}.some((turn) => turn.includes(${JSON.stringify(text)})) ? true : null)()`,
        { timeoutMs, label: `input ${JSON.stringify(text)} visible in the transcript` },
      );
    } catch (error) {
      throw await this.dumpPageState(ref, error);
    }
  }
}

function check(condition, message) {
  if (!condition) throw new Error(message);
}

// These gestures operate on the production ProseMirror instance through
// Chrome. No fixture editor, application hooks, or programmatic transactions.
async function runInlineEditing(driver) {
  const ref = driver.sessionA;
  const two = { text: TWO_SKILLS, chips: ["/skill-1", "/skill-2"], tiles: 0 };
  await driver.waitForTurnIdle(ref);
  const modifier = await evaluate(driver.send, `/Mac/.test(navigator.platform) ? 4 : 2`);
  await driver.focusComposer(ref);
  // Resolve the first reference with an EXISTING whitespace suffix: completion
  // must not add a second separator or shift the surrounding sentence.
  await driver.typeText(ref, "Run  and then ");
  const firstStart = TWO_SKILLS.indexOf("/skill-1");
  const firstEnd = firstStart + "/skill-1".length;
  await driver.selectRange(ref, firstStart);
  await driver.typeText(ref, "/skill-1");
  await driver.completeSkill(ref, "skill-1");
  check((await driver.composerState(ref)).text === "Run /skill-1 and then ", "completion doubled the existing separator");
  await driver.selectRange(ref, TWO_SKILLS.indexOf("/skill-2"));
  await driver.typeText(ref, "/skill-2");
  await driver.completeSkill(ref, "skill-2");
  await driver.assertComposerDraft(ref, "completion separator at end", { ...two, text: `${TWO_SKILLS} ` });
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "two original inline positions", two);

  // A caret can cross the atom but cannot enter its label. Inserting on each
  // side must leave the complete canonical reference intact.
  await driver.selectRange(ref, firstStart);
  await driver.moveCaret(ref, "ArrowRight");
  let caret = await evaluate(driver.send, driver.composerEditStateExpr(ref));
  check(caret.start === firstEnd && caret.end === firstEnd, `right arrow entered an atom: ${JSON.stringify(caret)}`);
  await driver.moveCaret(ref, "ArrowLeft");
  caret = await evaluate(driver.send, driver.composerEditStateExpr(ref));
  check(caret.start === firstStart && caret.end === firstStart, `left arrow entered an atom: ${JSON.stringify(caret)}`);

  await driver.selectRange(ref, firstEnd);
  await driver.press(ref, "Backspace");
  const removed = { ...two, text: "Run  and then /skill-2", chips: ["/skill-2"] };
  await driver.assertComposerDraft(ref, "single Backspace removes atom", removed);
  await driver.press(ref, "z", modifier);
  await driver.assertComposerDraft(ref, "native undo restores atom and metadata", two);
  await driver.press(ref, "z", modifier | 8);
  await driver.assertComposerDraft(ref, "native redo removes atom", removed);
  await driver.press(ref, "z", modifier);
  await driver.selectRange(ref, firstStart);
  await driver.press(ref, "Delete");
  await driver.assertComposerDraft(ref, "single Delete removes atom", removed);
  await driver.press(ref, "z", modifier);
  await driver.assertComposerDraft(ref, "undo Delete", two);

  await driver.selectRange(ref, firstStart, firstEnd);
  await driver.typeText(ref, "REPLACED");
  await driver.assertComposerDraft(ref, "selection replaces whole atom", { ...removed, text: "Run REPLACED and then /skill-2" });
  await driver.press(ref, "z", modifier);
  await driver.assertComposerDraft(ref, "undo selection replacement", two);

  await driver.selectRange(ref, firstStart);
  // Both runs end in a token character, so each is separated from the label
  // rather than allowed to swallow it; the offsets below include that space.
  await driver.typeTextAgainstAtom(ref, "BEFORE_", "before");
  await driver.selectRange(ref, firstEnd + "BEFORE_".length + 1);
  await driver.typeTextAgainstAtom(ref, "_AFTER", "after");
  await driver.assertComposerDraft(ref, "typing around atom", { ...two, text: "Run BEFORE_ /skill-1 _AFTER and then /skill-2" });
  await driver.selectRange(
    ref,
    firstEnd + "BEFORE_".length + 1,
    firstEnd + "BEFORE_".length + 1 + "_AFTER".length + 1,
  );
  await driver.press(ref, "Backspace");
  await driver.selectRange(ref, firstStart, firstStart + "BEFORE_".length + 1);
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "surrounding edits preserve atoms", two);


  // The OS clipboard needs a permission the guard's Chrome does not have on
  // macOS (#1669), so the milestone drives the same two events the browser
  // would fire, each carrying a real DataTransfer, instead of the copy/paste
  // chords. The editor's serializer and importer are still under test: the copy
  // event must emit the canonical display text, and the paste event must import
  // that text as plain prose only, never activation metadata. Original atoms
  // must survive while their pasted labels remain ordinary text.
  await driver.selectAll(ref);
  const copied = await driver.copySelection(ref);
  check(copied === TWO_SKILLS, `copy serialized ${JSON.stringify(copied)}, expected ${JSON.stringify(TWO_SKILLS)}`);
  await driver.focusComposer(ref);
  await driver.typeText(ref, " ");
  await driver.pasteText(ref, copied);
  const pastedText = `${TWO_SKILLS} ${TWO_SKILLS}`;
  await driver.waitPage(`(() => { const state = ${driver.composerStateExpr(ref)};
    return state && state.text === ${JSON.stringify(pastedText)} ? state : null; })()`,
    { label: "native clipboard preserves canonical display text" });
  await driver.assertComposerDraft(ref, "paste text without importing activation", { ...two, text: pastedText });
  await driver.selectRange(ref, TWO_SKILLS.length, pastedText.length);
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "original atoms survive clipboard edits", two);
  driver.milestone("inline-editing", await driver.composerState(ref));

  // Wrapping and scrolling are real browser layout, not jsdom geometry. Long
  // prose forces a wrap and then the editor's normal maximum-height scroller.
  const secondStart = TWO_SKILLS.indexOf("/skill-2");
  await driver.selectRange(ref, secondStart);
  const longText = "WRAP_SCROLL_14k ".repeat(160);
  await driver.typeText(ref, longText);
  const layout = await driver.waitPage(`(() => {
    const editor = ${driver.editorExpr(ref)};
    const chips = [...editor.querySelectorAll("[data-testid='composer-skill-chip']")];
    const rect = editor.getBoundingClientRect();
    const range = document.createRange(); range.selectNodeContents(editor);
    const lines = [...range.getClientRects()];
    editor.scrollTop = editor.scrollHeight;
    return { wrapped: lines.some((r) => r.top > lines[0].top),
      chipWrapped: chips.length === 2 && chips[1].getBoundingClientRect().top > chips[0].getBoundingClientRect().top,
      horizontalOverflow: editor.scrollWidth > editor.clientWidth + 1,
      heightBounded: rect.height <= window.innerHeight * 0.5,
      atoms: chips.map((chip) => ({ rects: chip.getClientRects().length, width: chip.getBoundingClientRect().width, inside: chip.getBoundingClientRect().right <= rect.right + 1 })),
      scrollTop: editor.scrollTop, scrollHeight: editor.scrollHeight, clientHeight: editor.clientHeight };
  })()`, { label: "composer wrapping and scrollable overflow" });
  check(layout.wrapped && layout.chipWrapped && layout.heightBounded && layout.atoms.length === 2 && !layout.horizontalOverflow && layout.atoms.every((a) => a.rects === 1 && a.width > 0 && a.inside) && layout.scrollTop > 0,
    `inline wrap/scroll layout failed: ${JSON.stringify(layout)}`);
  driver.milestone("inline-layout", layout);
  // Remove only the wrapping prose, retaining both original atoms.
  await driver.selectRange(ref, secondStart, secondStart + longText.length);
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "remove wrapping prose", two);

  // CDP starts a genuine browser composition. Enter with the IME's legacy
  // 229 code must not submit the message or accept a slash completion.
  await driver.focusComposer(ref);
  await driver.send("Input.imeSetComposition", { text: "あ", selectionStart: 1, selectionEnd: 1 });
  await driver.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 229, nativeVirtualKeyCode: 229 });
  await driver.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 229, nativeVirtualKeyCode: 229 });
  await driver.send("Input.insertText", { text: "あ" });
  // Composed straight against the last chip, which is exactly where a token
  // character has to be separated from it.
  await driver.assertComposerDraft(ref, "IME Enter retains draft", { ...two, text: `${TWO_SKILLS} あ` });
  const ime = await driver.composerState(ref);
  check(!ime.steerVisible, `IME Enter started a turn: ${JSON.stringify(ime)}`);
  const durable = await evaluate(driver.send, driver.durableRecordsExpr());
  check(![...durable.outbox, ...durable.optimistic, ...durable.recovery].some((record) => JSON.stringify(record.input).includes("あ")), "IME Enter persisted a submit");
  driver.milestone("inline-ime", ime);
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "composition cleanup", { ...two, text: `${TWO_SKILLS} ` });
  // The composed character sat directly against the last chip, so the editor
  // separated the two; drop that separator as well and the draft is back where
  // it started.
  await driver.press(ref, "Backspace");
  await driver.assertComposerDraft(ref, "composition cleanup separator", two);

  await driver.waitForTurnIdle(ref);
  driver.milestone("submitted-two-skills", await driver.composerState(ref));
  await driver.clickSubmit(ref, two);
  await driver.waitForComposerCleared(ref);
  // Match on the submitted prose: every scripted turn replies with the same
  // sentinel, so only the turn carrying this sentence is evidence of its reply.
  await driver.waitForReply(ref, TWO_SKILLS);
  await driver.waitPage(`(() => [...document.querySelectorAll("[data-testid='turn-block']")].some((el) => el.textContent.includes(${JSON.stringify(TWO_SKILLS)})) ? true : null)()`,
    { label: "exact two-reference sentence in visible transcript" });
}

async function runScenarios(driver) {
  // ---- prelude: auth + app shell ----
  await navigateTo(driver.page, driver.url);
  await driver.waitPage(`(() => document.querySelector("[data-testid='rail-brand']") !== null ? true : null)()`, {
    timeoutMs: 30000,
    label: "app shell (rail brand)",
  });
  const rows = await driver.waitPage(driver.railRowsExpr({ atLeast: 2 }), {
    timeoutMs: 30000,
    label: "two live sessions in the rail",
  });
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
  await driver.typeText(driver.sessionA, PROSE.canonical);
  await driver.selectSkillChip(driver.sessionA);
  let state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 1 && state.chips[0].includes(SKILL_NAME), `chip missing after selection: ${JSON.stringify(state)}`);
  check(state.text === inlineText(PROSE.canonical), `selection changed the prose: ${JSON.stringify(state.text)}`);
  driver.milestone("chip-added", state);
  await driver.removeSkillChip(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 0, "chip not removed");
  check(state.text === `${PROSE.canonical} `, "removal changed the surrounding prose");
  driver.milestone("chip-removed", state);
  await driver.selectSkillChip(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.chips.length === 1, "chip not re-selected");
  driver.milestone("chip-reselected", state);
  driver.milestone("chip-labels", {
    chipText: state.chips,
    removeLabels: state.removeLabels,
    chipDetails: state.chipDetails,
    prose: state.text,
  });
  // The durable outbox record is TRANSIENT — removed at the daemon's
  // turn/start ACK, the same event that clears the composer — so this read
  // races it and may legitimately capture an already-drained outbox. The
  // durable-evidence assertions that cannot race live in the transcript's
  // skill_state record and the transport scenario's stalled-transport
  // capture; a record the driver did catch must still carry prose and skill.
  // Turn-end barrier: instant here (the session has never run a turn), kept
  // so every turn/start submit in the choreography is barriered by
  // construction (see waitForTurnIdle).
  await driver.waitForTurnIdle(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.canonical));
  const durable = await evaluate(driver.send, driver.durableRecordsExpr()).catch(() => ({}));
  driver.milestone("durable-mutation", durable);
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("submitted-canonical", { ref: driver.sessionA, prose: PROSE.canonical });
  driver.milestone("draft-after-commit", await evaluate(driver.send, driver.draftStorageExpr()));
  await driver.waitForReply(driver.sessionA, PROSE.canonical);

  await runInlineEditing(driver);

  // ---- scenario: draft thread-switch / remount ----
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.draft);
  await driver.selectSkillChip(driver.sessionA);
  driver.milestone("draft-staged", await driver.composerState(driver.sessionA));
  await driver.openSession(driver.sessionB);
  driver.milestone("thread-switched", { ref: driver.sessionB });
  const bState = await driver.composerState(driver.sessionB);
  check(bState.text === "", `session B composer not fresh: ${JSON.stringify(bState)}`);
  await driver.openSession(driver.sessionA);
  state = await driver.composerState(driver.sessionA);
  check(state.text === inlineText(PROSE.draft), `draft text did not survive the switch: ${JSON.stringify(state)}`);
  check(state.chips.some((c) => c.includes(SKILL_NAME)), `draft chips did not survive the switch: ${JSON.stringify(state)}`);
  driver.milestone("draft-remounted", { ...state, storage: await evaluate(driver.send, driver.draftStorageExpr()) });
  // Select-all clears the text and inline atoms in one operation.
  await driver.focusComposer(driver.sessionA);
  await driver.selectAll(driver.sessionA);
  await driver.press(driver.sessionA, "Backspace");
  state = await driver.composerState(driver.sessionA);
  check(state.text === "" && state.chips.length === 0, `draft not cleared: ${JSON.stringify(state)}`);
  driver.milestone("draft-cleared", state);

  // ---- scenario: queue edit / return / drain ----
  // Turn-end barrier: the hold below captures the NEXT provider request, so
  // the canonical scenario's turn must be fully over first (see
  // waitForTurnIdle).
  await driver.waitForTurnIdle(driver.sessionA);
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.queueTurn);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.queueTurn, { chips: [] }));
  // The daemon's ACK clears the composer; wait for it before typing again —
  // clearIfUnchanged deliberately keeps a draft that was edited before the
  // ACK landed, so typing too early would strand the queue prose.
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  driver.milestone("hold-turn-started", { prose: PROSE.queueTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.queue1);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.queue1));
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
  // leaves the queue with its inline reference intact. The
  // click is effect-verified and retried: the strip re-renders as its
  // durable record settles and a single-shot click can be lost outright.
  await driver.clickQueueStripControl({
    locate: () => driver.click("button[aria-label='Edit message']"),
    effectExpr: `(() => { const state = ${driver.composerStateExpr(driver.sessionA)};
      return state && state.text.includes(${JSON.stringify(PROSE.queue1)}) ? true : null; })()`,
    label: "Edit message",
  });
  const returned = await driver.composerState(driver.sessionA);
  check(returned.text === inlineText(PROSE.queue1), `queue edit moved the inline text: ${JSON.stringify(returned)}`);
  driver.milestone("queue-returned", returned);
  await driver.selectAll(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.queue2);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.queue2));
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
  // The drain commits durably before the release below lets the held turn
  // finish; the queue strip empties once the daemon has folded the queue into
  // the running turn. The click is effect-verified and retried — a lost
  // click here strands the whole choreography behind a held turn.
  //
  // It also sends whatever the composer holds, appended to the queue, so this
  // scenario's "the queue drained" assertions are only about the queue while
  // the composer is empty. Held to that here rather than discovered as an
  // extra steering message in the provider requests.
  await driver.assertComposerDraft(driver.sessionA, "drainAsSteer", draft("", { chips: [] }));
  await driver.clickQueueStripControl({
    locate: () => driver.clickByText("Steer queue now"),
    effectExpr: `(() => { const strip = ${driver.queueStripExpr()}; return strip === null || strip.rows.length === 0 ? true : null; })()`,
    label: "Steer queue now (drain)",
  });
  driver.milestone("drain-committed", {
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  // The turns already rendered while the drain's continuation leg is still
  // undispatched: waitForContinuationReply requires the continuation to land in
  // a turn that is NOT one of these -- read from session A's own pane, the
  // session this scenario drives (every read below is pane-scoped).
  const drainTurns = await driver.turnIds(driver.sessionA);
  driver.control("release");
  // The reply wait below returns on the HELD leg's reply, which arrives while
  // the drain's steering leg is still pending: the release unblocks the held
  // provider call, the daemon folds the queue in at that turn boundary, and
  // only then dispatches the continuation leg. Wait for that leg's own reply
  // (the drained text's turn carrying the scripted answer) so the turn is
  // provably over before the next hold is armed — no fixed settling gap.
  await driver.waitForReply(driver.sessionA, PROSE.queueTurn);
  await driver.waitForContinuationReply(PROSE.queue2, drainTurns, { ref: driver.sessionA });
  driver.milestone("drain-released", {
    turnsBefore: drainTurns,
    blocks: await evaluate(driver.send, driver.transcriptBlockItemsExpr(driver.sessionA)),
  });
}

async function runScenariosPart2(driver) {
  // ---- scenario: selected steering ----
  // Turn-end barrier: the drain from the previous scenario folded its queued
  // input into the running turn as a steering leg the daemon dispatches only
  // AFTER the held first leg returned — the first leg's reply arriving did NOT
  // mean the turn was over, which is why the scenario above holds the turn to
  // that leg's own reply before this barrier. Arming this hold with that leg
  // undispatched would capture it (verified failure mode: the captured leg
  // never completes, the turn never ends, the steering submit silently routes
  // to the client-side queue). The barrier itself still re-checks the daemon's
  // published status and the session's pending work.
  await driver.waitForTurnIdle(driver.sessionA);
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.steerTurn);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.steerTurn, { chips: [] }));
  // Wait for the ACK's composer clear before typing the steer prose (same
  // clearIfUnchanged race as the queue scenario's hold turn).
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  // Steer only once the long turn's input is committed to the visible
  // transcript: steering before the daemon records the input folds the two
  // texts into ONE user message (interrupt semantics), which is a different
  // shape than the one this scenario's provider assertions describe.
  // The input is rendered by the daemon's own turn/start push, not by the
  // submit ACK, so it can lag the composer clear by a full round trip; and if
  // the previous turn was not genuinely over when this submit landed, the send
  // routes to the client queue and becomes a queued row that never turns into a
  // transcript turn at all. The wait is therefore generous and bounded, and its
  // failure names what the page actually showed instead of only the label.
  await driver.waitForTranscriptInput(PROSE.steerTurn, { ref: driver.sessionA });
  driver.milestone("steer-turn-started", { prose: PROSE.steerTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.steer);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSteer(driver.sessionA, draft(PROSE.steer));
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("steered", { prose: PROSE.steer });
  // Same baseline as the drain: the held turn is on screen while the interrupt
  // leg is still undispatched (see waitForContinuationReply).
  const steerTurns = await driver.turnIds(driver.sessionA);
  driver.control("release");
  // Same two-leg shape as the drain above: the release answers the HELD leg
  // first, and the interrupt leg the daemon dispatches after it is the one the
  // next hold could steal. Its own reply in the transcript is the end-of-turn
  // proof, independent of how long the dispatch takes.
  await driver.waitForReply(driver.sessionA, PROSE.steerTurn);
  await driver.waitForContinuationReply(PROSE.steer, steerTurns, { ref: driver.sessionA });
  driver.milestone("steer-released", {});

  // ---- scenario: attachment preservation ----
  // Turn-end barrier: this submit must route to turn/start, so the steering
  // scenario's turn (interrupt leg included) must be fully over — which the
  // continuation-reply wait above already proved, and the barrier re-checks
  // against the daemon's published status and the session's pending work.
  await driver.waitForTurnIdle(driver.sessionA);
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
  await driver.typeText(driver.sessionA, PROSE.attachment);
  await driver.selectSkillChip(driver.sessionA);
  await driver.removeSkillChip(driver.sessionA);
  await driver.selectSkillChip(driver.sessionA);
  const attachState = await driver.composerState(driver.sessionA);
  check(attachState.tiles === 1, `attachment tile lost across chip edits: ${JSON.stringify(attachState)}`);
  check(attachState.text.includes("[image 1]"), `attachment anchor missing: ${JSON.stringify(attachState.text)}`);
  driver.milestone("attachment-preserved", attachState);
  await driver.clickSubmit(driver.sessionA, draft(attachState.text, { chips: attachState.chips, tiles: attachState.tiles }));
  await driver.waitForComposerCleared(driver.sessionA);
  driver.milestone("attachment-submitted", {
    prose: PROSE.attachment,
    durable: await evaluate(driver.send, driver.durableRecordsExpr()),
  });
  await driver.waitForReply(driver.sessionA, PROSE.attachment);

  // No capability-loss scenario: see skillGuardAssert in
  // skill_composer_browser_test.go for why, and for where the composer's
  // skillInput refusal is covered instead.

  // ---- scenario: failed activation + explicit retry ----
  await driver.openSession(driver.sessionA);
  // Turn-end barrier before arming the hold (see waitForTurnIdle).
  await driver.waitForTurnIdle(driver.sessionA);
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.failTurn);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.failTurn, { chips: [] }));
  // Wait for the ACK's composer clear before typing the failing claim (same
  // clearIfUnchanged race as every other held turn-start).
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForActiveTurn(driver.sessionA);
  driver.milestone("fail-turn-started", { prose: PROSE.failTurn });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.fail);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.fail));
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
  //
  // The failed request can be waiting in the COMPOSER rather than the strip: a
  // rejected recovery record the composer has taken ownership of is filtered
  // out of the strip (QueueStrip drops the record whose clientMutationId is
  // activeRecoveryId) while its draft sits in the textarea, and a retry that
  // simply typed would append to it. So the composer is taken to the state the
  // retry composes from FIRST -- clearComposerDraft empties it and waits for
  // the session's durable recovery store to empty too -- and only then does
  // the barrier run (it re-reads the same store) and the retry type. Appending
  // is impossible from both sides: the draft is gone, and so is the record the
  // app restores drafts from.
  await driver.clearComposerDraft(driver.sessionA);
  await driver.waitForTurnIdle(driver.sessionA);
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.fail);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.fail));
  await driver.waitForComposerCleared(driver.sessionA);
  await driver.waitForReply(driver.sessionA, PROSE.fail);
  driver.milestone("fail-retried", { prose: PROSE.fail });

  // ---- scenario: delayed accepted-send vs newer chip edit ----
  // Turn-end barrier before arming the hold (see waitForTurnIdle): the
  // failed-activation retry's turn must be fully over.
  await driver.waitForTurnIdle(driver.sessionA);
  driver.control("hold");
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.delay);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.delay));
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
  await driver.typeText(driver.sessionA, ` ${PROSE.delayExtra}`);
  await driver.selectSkillChip(driver.sessionA);
  const editedState = await driver.composerState(driver.sessionA);
  check(editedState.text.includes(PROSE.delayExtra), `newer draft lost the typed edit: ${JSON.stringify(editedState)}`);
  driver.milestone("delay-edited", editedState);
  await driver.removeSkillChip(driver.sessionA);
  driver.control("release");
  await driver.waitForReply(driver.sessionA, PROSE.delay);
  const keptState = await driver.composerState(driver.sessionA);
  check(keptState.text.includes(PROSE.delayExtra), `delayed commit clobbered the newer draft: ${JSON.stringify(keptState)}`);
  check(keptState.chips.length === 0, `delayed commit restored the removed chip: ${JSON.stringify(keptState)}`);
  driver.milestone("delay-commit-kept", keptState);
  await driver.focusComposer(driver.sessionA);
  await driver.selectAll(driver.sessionA);
  await driver.press(driver.sessionA, "Backspace");

  // ---- scenario: transport loss + recovery ----
  // Turn-end barrier: the offline submit must persist as a turn/start
  // mutation, so the delayed-send turn must be fully over.
  await driver.waitForTurnIdle(driver.sessionA);
  await driver.send("Network.emulateNetworkConditions", { offline: true, latency: 0, downloadThroughput: 0, uploadThroughput: 0 });
  await driver.focusComposer(driver.sessionA);
  await driver.typeText(driver.sessionA, PROSE.transport);
  await driver.selectSkillChip(driver.sessionA);
  await driver.clickSubmit(driver.sessionA, draft(PROSE.transport));
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
  await driver.waitForReply(driver.sessionA, PROSE.transport, { timeoutMs: 45000 });
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
  // The Go owner runs this driver context-bound and cancels the context on
  // ANY test exit path, sending SIGTERM. Node runs no cleanup on an
  // unhandled signal and the driver's own finally-block would never fire, so
  // its Chrome — spawned DETACHED in its own process group, unreachable from
  // the Go side — would leak. Handle the signal here: stop the browser once,
  // then exit with the signal's conventional code. A second signal during
  // the cleanup exits immediately.
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

// Only run the guard when invoked as the entrypoint: importing this module
// (the unit test beside it) must not start Chrome.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
