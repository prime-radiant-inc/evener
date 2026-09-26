/**
 * reloadPage reloads the document. UI actions call it rather than
 * window.location.reload() so a test can observe the reload by spying on this
 * module: jsdom's window.location is non-configurable, so under Vitest's
 * vmThreads pool, where the global is the jsdom window itself, it cannot be
 * stubbed.
 */
export function reloadPage(): void {
  window.location.reload();
}
