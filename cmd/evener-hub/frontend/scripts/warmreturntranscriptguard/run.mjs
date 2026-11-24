#!/usr/bin/env node
// warmreturntranscriptguard — the Chrome proof for #7683: a warm reopen of a
// background-warmed session must not publish a pruned historical notice
// display ahead of the local restore that supplies its recorded bodies.
//
// It drives the REAL hub web app in Vite development mode in real Chrome
// against a REAL hub and a REAL `evener serve` daemon (started by
// TestWarmReturnTranscriptBrowser in cmd/evener-hub/warm_return_browser_test.go),
// with the only scripted piece the external LLM provider adapter. It reuses the
// background transcript guard's harness document and its evidence global; the
// production entry route, store, session cache and native IndexedDB are the
// ones the app ships.
//
// The run records STRUCTURAL COUNTS only (item, notice and body counts per
// committed model) and never writes transcript text to a milestone.
//
// Arguments:
//   --url            the hub's real /auth/<token> URL
//   --hub-url        the fixture hub base URL, exported before Vite starts
//   --artifact-dir   where the driver log and dumps survive
//   --control-path-a helper alpha's fixture control JSONL
//   --control-path-b helper beta's fixture control JSONL
//   --request-log-a  helper alpha's fixture request log
//   --request-log-b  helper beta's fixture request log
//   --trace-path     the real hub AppWire trace JSONL (read-only offsets)
//   --milestone-path JSONL the driver appends milestones to
//   --session-a      local:<id> of the empty control session
//   --session-b      local:<id> of the warm target session
import { appendFileSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  applyViewport,
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
import {
  countTargetRequestsInWindow,
  nativeEntryEvidenceExpression,
} from "../backgroundtranscriptguard/run.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const PROFILE_PREFIX = "warmreturntranscriptguard-chrome-";
const VIEWPORT = { width: 1440, height: 1000 };
const BOOT = {
  bootExpression: "window.backgroundTranscriptBooted === true",
  bootLabel: "the dev entry boot marker window.backgroundTranscriptBooted",
};
// The scripted target turn's own text, used only to locate the durable row that
// proves the recorded body was committed. It is fixture data, never milestone
// content.
const RECORDED_BODY = "BACKGROUND_BEFORE_EVICTION";

let URL = "";
let HUB_URL = "";
let ARTIFACT_DIR = "";
let CONTROL_PATHS = [];
let REQUEST_LOGS = [];
let TRACE_PATH = "";
let MILESTONE_PATH = "";
let SESSION_A = "";
let SESSION_B = "";

function loadConfig(argv) {
  const args = new Map();
  for (let i = 0; i < argv.length; i += 2) {
    const name = argv[i];
    if (!name?.startsWith("--") || i + 1 >= argv.length) throw new Error(`unexpected argument ${name}`);
    args.set(name.slice(2), argv[i + 1]);
  }
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
  if (missing.length > 0) throw new Error(`missing arguments: ${missing.join(", ")}`);
  URL = args.get("url");
  HUB_URL = args.get("hub-url");
  ARTIFACT_DIR = args.get("artifact-dir");
  CONTROL_PATHS = [args.get("control-path-a"), args.get("control-path-b")];
  REQUEST_LOGS = [args.get("request-log-a"), args.get("request-log-b")];
  TRACE_PATH = args.get("trace-path");
  MILESTONE_PATH = args.get("milestone-path");
  SESSION_A = args.get("session-a");
  SESSION_B = args.get("session-b");
}

function milestone(name, detail = {}) {
  appendFileSync(
    MILESTONE_PATH,
    `${JSON.stringify({ milestone: name, at: new Date().toISOString(), detail })}\n`,
  );
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function control(index, command) {
  appendFileSync(CONTROL_PATHS[index], `${JSON.stringify(command)}\n`);
}

// waitForTurnComplete waits for the helper's own per-turn ack for this prompt:
// a recorded failure is thrown, never treated as completion.
async function waitForTurnComplete(index, prompt, timeoutMs = 120000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    let text = "";
    try {
      text = readFileSync(REQUEST_LOGS[index], "utf8");
    } catch {
      /* not written yet */
    }
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
      throw new Error(`the target turn did not complete successfully: ${JSON.stringify(record)}`);
    }
    await delay(100);
  }
  throw new Error(`timed out waiting for the helper's turn-complete ack for ${JSON.stringify(prompt)}`);
}

