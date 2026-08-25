import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";

const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

const CHILD_EXIT_GRACE_MS = 2_000;
const CHROME_READINESS_TIMEOUT_MS = 30_000;
const SIGNAL_EXIT_CODES = { SIGINT: 130, SIGTERM: 143 };

/**
 * Format the diagnostic a browser guard prints when the stack will not come up.
 *
 * This is the surviving half of layoutguard/cdp.mjs's retired
 * probeBrowserCapability (kata 3htx). The claim when that went was that its
 * pinned intent lived on in run.mjs's startup error; what lived on was the
 * phrase "environment problem, not a test case failure" and VITE's stderr.
 * Chrome's binary path, its argv, its own stderr and the remediation steps did
 * not, so a Chrome that would not start surfaced as a 30-second waitForHttp
 * timeout next to a Vite log that had nothing to do with it.
 *
 * Pure and exported so it can be tested without launching anything, which is
 * the property the deleted probe's test had and its replacement did not.
 */
export function describeBrowserStartupFailure({
  error,
  subsystem = "chrome",
  chromeBinary,
  chromeArgv = [],
  chromeStderr = "",
  viteStderr = "",
}) {
  const message = error instanceof Error ? error.message : String(error);
  const lines = [
    `browser guard startup failed (environment problem, not a test case failure): ${message}`,
    "",
  ];
  // Name the subsystem that actually failed. One try now covers the launch,
  // Vite and Chrome, and remediation aimed at the wrong one is worse than none:
  // a dead Vite told to "install Chrome" sends the reader looking in the wrong
  // place with an authoritative-looking checklist.
  if (subsystem === "vite") {
    lines.push(
      `vite stderr: ${viteStderr.trim() || "(none)"}`,
      "",
      "To fix:",
      "  1. Read the vite stderr above - a port clash, a failed transform and a",
      "     missing dependency all report there.",
      "  2. Confirm the frontend installs cleanly: npm install",
      "  3. Chrome is not implicated; it had not been reached yet.",
    );
    return lines.join("\n");
  }
  if (subsystem === "launch") {
    lines.push(
      "To fix:",
      "  1. The browser binary could not be resolved at all - no candidate path",
      "     existed, so nothing was spawned and there is no stderr to read.",
      "  2. Install Chrome or Chromium at one of the candidate paths named above.",
      "  3. Neither Vite nor the test cases were reached.",
    );
    return lines.join("\n");
  }
  lines.push(
    `Chrome binary: ${chromeBinary || "(none found)"}`,
    `Chrome argv: ${chromeArgv.join(" ")}`,
    `chrome stderr: ${chromeStderr.trim() || "(none)"}`,
    `vite stderr: ${viteStderr.trim() || "(none)"}`,
    "",
    "To fix:",
    `  1. Confirm the binary above exists and runs: "${chromeBinary}" --version`,
    "  2. If it is missing, install Chrome or point at one with chromeBinary.",
    "  3. If it exists but will not start, read the chrome stderr above - a missing",
    "     library, a sandbox denial and an unwritable profile all report there.",
  );
  return lines.join("\n");
}

export function chromeProfileIsolationArgs(platform = process.platform) {
  const args = ["--disable-crash-reporter"];
  if (platform === "darwin") args.push("--use-mock-keychain");
  return args;
}

export function chromeProfileEnvironment(profileDir, environment = process.env) {
  return {
    ...environment,
    BREAKPAD_DUMP_LOCATION: path.join(profileDir, "Crashpad"),
  };
}

