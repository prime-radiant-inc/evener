// SessionChrome: the session pane's chrome surface (status row, model
// switch, session actions, goal, tasks panel), mounted by Session.tsx at
// PaneScaffold's footer slot (T1 carves this slot; Session.tsx is FROZEN
// for the wave once T1 lands).
//
// T1 ships this as an empty placeholder. T5 (panes/session/chrome/** + this
// slot) fills it in without ever touching Session.tsx again: status row
// (state dot, model chip, reasoning effort, work-time clock, context gauge,
// cost), mid-session model switch, session actions (fork/aside/compact/
// clear/shutdown/rename) with destructive-action confirmation, goal
// display/set, and the tasks panel.
export interface SessionChromeProps {
  ref: string;
}

// _ref: unused until T5 reads it (threadsStore lookups, setModel/compact/
// clearThread/... calls) - the leading underscore satisfies Biome's
// noUnusedFunctionParameters without a suppression comment.
export function SessionChrome({ ref: _ref }: SessionChromeProps) {
  return null;
}