// waitForEvidenceGlobal waits for the harness document's production evidence
// global, which the dev entry installs only after the real app module graph
// loads.
async function waitForEvidenceGlobal(driver, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const present = await evaluate(driver.send, "typeof window.backgroundTranscriptEvidence === 'object'");
    if (present === true) return;
    await delay(100);
  }
  throw new Error("the production-entry dev entry never exposed its evidence global");
}

// structuralStateExpr is the store model's shape, never its text: the guard's
// claim is about what a pane could render, so it counts notices against
// content-bearing items. It is self-contained (no recorder dependency) because
// the pre-restore wait observes the background lease before the recorder
// installs, and the harness evidence global is frozen.
function structuralStateExpr(ref) {
  return `(() => {
    const state = window.backgroundTranscriptEvidence;
    const model = state.model(${JSON.stringify(ref)});
    const items = (model?.turns ?? []).flatMap((turn) => turn.items ?? []);
    const overlay = model?.overlay ?? {};
    const noticeDisplay = items.filter((item) =>
      item.overlayKey !== undefined && overlay[item.overlayKey]?.kind === "notice").length;
    return {
      localOpening: state.entryRoute(${JSON.stringify(ref)}).localOpening,
      pending: state.entryRoute(${JSON.stringify(ref)}).localOpening,
      modelPresent: model !== undefined,
      displayItems: items.length,
      noticeDisplay,
      contentItems: items.filter((item) =>
        (item.text ?? "") !== "" || (item.output ?? "") !== "").length,
      overlayNotices: Object.values(overlay).filter((entry) => entry?.kind === "notice").length,
      historyItems: (model?.history?.turns ?? []).flatMap((turn) => turn.items ?? []).length,
      turns: (model?.turns ?? []).length,
    };
  })()`;
}

// PARK_EXPR patches only the entry cache's durable window read, so the local
// restore's pre-restore publication is deterministic. It is harness
// instrumentation of a production object; no product module is modified. The
// committed frames themselves are sampled from Node (startStructuralSampler),
// where the sampling cadence cannot be throttled by the page's own timers.
const PARK_EXPR = `(async () => {
  const cache = await import("/src/stores/sessionEntryCache.ts");
  let gate = null;
  const proto = cache.SessionEntryCache.prototype;
  if (proto.__warmReturnRealWindow === undefined) {
    proto.__warmReturnRealWindow = proto.getLatestWindow;
    proto.getLatestWindow = function (...args) {
      const armed = gate;
      if (armed === null) return proto.__warmReturnRealWindow.apply(this, args);
      return new Promise((resolve, reject) => {
        armed.parked += 1;
        armed.pending.push(() => proto.__warmReturnRealWindow.apply(this, args).then(resolve, reject));
      });
    };
  }
  window.__warmReturn = {
    arm: () => {
      gate = { parked: 0, pending: [] };
    },
    parked: () => gate?.parked ?? 0,
    release: () => {
      const armed = gate;
      gate = null;
      if (armed !== null) for (const run of armed.pending.splice(0)) run();
      return armed?.parked ?? 0;
    },
  };
  return true;
})()`;

async function waitForStructural(driver, predicate, label, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await evaluate(driver.send, structuralStateExpr(SESSION_B));
    if (value && predicate(value)) return value;
    await delay(100);
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function waitForParked(driver, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const parked = await evaluate(driver.send, "window.__warmReturn.parked()");
    if (parked >= 1) return parked;
    await delay(25);
  }
  throw new Error("the warm open never parked a durable window read");
}

// startStructuralSampler samples the app's own evidence global from Node, so
// the frame cadence is a driver cadence rather than a throttled page timer. It
// records every distinct structural state of the target ref, including the
// opening's pending flag, and never reads text.
function startStructuralSampler(driver) {
  const frames = [];
  let lastKey = "";
  let stopped = false;
  const keyOf = (frame) =>
    JSON.stringify([
      frame.pending,
      frame.modelPresent,
      frame.displayItems,
      frame.noticeDisplay,
      frame.contentItems,
      frame.overlayNotices,
      frame.historyItems,
      frame.turns,
    ]);
  const done = (async () => {
    while (!stopped) {
      let frame;
      try {
        frame = await evaluate(driver.send, structuralStateExpr(SESSION_B));
      } catch {
        break;
      }
      if (frame) {
        const key = keyOf(frame);
        if (key !== lastKey) {
          lastKey = key;
          frames.push(frame);
          if (frames.length > 400) frames.shift();
        }
      }
      await delay(20);
    }
  })();
  return {
    frames: () => frames.slice(),
    stop: async () => {
      stopped = true;
      await done;
      return frames.slice();
    },
  };
}

