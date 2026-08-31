// Shared mobile-viewport stub for shell tests. jsdom implements no matchMedia
// at all (useIsMobile.test.ts's own header documents the probe), so a
// mobile-layout test installs one: the mobile query matches, every other
// query does not. Returns a restore function; call it in afterEach/finally.
import { resetMobileViewportForTests } from "../useIsMobile";

export function installMobileViewport(): () => void {
  const original = window.matchMedia;
  window.matchMedia = ((media: string) => ({
    matches: media === "(max-width: 899px)",
    media,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  // Drop any source cached against a previous (or absent) matchMedia so the
  // next snapshot re-reads this stub.
  resetMobileViewportForTests();

  return () => {
    if (original) window.matchMedia = original;
    else {
      // @ts-expect-error jsdom has no matchMedia by default.
      delete window.matchMedia;
    }
    resetMobileViewportForTests();
  };
}
