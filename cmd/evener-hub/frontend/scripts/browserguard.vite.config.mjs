// Vite dev-server config for the browser guards (layoutguard, overflowguard,
// shellguard, spawnguard - all spawned through browserGuardProcess.mjs's
// startBrowserGuard). Extends the app's own config so the guards exercise the
// real plugin/transform pipeline, with two deviations that make a guard run
// hermetic:
//
//   server.watch: null  - no filesystem watcher. A guard GENERATES files
//                         under the served root mid-run (layoutguard's
//                         per-case tokens.css/resolved.css/harness.html);
//                         with the watcher on, chokidar reports those writes
//                         as add/change events and the dev server broadcasts
//                         "page reload" to any connected harness page -
//                         observed as Page.frameScheduledNavigation reason
//                         "reload" right after loadEventFired, racing the
//                         case's Runtime.evaluate ("Inspected target
//                         navigated or closed").
//   server.hmr: false   - no HMR client channel. Nothing in a guard hot-
//                         reloads by design; measurements must reflect the
//                         bytes the run wrote, not a live-reloading page.
//   cacheDir            - BROWSER_GUARD_VITE_CACHE_DIR when set. make
//                         test-web-browser runs guards side by side, and two
//                         Vite processes optimizing deps into one cache race
//                         (issue #1586), so it gives each guard its own.
import { defineConfig, mergeConfig } from "vite";
import baseConfig from "../vite.config";

export default mergeConfig(
  baseConfig,
  defineConfig({
    ...(process.env.BROWSER_GUARD_VITE_CACHE_DIR ? { cacheDir: process.env.BROWSER_GUARD_VITE_CACHE_DIR } : {}),
    server: {
      watch: null,
      hmr: false,
    },
  }),
);
