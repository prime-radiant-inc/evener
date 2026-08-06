import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";
import react from "@vitejs/plugin-react";

// The dev server proxies every hub-owned route to a locally running serf-hub.
// Cookies are port-agnostic on localhost, so the /auth?token= capability flow
// works through the proxy unchanged.
const hub = process.env.SERF_HUB_ADDR ?? "http://127.0.0.1:9180";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// dist/PLACEHOLDER is tracked in git so a fresh checkout's dist/ directory is
// never empty even before the frontend has been built once: cmd/serf-hub
// embeds it via `//go:embed all:frontend/dist`, which fails outright against
// an empty directory. build.emptyOutDir wipes dist/ - including
// PLACEHOLDER - before every real build, which then shows up in `git status`
// as a tracked file deleted. Kata 88nn: three agents saw that diff and each
// reached for `git checkout -- <file>` to undo it, a command this fleet
// otherwise forbids for good reason. Restoring the file HERE, as part of the
// build itself, means every invocation path (`make build-web`, `npm run
// build`, or `vite build` run directly during frontend dev) leaves the
// tracked file's content byte-identical afterward instead of missing.
function restoreDistPlaceholder(): Plugin {
  return {
    name: "restore-dist-placeholder",
    closeBundle() {
      const outDir = path.join(__dirname, "dist");
      fs.mkdirSync(outDir, { recursive: true });
      fs.writeFileSync(path.join(outDir, "PLACEHOLDER"), "run make build-web\n");
    },
  };
}

export default defineConfig({
  plugins: [react(), restoreDistPlaceholder()],
  build: { assetsDir: "webassets", outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/rpc": { target: hub, ws: true },
      "/api": hub,
      "/auth": hub,
      "/doc": hub,
      "/s": { target: hub, bypass: (req) => (req.url?.includes("/images/") ? undefined : req.url) },
    },
  },
  test: {
    environment: "jsdom",
    // Node 26's experimental Web Storage global shadows jsdom's working
    // localStorage unless it is disabled in each Vitest worker.
    execArgv: ["--no-experimental-webstorage"],
    pool: "threads",
    // Cross-file state leaks (stale pane-registry stubs, unclosed real
    // clients, leaked mock replacements) were root-caused and fixed, so the
    // suite is safe to run with a persistent module registry + jsdom per
    // worker instead of fresh ones per file - measured ~28% less wall time
    // on a sample. Explicit thread count matches this machine's logical
    // CPUs; leaving it unset defaults to half of them.
    isolate: false,
    // Vitest 4 moved threads.maxThreads/minThreads to this top-level,
    // single-value option (poolOptions.threads.* is deprecated - a
    // `test.poolOptions` warning fires if used). Derived from the runtime's
    // own logical CPU count rather than one developer machine's number
    // (10) hardcoded - a shared host with fewer processors would otherwise
    // be oversubscribed, especially since `make test-web` runs Vitest
    // alongside typechecking and linting. Capped so a many-core host still
    // leaves headroom for those siblings instead of spawning one worker per
    // core; the cap sits above this measured machine's 10 so its own
    // ~28% wall-time improvement (isolate:false comment above) is preserved.
    maxWorkers: Math.max(1, Math.min(os.availableParallelism(), 12)),
    setupFiles: [],
    // A handful of shell suites must import the real pane modules from inside
    // beforeAll rather than statically: those modules transitively pull in
    // stores/prefs, whose createStore initializer reads localStorage at module
    // scope, so the in-memory storage stub has to be installed first. That
    // puts Vite's transform of the whole dockview/session/welcome graph
    // (measured at ~4.1s idle) inside a hook, where the default 10s ceiling is
    // a coin flip on a loaded machine - it fired repeatedly with a dozen
    // concurrent vitest processes. The work is real and awaitable, so the
    // ceiling is the wrong lever to leave at its default; it stays a tripwire
    // for a genuine hang, just one sized for the transform it has to cover.
    hookTimeout: 60_000,
  },
});
