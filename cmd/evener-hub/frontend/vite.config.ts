import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { searchForWorkspaceRoot } from "vite";
import { defineConfig, type Plugin } from "vitest/config";

// The dev server proxies every hub-owned route to a locally running evener-hub.
// Cookies are port-agnostic on localhost, so the /auth/<token> capability flow
// works through the proxy unchanged.
const hub = process.env.EVENER_HUB_ADDR ?? "http://127.0.0.1:9180";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// The AppWire TypeScript package lives at the repo root (appwire-client/typescript),
// outside this app's source tree. Every resolver has to be told about it
// separately - tsconfig paths for tsc, the alias below for Vite/Vitest, and
// server.fs.allow for the dev server and the browser guards, which serve it
// over /@fs/ and 403 anything outside the allow list.
const appwirePackageDir = path.join(__dirname, "..", "..", "..", "appwire-client", "typescript");

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
  // Keep the dep-optimizer cache inside this checkout. Vite defaults it to
  // node_modules/.vite, and fleet worktrees symlink node_modules to ONE shared
  // install (docs/developing-evener/conventions/agent-fleets.md), so that
  // default's dep-cache temp dir is shared by every lane and every concurrent
  // Vite process - which races (issue #1586). path.join(__dirname, ...) is
  // lexical and never follows the symlink. make test-web starts only vitest's
  // Vite, and make test-web-browser, which runs guards side by side, gives
  // each guard its own cache (browserguard.vite.config.mjs).
  cacheDir: path.join(__dirname, ".vite-cache"),
  build: { assetsDir: "webassets", outDir: "dist", emptyOutDir: true },
  // These mirror tsconfig.json's paths - tsconfig paths are invisible to Vite,
  // so the alias is what the bundler, the dev server, Vitest and the five
  // browser guards all resolve through. The targets are the package's
  // TypeScript sources, never dist/: dist/ is gitignored and never built in a
  // dev or test flow. An alias key also matches `<key>/<subpath>`, so the
  // specific entries have to come first or the root entry swallows them.
  resolve: {
    alias: {
      "@evener/appwire-client/docContent": path.join(appwirePackageDir, "docContent.ts"),
      "@evener/appwire-client/state/navigation": path.join(appwirePackageDir, "state", "navigation", "index.ts"),
      "@evener/appwire-client/state/credentials": path.join(appwirePackageDir, "state", "credentials", "index.ts"),
      "@evener/appwire-client/state/extensions": path.join(appwirePackageDir, "state", "extensions", "index.ts"),
      "@evener/appwire-client/state/mutation": path.join(appwirePackageDir, "state", "mutation", "index.ts"),
      "@evener/appwire-client/state/connection": path.join(appwirePackageDir, "state", "connection", "index.ts"),
      "@evener/appwire-client/testing": path.join(appwirePackageDir, "testing"),
      "@evener/appwire-client": path.join(appwirePackageDir, "index.ts"),
      // Resolution runs from the importer, and the package's test files sit
      // above a repo root with no node_modules, so their bare imports find
      // nothing. tsconfig.json's paths name the same dependencies for the same
      // reason. Only the ones those files actually import are listed: a new
      // one fails loudly here rather than resolving to a second copy.
      "@testing-library/react": path.join(__dirname, "node_modules", "@testing-library", "react"),
      react: path.join(__dirname, "node_modules", "react"),
      // The package has its own node_modules once `npm ci --prefix
      // appwire-client/typescript` has run for `make test-api-package`, and
      // typescript is one of its devDependencies - so locally this alias looks
      // redundant and CI's web job, which installs only this app, fails
      // without it. scripts/package-test-files.mjs now holds every bare
      // specifier in the package's test graph against the keys of this block.
      typescript: path.join(__dirname, "node_modules", "typescript"),
      // The keybinding suites inject tinykeys' real parseKeybinding through
      // the package's KeybindingParser port; the package itself never names
      // tinykeys, so this alias serves only its tests.
      tinykeys: path.join(__dirname, "node_modules", "tinykeys"),
    },
  },
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
      // The hub's recorded wire fixtures (cmd/evener-hub/testdata) are the
      // last entry: the package's testing/hubWireFixtures.ts loads
      // authwire/responses.json through a `?raw` import, and Vitest's jsdom
      // suites transform that import through this server, which denies any
      // file outside the allow list.
      allow: [
        searchForWorkspaceRoot(__dirname),
        fs.realpathSync(path.join(__dirname, "node_modules")),
        appwirePackageDir,
        path.join(__dirname, "..", "testdata"),
      ],
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
    // Vitest collects relative to its root - this config's directory - so the
    // package's own test files, now at appwire-client/typescript/, fall
    // outside the default glob and would silently stop running. The first
    // entry restates that default; the second is the addition. They run here,
    // not under the package's own gate, because they import `vitest`, `react`
    // and `@testing-library/react` bare and load their .jsonl fixtures through
    // Vite's `?raw`: this app's install already provides all of that, while
    // giving the package its own dev dependencies would change what
    // `npm ci --prefix appwire-client/typescript` fetches for the
    // qualification gate, which needs only typescript, tinykeys and ws.
    include: [
      "**/*.{test,spec}.?(c|m)[jt]s?(x)",
      "../../../appwire-client/typescript/**/*.{test,spec}.?(c|m)[jt]s?(x)",
    ],
    // Node 26's experimental Web Storage global shadows jsdom's working
    // localStorage unless it is disabled in each Vitest worker.
    execArgv: ["--no-experimental-webstorage"],
    // vmThreads keeps each file's module registry and jsdom isolated in its own
    // VM context but reuses the worker, so jsdom itself (~450ms to load) loads
    // once per worker instead of once per file: the threads pool spent more CPU
    // re-loading jsdom than running tests (113s -> 41s wall at 8 workers). The
    // global is then the jsdom window itself, so a test cannot replace its
    // non-configurable members (location) or assign getter-only ones
    // (localStorage: use installLocalStorage), and `instanceof` fails for
    // objects built in the runner's realm, such as vi.mock's wrapper errors.
    pool: "vmThreads",
    // VM contexts grow a worker's memory file after file, and the default
    // recycle point is 1/maxWorkers of SYSTEM memory - effectively never on a
    // big host, and the whole machine on a small one. Recycling at 512MB held
    // the suite to ~3GB peak RSS at 4 workers (8.3GB unbounded; 2GB on the
    // threads pool) with no measurable wall-time cost.
    vmMemoryLimit: "512MB",
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
    // Vitest use on many-core hosts; the canonical npm test command instead
    // sizes itself from the host's spare capacity (scripts/lib/load-aware-workers.sh),
    // keeping four workers on an idle host so the root gate retains capacity
    // for its Go streams. Never fewer than two, whatever the entry point: with
    // one worker vitest shares a single VM context across every file (see
    // src/testSetup.ts), so a one-CPU container must still get two.
    maxWorkers: Math.max(2, Math.min(os.availableParallelism(), 12)),
    setupFiles: ["./src/testSetup.ts"],
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
      // The package sits outside this app's root, and v8 coverage drops such
      // files silently without this - which would take ~45 well-tested source
      // files out of the denominator and move the `web` floor for a reason
      // that has nothing to do with how well anything is tested. It brings in
      // the package files a test LOADS; the include glob's other job, putting
      // a file no test touches in the denominator at 0%, does not reach past
      // the root either way, so scripts/package-coverage.mjs asserts after the
      // run that every compiled package module is present.
      allowExternal: true,
      // Vitest reports only the files a test actually loaded, so a subsystem
      // with no test at all scores as ABSENT rather than as zero - the same
      // false green the Go side guards with its gap map. Naming the whole
      // source tree here puts every file in the denominator, so an untested
      // pane shows up as the 0% it is. (Vitest 4 removed `coverage.all`; an
      // explicit `include` is now the only lever for this.)
      include: ["src/**/*.{ts,tsx}", `${appwirePackageDir}/**/*.{ts,tsx}`],
      exclude: [
        "src/**/*.test.{ts,tsx}",
        "src/**/*.d.ts",
        `${appwirePackageDir}/**/*.test.{ts,tsx}`,
        // Fixture and harness modules exist to feed tests, and scoring them
        // measures the test rig rather than the app. The package's testing/
        // directory holds the fake client, fake socket and stream harnesses
        // the suites drive.
        `${appwirePackageDir}/fixtures/**`,
        `${appwirePackageDir}/testing/**`,
        "src/testSetup.ts",
        // Test rigging, and the one-line reload seam every test spies on so
        // no test can execute it: both would only ever score 0%.
        "src/storageTestUtils.ts",
        "src/resizeObserverTestUtils.ts",
        "src/shell/pageReload.ts",
        // A benchmark is not run by `vitest run`, so counting it only ever
        // reports 0% for code no test was ever meant to execute.
        "src/**/*.bench.ts",
        `${appwirePackageDir}/**/*.bench.ts`,
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
