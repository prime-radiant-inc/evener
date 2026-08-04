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
const DOCK_HOST_STYLESHEET_PATH = /\/DockHost-[A-Za-z0-9_-]+\.css$/;
const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

export type DockHostImporter = (retryURL?: string) => Promise<DockHostModule>;

interface LinkDescriptor {
  rel: string;
  as: string;
  href: string;
  crossOrigin: string | null;
  integrity: string;
  referrerPolicy: string;
  nonce: string | null;
}

let dockHostChunkURL: string | null = null;
let dockHostPreloadLink: LinkDescriptor | null = null;
let dockHostStylesheetLinks: LinkDescriptor[] = [];
let retrySequence = 0;

let dockHostImporterForTests: DockHostImporter | null = null;

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

function dockHostStylesheetURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !DOCK_HOST_STYLESHEET_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function dockHostStylesheetURLFromError(error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = dockHostStylesheetURL(candidate);
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

function rememberDockHostAssets(): void {
  if (typeof document === "undefined") return;

  for (const link of document.querySelectorAll<HTMLLinkElement>(
    'link[rel="modulepreload"][href], link[rel="stylesheet"][href]',
  )) {
    if (link.rel === "modulepreload") {
      const url = dockHostURL(link.href);
      if (url !== null) {
        dockHostChunkURL = dockHostChunkURL ?? url;
        dockHostPreloadLink = dockHostPreloadLink ?? describeLink(link);
      }
      continue;
    }

    const url = dockHostStylesheetURL(link.href);
    if (url === null || dockHostStylesheetLinks.some((saved) => linkPath(saved.href) === linkPath(url))) continue;
    dockHostStylesheetLinks.push(describeLink(link));
  }
}

function rememberDockHostURL(error: unknown): void {
  dockHostChunkURL = dockHostURLFromError(error) ?? dockHostChunkURL;
  rememberDockHostAssets();
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

function retryAssetURL(href: string, retryToken: string): string {
  const url = new URL(href, window.location.href);
  url.searchParams.set("serf-dock-retry", retryToken);
  return url.href;
}

function preloadRetryAssets(retryURL: string): Promise<void> {
  if (typeof document === "undefined") return Promise.resolve();

  const retryToken = new URL(retryURL).searchParams.get("serf-dock-retry");
  if (retryToken === null) return Promise.resolve();

  const modulepreload = createRetryLink(dockHostPreloadLink, "modulepreload", retryURL);
  document.head.appendChild(modulepreload);

  const styles = dockHostStylesheetLinks.map((template) => {
    const link = createRetryLink(template, "stylesheet", retryAssetURL(template.href, retryToken));
    const loaded = new Promise<void>((resolve, reject) => {
      link.addEventListener("load", () => resolve(), { once: true });
      link.addEventListener("error", () => reject(new Error(`Unable to preload DockHost CSS for ${link.href}`)), {
        once: true,
      });
    });
    document.head.appendChild(link);
    return loaded;
  });

  return Promise.all(styles).then(() => undefined);
}

// Test-only seam: it replaces only native module evaluation, leaving the
// retry asset boundary above real and observable in jsdom.
export function setDockHostImporterForTests(importer: DockHostImporter): void {
  dockHostImporterForTests = importer;
}

export function resetDockHostLoaderForTests(): void {
  dockHostChunkURL = null;
  dockHostPreloadLink = null;
  dockHostStylesheetLinks = [];
  retrySequence = 0;
  dockHostImporterForTests = null;
}

function rememberError(error: unknown): never {
  rememberDockHostURL(error);
  throw error;
}

export function isStaleDockHostChunkError(error: unknown): boolean {
  return dockHostURLFromError(error) !== null || dockHostStylesheetURLFromError(error) !== null;
}

export function loadDockHost(cacheBust = false): Promise<DockHostModule> {
  if (cacheBust && dockHostChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. Give the JS and every CSS
    // asset Vite exposed for DockHost the same new token, wait for CSS, then
    // evaluate the cache-busted module so a stylesheet failure cannot leave a
    // mounted host with an unstyled dock.
    const retryURL = new URL(dockHostChunkURL);
    retryURL.searchParams.set("serf-dock-retry", String(++retrySequence));
    return preloadRetryAssets(retryURL.href)
      .then(() =>
        dockHostImporterForTests === null
          ? (import(/* @vite-ignore */ retryURL.href) as Promise<DockHostModule>)
          : dockHostImporterForTests(retryURL.href),
      )
      .catch(rememberError);
  }

  const loading = dockHostImporterForTests === null ? import("./DockHost") : dockHostImporterForTests();
  rememberDockHostAssets();
  return loading.catch(rememberError);
}
