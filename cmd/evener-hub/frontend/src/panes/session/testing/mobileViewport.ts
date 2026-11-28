import { MOBILE_QUERY } from "../../../shell/useIsMobile";

export function installMobileViewport(queries: string | string[] = MOBILE_QUERY): () => void {
  const list = Array.isArray(queries) ? queries : [queries];
  const original = window.matchMedia;
  window.matchMedia = ((media: string) => ({
    matches: list.includes(media),
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
