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
const URL_IN_ERROR = /(?:https?:\/\/|\/)[^\s"'()]+/g;

let connectDialogChunkURL: string | null = null;
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

function rememberConnectDialogURL(error: unknown): void {
  connectDialogChunkURL = connectDialogURLFromError(error) ?? connectDialogChunkURL;
}

// Test-only seam: it replaces only native module evaluation, leaving the
// retry URL boundary above real and observable in jsdom.
export function setConnectDialogImporterForTests(importer: ConnectDialogImporter): void {
  connectDialogImporterForTests = importer;
}

export function resetConnectDialogLoaderForTests(): void {
  connectDialogChunkURL = null;
  retrySequence = 0;
  connectDialogImporterForTests = null;
}

function rememberError(error: unknown): never {
  rememberConnectDialogURL(error);
  throw error;
}

export function loadConnectDialog(cacheBust = false): Promise<ConnectDialogModule> {
  if (cacheBust && connectDialogChunkURL !== null) {
    // Chrome retains a failed module fetch by URL. A same-URL retry never
    // reaches the network stack again - it replays the cached failure - so
    // the retry carries a fresh token the server ignores but the browser's
    // module map treats as a distinct request.
    const retryURL = new URL(connectDialogChunkURL);
    retryURL.searchParams.set("evener-dialog-retry", String(++retrySequence));
    const href = retryURL.href;
    return (
      connectDialogImporterForTests === null
        ? (import(/* @vite-ignore */ href) as Promise<ConnectDialogModule>)
        : connectDialogImporterForTests(href)
    ).catch(rememberError);
  }

  const loading =
    connectDialogImporterForTests === null
      ? import("../settings/sections/credentials/ConnectProviderDialog")
      : connectDialogImporterForTests();
  return loading.catch(rememberError);
}
