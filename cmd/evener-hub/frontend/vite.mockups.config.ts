// vite.mockups.config.ts — serves the mockups page (mockups.html) to remote
// reviewers. It wraps the app's own vite.config.ts unchanged and only relaxes
// the dev server's host allowlist, which would otherwise answer the
// Tailscale hostname with 403 ("Blocked request. This host is not allowed")
// — Vite's DNS-rebinding protection. Mockup-preview only; the product's dev
// server and every guard keep the base config's behavior.
//
//   npm run dev -- --config vite.mockups.config.ts --host 0.0.0.0 --port 5199
import { defineConfig } from "vitest/config";
import base from "./vite.config";

export default defineConfig({
  ...base,
  server: {
    ...(base.server ?? {}),
    allowedHosts: true,
  },
});
