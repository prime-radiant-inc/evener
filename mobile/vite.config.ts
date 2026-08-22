import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { mobileRendererBoundary } from "./scripts/boundary-plugin.mjs";

const mobileRoot = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(mobileRoot, "..");
const protocolAllowlist = new Set(
  JSON.parse(readFileSync(path.join(mobileRoot, "protocol-imports.json"), "utf8")) as string[],
);

export default defineConfig({
  plugins: [
    react(),
    mobileRendererBoundary({
      root: repositoryRoot,
      allowlist: protocolAllowlist,
      rendererSourceRoot: path.join(mobileRoot, "src"),
    }),
  ],
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // `scripts/` holds node:test files (e.g. check-boundary.test.mjs) run via
    // `node --test`, not vitest; exclude them so `vitest run` stays pristine.
    exclude: ["**/node_modules/**", "**/dist/**", "scripts/**"],
  },
});
