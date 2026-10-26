#!/usr/bin/env node
// backgroundtranscriptguard — the whole-journey proof for background Live
// transcripts. It drives the REAL hub web app in Vite development mode in real
// Chrome through a REAL hub (started by TestBackgroundTranscriptsEntrySliceBrowser
// in cmd/evener-hub/background_transcripts_browser_test.go) against two REAL
// `evener serve` daemons. The only scripted piece is the external LLM provider
// adapter (cmd/evener/skill_browser_helper_test.go); the driver never invents a
// product endpoint.
//
// It reuses the skillguard driver's control-file format, CDP utilities and its
// exported Driver (openSession / composer gestures / milestone writing), and adds
// only the background-journey phases. Fixture phase commands are
// {"command":"background-phase","phase":...} and are acknowledged by the helper
// through the request log the Go owner reads.
//
// Arguments (validated before Chrome starts):
//   --url            the hub's real /auth/<token> URL to authenticate first
//   --hub-url        the fixture hub base URL; exported as EVENER_HUB_URL for
//                    the Vite proxy BEFORE Vite starts
//   --artifact-dir   where screenshots/dumps survive for triage
//   --control-path-a helper alpha's fixture control JSONL
//   --control-path-b helper beta's fixture control JSONL
//   --trace-path     the real hub AppWire trace JSONL (read-only offsets)
//   --milestone-path JSONL the driver appends milestones to
//   --session-a      local:<id> of the foreground sustained session
//   --session-b      local:<id> of the background target session
import { appendFileSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  applyViewport,
  closePage,
  connectPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  openPage,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";
import { Driver } from "../skillguard/run.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const PROFILE_PREFIX = "backgroundtranscriptguard-chrome-";
const VIEWPORT = { width: 1440, height: 1000 };
// At or below 899px the desktop RailHost is not mounted and the shell uses its
// mobile Sessions sheet; 420px exercises that repeat-activation shell.
const NARROW_VIEWPORT = { width: 420, height: 900 };

const BEFORE = "BACKGROUND_BEFORE_EVICTION";
const AFTER = "BACKGROUND_AFTER_EVICTION";
const TOOL_OUTPUT = "BACKGROUND_TOOL_OUTPUT";
const HISTORY_ASSISTANT = "BACKGROUND_HISTORY_REPLY";
// The turn produced on the target's own daemon while the browser socket is
// down; it must arrive with the reconnect's authoritative latest window.
const GAP_SENTINEL = "BACKGROUND_GAP_TURN";

// At least two older pages of history, and which turn is the visible
// later-page sentinel kept across the close/reopen.
// Sixty real history turns are produced through the helper BEFORE the browser
// opens its app page, so the target's initial thread/read is a truncated real
// history window with a nonempty olderCursor. A read window and an older page
// each hold a bounded item count, so two older pages load fewer than all sixty
// turns: the later-page sentinel is loaded, real older content remains, and the
// further-paging and reconnect phases have pages to issue.
const HISTORY_TURNS = 60;
// The later-page sentinel: inside the turns two older pages add, outside the
// initial latest window (so it is genuinely a later-page row).
const LATER_PAGE = 27;

// The dev entry installs 64 KiB resident / 32 KiB pending; the guard proves the
// configured limits are the ones in effect.
const LIMIT_RESIDENT_BYTES = 64 * 1024;
const LIMIT_PENDING_BYTES = 32 * 1024;
// The single background worker may hold ONE cache-sized record while updating
// it; that record's maximum size is limited by the existing 32 MiB disk budget.
// This is the worker's applicable bound — NOT the pending limit.
const LIMIT_WORKER_BYTES = 32 * 1024 * 1024;
// The spec's concurrent background network-hydration bound.
const MAX_NETWORK_HYDRATIONS = 2;

// The budget contract the sustained-workload proof enforces. `pending` bounds
// staged notification payload; `worker` bounds the one cache-sized worker
// record; they are distinct.
const BUDGET_LIMITS = Object.freeze({
  residentBytes: LIMIT_RESIDENT_BYTES,
  pendingBytes: LIMIT_PENDING_BYTES,
  workerBytes: LIMIT_WORKER_BYTES,
  hydrations: MAX_NETWORK_HYDRATIONS,
});

const BOOT = {
  // The entry sets this synchronously right after installing the tiny budgets,
  // BEFORE its `await import("../main")`: a boot check on the evidence global
  // would race the whole app module graph and re-navigate the live document.
  bootExpression: "window.backgroundTranscriptBooted === true",
  bootLabel: "the dev entry boot marker window.backgroundTranscriptBooted",
};

function parseArgs(argv) {
  const values = new Map();
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) throw new Error(`unexpected argument ${arg}`);
    if (i + 1 >= argv.length) throw new Error(`missing value for ${arg}`);
    values.set(arg.slice(2), argv[i + 1]);
    i += 1;
  }
  return values;
}

// The journey configuration. It is populated by loadConfig only when this module
// is the program entry point, so importing the module for tests has no side
// effects (no Chrome, no mkdir, no process.exit).
let AUTH_URL = "";
let HUB_URL = "";
let ARTIFACT_DIR = "";
let CONTROL_PATHS = [];
let TRACE_PATH = "";
let MILESTONE_PATH = "";
let SESSION_A = "";
let SESSION_B = "";
let ENTRY_PRODUCTION = false;

function loadConfig(argv) {
  const args = parseArgs(argv);
  const required = [
    "url",
    "hub-url",
    "artifact-dir",
    "control-path-a",
    "control-path-b",
    "request-log-a",
    "request-log-b",
    "trace-path",
    "milestone-path",
    "session-a",
    "session-b",
  ];
  const missing = required.filter((name) => !args.get(name));
  if (missing.length > 0) {
    console.error(`backgroundtranscriptguard: missing required arguments: ${missing.map((n) => `--${n}`).join(", ")}`);
    console.error(
      "This guard must be invoked by TestBackgroundTranscriptsEntrySliceBrowser in cmd/evener-hub/background_transcripts_browser_test.go",
    );
    process.exit(1);
  }
  AUTH_URL = args.get("url");
  HUB_URL = args.get("hub-url");
  ARTIFACT_DIR = args.get("artifact-dir");
  CONTROL_PATHS = [args.get("control-path-a"), args.get("control-path-b")];
  process.env.BACKGROUND_GUARD_REQUEST_LOG_A = args.get("request-log-a");
  process.env.BACKGROUND_GUARD_REQUEST_LOG_B = args.get("request-log-b");
  TRACE_PATH = args.get("trace-path");
  MILESTONE_PATH = args.get("milestone-path");
  SESSION_A = args.get("session-a");
  SESSION_B = args.get("session-b");
  ENTRY_PRODUCTION = args.get("entry-production") === "true";
  mkdirSync(ARTIFACT_DIR, { recursive: true });
  console.log(`backgroundtranscriptguard artifacts: ${ARTIFACT_DIR}`);
}

function traceBytes(tracePath = TRACE_PATH) {
  // An unreadable trace must fail, not report a zero-size window that would
  // make a later no-read verdict silently succeed.
  return statSync(tracePath).size;
}

// The evidence expression converts the store's read-only residency snapshot into
// plain JSON (a Map does not survive Runtime.evaluate). The dev entry installed
// the tiny budgets before the app imported, so the snapshot this reads is the
// budget contract under test.
const EVIDENCE_EXPR = `(async () => {
  const evidence = window.backgroundTranscriptEvidence;
  const snapshot = evidence.snapshot();
  // Real read-only evidence of the produced payload: the live store's own model
  // text length per ref, read through the SAME threads binding the app uses
  // (evidence.model), so no second module instance can stand in for it. The
  // configured-limit proof measures what was produced, not only what stayed
  // resident.
  let producedBytes = {};
  try {
    for (const [ref] of snapshot.refs) {
      const model = evidence.model(ref);
      if (!model) {
        producedBytes[ref] = 0;
        continue;
      }
      let bytes = 0;
      for (const turn of model.turns ?? []) {
        // The synthetic prelude turn carries the session's system prompt, not
        // produced workload; a system prompt must never count as produced work.
        if (turn.id === "turn_system") continue;
        for (const item of turn.items ?? []) {
          if (typeof item.text === "string") bytes += item.text.length;
        }
      }
      producedBytes[ref] = bytes;
    }
  } catch (error) {
    producedBytes = { error: String(error) };
  }
  return {
    residentBytes: snapshot.residentBytes,
    pendingBytes: snapshot.pendingBytes,
    workerBytes: snapshot.workerBytes,
    networkHydrations: snapshot.networkHydrations,
    committedWrites: snapshot.committedWrites,
    producedBytes,
    refs: [...snapshot.refs.entries()].map(([ref, entry]) => ({
      ref, resident: entry.resident, residentBytes: entry.residentBytes,
      handoffBytes: entry.handoffBytes, current: entry.current,
      appliedSequence: entry.appliedSequence,
    })),
  };
})()`;

// countTargetRequestsInWindow decodes a raw trace byte window and counts the
// TARGET session's request frames: thread/read and thread/turns/list. It is used
// to forbid fetches during a local-restoration interval.
function countTargetRequestsInWindow(ref, before, after, tracePath = TRACE_PATH) {
  // before/after are BYTE offsets (from statSync/traceBytes), so the window is
  // sliced as bytes and decoded after, never by string index (multibyte safe).
  // A missing/unreadable expected trace fails rather than reading as zero.
  const bytes = readFileSync(tracePath);
  if (after < before) throw new Error(`trace ${tracePath} window has after ${after} before before ${before}`);
  // The window is exactly [before, after). If `after` lands inside a record (it
  // started before `after`), complete that one straddling record so a target
  // request at the window's end is not silently dropped. A record that starts at
  // or after `after` is outside the window and must never be counted. An empty
  // window counts nothing.
  let end = after;
  if (after > before) {
    const straddlesAfter = bytes[after - 1] !== 0x0a;
    if (straddlesAfter) {
      const newline = bytes.indexOf(0x0a, after);
      if (newline === -1) {
        // The window ends inside an unterminated record: the evidence is
        // incomplete and must not yield a definitive count. (Synthetic: the Go
        // writer emits record+newline in one locked write.)
        throw new Error(`trace ${tracePath} window ends in an incomplete record at ${after}`);
      }
      end = newline + 1;
    }
  }
  const startMidRecord = before > 0 && bytes[before - 1] !== 0x0a;
  let lines = bytes.subarray(before, end).toString("utf8").split("\n");
  if (startMidRecord && lines.length > 0) lines = lines.slice(1);
  const threadID = ref.replace(/^local:/, "");
  let reads = 0;
  let pages = 0;
  let subscribeReads = 0;
  let recoveryReads = 0;
  let plainReads = 0;
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let record;
    try {
      record = JSON.parse(trimmed);
    } catch (error) {
      throw new Error(`trace ${tracePath} has a malformed record in the window: ${error.message}`);
    }
    if (record?.event !== "frame" || typeof record.frame !== "string") continue;
    let frame;
    try {
      frame = JSON.parse(record.frame);
    } catch (error) {
      throw new Error(`trace ${tracePath} has a malformed frame in the window: ${error.message}`);
    }
    if (record.direction !== "browser_to_hub") continue;
    if (!frame.method) continue;
    const params = frame.params ?? {};
    if (params.ref !== ref && params.threadId !== threadID) continue;
    if (frame.method === "thread/read") {
      reads += 1;
      // A foreground subscription read is the load the zero-read promise
      // forbids; the entry route's honest recovery read is non-subscribing and
      // carries a held snapshot. Distinguish them so a regression that turned
      // recovery into a foreground subscription still fails.
      if (params.subscribe === true) subscribeReads += 1;
      else if (params.heldSnapshot !== undefined) recoveryReads += 1;
      else plainReads += 1;
    } else if (frame.method === "thread/turns/list") pages += 1;
  }
  return { reads, pages, subscribeReads, recoveryReads, plainReads };
}

// targetPageRequestCursorsInWindow decodes a raw trace byte window and returns
// the cursor of every TARGET thread/turns/list REQUEST frame in it. A reopened
// pane may issue one older page for genuinely uncached content; that request
// must carry the retained cursor (the bottom of the loaded window), so a
// different cursor would be re-fetching content the durable rows already held.
function targetPageRequestCursorsInWindow(ref, before, after, tracePath = TRACE_PATH) {
  const bytes = readFileSync(tracePath);
  if (after < before) throw new Error(`trace ${tracePath} window has after ${after} before before ${before}`);
  let end = after;
  if (after > before) {
    const straddlesAfter = bytes[after - 1] !== 0x0a;
    if (straddlesAfter) {
      const newline = bytes.indexOf(0x0a, after);
      if (newline === -1) {
        throw new Error(`trace ${tracePath} window ends in an incomplete record at ${after}`);
      }
      end = newline + 1;
    }
  }
  const startMidRecord = before > 0 && bytes[before - 1] !== 0x0a;
  let lines = bytes.subarray(before, end).toString("utf8").split("\n");
  if (startMidRecord && lines.length > 0) lines = lines.slice(1);
  const threadID = ref.replace(/^local:/, "");
  const cursors = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let record;
    try {
      record = JSON.parse(trimmed);
    } catch (error) {
      throw new Error(`trace ${tracePath} has a malformed record in the window: ${error.message}`);
    }
    if (record?.event !== "frame" || typeof record.frame !== "string") continue;
    if (record.direction !== "browser_to_hub") continue;
    let frame;
    try {
      frame = JSON.parse(record.frame);
    } catch (error) {
      throw new Error(`trace ${tracePath} has a malformed frame in the window: ${error.message}`);
    }
    if (frame.method !== "thread/turns/list") continue;
    const params = frame.params ?? {};
    if (params.ref !== ref && params.threadId !== threadID) continue;
    cursors.push(params.cursor ?? "");
  }
  return cursors;
}

// restoredContainsRetained reports whether the retained loaded window is present
// in the restored model, in order. The restored model may carry OLDER content a
// resumed paging intent fetched; it must not be missing any retained item.
function restoredContainsRetained(retained, restored) {
  const retainedItems = retained?.items ?? [];
  const restoredItems = restored?.items ?? [];
  if (retainedItems.length === 0) return false;
  let index = 0;
  for (const item of restoredItems) {
    if (index >= retainedItems.length) break;
    const want = itemIdentity(retainedItems[index]);
    const got = itemIdentity(item);
    if (
      want.turnId === got.turnId &&
      want.id === got.id &&
      want.type === got.type &&
      want.text === got.text &&
      want.output === got.output &&
      want.arguments === got.arguments &&
      want.status === got.status
    ) {
      index += 1;
    }
  }
  return index === retainedItems.length;
}

// narrowViewportContentPreserved reports whether a repeat activation at a narrow
// viewport preserved the transcript's actual payload. Preservation means the
// before/after bodies agree in order — text, tool output, arguments and status —
// not merely that the same item IDs are present. It reuses the reopen restore's
// own comparison (restoredContainsRetained) rather than defining a second one;
// the restored model may carry OLDER content the re-activation fetched, but it
// must not be missing or rewrite any item that was present before.
function narrowViewportContentPreserved(before, after) {
  return restoredContainsRetained(before, after);
}

