/**
 * StubResizeObserver is a no-op ResizeObserver for tests. jsdom ships none,
 * and dockview-core constructs one on mount to drive its auto-resizing
 * (verified via a live probe), so any test that mounts DockHost installs this
 * with `globalThis.ResizeObserver = StubResizeObserver`. It never fires, which
 * suits tests that do not assert on pixel geometry; a test that needs to
 * drive size changes keeps its own callback-capturing stub.
 */
export class StubResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
