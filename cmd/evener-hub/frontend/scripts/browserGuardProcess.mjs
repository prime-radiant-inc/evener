import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { stripVTControlCharacters } from "node:util";

import { createStartupDeadline, devtoolsHttpURL, waitForHttp } from "./browserGuardCdp.mjs";

const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

const CHILD_EXIT_GRACE_MS = 2_000;
const SIGNAL_EXIT_CODES = { SIGINT: 130, SIGTERM: 143 };
const DEVTOOLS_ANNOUNCEMENT_PREFIX = "DevTools listening on ";
const VITE_READY_TIMEOUT_MS = 30_000;

/**
 * How long Chrome gets to print "DevTools listening" before the guard calls it
 * an environment failure. Deliberately FAR larger than the 30s an endpoint gets
 * to answer once announced, because the two waits fail for different reasons.
 *
 * Measured on the GitHub runner that failed run 34570447477: Chrome's first
 * stderr byte arrived 21s after launch and it still had not announced at 30s,
 * with its dbus retries running to 27s - a browser making progress, on a cold
 * page cache, on a loaded two-core VM. The four guards after it on the SAME
 * runner came up in seconds. 30s was simply under the cold-start floor.
 *
 * This is a tripwire and nothing else depends on its value: a Chrome that
 * CANNOT start never reaches it, because the exit and spawn-error handlers
 * below reject the readiness promise the moment either fires. What it bounds is
 * the one case where the process is alive and silent, and four times the
 * observed floor is the margin chosen for it.
 */
const CHROME_ANNOUNCEMENT_DEADLINE_MS = 120_000;
const VITE_LOCAL_ANNOUNCEMENT = /Local:\s+http:\/\/(\[[^\]]+\]|[^/:\s]+):(\d+)(?:\/\s*)?$/;

function isLoopbackHost(hostname) {
  if (hostname === "localhost" || hostname === "[::1]") return true;
  const octets = hostname.split(".");
  return (
    octets.length === 4 &&
    octets[0] === "127" &&
    octets.every((octet) => /^(0|[1-9]\d*)$/.test(octet) && Number(octet) <= 255)
  );
}

/** Parse Vite's local URL announcement after it has bound its listening socket. */
export function parseViteReadyAnnouncement(line) {
  const normalized = stripVTControlCharacters(line).trim();
  const match = normalized.match(VITE_LOCAL_ANNOUNCEMENT);
  if (!match) return null;
  const hostname = match[1];
  const port = Number(match[2]);
  if (!isLoopbackHost(hostname) || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`invalid Vite local announcement: ${normalized}`);
  }
  return { host: hostname, port };
}