// targetDomainExpr returns the target's complete model domain: non-prelude turn
// ids with each item's id/type/text/output/argument identity, plus the model
// cursor. It is the content oracle; it never reads a DOM tail.
function targetDomainExpr(ref) {
  return `(() => {
    const m = window.backgroundTranscriptEvidence.model(${JSON.stringify(ref)});
    if (!m) return null;
    const turns = (m.turns ?? []).filter((t) => t.id !== "turn_system");
    const items = turns.flatMap((t) => (t.items ?? []).map((i) => ({
      turnId: t.id, id: i.id, type: i.type,
      text: i.text ?? "", output: i.output ?? "",
      arguments: i.argumentsJSON ?? undefined, status: i.status ?? null,
      callId: i.callId ?? null, transcriptKey: i.transcriptKey ?? null,
      position: i.position ?? null, version: i.version ?? null,
    })));
    return {
      cursor: m.olderCursor ?? "",
      turns: turns.map((t) => ({ id: t.id, status: t.status ?? null })),
      items,
      overlayKeys: Object.keys(m.overlay ?? {}),
    };
  })()`;
}

async function readTargetDomain(driver, ref) {
  return evaluate(driver.send, targetDomainExpr(ref));
}

function renderedSentinelRowsExpr(ref) {
  return `(() => {
    const pane = document.querySelector("[data-pane-scaffold='session:${ref}']");
    if (!pane) return [];
    return [...pane.querySelectorAll("[data-view-anchor-turn-id][data-view-anchor-id]")]
      .map((row) => ({
        id: row.getAttribute("data-view-anchor-id"),
        turnId: row.getAttribute("data-view-anchor-turn-id"),
        text: row.textContent ?? "",
      }))
      .filter((row) => row.text.includes(${JSON.stringify(BEFORE)}) || row.text.includes(${JSON.stringify(AFTER)}));
  })()`;
}

// A fresh browser document joins the real hub while the provider is gated
// mid-turn. Both panes must load that turn and receive its remaining output.
async function runLiveTwoTabs(driver, harnessUrl) {
  const opened = await openPage(driver.endpoint, "about:blank");
  const tab = await connectPage(driver.endpoint, opened.id);
  const sibling = new Driver({
    url: driver.url,
    artifactDir: driver.artifactDir,
    controlPath: driver.controlPath,
    milestonePath: driver.milestonePath,
  });
  sibling.page = tab;
  try {
    await applyViewport(sibling.send, VIEWPORT);
    await navigateTo(tab, harnessUrl, BOOT);
    if (!(await waitForEvidenceGlobal(sibling))) throw new Error("second live tab did not boot");
    await sibling.openSession(SESSION_B);
    const beforeRows = [];
    for (const [index, pane] of [driver, sibling].entries()) {
      await pane.send("Page.bringToFront");
      await waitForPaneText(pane, SESSION_B, BEFORE, 30000, `initial live output in tab ${index}`);
      await waitForTargetRunning(pane, SESSION_B);
      beforeRows.push(await evaluate(pane.send, renderedSentinelRowsExpr(SESSION_B)));
    }
    driver.phase("after-eviction");
    for (const pane of [driver, sibling]) {
      await pane.send("Page.bringToFront");
      await waitForPaneText(pane, SESSION_B, AFTER, 30000, "new live output in both tabs");
      await waitForTargetRunning(pane, SESSION_B);
    }
    driver.phase("finish");
    for (const pane of [driver, sibling]) {
      await pane.send("Page.bringToFront");
      await pane.waitForTurnIdle(SESSION_B, { timeoutMs: 60000 });
    }
    await waitForNativeEntryEvidence(
      sibling,
      (value) => value.rows.some((row) => row.item?.output?.includes(TOOL_OUTPUT)),
      "second tab's completed real tool output",
    );
    const afterRows = await evaluate(sibling.send, renderedSentinelRowsExpr(SESSION_B));
    const domain = await readTargetDomain(sibling, SESSION_B);
    const output = await readShellOutputRegion(sibling, SESSION_B, TOOL_OUTPUT);
    return {
      beforeRows: beforeRows[0],
      sibling: {
        beforeRows: beforeRows[1],
        afterRows,
        items: (domain?.items ?? []).filter((item) => item.turnId === afterRows[0]?.turnId),
        output: output?.outputs?.[0] ?? "",
      },
    };
  } catch (error) {
    await sibling.screenshot("second-tab-failure");
    await sibling.dumpState("second-tab-failure");
    const evidence = await evaluate(sibling.send, `({
      model: window.backgroundTranscriptEvidence?.model(${JSON.stringify(SESSION_B)}),
      route: window.backgroundTranscriptEvidence?.entryRoute(${JSON.stringify(SESSION_B)})
    })`);
    writeFileSync(path.join(ARTIFACT_DIR, "second-tab-failure-model.json"), JSON.stringify(evidence), { mode: 0o600 });
    throw error;
  } finally {
    tab.close();
    await closePage(driver.endpoint, opened.id);
  }
}

// renderedOutputExpr reads the shell OUTPUT region: the <pre> belonging to the
// CodeBlock that carries the "Copy output" control. The command block is a
// separate element, so command arguments cannot satisfy it.
function renderedOutputExpr(ref) {
  return `(() => {
    const pane = document.querySelector("[data-pane-scaffold='session:${ref}']");
    if (!pane) return null;
    const rows = [...pane.querySelectorAll("[data-testid='tool-call-item'][data-tool-name='shell']")];
    const outputs = rows.map((row) => {
      const btn = [...row.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") ?? "") === "Copy output");
      if (!btn) return null;
      let el = btn.parentElement;
      while (el && el !== row && !el.querySelector("pre")) el = el.parentElement;
      const pre = el ? el.querySelector("pre") : null;
      return pre ? pre.textContent ?? "" : null;
    }).filter((text) => text !== null);
    return { shellRows: rows.length, outputRegions: outputs.length, outputs };
  })()`;
}

// waitForTargetRunning observes the actual running state of the target's own
// turn (an in-progress turn or a live overlay), not merely finished text.
async function waitForTargetRunning(driver, ref, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    const d = await readTargetDomain(driver, ref);
    last = d;
    if (d && (d.turns.some((t) => (t.status ?? "").toLowerCase().includes("progress")) || d.overlayKeys.length > 0)) {
      return true;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`the target turn never showed a running state; last domain=${JSON.stringify(last)}`);
}

// waitForRenderedOutput waits for the shell OUTPUT region (the CodeBlock that
// owns the "Copy output" control) to show the produced literal.
async function waitForRenderedOutput(driver, ref, marker, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await evaluate(driver.send, renderedOutputExpr(ref)).catch(() => null);
    if (last?.outputRegions === 1 && last.outputs[0]?.includes(marker)) return last;
    await new Promise((r) => setTimeout(r, 100));
  }
  return false;
}

// Owning gate for the shell OUTPUT region (finding 2). It opens the shell
// disclosure with a real UI gesture (the `tools` default mounts the body
// collapsed), then reads the OUTPUT region. The gesture lives here, never inside
// the pure wait.
async function readShellOutputRegion(driver, ref, marker, timeoutMs = 30000) {
  const pane = `[data-pane-scaffold='session:${ref}']`;
  const toggleSelector = `${pane} [data-testid='tool-call-item'] button[aria-controls]`;
  // A bounded wait for a genuinely asynchronous UI state: the transcript
  // virtualizer mounts and re-renders rows while the live turn streams, so the
  // control can be absent for a frame even right after a successful existence
  // wait (the observed failure was driver.click finding no element at its own
  // box lookup, not a wrong selector). Wait until the control is present AND
  // has a non-zero box before clicking; never click into the void.
  const deadline = Date.now() + timeoutMs;
  let clicked = false;
  while (Date.now() < deadline && !clicked) {
    const ready = await evaluate(
      driver.send,
      `(() => {
         const row = document.querySelector(${JSON.stringify(`${pane} [data-testid='tool-call-item']`)});
         const btn = row ? row.querySelector("button[aria-controls]") : null;
         if (!btn) return false;
         const rect = btn.getBoundingClientRect();
         return rect.width > 0 && rect.height > 0;
       })()`,
    ).catch(() => false);
    if (ready) {
      try {
        await driver.click(toggleSelector);
        clicked = true;
      } catch {
        await delay(100);
      }
    } else {
      await delay(100);
    }
  }
  if (!clicked) throw new Error(`the shell disclosure control for ${ref} never became clickable`);
  return waitForRenderedOutput(driver, ref, marker, timeoutMs);
}

// The produced history sentinels in transcript order (oldest first): the
// independent oracle for the loaded history, not the current model.
function expectedHistorySequence() {
  const out = [];
  for (let n = 1; n <= HISTORY_TURNS; n += 1) out.push(historySentinel(n));
  return out;
}

// The structured identity of one item: every field the projection keeps, so a
// change to type, arguments, status or turn association fails equality.
function itemIdentity(item) {
  return {
    turnId: item.turnId ?? null,
    id: item.id ?? null,
    type: item.type ?? null,
    text: item.text ?? "",
    output: item.output ?? "",
    arguments: item.arguments ?? item.argumentsJSON ?? null,
    status: item.status ?? null,
  };
}

// Preserve source metadata in the raw reopen/resize evidence as well as bodies.
// Keep wire-window normalization separate: its reconciliation contract is unchanged.
function recordedItemIdentity(item) {
  return { ...itemIdentity(item), transcriptKey: item.transcriptKey ?? null,
    version: item.version ?? null, position: item.position ?? null };
}

// Ordered structured equality over items, in order.
function orderedHistoryEqual(expected, actual) {
  if (expected.length !== actual.length) return false;
  return expected.every((item, index) => {
    const a = itemIdentity(item);
    const b = itemIdentity(actual[index] ?? {});
    return (
      a.turnId === b.turnId &&
      a.id === b.id &&
      a.type === b.type &&
      a.text === b.text &&
      a.output === b.output &&
      a.arguments === b.arguments &&
      a.status === b.status
    );
  });
}

// Normalize a wire read window (result.thread.turns) and an applied model to the
// SAME representation (system turn excluded), so replacement is exact.
function normalizeWindowFromWire(turns) {
  return (turns ?? [])
    .filter((turn) => turn.id !== "turn_system")
    .map((turn) => ({
      turnId: turn.id ?? null,
      items: (turn.items ?? []).map((item) => ({
        turnId: turn.id ?? null,
        id: item.id ?? null,
        type: item.type ?? null,
        text: item.text ?? "",
        output: item.output ?? "",
        // The wire's own field name is argumentsJson (types.gen:3794).
        arguments: item.argumentsJson ?? null,
        status: item.status ?? null,
      })),
    }));
}

function normalizeWindowFromModel(domain) {
  const byTurn = new Map();
  for (const item of domain?.items ?? []) {
    if (item.turnId === "turn_system") continue;
    const key = item.turnId ?? "";
    if (!byTurn.has(key)) byTurn.set(key, { turnId: item.turnId ?? null, items: [] });
    byTurn.get(key).items.push({
      turnId: item.turnId ?? null,
      id: item.id ?? null,
      type: item.type ?? null,
      text: item.text ?? "",
      output: item.output ?? "",
      arguments: item.arguments ?? item.argumentsJSON ?? null,
      status: item.status ?? null,
    });
  }
  return [...byTurn.values()];
}

// domainContent concatenates the target model's item text and output fields.
function domainContent(domain) {
  if (!domain) return "";
  return domain.items.map((i) => (i.text ?? "") + (i.output ?? "")).join("\n");
}

// Owning reconnect validator (finding 4): the reconnect's authoritative read
// must arrive on a connection NOT seen before, and the applied model window
// (cursor) must match that read's response. A same-connection refresh is not a
// reconnect.
function reconnectValidator(before, evidence, applied = {}) {
  const beforeConnections = new Set(before?.connections ?? []);
  const reads = (evidence?.readConnections ?? []).map((connection, index) => ({
    connection,
    olderCursor: (evidence?.readResponseCursors ?? [])[index] ?? "",
    window: (evidence?.readResponseWindows ?? [])[index] ?? null,
  }));
  const newConnectionReads = reads.filter((r) => !beforeConnections.has(r.connection));
  const newReads = (evidence?.readResponses ?? 0) - (before?.readResponses ?? 0);
  const latest = newConnectionReads.length > 0 ? newConnectionReads[newConnectionReads.length - 1] : null;
  const responseCursor = latest?.olderCursor ?? "";
  const responseWindow = latest?.window ?? null;
  const appliedCursor = applied?.cursor ?? "";
  const appliedWindow = applied?.domain ? normalizeWindowFromModel(applied.domain) : null;
  const reconnected = newConnectionReads.length > 0;
  // The legacy route REPLACED its model with the reconnect read's window, so the
  // halves compared exactly. The ordinary entry route applies the new
  // connection's read and ALSO carries the model's own system/notice items
  // (e.g. the round-total systemMessages) interleaved in those turns, and keeps
  // the history a reader had already loaded. So require, per returned turn, that
  // the read's RECORDED items are present in order in the applied model's same
  // turn, tolerating the extra systemMessage notices; the cursor must still
  // match. This keeps the observation's meaning (the reconnect read was applied)
  // without the legacy whole-window exact-identity premise.
  const recordedItems = (items) => (items ?? []).filter((item) => item.type !== "systemMessage");
  const windowExact =
    Array.isArray(responseWindow) &&
    responseWindow.length > 0 &&
    Array.isArray(appliedWindow) &&
    responseWindow.every((responseTurn) => {
      const appliedTurn = appliedWindow.find((turn) => turn.turnId === responseTurn.turnId);
      return (
        appliedTurn !== undefined &&
        orderedHistoryEqual(recordedItems(responseTurn.items), recordedItems(appliedTurn.items))
      );
    });
  return {
    reconnected,
    newReads,
    newConnectionReads: newConnectionReads.length,
    responseCursor,
    responseWindow,
    appliedWindow,
    appliedCursor,
    appliedMatches: reconnected && appliedCursor === responseCursor && windowExact,
  };
}

// observedConnection reads the app's own connection state through the dev
// evidence getter (the same module graph as the app), independently of
// navigator.onLine.
const CONNECTION_EXPR = `(() => {
  const evidence = window.backgroundTranscriptEvidence;
  if (!evidence || typeof evidence.connectionState !== "function") return null;
  return evidence.connectionState();
})()`;

async function observedConnection(driver) {
  const appState = await evaluate(driver.send, CONNECTION_EXPR).catch(() => null);
  const online = await evaluate(driver.send, "navigator.onLine").catch(() => null);
  return { appState, online };
}

// waitForDisconnected waits for the app's own connection state to leave the
// connected state, not navigator.onLine alone.
async function waitForDisconnected(driver, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { appState } = await observedConnection(driver);
    if (appState !== null && appState !== undefined && appState !== "ready") return true;
    await new Promise((r) => setTimeout(r, 100));
  }
  return false;
}

// samplePeaksOnce reads one residency sample, letting an observation error
// FAIL rather than be swallowed.
async function samplePeaksOnce(driver, evaluatePage = evaluate) {
  const evidence = await evaluatePage(driver.send, EVIDENCE_EXPR);
  return {
    residentBytes: validMeasurement(evidence.residentBytes, "residentBytes"),
    pendingBytes: validMeasurement(evidence.pendingBytes, "pendingBytes"),
    workerBytes: validMeasurement(evidence.workerBytes, "workerBytes"),
    networkHydrations: validMeasurement(evidence.networkHydrations, "networkHydrations"),
    committedWrites: validMeasurement(evidence.committedWrites, "committedWrites"),
    producedBytes: reduceProducedPeak(evidence.producedBytes),
  };
}

