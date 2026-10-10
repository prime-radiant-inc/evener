#!/usr/bin/env node
// backgroundtranscriptguard — the whole-journey proof for background Live
// transcripts. It drives the REAL hub web app in Vite development mode in real
// Chrome through a REAL hub (started by TestBackgroundTranscriptsBrowser in
// cmd/evener-hub/background_transcripts_browser_test.go) against two REAL
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
//   --hub-url        the fixture hub base URL; exported as EVENER_HUB_ADDR for
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
  connectPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";
import { Driver } from "../skillguard/run.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const PROFILE_PREFIX = "backgroundtranscriptguard-chrome-";
const VIEWPORT = { width: 1440, height: 1000 };
const NARROW_VIEWPORT = { width: 420, height: 900 };

const BEFORE = "BACKGROUND_BEFORE_EVICTION";
const AFTER = "BACKGROUND_AFTER_EVICTION";
const TOOL_OUTPUT = "BACKGROUND_TOOL_OUTPUT";
const GAP_SENTINEL = "BACKGROUND_GAP_TURN";
const SUSTAINED_SENTINEL = "BACKGROUND_SUSTAINED_TURN";
const FULL_WORKLOAD = "W".repeat(16 * 1024);
const HISTORY_ASSISTANT = "BACKGROUND_HISTORY_REPLY";

// At least two older pages of history, and which turn is the visible
// later-page sentinel kept across the close/reopen.
// Forty real history turns are produced through the helper BEFORE the browser
// opens its app page, so the target's initial thread/read is a truncated real
// history window with a nonempty olderCursor. Forty short turns (~3 items each)
// span more than three 40-ITEM pages, leaving a nonempty cursor after two older
// pages are loaded.
const HISTORY_TURNS = 40;
const LATER_PAGE = 7;
// Completing sustained turns on the pressure session; their accumulated
// produced payload (6 x 16 KiB) exceeds the injected 64 KiB resident budget.
const SUSTAINED_TURNS = 6;

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
// The production default the injected limit replaces; the proof needs a payload
// between the two so eviction can only be explained by the injected limit.
const DEFAULT_RESIDENT_BYTES = 16 * 1024 * 1024;

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
let ENTRY_SLICE = false;

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
      "This guard must be invoked by TestBackgroundTranscriptsBrowser in cmd/evener-hub/background_transcripts_browser_test.go",
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
  ENTRY_SLICE = args.get("entry-slice") === "true";
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

function refEntry(evidence, ref) {
  return evidence.refs.find((entry) => entry.ref === ref) ?? null;
}

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
    if (frame.method === "thread/read") reads += 1;
    else if (frame.method === "thread/turns/list") pages += 1;
  }
  return { reads, pages };
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
  await driver.waitPage(
    `(() => {
       const row = document.querySelector(${JSON.stringify(`${pane} [data-testid='tool-call-item']`)});
       if (!row) return null;
       const btn = row.querySelector("button[aria-controls]");
       return btn ? true : null;
     })()`,
    { timeoutMs, label: `the shell disclosure control for ${ref}` },
  );
  await driver.click(toggleSelector);
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

function windowsEqual(a, b) {
  if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
  return a.every((turn, index) => {
    const other = b[index];
    return other && turn.turnId === other.turnId && orderedHistoryEqual(turn.items, other.items);
  });
}

// domainContent concatenates the target model's item text and output fields.
function domainContent(domain) {
  if (!domain) return "";
  return domain.items.map((i) => (i.text ?? "") + (i.output ?? "")).join("\n");
}

// waitForNewTargetRead waits until a NEW target thread/read response appears
// (the reconnect's authoritative read), returning the refreshed evidence.
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
  const windowExact =
    Array.isArray(responseWindow) && responseWindow.length > 0 && windowsEqual(appliedWindow, responseWindow);
  return {
    reconnected,
    newReads,
    newConnectionReads: newConnectionReads.length,
    responseCursor,
    responseWindow,
    appliedCursor,
    appliedMatches: reconnected && appliedCursor === responseCursor && windowExact,
  };
}

// targetCheckpointEvidence reads the TARGET's native current checkpoint as a
// structured value: its appliedSequence plus its history turns' items. Every
// created connection is closed.
async function targetCheckpointEvidence(driver, ref) {
  const expr = `(async () => {
    try {
      const mod = await import("/src/stores/sessionCacheIndexedDB");
      const openDb = (name) => new Promise((res, rej) => {
        const r = indexedDB.open(name);
        r.addEventListener("success", () => res(r.result), { once: true });
        r.addEventListener("error", () => rej(r.error), { once: true });
      });
      const req = (r) => new Promise((res, rej) => {
        r.addEventListener("success", () => res(r.result), { once: true });
        r.addEventListener("error", () => rej(r.error), { once: true });
      });
      const db = await openDb("evener-session-cache");
      let rows = [];
      try {
        rows = await req(db.transaction("current", "readonly").objectStore("current").index("ref").getAll(${JSON.stringify(ref)}));
      } finally {
        db.close();
      }
      const ownerToken = Array.isArray(rows) && rows.length > 0 ? rows[0].ownerToken : undefined;
      if (!ownerToken) return "";
      const cache = new mod.SessionCacheIndexedDB();
      try {
        const found = await cache.getCurrent([ownerToken, ${JSON.stringify(ref)}], Date.now());
        const state = found?.record?.state;
        return {
          appliedSequence: found?.record?.appliedSequence ?? 0,
          savedAt: found?.record?.savedAt ?? 0,
          turns: (state?.history?.turns ?? []).map((turn) => ({
            id: turn.id,
            items: (turn.items ?? []).map((item) => ({
              id: item.id,
              type: item.type,
              text: item.text ?? "",
              output: item.output ?? "",
              status: item.status ?? null,
            })),
          })),
        };
      } finally {
        cache.close();
      }
    } catch {
      return null;
    }
  })()`;
  return evaluate(driver.send, expr);
}

