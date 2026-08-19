// Registers the "session" pane type. Split from Session.tsx (the actual
// component) for the same reason panes/welcome/index.tsx is split from
// Welcome.tsx: component.tsx dynamic-import()s a DIFFERENT module than the
// one running this registerPane() call - lazy(() => import("./index")) on
// itself would be a self-import, defeating code-splitting. This file stays
// tiny and eager; Session.tsx is the lazy-loaded chunk.
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import type { SessionPaneParams } from "./Session";

registerPane<SessionPaneParams>({
  id: "session",
  // Prefer the live ThreadModel name (DockHost's ctx is backed by the
  // threads store) so the tab tracks a rename in real time; fall back to
  // the raw ref when the thread hasn't hydrated a name yet (or isn't
  // tracked at all - a freshly-opened deep link, before ensureThread()
  // resolves), rather than showing a blank or placeholder tab title.
  title: (params, ctx) => ctx.threadName?.(params.ref) ?? params.ref,
  component: lazy(() => import("./Session")),
  // Not a singleton: distinct refs are distinct panes. Reopening the SAME
  // ref is deduped by workspace.ts's own same-params check, not this flag.
});