// validMeasurement requires a finite, non-negative number before any use. A
// missing/null/string scalar, NaN or Infinity must fail evidence collection
// rather than be coerced to zero or passed through as a bogus value.
function validMeasurement(value, name) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    throw new Error(`invalid ${name} measurement: ${JSON.stringify(value)}`);
  }
  return value;
}

// reduceProducedPeak reduces the per-ref produced-payload map to its maximum.
// An observation error (the EVIDENCE_EXPR `{error}` shape), a missing map, or a
// non-number/NaN/negative/infinite entry must FAIL evidence collection; a real
// zero is retained.
function reduceProducedPeak(producedBytes) {
  if (producedBytes === undefined || producedBytes === null || typeof producedBytes !== "object") {
    throw new Error(`produced-bytes measurement is missing: ${JSON.stringify(producedBytes)}`);
  }
  const entries = Object.values(producedBytes);
  let numeric = 0;
  let max = 0;
  for (const value of entries) {
    if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
      throw new Error(`invalid produced-bytes measurement: ${JSON.stringify(value)}`);
    }
    numeric += 1;
    max = Math.max(max, value);
  }
  if (numeric === 0 && entries.length > 0) {
    throw new Error(`produced-bytes observation did not contain a measurement: ${JSON.stringify(producedBytes)}`);
  }
  return max;
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function accumulatePeak(peak, sample) {
  for (const key of Object.keys(peak)) peak[key] = Math.max(peak[key], sample[key] ?? 0);
  return peak;
}

// budgetFailures reports the sustained-workload peaks that violate their
// applicable bound. Resident, pending, the one cache-sized worker record, and
// concurrent hydrations are distinct bounds; the worker is NOT compared to the
// pending limit.
function budgetFailures(peak, limits = BUDGET_LIMITS) {
  const failures = [];
  if (!(peak.residentBytes <= limits.residentBytes)) {
    failures.push(`sustained resident peak ${peak.residentBytes} exceeds ${limits.residentBytes}`);
  }
  if (!(peak.pendingBytes <= limits.pendingBytes)) {
    failures.push(`sustained pending peak ${peak.pendingBytes} exceeds ${limits.pendingBytes}`);
  }
  if (!(peak.workerBytes <= limits.workerBytes)) {
    failures.push(`sustained worker peak ${peak.workerBytes} exceeds the one-record ${limits.workerBytes} bound`);
  }
  if (!(peak.networkHydrations <= limits.hydrations)) {
    failures.push(
      `sustained hydration peak ${peak.networkHydrations} exceeds the spec's ${limits.hydrations} concurrent background hydrations`,
    );
  }
  return failures;
}

// collectProductionPeak runs `turns` producer turns and returns the residency
// peak across their PRODUCTION interval. startTurn(prompt) triggers one
// producer; waitForComplete(prompt) awaits its real completion; sample() reads
// one residency snapshot. The sampler starts BEFORE the first producer turn and
// runs throughout every wait, so a peak held only while a producer is pending is
// observed. A sample error rejects the collection; a producer error stops and
// joins the sampler. The sampler never rejects on its own (no unhandled
// rejection) and its in-flight delay is joined in `finally` (no leaked timer).
async function collectProductionPeak({ turns, sentinel, startTurn, waitForComplete, sample, intervalMs = 25 }) {
  const peak = {
    retainedBytes: 0,
    residentBytes: 0,
    pendingBytes: 0,
    workerBytes: 0,
    networkHydrations: 0,
    committedWrites: 0,
    producedBytes: 0,
  };
  let stopped = false;
  let sampleError = null;
  const sampler = (async () => {
    while (!stopped) {
      try {
        accumulatePeak(peak, await sample());
      } catch (error) {
        sampleError = error;
        return;
      }
      if (!stopped) await delay(intervalMs);
    }
  })();
  try {
    for (let n = 0; n < turns; n += 1) {
      const prompt = `${sentinel}_${n}`;
      startTurn(prompt);
      await waitForComplete(prompt);
      if (sampleError) break;
    }
  } finally {
    stopped = true;
    await sampler;
  }
  if (sampleError) throw sampleError;
  return peak;
}

async function main() {
  loadConfig(process.argv.slice(2));
  // The Vite child inherits this environment; the dev server proxies /rpc and
  // the hub HTTP routes to the fixture hub. Set BEFORE startBrowserGuard.
  delete process.env.EVENER_HUB_ADDR;
  process.env.EVENER_HUB_URL = HUB_URL;

  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: PROFILE_PREFIX,
      viteConfigFile: "scripts/browserguard.vite.config.mjs",
    });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;

  const driver = new Driver({
    url: AUTH_URL,
    artifactDir: ARTIFACT_DIR,
    controlPath: CONTROL_PATHS[0],
    milestonePath: MILESTONE_PATH,
  });
  driver.sessionA = SESSION_A;
  driver.sessionB = SESSION_B;
  const controlIndex = (which) => (which === "target" ? 1 : 0);
  const writeControl = (which, command) => {
    appendFileSync(CONTROL_PATHS[controlIndex(which)], `${JSON.stringify(command)}\n`);
  };
  driver.phase = (phase) => {
    for (const controlPath of CONTROL_PATHS) {
      appendFileSync(controlPath, `${JSON.stringify({ command: "background-phase", phase })}\n`);
    }
  };
  driver.phaseOne = (which, phase) => writeControl(which, { command: "background-phase", phase });
  driver.setRole = (which, role) => writeControl(which, { command: "background-role", phase: role });
  driver.setAuto = (which) => writeControl(which, { command: "background-auto" });
  driver.clearAuto = (which) => writeControl(which, { command: "background-auto", phase: "off" });
  driver.startTurn = (which, prompt) => writeControl(which, { command: "background-start-turn", phase: prompt });
  // The unopened producer: session B's own daemon starts a real turn through its
  // AppWire, so the target's text is produced without its web pane ever opening.
  driver.startTargetTurn = () => {
    writeControl("target", { command: "background-start-turn" });
  };

  let cdpEndpoint;
  try {
    try {
      await waitForHttp(
        `http://127.0.0.1:${vitePort}/scripts/backgroundtranscriptguard/index.html`,
        "vite dev server",
        guard.getViteLaunchError,
      );
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    }
    const startupDeadline = createStartupDeadline();
    try {
      cdpEndpoint = await guard.waitForChrome({ signal: startupDeadline.signal });
      await waitForHttp(
        devtoolsHttpURL(cdpEndpoint, "/json/version"),
        "chrome devtools endpoint",
        guard.getChromeLaunchError,
        {
          signal: startupDeadline.signal,
          failure: guard.getChromeFailure(),
        },
      );
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({
          error,
          subsystem: "chrome",
          chromeBinary: guard.chromeBinary,
          chromeArgv: guard.getChromeArgv(),
          chromeStderr: guard.getChromeError(),
          viteStderr: guard.getViteError(),
        }),
      );
    } finally {
      startupDeadline.clear();
    }

    const opened = await openPage(cdpEndpoint, "about:blank");
    const page = await connectPage(cdpEndpoint, opened.id);
    driver.page = page;
    driver.endpoint = cdpEndpoint;
    await page.send("Page.enable").catch(() => {});
    await page.send("Runtime.enable").catch(() => {});
    await page.send("Network.enable").catch(() => {});
    // Capture in-page module/exceptions so a harness that never booted reports
    // what actually threw, not only that its global is absent.
    const pageErrors = [];
    page.ws.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (message.method === "Runtime.exceptionThrown") {
        const details = message.params?.exceptionDetails;
        pageErrors.push(details?.exception?.description ?? details?.text ?? "exception");
      } else if (message.method === "Runtime.consoleAPICalled" && message.params?.type === "error") {
        pageErrors.push((message.params.args ?? []).map((arg) => arg.value ?? arg.description ?? "").join(" "));
      }
    });

    // Ruling 82: track every WebSocket the app opens so the gap step can close
    // the REAL hub socket at the transport level. CDP Network.emulateNetworkConditions
    // sets navigator.onLine=false but does NOT close an established WebSocket, so
    // the app's own connection store never leaves "ready" (verified: ws-broken
    // {disconnected:false, appState:"ready", online:false}). This wrapper is a
    // harness pre-load script, the same mechanism cascadeguard/run.mjs uses to
    // close the app's shared hub socket; it touches only the browser's socket
    // object, never the app or its store.
    await page
      .send("Page.addScriptToEvaluateOnNewDocument", {
        source: `(() => {
          const Native = window.WebSocket;
          window.__backgroundTranscriptSockets = [];
          window.WebSocket = class extends Native {
            constructor(...args) { super(...args); window.__backgroundTranscriptSockets.push(this); }
          };
        })()`,
      })
      .catch(() => {});

    try {
      await applyViewport(driver.send, VIEWPORT);
      // Produce the forty real history turns BEFORE the browser opens the app
      // page, so the target's initial thread/read is a truncated real history
      // window with a nonempty olderCursor. The pane is never opened here.
      await seedTargetHistory(driver);
      // Authenticate against the hub issue URL first: its cookie is shared with
      // the Vite origin (same hostname, cookies are port-agnostic), then load the
      // guard's own nested HTML through Vite.
      await navigateTo(page, AUTH_URL, { retryDelayMs: 250 });
      // The entry route is the only route, so the harness budget query is always
      // present; runJourney rejects a run without it.
      const harnessUrl = `http://127.0.0.1:${vitePort}/scripts/backgroundtranscriptguard/index.html?entry-production=true`;
      await runDisplaySynchronization(driver, cdpEndpoint, harnessUrl);
      try {
        await navigateTo(page, harnessUrl, BOOT);
      } catch (error) {
        const dump = await evaluate(
          driver.send,
          `(async () => {
            const moduleResponse = await fetch("/src/dev/backgroundTranscriptGuard.tsx");
            const moduleText = await moduleResponse.text();
            return { href: location.href, readyState: document.readyState,
              scripts: [...document.querySelectorAll("script")].map((s) => s.src),
              body: (document.body?.innerHTML ?? "").slice(0, 300),
              hasGlobal: typeof window.backgroundTranscriptEvidence,
              bootError: window.__backgroundGuardBootError ?? null,
              moduleStatus: moduleResponse.status, moduleType: moduleResponse.headers.get("content-type"),
              moduleHead: moduleText.slice(0, 300) };
          })()`,
        ).catch((e) => `dump failed: ${e}`);
        throw new Error(
          `${error instanceof Error ? error.message : String(error)}\npage errors:\n${pageErrors.join("\n") || "(none captured)"}\ndom: ${JSON.stringify(dump)}`,
        );
      }
      await runJourney(driver, ENTRY_PRODUCTION, harnessUrl);
      await runTwoTabFencing(driver, cdpEndpoint, harnessUrl);
    } finally {
      try {
        await driver.screenshot("final");
        await driver.dumpState?.("final");
      } catch {
        /* best-effort dump */
      }
      try {
        page.close();
      } catch {
        /* already closed */
      }
    }
  } finally {
    await cleanup();
  }
}

// Literal inputs are independent of the production display codec and receiver.
const DISPLAY_CONFIGS = [
  {
    version: 1,
    content: { kind: "preset", level: "full" },
    advanced: {
      roundTimings: true,
      tokenCounts: false,
      estimatedCost: true,
      systemEvents: false,
      promptEvents: true,
      hookExits: "all",
    },
  },
  {
    version: 1,
    content: { kind: "preset", level: "tools" },
    advanced: {
      roundTimings: false,
      tokenCounts: true,
      estimatedCost: false,
      systemEvents: true,
      promptEvents: false,
      hookExits: "successful",
    },
  },
  {
    version: 1,
    content: { kind: "preset", level: "activity" },
    advanced: {
      roundTimings: false,
      tokenCounts: true,
      estimatedCost: true,
      systemEvents: true,
      promptEvents: false,
      hookExits: "none",
    },
  },
  {
    version: 1,
    content: { kind: "preset", level: "intent" },
    advanced: {
      roundTimings: true,
      tokenCounts: false,
      estimatedCost: false,
      systemEvents: false,
      promptEvents: true,
      hookExits: "all",
    },
  },
];
const DISPLAY_KEYS = ["desktop", "mobile"].map((layout) => `evener.prefs.transcriptDisplay.${layout}`);
const DISPLAY_LEGACY_KEYS = [
  "transcriptRoundTimings",
  "transcriptTokenCounts",
  "transcriptHookExitsAll",
  "transcriptHookExitsNormal",
  "transcriptPromptLoaded",
  "showCost",
].map((key) => `evener.prefs.${key}`);
const displayCall = (tab, method, ...args) =>
  evaluate(tab.send, `window.backgroundDisplayGuard.${method}(${args.map((arg) => JSON.stringify(arg)).join(",")})`);
const sameDisplayValue = (a, b) => JSON.stringify(a) === JSON.stringify(b);

async function waitDisplay(tab, predicate, label) {
  const deadline = Date.now() + 15000;
  let snapshot;
  do {
    snapshot = await displayCall(tab, "snapshot");
    if (predicate(snapshot)) return snapshot;
    await delay(25);
  } while (Date.now() < deadline);
  throw new Error(`display ${label}: ${JSON.stringify(snapshot)}`);
}

