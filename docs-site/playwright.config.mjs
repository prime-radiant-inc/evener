import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./scripts",
  testMatch: "*.browser.mjs",
  use: {
    baseURL: "http://127.0.0.1:4173/evener/",
    channel: process.env.DOCS_BROWSER_CHANNEL || undefined,
  },
  webServer: {
    command: "npm run preview -- --host 127.0.0.1 --port 4173",
    url: "http://127.0.0.1:4173/evener/",
    reuseExistingServer: false,
  },
});
