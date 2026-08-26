import { spawn } from "node:child_process";
import { access, mkdtemp, readFile, rm, watch } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { connectCdp } from "./cdp.mjs";

const defaultChrome =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

async function activePort(profile, child, stderr) {
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
  await Promise.race([immediate, exited]);
  controller.abort();
  const [port, browserPath] = (await readFile(file, "utf8"))
    .trim()
    .split(/\r?\n/);
  if (!port || !browserPath)
    throw new Error(`Invalid DevToolsActivePort in ${profile}`);
  return { port: Number(port), browserPath };
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
    child.kill("SIGTERM");
    throw new Error(
      `${error.message}\nOwned Chrome profile retained: ${profile}`,
    );
  }
  const httpBase = `http://127.0.0.1:${port}`;
  const browser = await connectCdp(`ws://127.0.0.1:${port}${browserPath}`);
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
      const page = await connectCdp(target.webSocketDebuggerUrl);
      return {
        ...page,
        async close() {
          await browser.send("Target.closeTarget", { targetId: target.id });
          await page.close().catch(() => {});
        },
      };
    },
    async close({ retainProfile = false } = {}) {
      if (closed) return;
      closed = true;
      const exit =
        child.exitCode === null
          ? new Promise((resolve) => child.once("exit", resolve))
          : Promise.resolve();
      let shutdownFailed = false;
      try {
        await browser.send("Browser.close");
      } catch {
        child.kill("SIGTERM");
        shutdownFailed = true;
      }
      await exit;
      await browser.close().catch(() => {});
      if (retainProfile || shutdownFailed) {
        console.error(`Owned Chrome profile retained: ${profile}`);
      } else {
        await rm(profile, { recursive: true, force: true });
      }
    },
  };
}