async function runDisplaySynchronization(driver, endpoint, harnessUrl) {
  for (const transport of ["broadcast", "storage"]) {
    const tabs = [];
    try {
      for (const name of ["a", "b"]) {
        const target = await openPage(endpoint, "about:blank");
        const page = await connectPage(endpoint, target.id);
        const tab = { ...page, target_id: target.id };
        tabs.push(tab);
        await tab.send("Runtime.enable");
        // Run before any app import. Only A clears the named display keys, and
        // does so before B starts. No synthetic events or receiver reinit.
        await tab.send("Page.addScriptToEvaluateOnNewDocument", {
          source: `(() => {
            ${transport === "storage" ? 'Object.defineProperty(window, "BroadcastChannel", { value: undefined, configurable: true });' : ""}
            if (${JSON.stringify(name)} === "a" && location.origin === ${JSON.stringify(new URL(harnessUrl).origin)}) {
              for (const key of ${JSON.stringify([...DISPLAY_KEYS, ...DISPLAY_LEGACY_KEYS])}) localStorage.removeItem(key);
            }
          })()`,
        });
        await navigateTo(tab, `${harnessUrl}&display-transport=${transport}&display-tab=${name}`, BOOT);
        const ready = await evaluate(
          tab.send,
          `(async () => {
          const deadline = Date.now() + 30000;
          while (typeof window.backgroundTranscriptEvidence === "undefined" && Date.now() < deadline) {
            if (window.__backgroundGuardBootError) throw new Error(window.__backgroundGuardBootError);
            await new Promise((resolve) => setTimeout(resolve, 25));
          }
          return typeof window.backgroundDisplayGuard !== "undefined";
        })()`,
        );
        if (!ready) throw new Error(`display ${transport}: missing display observer window.backgroundDisplayGuard`);
      }
      const baseline = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
      driver.milestone(`display-${transport}-baseline`, {
        target_ids: tabs.map((tab) => tab.target_id),
        tabs: baseline,
      });
      if (tabs[0].target_id === tabs[1].target_id || baseline[0].origin !== baseline[1].origin)
        throw new Error("display requires distinct same-origin documents");
      for (const value of baseline) {
        if (
          !sameDisplayValue(value.local, {}) ||
          value.storage_blocked !== (transport === "broadcast") ||
          value.broadcast_channel !== (transport === "broadcast" ? "function" : "undefined")
        )
          throw new Error(`display transport baseline: ${JSON.stringify(value)}`);
      }
      const expected = {};
      const sourceIds = new Map();
      const operations = [
        ["desktop-v1", 0, "desktop", DISPLAY_CONFIGS[0]],
        ["mobile-v1", 1, "mobile", DISPLAY_CONFIGS[1]],
        ["desktop-v2", 0, "desktop", DISPLAY_CONFIGS[2]],
        ["mobile-v2", 1, "mobile", DISPLAY_CONFIGS[3]],
        ["clear-desktop", 0, "desktop", null],
        ["clear-mobile", 1, "mobile", null],
      ];
      for (const [name, sender, layout, config] of operations) {
        await Promise.all(tabs.map((tab) => displayCall(tab, "resetObservations")));
        const before = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
        if (config === null) {
          await displayCall(tabs[sender], "clearLocal", layout);
          delete expected[layout];
        } else {
          await displayCall(tabs[sender], "setLocal", layout, config);
          expected[layout] = config;
        }
        const receiver = 1 - sender;
        await waitDisplay(
          tabs[receiver],
          (value) =>
            value.publications.length > 0 &&
            value.storage.length > 0 &&
            (transport === "storage" || value.messages.length > 0),
          `${transport} ${name} delivered`,
        );
        if (transport === "broadcast")
          await waitDisplay(tabs[sender], (value) => value.messages.length > 0, `${name} sender observation`);
        const after = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
        driver.milestone(`display-${transport}-${name}`, { sender, layout, before, after });
        const encoded = config === null ? null : JSON.stringify(config);
        for (let n = 0; n < 2; n += 1) {
          const value = after[n];
          if (
            !sameDisplayValue(value.local, expected) ||
            value.publications.length !== 1 ||
            !sameDisplayValue(value.publications[0], expected)
          )
            throw new Error(`display ${transport} ${name} map/publication: ${JSON.stringify(value)}`);
          for (const key of ["desktop", "mobile"]) {
            if (!sameDisplayValue(value.effective[key], expected[key] ?? baseline[n].effective[key]))
              throw new Error(`display ${name} effective ${key}: ${JSON.stringify(value)}`);
          }
          if (value.persisted[layout] !== encoded)
            throw new Error(`display ${name} durable value: ${JSON.stringify(value)}`);
          const events = value.storage;
          if (events.length !== (n === receiver ? 1 : 0))
            throw new Error(`display ${name} storage event count: ${JSON.stringify(value)}`);
          if (n === receiver) {
            const event = events[0];
            if (
              !event.trusted ||
              !event.local_storage ||
              event.blocked !== (transport === "broadcast") ||
              event.key !== `evener.prefs.transcriptDisplay.${layout}` ||
              event.old_value !== before[n].persisted[layout] ||
              event.new_value !== encoded ||
              event.url !== before[sender].href
            )
              throw new Error(`display ${name} native storage evidence: ${JSON.stringify(event)}`);
          }
          if (transport === "broadcast") {
            const message = value.messages[0];
            if (
              value.messages.length !== 1 ||
              Object.keys(message).length !== 5 ||
              message.version !== 1 ||
              !message.sourceId ||
              message.layout !== layout ||
              message.config !== encoded ||
              message.fingerprint !== encoded
            )
              throw new Error(`display ${name} native envelope: ${JSON.stringify(value.messages)}`);
            const previous = sourceIds.get(sender);
            if (previous && previous !== message.sourceId) throw new Error("display sender identity changed");
            sourceIds.set(sender, message.sourceId);
          } else if (value.messages.length !== 0) throw new Error("fallback unexpectedly observed BroadcastChannel");
        }
      }
      if (transport === "broadcast") {
        if (sourceIds.get(0) === sourceIds.get(1)) throw new Error("display real sender identities are equal");
        await runDisplayFingerprint(driver, tabs, [...sourceIds.values()]);
      }
      // clearLocal also dual-writes legacy toggles. Remove only this phase's
      // named keys so the next document does not migrate those test writes
      // into an activity override and change the existing transcript journey.
      await evaluate(
        tabs[0].send,
        `(() => { for (const key of ${JSON.stringify([...DISPLAY_KEYS, ...DISPLAY_LEGACY_KEYS])}) localStorage.removeItem(key); })()`,
      );
    } finally {
      for (const tab of tabs) {
        tab.close();
        await closePage(endpoint, tab.target_id).catch(() => {});
      }
    }
  }
}

async function runDisplayFingerprint(driver, tabs, sourceIds) {
  await Promise.all(tabs.map((tab) => displayCall(tab, "resetObservations")));
  const before = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
  const invalid = {
    version: 1,
    sourceId: "display-guard-fingerprint-control",
    layout: "desktop",
    config: JSON.stringify(DISPLAY_CONFIGS[0]),
    fingerprint: JSON.stringify(DISPLAY_CONFIGS[1]),
  };
  if (sourceIds.includes(invalid.sourceId)) throw new Error("fingerprint control must have a different source ID");
  // This is an extra NATIVE sending channel, not the production receiver and
  // not a storage write. Its sibling observer was created after that receiver.
  await evaluate(
    tabs[0].send,
    `window.__displayFingerprintSender = new BroadcastChannel("evener.transcript-display.v1"); window.__displayFingerprintSender.postMessage(${JSON.stringify(invalid)})`,
  );
  await Promise.all(
    tabs.map((tab) =>
      waitDisplay(
        tab,
        (value) => value.messages.length === 1,
        "mismatched fingerprint observed after production receiver",
      ),
    ),
  );
  const rejected = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
  if (
    !sameDisplayValue(rejected[1].messages, [invalid]) ||
    rejected[1].publications.length !== 0 ||
    !sameDisplayValue(rejected[1].local, before[1].local)
  )
    throw new Error(`display mismatched fingerprint was not rejected: ${JSON.stringify(rejected[1])}`);
  const valid = { ...invalid, fingerprint: invalid.config };
  await evaluate(tabs[0].send, `window.__displayFingerprintSender.postMessage(${JSON.stringify(valid)})`);
  await Promise.all(
    tabs.map((tab) =>
      waitDisplay(tab, (value) => value.messages.length === 2, "corrected fingerprint positive control delivered"),
    ),
  );
  const accepted = await Promise.all(tabs.map((tab) => displayCall(tab, "snapshot")));
  driver.milestone("display-fingerprint", { invalid, valid, before, rejected, accepted });
  if (
    !sameDisplayValue(accepted[1].messages, [invalid, valid]) ||
    !sameDisplayValue(accepted[1].local, { desktop: DISPLAY_CONFIGS[0] }) ||
    !sameDisplayValue(accepted[1].publications, [{ desktop: DISPLAY_CONFIGS[0] }])
  )
    throw new Error(`display corrected fingerprint not applied exactly once: ${JSON.stringify(accepted[1])}`);
  for (let n = 0; n < 2; n += 1) {
    if (!sameDisplayValue(before[n].persisted, accepted[n].persisted) || accepted[n].storage.length !== 0)
      throw new Error("fingerprint control wrote browser storage");
  }
  await displayCall(tabs[1], "clearLocal", "desktop");
  await waitDisplay(
    tabs[0],
    (value) => sameDisplayValue(value.local, {}) && value.messages.length === 3,
    "fingerprint override cleared through production channel",
  );
  await evaluate(tabs[0].send, "window.__displayFingerprintSender.close()");
}

