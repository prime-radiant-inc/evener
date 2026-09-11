import { realpathSync } from "node:fs";
import path from "node:path";

// The editorial preview pins its dev server's fs.allow to exactly [frontend]
// and lets Vite's dep cache live under node_modules - both only hold when the
// install is the checkout's OWN, not a fleet worktree's symlink into the one
// shared install (a tree every concurrent lane can write). The config refuses
// to serve on a shared install; the preview's own tests skip on one instead of
// re-testing a contract that cannot hold there. CI's fresh npm ci checkout is
// where these actually run.
export function isSharedNodeModules(frontend) {
  const nodeModules = path.join(frontend, "node_modules");
  return realpathSync(nodeModules) !== nodeModules;
}
