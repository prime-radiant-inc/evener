export function installMobileViewport(): () => void {
  const original = window.matchMedia;
  window.matchMedia = ((media: string) => ({
    matches: media === "(max-width: 899px)",
    media,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;

  return () => {
    if (original) window.matchMedia = original;
    else {
      // @ts-expect-error jsdom has no matchMedia by default.
      delete window.matchMedia;
    }
  };
}
