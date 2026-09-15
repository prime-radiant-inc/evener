// The producer's wildcard reads as words on every watch surface, never as a bare "*".
export function watchEventLabel(name: string): string {
  return name === "*" ? "any event" : name;
}
