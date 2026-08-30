// Vite dev-server config for the live conversation browser guard.
//
// Serves `live-conversation-harness.html` from `mobile/` on the private port
// selected by `startBrowserGuard`. It never changes production `index.html` or
// app routing. It extends the mobile app's own vitest/vite config so the guard
// exercises the real plugin/transform pipeline, with the same hermetic
// deviations as the frontend browser guard:
//
//   server.watch: null  - no filesystem watcher.
//   server.hmr: false   - no HMR client channel.
//   server.open: false  - never auto-open a browser.
//   build.rollupOptions.input — point at the harness HTML, not index.html.
import { defineConfig, mergeConfig } from "vite";
import { fileURLToPath } from "node:url";
import path from "node:path";
import baseConfig from "../vite.config.ts";

const moduleDirectory = path.dirname(fileURLToPath(import.meta.url));
const mobileRoot = path.resolve(moduleDirectory, "..");

export default mergeConfig(
  baseConfig,
  defineConfig({
    root: mobileRoot,
    server: {
      watch: null,
      hmr: false,
      open: false,
    },
    build: {
      rollupOptions: {
        input: path.join(mobileRoot, "live-conversation-harness.html"),
      },
    },
  }),
);
