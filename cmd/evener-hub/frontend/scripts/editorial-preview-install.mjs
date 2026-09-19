import { lstatSync } from "node:fs";
import path from "node:path";

// The editorial preview pins its dev server's fs.allow to exactly the
// frontend plus the AppWire package at appwire-client/typescript, the one
// intentional entry outside the checkout (see editorial-preview.vite.config.mjs
// and its test - the package's modules are served over /@fs/ and 403 without
// it) - a contract that only holds when the install is the checkout's OWN, not
// a fleet worktree's symlink into the one shared install (a tree every
// concurrent lane can write). The config refuses to serve on a shared install;
// the preview's own tests skip on one instead of re-testing a contract that
// cannot hold there. CI's fresh npm ci checkout is where these actually run.
//
// The verdict tests node_modules ITSELF (lstat), never the whole resolved
// path: realpathSync would also resolve a symlinked PARENT (macOS's
// /tmp -> /private/tmp, a symlinked worktree root) and call every checkout
// living under one "shared", silently refusing real installs (roborev #1143).
// A missing install is its own failure - every caller of this helper has
// already imported vite, which cannot resolve without it - so it returns the
// "not shared" verdict rather than throwing from under the caller's message.
export function isSharedNodeModules(frontend) {
  try {
    return lstatSync(path.join(frontend, "node_modules")).isSymbolicLink();
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}