async function runEntryProductionJourney(driver, harnessUrl) {
  const installed = await waitForEvidenceGlobal(driver);
  if (!installed) throw new Error("production-entry dev entry did not expose the real app evidence global");
  await driver.waitPage("(() => document.querySelector(\"[data-testid='rail-brand']\") !== null ? true : null)()", {
    timeoutMs: 30000,
    label: "real app shell",
  });
  const rows = await driver.waitPage(driver.railRowsExpr({ atLeast: 2 }), {
    timeoutMs: 30000,
    label: "both real fixture sessions in the rail",
  });
  if (!rows.rows.some((row) => row.ref === SESSION_B)) throw new Error("selected target is absent from the real rail");

  const baseline = await waitForNativeEntryEvidence(driver, (value) => value.rows.length > 0, "entry baseline commit");
  driver.milestone("entry-baseline", {
    ref: SESSION_B,
    historyKey: baseline.historyKey,
    entryRows: baseline.rows.length,
    legacyRecordCount: baseline.legacyRecordCount,
  });

  driver.startTargetTurn();
  await driver.waitPage(
    `(() => {
      const model = window.backgroundTranscriptEvidence.model(${JSON.stringify(SESSION_B)});
      if (!model) return null;
      const text = (model.turns ?? []).flatMap((turn) => turn.items ?? []).map((item) => item.text ?? "").join("\\n");
      const overlay = Object.values(model.overlay ?? {}).map((item) => item.text ?? "").join("\\n");
      return text.includes(${JSON.stringify(BEFORE)}) || overlay.includes(${JSON.stringify(BEFORE)})
        ? { text: text + "\\n" + overlay } : null;
    })()`,
    { timeoutMs: 30000, label: "unopened selected session's real background output" },
  );
  const runningBeforeOpen = await waitForTargetRunning(driver, SESSION_B);

  const traceBeforeOpen = traceBytes();
  await driver.openSession(SESSION_B);
  await driver.waitPage(
    `(() => document.querySelector("[data-pane-scaffold='session:${SESSION_B}']")?.textContent?.includes(${JSON.stringify(BEFORE)}) ? true : null)()`,
    { timeoutMs: 30000, label: "selected session rendered from local entry history" },
  );
  const traceAfterOpen = traceBytes();
  const activation = countTargetRequestsInWindow(SESSION_B, traceBeforeOpen, traceAfterOpen);
  const midstream = await waitForNativeEntryEvidence(
    driver,
    (value) => value.rows.length > 0,
    "entry rows during live stream",
  );
  const beforeIsPersisted = midstream.rows.some((row) => row.item?.text?.includes(BEFORE));
  driver.milestone("entry-opened", {
    ref: SESSION_B,
    traceBefore: traceBeforeOpen,
    traceAfter: traceAfterOpen,
    running: runningBeforeOpen,
    activationReads: activation.reads,
    activationPages: activation.pages,
    liveBeforeRendered: true,
    liveBeforePersisted: beforeIsPersisted,
  });
  if (beforeIsPersisted) throw new Error("live-only output was written to entry storage before its turn completed");

  const liveTabs = await runLiveTwoTabs(driver, harnessUrl);
  await evaluate(driver.send, `window.backgroundTranscriptEvidence.entrySettled(${JSON.stringify(SESSION_B)})`);
  const completed = await waitForNativeEntryEvidence(
    driver,
    (value) =>
      value.rows.some((row) => row.item?.text?.includes(AFTER)) &&
      value.rows.some((row) => row.item?.output?.includes(TOOL_OUTPUT)),
    "completed recorded transcript in native entry storage",
  );
  const target = await readTargetDomain(driver, SESSION_B);
  const outputRegion = await readShellOutputRegion(driver, SESSION_B, TOOL_OUTPUT);
  const outputText = outputRegion?.outputs?.[0] ?? "";
  const rendererItems = target?.items ?? [];
  const renderedSentinelRows = await evaluate(driver.send, renderedSentinelRowsExpr(SESSION_B));
  driver.milestone("two-tab-live", {
    tabs: [
      {
        beforeRows: liveTabs.beforeRows,
        afterRows: renderedSentinelRows,
        items: rendererItems.filter((item) => item.turnId === renderedSentinelRows[0]?.turnId),
        output: outputText,
      },
      liveTabs.sibling,
    ],
  });
  const recordedRendererItems = rendererItems.filter((item) => item.transcriptKey && item.position);
  const renderedToolItems = recordedRendererItems.filter((item) => item.output.includes(TOOL_OUTPUT));
  const nativeItems = completed.rows.map((row) => ({
    transcriptKey: row.transcriptKey,
    position: row.position,
    item: row.item,
  }));
  const nativeTurns = completed.turns;
  const nativeCoverage = completed.coverage;
  const rendererToNative = recordedRendererItems.map((item) => {
    const matches = nativeItems.filter(
      (row) => row.transcriptKey === item.transcriptKey && row.item?.id === item.id && row.item?.turnId === item.turnId,
    );
    const native = matches[0] ?? null;
    const identityMatches =
      native !== null &&
      native.item.type === item.type &&
      (native.item.text ?? "") === item.text &&
      (native.item.output ?? "") === item.output &&
      (native.item.argumentsJSON ?? null) === (item.arguments ?? null) &&
      native.item.status === item.status &&
      (native.item.callId ?? null) === item.callId &&
      native.position.entry === item.position.entry &&
      native.position.item === item.position.item &&
      (native.position.sub ?? 0) === (item.position.sub ?? 0) &&
      native.item.version === item.version;
    return { id: item.id, turnId: item.turnId, matches: matches.length, identityMatches, native };
  });
  const comparePosition = (left, right) =>
    left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
  const nativeCoverageMatches = nativeItems.map((row) => ({
    id: row.item?.id,
    turnId: row.item?.turnId,
    position: row.position,
    producerEvidence:
      row.item?.text?.includes(BEFORE) || row.item?.text?.includes(AFTER) || row.item?.output?.includes(TOOL_OUTPUT),
    coveringRows: nativeCoverage
      .filter(
        (coverage) =>
          coverage.start &&
          coverage.end &&
          comparePosition(coverage.start, row.position) <= 0 &&
          comparePosition(row.position, coverage.end) <= 0,
      )
      .map((coverage) => ({
        rangeId: coverage.rangeId,
        kind: coverage.kind,
        start: coverage.start,
        end: coverage.end,
        boundary: coverage.boundary,
      })),
  }));
  const producerCoverage = nativeCoverageMatches.filter((row) => row.producerEvidence);
  const rendererTurns = target?.turns ?? [];
  const itemTurnMatches = renderedToolItems.map((item) => {
    const native = nativeTurns.filter((turn) => turn.turnId === item.turnId);
    const renderer = rendererTurns.filter((turn) => turn.id === item.turnId);
    return {
      itemId: item.id,
      turnId: item.turnId,
      nativeTurns: native.length,
      rendererTurns: renderer.length,
      native: native[0] ?? null,
      renderer: renderer[0] ?? null,
    };
  });
  const domCounts = {
    before: countOccurrences(renderedSentinelRows.map((row) => row.text).join("\n"), BEFORE),
    after: countOccurrences(renderedSentinelRows.map((row) => row.text).join("\n"), AFTER),
    tool: countOccurrences(outputText, TOOL_OUTPUT),
  };
  const legacyRows = completed.legacyRecordCount;
  const audit = {
    ref: SESSION_B,
    historyKey: completed.historyKey,
    rendererTurns,
    rendererItems,
    renderedSentinelRows,
    recordedRendererItems,
    renderedOutputRegion: outputRegion,
    nativeItems,
    nativeTurns,
    nativeCoverage,
    rendererToNative,
    nativeCoverageMatches,
    itemTurnMatches,
  };
  writeFileSync(path.join(ARTIFACT_DIR, "entry-audit.json"), `${JSON.stringify(audit, null, 2)}\n`, { mode: 0o600 });
  driver.milestone("entry-completed", {
    ref: SESSION_B,
    domCounts,
    renderedOutputRegions: outputRegion?.outputRegions ?? 0,
    shellRows: outputRegion?.shellRows ?? 0,
    renderedOutputText: outputText,
    renderedToolItemCount: renderedToolItems.length,
    rendererToNativeMatches: rendererToNative.every((match) => match.matches === 1 && match.identityMatches),
    itemTurnMatches,
    producerItemsCovered:
      producerCoverage.length === 2 && producerCoverage.every((match) => match.coveringRows.length > 0),
    nativeEntryRows: completed.rows.length,
    nativeTurnRows: nativeTurns.length,
    nativeCoverageRows: nativeCoverage.length,
    nativeLegacyRows: legacyRows,
  });
  if (outputRegion?.shellRows !== 1 || outputRegion.outputRegions !== 1 || domCounts.tool !== 1) {
    throw new Error(
      `the actual rendered shell output region did not contain the tool output exactly once: ${JSON.stringify({ outputRegion, domCounts })}`,
    );
  }
  if (domCounts.before !== 1 || domCounts.after !== 1 || renderedToolItems.length !== 1) {
    throw new Error(
      `selected renderer did not show each produced item exactly once: ${JSON.stringify({ domCounts, renderedSentinelRows, renderedToolItems })}`,
    );
  }
  for (const sentinel of [BEFORE, AFTER]) {
    const rowMatches = renderedSentinelRows.filter((row) => row.text.includes(sentinel));
    const itemMatches = recordedRendererItems.filter((item) => item.text.includes(sentinel));
    if (rowMatches.length !== 1 || itemMatches.length !== 1 || rowMatches[0].turnId !== itemMatches[0].turnId) {
      throw new Error(
        `rendered sentinel row does not identify exactly one matching recorded item for ${sentinel}: ${JSON.stringify({ rowMatches, itemMatches })}`,
      );
    }
  }
  if (rendererToNative.some((match) => match.matches !== 1 || !match.identityMatches)) {
    throw new Error(`renderer items do not map one-to-one to native entry items: ${JSON.stringify(rendererToNative)}`);
  }
  if (producerCoverage.length !== 2 || producerCoverage.some((match) => match.coveringRows.length === 0)) {
    throw new Error(
      `durable producer items are outside stored coverage boundaries: ${JSON.stringify(producerCoverage)}`,
    );
  }
  if (itemTurnMatches.some((match) => match.nativeTurns !== 1 || match.rendererTurns !== 1)) {
    throw new Error(
      `rendered tool item turn identity disagrees with native turn metadata: ${JSON.stringify(itemTurnMatches)}`,
    );
  }
  if (legacyRows !== 0) throw new Error(`selected session created ${legacyRows} legacy cache record(s)`);
  if (!runningBeforeOpen) throw new Error("target background stream was not running when opened");
  if (activation.reads !== 0 || activation.pages !== 0) {
    throw new Error(`selected pane opening issued network history requests: ${JSON.stringify(activation)}`);
  }

  // ---- paging: real older pages, a visible later-page row, remaining cursor ----
  // The sixty seeded turns predate the app's first read, so the target's initial
  // read is a truncated real history window with a nonempty cursor.
  const seeded = targetPagingEvidence(SESSION_B);
  if (seeded.readResponses === 0 || !seeded.initialOlderCursor) {
    throw new Error(
      `the selected session's initial read is not a truncated real history window: readResponses=${seeded.readResponses}, initialOlderCursor=${JSON.stringify(seeded.initialOlderCursor)}`,
    );
  }
  await waitForPaneText(driver, SESSION_B, historySentinel(HISTORY_TURNS), 60000, "the newest seeded history turn");
  // Load at least two REAL older pages through the real scroll port. Each scroll
  // must issue a thread/turns/list request; a synthetic event or an ancestor
  // element would issue none.
  const pagingTraceBefore = traceBytes();
  let paging = await scrollToTopAndWaitPage(driver, SESSION_B, seeded.pages);
  paging = await scrollToTopAndWaitPage(driver, SESSION_B, paging.pages);
  const laterPageSentinel = historySentinel(LATER_PAGE);
  const renderedLater = await scrollSentinelIntoView(driver, SESSION_B, laterPageSentinel);
  const pagingTraceAfter = traceBytes();
  const pagesComplete = furtherHistoryValidator(
    await readTargetDomain(driver, SESSION_B),
    expectedHistorySequence(),
  ).ordered;
  driver.milestone("history-paging", {
    turns: HISTORY_TURNS,
    traceBefore: pagingTraceBefore,
    traceAfter: pagingTraceAfter,
    initialOlderCursor: seeded.initialOlderCursor,
    pages: paging.pages,
    remainingCursor: paging.lastCursor,
    pagesComplete,
    laterSentinel: laterPageSentinel,
    laterRendered: renderedLater,
  });
  if (paging.pages < 2) throw new Error(`the pane issued ${paging.pages} older pages, want at least two`);
  if (!renderedLater) {
    throw new Error(`the later-page sentinel ${laterPageSentinel} did not render after two older pages`);
  }
  if (!paging.lastCursor && !pagesComplete) {
    throw new Error("two older pages exhausted the cursor; none remains for the further-paging phase");
  }

  // ---- further paging: real older pages, complete in order, no duplicates ----
  // The loaded window is still incomplete after the two history pages, so this
  // phase must issue real older pages of its own before the history is complete.
  const furtherTraceBefore = traceBytes();
  const beforeFurther = targetPagingEvidence(SESSION_B).pages;
  let pagedDomain = await readTargetDomain(driver, SESSION_B);
  const alreadyComplete = furtherHistoryValidator(pagedDomain, expectedHistorySequence()).ordered;
  let scrolled = { pages: beforeFurther, lastCursor: targetPagingEvidence(SESSION_B).lastCursor };
  for (let attempt = 0; attempt < 6; attempt += 1) {
    if (furtherHistoryValidator(pagedDomain, expectedHistorySequence()).ordered) break;
    scrolled = await scrollToTopAndWaitPage(driver, SESSION_B, scrolled.pages);
    pagedDomain = await readTargetDomain(driver, SESSION_B);
  }
  const furtherTraceAfter = traceBytes();
  const pagedIds = (pagedDomain?.items ?? []).map((i) => i.id);
  const noDuplicates = new Set(pagedIds).size === pagedIds.length;
  const furtherCheck = furtherHistoryValidator(pagedDomain, expectedHistorySequence());
  driver.milestone("further-paging", {
    traceBefore: furtherTraceBefore,
    traceAfter: furtherTraceAfter,
    pages: scrolled.pages - beforeFurther,
    alreadyComplete,
    requiredPage: !alreadyComplete,
    cursor: scrolled.lastCursor,
    noDuplicates,
    historyOrdered: furtherCheck.ordered,
    historyCount: furtherCheck.count,
    expectedCount: furtherCheck.expectedCount,
  });
  if (scrolled.pages <= beforeFurther) {
    throw new Error("further paging issued no older target page");
  }
  if (!noDuplicates) throw new Error("further paging introduced duplicate items");
  if (!furtherCheck.ordered) {
    throw new Error(`further paging did not restore the produced history in order: ${JSON.stringify(furtherCheck)}`);
  }

  // ---- close/reopen: the local restore must not need a thread/read, and any
  //      page it issues must be for genuinely older uncached content ----
  // The contract (approved spec 246-259, 459-464) is that the loaded window is
  // restored locally without re-fetching content the durable rows already hold;
  // it is NOT that a reopened pane never issues any page request. A resumed
  // retained paging intent may issue one older thread/turns/list for content
  // below the loaded window; that request carries the retained cursor (see
  // retained/reopen-cursor-60turn for a run where that branch fires).
  const retainedDomain = await readTargetDomain(driver, SESSION_B);
  const retainedCursor = retainedDomain?.cursor ?? "";
  await driver.openSession(SESSION_A);
  await driver.waitPage(
    `(() => document.querySelector("[data-pane-scaffold='session:${SESSION_B}']") === null ? true : null)()`,
    { timeoutMs: 30000, label: "the selected pane closes when another session opens" },
  );
  const reopenBefore = traceBytes();
  await driver.openSession(SESSION_B);
  const restoredDomain = await waitForRestoredDomain(driver, SESSION_B, laterPageSentinel);
  const reopenAfter = traceBytes();
  const reopenWindow = countTargetRequestsInWindow(SESSION_B, reopenBefore, reopenAfter);
  const pageRequestCursors = targetPageRequestCursorsInWindow(SESSION_B, reopenBefore, reopenAfter);
  const retainedItemsPresent = restoredContainsRetained(retainedDomain, restoredDomain);
  const restoredHasSentinel = domainContent(restoredDomain).includes(laterPageSentinel);
  // A page request is older demand only when it carries the retained cursor
  // (content below the loaded window). A different cursor would be re-fetching
  // the window the durable rows already held.
  const olderDemand = pageRequestCursors.every((cursor) => cursor === retainedCursor);
  driver.milestone("reopen-without-read", {
    ref: SESSION_B,
    traceBefore: reopenBefore,
    traceAfter: reopenAfter,
    sentinel: laterPageSentinel,
    cursor: retainedCursor,
    restoredCursor: restoredDomain?.cursor ?? "",
    cursorRetentionExercised: retainedCursor !== "",
    reads: reopenWindow.reads,
    pages: reopenWindow.pages,
    pageRequestCursors,
    olderDemand,
    retainedItemsPresent,
    restoredHasSentinel,
  });
  if (reopenWindow.reads !== 0) {
    throw new Error(`reopen depended on a thread/read (reads=${reopenWindow.reads}) instead of the local durable rows`);
  }
  if (!olderDemand) {
    throw new Error(
      `reopen re-fetched content the durable rows held (page cursors ${JSON.stringify(pageRequestCursors)}, retained cursor ${JSON.stringify(retainedCursor)})`,
    );
  }
  if (!retainedItemsPresent) throw new Error("reopen did not restore the retained loaded window");
  if (!restoredHasSentinel) {
    throw new Error(`the retained later-page sentinel ${laterPageSentinel} is missing from the restored model`);
  }

  // ---- a real WebSocket gap, a turn completing during it, reconnect ----
  const reconnectTraceBefore = traceBytes();
  await driver.send("Network.emulateNetworkConditions", {
    offline: true,
    latency: 0,
    downloadThroughput: 0,
    uploadThroughput: 0,
  });
  // CDP offline does not close the live socket, so close the app's real hub
  // /rpc WebSocket(s) at the transport level; the offline emulation then keeps
  // the app from reconnecting until the network is restored below.
  const closedSockets = await evaluate(
    driver.send,
    `(() => {
       const open = (window.__backgroundTranscriptSockets ?? []).filter(
         (socket) => socket.readyState === WebSocket.OPEN && new URL(socket.url).pathname === "/rpc",
       );
       for (const socket of open) socket.close(4000, "background transcript guard transport boundary");
       return open.length;
     })()`,
  ).catch(() => -1);
  const disconnected = await waitForDisconnected(driver);
  const offlineSample = await observedConnection(driver);
  driver.milestone("ws-broken", {
    disconnected,
    closedSockets,
    appState: offlineSample.appState,
    online: offlineSample.online,
  });
  if (!disconnected) throw new Error("the browser never observed the network disruption");
  const preGap = targetPagingEvidence(SESSION_B);
  const preGapReads = preGap.readResponses;
  const preGapConnections = preGap.readConnections ?? [];
  // The gap turn must complete while the browser is disconnected, so use the
  // self-completing neutral history role; no unreleased phase gates it.
  driver.setRole("target", "history");
  driver.setAuto("target");
  driver.startTurn("target", GAP_SENTINEL);
  const gapAck = await waitForTurnComplete("target", GAP_SENTINEL, 60000);
  driver.milestone("gap-turn-completed", { sentinel: GAP_SENTINEL, turnId: gapAck.turnId, status: gapAck.status });
  await driver.send("Network.emulateNetworkConditions", {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  });
  await waitForPaneText(driver, SESSION_B, GAP_SENTINEL, 60000, "the gap turn arrived after reconnect");
  // Correlate the NEW connection's target authoritative read and its applied
  // replacement window/cursor: the entry route publishes the repository's
  // accepted model through its subscription, so poll for the applied window.
  const reconnect = await waitForNewTargetRead(driver, SESSION_B, preGapReads, 30000);
  const reconnectBefore = { readResponses: preGapReads, connections: preGapConnections };
  const appliedDeadline = Date.now() + 20000;
  let reconnectDomain = await readTargetDomain(driver, SESSION_B);
  let reconnectCheck = reconnectValidator(reconnectBefore, reconnect, {
    cursor: reconnectDomain?.cursor ?? "",
    domain: reconnectDomain,
  });
  while (!reconnectCheck.appliedMatches && Date.now() < appliedDeadline) {
    await delay(150);
    reconnectDomain = await readTargetDomain(driver, SESSION_B);
    reconnectCheck = reconnectValidator(reconnectBefore, reconnect, {
      cursor: reconnectDomain?.cursor ?? "",
      domain: reconnectDomain,
    });
  }
  const reconnectTraceAfter = traceBytes();
  const reconnectedHasGap = domainContent(reconnectDomain).includes(GAP_SENTINEL);
  driver.milestone("reconnected-latest-window", {
    traceBefore: reconnectTraceBefore,
    traceAfter: reconnectTraceAfter,
    sentinel: GAP_SENTINEL,
    newReads: reconnectCheck.newReads,
    cursorAfter: reconnect.lastReadOlderCursor,
    hasGap: reconnectedHasGap,
  });
  if (!reconnectCheck.reconnected) {
    throw new Error("reconnect produced no new target authoritative read on a new connection");
  }
  if (!reconnectCheck.appliedMatches) {
    throw new Error(
      `the reconnect's applied window does not match the new connection's read: ${JSON.stringify(reconnectCheck)}`,
    );
  }
  if (!reconnectedHasGap) throw new Error("the reconnect replacement window does not contain the gap turn");
  // Page back to the formerly loaded later-page row and validate the produced
  // range in order with no duplicates. The row is checked in the RESTORED MODEL
  // (present + ordered), not by DOM visibility: after paging the whole history
  // the virtualizer is at the oldest rows, so an older-page row such as
  // BACKGROUND_HISTORY_27 need not be inside the top render window. The visible
  // later-page row guarantee is carried by the history-paging phase.
  const back = await reconnectPagedRange(driver, SESSION_B, laterPageSentinel, expectedHistoryRange(), 6, {
    visible: async () => domainContent(await readTargetDomain(driver, SESSION_B)).includes(laterPageSentinel),
  });
  driver.milestone("paged-back-sentinel", {
    sentinel: laterPageSentinel,
    pages: back.pages,
    noDuplicates: back.noDuplicates,
    sentinelPresent: back.sentinelVisible,
    complete: back.complete,
    historyOrdered: back.rangeCheck.ordered,
    historyCount: back.rangeCheck.loaded,
    expectedCount: back.rangeCheck.expectedCount,
  });
  if (back.pages < 1) throw new Error("paging back after reconnect issued no older target page");
  if (!back.noDuplicates) throw new Error("paging back introduced duplicate items");
  if (!back.sentinelVisible) {
    throw new Error(
      `the formerly loaded later-page sentinel ${laterPageSentinel} did not return after reconnect paging`,
    );
  }
  if (!back.rangeCheck.ordered) {
    throw new Error(`paging back did not restore the produced ordered range: ${JSON.stringify(back.rangeCheck)}`);
  }

  // ---- narrow-viewport repeat activation: compare real content before/after ----
  await positionOlderReading(driver, SESSION_B, laterPageSentinel);
  const readingBefore = await settledReading(driver, SESSION_B);
  await driver.screenshot("older-desktop");
  const narrowBefore = await readTargetDomain(driver, SESSION_B);
  const narrowTraceBefore = traceBytes();
  await applyViewport(driver.send, NARROW_VIEWPORT);
  await driver.waitPage(`Boolean(document.querySelector("[data-stacked-pane-type='session']"))`, {
    label: "the same session has mounted in the mobile host",
  });
  const restoredNarrow = await waitForRestoredDomain(driver, SESSION_B, laterPageSentinel);
  const readingAfter = await settledReading(driver, SESSION_B);
  driver.milestone("responsive-reading", {
    before: readingBefore, after: readingAfter, sentinel: laterPageSentinel,
    traceBefore: narrowTraceBefore, traceAfter: traceBytes(),
    beforePayload: (narrowBefore?.items ?? []).map(recordedItemIdentity),
    afterPayload: (restoredNarrow?.items ?? []).map(recordedItemIdentity),
    entry: await evaluate(driver.send, `window.backgroundTranscriptEvidence.entryRoute(${JSON.stringify(SESSION_B)})`),
  });
  await driver.screenshot("older-responsive");
  // Start the close control away from latest even when responsive preservation
  // is broken, so a new-pane-at-latest observation cannot be vacuous. The
  // responsive assertion remains mandatory below, after collecting both cases.
  await positionOlderReading(driver, SESSION_B, laterPageSentinel);
  const closedReading = await settledReading(driver, SESSION_B);
  await driver.screenshot("older-before-close");
  await openSessionInCurrentShell(driver, SESSION_A);
  const closedPanes = await evaluate(driver.send, `window.backgroundTranscriptEvidence.panes(${JSON.stringify(SESSION_B)})`);
  if (closedPanes.length !== 0) throw new Error("switching the main session did not retire the target pane");
  await openSessionInCurrentShell(driver, SESSION_B);
  const newReading = await settledReading(driver, SESSION_B);
  driver.milestone("new-pane-latest", { before: closedReading, closedPanes, after: newReading });
  await driver.screenshot("new-pane-latest");
  assertNewPaneLatest(closedReading, newReading);
  await waitForPaneText(
    driver,
    SESSION_B,
    GAP_SENTINEL,
    30000,
    "preserved content after repeat activation at a narrow viewport",
  );
  const narrowAfter = await waitForRestoredDomain(driver, SESSION_B, laterPageSentinel);
  const beforeItems = (narrowBefore?.items ?? []).map((item) => item.id).sort();
  const afterItems = (narrowAfter?.items ?? []).map((item) => item.id).sort();
  const contentPreserved = narrowViewportContentPreserved(narrowBefore, narrowAfter);
  const sentinelAfter = domainContent(narrowAfter).includes(GAP_SENTINEL);
  driver.milestone("narrow-viewport", {
    width: NARROW_VIEWPORT.width,
    traceBefore: narrowTraceBefore,
    traceAfter: traceBytes(),
    beforeItems: beforeItems.length,
    afterItems: afterItems.length,
    contentPreserved,
    sentinelAfter,
    // The full before/after item identities, so the Go owner can compare the
    // bodies independently of this driver-computed boolean.
    beforePayload: (narrowBefore?.items ?? []).map(recordedItemIdentity),
    afterPayload: (narrowAfter?.items ?? []).map(recordedItemIdentity),
  });
  if (!contentPreserved) {
    throw new Error(
      `narrow-viewport repeat activation changed the transcript content (before ${beforeItems.length}, after ${afterItems.length})`,
    );
  }
  if (!sentinelAfter) {
    throw new Error("narrow-viewport repeat activation lost the produced content after re-activation");
  }
  assertResponsiveReading(readingBefore, readingAfter, laterPageSentinel);
  await applyViewport(driver.send, VIEWPORT);

  driver.milestone("done", { mode: "entry-production" });
}

