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
import { createChunkRetryLoader } from "../../shell/chunkRetry";

export type ConnectDialogModule = typeof import("../settings/sections/credentials/ConnectProviderDialog");

export type ConnectDialogImporter = (retryURL?: string) => Promise<ConnectDialogModule>;

const connectDialogLoader = createChunkRetryLoader<ConnectDialogModule>(
  {
    chunkPath: /\/ConnectProviderDialog-[A-Za-z0-9_-]+\.js$/,
    stylesheetPath: /\/ConnectProviderDialog-[A-Za-z0-9_-]+\.css$/,
    retryParam: "evener-dialog-retry",
    assetLabel: "ConnectDialog",
  },
  () => import("../settings/sections/credentials/ConnectProviderDialog"),
  (retryURL) => import(/* @vite-ignore */ retryURL) as Promise<ConnectDialogModule>,
);

// Test-only seam: it replaces only native module evaluation, leaving the
// retry asset boundary above real and observable in jsdom.
export function setConnectDialogImporterForTests(importer: ConnectDialogImporter): void {
  connectDialogLoader.setImporterForTests(importer);
}

export function resetConnectDialogLoaderForTests(): void {
  connectDialogLoader.resetLoaderForTests();
}

export function isStaleConnectDialogChunkError(error: unknown): boolean {
  return connectDialogLoader.isStaleChunkError(error);
}

export function loadConnectDialog(cacheBust = false): Promise<ConnectDialogModule> {
  return connectDialogLoader.load(cacheBust);
}