function childHasExited(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

function processGroupRunning(processGroupId) {
  try {
    process.kill(-processGroupId, 0);
    return true;
  } catch (error) {
    if (error?.code === "ESRCH") return false;
    if (error?.code === "EPERM") return true;
    throw error;
  }
}

function signalProcessGroup(processGroupId, signal) {
  try {
    process.kill(-processGroupId, signal);
    return true;
  } catch (error) {
    if (error?.code === "ESRCH") return false;
    throw error;
  }
}

function signalProfileProcess(processIdentity, signal) {
  // Measured on real macOS Chrome (issue #119): Crashpad DOUBLE-SPAWNS its
  // handler. An intermediate process becomes a new group leader, spawns
  // chrome_crashpad_handler into that group, and exits — so the handler's
  // pgid is a dead intermediate's pid, never the handler's own pid.
  // kill(-pid) is therefore always ESRCH, and a direct pid signal
  // intermittently gets EPERM once the handler is orphaned to launchd. The
  // only signal that reliably lands is the group id captured at discovery:
  // POSIX keeps a live group's id reserved while the group exists, so
  // kill(-pgid) can only reach the handler's own group.
  const { pgid } = processIdentity;
  if (!Number.isInteger(pgid) || pgid <= 0) {
    // Live-fire guard: a fabricated identity (a test injecting
    // findProfileProcesses without injecting signalProfileProcess) has no
    // discovered pgid. Refuse loudly instead of SIGKILLing whatever real
    // process group happens to own a made-up id.
    throw new Error(
      `refusing to signal profile process ${processIdentity.pid} without a pgid captured at discovery; ` +
        "identities must come from findMacOSProfileProcesses (tests must inject signalProfileProcess)",
    );
  }
  try {
    process.kill(-pgid, signal);
    return true;
  } catch (error) {
    if (error?.code === "ESRCH") return false;
    throw error;
  }
}

function killProfileProcess(processIdentity, signalProfile, isProfileProcessRunning) {
  // Never signal a possibly-reused pid: re-verify the identity (pid + argv,
  // via ps) immediately before every signal. Between capture and signal —
  // seconds, across the graceful-close/TERM/KILL teardown — the helper may
  // exit and the pid be reused by an unrelated process.
  if (!isProfileProcessRunning(processIdentity)) return;
  try {
    signalProfile(processIdentity, "SIGKILL");
  } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}

function reportCleanupFailure(error) {
  console.error(error instanceof Error ? error.message : String(error));
}

function listSystemProcesses() {
  return execFileSync("/bin/ps", ["-axo", "pid=,pgid=,command="], { encoding: "utf8" });
}

function listSystemProcess(pid) {
  try {
    return execFileSync("/bin/ps", ["-p", String(pid), "-o", "pid=,pgid=,command="], { encoding: "utf8" });
  } catch (error) {
    if (error?.status === 1) return "";
    throw error;
  }
}

function parseProcesses(processList) {
  return processList.split("\n").flatMap((line) => {
    const match = line.match(/^\s*(\d+)\s+(\d+)\s+(.+)$/);
    if (!match) return [];
    return [{ pid: Number(match[1]), pgid: Number(match[2]), command: match[3] }];
  });
}

function commandMatchesProfileProcess(command, databaseArg) {
  if (!/(?:^|\/)chrome_crashpad_handler(?:\s|$)/.test(command)) return false;
  return command.includes(`${databaseArg} `) || command.endsWith(databaseArg);
}

export function findMacOSProfileProcesses(
  profileDir,
  { platform = process.platform, listProcesses = listSystemProcesses } = {},
) {
  if (platform !== "darwin") return [];
  const databaseArg = `--database=${path.join(realpathSync(profileDir), "Crashpad")}`;
  // The pgid captured here is what signalProfileProcess targets: the handler's
  // group leader is a dead intermediate (see signalProfileProcess), so the
  // group id is only discoverable from ps, never derivable from the pid.
  return parseProcesses(listProcesses()).flatMap(({ pid, pgid, command }) =>
    commandMatchesProfileProcess(command, databaseArg) ? [{ pid, pgid, databaseArg }] : [],
  );
}

export function profileProcessIdentityRunning(
  { pid, databaseArg },
  { platform = process.platform, listProcesses = listSystemProcess } = {},
) {
  if (platform !== "darwin") return false;
  return parseProcesses(listProcesses(pid)).some(
    (candidate) => candidate.pid === pid && commandMatchesProfileProcess(candidate.command, databaseArg),
  );
}

function waitForProcessTargetExit({
  targetRunning,
  signalTarget,
  subscribeToExit = null,
  unsubscribeFromExit = null,
  pollTarget,
  gracefulClose,
  scheduleEscalation,
  cancelEscalation,
  scheduleCheck,
  cancelCheck,
}) {
  if (!targetRunning()) return Promise.resolve();

  return new Promise((resolve, reject) => {
    let gracefulDeadline = null;
    let processCheck = null;
    let killEscalation = null;
    let settled = false;
    let termStarted = false;
    const cancelScheduled = () => {
      if (gracefulDeadline !== null) cancelEscalation(gracefulDeadline);
      if (killEscalation !== null) cancelEscalation(killEscalation);
      if (processCheck !== null) cancelCheck(processCheck);
    };
    const targetExitListener = () => finish();
    const finish = () => {
      if (settled) return;
      settled = true;
      cancelScheduled();
      unsubscribeFromExit?.(targetExitListener);
      resolve();
    };
    const fail = (error) => {
      if (settled) return;
      settled = true;
      cancelScheduled();
      unsubscribeFromExit?.(targetExitListener);
      reject(error);
    };
    const checkProcess = () => {
      processCheck = null;
      try {
        if (!targetRunning()) {
          finish();
          return;
        }
        processCheck = scheduleCheck(checkProcess);
      } catch (error) {
        fail(error);
      }
    };
    const signalTerm = () => {
      if (settled || termStarted) return;
      termStarted = true;
      if (gracefulDeadline !== null) cancelEscalation(gracefulDeadline);
      killEscalation = scheduleEscalation(() => {
        try {
          if (targetRunning() && signalTarget("SIGKILL") === false) finish();
        } catch (error) {
          if (error?.code === "ESRCH") finish();
          else fail(error);
        }
      }, CHILD_EXIT_GRACE_MS);

      try {
        if (signalTarget("SIGTERM") === false) finish();
      } catch (error) {
        if (error?.code === "ESRCH") finish();
        else fail(error);
      }
    };

    subscribeToExit?.(targetExitListener);
    if (pollTarget) processCheck = scheduleCheck(checkProcess);
    if (!gracefulClose) {
      signalTerm();
      return;
    }

    gracefulDeadline = scheduleEscalation(signalTerm, CHILD_EXIT_GRACE_MS);
    void (async () => {
      try {
        await gracefulClose();
      } catch {
        signalTerm();
        return;
      }
      try {
        if (!targetRunning()) finish();
      } catch (error) {
        fail(error);
      }
    })();
  });
}

function waitForChildExit(
  child,
  processGroupId,
  gracefulClose,
  scheduleEscalation,
  cancelEscalation,
  isProcessGroupRunning,
  signalGroup,
  scheduleGroupCheck,
  cancelGroupCheck,
) {
  if (!child) return Promise.resolve();
  return waitForProcessTargetExit({
    targetRunning: () => (processGroupId === null ? !childHasExited(child) : isProcessGroupRunning(processGroupId)),
    signalTarget: (signal) => (processGroupId === null ? child.kill(signal) : signalGroup(processGroupId, signal)),
    subscribeToExit: processGroupId === null ? (listener) => child.once("exit", listener) : null,
    unsubscribeFromExit: processGroupId === null ? (listener) => child.removeListener("exit", listener) : null,
    pollTarget: processGroupId !== null,
    gracefulClose,
    scheduleEscalation,
    cancelEscalation,
    scheduleCheck: scheduleGroupCheck,
    cancelCheck: cancelGroupCheck,
  });
}

function waitForProfileProcessExit(
  processIdentity,
  isProcessRunning,
  profileDir,
  scheduleDeadline,
  cancelDeadline,
  scheduleProcessCheck,
  cancelProcessCheck,
) {
  if (!isProcessRunning(processIdentity)) return Promise.resolve();

  return new Promise((resolve, reject) => {
    let deadline = null;
    let processCheck = null;
    let settled = false;
    const cancelScheduled = () => {
      if (deadline !== null) cancelDeadline(deadline);
      if (processCheck !== null) cancelProcessCheck(processCheck);
    };
    const finish = () => {
      if (settled) return;
      settled = true;
      cancelScheduled();
      resolve();
    };
    const fail = (error) => {
      if (settled) return;
      settled = true;
      cancelScheduled();
      reject(error);
    };
    const checkProcess = () => {
      processCheck = null;
      try {
        if (!isProcessRunning(processIdentity)) {
          finish();
          return;
        }
        processCheck = scheduleProcessCheck(checkProcess);
      } catch (error) {
        fail(error);
      }
    };
    deadline = scheduleDeadline(() => {
      try {
        if (!isProcessRunning(processIdentity)) {
          finish();
          return;
        }
        fail(
          new Error(
            `escaped Chrome helper ${processIdentity.pid} did not exit; private profile retained at ${profileDir}`,
          ),
        );
      } catch (error) {
        fail(error);
      }
    }, CHILD_EXIT_GRACE_MS);
    processCheck = scheduleProcessCheck(checkProcess);
  });
}

export async function requestBrowserClose(port, fetchImpl = fetch, WebSocketImpl = WebSocket) {
  const response = await fetchImpl(`http://127.0.0.1:${port}/json/version`);
  if (!response.ok) throw new Error(`Chrome CDP endpoint returned ${response.status}`);
  const version = await response.json();
  if (!version.webSocketDebuggerUrl) throw new Error("Chrome CDP endpoint omitted its browser WebSocket");

  const socket = new WebSocketImpl(version.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    let settled = false;
    const finish = (action, value) => {
      if (settled) return;
      settled = true;
      action(value);
    };
    socket.addEventListener("open", () => socket.send(JSON.stringify({ id: 1, method: "Browser.close" })), {
      once: true,
    });
    socket.addEventListener("close", () => finish(resolve), { once: true });
    socket.addEventListener("error", (error) => finish(reject, error), { once: true });
  });
}

