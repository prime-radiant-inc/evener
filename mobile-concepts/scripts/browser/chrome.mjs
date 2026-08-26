import { spawn } from "node:child_process";
import { access, mkdtemp, readFile, rm, watch } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { connectCdp } from "./cdp.mjs";

const defaultChrome =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

export async function activePort(profile, child, stderr) {
  const file = path.join(profile, "DevToolsActivePort");
  const controller = new AbortController();
  const exited = new Promise((_, reject) =>
    child.once("exit", (code, signal) =>
      reject(
        new Error(
          `Chrome exited before readiness (${code ?? signal})\n${stderr()}`,
        ),
      ),
    ),
  );
  const errored = new Promise((_, reject) =>
    child.once("error", (error) =>
      reject(new Error(`Chrome process error: ${error.message}\n${stderr()}`)),
    ),
  );
  const changed = (async () => {
    try {
      for await (const event of watch(profile, { signal: controller.signal })) {
        if (event.filename === "DevToolsActivePort") break;
      }
    } catch (error) {
      if (error.name !== "AbortError") throw error;
    }
  })();
  const immediate = access(file).then(
    () => undefined,
    () => changed,
  );
  try {
    await Promise.race([immediate, exited, errored]);
  } finally {
    controller.abort();
  }
  const [port, browserPath] = (await readFile(file, "utf8"))
    .trim()
    .split(/\r?\n/);
  if (!port || !browserPath)
    throw new Error(`Invalid DevToolsActivePort in ${profile}`);
  return { port: Number(port), browserPath };
}

function exitEvent(child) {
  return child.exitCode === null && !child.signalCode
    ? new Promise((resolve) =>
        child.once("exit", (code, signal) => resolve({ code, signal })),
      )
    : Promise.resolve({
        code: child.exitCode,
        signal: child.signalCode ?? null,
      });
}

export async function bounded(promise, milliseconds, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`${label} exceeded ${milliseconds}ms`)),
          milliseconds,
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

export async function runOwnedCleanup(primaryError, operations) {
  const errors = primaryError ? [primaryError] : [];
  for (const operation of operations) {
    try {
      await operation();
    } catch (error) {
      if (error instanceof AggregateError) errors.push(...error.errors);
      else errors.push(error);
    }
  }
  if (errors.length > 1)
    throw new AggregateError(errors, "owned operation and cleanup failed");
  if (errors.length === 1) throw errors[0];
}

export async function closePageTarget(browser, page, targetId, primaryError) {
  return runOwnedCleanup(primaryError, [
    () => browser.send("Target.closeTarget", { targetId }),
    () => page.close(),
  ]);
}

export async function withOwnedCleanup(operation, cleanups) {
  let result;
  let primaryError;
  try {
    result = await operation();
  } catch (error) {
    primaryError = error;
  }
  await runOwnedCleanup(primaryError, cleanups);
  return result;
}

async function terminateOwned(child) {
  if (child.exitCode !== null) return false;
  const graceful = exitEvent(child);
  child.kill("SIGTERM");
  try {
    await bounded(graceful, 3_000, "Chrome SIGTERM shutdown");
    return false;
  } catch {
    const forced = exitEvent(child);
    child.kill("SIGKILL");
    await bounded(forced, 3_000, "Chrome SIGKILL shutdown");
    return true;
  }
}

export async function closeBrowserProcess(browser, child, timeout = 3_000) {
  try {
    const exit = exitEvent(child);
    const protocolClose = browser.send("Browser.close").catch(() => undefined);
    const [, observedExit] = await bounded(
      Promise.all([protocolClose, exit]),
      timeout,
      "Chrome protocol Browser.close and process exit",
    );
    return observedExit.code !== 0 || observedExit.signal !== null;
  } catch {
    await terminateOwned(child);
    return true;
  }
}

export async function startChrome(options = {}) {
  const chrome = options.chromeBin ?? process.env.CHROME_BIN ?? defaultChrome;
  await access(chrome);
  const scratch = process.env.EVENER_SCRATCH_DIR ?? os.tmpdir();
  const profile = await mkdtemp(path.join(scratch, "mobile-concepts-chrome-"));
  const child = spawn(
    chrome,
    [
      "--headless=new",
      "--remote-debugging-port=0",
      `--user-data-dir=${profile}`,
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--disable-component-update",
      "--disable-default-apps",
      "--disable-sync",
      "--metrics-recording-only",
      "--mute-audio",
      "about:blank",
    ],
    { stdio: ["ignore", "ignore", "pipe"] },
  );
  let errors = "";
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => {
    errors = `${errors}${chunk}`.slice(-32_768);
  });

  let port;
  let browserPath;
  try {
    ({ port, browserPath } = await activePort(profile, child, () => errors));
  } catch (error) {
    await terminateOwned(child);
    throw new Error(
      `${error.message}\nOwned Chrome profile retained: ${profile}`,
    );
  }
  const httpBase = `http://127.0.0.1:${port}`;
  let browser;
  try {
    browser = await connectCdp(`ws://127.0.0.1:${port}${browserPath}`);
  } catch (error) {
    await terminateOwned(child);
    throw new Error(
      `${error.message}\nOwned Chrome profile retained: ${profile}`,
    );
  }
  let closed = false;

  return {
    profile,
    async newPage() {
      const response = await fetch(`${httpBase}/json/new?about:blank`, {
        method: "PUT",
      });
      if (!response.ok)
        throw new Error(`Chrome target creation failed: ${response.status}`);
      const target = await response.json();
      let page;
      try {
        page = await connectCdp(target.webSocketDebuggerUrl);
      } catch (error) {
        await browser
          .send("Target.closeTarget", { targetId: target.id })
          .catch(() => {});
        throw error;
      }
      return {
        ...page,
        async close() {
          await closePageTarget(browser, page, target.id);
        },
      };
    },
    async close({ retainProfile = false } = {}) {
      if (closed) return;
      closed = true;
      const shutdownFailed = await closeBrowserProcess(browser, child);
      await browser.close().catch(() => {});
      if (retainProfile || shutdownFailed) {
        console.error(`Owned Chrome profile retained: ${profile}`);
      } else {
        await rm(profile, { recursive: true, force: true });
      }
    },
  };
}
