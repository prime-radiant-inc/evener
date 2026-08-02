// The one place the desktop workspace host's chunk is fetched.
//
// DockHost pulls in dockview (~636kB), dead weight for the mobile path, so
// it ships as its own chunk that only a desktop session downloads. That
// fetch is a separate network request from index.html and can fail on its
// own - a hub restarting mid-load, a slow link, or a deploy that replaced
// the hashed filename. Isolating the import here gives DockRegion's failure
// and retry tests one lever to fail it with, instead of reaching into the
// bundler.
//
// `typeof import(...)` is a type-only reference and is erased at build
// time, so it does not pull DockHost back into the eager graph.
export type DockHostModule = typeof import("./DockHost");

const DOCK_HOST_CHUNK_PATH = /\/DockHost-[A-Za-z0-9_-]+\.js$/;
const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

let dockHostChunkURL: string | null = null;
let retrySequence = 0;

function dockHostURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !DOCK_HOST_CHUNK_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function dockHostURLFromError(error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = dockHostURL(candidate);
    if (url !== null) return url;
  }
  return null;
}

function preloadedDockHostURL(): string | null {
  if (typeof document === "undefined") return null;
  // Vite inserts this link synchronously when the static import starts. It is
  // the only place the built hash is observable while the request is still
  // pending, before an import error exists to name it.
  for (const link of document.querySelectorAll<HTMLLinkElement>('link[rel="modulepreload"][href]')) {
    const url = dockHostURL(link.href);
    if (url !== null) return url;
  }
  return null;
}

function rememberDockHostURL(error: unknown): void {
  dockHostChunkURL = dockHostURLFromError(error) ?? dockHostChunkURL;
}

export function isStaleDockHostChunkError(error: unknown): boolean {
  return dockHostURLFromError(error) !== null;
}

export function loadDockHost(cacheBust = false): Promise<DockHostModule> {
  if (cacheBust && dockHostChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. A distinct query reaches
    // the network while preserving the same built module and relative imports.
    const retryURL = new URL(dockHostChunkURL);
    retryURL.searchParams.set("serf-dock-retry", String(++retrySequence));
    return import(/* @vite-ignore */ retryURL.href).catch((error) => {
      rememberDockHostURL(error);
      throw error;
    }) as Promise<DockHostModule>;
  }

  const loading = import("./DockHost");
  dockHostChunkURL = preloadedDockHostURL() ?? dockHostChunkURL;
  return loading.catch((error) => {
    rememberDockHostURL(error);
    throw error;
  });
}
