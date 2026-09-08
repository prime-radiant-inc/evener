// The shared machinery behind the shell's lazily-fetched chunks: the rail
// host (shell/rail/railHostChunk.ts) and the spawn pane's connect-provider
// dialog (panes/spawn/connectDialogChunk.ts) fetch their chunk over a
// separate network request from index.html that can fail on its own - a hub
// restarting mid-load, a slow link, or a deploy that replaced the hashed
// filename. Both loaders keep the same shape - remember the hashed JS/CSS
// URLs Vite exposed, retry over a cache-busted URL (Chrome retains a failed
// module fetch by URL, so a same-URL retry would replay the cached failure),
// and classify stale hashed-asset URLs back out of errors - differing only in
// the chunk's path regexes, the retry query-param token, and the label their
// CSS-preload failure carries. Those three live in ChunkRetryOptions; the
// per-chunk module keeps only its options plus the thin typed wrapper its
// importers already call. DockRegion's own dockHostChunk.ts keeps its private
// copy: it is the read-only reference the rail and dialog mirrors were built
// from, and unifying it too would touch files outside this helper's lane.
//
// `typeof import(...)` references stay in the per-chunk modules as type-only
// aliases (erased at build time), so this helper never pulls a chunk back
// into the eager graph.
export interface ChunkRetryOptions {
  // Matches the hashed chunk JS path Vite emits, e.g. /RailHost-a1b2c3.js.
  chunkPath: RegExp;
  // Matches the hashed chunk CSS path Vite emits, e.g. /RailHost-d4e5f6.css.
  stylesheetPath: RegExp;
  // Query-param token shared by the chunk JS and every chunk CSS retry URL,
  // e.g. "evener-rail-retry". Distinct per chunk so concurrent retries of two
  // chunks never share a cache-busting sequence.
  retryParam: string;
  // Label for the CSS-preload failure the retry can reject with, e.g.
  // "RailHost" in `Unable to preload RailHost CSS for ...`.
  assetLabel: string;
}

export type ChunkImporter<Module> = (retryURL?: string) => Promise<Module>;

interface LinkDescriptor {
  rel: string;
  as: string;
  href: string;
  crossOrigin: string | null;
  integrity: string;
  referrerPolicy: string;
  nonce: string | null;
}

const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

export interface ChunkRetryLoader<Module> {
  load(cacheBust?: boolean): Promise<Module>;
  isStaleChunkError(error: unknown): boolean;
  setImporterForTests(importer: ChunkImporter<Module>): void;
  resetLoaderForTests(): void;
}

function chunkURL(options: ChunkRetryOptions, candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !options.chunkPath.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function chunkURLFromError(options: ChunkRetryOptions, error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = chunkURL(options, candidate);
    if (url !== null) return url;
  }
  return null;
}

function chunkStylesheetURL(options: ChunkRetryOptions, candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !options.stylesheetPath.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function chunkStylesheetURLFromError(options: ChunkRetryOptions, error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = chunkStylesheetURL(options, candidate);
    if (url !== null) return url;
  }
  return null;
}

function linkPath(href: string): string {
  try {
    return new URL(href, window.location.href).pathname;
  } catch {
    return href;
  }
}

function describeLink(link: HTMLLinkElement): LinkDescriptor {
  return {
    rel: link.rel,
    as: link.as,
    href: link.href,
    crossOrigin: link.getAttribute("crossorigin"),
    integrity: link.integrity,
    referrerPolicy: link.referrerPolicy,
    nonce: link.getAttribute("nonce"),
  };
}

function setLinkAttributes(link: HTMLLinkElement, template: LinkDescriptor | null): void {
  link.crossOrigin = template?.crossOrigin ?? "";
  if (template?.as) link.as = template.as;
  if (template?.integrity) link.integrity = template.integrity;
  if (template?.referrerPolicy) link.referrerPolicy = template.referrerPolicy;

  const nonce =
    template?.nonce ?? document.querySelector<HTMLMetaElement>('meta[property="csp-nonce"]')?.getAttribute("nonce");
  if (nonce) link.setAttribute("nonce", nonce);
}

function createRetryLink(template: LinkDescriptor | null, rel: string, href: string): HTMLLinkElement {
  const link = document.createElement("link");
  link.rel = template?.rel ?? rel;
  setLinkAttributes(link, template);
  link.href = href;
  return link;
}

// One loader's worth of remembered chunk state: the hashed JS URL the last
// failure named (or Vite's own modulepreload link), the preload link's
// attributes to clone onto retries, every chunk stylesheet link, and the
// retry sequence that keeps each cache-busted URL fresh.
interface ChunkRetryState {
  chunkURL: string | null;
  preloadLink: LinkDescriptor | null;
  stylesheetLinks: LinkDescriptor[];
  retrySequence: number;
}

