// Registers the "spawn" pane type. Split from Spawn.tsx (the actual
// component) for the same reason panes/welcome/index.tsx is split from
// Welcome.tsx: component.tsx dynamic-import()s a DIFFERENT module than the one
// running this registerPane() call - lazy(() => import("./index")) on itself
// would be a self-import, defeating code-splitting. This file stays tiny and
// eager; Spawn.tsx is the lazy-loaded chunk.
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import type { SpawnPaneParams } from "./Spawn";

registerPane<SpawnPaneParams>({
  id: "spawn",
  title: () => "New session",
  component: lazy(() => import("./Spawn")),
  // Only one "start a new session" pane ever makes sense at a time; a second
  // /new focuses the existing one (workspace.ts's singleton handling).
  singleton: true,
});
