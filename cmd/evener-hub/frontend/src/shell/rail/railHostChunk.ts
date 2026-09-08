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
const RAIL_HOST_STYLESHEET_PATH = /\/RailHost-[A-Za-z0-9_-]+\.css$/;
const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

interface LinkDescriptor {
  rel: string;
  as: string;
  href: string;
  crossOrigin: string | null;
  integrity: string;
  referrerPolicy: string;
  nonce: string | null;
}

let railHostChunkURL: string | null = null;
let railHostPreloadLink: LinkDescriptor | null = null;
let railHostStylesheetLinks: LinkDescriptor[] = [];
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

function railHostStylesheetURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !RAIL_HOST_STYLESHEET_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function railHostStylesheetURLFromError(error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = railHostStylesheetURL(candidate);
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

function rememberRailHostAssets(): void {
  if (typeof document === "undefined") return;

  for (const link of document.querySelectorAll<HTMLLinkElement>(
    'link[rel="modulepreload"][href], link[rel="stylesheet"][href]',
  )) {
    if (link.rel === "modulepreload") {
      const url = railHostURL(link.href);
      if (url !== null) {
        railHostChunkURL = railHostChunkURL ?? url;
        railHostPreloadLink = railHostPreloadLink ?? describeLink(link);
      }
      continue;
    }

    const url = railHostStylesheetURL(link.href);
    if (url === null || railHostStylesheetLinks.some((saved) => linkPath(saved.href) === linkPath(url))) continue;
    railHostStylesheetLinks.push(describeLink(link));
  }
}

function rememberRailHostURL(error: unknown): void {
  railHostChunkURL = railHostURLFromError(error) ?? railHostChunkURL;
  rememberRailHostAssets();
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
  url.searchParams.set("evener-rail-retry", retryToken);
  return url.href;
}

function preloadRetryAssets(retryURL: string): Promise<void> {
  if (typeof document === "undefined") return Promise.resolve();

  const retryToken = new URL(retryURL).searchParams.get("evener-rail-retry");
  if (retryToken === null) return Promise.resolve();

  const modulepreload = createRetryLink(railHostPreloadLink, "modulepreload", retryURL);
  document.head.appendChild(modulepreload);

  const styles = railHostStylesheetLinks.map((template) => {
    const link = createRetryLink(template, "stylesheet", retryAssetURL(template.href, retryToken));
    const loaded = new Promise<void>((resolve, reject) => {
      link.addEventListener("load", () => resolve(), { once: true });
      link.addEventListener("error", () => reject(new Error(`Unable to preload RailHost CSS for ${link.href}`)), {
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
export function setRailHostImporterForTests(importer: RailHostImporter): void {
  railHostImporterForTests = importer;
}

export function resetRailHostLoaderForTests(): void {
  railHostChunkURL = null;
  railHostPreloadLink = null;
  railHostStylesheetLinks = [];
  retrySequence = 0;
  railHostImporterForTests = null;
}

function rememberError(error: unknown): never {
  rememberRailHostURL(error);
  throw error;
}

export function isStaleRailHostChunkError(error: unknown): boolean {
  return railHostURLFromError(error) !== null || railHostStylesheetURLFromError(error) !== null;
}

export function loadRailHost(cacheBust = false): Promise<RailHostModule> {
  if (cacheBust && railHostChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. Give the JS and every CSS
    // asset Vite exposed for RailHost the same new token, wait for CSS, then
    // evaluate the cache-busted module so a stylesheet failure cannot leave a
    // mounted host with an unstyled rail.
    const retryURL = new URL(railHostChunkURL);
    retryURL.searchParams.set("evener-rail-retry", String(++retrySequence));
    return preloadRetryAssets(retryURL.href)
      .then(() =>
        railHostImporterForTests === null
          ? (import(/* @vite-ignore */ retryURL.href) as Promise<RailHostModule>)
          : railHostImporterForTests(retryURL.href),
      )
      .catch(rememberError);
  }

  const loading = railHostImporterForTests === null ? import("./RailHost") : railHostImporterForTests();
  rememberRailHostAssets();
  return loading.catch(rememberError);
}