export function createBrowserProcessCleanup({
  profileDir,
  processTarget = process,
  scheduleEscalation = setTimeout,
  cancelEscalation = clearTimeout,
  processGroupRunning: isProcessGroupRunning = processGroupRunning,
  signalProcessGroup: signalGroup = signalProcessGroup,
  scheduleGroupCheck = setImmediate,
  cancelGroupCheck = clearImmediate,
  reportCleanupFailure: reportFailure = reportCleanupFailure,
  findProfileProcesses = findMacOSProfileProcesses,
  profileProcessRunning: isProfileProcessRunning = profileProcessIdentityRunning,
  scheduleProfileProcessCheck = setImmediate,
  cancelProfileProcessCheck = clearImmediate,
  signalProfileProcess: signalProfile = signalProfileProcess,
}) {
  const children = [];
  let cleanupPromise = null;
  let interruptSignal = null;

  const removeSignalHandlers = () => {
    processTarget.removeListener("SIGINT", handleSIGINT);
    processTarget.removeListener("SIGTERM", handleSIGTERM);
  };
  const finishInterruptedProcess = (signal) => {
    const exitCode = SIGNAL_EXIT_CODES[signal];
    if (typeof processTarget.exit === "function") processTarget.exit(exitCode);
    else processTarget.exitCode = exitCode;
  };
  const handleSignal = (signal) => {
    if (interruptSignal !== null) return;
    interruptSignal = signal;
    cleanup().then(
      () => finishInterruptedProcess(signal),
      (error) => {
        try {
          reportFailure(error);
        } finally {
          finishInterruptedProcess(signal);
        }
      },
    );
  };
  const handleSIGINT = () => handleSignal("SIGINT");
  const handleSIGTERM = () => handleSignal("SIGTERM");
  const cleanup = () => {
    if (cleanupPromise !== null) return cleanupPromise;
    cleanupPromise = (async () => {
      try {
        const profileProcessesBeforeClose = findProfileProcesses(profileDir);
        // Crashpad's handler lives in a process group of its own — one led by
        // a dead intermediate, never by Chrome and never by the handler itself
        // (see signalProfileProcess) — so the group teardown
        // `process.kill(-chromePgid)` below never reaches it. SIGKILL each
        // discovered helper's captured group NOW: without any signal the
        // helper is only polled then rejected, escaping cleanup ~1 run in 3
        // (issue #119).
        for (const processIdentity of profileProcessesBeforeClose) {
          killProfileProcess(processIdentity, signalProfile, isProfileProcessRunning);
        }
        await Promise.all(
          children.map(({ child, processGroupId, gracefulClose }) =>
            waitForChildExit(
              child,
              processGroupId,
              gracefulClose,
              scheduleEscalation,
              cancelEscalation,
              isProcessGroupRunning,
              signalGroup,
              scheduleGroupCheck,
              cancelGroupCheck,
            ),
          ),
        );
        const profileProcesses = [
          ...new Map(
            [...profileProcessesBeforeClose, ...findProfileProcesses(profileDir)].map((processIdentity) => [
              `${processIdentity.pid}\0${processIdentity.databaseArg ?? ""}`,
              processIdentity,
            ]),
          ).values(),
        ];
        // Reap any helpers that escaped DURING the group close (the late-helper
        // path) and confirm the pre-close helpers are gone. The teardown above
        // can take seconds, so killProfileProcess's identity re-check is what
        // keeps this pass from signaling a pid captured before the close that
        // has since exited and been reused.
        for (const processIdentity of profileProcesses) {
          killProfileProcess(processIdentity, signalProfile, isProfileProcessRunning);
        }
        await Promise.all(
          profileProcesses.map((processIdentity) =>
            waitForProfileProcessExit(
              processIdentity,
              isProfileProcessRunning,
              profileDir,
              scheduleEscalation,
              cancelEscalation,
              scheduleProfileProcessCheck,
              cancelProfileProcessCheck,
            ),
          ),
        );
        rmSync(profileDir, { recursive: true, force: true });
      } finally {
        removeSignalHandlers();
      }
    })();
    return cleanupPromise;
  };

  processTarget.on("SIGINT", handleSIGINT);
  processTarget.on("SIGTERM", handleSIGTERM);

  return {
    addChild(child, { processGroupId = null, gracefulClose = null } = {}) {
      children.push({ child, processGroupId, gracefulClose });
    },
    cleanup,
  };
}

