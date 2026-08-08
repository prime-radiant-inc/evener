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
const SIGNAL_EXIT_CODES = { SIGINT: 130, SIGTERM: 143 };

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

function reportCleanupFailure(error) {
  console.error(error instanceof Error ? error.message : String(error));
}

function listSystemProcesses() {
  return execFileSync("/bin/ps", ["-axo", "pid=,command="], { encoding: "utf8" });
}

function listSystemProcess(pid) {
  try {
    return execFileSync("/bin/ps", ["-p", String(pid), "-o", "pid=,command="], { encoding: "utf8" });
  } catch (error) {
    if (error?.status === 1) return "";
    throw error;
  }
}

function parseProcesses(processList) {
  return processList.split("\n").flatMap((line) => {
    const match = line.match(/^\s*(\d+)\s+(.+)$/);
    if (!match) return [];
    return [{ pid: Number(match[1]), command: match[2] }];
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
  return parseProcesses(listProcesses()).flatMap(({ pid, command }) =>
    commandMatchesProfileProcess(command, databaseArg) ? [{ pid, databaseArg }] : [],
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
  const cdpPort = await findAvailablePort([vitePort]);
  const profileDir = mkdtempSync(path.join(tmpdir(), profilePrefix));
  let vite = null;
  let chrome = null;
  let viteErr = "";
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processTarget,
    scheduleEscalation,
    cancelEscalation,
  });

  try {
    vite = spawnProcess(
      "./node_modules/.bin/vite",
      ["--port", String(vitePort), "--strictPort", "--host", "127.0.0.1", "--clearScreen", "false"],
      { cwd: frontend, stdio: ["ignore", "ignore", "pipe"], detached: useProcessGroups },
    );
    lifecycle.addChild(vite, {
      processGroupId: useProcessGroups && Number.isInteger(vite.pid) ? vite.pid : null,
    });
    vite.stderr?.on("data", (chunk) => {
      viteErr += chunk;
    });
    chrome = spawnProcess(
      resolvedChrome,
      [
        "--headless=new",
        "--disable-gpu",
        ...chromeProfileIsolationArgs(platform),
        `--remote-debugging-port=${cdpPort}`,
        `--user-data-dir=${profileDir}`,
        "--no-first-run",
        "--disable-extensions",
        ...chromeArgs,
        "about:blank",
      ],
      {
        stdio: "ignore",
        env: chromeProfileEnvironment(profileDir),
        detached: useProcessGroups,
      },
    );
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
    cleanup: lifecycle.cleanup,
  };
}