function nativeEntryEvidenceExpression(ref = SESSION_B) {
  return `(async () => {
    const evidence = window.backgroundTranscriptEvidence;
    const databaseName = evidence.entryCacheDatabase;
    const version = evidence.entryCacheVersion;
    const ref = ${JSON.stringify(ref)};
    // Never create the database as an observation side effect. The real cache
    // must have opened and committed its schema before native inspection starts.
    const databases = await indexedDB.databases();
    if (!databases.some((database) => database.name === databaseName && database.version === version)) return null;
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open(databaseName, version);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
      request.onupgradeneeded = () => reject(new Error("production database was not opened by the real cache"));
    });
    const stores = ["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"];
    for (const legacy of ["records", "current"]) if (db.objectStoreNames.contains(legacy)) stores.push(legacy);
    const transaction = db.transaction(stores, "readonly");
    const values = await Promise.all(stores.map((name) => new Promise((resolve, reject) => {
      const request = transaction.objectStore(name).getAll();
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    })));
    const metadata = values[3].find((row) => row.requestedRefs?.some((binding) => binding.ref === ref && binding.clientId === location.origin));
    if (!metadata) { db.close(); return null; }
    const historyKey = metadata.historyKey;
    const rows = values[0].filter((row) => row.historyKey === historyKey);
    const turns = values[1].filter((row) => row.historyKey === historyKey);
    const coverage = values[2].filter((row) => row.historyKey === historyKey);
    const legacyCount = (name) => {
      const index = stores.indexOf(name);
      return index < 0 ? 0 : values[index].filter((row) => row.ref === ref).length;
    };
    const legacyRecordCount = legacyCount("records") + legacyCount("current");
    db.close();
    return { historyKey, rows, turns, coverage, metadata, legacyRecordCount };
  })()`;
}

async function waitForNativeEntryEvidence(driver, predicate, label, ref = SESSION_B) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const value = await evaluate(driver.send, nativeEntryEvidenceExpression(ref));
    if (value && predicate(value)) return value;
    await delay(100);
  }
  throw new Error(`timed out waiting for ${label}`);
}

// waitForPaneText waits until the named pane's own text carries the marker.
async function waitForPaneText(driver, ref, marker, timeoutMs, label) {
  return driver.waitPage(paneContainsExpr(ref, [marker]), { timeoutMs, label });
}

// scrollSentinelIntoView scrolls the real port to the top until the wanted
// history sentinel renders; the virtualizer may need the scroll first.
async function scrollSentinelIntoView(driver, ref, wanted, rounds = 8) {
  for (let round = 0; round < rounds; round += 1) {
    if (await evaluate(driver.send, paneContainsExpr(ref, [wanted])).catch(() => null)) return true;
    await scrollTranscriptToTop(driver, ref);
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  return Boolean(await evaluate(driver.send, paneContainsExpr(ref, [wanted])).catch(() => null));
}

// waitForRestoredDomain waits for a local reopen to republish the restored
// model: the pane's model must carry the retained later-page sentinel. A
// restore that needed the wire still shows up as a read/page in the caller's
// trace window.
async function waitForRestoredDomain(driver, ref, sentinel, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await readTargetDomain(driver, ref).catch(() => null);
    if (last && (last.items?.length ?? 0) > 0 && domainContent(last).includes(sentinel)) return last;
    await delay(150);
  }
  return last;
}

// A hidden page gets no rendering opportunities, so a viewport change made
// while the page is hidden never reaches the responsive host (the media-query
// change event does not fire). Callers that hide the page must bring it back
// to the front before waiting here.
async function waitForResponsiveHost(driver) {
  await driver.waitPage(`(() => {
    const mobile = matchMedia("(max-width: 899px)").matches;
    return mobile === Boolean(document.querySelector("[data-stacked-pane-type]"));
  })()`, { label: "the responsive host has committed" });
}

// openSessionInCurrentShell activates a session through whichever shell the
// current viewport hosts. At or below the mobile breakpoint
// (useIsMobile.ts MOBILE_QUERY "(max-width: 899px)") the desktop RailHost is
// not mounted and the same rail lives in the mobile Sessions sheet, reached
// through the tree-drawer trigger. The guarantee - a real rail-row activation
// that re-focuses the session - is identical.
async function openSessionInCurrentShell(driver, ref) {
  await waitForResponsiveHost(driver);
  const rowSelector = `[data-session-ref='${ref}']`;
  const rowVisible = async () =>
    evaluate(
      driver.send,
      `(() => { const el = document.querySelector(${JSON.stringify(rowSelector)});
         if (!el) return false; const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; })()`,
    ).catch(() => false);
  if (!(await rowVisible())) {
    for (let attempt = 0; attempt < 5; attempt += 1) {
      await driver.click('[aria-label="Sessions"]');
      await delay(300);
      if (await rowVisible()) break;
    }
  }
  await driver.waitPage(`(() => {
    const row = document.querySelector(${JSON.stringify(rowSelector)});
    if (!row) return false;
    const rect = row.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0 &&
      row.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
  })()`, { label: "the session row is exposed for the click" });
  await driver.click(rowSelector);
  await driver.waitPage(
    `(() => document.querySelector(${JSON.stringify(driver.composerSelector(ref))}) !== null ? true : null)()`,
    { label: `composer for ${ref}` },
  );
}

// waitForNewTargetRead waits for the target's read count to advance past its
// pre-gap value, returning refreshed evidence (the reconnect's authoritative
// read on a new connection).
async function waitForNewTargetRead(_driver, ref, beforeReads, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let ev = targetPagingEvidence(ref);
  while (Date.now() < deadline) {
    ev = targetPagingEvidence(ref);
    if (ev.readResponses > beforeReads) return ev;
    await new Promise((r) => setTimeout(r, 150));
  }
  return ev;
}

async function runJourney(driver, entryProduction = false, harnessUrl) {
  // The legacy session-cache journey was retired with the legacy stores (6d-2b),
  // so the production entry route is the only journey. The flag still selects
  // the entry harness budget and is always true from the registered guard.
  if (!entryProduction) {
    throw new Error(
      "backgroundtranscriptguard requires --entry-production true: the legacy session-cache journey was removed with the legacy stores",
    );
  }
  return runEntryProductionJourney(driver, harnessUrl);
}

// runTwoTabFencing drives the entry route's Clear and delete fences across two
// REAL tabs sharing one native BroadcastChannel and one IndexedDB. The first tab
// is the journey's own page; the second is a real second document on the same
// guard harness. Only the fence entry points are invoked (threads.clearCachedSessions
// and markCacheSessionsDeleted, the same effect set a real Clear/delete owes the
// cache); the propagation and the durable removal are the browser's own.
async function runTwoTabFencing(driver, endpoint, harnessUrl) {
  const opened = await openPage(endpoint, "about:blank");
  const tab = await connectPage(endpoint, opened.id);
  try {
    const tabErrors = [];
    tab.ws.addEventListener("message", (message) => {
      const event = JSON.parse(message.data);
      if (event.method === "Runtime.exceptionThrown") {
        const details = event.params?.exceptionDetails;
        tabErrors.push(details?.exception?.description ?? details?.text ?? "exception");
      } else if (event.method === "Runtime.consoleAPICalled" && event.params?.type === "error") {
        tabErrors.push((event.params.args ?? []).map((arg) => arg.value ?? arg.description ?? "").join(" "));
      }
    });
    // Boot on the synchronous marker the harness sets before it imports the app,
    // the same check the first tab uses; the evidence globals arrive later.
    await navigateTo(tab, harnessUrl, BOOT);
    try {
      await evaluate(
        tab.send,
        `(async () => {
           const deadline = Date.now() + 30000;
           while (Date.now() < deadline) {
             if (typeof window.backgroundTranscriptEvidence !== "undefined" &&
                 typeof window.backgroundTranscriptGuard !== "undefined") return true;
             await new Promise((resolve) => setTimeout(resolve, 100));
           }
           return { booted: typeof window.backgroundTranscriptBooted, evidence: typeof window.backgroundTranscriptEvidence,
             guard: typeof window.backgroundTranscriptGuard, bootError: window.__backgroundGuardBootError ?? null,
             body: (document.body?.innerHTML ?? "").slice(0, 200) };
         })()`,
      ).then((ready) => {
        if (ready !== true)
          throw new Error(`second tab never exposed the entry evidence globals: ${JSON.stringify(ready)}`);
      });
    } catch (error) {
      throw new Error(
        `${error instanceof Error ? error.message : String(error)}\ntab errors:\n${tabErrors.join("\n") || "(none captured)"}`,
      );
    }
    // Opening the second tab hid the driver's page; restore it so a viewport
    // change still pending from the journey reaches the desktop rail before the
    // rail row is pressed.
    await driver.send("Page.bringToFront");
    await waitForResponsiveHost(driver);
    const call = (send, method, ...args) =>
      evaluate(send, `window.backgroundTranscriptGuard.${method}(${args.map((arg) => JSON.stringify(arg)).join(",")})`);
    const fence = (send, ref) => call(send, "fenceState", ref);
    const durable = (send, ref) => evaluate(send, nativeEntryEvidenceExpression(ref));

    // ---- delete fence: one tab deletes, the sibling observes over the channel ----
    const ref = SESSION_B;
    const seeded = await durable(tab.send, ref);
    if (!seeded || seeded.rows.length === 0) {
      throw new Error(`two-tab delete: the target has no durable entry rows to fence: ${JSON.stringify(seeded)}`);
    }
    const aBefore = await fence(driver.send, ref);
    const bBefore = await fence(tab.send, ref);
    if (aBefore.deletedRefs.includes(ref) || bBefore.deletedRefs.includes(ref)) {
      throw new Error(`two-tab delete: the ref was already fenced: ${JSON.stringify({ aBefore, bBefore })}`);
    }
    await call(driver.send, "delete", ref);
    const suppressDeadline = Date.now() + 15000;
    let bAfter = await fence(tab.send, ref);
    while (bAfter.lifetime?.suppressed !== true && Date.now() < suppressDeadline) {
      await delay(100);
      bAfter = await fence(tab.send, ref);
    }
    const aAfter = await fence(driver.send, ref);
    // The durable heal is asynchronous (deleteCachedRefs), so poll for the rows
    // to leave the shared IndexedDB rather than reading one instant.
    const removeDeadline = Date.now() + 15000;
    let removed = await durable(tab.send, ref);
    while (removed && removed.rows.length > 0 && Date.now() < removeDeadline) {
      await delay(100);
      removed = await durable(tab.send, ref);
    }
    driver.milestone("two-tab-delete-fence", {
      ref,
      aDeletedRefs: aAfter.deletedRefs.includes(ref),
      bSuppressed: bAfter.lifetime?.suppressed === true,
      bJoinedDeletedRefs: bAfter.deletedRefs.includes(ref),
      durableBefore: seeded.rows.length,
      durableRemoved: removed === null || removed.rows.length === 0,
    });
    if (!aAfter.deletedRefs.includes(ref)) {
      throw new Error("two-tab delete: the deleting tab did not fence deletedRefs");
    }
    if (bAfter.lifetime?.suppressed !== true) {
      throw new Error("two-tab delete: the sibling tab never observed suppression over the real BroadcastChannel");
    }
    if (bAfter.deletedRefs.includes(ref)) {
      throw new Error("two-tab delete: the sibling tab joined the deleting tab's deletedRefs");
    }
    if (!(removed === null || removed.rows.length === 0)) {
      throw new Error("two-tab delete: the durable record still exists in the shared IndexedDB");
    }
    // The sibling's fresh acquisition must not resurrect the deleted session:
    // the deletion fence refuses the hydration and no route or durable row
    // appears. Poll the real condition (the ref acquired no route) rather than
    // sleeping a fixed interval.
    await call(tab.send, "open", ref);
    const refusalDeadline = Date.now() + 15000;
    let routePresent = await evaluate(
      tab.send,
      `window.backgroundTranscriptEvidence.entryRoute(${JSON.stringify(ref)}).present`,
    );
    while (routePresent !== false && Date.now() < refusalDeadline) {
      await delay(100);
      routePresent = await evaluate(
        tab.send,
        `window.backgroundTranscriptEvidence.entryRoute(${JSON.stringify(ref)}).present`,
      );
    }
    const resurrected = await durable(tab.send, ref);
    const resurrectedRows = resurrected === null ? 0 : resurrected.rows.length;
    driver.milestone("two-tab-delete-refusal", { ref, routePresent, rowsAfterOpen: resurrectedRows });
    if (routePresent === true) {
      throw new Error("two-tab delete: the sibling's fresh acquisition opened a route for a deleted ref");
    }
    if (resurrectedRows !== 0) {
      throw new Error(`two-tab delete: a fresh sibling acquisition resurrected ${resurrectedRows} durable rows`);
    }

    // ---- clear fence: a held old-epoch lease is suppressed, not rebound ----
    await driver.openSession(SESSION_A);
    const armDeadline = Date.now() + 15000;
    let aClearBefore = await fence(driver.send, SESSION_A);
    while (aClearBefore.lifetime?.leaseEpoch === undefined && Date.now() < armDeadline) {
      await delay(100);
      aClearBefore = await fence(driver.send, SESSION_A);
    }
    if (aClearBefore.lifetime?.leaseEpoch === undefined) {
      throw new Error(`two-tab clear: the holding tab never captured a cache lease: ${JSON.stringify(aClearBefore)}`);
    }
    const cleared = await call(tab.send, "clear");
    if (!cleared.committed) {
      throw new Error(`two-tab clear: the sibling clear did not commit: ${JSON.stringify(cleared)}`);
    }
    const clearDeadline = Date.now() + 15000;
    let aClearAfter = await fence(driver.send, SESSION_A);
    while (aClearAfter.lifetime?.suppressed !== true && Date.now() < clearDeadline) {
      await delay(100);
      aClearAfter = await fence(driver.send, SESSION_A);
    }
    driver.milestone("two-tab-clear-fence", {
      ref: SESSION_A,
      epochBefore: aClearBefore.lifetime?.leaseEpoch ?? null,
      epochAfter: aClearAfter.lifetime?.leaseEpoch ?? null,
      suppressed: aClearAfter.lifetime?.suppressed === true,
    });
    if (aClearAfter.lifetime?.suppressed !== true) {
      throw new Error("two-tab clear: the holding tab never observed the sibling clear over the real BroadcastChannel");
    }
    if (aClearAfter.lifetime?.leaseEpoch !== aClearBefore.lifetime?.leaseEpoch) {
      throw new Error(
        `two-tab clear: the sibling clear rebound the held lease epoch (${aClearBefore.lifetime?.leaseEpoch} -> ${aClearAfter.lifetime?.leaseEpoch})`,
      );
    }
  } finally {
    try {
      tab.close();
    } catch {
      /* already closed */
    }
    try {
      await closePage(endpoint, opened.id);
    } catch {
      /* target already gone */
    }
  }
}

