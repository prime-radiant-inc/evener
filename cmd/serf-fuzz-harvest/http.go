package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// fuzzReadOnlyRoutes MUST stay in lockstep with the allowlist of the same name
// in cmd/serf-hub/web_fuzz_test.go: FuzzWebHandler indexes it by routeIdx, so a
// harvested (routeIdx, suffix) seed only addresses the right route if the order
// here matches the target's. (The list lives in a _test file, unimportable from
// this module, hence the deliberate copy.)
var fuzzReadOnlyRoutes = []string{
	"/",                          // 0
	"/new",                       // 1
	"/assets/",                   // 2
	"/doc/file",                  // 3
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
	"/api/sessions/",             // 15
	"/settings",                  // 16
}

// recordedHTTPRequest mirrors the hub HTTP recorder's JSONL line.
type recordedHTTPRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
}

// harvestHTTP walks hub-http.jsonl files, reverse-maps each replayable GET to a
// (routeIdx, suffix) the FuzzWebHandler target can drive, and emits it. Non-GET
// requests and paths matching no allowlisted route are dropped (the target
// cannot replay them).
func harvestHTTP(r *runner, paths []string) {
	st := r.stat("http")
	for _, path := range paths {
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck
			var req recordedHTTPRequest
			if json.Unmarshal(line, &req) != nil {
				return
			}
			idx, suffix, ok := reverseMapHTTP(req.Method, req.Path, req.Query)
			if !ok {
				return
			}
			st.scanned++
			safe, ok := r.gateString(st, suffix)
			if !ok {
				return
			}
			status, err := r.emit.EmitUint8String(r.dir(dirWebHandler), idx, safe)
			if err != nil {
				r.logf("emit error http: %v", err)
				return
			}
			r.recordEmit(st, status)
		})
	}
}

// reverseMapHTTP maps a recorded request to the fuzz target's (routeIdx, suffix)
// by longest-prefix-matching the path against the allowlist. The /doc/file route
// carries its suffix in the ?path query; other routes carry it as the path
// remainder (URL-decoded so the target's re-escaping reproduces the recording).
func reverseMapHTTP(method, reqPath, rawQuery string) (uint8, string, bool) {
	if method != http.MethodGet {
		return 0, "", false
	}
	best := -1
	for i, base := range fuzzReadOnlyRoutes {
		if strings.HasPrefix(reqPath, base) && (best < 0 || len(base) > len(fuzzReadOnlyRoutes[best])) {
			best = i
		}
	}
	if best < 0 {
		return 0, "", false
	}
	base := fuzzReadOnlyRoutes[best]
	if base == "/doc/file" {
		q, err := url.ParseQuery(rawQuery)
		if err != nil {
			return 0, "", false
		}
		return uint8(best), q.Get("path"), true
	}
	rem := reqPath[len(base):]
	if dec, err := url.PathUnescape(rem); err == nil {
		rem = dec
	}
	return uint8(best), rem, true
}
