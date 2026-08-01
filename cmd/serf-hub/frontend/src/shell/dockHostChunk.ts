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

export function loadDockHost(): Promise<DockHostModule> {
  return import("./DockHost");
}