async function waitForEvidenceGlobal(driver) {
  const deadline = Date.now() + 90000;
  while (Date.now() < deadline) {
    const present = await evaluate(driver.send, "typeof window.backgroundTranscriptEvidence !== 'undefined'").catch(
      () => false,
    );
    if (present) return true;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  return false;
}

// seedTargetHistory produces the approved forty real history turns on the
// target's own daemon through the helper, before any browser app read. Each
// turn is awaited to its own correlated completion, so the session never gets a
// second concurrent turn/start. The role is restored to the target's gated
// before/after shape afterwards.
async function seedTargetHistory(driver) {
  driver.setRole("target", "history");
  driver.setAuto("target");
  for (let n = 1; n <= HISTORY_TURNS; n += 1) {
    const prompt = historySentinel(n);
    driver.startTurn("target", prompt);
    await waitForTurnComplete("target", prompt, 180000);
  }
  driver.clearAuto("target");
  driver.setRole("target", "target");
  driver.milestone("history-seeded", { turns: HISTORY_TURNS, target: SESSION_B });
}

// waitForTurnComplete waits for the helper's OWN per-turn completion ack,
// which records the submitted turn's id and real outcome read from its
// AppWire. A matching prompt alone means dispatched, and a global write means
// nothing about this turn; only ok:true for this turn is success. A recorded
// failure is thrown, never treated as completion.
async function waitForTurnComplete(which, prompt, timeoutMs = 120000) {
  const logPath =
    which === "target" ? process.env.BACKGROUND_GUARD_REQUEST_LOG_B : process.env.BACKGROUND_GUARD_REQUEST_LOG_A;
  if (!logPath) throw new Error(`no ${which} helper request log path is configured`);
  const deadline = Date.now() + timeoutMs;
  let tail = "";
  while (Date.now() < deadline) {
    let text = "";
    try {
      text = readFileSync(logPath, "utf8");
    } catch {
      /* not written yet */
    }
    tail = text.slice(-1000);
    for (const line of text.split("\n")) {
      if (!line.trim()) continue;
      let record;
      try {
        record = JSON.parse(line);
      } catch {
        continue;
      }
      if (record?.event !== "turn-complete" || record.prompt !== prompt) continue;
      if (record.ok === true) return record;
      throw new Error(
        `the ${which} turn ${JSON.stringify(prompt)} did not complete successfully: ${JSON.stringify(record)}`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for the ${which} turn ${JSON.stringify(prompt)} to complete; log tail: ${tail}`);
}

function historySentinel(n) {
  return `BACKGROUND_HISTORY_${String(n).padStart(2, "0")}`;
}

// paneTextExpr returns a page expression that resolves to the pane's text when
// it contains every wanted sentinel, or null otherwise.
function paneContainsExpr(ref, wanted) {
  return `(() => {
    const pane = document.querySelector("[data-pane-scaffold='session:${ref}']");
    if (!pane) return null;
    const text = pane.textContent ?? "";
    return ${JSON.stringify(wanted)}.every((marker) => text.includes(marker)) ? text.slice(0, 4000) : null;
  })()`;
}

// scrollPortExpr resolves the transcript's scrollable ancestor inside a pane.
const SCROLL_PORT_EXPR = (ref) => `(() => {
  const pane = document.querySelector("[data-pane-scaffold='session:${ref}']");
  if (!pane) return null;
  // The REAL scroll port is the VirtualList's own scroller: the first child div
  // of the transcript-virtual-list section (matching the working
  // transcriptscrollguard / editorial-preview guards). An ancestor is never it.
  return pane.querySelector("[data-testid='transcript-virtual-list'] > div");
})()`;

// scrollTranscriptToTop drives the REAL port to the top, which the paging
// coordinator reads (isNearTop -> loadOlder). A missing port is a failure, never
// a silent no-op on the wrong element.
async function scrollTranscriptToTop(driver, ref) {
  const result = await evaluate(
    driver.send,
    `(() => {
      const el = ${SCROLL_PORT_EXPR(ref)};
      if (!el) return null;
      el.scrollTop = 0;
      el.dispatchEvent(new Event("scroll", { bubbles: true }));
      return { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight };
    })()`,
  );
  if (!result) {
    throw new Error(
      `no transcript scroll port for ${ref}: expected [data-testid='transcript-virtual-list'] > div (the VirtualList scroller)`,
    );
  }
  return result;
}

// readTraceFrames decodes the real hub AppWire trace JSONL: each line's `frame`
// is parsed as JSON, with its connection and direction retained. It never scans
// escaped raw text for a value.
function readTraceFrames(tracePath = TRACE_PATH) {
  // A missing/unreadable expected trace must fail, not read as an empty trace.
  const text = readFileSync(tracePath).toString("utf8");
  const frames = [];
  const lines = text.split("\n");
  const last = lines.length - 1;
  // A live file may end with an unterminated partial write; only that final line
  // may be deferred. Any newline-terminated malformed record or frame is
  // corruption and must fail.
  const deferLast = !text.endsWith("\n");
  for (let i = 0; i < lines.length; i += 1) {
    const trimmed = lines[i].trim();
    if (!trimmed) continue;
    let record;
    try {
      record = JSON.parse(trimmed);
    } catch (error) {
      if (i === last && deferLast) continue;
      throw new Error(`trace ${tracePath} has a malformed record at line ${i + 1}: ${error.message}`);
    }
    if (record?.event !== "frame" || typeof record.frame !== "string") continue; // valid non-frame metadata
    let frame;
    try {
      frame = JSON.parse(record.frame);
    } catch (error) {
      if (i === last && deferLast) continue;
      throw new Error(`trace ${tracePath} has a malformed frame at line ${i + 1}: ${error.message}`);
    }
    frames.push({ connection: record.connection ?? "", direction: record.direction ?? "", frame });
  }
  return frames;
}

// targetPagingEvidence pairs each response with the most recent UNMATCHED
// request on the SAME connection and id, requiring opposite directions, so a
// reused id (sequentially or across the reconnect boundary) cannot be
// misattributed. It returns only the TARGET session's thread/read and
// thread/turns/list evidence.
function targetPagingEvidence(ref, tracePath = TRACE_PATH) {
  const frames = readTraceFrames(tracePath);
  const threadID = ref.replace(/^local:/, "");
  let initialOlderCursor = null;
  let readResponses = 0;
  let subscribedRead = false;
  let lastReadOlderCursor = null;
  const pageCursors = [];
  const readConnections = [];
  const readResponseCursors = [];
  const readResponseWindows = [];
  const pending = new Map();
  for (const { connection, direction, frame } of frames) {
    if (frame?.method && frame.id !== undefined) {
      let byId = pending.get(connection);
      if (!byId) {
        byId = new Map();
        pending.set(connection, byId);
      }
      byId.set(String(frame.id), { frame, direction });
      continue;
    }
    if (!frame?.result) continue;
    const byId = pending.get(connection);
    const held = byId?.get(String(frame.id));
    if (!held) continue;
    byId.delete(String(frame.id));
    if (held.direction === direction) continue;
    const params = held.frame.params ?? {};
    if (params.ref !== ref && params.threadId !== threadID) continue;
    if (held.frame.method === "thread/read") {
      readResponses += 1;
      if (params.subscribe === true || params.replaceSubscription === true) subscribedRead = true;
      const cursor = frame.result.olderCursor ?? "";
      if (initialOlderCursor === null) initialOlderCursor = cursor;
      lastReadOlderCursor = cursor;
      readConnections.push(connection);
      readResponseCursors.push(cursor);
      readResponseWindows.push(normalizeWindowFromWire(frame.result.thread?.turns));
    } else if (held.frame.method === "thread/turns/list") {
      pageCursors.push(frame.result.nextCursor ?? "");
    }
  }
  return {
    initialOlderCursor,
    readResponses,
    subscribedRead,
    lastReadOlderCursor,
    readConnections,
    readResponseCursors,
    readResponseWindows,
    pageCursors,
    pages: pageCursors.length,
    lastCursor: pageCursors.length > 0 ? pageCursors[pageCursors.length - 1] : "",
  };
}

// scrollToTopAndWaitPage scrolls the real port to the top and waits until a NEW
// target thread/turns/list page has been issued, returning updated evidence.
async function scrollToTopAndWaitPage(driver, ref, beforePages, timeoutMs = 60000) {
  await scrollTranscriptToTop(driver, ref);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const evidence = targetPagingEvidence(ref);
    if (evidence.pages > beforePages) return evidence;
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`scrolling to the top of ${ref} issued no older thread/turns/list page (still ${beforePages})`);
}

// pageBackToSentinel pages older until the sentinel renders, requiring real
// thread/turns/list pages and returning how many were issued.
async function _pageBackToSentinel(driver, ref, sentinel, maxPages = 6) {
  let evidence = targetPagingEvidence(ref);
  const startPages = evidence.pages;
  for (let i = 0; i <= maxPages; i += 1) {
    if (await evaluate(driver.send, paneContainsExpr(ref, [sentinel])).catch(() => null)) {
      return { pages: evidence.pages - startPages, cursor: evidence.lastCursor };
    }
    if (i === maxPages) break;
    evidence = await scrollToTopAndWaitPage(driver, ref, evidence.pages);
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  throw new Error(`paging back never rendered ${JSON.stringify(sentinel)} for ${ref} within ${maxPages} pages`);
}

// transcriptState reads the loaded row count and scroll anchor for the history
// holder assertion.
async function transcriptState(driver, ref) {
  return evaluate(
    driver.send,
    `(() => {
      const pane = document.querySelector("[data-pane-scaffold='session:${ref}']");
      const list = pane && pane.querySelector("[data-testid='transcript-virtual-list']");
      const el = ${SCROLL_PORT_EXPR(ref)};
      const rowEls = list ? [...list.querySelectorAll("[data-testid='transcript-row']")] : [];
      let anchor = { itemId: null, offset: null };
      if (el && rowEls.length > 0) {
        const portTop = el.getBoundingClientRect().top;
        let best = null;
        for (const row of rowEls) {
          const rect = row.getBoundingClientRect();
          if (rect.top <= portTop && portTop < rect.bottom) { best = { row, offset: portTop - rect.top }; break; }
        }
        if (!best) {
          const first = rowEls[0].getBoundingClientRect();
          best = { row: rowEls[0], offset: portTop - first.top };
        }
        // The real transcript row exposes data-row-id (TranscriptBody.tsx:422/438/483).
        anchor = { itemId: best.row.getAttribute("data-row-id"), offset: Math.round(best.offset) };
      }
      const port = el?.getBoundingClientRect();
      const entries = el ? [...el.querySelectorAll("[data-view-anchor-id]")].map(entry => {
        const rect = entry.getBoundingClientRect();
        return { id: entry.getAttribute("data-view-anchor-id"), offset: rect.top - port.top,
          height: rect.height, text: entry.textContent ?? "" };
      }).filter(entry => entry.height > 0 && entry.offset + entry.height > 0 && entry.offset < el.clientHeight) : [];
      const semantic = entries.find(entry => entry.offset <= 0) ?? entries[0] ?? null;
      const visibleRows = port ? rowEls.filter(row => {
        const rect = row.getBoundingClientRect();
        return rect.bottom > port.top && rect.top < port.bottom;
      }).map(row => ({ id: row.getAttribute("data-row-id"), text: row.textContent ?? "" })) : [];
      return { rows: rowEls.length, scrollTop: el ? el.scrollTop : null, anchor,
        scrollHeight: el?.scrollHeight ?? null, clientHeight: el?.clientHeight ?? null,
        width: innerWidth, viewportWidth: el?.clientWidth ?? null, semantic, visibleRows,
        panes: window.backgroundTranscriptEvidence?.panes?.(${JSON.stringify(ref)}) ?? [] };
    })()`,
  );
}

// Observe committed DOM geometry, not a model claim. Repeated observations must
// agree before acceptance, including composer/virtualizer measurement changes.
async function settledReading(driver, ref) {
  await evaluate(driver.send, `window.backgroundTranscriptEvidence.entrySettled(${JSON.stringify(ref)})`);
  await driver.waitPage(`!window.backgroundTranscriptEvidence.entryRoute(${JSON.stringify(ref)}).localOpening`, {
    label: "the local opening hold has settled",
  });
  let previous;
  let stable = 0;
  let last;
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    await evaluate(driver.send, "new Promise(resolve => requestAnimationFrame(() => resolve(true)))");
    last = await transcriptState(driver, ref);
    const signature = JSON.stringify(last);
    stable = last.clientHeight > 0 && last.anchor.itemId && signature === previous ? stable + 1 : 0;
    if (stable >= 3) return last;
    previous = signature;
  }
  throw new Error(`reading geometry never settled: ${JSON.stringify(last)}`);
}

async function positionOlderReading(driver, ref, sentinel) {
  const domain = await waitForRestoredDomain(driver, ref, sentinel);
  const item = domain?.items.find(item => item.text === sentinel);
  if (!item) throw new Error(`older fixture item absent: ${sentinel}`);
  const result = await evaluate(driver.send, `(async () => {
    const port = ${SCROLL_PORT_EXPR(ref)};
    if (!port) throw new Error("older reading has no scroll port");
    const deadline = performance.now() + 15000;
    let aligned = 0;
    port.scrollTop = 0;
    while (performance.now() < deadline) {
      await new Promise(requestAnimationFrame);
      const row = [...port.querySelectorAll("[data-testid='transcript-row']")]
        .find(row => row.textContent.includes(${JSON.stringify(sentinel)}));
      if (row) {
        const delta = row.getBoundingClientRect().top - port.getBoundingClientRect().top + 8;
        aligned = Math.abs(delta) <= 1 ? aligned + 1 : 0;
        if (aligned >= 3) return true;
        port.scrollTop += delta;
      } else {
        aligned = 0;
        port.scrollTop += port.clientHeight / 2;
      }
    }
    throw new Error("older fixture row never rendered");
  })()`);
  if (!result) throw new Error("older reading gesture failed");
  const box = await evaluate(driver.send, `(() => {
    const port = ${SCROLL_PORT_EXPR(ref)};
    const rect = port.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2, top: port.scrollTop };
  })()`);
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseWheel", x: box.x, y: box.y, deltaX: 0, deltaY: 8,
  });
  await driver.waitPage(`(${SCROLL_PORT_EXPR(ref)}).scrollTop > ${box.top}`, {
    label: "the native wheel moves the older reader",
  });
}

function assertResponsiveReading(before, after, sentinel) {
  // This fixture reads a short semantic entry's visible tail. Measure that
  // tail directly in both DOMs, independently of production capture helpers.
  // A changed box height changes the offset, not the tail's reading line.
  // Two CSS px cover integer scroll read-back, not a different reading point.
  const oldEntry = before.semantic;
  const newEntry = after.semantic;
  if (before.width !== 1440 || after.width !== 420 ||
      before.panes.length !== 1 || after.panes.length !== 1 ||
      before.panes[0].identity !== after.panes[0].identity ||
      before.panes[0].id !== after.panes[0].id ||
      !before.anchor.itemId || before.anchor.itemId !== after.anchor.itemId ||
      !oldEntry?.id || oldEntry.id !== newEntry?.id ||
      !Number.isFinite(oldEntry.offset) || !Number.isFinite(newEntry.offset) ||
      !(oldEntry.height > 0 && oldEntry.height < before.clientHeight && oldEntry.offset < 0) ||
      !(newEntry.height > 0 && newEntry.offset <= 0 && newEntry.offset + newEntry.height > 0) ||
      !(before.viewportWidth > 0 && after.viewportWidth > 0 && before.viewportWidth !== after.viewportWidth) ||
      Math.abs((oldEntry.offset + oldEntry.height) - (newEntry.offset + newEntry.height)) > 2 ||
      !oldEntry.text.includes(sentinel) || !newEntry.text.includes(sentinel) ||
      before.scrollHeight - before.clientHeight - before.scrollTop <= before.clientHeight ||
      after.scrollHeight - after.clientHeight - after.scrollTop <= after.clientHeight ||
      !before.visibleRows.some(row => row.id === before.anchor.itemId && row.text.includes(sentinel)) ||
      !after.visibleRows.some(row => row.id === after.anchor.itemId && row.text.includes(sentinel))) {
    throw new Error(`responsive older reading changed: ${JSON.stringify({ before, after })}`);
  }
}

function assertNewPaneLatest(before, after) {
  if (before.panes.length !== 1 ||
      before.scrollHeight - before.clientHeight - before.scrollTop <= before.clientHeight ||
      !before.visibleRows.some(row => row.id === before.anchor.itemId && row.text.includes(historySentinel(LATER_PAGE))) ||
      after.panes.length !== 1 || after.panes[0].identity === before.panes[0].identity ||
      after.clientHeight <= 0 || !Number.isFinite(after.scrollTop) ||
      Math.abs(after.scrollHeight - after.clientHeight - after.scrollTop) > 2 ||
      !after.visibleRows.some(row => row.text.includes(GAP_SENTINEL))) {
    throw new Error(`new pane did not open at latest: ${JSON.stringify({ before, after })}`);
  }
}

// Owning holder capture (finding 5): the loaded domain, its cursor and the real
// reader anchor in ONE shape the validator consumes, so the journey can never
// pass a different shape than the validator reads.
async function holderSnapshot(driver, ref) {
  const domain = await readTargetDomain(driver, ref);
  const state = await transcriptState(driver, ref);
  return { domain, cursor: domain?.cursor ?? "", anchor: state.anchor };
}

// Owning pressure observation (finding 5): the newly produced operation's
// complete content must be applied in the PRODUCER's own model. The pressure
// turn runs on the producer session; the holder stays unchanged.
function producerOperationApplied(domain, expected) {
  if (typeof expected === "string") return domainContent(domain).includes(expected);
  const turnItems = (domain?.items ?? []).filter((item) => item.turnId === expected?.turnId);
  if (turnItems.length === 0) return false;
  const text = turnItems.map((item) => (item.text ?? "") + (item.output ?? "")).join("\\n");
  if (typeof expected?.content !== "string" || !text.includes(expected.content)) return false;
  return expected?.releaseSentinel === undefined || text.includes(expected.releaseSentinel);
}

// Wait until the producer session's own model carries the produced operation.
async function waitForProducerOperationApplied(driver, ref, marker, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await readTargetDomain(driver, ref);
    if (producerOperationApplied(last, marker)) return true;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(
    `the producer ${ref} never applied the produced operation ${JSON.stringify(marker)}; last=${JSON.stringify(last)}`,
  );
}

// Owning holder validator (finding 5). Preserved means the full loaded content
// (id, text, output) and count are unchanged, the cursor is unchanged, and the
// reader's anchored item and within-item offset are unchanged.
function holderValidator(before, after) {
  const beforeItems = before.domain?.items ?? [];
  const afterItems = after.domain?.items ?? [];
  const contentEqual = after.domain !== null && orderedHistoryEqual(beforeItems, afterItems);
  const cursorEqual = (before.cursor ?? "") === (after.cursor ?? "");
  const anchorValid = (anchor) =>
    anchor !== null &&
    anchor !== undefined &&
    typeof anchor.itemId === "string" &&
    anchor.itemId.length > 0 &&
    Number.isFinite(anchor.offset);
  const anchorEqual =
    anchorValid(before.anchor) &&
    anchorValid(after.anchor) &&
    before.anchor.itemId === after.anchor.itemId &&
    before.anchor.offset === after.anchor.offset;
  return {
    preserved: contentEqual && cursorEqual && anchorEqual,
    itemsBefore: beforeItems.length,
    itemsAfter: afterItems.length,
    contentEqual,
    cursorEqual,
    anchorEqual,
  };
}

// Owning retained-history validator (finding 3). The reopened model must carry
// the retained loaded history in the SAME order with the SAME full content
// (id, text, output), the sentinel, and the same cursor - not merely an id
// subset.
function retainedHistoryValidator(retained, restored) {
  const retainedItems = retained.domain?.items ?? [];
  const restoredItems = restored?.items ?? [];
  const contentOrdered = orderedHistoryEqual(retainedItems, restoredItems);
  return {
    keptItems: contentOrdered,
    contentOrdered,
    restoredHasSentinel: domainContent(restored).includes(retained.sentinel),
    cursorEqual: (restored?.cursor ?? "") === (retained.cursor ?? ""),
    retainedCursor: retained.cursor ?? "",
    restoredCursor: restored?.cursor ?? "",
    ok:
      contentOrdered &&
      domainContent(restored).includes(retained.sentinel) &&
      (restored?.cursor ?? "") === (retained.cursor ?? ""),
  };
}

// Owning further-paging validator (finding 3): the loaded history must be the
// literal produced history in order (a no-skip, no-reorder prefix of the
// expected sequence), not only unique ids.
function furtherHistoryValidator(domain, expected = expectedHistorySequence()) {
  const loaded = (domain?.items ?? [])
    .map((i) => i.text ?? "")
    .filter((text) => /^BACKGROUND_HISTORY_\d\d$/.test(text));
  const ordered = loaded.length === expected.length && loaded.every((text, index) => text === expected[index]);
  return {
    ordered,
    count: loaded.length,
    expectedCount: expected.length,
    oldestPresent: loaded[0] === expected[0],
  };
}

// The independently produced history range: each produced prompt followed by
// its assistant item.
function expectedHistoryRange() {
  const out = [];
  for (let n = 1; n <= HISTORY_TURNS; n += 1) {
    out.push({ kind: "prompt", text: historySentinel(n) });
    out.push({ kind: "assistant", text: HISTORY_ASSISTANT });
  }
  return out;
}

// Reconnect paged-range validator: the produced prompt+assistant range in order.
function reconnectPagedRangeValidator(domain, expected = expectedHistoryRange()) {
  const loaded = [];
  for (const item of domain?.items ?? []) {
    const text = item.text ?? "";
    if (/^BACKGROUND_HISTORY_\d\d$/.test(text)) loaded.push({ kind: "prompt", text });
    else if (item.type === "agentMessage" && loaded.length > 0 && loaded[loaded.length - 1].kind === "prompt") {
      loaded.push({ kind: "assistant", text });
    }
  }
  return {
    ordered:
      loaded.length === expected.length &&
      loaded.every((entry, index) => entry.kind === expected[index].kind && entry.text === expected[index].text),
    loaded: loaded.length,
    expectedCount: expected.length,
  };
}

// Checkpoint commit predicate: baseline advancement plus the correlated new
// turn's full post-release content.
// Owning reconnect paging (finding 5): page back to the sentinel, then validate
// the independently produced ordered range (assistant items included).
async function reconnectPagedRange(
  driver,
  ref,
  sentinel,
  expected = expectedHistoryRange(),
  maxPages = 6,
  deps = null,
) {
  const read = deps?.read ?? (() => readTargetDomain(driver, ref));
  const visible =
    deps?.visible ??
    (async () => Boolean(await evaluate(driver.send, paneContainsExpr(ref, [sentinel])).catch(() => null)));
  const page =
    deps?.page ??
    (async () => {
      const evidence = targetPagingEvidence(ref);
      await scrollToTopAndWaitPage(driver, ref, evidence.pages);
      await new Promise((resolve) => setTimeout(resolve, 300));
    });

  let domain = await read();
  let rangeCheck = reconnectPagedRangeValidator(domain, expected);
  let sentinelVisible = await visible();
  let pages = 0;
  let exhausted = false;
  // Keep paging while either the visible sentinel or the complete produced range
  // is still pending, within the unchanged page cap. A page that issues no new
  // request is terminal (no more older content exists).
  for (let i = 0; i < maxPages && !(sentinelVisible && rangeCheck.ordered); i += 1) {
    try {
      await page();
      pages += 1;
    } catch {
      exhausted = true;
      break;
    }
    domain = await read();
    rangeCheck = reconnectPagedRangeValidator(domain, expected);
    sentinelVisible = await visible();
  }
  const ids = (domain?.items ?? []).map((item) => item.id);
  const complete = sentinelVisible && rangeCheck.ordered;
  return {
    pages,
    domain,
    noDuplicates: new Set(ids).size === ids.length,
    sentinelVisible,
    rangeCheck,
    complete,
    pending: !complete && !exhausted && pages < maxPages,
    exhausted,
  };
}

function batchingCheckpointSatisfied(preRelease, postRelease, expected) {
  const advanced = (postRelease?.appliedSequence ?? 0) > (preRelease?.appliedSequence ?? 0);
  const turn = (postRelease?.turns ?? []).find((candidate) => candidate.id === expected?.turnId);
  const text = turn ? (turn.items ?? []).map((item) => item.text ?? "").join("\n") : "";
  return advanced && text.includes(expected?.workload) && text.includes(expected?.releaseSentinel);
}

function countOccurrences(haystack, needle) {
  let count = 0;
  let index = haystack.indexOf(needle);
  while (index !== -1) {
    count += 1;
    index = haystack.indexOf(needle, index + needle.length);
  }
  return count;
}

// Run the journey only when this module is the program entry point, so importing
// it for tests neither launches Chrome nor touches the filesystem.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().then(
    () => {
      writeFileSync(path.join(ARTIFACT_DIR, "result.json"), JSON.stringify({ ok: true }, null, 2));
      process.exitCode = 0;
    },
    (error) => {
      try {
        writeFileSync(
          path.join(ARTIFACT_DIR, "result.json"),
          JSON.stringify({ ok: false, error: error?.stack ?? String(error) }, null, 2),
        );
      } catch {
        /* artifact dir may be gone */
      }
      console.error(`backgroundtranscriptguard FAIL: ${error?.stack ?? error}`);
      process.exitCode = 1;
    },
  );
}

// Test seam. Shared with the reader-author's separate tree for integration.
export {
  accumulatePeak,
  assertNewPaneLatest,
  assertResponsiveReading,
  BUDGET_LIMITS,
  batchingCheckpointSatisfied,
  budgetFailures,
  collectProductionPeak,
  countTargetRequestsInWindow,
  EVIDENCE_EXPR,
  expectedHistoryRange,
  expectedHistorySequence,
  furtherHistoryValidator,
  holderSnapshot,
  holderValidator,
  narrowViewportContentPreserved,
  observedConnection,
  producerOperationApplied,
  readShellOutputRegion,
  readTraceFrames,
  reconnectPagedRange,
  reconnectPagedRangeValidator,
  reconnectValidator,
  reduceProducedPeak,
  renderedOutputExpr,
  retainedHistoryValidator,
  samplePeaksOnce,
  targetPagingEvidence,
  traceBytes,
  transcriptState,
  waitForDisconnected,
  waitForProducerOperationApplied,
  waitForRenderedOutput,
};