function scanChunkLinks(options: ChunkRetryOptions, state: ChunkRetryState): void {
  if (typeof document !== "undefined") {
    for (const link of document.querySelectorAll<HTMLLinkElement>(
      'link[rel="modulepreload"][href], link[rel="stylesheet"][href]',
    )) {
      if (link.rel === "modulepreload") {
        const url = chunkURL(options, link.href);
        if (url !== null) {
          state.chunkURL = state.chunkURL ?? url;
          state.preloadLink = state.preloadLink ?? describeLink(link);
        }
        continue;
      }

      const url = chunkStylesheetURL(options, link.href);
      if (url === null || state.stylesheetLinks.some((saved) => linkPath(saved.href) === linkPath(url))) continue;
      state.stylesheetLinks.push(describeLink(link));
    }
  }
}

function rememberChunkURL(options: ChunkRetryOptions, state: ChunkRetryState, error: unknown): never {
  state.chunkURL = chunkURLFromError(options, error) ?? state.chunkURL;
  scanChunkLinks(options, state);
  throw error;
}

function retryAssetURL(options: ChunkRetryOptions, href: string, retryToken: string): string {
  const url = new URL(href, window.location.href);
  url.searchParams.set(options.retryParam, retryToken);
  return url.href;
}

function preloadRetryAssets(options: ChunkRetryOptions, state: ChunkRetryState, retryURL: string): Promise<void> {
  if (typeof document === "undefined") return Promise.resolve();

  const retryToken = new URL(retryURL).searchParams.get(options.retryParam);
  if (retryToken === null) return Promise.resolve();

  const modulepreload = createRetryLink(state.preloadLink, "modulepreload", retryURL);
  document.head.appendChild(modulepreload);

  const styles = state.stylesheetLinks.map((template) => {
    const link = createRetryLink(template, "stylesheet", retryAssetURL(options, template.href, retryToken));
    const loaded = new Promise<void>((resolve, reject) => {
      link.addEventListener("load", () => resolve(), { once: true });
      link.addEventListener(
        "error",
        () => reject(new Error(`Unable to preload ${options.assetLabel} CSS for ${link.href}`)),
        { once: true },
      );
    });
    document.head.appendChild(link);
    return loaded;
  });

  return Promise.all(styles).then(() => undefined);
}

// Builds one chunk's loader from its chunk-specific options plus the two
// import shapes: the eager static import for the first load and the
// cache-busted URL import for retries. The test importer seam replaces only
// native module evaluation, leaving the retry asset boundary above real and
// observable in jsdom.
export function createChunkRetryLoader<Module>(
  options: ChunkRetryOptions,
  initialImport: () => Promise<Module>,
  retryImport: (retryURL: string) => Promise<Module>,
): ChunkRetryLoader<Module> {
  const state: ChunkRetryState = { chunkURL: null, preloadLink: null, stylesheetLinks: [], retrySequence: 0 };
  let importerForTests: ChunkImporter<Module> | null = null;

  const rememberError = (error: unknown): never => rememberChunkURL(options, state, error);

  return {
    load(cacheBust = false): Promise<Module> {
      if (cacheBust && state.chunkURL !== null) {
        // Chrome retains a failed module fetch by URL. Give the JS and every
        // CSS asset Vite exposed for the chunk the same new token, wait for
        // CSS, then evaluate the cache-busted module so a stylesheet failure
        // cannot leave a mounted host with an unstyled surface.
        const retryURL = new URL(state.chunkURL);
        retryURL.searchParams.set(options.retryParam, String(++state.retrySequence));
        return preloadRetryAssets(options, state, retryURL.href)
          .then(() => (importerForTests === null ? retryImport(retryURL.href) : importerForTests(retryURL.href)))
          .catch(rememberError);
      }

      const loading = importerForTests === null ? initialImport() : importerForTests();
      scanChunkLinks(options, state);
      return loading.catch(rememberError);
    },

    isStaleChunkError(error: unknown): boolean {
      return chunkURLFromError(options, error) !== null || chunkStylesheetURLFromError(options, error) !== null;
    },

    // Test-only seam: it replaces only native module evaluation, leaving the
    // retry asset boundary above real and observable in jsdom.
    setImporterForTests(importer: ChunkImporter<Module>): void {
      importerForTests = importer;
    },

    resetLoaderForTests(): void {
      state.chunkURL = null;
      state.preloadLink = null;
      state.stylesheetLinks = [];
      state.retrySequence = 0;
      importerForTests = null;
    },
  };
}
