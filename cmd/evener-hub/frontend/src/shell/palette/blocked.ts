// The blocked sentinel is a command run-handler's way to say "I did not run;
// keep the palette open and show the user why" (parity-m6-surfaces.md §2.7,
// search.js:198,839-846). A turn-scoped command that needs an active turn,
// or a wire call that came back Conflict, resolves to one of these instead of
// throwing - the palette renders its message in the inline .palette-error
// strip and stays open, so the user loses no typed text.

export interface Blocked {
  paletteBlocked: true;
  message: string;
}

export function blocked(message: string): Blocked {
  return { paletteBlocked: true, message };
}

export function isBlocked(value: unknown): value is Blocked {
  return typeof value === "object" && value !== null && (value as { paletteBlocked?: unknown }).paletteBlocked === true;
}

export function blockedMessage(value: unknown): string | undefined {
  return isBlocked(value) ? value.message : undefined;
}
