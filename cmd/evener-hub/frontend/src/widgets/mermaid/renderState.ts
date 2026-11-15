// Which Mermaid sources failed to render, so Find can search the code a failed
// diagram shows.
//
// A diagram that renders is a picture: its source is not on screen, so Find
// must not count it. A diagram that fails falls back to a code block holding
// the source verbatim, and words the reader can see must have a match.
//
// The source text is the identity here, not the item id: one message can hold
// several diagrams, and the widget knows only the string it was given.
const failed = new Set<string>();
let version = 0;
const listeners = new Set<() => void>();

/** The segment text a diagram is drawn from, without the trailing newline the
 * fence's text carries, so the widget and the find layer name it identically. */
function keyOf(source: string): string {
  return source.trimEnd();
}

/** Record the outcome of rendering `source`. Rendering is async, so a failure
 * can land after Find has already indexed the message. */
export function noteMermaidRender(source: string, outcome: "ok" | "error"): void {
  const key = keyOf(source);
  const was = failed.has(key);
  if (outcome === "ok") {
    if (!was) return;
    failed.delete(key);
  } else {
    if (was) return;
    failed.add(key);
  }
  version += 1;
  for (const notify of listeners) notify();
}

/** Whether a diagram drawn from `source` is showing its source as code. */
export function mermaidSourceShown(source: string): boolean {
  return failed.has(keyOf(source));
}

/** A counter that changes whenever the set of failed sources does, so a
 * searchable-text cache can tell it is stale. */
export function mermaidRenderVersion(): number {
  return version;
}

/** Notified after every change, for a consumer that must re-index. */
export function subscribeMermaidRender(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Test-only: a fresh set between cases. */
export function resetMermaidRenderStateForTests(): void {
  failed.clear();
  version += 1;
  for (const notify of listeners) notify();
}
