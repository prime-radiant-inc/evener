// Session-pane cycling for the session.next/session.previous keybinding
// actions (webui-keybindings-p3 Task 1): Alt+ArrowRight/Left move focus
// through the workspace's SESSION panes in workspaceStore.panes order,
// wrapping at both ends. The action handlers (AppShell) call
// cycleSessionPane; the semantics live here, against the store, so the
// handler is one line and the policy is unit-testable without the shell.
//
// Only panes of type "session" participate - the transcript/doc/tasks/etc.
// panels beside a session are not cycling targets. Fewer than two session
// panes open is a no-op (cycling one pane onto itself would be motion
// without movement). When the focused pane is not a session (settings, a
// doc, nothing focused at all), next lands on the FIRST session pane and
// previous on the LAST - needsYouCycle.ts's nextNeedsYouRef precedent for
// "current not in the cycle list".

import { workspaceStore } from "./workspace";

export type SessionCycleDirection = "next" | "previous";

export function cycleSessionPane(direction: SessionCycleDirection): void {
  const state = workspaceStore.getState();
  const sessions = state.panes.filter((pane) => pane.type === "session");
  if (sessions.length < 2) return;
  const index = sessions.findIndex((pane) => pane.id === state.focusedPaneId);
  let target: (typeof sessions)[number] | undefined;
  if (index < 0) {
    target = direction === "next" ? sessions[0] : sessions[sessions.length - 1];
  } else {
    const step = direction === "next" ? 1 : -1;
    target = sessions[(index + step + sessions.length) % sessions.length];
  }
  if (target) state.focusPane(target.id);
}
