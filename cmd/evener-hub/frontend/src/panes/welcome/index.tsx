// Registers the "welcome" pane type. Split from Welcome.tsx (the actual
// component) so component.tsx dynamic-import()s a DIFFERENT module than the
// one running this registerPane() call - lazy(() => import("./index")) on
// itself would be a self-import, which defeats code-splitting and is a
// known footgun. This file stays tiny and eager; Welcome.tsx is the
// lazy-loaded chunk.
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import type { WelcomePaneParams } from "./Welcome";

registerPane<WelcomePaneParams>({
  id: "welcome",
  title: () => "Welcome",
  component: lazy(() => import("./Welcome")),
  // Only one "no session open" view ever makes sense at a time.
  singleton: true,
});
