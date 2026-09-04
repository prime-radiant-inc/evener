// Canonical shell action ids. Where a palette command already owns the
// behavior, the action id IS the palette command id (see
// shell/palette/commands.ts: "next-needs-you" and "settings"); the rest are
// shell action ids. The components that own each behavior register its run
// function against the registry (AppShell, RailHost, Settings,
// SelectionQuote, the session transcript's useTranscriptScrollKeys).

export const ACTIONS = {
  paletteOpen: "palette.open",
  railToggle: "rail.toggle",
  composerFocus: "composer.focus",
  nextNeedsYou: "next-needs-you",
  selectionQuote: "selection.quote",
  sessionNext: "session.next",
  sessionPrevious: "session.previous",
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
} as const;

export type ActionId = (typeof ACTIONS)[keyof typeof ACTIONS];
