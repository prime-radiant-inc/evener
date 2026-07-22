// Single-pane layout mode for the /thread/{ref} share-link target (wave 8).
// isSinglePaneRoute is the one locked predicate this seam ships in T1: it is
// true for a /thread/{ref} pathname, and AppShell reads it to strip the shell
// chrome (rail + the search/settings entry points that live in it, floor §2.3:
// #sidebar / #search-dialog / .settings-link absent) and mark the shell root
// with data-single-pane. It is a pure string predicate - no React/browser
// dependency - so any host can call it (AppShell today; a mobile host later)
// without pulling in a router, mirroring routing.ts's own paneToURL/urlToPane.
//
// The FULLER single-pane presentation - suppressing dockview's own tab strip
// and letting the one pane fill the viewport - is wave-8 T6's, built inside
// shell/singlePane/** and keyed off the [data-single-pane] marker AppShell
// applies, so T6 completes it without re-touching this chokepoint.
//
// The /thread/ pattern matches routing.ts's own /thread/([^/]+) branch exactly
// (one non-empty, slash-free ref segment) so "resolves to a single-pane route"
// and "resolves to a session pane" can never disagree about the same path.
export function isSinglePaneRoute(pathname: string): boolean {
  return /^\/thread\/[^/]+$/.test(pathname);
}
