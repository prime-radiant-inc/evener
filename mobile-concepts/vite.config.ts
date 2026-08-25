import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const tauriPlatform = process.env.TAURI_ENV_PLATFORM;
const buildPlatform = tauriPlatform === "android" ? "android" : "ios";

export default defineConfig({
  plugins: [react()],
  define: { __TAURI_BUILD_PLATFORM__: JSON.stringify(buildPlatform) },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    exclude: ["**/node_modules/**", "**/dist/**", "scripts/**"],
  },
});
