// Registers the "settings" pane type. Split from Settings.tsx (the actual
// component) for the same reason panes/welcome/index.tsx and
// panes/session/index.tsx are split from their own components:
// component.tsx dynamic-import()s a DIFFERENT module than the one running
// this registerPane() call - lazy(() => import("./index")) on itself would
// be a self-import, defeating code-splitting. This file stays tiny and
// eager; Settings.tsx is the lazy-loaded chunk.
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import type { SettingsPaneParams } from "./Settings";
import { DEFAULT_SECTION_ID, settingsSectionLabel } from "./sections";

registerPane<SettingsPaneParams>({
  id: "settings",
  // The focused section's own label (e.g. "Theme", "Providers &
  // credentials") - no "Settings:" prefix, matching Welcome/Session's own
  // tab-title precedent of showing just the one thing this pane instance
  // is currently about, not a generic pane-type name.
  title: (params) => settingsSectionLabel(params.section ?? DEFAULT_SECTION_ID),
  component: lazy(() => import("./Settings")),
  // Every settings section lives in ONE pane instance (nav + content) -
  // navigating between sections updates this same pane's params in place
  // (workspace.ts's own singleton handling), it never opens a second copy.
  singleton: true,
});
