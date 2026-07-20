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
  },
});
