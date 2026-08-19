// Registers the "doc" pane type. Split from DocPane.tsx (the component) for
// the same reason panes/welcome/index.tsx is split from Welcome.tsx: this
// module runs registerPane() eagerly and keeps DocPane's own chunk behind a
// lazy() import so it loads only when a doc pane actually opens.
//
// AppShell.tsx imports the panes that can appear in the initial layout to
// register them; a doc pane never appears there - it only ever opens on
// demand through openDocBeside - so openDoc.ts imports THIS module for its
// side effect instead, guaranteeing "doc" is registered before dockview is
// asked to build the panel (no AppShell chokepoint edit; panes self-register).
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import { filenameOf } from "./docFile";
import type { DocParams } from "./openDoc";

registerPane<DocParams>({
  id: "doc",
  title: (params) => filenameOf(params.path),
  component: lazy(() => import("./DocPane")),
});
