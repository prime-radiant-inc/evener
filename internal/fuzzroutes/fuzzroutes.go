// Package fuzzroutes holds the single canonical allowlist of hub HTTP routes the
// handler fuzz drives. FuzzWebHandler (cmd/serf-hub) selects a route by
// routeIdx % len(ReadOnly), and the corpus harvester (cmd/serf-fuzz-harvest)
// reverse-maps recorded request paths to those same indices — so both MUST share
// one ordered list. This package is that single source of truth (it replaces the
// two copies that previously had to be kept "in lockstep" by hand).
package fuzzroutes

// ReadOnly is the ordered allowlist of GET-only, non-mutating, non-networked hub
// routes the handler fuzz exercises. ORDER IS SIGNIFICANT: FuzzWebHandler indexes
// it by routeIdx, and harvested (routeIdx, suffix) seeds address routes by that
// index, so appends are safe but reorders/removals break existing seeds.
var ReadOnly = []string{
	"/",                          // 0
	"/new",                       // 1
	"/assets/",                   // 2  file server — path-escape surface
	"/doc/file",                  // 3  custom file resolver — path-escape surface
	"/manifest.webmanifest",      // 4
	"/_partials/sidebar",         // 5
	"/_partials/workspace/empty", // 6
	"/_partials/workspace/spawn", // 7
	"/_partials/s/",              // 8
	"/_partials/settings",        // 9
	"/s/",                        // 10
	"/thread/",                   // 11
	"/api/tree",                  // 12
	"/api/health",                // 13
	"/api/search",                // 14
	"/api/sessions/",             // 15 GET reads a session detail; POST verbs excluded
	"/settings",                  // 16
}