/** Parse one complete Chrome stderr announcement, or ignore an unrelated line. */
export function parseChromeDevToolsAnnouncement(line) {
  if (!line.startsWith(DEVTOOLS_ANNOUNCEMENT_PREFIX)) return null;
  const raw = line.slice(DEVTOOLS_ANNOUNCEMENT_PREFIX.length);
  if (!raw || /\s/.test(raw)) throw new Error(`malformed DevTools announcement URL: ${raw || "(empty)"}`);
  const authority = raw.match(/^ws:\/\/(\[[^\]]+\]|[^/?#:]+):(\d+)(?:\/|$)/i);
  if (!authority) throw new Error(`malformed DevTools announcement authority: ${raw}`);
  const announcedPort = Number(authority[2]);
  if (!Number.isInteger(announcedPort) || announcedPort < 1 || announcedPort > 65535) {
    throw new Error(`invalid DevTools announcement port: ${raw}`);
  }

  let endpoint;
  try {
    endpoint = new URL(raw);
  } catch {
    throw new Error(`malformed DevTools announcement URL: ${raw}`);
  }
  if (endpoint.protocol !== "ws:" || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
    throw new Error(`invalid DevTools announcement URL: ${raw}`);
  }
  if (!isLoopbackHost(endpoint.hostname)) throw new Error(`DevTools announcement is not loopback: ${raw}`);
  if (!/^\/devtools\/browser\/[^/]+$/.test(endpoint.pathname)) {
    throw new Error(`invalid DevTools announcement path: ${raw}`);
  }
  return { url: raw, host: endpoint.hostname, port: announcedPort };
}

function withAbort(promise, signal, failure = null) {
  if (!signal && !failure) return promise;
  if (signal?.aborted) return Promise.reject(signal.reason ?? new Error("operation aborted"));
  const racers = [promise];
  if (failure) racers.push(new Promise((_, reject) => failure.then(reject)));
  const raced = Promise.race(racers);
  if (!signal) return raced;
  return new Promise((resolve, reject) => {
    const abort = () => reject(signal.reason ?? new Error("operation aborted"));
    signal.addEventListener("abort", abort, { once: true });
    raced.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
  });
}

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
  const effectiveSubsystem = error?.browserGuardSubsystem ?? subsystem;
  const effectiveViteStderr = error?.browserGuardViteStderr ?? viteStderr;
  const lines = [`browser guard startup failed (environment problem, not a test case failure): ${message}`, ""];
  // Name the subsystem that actually failed. One try now covers the launch,
  // Vite and Chrome, and remediation aimed at the wrong one is worse than none:
  // a dead Vite told to "install Chrome" sends the reader looking in the wrong
  // place with an authoritative-looking checklist.
  if (effectiveSubsystem === "vite") {
    lines.push(
      `vite stderr: ${effectiveViteStderr.trim() || "(none)"}`,
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
    `vite stderr: ${effectiveViteStderr.trim() || "(none)"}`,
    "",
    "To fix:",
    `  1. Confirm the binary above exists and runs: "${chromeBinary}" --version`,
    "  2. If it is missing, install Chrome or point at one with chromeBinary.",
    "  3. If it exists but will not start, read the chrome stderr above - a missing",
    "     library, a sandbox denial and an unwritable profile all report there.",
  );
  return lines.join("\n");
}

/**
 * Take a started guard from "processes are running" to "there is a browser to
 * drive", and frame anything that goes wrong as the environment problem it is.
 *
 * Every guard runner had a verbatim copy of this. It is one function because
 * the two waits inside it have to be reasoned about together: the announcement
 * and the endpoint answering are different failures with different causes, so
 * they get SEPARATE budgets. Sharing one, as the copies did, let a cold Chrome
 * that took 25 seconds to announce hand the endpoint wait five - the poll would
 * then die of a deadline that had already been spent by the phase before it.
 */
export async function waitForBrowserReady(guard, { announcementTimeoutMs = CHROME_ANNOUNCEMENT_DEADLINE_MS } = {}) {
  const announcement = createStartupDeadline(announcementTimeoutMs);
  let endpointAnswer = null;
  try {
    let endpoint;
    try {
      endpoint = await guard.waitForChrome({ signal: announcement.signal });
    } catch (error) {
      // Name the phase, and say what the browser had managed to do. Run
      // 34570447477 printed "browser startup deadline exceeded after 30000ms"
      // and nothing else - the same sentence the endpoint poll after it would
      // have printed - and which of the two had stalled had to be argued out
      // of microtask ordering rather than read.
      if (error !== announcement.signal.reason) throw error;
      const firstStderr = guard.getChromeFirstStderrDelay();
      throw new Error(
        `${error.message} while waiting for Chrome's DevTools announcement on stderr ` +
          `(${
            firstStderr === null
              ? "Chrome had written nothing to stderr"
              : `Chrome's first stderr byte arrived ${firstStderr}ms after launch`
          })`,
      );
    }
    endpointAnswer = createStartupDeadline();
    await waitForHttp(
      devtoolsHttpURL(endpoint, "/json/version"),
      "chrome devtools endpoint",
      guard.getChromeLaunchError,
      { signal: endpointAnswer.signal, failure: guard.getChromeFailure() },
    );
    return endpoint;
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
    announcement.clear();
    endpointAnswer?.clear();
  }
}

export function chromeProfileIsolationArgs(platform = process.platform) {
  const args = ["--disable-crash-reporter"];
  if (platform === "darwin") args.push("--use-mock-keychain");
  return args;
}

export function chromeProfileEnvironment(profileDir, environment = process.env) {
  return {
    ...environment,
    // Chrome's process singleton creates its socket directory under the temp
    // directory, so the launcher's long scratch TMPDIR would push the derived
    // SingletonSocket path past sun_path (issue #1141). The profile is already
    // minted under a short root and bounded so this derived path fits; pointing
    // TMPDIR at it keeps every socket Chrome binds short and reaps them with the
    // profile.
    TMPDIR: profileDir,
    BREAKPAD_DUMP_LOCATION: path.join(profileDir, "Crashpad"),
  };
}

// Chrome's process singleton mints a socket directory under the TEMP DIRECTORY
// (base::GetTempDir(), i.e. $TMPDIR) named <branding>.<6 random chars> and binds
// SingletonSocket inside it, and it also puts a SingletonSocket in the profile.
// AF_UNIX's sun_path is 108 bytes including the terminating NUL, so the usable
// path is 107 bytes; a longer one makes Chrome abort with "Socket path too long"
// before DevTools is up (issue #1141). A guard run whose scratch TMPDIR is a
// nested agent-sandbox path overflows that budget. Both halves of the fix live
// here: the profile is minted under a short root chosen independently of the
// ambient TMPDIR, and Chrome is launched with TMPDIR pointed at that short
// profile (chromeProfileEnvironment) so its temp socket directory lands there
// too. createChromeProfileDir bounds the profile so the derived socket path
// always fits.
export const CHROME_SOCKET_PATH_LIMIT = 107;
const CHROME_SINGLETON_SUFFIX = "/com.google.Chrome.XXXXXX/SingletonSocket";
// Node's mkdtemp appends this many random characters to its prefix.
const CHROME_PROFILE_RANDOM_CHARS = 6;

export function chromeSingletonSocketPath(profileDir) {
  return `${profileDir}${CHROME_SINGLETON_SUFFIX}`;
}

// Candidate roots, most preferred first. /private/tmp precedes /tmp so macOS
// resolves to the canonical short root rather than the long /var/folders/...
// path os.tmpdir() returns. The ambient TMPDIR is last: it is only used when no
// short root is usable, and only when the derived socket path still fits.
export function chromeProfileRootCandidates({ platform = process.platform, ambient = tmpdir() } = {}) {
  if (platform === "win32") return [ambient];
  return [...new Set(["/private/tmp", "/tmp", "/var/tmp", ambient])];
}

function maxProfilePrefixBytes(root, socketLimit) {
  return (
    socketLimit -
    Buffer.byteLength(root) -
    // path separator between the root and the profile directory
    1 -
    CHROME_PROFILE_RANDOM_CHARS -
    Buffer.byteLength(CHROME_SINGLETON_SUFFIX)
  );
}

// The base directory a Chrome profile should be minted under, independent of a
// long ambient TMPDIR. Pure path selection: the caller may inject `exists`.
export function chromeProfileRoot({
  platform = process.platform,
  ambient = tmpdir(),
  socketLimit = CHROME_SOCKET_PATH_LIMIT,
  exists = existsSync,
} = {}) {
  const candidates = chromeProfileRootCandidates({ platform, ambient });
  for (const root of candidates) {
    if (platform !== "win32" && maxProfilePrefixBytes(root, socketLimit) <= 0) continue;
    if (platform !== "win32" && !exists(root)) continue;
    return root;
  }
  return ambient;
}

function trimToByteBudget(text, budget) {
  if (Buffer.byteLength(text) <= budget) return text;
  let end = text.length;
  while (end > 0 && Buffer.byteLength(text.slice(0, end)) > budget) end -= 1;
  return text.slice(0, end);
}

// Mint the private profile Chrome is pointed at with --user-data-dir. The
// caller's prefix stays the directory's leading component (callers and tests use
// it to identify the run) but is trimmed when it would push the derived socket
// path past the limit -- the invariant is that the path Chrome binds always
// fits, whatever the ambient TMPDIR. Each candidate root is tried in order so an
// unwritable /tmp still falls through; failure to fit any root is fatal.
export function createChromeProfileDir(
  profilePrefix,
  {
    platform = process.platform,
    ambient = tmpdir(),
    socketLimit = CHROME_SOCKET_PATH_LIMIT,
    makeTempDir = mkdtempSync,
    exists = existsSync,
  } = {},
) {
  const candidates = chromeProfileRootCandidates({ platform, ambient });
  let lastError = null;
  for (const root of candidates) {
    if (platform !== "win32" && !exists(root)) continue;
    const prefix = trimToByteBudget(profilePrefix, maxProfilePrefixBytes(root, socketLimit));
    let dir;
    try {
      // Keep a trailing separator when the prefix was trimmed away so mkdtemp
      // appends its random characters INSIDE the root rather than beside it.
      const template = `${root.replace(/[\\/]+$/, "")}${path.sep}${prefix}`;
      dir = makeTempDir(template);
    } catch (error) {
      lastError = error;
      continue;
    }
    if (platform === "win32" || Buffer.byteLength(chromeSingletonSocketPath(dir)) <= socketLimit) {
      return dir;
    }
    rmSync(dir, { recursive: true, force: true });
    lastError = new Error(
      `profile ${dir} would put Chrome's singleton socket past the ${socketLimit}-byte sun_path limit`,
    );
  }
  throw new Error(
    `could not mint a Chrome profile whose socket path fits ${socketLimit} bytes (tried ${candidates.join(", ")}): ${lastError?.message ?? "no usable root"}`,
  );
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

export function listSystemProcesses({ processCommand = ["/bin/ps"] } = {}) {
  // Stream the complete table through awk and retain only full-argv Crashpad
  // candidates. Capturing the unfiltered table exceeds execFileSync's 1 MiB
  // maxBuffer on a process-heavy host. Filtering the command column, rather
  // than pgrep's comm name, also works on Linux where comm is truncated to 15
  // bytes and would miss chrome_crashpad_handler.
  const filter =
    '{ line=$0; sub(/^[[:space:]]*[0-9]+[[:space:]]+[0-9]+[[:space:]]+/, "", line); ' +
    // Keep the filter narrow, but leave argv0 validation to the JS parser. The
    // second condition rejects an absolute path appearing after an unrelated
    // argv0 while still allowing the spaces in a normal macOS Chrome path.
    "if (line ~ /^.*\\/chrome_crashpad_handler([[:space:]]|$)/ && " +
    "line !~ /^\\/[^[:space:]]+[[:space:]]+\\/.*\\/chrome_crashpad_handler([[:space:]]|$)/) print $0 }";
  if (!Array.isArray(processCommand) || processCommand.length === 0) {
    throw new TypeError("processCommand must contain an executable");
  }
  return execFileSync(
    "/bin/bash",
    [
      "-o",
      "pipefail",
      "-c",
      `"$@" -axo pid=,pgid=,command= | /usr/bin/awk '${filter}'`,
      "browser-guard-ps",
      ...processCommand,
    ],
    { encoding: "utf8" },
  );
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
  // ps exposes argv0 and the remaining argv as one string. Anchor the helper
  // to argv0: a handler-looking positional argument must never own a profile.
  // macOS app/framework paths contain spaces, so do not split on whitespace;
  // instead reject a second absolute path before the helper and require a
  // Chrome/Chromium path when the executable path itself contains spaces.
  const handler = command.match(/^(.*\/)?chrome_crashpad_handler(?=\s|$)/);
  if (!handler || (handler[1] && !command.startsWith("/"))) return false;
  const executablePath = handler[1] ?? "";
  if (/\s+\//.test(executablePath)) return false;
  if (/\s/.test(executablePath) && !/(?:chrome|chromium)/i.test(executablePath)) return false;
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
  { pid, pgid, databaseArg },
  { platform = process.platform, listProcesses = listSystemProcess } = {},
) {
  if (platform !== "darwin" || !Number.isInteger(pid) || !Number.isInteger(pgid) || pgid <= 0) return false;
  return parseProcesses(listProcesses(pid)).some(
    (candidate) =>
      candidate.pid === pid && candidate.pgid === pgid && commandMatchesProfileProcess(candidate.command, databaseArg),
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

export async function requestBrowserClose(endpoint, fetchImpl = fetch, WebSocketImpl = WebSocket) {
  if (!endpoint) throw new Error("Chrome DevTools endpoint was not announced");
  const announced = new URL(endpoint.url);
  const host = endpoint.host ?? announced.hostname;
  const port = endpoint.port ?? Number(announced.port || 80);
  const httpURL = `http://${host}:${port}/json/version`;
  const response = await fetchImpl(httpURL);
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
              `${processIdentity.pid}\0${processIdentity.pgid ?? ""}\0${processIdentity.databaseArg ?? ""}`,
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
  signal = null,
  startupTimeoutMs = VITE_READY_TIMEOUT_MS,
  // A guard that must serve a different Vite config (the editorial preview's
  // fixture-only config) hands it to the wrapper here; the wrapper owns config
  // resolution, so the path is frontend-relative.
  viteConfigFile = null,
}) {
  const resolvedChrome = chromeBinary ?? findChrome();
  const vitePort = 0;
  let actualVitePort = vitePort;
  const profileDir = createChromeProfileDir(profilePrefix);
  let vite = null;
  let chrome = null;
  let viteErr = "";
  let chromeErr = "";
  let viteLaunchError = null;
  let viteReadySettled = false;
  let viteReadySucceeded = false;
  let resolveViteReady;
  let rejectViteReady;
  const viteReady = new Promise((resolve, reject) => {
    resolveViteReady = (address) => {
      if (viteReadySettled) return;
      viteReadySettled = true;
      resolve(address);
    };
    rejectViteReady = (error) => {
      if (viteReadySettled) return;
      viteReadySettled = true;
      reject(error);
    };
  });
  viteReady.catch(() => {});
  let viteLineBuffer = "";
  let chromeLaunchError = null;
  let chromeArgv = [];
  let chromeSpawnedAt = 0;
  let chromeFirstStderrAfterMs = null;
  let chromeEndpoint = null;
  let chromeLineBuffer = "";
  let resolveChromeReady;
  let rejectChromeReady;
  let resolveChromeFailure;
  let chromeFailureSettled = false;
  let chromeReadySettled = false;
  const chromeFailure = new Promise((resolve) => {
    resolveChromeFailure = (error) => {
      if (chromeFailureSettled) return;
      chromeFailureSettled = true;
      resolve(error);
    };
  });
  const chromeReady = new Promise((resolve, reject) => {
    resolveChromeReady = (endpoint) => {
      if (chromeReadySettled) return;
      chromeReadySettled = true;
      resolve(endpoint);
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
  const failChrome = (error) => {
    chromeLaunchError ??= error;
    resolveChromeFailure(error);
    if (!chromeReadySettled) rejectChromeReady(error);
  };
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processTarget,
    scheduleEscalation,
    cancelEscalation,
  });

  try {
    vite = spawnProcess(process.execPath, ["scripts/browserguard-vite.mjs", ...(viteConfigFile ? [viteConfigFile] : [])], {
      cwd: frontend,
      stdio: ["ignore", "pipe", "pipe"],
      detached: useProcessGroups,
    });
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
      rejectViteReady(error);
    });
    vite.once("exit", (code, signal) => {
      if (!viteReadySettled) {
        const error = new Error(`Vite exited before readiness (code ${code ?? "unknown"}, signal ${signal ?? "none"})`);
        viteLaunchError ??= error;
        rejectViteReady(error);
      }
    });
    vite.stdout?.on("data", (chunk) => {
      viteLineBuffer += chunk.toString();
      const lines = viteLineBuffer.split(/\r\n|\r|\n/);
      viteLineBuffer = lines.pop() ?? "";
      for (const line of lines) {
        let address;
        try {
          address = parseViteReadyAnnouncement(line);
        } catch (error) {
          viteLaunchError ??= error;
          rejectViteReady(error);
          continue;
        }
        if (address) resolveViteReady(address);
      }
    });
    vite.stderr?.on("data", (chunk) => {
      viteErr += chunk;
    });
    let viteReadyTimer;
    try {
      const viteAddress = await withAbort(
        Promise.race([
          viteReady,
          new Promise((_, reject) => {
            viteReadyTimer = setTimeout(
              () => reject(new Error(`Vite readiness timed out after ${startupTimeoutMs}ms`)),
              startupTimeoutMs,
            );
          }),
        ]),
        signal,
      );
      actualVitePort = viteAddress.port;
      viteReadySucceeded = true;
    } finally {
      clearTimeout(viteReadyTimer);
    }
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
    chromeSpawnedAt = Date.now();
    chrome = spawnProcess(resolvedChrome, chromeArgv, {
      // Chrome's stderr is the only thing that says WHY it would not start (a
      // missing dylib, a sandbox denial, a profile it cannot write).
      // Discarding it left a failed launch looking like a bare 30s
      // waitForHttp timeout beside an irrelevant Vite log (kata 3htx).
      stdio: ["ignore", "ignore", "pipe"],
      env: chromeProfileEnvironment(profileDir),
      detached: useProcessGroups,
    });
    chrome.on("error", (error) => {
      failChrome(error);
    });
    chrome.stderr?.on("data", (chunk) => {
      chromeFirstStderrAfterMs ??= Date.now() - chromeSpawnedAt;
      chromeErr += chunk;
      chromeLineBuffer += chunk.toString();
      const lines = chromeLineBuffer.split(/\r\n|\r|\n/);
      chromeLineBuffer = lines.pop() ?? "";
      for (const line of lines) {
        let endpoint;
        try {
          endpoint = parseChromeDevToolsAnnouncement(line);
        } catch (error) {
          failChrome(error);
          continue;
        }
        if (!endpoint) continue;
        // Identical duplicate announcements are harmless; a different valid
        // endpoint invalidates startup rather than making listener order decide
        // which browser to measure or close.
        if (chromeEndpoint && chromeEndpoint.url !== endpoint.url) {
          const error = new Error(`conflicting DevTools announcements: ${chromeEndpoint.url} and ${endpoint.url}`);
          failChrome(error);
          continue;
        }
        chromeEndpoint ??= endpoint;
        resolveChromeReady(endpoint);
      }
    });
    chrome.once("exit", (code, signal) => {
      const error = new Error(
        `Chrome exited ${chromeEndpoint ? "after DevTools announcement " : "before DevTools readiness "}` +
          `(code ${code ?? "unknown"}, signal ${signal ?? "none"})`,
      );
      failChrome(error);
    });
    lifecycle.addChild(chrome, {
      processGroupId: useProcessGroups && Number.isInteger(chrome.pid) ? chrome.pid : null,
      gracefulClose: () => closeBrowser(chromeEndpoint),
    });
  } catch (error) {
    await lifecycle.cleanup();
    if (!viteReadySucceeded) {
      const startupError = new Error(error instanceof Error ? error.message : String(error), { cause: error });
      startupError.browserGuardSubsystem = "vite";
      startupError.browserGuardViteStderr = viteErr;
      throw startupError;
    }
    throw error;
  }

  return {
    vitePort: actualVitePort,
    getChromeEndpoint: () => chromeEndpoint,
    profileDir,
    getViteError: () => viteErr,
    getChromeError: () => chromeErr,
    getViteLaunchError: () => viteLaunchError,
    getChromeLaunchError: () => chromeLaunchError,
    getChromeFailure: () => chromeFailure,
    chromeBinary: resolvedChrome,
    getChromeArgv: () => chromeArgv,
    // This promise is the process/devtools readiness handoff. It rejects with
    // the caller's own abort reason; waitForBrowserReady, which arms the
    // announcement budget, is what says which phase that reason belongs to.
    waitForChrome: ({ signal } = {}) => withAbort(chromeReady, signal, chromeFailure),
    // How long after launch Chrome first wrote ANYTHING, or null if it never
    // did. The number that separates a browser which is slow from one which is
    // not running: 21000ms of silence and then dbus retries, in run
    // 34570447477, is a cold page cache, not a broken install.
    getChromeFirstStderrDelay: () => chromeFirstStderrAfterMs,
    cleanup: lifecycle.cleanup,
  };
}
