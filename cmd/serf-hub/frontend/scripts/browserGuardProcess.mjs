import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";

const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

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
}) {
  const resolvedChrome = chromeBinary ?? findChrome();
  const vitePort = await findAvailablePort();
  const cdpPort = await findAvailablePort([vitePort]);
  const profileDir = mkdtempSync(path.join(tmpdir(), profilePrefix));
  let vite = null;
  let chrome = null;
  let viteErr = "";

  const cleanup = () => {
    for (const child of [chrome, vite]) {
      try {
        child?.kill();
      } catch {
        // The child already exited or never started.
      }
    }
    try {
      rmSync(profileDir, { recursive: true, force: true });
    } catch {
      // Best-effort cleanup of the private profile.
    }
  };

  try {
    vite = spawnProcess(
      "./node_modules/.bin/vite",
      ["--port", String(vitePort), "--strictPort", "--host", "127.0.0.1", "--clearScreen", "false"],
      { cwd: frontend, stdio: ["ignore", "ignore", "pipe"] },
    );
    vite.stderr?.on("data", (chunk) => {
      viteErr += chunk;
    });
    chrome = spawnProcess(
      resolvedChrome,
      [
        "--headless=new",
        "--disable-gpu",
        `--remote-debugging-port=${cdpPort}`,
        `--user-data-dir=${profileDir}`,
        "--no-first-run",
        "--disable-extensions",
        ...chromeArgs,
        "about:blank",
      ],
      { stdio: "ignore" },
    );
  } catch (error) {
    cleanup();
    throw error;
  }

  return {
    vitePort,
    cdpPort,
    profileDir,
    getViteError: () => viteErr,
    cleanup,
  };
}
