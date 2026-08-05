// Registers the read-only "transcript" pane type (wave 8 T6). Split from
// Transcript.tsx for the same reason panes/session/index.tsx is split from
// Session.tsx: this tiny module stays eager (it runs registerPane) while the
// heavy pane component is the lazy-loaded chunk - lazy(() => import("./index"))
// on itself would be a self-import that defeats code-splitting.
//
// "transcript" is not a routed pane (routing.ts's paneToURL returns null for
// it); it is opened contextually via shell/paneActions.ts's openBeside, which
// side-effect-imports THIS module so the type is registered before it opens
// one. PaneTypeId already carries "transcript" (paneRegistry.ts) - no union
// edit needed.
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import type { TranscriptParams } from "./Transcript";

registerPane<TranscriptParams>({
  id: "transcript",
  // Same title contract as the session pane: prefer the live ThreadModel name
  // (DockHost's ctx is backed by the threads store, so the tab tracks a rename)
  // and fall back to the raw ref when no name has hydrated yet - never a blank
  // or placeholder tab.
  title: (params, ctx) => ctx.threadName?.(params.ref) ?? params.ref,
  component: lazy(() => import("./Transcript")),
  // Not a singleton: distinct refs are distinct read-only panes. Re-opening the
  // SAME ref is deduped by workspace.ts's own same-params check (openBeside
  // relies on exactly that), not this flag.
});
