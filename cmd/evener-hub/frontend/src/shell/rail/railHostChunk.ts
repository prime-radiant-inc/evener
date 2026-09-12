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
import { createChunkRetryLoader } from "../chunkRetry";

export type RailHostModule = typeof import("./RailHost");

export type RailHostImporter = (retryURL?: string) => Promise<RailHostModule>;

const railHostLoader = createChunkRetryLoader<RailHostModule>(
  {
    chunkPath: /\/RailHost-[A-Za-z0-9_-]+\.js$/,
    stylesheetPath: /\/RailHost-[A-Za-z0-9_-]+\.css$/,
    retryParam: "evener-rail-retry",
    assetLabel: "RailHost",
  },
  () => import("./RailHost"),
  (retryURL) => import(/* @vite-ignore */ retryURL) as Promise<RailHostModule>,
);

// Test-only seam: it replaces only native module evaluation, leaving the
// retry asset boundary above real and observable in jsdom.
export function setRailHostImporterForTests(importer: RailHostImporter): void {
  railHostLoader.setImporterForTests(importer);
}

export function resetRailHostLoaderForTests(): void {
  railHostLoader.resetLoaderForTests();
}

export function isStaleRailHostChunkError(error: unknown): boolean {
  return railHostLoader.isStaleChunkError(error);
}

export function loadRailHost(cacheBust = false): Promise<RailHostModule> {
  return railHostLoader.load(cacheBust);
}
