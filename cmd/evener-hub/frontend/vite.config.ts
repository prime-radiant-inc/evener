import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { searchForWorkspaceRoot } from "vite";
import { defineConfig, type Plugin } from "vitest/config";

// The dev server proxies every hub-owned route to a locally running evener-hub.
// Cookies are port-agnostic on localhost, so the /auth?token= capability flow
// works through the proxy unchanged.
const hub = process.env.EVENER_HUB_ADDR ?? "http://127.0.0.1:9180";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// dist/PLACEHOLDER is tracked in git so a fresh checkout's dist/ directory is
// never empty even before the frontend has been built once: cmd/evener-hub
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
    host: "127.0.0.1",
    fs: {
      // Agent worktrees keep cmd/evener-hub/frontend/node_modules as a symlink
      // to one shared install (see docs/developing-evener/conventions/agent-fleets.md), whose
      // realpath sits outside the worktree entirely. global.css's @font-face
      // src ("../../node_modules/...") gets rewritten to that realpath as a
      // /@fs/ request when Vite transforms the CSS, and Vite's fs.allow
      // denies any /@fs/ path outside its allow list - so every font 404s
      // and every browser-guard case fails with "a web font this page
      // requested failed to load" (kata 4s8g). server.fs.allow replaces
      // Vite's computed default rather than extending it, so
      // searchForWorkspaceRoot reproduces that default explicitly; the
      // second entry is the one addition, the shared install's real path.
      // In a normal (non-symlinked) checkout this resolves to the same
      // directory already covered by the first entry, so it's a no-op there.
      allow: [searchForWorkspaceRoot(__dirname), fs.realpathSync(path.join(__dirname, "node_modules"))],
    },
    proxy: {
      // changeOrigin + an explicit Origin header: the hub's same-origin
      // guard (internal/httpguard) only admits its own host as Origin, so
      // the browser's dev-server origin must be rewritten on the proxied
      // WebSocket upgrade or /rpc never connects in dev.
      "/rpc": { target: hub, ws: true, changeOrigin: true, headers: { Origin: hub } },
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
    // Frontend stores, pane registrations, and module mocks are deliberately
    // module-scoped. Keep each file's module registry and jsdom isolated: a
    // worker-count change otherwise changes file-to-worker assignment and can
    // hand a test another file's singleton or mock before its first hook runs.
    // Per-file teardown still matters for timers, clients, and listeners that
    // can outlive that file's environment.
    isolate: true,
    // Vitest 4 moved threads.maxThreads/minThreads to this top-level,
    // single-value option (poolOptions.threads.* is deprecated - a
    // `test.poolOptions` warning fires if used). This ceiling protects direct
    // Vitest use on many-core hosts; the canonical npm test command tightens
    // it to four workers so the root gate retains capacity for its Go streams.
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
    coverage: {
      provider: "v8",
      // Vitest reports only the files a test actually loaded, so a subsystem
      // with no test at all scores as ABSENT rather than as zero - the same
      // false green the Go side guards with its gap map. Naming the whole
      // source tree here puts every file in the denominator, so an untested
      // pane shows up as the 0% it is. (Vitest 4 removed `coverage.all`; an
      // explicit `include` is now the only lever for this.)
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/**/*.test.{ts,tsx}",
        "src/**/*.d.ts",
        // Fixture and harness modules exist to feed tests, and scoring them
        // measures the test rig rather than the app. src/protocol/testing holds
        // the fake client, fake socket and stream harnesses the suites drive.
        "src/protocol/fixtures/**",
        "src/protocol/testing/**",
        // A benchmark is not run by `vitest run`, so counting it only ever
        // reports 0% for code no test was ever meant to execute.
        "src/**/*.bench.ts",
        // The dev harness, gallery, and their standalone entry points back
        // the layout/overflow/spawn guard pages, not shipped runtime - the
        // same carve-out scripts/fuzzcov-ignore.txt grants dev-only tooling.
        "src/dev/**",
      ],
      reportsDirectory: "coverage",
      // json-summary is what scripts/coverage/coverage-floor.sh's web row
      // ratchets against;
      // text-summary keeps the terminal readable; html is for reading a miss.
      reporter: ["text-summary", "json-summary", "html"],
      // Report even on failure so a red suite still yields a coverage number
      // to compare, instead of losing the whole measurement to one bad test.
      reportOnFailure: true,
    },
  },
});
