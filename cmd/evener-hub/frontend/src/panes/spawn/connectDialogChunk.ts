// The one place the spawn pane's connect-provider dialog chunk is fetched.
//
// ConnectProviderDialog pulls in the instance-credential editors
// (instanceDialogs, oauthDialogs, oauthFlow), dead weight for the spawn
// pane's first paint, so it ships as its own chunk that only a "Connect
// provider" click downloads. That fetch is a separate network request from
// index.html and can fail on its own - a hub restarting mid-load, a slow
// link, or a deploy that replaced the hashed filename. Isolating the import
// here gives the dialog's failure and retry tests one lever to fail it
// with, instead of reaching into the bundler.
//
// `typeof import(...)` is a type-only reference and is erased at build
// time, so it does not pull ConnectProviderDialog back into the eager graph.
export type ConnectDialogModule = typeof import("../settings/sections/credentials/ConnectProviderDialog");

export type ConnectDialogImporter = (retryURL?: string) => Promise<ConnectDialogModule>;

const CONNECT_DIALOG_CHUNK_PATH = /\/ConnectProviderDialog-[A-Za-z0-9_-]+\.js$/;
const CONNECT_DIALOG_STYLESHEET_PATH = /\/ConnectProviderDialog-[A-Za-z0-9_-]+\.css$/;
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

let connectDialogChunkURL: string | null = null;
let connectDialogPreloadLink: LinkDescriptor | null = null;
let connectDialogStylesheetLinks: LinkDescriptor[] = [];
let retrySequence = 0;

let connectDialogImporterForTests: ConnectDialogImporter | null = null;

function connectDialogURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !CONNECT_DIALOG_CHUNK_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
}

function connectDialogURLFromError(error: unknown): string | null {
  const message = error instanceof Error ? error.message : String(error);
  for (const candidate of message.match(URL_IN_ERROR) ?? []) {
    const url = connectDialogURL(candidate);
    if (url !== null) return url;
  }
  return null;
}

function connectDialogStylesheetURL(candidate: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const url = new URL(candidate, window.location.href);
    if (url.origin !== window.location.origin || !CONNECT_DIALOG_STYLESHEET_PATH.test(url.pathname)) return null;
    return url.href;
  } catch {
    return null;
  }
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

function rememberConnectDialogAssets(): void {
  if (typeof document === "undefined") return;

  for (const link of document.querySelectorAll<HTMLLinkElement>(
    'link[rel="modulepreload"][href], link[rel="stylesheet"][href]',
  )) {
    if (link.rel === "modulepreload") {
      const url = connectDialogURL(link.href);
      if (url !== null) {
        connectDialogChunkURL = connectDialogChunkURL ?? url;
        connectDialogPreloadLink = connectDialogPreloadLink ?? describeLink(link);
      }
      continue;
    }

    const url = connectDialogStylesheetURL(link.href);
    if (url === null || connectDialogStylesheetLinks.some((saved) => linkPath(saved.href) === linkPath(url))) continue;
    connectDialogStylesheetLinks.push(describeLink(link));
  }
}

function rememberConnectDialogURL(error: unknown): void {
  connectDialogChunkURL = connectDialogURLFromError(error) ?? connectDialogChunkURL;
  rememberConnectDialogAssets();
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
  url.searchParams.set("evener-dialog-retry", retryToken);
  return url.href;
}

function preloadRetryAssets(retryURL: string): Promise<void> {
  if (typeof document === "undefined") return Promise.resolve();

  const retryToken = new URL(retryURL).searchParams.get("evener-dialog-retry");
  if (retryToken === null) return Promise.resolve();

  const modulepreload = createRetryLink(connectDialogPreloadLink, "modulepreload", retryURL);
  document.head.appendChild(modulepreload);

  const styles = connectDialogStylesheetLinks.map((template) => {
    const link = createRetryLink(template, "stylesheet", retryAssetURL(template.href, retryToken));
    const loaded = new Promise<void>((resolve, reject) => {
      link.addEventListener("load", () => resolve(), { once: true });
      link.addEventListener("error", () => reject(new Error(`Unable to preload ConnectDialog CSS for ${link.href}`)), {
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
export function setConnectDialogImporterForTests(importer: ConnectDialogImporter): void {
  connectDialogImporterForTests = importer;
}

export function resetConnectDialogLoaderForTests(): void {
  connectDialogChunkURL = null;
  connectDialogPreloadLink = null;
  connectDialogStylesheetLinks = [];
  retrySequence = 0;
  connectDialogImporterForTests = null;
}

function rememberError(error: unknown): never {
  rememberConnectDialogURL(error);
  throw error;
}

export function loadConnectDialog(cacheBust = false): Promise<ConnectDialogModule> {
  if (cacheBust && connectDialogChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. Give the JS and every CSS
    // asset Vite exposed for ConnectProviderDialog the same new token, wait
    // for CSS, then evaluate the cache-busted module so a stylesheet failure
    // cannot leave a mounted dialog with an unstyled form.
    const retryURL = new URL(connectDialogChunkURL);
    retryURL.searchParams.set("evener-dialog-retry", String(++retrySequence));
    return preloadRetryAssets(retryURL.href)
      .then(() =>
        connectDialogImporterForTests === null
          ? (import(/* @vite-ignore */ retryURL.href) as Promise<ConnectDialogModule>)
          : connectDialogImporterForTests(retryURL.href),
      )
      .catch(rememberError);
  }

  const loading =
    connectDialogImporterForTests === null
      ? import("../settings/sections/credentials/ConnectProviderDialog")
      : connectDialogImporterForTests();
  rememberConnectDialogAssets();
  return loading.catch(rememberError);
}
