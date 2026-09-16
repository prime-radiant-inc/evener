// Canonical action ids for the keybinding registry, shared by the web shell
// (which registers a run function per action) and native (which lists them
// for its shortcut settings). Where a web palette command already owns the
// behavior, the action id IS the palette command id ("next-needs-you" and
// "settings"); the rest are shell action ids.

export const ACTIONS = {
  paletteOpen: "palette.open",
  railToggle: "rail.toggle",
  composerFocus: "composer.focus",
  nextNeedsYou: "next-needs-you",
  selectionQuote: "selection.quote",
  sessionNext: "session.next",
  sessionPrevious: "session.previous",
  sessionLiveNext: "session.liveNext",
  sessionLivePrevious: "session.livePrevious",
  transcriptLineUp: "transcript.lineUp",
  transcriptLineDown: "transcript.lineDown",
  transcriptPageUp: "transcript.pageUp",
  transcriptPageDown: "transcript.pageDown",
  transcriptScrollTop: "transcript.scrollTop",
  transcriptScrollBottom: "transcript.scrollBottom",
  // Reuses the palette's "settings" command id: that command's run owns the
  // behavior (navigate("/settings")), exactly the next-needs-you precedent
  // above.
  settingsOpen: "settings",
  settingsClose: "settings.close",
  // Phase 4a (the p4 plan's Design decision 3): the cheatsheet overlay's
  // open/toggle and close. toggle's handler lives in the CheatsheetOverlay
  // component (shell/cheatsheet/), close's next to it, scope-gated.
  cheatsheetToggle: "cheatsheet.toggle",
  cheatsheetClose: "cheatsheet.close",
} as const;

export type ActionId = (typeof ACTIONS)[keyof typeof ACTIONS];
