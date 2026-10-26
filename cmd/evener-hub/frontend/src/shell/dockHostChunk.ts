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
import { createChunkRetryLoader } from "./chunkRetry";

export type DockHostModule = typeof import("./DockHost");

export type DockHostImporter = (retryURL?: string) => Promise<DockHostModule>;

const dockHostLoader = createChunkRetryLoader<DockHostModule>(
  {
    chunkPath: /\/DockHost-[A-Za-z0-9_-]+\.js$/,
    stylesheetPath: /\/DockHost-[A-Za-z0-9_-]+\.css$/,
    retryParam: "evener-dock-retry",
    assetLabel: "DockHost",
  },
  () => import("./DockHost"),
  (retryURL) => import(/* @vite-ignore */ retryURL) as Promise<DockHostModule>,
);

// Test-only seam: it replaces only native module evaluation, leaving the
// retry asset boundary above real and observable in jsdom.
export function setDockHostImporterForTests(importer: DockHostImporter): void {
  dockHostLoader.setImporterForTests(importer);
}

export function resetDockHostLoaderForTests(): void {
  dockHostLoader.resetLoaderForTests();
}

export function isStaleDockHostChunkError(error: unknown): boolean {
  return dockHostLoader.isStaleChunkError(error);
}

export function loadDockHost(cacheBust = false): Promise<DockHostModule> {
  return dockHostLoader.load(cacheBust);
}
