import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createConnection } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const runner = new URL("./run.mjs", import.meta.url);
const helper = new URL("../browserGuardProcess.mjs", import.meta.url);
const frontend = fileURLToPath(new URL("../../", import.meta.url));
const announcementTimeoutMs = 5000;
const supervisorTimeoutMs = 15000;

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}

async function portOpen(port) {
  const socket = createConnection({ host: "127.0.0.1", port });
  try {
    await once(socket, "connect");
    return true;
  } catch (error) {
    if (error.code === "ECONNREFUSED") return false;
    throw error;
  } finally {
    socket.destroy();
  }
}

// This nested real-process test is selected explicitly with node --test.
// No browser substitute and no missing-browser skip: startBrowserGuard resolves
// the installed Chrome and launches actual Vite before the injected boundary.
test("runner bounds a live Chrome with withheld stderr and cleans up before supervisor rescue", async () => {
  const root = process.env.DOCUMENT_FILE_LINKS_ARTIFACT_DIR ?? process.env.EVENER_SCRATCH_DIR ?? tmpdir();
  mkdirSync(root, { recursive: true });
  const artifacts = mkdtempSync(path.join(root, "document-startup-"));
  const events = [];
  const record = (event) => {
    events.push({ utc: new Date().toISOString(), ...event });
    writeFileSync(path.join(artifacts, "supervisor.json"), JSON.stringify(events, null, 2));
  };
  // Only resolve the runner's helper import differently. The runner source,
  // including the awaited startup call and its catch/finally, is unchanged.
  const adapterPath = path.join(artifacts, "adapter.mjs");
  const loaderPath = path.join(artifacts, "loader.mjs");
  writeFileSync(loaderPath, `
export async function resolve(specifier, context, nextResolve) {
  if (context.parentURL === ${JSON.stringify(runner.href)} && specifier === "../browserGuardProcess.mjs") {
    return { url: ${JSON.stringify(pathToFileURL(adapterPath).href)}, shortCircuit: true };
  }
  return nextResolve(specifier, context);
}
`);
  writeFileSync(adapterPath, `
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { appendFileSync, existsSync } from "node:fs";
import { PassThrough } from "node:stream";
import * as real from ${JSON.stringify(helper.href)};
const artifacts = ${JSON.stringify(artifacts)};
let chrome;
let vite;
let guard;
const emit = (event) => {
  const value = { utc: new Date().toISOString(), ...event };
  appendFileSync(artifacts + "/adapter.jsonl", JSON.stringify(value) + "\\n");
  process.send(value);
};
export async function startBrowserGuard(options) {
  guard = await real.startBrowserGuard({ ...options, spawnProcess(binary, argv, spawnOptions) {
    const child = spawn(binary, argv, spawnOptions);
    const isChrome = argv.includes("--remote-debugging-port=0");
    emit({ event: "spawn", kind: isChrome ? "chrome" : "vite", pid: child.pid,
      binary, argv, cwd: spawnOptions.cwd ?? process.cwd(), detached: spawnOptions.detached });
    child.on("exit", (code, signal) => emit({ event: "child-exit", kind: isChrome ? "chrome" : "vite", code, signal }));
    if (isChrome) {
      chrome = child;
      const stderr = child.stderr;
      // Drain and retain real stderr, but deliver none to the helper. All
      // ChildProcess exit/error/kill behavior and lifecycle ownership stay real.
      child.stderr = new PassThrough();
      let lines = "";
      stderr.on("data", (chunk) => {
        appendFileSync(artifacts + "/chrome-stderr.log", chunk);
        lines += chunk.toString();
        const complete = lines.split(/\\r?\\n/);
        lines = complete.pop();
        for (const line of complete) {
          const endpoint = real.parseChromeDevToolsAnnouncement(line);
          if (endpoint) emit({ event: "withheld-announcement", endpoint });
        }
      });
      stderr.on("end", () => child.stderr.end());
    } else {
      vite = child;
      child.stdout.on("data", (chunk) => appendFileSync(artifacts + "/vite-stdout.log", chunk));
      child.stderr.on("data", (chunk) => appendFileSync(artifacts + "/vite-stderr.log", chunk));
    }
    return child;
  }});
  emit({ event: "guard-started", chromePid: chrome.pid, vitePid: vite.pid,
    profileDir: guard.profileDir, vitePort: guard.vitePort,
    endpoint: guard.getChromeEndpoint(), firstStderr: guard.getChromeFirstStderrDelay() });
  return { ...guard, async cleanup() {
    emit({ event: "runner-cleanup-start", endpoint: guard.getChromeEndpoint(),
      firstStderr: guard.getChromeFirstStderrDelay(), chromeExit: chrome.exitCode,
      profileExists: existsSync(guard.profileDir) });
    await guard.cleanup();
    emit({ event: "runner-cleanup-end", profileExists: existsSync(guard.profileDir) });
  }};
}
export async function waitForBrowserReady(...args) {
  assert.equal(args.length, 1, "normal runner must use shared default, no timeout override");
  emit({ event: "shared-readiness", argumentCount: args.length, announcementTimeoutMs: ${announcementTimeoutMs} });
  return real.waitForBrowserReady(args[0], { announcementTimeoutMs: ${announcementTimeoutMs} });
}
`);
  const registration = `import { register } from 'node:module'; register(${JSON.stringify(pathToFileURL(loaderPath).href)}, ${JSON.stringify(import.meta.url)});`;
  const argv = ["--import", `data:text/javascript,${encodeURIComponent(registration)}`, fileURLToPath(runner)];
  const started = Date.now();
  const child = spawn(process.execPath, argv, {
    cwd: frontend,
    env: { ...process.env, DOCUMENT_FILE_LINKS_ARTIFACT_DIR: artifacts },
    stdio: ["ignore", "pipe", "pipe", "ipc"],
  });
  record({ event: "runner-spawn", binary: process.execPath, argv, cwd: frontend, pid: child.pid,
    runnerSource: readFileSync(runner, "utf8"), announcementTimeoutMs, supervisorTimeoutMs });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += chunk; writeFileSync(path.join(artifacts, "runner-stdout.log"), stdout); });
  child.stderr.on("data", (chunk) => { stderr += chunk; writeFileSync(path.join(artifacts, "runner-stderr.log"), stderr); });
  let guard;
  let supervisorRescue = false;
  let timer;
  let probe;
  const closed = once(child, "close");
  child.on("message", (event) => {
    record(event);
    if (event.event === "guard-started") {
      guard = event;
      timer = setTimeout(() => {
        supervisorRescue = true;
        record({ event: "supervisor-rescue", chromeAlive: alive(guard.chromePid), viteAlive: alive(guard.vitePid),
          profileExists: existsSync(guard.profileDir) });
        // This is deliberately supervisor-triggered signal cleanup, not the
        // runner finally. The adapter observes only runner cleanup calls.
        child.kill("SIGTERM");
      }, supervisorTimeoutMs);
    }
    if (event.event === "withheld-announcement") {
      probe = (async () => {
        const response = await fetch(`http://${event.endpoint.host}:${event.endpoint.port}/json/version`, {
          signal: AbortSignal.timeout(5000),
        });
        const version = await response.json();
        const observation = { event: "live-silent-proof", status: response.status, browser: version.Browser,
          chromeAlive: alive(events.find((e) => e.event === "spawn" && e.kind === "chrome").pid),
          viteOpen: await portOpen(guard.vitePort) };
        record(observation);
        return observation;
      })();
      // Observe rejection now, report it through the assertion below.
      probe.catch(() => {});
    }
  });
  try {
    const [code, signal] = await closed;
    clearTimeout(timer);
    record({ event: "runner-exit", code, signal, elapsedMs: Date.now() - started, supervisorRescue });
    assert(guard, "actual Vite readiness and actual Chrome spawn must finish");
    assert(probe, "real Chrome must announce on the withheld raw pipe");
    const live = await probe;
    assert.equal(live.status, 200);
    assert.match(live.browser, /Chrome|Chromium/);
    assert.equal(live.chromeAlive, true);
    assert.equal(live.viteOpen, true);
    const after = { event: "after-exit", chromeAlive: alive(guard.chromePid), viteAlive: alive(guard.vitePid),
      viteOpen: await portOpen(guard.vitePort), profileExists: existsSync(guard.profileDir) };
    record(after);
    assert.equal(after.chromeAlive, false);
    assert.equal(after.viteAlive, false);
    assert.equal(after.viteOpen, false);
    assert.equal(after.profileExists, false);
    assert.equal(supervisorRescue, false, "shared readiness must fail before supervisor rescue, not hang on raw waitForChrome");
    assert.equal(code, 1, "runner must fail, not claim browser journey success");
    assert.equal(signal, null);
    assert.match(stderr, /environment problem, not a test case failure/);
    assert.match(stderr, /browser startup deadline exceeded after 5000ms while waiting for Chrome's DevTools announcement on stderr/);
    assert.match(stderr, /Chrome had written nothing to stderr/);
    assert.equal(events.filter((e) => e.event === "shared-readiness").length, 1);
    const cleanup = events.find((e) => e.event === "runner-cleanup-start");
    assert(cleanup, "runner finally must own teardown");
    assert.equal(cleanup.endpoint, null);
    assert.equal(cleanup.firstStderr, null);
    assert.equal(cleanup.chromeExit, null, "Chrome must still be live when readiness fails");
    assert.equal(cleanup.profileExists, true);
    assert.equal(events.find((e) => e.event === "runner-cleanup-end")?.profileExists, false);
    assert.match(readFileSync(path.join(artifacts, "failure.txt"), "utf8"), /DevTools announcement/);
  } finally {
    clearTimeout(timer);
    // Launch failures also get reclaimed, but never qualify as the negative.
    if (child.exitCode === null && child.signalCode === null) {
      record({ event: "supervisor-finally-rescue" });
      child.kill("SIGTERM");
      await closed;
    }
    console.log(`startup evidence: ${artifacts}`);
  }
});
