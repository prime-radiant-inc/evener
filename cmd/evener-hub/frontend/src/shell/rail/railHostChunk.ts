// The one place the desktop rail host's chunk is fetched.
//
// RailHost pulls in the 1605-line Rail tree plus RailRow, railNodes and the
// navigation selectors, dead weight for the first paint of AppShell, so it
// ships as its own chunk behind the shell/rail barrel's lazy seam. That fetch
// is a separate network request from index.html and can fail on its own - a
// hub restarting mid-load, a slow link, or a deploy that replaced the hashed
// filename. Isolating the import here gives the rail's failure and retry
// tests one lever to fail it with, instead of reaching into the bundler.
//
// `typeof import(...)` is a type-only reference and is erased at build
// time, so it does not pull RailHost back into the eager graph.
export type RailHostModule = typeof import("./RailHost");

export type RailHostImporter = (retryURL?: string) => Promise<RailHostModule>;

const RAIL_HOST_CHUNK_PATH = /\/RailHost-[A-Za-z0-9_-]+\.js$/;
const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

let railHostChunkURL: string | null = null;
let retrySequence = 0;

let railHostImporterForTests: RailHostImporter | null = null;

function railHostURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !RAIL_HOST_CHUNK_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function railHostURLFromError(error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = railHostURL(candidate);
    if (url !== null) return url;
  }
  return null;
}

function rememberRailHostURL(error: unknown): void {
  railHostChunkURL = railHostURLFromError(error) ?? railHostChunkURL;
}

// Test-only seam: it replaces only native module evaluation, leaving the
// retry URL boundary above real and observable in jsdom.
export function setRailHostImporterForTests(importer: RailHostImporter): void {
  railHostImporterForTests = importer;
}

export function resetRailHostLoaderForTests(): void {
  railHostChunkURL = null;
  retrySequence = 0;
  railHostImporterForTests = null;
}

function rememberError(error: unknown): never {
  rememberRailHostURL(error);
  throw error;
}

export function loadRailHost(cacheBust = false): Promise<RailHostModule> {
  if (cacheBust && railHostChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. A same-URL retry never
    // reaches the network stack again - it replays the cached failure - so
    // the retry carries a fresh token the server ignores but the browser's
    // module map treats as a distinct request.
    const retryURL = new URL(railHostChunkURL);
    retryURL.searchParams.set("evener-rail-retry", String(++retrySequence));
    const href = retryURL.href;
    return (
      railHostImporterForTests === null
        ? (import(/* @vite-ignore */ href) as Promise<RailHostModule>)
        : railHostImporterForTests(href)
    ).catch(rememberError);
  }

  const loading = railHostImporterForTests === null ? import("./RailHost") : railHostImporterForTests();
  return loading.catch(rememberError);
}
