// Shared vocabulary for reasoning-effort chips and pickers, so every surface
// (session status row, command palette, spawn form) speaks one language:
// "" is "(default)" (the session default applies), "none" is an explicit off
// the user chose and always reads "none (off)".

export function effortLabel(level: string): string {
  if (level === "") return "(default)";
  if (level === "none") return "none (off)";
  return level;
}

// The option values a live-session effort picker offers: a "(default)" head,
// the model's ladder as the model orders it (a ladder-listed "none" rides in
// place), and the current value appended when the session already runs at a
// level the ladder omits — the select must be able to display it.
export function effortOptionLevels(levels: string[], current: string): string[] {
  return ["", ...levels, ...(current !== "" && !levels.includes(current) ? [current] : [])];
}
