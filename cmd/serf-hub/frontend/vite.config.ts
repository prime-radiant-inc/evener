import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The dev server proxies every hub-owned route to a locally running serf-hub.
// Cookies are port-agnostic on localhost, so the /auth?token= capability flow
// works through the proxy unchanged.
const hub = process.env.SERF_HUB_ADDR ?? "http://127.0.0.1:9180";

export default defineConfig({
  plugins: [react()],
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
