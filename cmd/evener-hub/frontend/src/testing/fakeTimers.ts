// Fake timers that move only when the test moves them, wired so Testing
// Library keeps working on them.
import { vi } from "vitest";

/** Fakes the clock with no drift from wall time: a faked timer fires only when
 * the test advances it, so the outcome never depends on how loaded the host
 * is. Testing Library's async utilities (user-event's drain after each action,
 * waitFor, findBy*) wait on a setTimeout themselves, and they advance a faked
 * one only when they find a `jest` global, so this stubs one bound to
 * vitest's clock. Restore real timers in the file's afterEach. */
export function useTestDrivenFakeTimers(options?: Parameters<typeof vi.useFakeTimers>[0]): void {
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers(options);
}
