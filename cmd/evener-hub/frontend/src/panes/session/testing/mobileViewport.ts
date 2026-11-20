import { MOBILE_QUERY } from "../../../shell/useIsMobile";

export function installMobileViewport(query: string = MOBILE_QUERY): () => void {
  const original = window.matchMedia;
  window.matchMedia = ((media: string) => ({
    matches: media === query,
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