async function waitForCheckpointCommit(driver, ref, preRelease, expected, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await targetCheckpointEvidence(driver, ref).catch(() => null);
    if (last && batchingCheckpointSatisfied(preRelease, last, expected)) return true;
    await new Promise((r) => setTimeout(r, 250));
  }
  return false;
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

// waitForPhaseAckSince waits for a helper's NEW phase ack after a byte offset in
// its request log, so an old acknowledgment cannot satisfy it.
async function waitForPhaseAckSince(which, phase, sinceBytes, timeoutMs = 20000) {
  const logPath =
    which === "target" ? process.env.BACKGROUND_GUARD_REQUEST_LOG_B : process.env.BACKGROUND_GUARD_REQUEST_LOG_A;
  if (!logPath) throw new Error(`no ${which} helper request log configured`);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    let text = "";
    try {
      text = readFileSync(logPath, "utf8");
    } catch {
      /* not written yet */
    }
    for (const line of text.slice(sinceBytes).split("\n")) {
      if (!line.trim()) continue;
      let record;
      try {
        record = JSON.parse(line);
      } catch {
        continue;
      }
      if (record?.event === "background-phase-ack" && record.phase === phase) return true;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  return false;
}

function logByteLength(path) {
  try {
    return statSync(path).size;
  } catch {
    return 0;
  }
}

async function main() {
  loadConfig(process.argv.slice(2));
  // The Vite child inherits this environment; the dev server proxies /rpc and
  // the hub HTTP routes to the fixture hub. Set BEFORE startBrowserGuard.
  process.env.EVENER_HUB_ADDR = HUB_URL;

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

    const page = await connectPage(cdpEndpoint);
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
      const entryQuery = ENTRY_SLICE ? `?entryRef=${encodeURIComponent(SESSION_B)}` : "";
      const harnessUrl = `http://127.0.0.1:${vitePort}/scripts/backgroundtranscriptguard/index.html${entryQuery}`;
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
      await runJourney(driver, ENTRY_SLICE);
    } finally {
      try {
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

async function runEntrySliceJourney(driver) {
  const installed = await waitForEvidenceGlobal(driver);
  if (!installed) throw new Error("entry-slice dev entry did not expose the real app evidence global");
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

  driver.phase("after-eviction");
  await driver.waitPage(
    `(() => document.querySelector("[data-pane-scaffold='session:${SESSION_B}']")?.textContent?.includes(${JSON.stringify(AFTER)}) ? true : null)()`,
    { timeoutMs: 30000, label: "streaming output remains visible in the selected pane" },
  );
  await waitForTargetRunning(driver, SESSION_B);
  driver.phase("finish");
  await driver.waitForTurnIdle(SESSION_B, { timeoutMs: 60000 });
  await evaluate(driver.send, "window.backgroundTranscriptEvidence.entrySettled()");
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
  const renderedSentinelRows = await evaluate(
    driver.send,
    `(() => {
      const pane = document.querySelector("[data-pane-scaffold='session:${SESSION_B}']");
      if (!pane) return [];
      return [...pane.querySelectorAll("[data-view-anchor-turn-id][data-view-anchor-id]")]
        .map((row) => ({
          id: row.getAttribute("data-view-anchor-id"),
          turnId: row.getAttribute("data-view-anchor-turn-id"),
          text: row.textContent ?? "",
        }))
        .filter((row) => row.text.includes(${JSON.stringify(BEFORE)}) || row.text.includes(${JSON.stringify(AFTER)}));
    })()`,
  );
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
  driver.milestone("done", { mode: "entry-slice" });
}

function nativeEntryEvidenceExpression() {
  return `(async () => {
    const fixture = window.backgroundTranscriptEvidence.entryFixture();
    if (!fixture?.historyKey) return null;
    const databaseName = window.backgroundTranscriptEvidence.entryCacheDatabase;
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open(databaseName, 3);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
      request.onupgradeneeded = () => reject(new Error("entry database was not opened by the real cache"));
    });
    const stores = ["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"];
    const transaction = db.transaction(stores, "readonly");
    const values = await Promise.all(stores.map((name) => new Promise((resolve, reject) => {
      const request = transaction.objectStore(name).getAll();
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    })));
    const rows = values[0].filter((row) => row.historyKey === fixture.historyKey);
    const turns = values[1].filter((row) => row.historyKey === fixture.historyKey);
    const coverage = values[2].filter((row) => row.historyKey === fixture.historyKey);
    const metadata = values[3].find((row) => row.historyKey === fixture.historyKey) ?? null;
    db.close();
    const databases = await indexedDB.databases();
    let legacyRecordCount = 0;
    if (databases.some((database) => database.name === "evener-session-cache")) {
      const legacy = await new Promise((resolve, reject) => {
        const request = indexedDB.open("evener-session-cache");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      if (legacy.objectStoreNames.contains("records")) {
        const records = await new Promise((resolve, reject) => {
          const request = legacy.transaction("records", "readonly").objectStore("records").getAll();
          request.onsuccess = () => resolve(request.result);
          request.onerror = () => reject(request.error);
        });
        legacyRecordCount = records.filter((row) => row.ref === fixture.ref).length;
      }
      legacy.close();
    }
    return { historyKey: fixture.historyKey, rows, turns, coverage, metadata, legacyRecordCount };
  })()`;
}

async function waitForNativeEntryEvidence(driver, predicate, label) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const value = await evaluate(driver.send, nativeEntryEvidenceExpression());
    if (value && predicate(value)) return value;
    await delay(100);
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function runJourney(driver, entrySlice = false) {
  if (entrySlice) return runEntrySliceJourney(driver);
  const failures = [];

  // ---- assertion 0: the dev entry installed the tiny budgets BEFORE the app ----
  // The synchronous boot marker (checked by navigateTo) proves the entry ran;
  // the evidence global appears once the real app graph has loaded behind it.
  const installed = await waitForEvidenceGlobal(driver);
  if (!installed)
    throw new Error("the dev entry global window.backgroundTranscriptEvidence never appeared; the app did not boot");
  driver.milestone("installer-before-init", { evidenceGlobal: true, booted: true });

  // ---- a real hub connection before the workload ----
  await driver.waitPage("(() => document.querySelector(\"[data-testid='rail-brand']\") !== null ? true : null)()", {
    timeoutMs: 30000,
    label: "app shell (rail brand)",
  });
  const rows = await driver.waitPage(driver.railRowsExpr({ atLeast: 2 }), {
    timeoutMs: 30000,
    label: "two live sessions in the rail",
  });
  const rowRefs = new Set(rows.rows.map((row) => row.ref));
  if (!rowRefs.has(SESSION_A) || !rowRefs.has(SESSION_B)) {
    throw new Error(`rail does not show both fixture sessions: ${JSON.stringify(rows.rows)}`);
  }
  driver.milestone("hub-connected", { sessions: rows.rows.length });
  driver.milestone("sessions-visible", { sessionA: SESSION_A, sessionB: SESSION_B, rows: rows.rows });

  // ---- assertion 1: the UNOPENED target produces text in the background ----
  // The fixture starts the target's turn through its own daemon's real AppWire
  // (never its web pane), so only the target's background subscription can
  // observe the produced text. The pane is never opened to drive it.
  driver.startTargetTurn();
  driver.milestone("target-turn-started", { ref: SESSION_B, opened: false });

  // The unopened target's background subscription must apply the before text —
  // an actual read-only residency observation, not a coordinator flag.
  const backgroundBefore = await waitForEvidence(
    driver,
    (evidence) => {
      const entry = refEntry(evidence, SESSION_B);
      return entry !== null && entry.appliedSequence > 0;
    },
    "the unopened target's background read to apply its before-eviction text",
  );
  // The literal before-eviction text must be present in the target's own model,
  // not merely an advanced sequence: read the app's store content directly.
  const beforeContent = await waitForTargetContent(
    driver,
    SESSION_B,
    (content) => content.has.before,
    "the target's applied content to carry the literal before-eviction text",
  );
  driver.milestone("target-background-before", {
    observedBefore: true,
    contentHasBefore: beforeContent.has.before,
    receiptHasBefore: beforeContent.receiptHas.before,
    residentRefs: backgroundBefore.refs.map((entry) => entry.ref),
    committedWrites: backgroundBefore.committedWrites,
  });

  // A second sustained session's deterministic payload exceeds the injected
  // 64 KiB resident budget. The target must become absent from the resident map
  // AND have a committed checkpoint BEFORE the after-eviction phase is released.
  // The pressure must come from a BACKGROUND session: a foreground pane's model
  // is not charged to the background resident budget, so opening the second
  // session would never evict anything.
  // Its turn must COMPLETE so the large payload is recorded (a held overlay is
  // not charged as a resident body): auto phases release it immediately.
  driver.setAuto("alpha");
  // Several completing turns, each under the injected 32 KiB pending budget, so
  // their accumulated produced payload exceeds the injected 64 KiB resident
  // budget (a single larger update is rejected by the residency). Sample DURING
  // this production-to-settlement interval: the budget claim is about the
  // pressure turns, not a late window while producers are held.
  // Six completing turns, each awaited to THIS submitted turn's real outcome on
  // the helper's own AppWire (not a historical prompt match or a global write).
  const productionPeak = await collectProductionPeak({
    turns: SUSTAINED_TURNS,
    sentinel: SUSTAINED_SENTINEL,
    startTurn: (prompt) => driver.startTurn("alpha", prompt),
    waitForComplete: (prompt) => waitForTurnComplete("alpha", prompt),
    sample: () => samplePeaksOnce(driver),
  });

  const evicted = await waitForEvidence(
    driver,
    (evidence) => {
      const entry = refEntry(evidence, SESSION_B);
      // Absent from the resident map with its checkpoint committed (the entry
      // is `current`); eviction zeroes handoffBytes, so absence + current is the
      // real evidence, not handoffBytes > 0.
      return entry !== null && !entry.resident && entry.current;
    },
    "the target to be absent from the resident map with a committed checkpoint, before phase release",
  );
  const evictedEntry = refEntry(evicted, SESSION_B);
  driver.milestone("target-evicted", {
    resident: evictedEntry?.resident ?? null,
    targetResident: evictedEntry?.resident ?? null,
    targetChargedBytes: evictedEntry?.residentBytes ?? 0,
    residentBytes: evicted.residentBytes,
  });
  driver.milestone("target-checkpoint", {
    current: evictedEntry?.current ?? false,
    handoffBytes: evictedEntry?.handoffBytes ?? 0,
    committedWrites: evicted.committedWrites,
  });

  // ---- strict pre-release gate: the target's NATIVE current checkpoint must
  //      carry the literal before text BEFORE after-eviction is released. A
  //      current flag plus a later read does not prove this earlier gate.
  const checkpointBefore = await waitForTargetContent(
    driver,
    SESSION_B,
    (content) => content.idbHas.before,
    "the target's native checkpoint to carry the literal before text before the release",
  );
  driver.milestone("target-checkpoint-content", {
    ref: SESSION_B,
    idbHasBefore: checkpointBefore.idbHas.before,
    idbAppliedSequence: checkpointBefore.idbAppliedSequence,
    idbPresent: checkpointBefore.idbPresent,
  });

  // ---- assertion 2: ONLY NOW release after-eviction; the stream advances ----
  driver.phase("after-eviction");
  driver.milestone("after-eviction-released", {});

  const backgroundAfter = await waitForEvidence(
    driver,
    (evidence) => evidence.committedWrites > evicted.committedWrites,
    "the target's after-eviction background checkpoint",
  );
  const targetEntry = refEntry(backgroundAfter, SESSION_B);
  // The target is evicted, so its literal content must live in the native
  // IndexedDB current record: decode it with the production decoder and require
  // both the before and after sentinels. `current:true` alone proves nothing.
  const afterContent = await waitForTargetContent(
    driver,
    SESSION_B,
    (content) => content.idbHas.before && content.idbHas.after,
    "the target's decoded IndexedDB checkpoint to carry the literal before and after text",
  );
  driver.milestone("target-background-after", {
    observedBefore: targetEntry?.current,
    idbHasBefore: afterContent.idbHas.before,
    idbHasAfter: afterContent.idbHas.after,
    idbAppliedSequence: afterContent.idbAppliedSequence,
    residentRefs: backgroundAfter.refs.map((entry) => entry.ref),
    checkpointOK: afterContent.idbPresent,
    committedWrites: backgroundAfter.committedWrites,
  });

  // ---- assertion 3: open through the rail; assert rendered content + tool OUTPUT ----
  const traceBeforeOpen = traceBytes();
  await driver.openSession(SESSION_B);
  // The TOOL OUTPUT is the shell result's own output region. The command block's
  // arguments also carry the marker string, so a whole-pane substring is not
  // evidence of output.
  const paneSeen = await driver.waitPage(
    `(() => {
      const pane = document.querySelector("[data-pane-scaffold='session:${SESSION_B}']");
      if (!pane) return null;
      const text = pane.textContent ?? "";
      const sawBefore = text.includes(${JSON.stringify(BEFORE)});
      const sawAfter = text.includes(${JSON.stringify(AFTER)});
      return sawBefore && sawAfter ? { sawBefore, sawAfter } : null;
    })()`,
    { timeoutMs: 30000, label: "rendered before/after content in the target pane" },
  );
  const outputRegionSeen = await readShellOutputRegion(driver, SESSION_B, TOOL_OUTPUT);
  const targetDomain = await readTargetDomain(driver, SESSION_B);
  const toolOutputItem = targetDomain?.items.find(
    (i) => typeof i.output === "string" && i.output.includes(TOOL_OUTPUT),
  );
  const commandArgItem = targetDomain?.items.find(
    (i) => typeof i.arguments === "string" && i.arguments.includes(TOOL_OUTPUT),
  );
  const traceAfterReady = traceBytes();
  const activationWindow = countTargetRequestsInWindow(SESSION_B, traceBeforeOpen, traceAfterReady);
  driver.milestone("pane-opened", { ref: SESSION_B });
  driver.milestone("pane-rendered", {
    ...paneSeen,
    sawOutputRegion: outputRegionSeen,
    sawToolOutputField: Boolean(toolOutputItem),
  });
  driver.milestone("pane-activation-reads", {
    ref: SESSION_B,
    traceBefore: traceBeforeOpen,
    traceAfter: traceAfterReady,
    reads: activationWindow.reads,
    pages: activationWindow.pages,
  });
  if (!outputRegionSeen) failures.push("the shell OUTPUT region did not render the produced tool output");
  if (!toolOutputItem) failures.push("the target model has no tool item whose output carries the produced literal");
  if (!commandArgItem)
    failures.push("the shell command argument field is missing (cannot distinguish output from arguments)");

  // ---- assertion 3: observe the running turn, then finish; each item once ----
  await waitForTargetRunning(driver, SESSION_B);
  driver.phase("finish");
  await driver.waitForTurnIdle(SESSION_B, { timeoutMs: 60000 });
  await waitForEvidence(driver, (evidence) => refEntry(evidence, SESSION_B) !== null, "settled resident entry");
  const finishedDomain = await readTargetDomain(driver, SESSION_B);
  const finishedContent = domainContent(finishedDomain);
  const afterOnce = countOccurrences(finishedContent, AFTER);
  const toolOnce = countOccurrences(finishedContent, TOOL_OUTPUT);
  driver.milestone("turn-finished", { runningObserved: true });
  driver.milestone("exact-once", { afterOnce, toolOnce });
  if (afterOnce !== 1 || toolOnce !== 1)
    failures.push(
      `post-finish target content does not show each produced item exactly once: after=${afterOnce} tool=${toolOnce}`,
    );

  // ---- assertion 4: real older pages, later-page sentinel/cursor, handoff, reopen ----
  // History was seeded as forty real turns BEFORE the app read, so the target's
  // initial read must be a truncated real history window with a nonempty cursor.
  // The pane was never opened during setup/pressure; this is the later user open.
  const seeded = targetPagingEvidence(SESSION_B);
  if (seeded.readResponses === 0 || !seeded.initialOlderCursor) {
    failures.push(
      `the target's initial read is not a truncated real history window: readResponses=${seeded.readResponses}, initialOlderCursor=${JSON.stringify(seeded.initialOlderCursor)}`,
    );
  }
  await waitForPaneText(driver, SESSION_B, historySentinel(HISTORY_TURNS), 60000, "the newest history turn");

  // Load at least two REAL older pages through the real scroll port. Each scroll
  // must issue a thread/turns/list request; a synthetic event or an ancestor
  // element would issue none.
  let paging = await scrollToTopAndWaitPage(driver, SESSION_B, seeded.pages);
  paging = await scrollToTopAndWaitPage(driver, SESSION_B, paging.pages);
  const laterPageSentinel = historySentinel(LATER_PAGE);
  const renderedLater = await scrollSentinelIntoView(driver, SESSION_B, laterPageSentinel);
  if (!renderedLater) {
    failures.push(`the later-page sentinel ${laterPageSentinel} did not render after two older pages`);
  }
  if (!paging.lastCursor) {
    failures.push("two older pages exhausted the cursor; none remains for the post-reopen further-paging phase");
  }
  driver.milestone("history-pages", {
    turns: HISTORY_TURNS,
    initialOlderCursor: seeded.initialOlderCursor,
    pages: paging.pages,
    remainingCursor: paging.lastCursor,
    laterSentinel: laterPageSentinel,
    laterRendered: renderedLater,
  });

  // Retained local state before the pane closes: the reopened model must match
  // this WITHOUT any fetch-capable gesture.
  const retainedDomain = await readTargetDomain(driver, SESSION_B);
  const retainedCursor = retainedDomain?.cursor ?? "";

  // Close the target's pane (open the other session) so its model leaves the
  // resident map and hands to disk. The main slot is replaceable by design
  // (DockHost), so opening another session displaces it; observe the closure and
  // the model departing rather than assuming either.
  await driver.openSession(SESSION_A);
  await driver.waitPage(
    `(() => document.querySelector("[data-pane-scaffold='session:${SESSION_B}']") === null ? true : null)()`,
    { timeoutMs: 30000, label: "the target pane to close after opening another session" },
  );
  const handedOff = await waitForEvidence(
    driver,
    (evidence) => {
      const entry = refEntry(evidence, SESSION_B);
      return entry !== null && !entry.resident;
    },
    "the target handed to disk after its pane closed",
  );
  driver.milestone("disk-handoff", { paneClosed: true, residentBytes: handedOff.residentBytes });

  // Reopen WITHOUT a read: compare the RESTORED model to the retained state
  // BEFORE any gesture that could fetch missing data, and reject BOTH target
  // thread/read and thread/turns/list in the interval.
  const reopenBefore = traceBytes();
  await driver.openSession(SESSION_B);
  const restoredDomain = await readTargetDomain(driver, SESSION_B);
  const reopenAfter = traceBytes();
  const reopenWindow = countTargetRequestsInWindow(SESSION_B, reopenBefore, reopenAfter);
  const restoreCheck = retainedHistoryValidator(
    { domain: retainedDomain, cursor: retainedCursor, sentinel: laterPageSentinel },
    restoredDomain,
  );
  driver.milestone("reopen-without-read", {
    ref: SESSION_B,
    traceBefore: reopenBefore,
    traceAfter: reopenAfter,
    sentinel: laterPageSentinel,
    cursor: retainedCursor,
    restoredCursor: restoreCheck.restoredCursor,
    keptItems: restoreCheck.keptItems,
    restoredHasSentinel: restoreCheck.restoredHasSentinel,
    reads: reopenWindow.reads,
    pages: reopenWindow.pages,
  });
  if (reopenWindow.reads !== 0 || reopenWindow.pages !== 0) {
    failures.push(
      `reopen performed a fetch (reads=${reopenWindow.reads}, pages=${reopenWindow.pages}) instead of local restoration`,
    );
  }
  if (!restoreCheck.cursorEqual) {
    failures.push(
      `reopen changed the cursor: retained ${JSON.stringify(restoreCheck.retainedCursor)} vs restored ${JSON.stringify(restoreCheck.restoredCursor)}`,
    );
  }
  if (!restoreCheck.contentOrdered) failures.push("reopen lost retained loaded history content or order");
  if (!restoreCheck.restoredHasSentinel) {
    failures.push(`the retained later-page sentinel ${laterPageSentinel} is missing from the restored model`);
  }

  // Separately: further paging must issue a NEW real older target page and
  // preserve item identity/order without duplicates.
  const beforeFurther = targetPagingEvidence(SESSION_B).pages;
  let scrolled = await scrollToTopAndWaitPage(driver, SESSION_B, beforeFurther);
  let pagedDomain = await readTargetDomain(driver, SESSION_B);
  // Page until the produced history is loaded in full, oldest first (bounded).
  for (let attempt = 0; attempt < 6; attempt += 1) {
    if (furtherHistoryValidator(pagedDomain, expectedHistorySequence()).ordered) break;
    scrolled = await scrollToTopAndWaitPage(driver, SESSION_B, scrolled.pages);
    pagedDomain = await readTargetDomain(driver, SESSION_B);
  }
  const pagedIds = (pagedDomain?.items ?? []).map((i) => i.id);
  const noDuplicates = new Set(pagedIds).size === pagedIds.length;
  const furtherCheck = furtherHistoryValidator(pagedDomain, expectedHistorySequence());
  driver.milestone("further-paging", {
    pages: scrolled.pages - beforeFurther,
    cursor: scrolled.lastCursor,
    noDuplicates,
    historyOrdered: furtherCheck.ordered,
    historyCount: furtherCheck.count,
    expectedCount: furtherCheck.expectedCount,
  });
  if (scrolled.pages <= beforeFurther) failures.push("further paging after reopen issued no older target page");
  if (!noDuplicates) failures.push("further paging introduced duplicate items");
  if (!furtherCheck.ordered)
    failures.push(`further paging did not restore the produced history in order: ${JSON.stringify(furtherCheck)}`);
  // An open history holder (loaded pages + scroll anchor) must survive pressure
  // with its model intact: a foreground holder is protected from eviction.
  const holderBefore = await holderSnapshot(driver, SESSION_B);
  driver.setRole("alpha", "sustained");
  driver.startTurn("alpha", SUSTAINED_SENTINEL);
  // Await the pressure turn's OWN completion AND its newly produced operation
  // applied in the PRODUCER's (alpha) model, including its post-completion
  // sentinel. The holder (B) must be observed unchanged afterwards.
  const pressureAck = await waitForTurnComplete("alpha", SUSTAINED_SENTINEL);
  await waitForProducerOperationApplied(
    driver,
    SESSION_A,
    { turnId: pressureAck.turnId, content: FULL_WORKLOAD, releaseSentinel: AFTER },
    30000,
  );
  const holderAfter = await holderSnapshot(driver, SESSION_B);
  const holderCheck = holderValidator(holderBefore, holderAfter);
  const protectedModel = holderCheck.preserved;
  driver.milestone("history-holder", {
    itemsBefore: holderCheck.itemsBefore,
    itemsAfter: holderCheck.itemsAfter,
    cursorBefore: holderBefore.cursor,
    cursorAfter: holderAfter.cursor,
    anchorBefore: holderBefore.anchor?.offset ?? null,
    anchorAfter: holderAfter.anchor?.offset ?? null,
    protectedModel,
  });
  if (!protectedModel) failures.push("the open history holder lost loaded item identities under pressure");

  // ---- assertion 5: a real WebSocket gap, a completed turn in it, reconnect ----
  await driver.send("Network.emulateNetworkConditions", {
    offline: true,
    latency: 0,
    downloadThroughput: 0,
    uploadThroughput: 0,
  });
  // Observe the browser ACTUALLY offline before the gap producer runs.
  const disconnected = await waitForDisconnected(driver);
  driver.milestone("ws-broken", { disconnected });
  if (!disconnected) failures.push("the browser never observed the network disruption");
  const preGap = targetPagingEvidence(SESSION_B);
  const preGapReads = preGap.readResponses;
  const preGapConnections = preGap.readConnections ?? [];
  // The gap turn must complete while the browser is disconnected, so use the
  // self-completing neutral history role; no unreleased phase gates it.
  driver.setRole("target", "history");
  driver.setAuto("target");
  driver.startTurn("target", GAP_SENTINEL);
  // The helper observes the gap turn on its OWN daemon AppWire, independent of
  // the disconnected browser, so completion is the submitted turn's real
  // outcome and does not depend on the browser trace.
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
  // replacement window/cursor.
  const reconnect = await waitForNewTargetRead(driver, SESSION_B, preGapReads, 30000);
  const reconnectDomain = await readTargetDomain(driver, SESSION_B);
  const reconnectedHasGap = domainContent(reconnectDomain).includes(GAP_SENTINEL);
  const reconnectCheck = reconnectValidator({ readResponses: preGapReads, connections: preGapConnections }, reconnect, {
    cursor: reconnectDomain?.cursor ?? "",
    domain: reconnectDomain,
  });
  driver.milestone("reconnected-latest-window", {
    sentinel: GAP_SENTINEL,
    newReads: reconnectCheck.newReads,
    cursorAfter: reconnect.lastReadOlderCursor,
    hasGap: reconnectedHasGap,
  });
  if (!reconnectCheck.reconnected)
    failures.push("reconnect produced no new target authoritative read on a new connection");
  if (!reconnectCheck.appliedMatches)
    failures.push(
      `the reconnect's applied window does not match the new connection's read: ${JSON.stringify(reconnectCheck)}`,
    );
  if (!reconnectedHasGap) failures.push("the reconnect replacement window does not contain the gap turn");
  // Page back to the formerly visible later-page item and compare item
  // identity/order without duplicates.
  const back = await reconnectPagedRange(driver, SESSION_B, laterPageSentinel, expectedHistoryRange(), 6);
  driver.milestone("paged-back-sentinel", {
    sentinel: laterPageSentinel,
    pages: back.pages,
    noDuplicates: back.noDuplicates,
    sentinelVisible: back.sentinelVisible,
    complete: back.complete,
    historyOrdered: back.rangeCheck.ordered,
    historyCount: back.rangeCheck.loaded,
    expectedCount: back.rangeCheck.expectedCount,
  });
  if (back.pages < 1) failures.push("paging back after reconnect issued no older target page");
  if (!back.noDuplicates) failures.push("paging back introduced duplicate items");
  if (!back.sentinelVisible)
    failures.push(
      `the formerly visible later-page sentinel ${laterPageSentinel} did not render after reconnect paging`,
    );
  if (!back.rangeCheck.ordered)
    failures.push(`paging back did not restore the produced ordered range: ${JSON.stringify(back.rangeCheck)}`);

  // ---- assertion 6: narrow-viewport repeat activation ----
  await applyViewport(driver.send, NARROW_VIEWPORT);
  await driver.openSession(SESSION_A);
  await driver.openSession(SESSION_B);
  await waitForPaneText(
    driver,
    SESSION_B,
    GAP_SENTINEL,
    30000,
    "preserved content after repeat activation at a narrow viewport",
  );
  driver.milestone("narrow-viewport", { preserved: true, width: NARROW_VIEWPORT.width });
  await applyViewport(driver.send, VIEWPORT);

  // ---- assertion 6: bounded sustained two-session workload, observed peaks,
  //      and deterministic batching with acknowledged held-work phases ----
  // Establish the target as a BACKGROUND holder for batched held work: the main
  // slot is replaceable, so opening the other session displaces the target pane.
  await driver.openSession(SESSION_A);
  await driver.waitPage(
    `(() => document.querySelector("[data-pane-scaffold='session:${SESSION_B}']") === null ? true : null)()`,
    { timeoutMs: 30000, label: "the target pane to close before batching" },
  );
  driver.setRole("target", "sustained");
  driver.clearAuto("target");
  driver.setRole("alpha", "sustained");
  driver.clearAuto("alpha");
  // Fresh log offset: the target already acknowledged after-eviction earlier, so
  // only an ack AFTER this offset is evidence for THIS operation.
  const ackOffset = logByteLength(process.env.BACKGROUND_GUARD_REQUEST_LOG_B);
  driver.startTurn("target", SUSTAINED_SENTINEL);
  driver.startTurn("alpha", SUSTAINED_SENTINEL);
  // The sustained budget proof uses the six pressure turns' production interval
  // (productionPeak), sampled maxima — not an exhaustive bound.
  driver.milestone("sustained-workload", {
    peakResidentBytes: productionPeak.residentBytes,
    peakWorkerBytes: productionPeak.workerBytes,
    peakPendingBytes: productionPeak.pendingBytes,
    peakNetworkHydrations: productionPeak.networkHydrations,
    peakProducedBytes: productionPeak.producedBytes,
    committedWrites: productionPeak.committedWrites,
    limitBytes: LIMIT_RESIDENT_BYTES,
    pendingLimitBytes: LIMIT_PENDING_BYTES,
    workerLimitBytes: LIMIT_WORKER_BYTES,
    defaultLimitBytes: DEFAULT_RESIDENT_BYTES,
    hydrationLimit: MAX_NETWORK_HYDRATIONS,
    sampled: true,
  });
  console.log(
    `backgroundtranscriptguard: sustained resident peak ${productionPeak.residentBytes} (injected ${LIMIT_RESIDENT_BYTES}); produced peak ${productionPeak.producedBytes}; hydrations peak ${productionPeak.networkHydrations}`,
  );
  if (productionPeak.producedBytes <= LIMIT_RESIDENT_BYTES) {
    failures.push(
      `sustained produced payload peak ${productionPeak.producedBytes} does not exceed the injected ${LIMIT_RESIDENT_BYTES}-byte limit, so eviction under it is not proven`,
    );
  }
  if (productionPeak.producedBytes >= DEFAULT_RESIDENT_BYTES) {
    failures.push(
      `sustained produced payload peak ${productionPeak.producedBytes} is not below the ${DEFAULT_RESIDENT_BYTES}-byte default`,
    );
  }
  for (const failure of budgetFailures(productionPeak)) failures.push(failure);

  // Deterministic batching: hold the target at the after-eviction phase and show
  // its produced work is held (pending/worker bytes) before the acknowledged
  // release, then observe the commit only after the ack.
  const heldBeforeAck = await samplePeaksOnce(driver);
  const preReleaseCheckpoint = await targetCheckpointEvidence(driver, SESSION_B);
  driver.phaseOne("target", "after-eviction");
  const acknowledged = await waitForPhaseAckSince("target", "after-eviction", ackOffset, 20000);
  // The correlated new turn's own post-release state must commit past the
  // pre-release baseline; a global write count or another ref's commit does not.
  const batchingAck = await waitForTurnComplete("target", SUSTAINED_SENTINEL, 60000);
  const workloadCommitted = await waitForCheckpointCommit(
    driver,
    SESSION_B,
    preReleaseCheckpoint,
    { turnId: batchingAck.turnId, workload: FULL_WORKLOAD, releaseSentinel: AFTER },
    30000,
  );
  driver.milestone("batching", {
    heldPendingBytes: heldBeforeAck.pendingBytes,
    heldWorkerBytes: heldBeforeAck.workerBytes,
    ackObserved: acknowledged,
    targetBackground: true,
    workloadCommitted,
    committedWritesBefore: heldBeforeAck.committedWrites,
  });
  if (!acknowledged) failures.push("the batching release was never acknowledged after the log offset");
  if (!workloadCommitted) {
    failures.push("the target's native checkpoint never contained the produced sustained workload after the release");
  }
  driver.phaseOne("target", "finish");
  driver.phaseOne("alpha", "after-eviction");

  driver.milestone("done", { failures });
  if (failures.length > 0)
    throw new Error(`backgroundtranscriptguard journey assertions failed: ${failures.join(" | ")}`);
}

// waitForEvidence polls the read-only evidence gate until predicate(evidence)
// holds, then returns that evidence. It is bounded: a condition that never
// holds fails the guard rather than hanging it.
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

async function waitForEvidence(driver, predicate, label) {
  const deadline = Date.now() + 60000;
  let last = null;
  while (Date.now() < deadline) {
    const evidence = await evaluate(driver.send, EVIDENCE_EXPR);
    last = evidence;
    if (predicate(evidence)) return evidence;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for ${label}; last evidence: ${JSON.stringify(last)}`);
}

// targetContentExpr reads the target's LITERAL content two ways: the app's own
// store model through evidence.model(ref) (the same threads binding), and its
// native IndexedDB current record, decoded with the production
// SessionCacheIndexedDB decoder. `appliedSequence`/`current`/a global write
// count alone cannot certify either: the literal sentinels must be present.
function targetContentExpr(ref) {
  return `(async () => {
    const evidence = window.backgroundTranscriptEvidence;
    // Applied content for a RESIDENT ref: the SAME store binding the app uses,
    // full turns plus the live overlay. The synthetic prelude turn is excluded
    // (it is the system prompt, not applied content).
    const model = evidence.model(${JSON.stringify(ref)});
    const modelText = [
      (model?.turns ?? []).filter((turn) => turn.id !== "turn_system")
        .flatMap((turn) => (turn.items ?? []).map((item) => item.text ?? "")),
      ...Object.values(model?.overlay ?? {}).map((overlayItem) => overlayItem?.item?.text ?? ""),
    ].join("\\n");
    // Receipt evidence ONLY: the residency's retained updates prove a payload
    // arrived, not that it was applied to the model. Logged, never a predicate.
    const snapshot = evidence.snapshot();
    const entry = snapshot.refs.get(${JSON.stringify(ref)});
    const receiptText = [...(entry?.pending ?? []), ...(entry?.inFlight ?? [])]
      .map((update) => {
        const params = update?.notification?.params ?? {};
        const text = params.item?.item?.text ?? params.item?.text ?? params.delta ?? "";
        return typeof text === "string" ? text : "";
      })
      .join("\\n");
    let idb = null;
    try {
      const mod = await import("/src/stores/sessionCacheIndexedDB");
      const openDb = (name) => new Promise((resolve, reject) => {
        const request = indexedDB.open(name);
        request.addEventListener("success", () => resolve(request.result), { once: true });
        request.addEventListener("error", () => reject(request.error), { once: true });
      });
      const req = (request) => new Promise((resolve, reject) => {
        request.addEventListener("success", () => resolve(request.result), { once: true });
        request.addEventListener("error", () => reject(request.error), { once: true });
      });
      const database = await openDb("evener-session-cache");
      let rows = [];
      try {
        rows = await req(database.transaction("current", "readonly").objectStore("current").index("ref").getAll(${JSON.stringify(ref)}));
      } finally {
        database.close();
      }
      const ownerToken = Array.isArray(rows) && rows.length > 0 ? rows[0].ownerToken : undefined;
      if (ownerToken) {
        const cache = new mod.SessionCacheIndexedDB();
        try {
          const found = await cache.getCurrent([ownerToken, ${JSON.stringify(ref)}], Date.now());
          const state = found?.record?.state;
          const text = [
            (state?.history?.turns ?? []).flatMap((turn) => (turn.items ?? []).map((item) => item.text ?? "")),
            ...Object.values(state?.overlay ?? {}).map((overlayItem) => overlayItem?.item?.text ?? ""),
          ].join("\\n");
          idb = {
            appliedSequence: found?.record?.appliedSequence ?? null,
            savedAt: found?.record?.savedAt ?? null,
            text,
          };
        } finally {
          // Every created cache/connection must be closed.
          cache.close();
        }
      }
    } catch (error) {
      idb = { error: String(error) };
    }
    const sentinels = [${JSON.stringify(BEFORE)}, ${JSON.stringify(AFTER)}, ${JSON.stringify(TOOL_OUTPUT)}];
    const foundIn = (value) => ({
      before: typeof value === "string" && value.includes(sentinels[0]),
      after: typeof value === "string" && value.includes(sentinels[1]),
      tool: typeof value === "string" && value.includes(sentinels[2]),
    });
    // Literal booleans are computed on the FULL strings; the returned copies are
    // truncated only for logging, never reused for assertions.
    return {
      text: modelText.slice(0, 2000),
      has: foundIn(modelText),
      receiptHas: foundIn(receiptText),
      receiptText: receiptText.slice(0, 500),
      idbHas: foundIn(idb?.text),
      idbAppliedSequence: idb?.appliedSequence ?? null,
      idbPresent: idb !== null && idb !== undefined && !idb.error,
      idb: idb ? { ...idb, text: (idb.text ?? "").slice(0, 2000) } : idb,
    };
  })()`;
}

// waitForTargetContent polls the target content until predicate holds and
// returns it, so a transient IndexedDB lookup deadline cannot be read as
// absence. Bounded: a target whose content never appears fails loudly.
async function waitForTargetContent(driver, ref, predicate, label) {
  const deadline = Date.now() + 60000;
  let last = null;
  while (Date.now() < deadline) {
    const content = await evaluate(driver.send, targetContentExpr(ref));
    last = content;
    if (predicate(content)) return content;
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`timed out waiting for ${label}; last content: ${JSON.stringify(last)}`);
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

async function waitForPaneText(driver, ref, marker, timeoutMs, label) {
  return driver.waitPage(paneContainsExpr(ref, [marker]), { timeoutMs, label });
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
      return { rows: rowEls.length, scrollTop: el ? el.scrollTop : null, anchor };
    })()`,
  );
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
  targetCheckpointEvidence,
  targetPagingEvidence,
  traceBytes,
  transcriptState,
  waitForCheckpointCommit,
  waitForDisconnected,
  waitForProducerOperationApplied,
  waitForRenderedOutput,
};
