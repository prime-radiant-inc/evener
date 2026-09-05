// Canonical shell action ids. Where a palette command already owns the
// behavior, the action id IS the palette command id (see
// shell/palette/commands.ts: "next-needs-you"); the rest are shell action ids.
// Task 2 registers each id's real run function against the registry.

export const ACTIONS = {
  paletteOpen: "palette.open",
  railToggle: "rail.toggle",
  composerFocus: "composer.focus",
  nextNeedsYou: "next-needs-you",
  selectionQuote: "selection.quote",
  settingsClose: "settings.close",
} as const;

export type ActionId = (typeof ACTIONS)[keyof typeof ACTIONS];
