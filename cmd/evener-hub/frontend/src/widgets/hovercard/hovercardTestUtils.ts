import { vi } from "vitest";

/**
 * Stubs matchMedia to report only "(hover: none)" as matching - a touch-only
 * device. Shared by the HoverCard's own tests and the rail row's integration
 * test, which both pin what a hoverless device does with the title's status
 * card. Restore it with vi.unstubAllGlobals() in the test's own afterEach.
 */
export function installHoverlessMatchMedia(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockImplementation((query: string) => ({
      matches: query === "(hover: none)",
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  );
}
