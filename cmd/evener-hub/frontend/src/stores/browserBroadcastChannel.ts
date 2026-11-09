/**
 * The browser's cross-tab BroadcastChannel, or null where one must not exist.
 *
 * In the test VM the visible BroadcastChannel is Node's own: it crosses worker
 * threads and keeps an event loop alive, so one test file's broadcasts can
 * reach another concurrent file's handler and stall its worker at exit. Tests
 * that need a channel install one through their own factory seam. A host
 * without BroadcastChannel degrades the same way: the durable stores keep
 * correctness without cross-tab wakeups.
 */
export function browserBroadcastChannelFactory(): ((name: string) => BroadcastChannel) | undefined {
  return typeof BroadcastChannel !== "undefined" && import.meta.env.MODE !== "test"
    ? (name) => new BroadcastChannel(name)
    : undefined;
}

export function createBrowserBroadcastChannel(name: string): BroadcastChannel | null {
  return browserBroadcastChannelFactory()?.(name) ?? null;
}
