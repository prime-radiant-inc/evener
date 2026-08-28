// clean-dist.mjs — reduce frontend/dist to the tracked PLACEHOLDER before the
// build's fallible steps run.
//
// Why: `npm run build` is `tsc --noEmit && vite build`, and vite's
// emptyOutDir only fires inside the second half. A failed typecheck leaves
// the PREVIOUS build's dist fully in place, and any `go build` afterward
// silently embeds that stale SPA — the hub serves an old UI with no signal
// anything failed (the manual `rm -rf dist` workaround this script
// replaces). Cleaning first means a failed build leaves dist holding only
// the tracked PLACEHOLDER: go:embed still compiles and the hub serves its
// documented 503 "web app not built" instead of a stale app.
//
// The content written here is byte-identical to the tracked
// dist/PLACEHOLDER and to what vite.config.ts's restoreDistPlaceholder
// plugin writes at closeBundle (kata 88nn), so `git status` stays clean
// however the build ends.
//
// Safety: this script recursively deletes the contents of one directory.
// That directory is derived from this file's own location — never from
// arguments or the environment — so no caller can redirect the delete.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const distDir = path.join(frontendRoot, "dist");
const placeholderPath = path.join(distDir, "PLACEHOLDER");
const placeholderContent = "run make build-web\n";

fs.mkdirSync(distDir, { recursive: true });
for (const entry of fs.readdirSync(distDir)) {
  fs.rmSync(path.join(distDir, entry), { recursive: true, force: true });
}
fs.writeFileSync(placeholderPath, placeholderContent);
