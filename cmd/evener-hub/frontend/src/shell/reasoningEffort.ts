// Shared vocabulary for reasoning-effort chips and pickers, so every surface
// (session status row, command palette, spawn form) speaks one language:
// "" is "(default)" (the session default applies), "none" is an explicit off
// the user chose and always reads "none (off)".

export function effortLabel(level: string, levels: string[]): string {
  if (level === "") return "(default)";
  if (level === "none") {
    // Only a model whose ladder lists a none level can actually turn
    // thinking off; elsewhere an explicit none omits the field and the
    // provider's own default applies.
    return levels.includes("none") ? "none (off)" : "none (provider default)";
  }
  return level;
}

// The option values a live-session effort picker offers: a "(default)" head,
// the model's ladder as the model orders it (a ladder-listed "none" rides in
// place), and the current value appended when the session already runs at a
// level the ladder omits — the select must be able to display it.
export function effortOptionLevels(levels: string[], current: string): string[] {
  return ["", ...levels, ...(current !== "" && !levels.includes(current) ? [current] : [])];
}
