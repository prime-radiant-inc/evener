import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

const root = process.env.OVERFLOW_FRONTEND;
if (!root) throw new Error("OVERFLOW_FRONTEND is required");
if (!process.env.OVERFLOW_DIST) throw new Error("OVERFLOW_DIST is required");

export default defineConfig({
  root,
  base: "./",
  plugins: [react()],
  build: {
    outDir: process.env.OVERFLOW_DIST,
    emptyOutDir: true,
    rollupOptions: { input: path.join(root, "overflowharness.html") },
  },
});
