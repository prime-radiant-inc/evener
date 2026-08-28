package fuzzharvest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/evener/internal/fuzzroutes"
)

// fuzzReadOnlyRoutes is the canonical, order-significant hub-route allowlist
// shared with FuzzWebHandler (cmd/evener-hub) via internal/fuzzroutes, so the
// harvester's index reverse-mapping can't drift from the target's.
var fuzzReadOnlyRoutes = fuzzroutes.ReadOnly

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
			status, err := r.emit.emitUint8String(r.dir(dirWebHandler), idx, safe)
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
	if isAuthBootstrapPath(reqPath) {
		// /auth has no entry in fuzzReadOnlyRoutes, so without this guard it
		// falls through the longest-prefix match below to the "/" catch-all
		// and a path-carried credential becomes the harvested suffix — gated
		// only by known-secret regexes (entropy-checking is off for this
		// surface; see gateString), none of which matches a bare high-entropy
		// token. Drop it instead: /auth was never a fuzzed route anyway
		// (AuthGuard intercepts it ahead of the SPA shell), so this costs no
		// coverage. See isAuthBootstrapPath's doc comment for what this
		// matches and why. Issue #795.
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

// isAuthBootstrapPath reports whether reqPath is the hub's auth bootstrap
// endpoint: bare /auth or /auth/<token>, matched case-insensitively so a
// manually re-cased path (e.g. /Auth/<token>) is excluded too — see
// scenarioReverseHTTPAuthPathTokenCaseInsensitive. hubedge's real router
// isn't case-insensitive (net/http.ServeMux is byte-exact, so a re-cased
// request just 401s), but redaction stays conservative regardless of whether
// the shape could authenticate today.
//
// hubedge's own recognition of this path is split across two states.
// Today, on main, isAuthExempt (cmd/evener-hub/internal/hubedge/auth_token.go)
// matches only the bare "/auth" case, and HandleAuth reads the token from
// the query string. The not-yet-merged PR #431 adds the /auth/<token> path
// form (HandleAuth's strings.TrimPrefix(r.URL.Path, "/auth/")) and a
// matching "/auth/" mux registration. isAuthBootstrapPath covers both so
// this guard already holds once 431 lands (issue #795's own gating note:
// "#431 must not merge until this lands").
//
// This can't import hubedge to check itself against the original: hubedge
// is internal to cmd/evener-hub, and this package lives outside that
// subtree (Go's internal/ visibility rule), so there is no shared constant
// or function either side can reference — this is a second, hand-maintained
// encoding of the same fact. scenarioIsAuthBootstrapPathShapes pins its
// exact accept/reject contract so a future /auth shape change here fails a
// test instead of silently reopening this issue's leak.
func isAuthBootstrapPath(reqPath string) bool {
	lower := strings.ToLower(reqPath)
	return lower == "/auth" || strings.HasPrefix(lower, "/auth/")
}