async function waitForSampledFrames(sampler, predicate, label, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const frames = sampler.frames();
    if (predicate(frames)) return frames;
    await delay(20);
  }
  throw new Error(`timed out waiting for ${label}: ${JSON.stringify(sampler.frames())}`);
}

// noticeOnlyFrame reports a committed model that paints only overlay notices
// while the open is pending: the frame the historical notice must never reach.
function noticeOnlyFrame(frame) {
  return frame.pending && frame.overlayNotices > 0 && frame.displayItems === frame.noticeDisplay && frame.historyItems > 0;
}

async function waitForNativeBody(driver, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const evidence = await evaluate(driver.send, nativeEntryEvidenceExpression(SESSION_B));
    if (evidence && evidence.rows.some((row) => row.item?.text?.includes(RECORDED_BODY))) return evidence;
    await delay(100);
  }
  throw new Error("the background lease never committed the recorded body to native entry storage");
}

async function main() {
  loadConfig(process.argv.slice(2));
  delete process.env.EVENER_HUB_ADDR;
  process.env.EVENER_HUB_URL = HUB_URL;
  process.env.BACKGROUND_GUARD_REQUEST_LOG_A = REQUEST_LOGS[0];
  process.env.BACKGROUND_GUARD_REQUEST_LOG_B = REQUEST_LOGS[1];

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
    url: URL,
    artifactDir: ARTIFACT_DIR,
    controlPath: CONTROL_PATHS[0],
    milestonePath: MILESTONE_PATH,
  });
  driver.sessionA = SESSION_A;
  driver.sessionB = SESSION_B;
  try {
    await waitForHttp(
      `http://127.0.0.1:${vitePort}/scripts/backgroundtranscriptguard/index.html`,
      "vite dev server",
      guard.getViteLaunchError,
    );
    const startupDeadline = createStartupDeadline();
    let cdpEndpoint;
    try {
      cdpEndpoint = await guard.waitForChrome({ signal: startupDeadline.signal });
      await waitForHttp(
        devtoolsHttpURL(cdpEndpoint, "/json/version"),
        "chrome devtools endpoint",
        guard.getChromeLaunchError,
        { signal: startupDeadline.signal, failure: guard.getChromeFailure() },
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

    try {
      await applyViewport(driver.send, VIEWPORT);
      await navigateTo(page, URL, { retryDelayMs: 250 });
      const harnessUrl = `http://127.0.0.1:${vitePort}/scripts/backgroundtranscriptguard/index.html?entry-production=true`;
      await navigateTo(page, harnessUrl, BOOT);
      await waitForEvidenceGlobal(driver);
      await driver.waitPage(
        `(() => document.querySelector("[data-testid='rail-brand']") !== null ? true : null)()`,
        { timeoutMs: 30000, label: "real app shell" },
      );
      await driver.waitPage(driver.railRowsExpr({ atLeast: 2 }), {
        timeoutMs: 30000,
        label: "both real fixture sessions in the rail",
      });
      // The app's ordinary background lease acquires both fixture sessions
      // before any pane opens, so the hub's own live history for the target is
      // attached and observes the turn's events. The target daemon then produces
      // one recorded turn through its own AppWire; the app meets it only as a
      // background session.
      await waitForStructural(
        driver,
        (value) => value.modelPresent,
        "the background lease's target model",
        60000,
      );
      control(1, { command: "background-auto" });
      control(1, { command: "background-start-turn", phase: "warm-recorded" });
      await waitForTurnComplete(1, "warm-recorded");
      control(1, { command: "background-auto", phase: "off" });

      // The background lease commits the recorded body to native storage and
      // publishes the pruned store model: overlay notices only.
      const native = await waitForNativeBody(driver);
      milestone("warm-pruned-native", {
        rows: native.rows.length,
        turns: native.turns.length,
        coverage: native.coverage.length,
      });
      const pruned = await waitForStructural(
        driver,
        (value) =>
          value.modelPresent &&
          value.historyItems > 0 &&
          value.overlayNotices > 0 &&
          value.displayItems === value.noticeDisplay,
        "the pruned background display (overlay notices, no recorded bodies)",
      );
      milestone("warm-pruned", { ref: SESSION_B, ...pruned });

      await evaluate(driver.send, PARK_EXPR);
      await evaluate(driver.send, "window.__warmReturn.arm()");
      const sampler = startStructuralSampler(driver);
      const traceBefore = statSync(TRACE_PATH).size;
      // The production rail press opens the pane; the entry route restores the
      // released bodies from the same native database.
      await driver.openSession(SESSION_B);
      const parked = await waitForParked(driver);
      const frames = await waitForSampledFrames(
        sampler,
        (value) => value.some((frame) => frame.pending),
        "a committed frame while the warm open is pending",
        5000,
      );
      const pending = frames.filter((frame) => frame.pending);
      const noticeOnly = frames.filter(noticeOnlyFrame);
      milestone("warm-opening", {
        parked,
        frames: frames.length,
        pendingFrames: pending.length,
        noticeOnlyFrames: noticeOnly.length,
        pendingShape: pending.map((frame) => ({
          modelPresent: frame.modelPresent,
          displayItems: frame.displayItems,
          noticeDisplay: frame.noticeDisplay,
          contentItems: frame.contentItems,
          overlayNotices: frame.overlayNotices,
          historyItems: frame.historyItems,
        })),
        traceBefore,
      });
      if (pending.length === 0) throw new Error("the warm open committed no pending frame to inspect");
      if (noticeOnly.length > 0) {
        throw new Error(
          `the warm open committed ${noticeOnly.length} notice-only frame(s) before the local restore`,
        );
      }
      await sampler.stop();
      const released = await evaluate(driver.send, "window.__warmReturn.release()");
      if (released < 1) throw new Error("the warm open parked no durable window read to release");
      await evaluate(driver.send, `window.backgroundTranscriptEvidence.entrySettled(${JSON.stringify(SESSION_B)})`);
      const restored = await waitForStructural(
        driver,
        (value) => value.contentItems > 0 && value.overlayNotices > 0 && !value.pending,
        "the restored display (recorded bodies with the historical notice)",
      );
      const traceAfter = statSync(TRACE_PATH).size;
      const activation = countTargetRequestsInWindow(SESSION_B, traceBefore, traceAfter, TRACE_PATH);
      milestone("warm-restored", { ref: SESSION_B, ...restored, traceBefore, traceAfter, activation });
      if (activation.reads !== 0 || activation.pages !== 0) {
        throw new Error(
          `the healthy warm reopen issued ${activation.reads} thread/read and ${activation.pages} thread/turns/list request(s)`,
        );
      }
      // The control session holds no released recorded body the restore owes, so
      // the hold must not apply to it: its own notices settle the local opening
      // instead of a pending display.
      await driver.openSession(SESSION_A);
      const controlDeadline = Date.now() + 30000;
      let controlState;
      while (Date.now() < controlDeadline) {
        controlState = await evaluate(driver.send, structuralStateExpr(SESSION_A));
        if (controlState && controlState.localOpening === false) break;
        await delay(100);
      }
      milestone("warm-no-bodies-control", { ref: SESSION_A, ...controlState });
      if (!controlState || controlState.localOpening) {
        throw new Error("the no-bodies control session never settled its local opening");
      }
      if (controlState.pending) {
        throw new Error("the no-bodies control session held a pending display it owes nothing for");
      }
      if (!controlState.modelPresent || controlState.displayItems < 1) {
        throw new Error("the no-bodies control session settled without publishing its own notices");
      }

      // A clear retires the durable cache and the entry route's publication
      // claims. Readable content legally stays in the store model
      // (threads.ts clearCachedSessions), so the measured boundary is the
      // durable one: the released rows must be gone from IndexedDB.
      await evaluate(driver.send, "window.backgroundTranscriptGuard.clear()");
      const deadline = Date.now() + 30000;
      let clearedNative;
      while (Date.now() < deadline) {
        clearedNative = await evaluate(driver.send, nativeEntryEvidenceExpression(SESSION_B));
        // The shared expression reports null once the clear has retired the
        // target's durable metadata (and the database with it), which is the
        // clear's own boundary.
        if (clearedNative === null) break;
        await delay(100);
      }
      const cleared = await evaluate(driver.send, structuralStateExpr(SESSION_B));
      milestone("warm-cleared", {
        ref: SESSION_B,
        nativeRows: clearedNative === null ? null : clearedNative.rows.length,
        nativeMetadata: clearedNative === null ? "absent" : "present",
        ...cleared,
      });
      if (clearedNative !== null) {
        throw new Error("the clear left the released rows in native entry storage");
      }
      milestone("done", { mode: "warm-return" });
    } finally {
      try {
        await page.close();
      } catch {
        /* already closed */
      }
    }
  } finally {
    await cleanup();
  }
}

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
      console.error(`warmreturntranscriptguard FAIL: ${error?.stack ?? error}`);
      process.exitCode = 1;
    },
  );
}

export { noticeOnlyFrame, structuralStateExpr };