export function findChrome() {
  for (const candidate of CHROME_CANDIDATES) {
    if (existsSync(candidate)) return candidate;
  }
  throw new Error(`no Chrome/Chromium found (looked at: ${CHROME_CANDIDATES.join(", ")})`);
}

export function findAvailablePort(excludedPorts = []) {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        server.close();
        reject(new Error("local port allocation returned no TCP address"));
        return;
      }
      server.close((error) => {
        if (error) {
          reject(error);
        } else if (excludedPorts.includes(address.port)) {
          findAvailablePort(excludedPorts).then(resolve, reject);
        } else {
          resolve(address.port);
        }
      });
    });
  });
}

export async function startBrowserGuard({
  frontend,
  profilePrefix,
  chromeArgs = [],
  spawnProcess = spawn,
  chromeBinary = null,
  closeBrowser = requestBrowserClose,
  platform = process.platform,
  useProcessGroups = platform !== "win32",
  processTarget = process,
  scheduleEscalation = setTimeout,
  cancelEscalation = clearTimeout,
}) {
  const resolvedChrome = chromeBinary ?? findChrome();
  const vitePort = await findAvailablePort();
  const profileDir = mkdtempSync(path.join(tmpdir(), profilePrefix));
  let vite = null;
  let chrome = null;
  let viteErr = "";
  let chromeErr = "";
  let viteLaunchError = null;
  let chromeLaunchError = null;
  let chromeArgv = [];
  let cdpPort = 0;
  let resolveChromeReady;
  let rejectChromeReady;
  let chromeReadySettled = false;
  const chromeReady = new Promise((resolve, reject) => {
    resolveChromeReady = (port) => {
      if (chromeReadySettled) return;
      chromeReadySettled = true;
      cdpPort = port;
      resolve(port);
    };
    rejectChromeReady = (error) => {
      if (chromeReadySettled) return;
      chromeReadySettled = true;
      reject(error);
    };
  });
  // A guard normally awaits this promise, but attach a rejection handler here
  // too so a child that exits while startup is being abandoned cannot become an
  // unhandled rejection during cleanup.
  chromeReady.catch(() => {});
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processTarget,
    scheduleEscalation,
    cancelEscalation,
  });

  try {
    vite = spawnProcess(
      "./node_modules/.bin/vite",
      [
        "--config",
        "scripts/browserguard.vite.config.mjs",
        "--port",
        String(vitePort),
        "--strictPort",
        "--host",
        "127.0.0.1",
        "--clearScreen",
        "false",
      ],
      { cwd: frontend, stdio: ["ignore", "ignore", "pipe"], detached: useProcessGroups },
    );
    lifecycle.addChild(vite, {
      processGroupId: useProcessGroups && Number.isInteger(vite.pid) ? vite.pid : null,
    });
    // A spawn() that never launches reports ASYNCHRONOUSLY on the child's
    // "error" event (EACCES on a binary that is not executable, ENOENT on one
    // that vanished between findChrome() and here) and returns a ChildProcess
    // regardless, so the try around these spawns cannot see it. Unhandled, an
    // "error" event on an EventEmitter throws - out past
    // describeBrowserStartupFailure and through lifecycle.cleanup(), which is
    // what reaps the Vite server and the private profile, so the guard died
    // noisily and left a dev server behind (kata ssca). Recorded per subsystem
    // so the readiness wait aborts naming the launch error instead of timing
    // out for 30 seconds against something that was never running, and so a
    // dead Chrome is never blamed on a healthy Vite.
    vite.on("error", (error) => {
      viteLaunchError ??= error;
    });
    vite.stderr?.on("data", (chunk) => {
      viteErr += chunk;
    });
    chromeArgv = [
      "--headless=new",
      "--disable-gpu",
      ...chromeProfileIsolationArgs(platform),
      // Let Chrome bind the port itself and report the bound endpoint on stderr.
      // Picking a free port, closing it, then asking Chrome to reuse it is a
      // TOCTOU race when several guards start together.
      "--remote-debugging-port=0",
      `--user-data-dir=${profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      ...chromeArgs,
      "about:blank",
    ];
    chrome = spawnProcess(
      resolvedChrome,
      chromeArgv,
      {
        // Chrome's stderr is the only thing that says WHY it would not start (a
        // missing dylib, a sandbox denial, a profile it cannot write).
        // Discarding it left a failed launch looking like a bare 30s
        // waitForHttp timeout beside an irrelevant Vite log (kata 3htx).
        stdio: ["ignore", "ignore", "pipe"],
        env: chromeProfileEnvironment(profileDir),
        detached: useProcessGroups,
      },
    );
    chrome.on("error", (error) => {
      chromeLaunchError ??= error;
      rejectChromeReady(error);
    });
    chrome.stderr?.on("data", (chunk) => {
      chromeErr += chunk;
      const match = chromeErr.match(/DevTools listening on ws:\/\/(?:127\.0\.0\.1|localhost|\[::1\]):(\d+)\//);
      if (match) resolveChromeReady(Number(match[1]));
    });
    chrome.once("exit", (code, signal) => {
      if (chromeReadySettled) return;
      const error = new Error(
        `Chrome exited before DevTools readiness (code ${code ?? "unknown"}, signal ${signal ?? "none"})`,
      );
      chromeLaunchError ??= error;
      rejectChromeReady(error);
    });
    lifecycle.addChild(chrome, {
      processGroupId: useProcessGroups && Number.isInteger(chrome.pid) ? chrome.pid : null,
      gracefulClose: () => closeBrowser(cdpPort),
    });
  } catch (error) {
    await lifecycle.cleanup();
    throw error;
  }

  return {
    vitePort,
    cdpPort,
    profileDir,
    getViteError: () => viteErr,
    getChromeError: () => chromeErr,
    getViteLaunchError: () => viteLaunchError,
    getChromeLaunchError: () => chromeLaunchError,
    chromeBinary: resolvedChrome,
    getChromeArgv: () => chromeArgv,
    // This promise is the process/devtools readiness handoff. The caller must
    // use its returned port rather than probing a port selected before Chrome
    // started, which could be claimed by another concurrent guard.
    waitForChrome: () => {
      let timer;
      return Promise.race([
        chromeReady,
        new Promise((_, reject) => {
          timer = setTimeout(
            () => reject(new Error("Chrome DevTools readiness line never appeared")),
            CHROME_READINESS_TIMEOUT_MS,
          );
        }),
      ]).finally(() => clearTimeout(timer));
    },
    cleanup: lifecycle.cleanup,
  };
}
