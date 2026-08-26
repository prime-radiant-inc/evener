# Task 6 report: conditional navigation HTTP resources

## Delivered

- Added authenticated, pre-ServeMux raw navigation dispatch for `/api/navigation` and its resource subtree.
- Added canonical single-decode dynamic path handling; strict route shape, query, `uint32`, pagination, tier, and ref validation.
- Added cached identity/gzip representation delivery with Accept-Encoding q/wildcard negotiation, weak If-None-Match handling, explicit header/content-length matrices, and bodyless 304 responses.
- Added typed navigation 404/503/500 HTTP mapping without path/key disclosure.
- Added injectable key-free HTTP navigation metrics.
- Redacted navigation recorder entries before authentication: route class only, with no query, body, Authorization, Cookie, or other inbound headers.
- Added Handler/raw-path/auth-isolation/conditional-gzip/header/metric tests and recorder redaction coverage.

## Verification

Passed:

```text
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestNavigation.*(HTTP|Route|Conditional|Auth|Redact)' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestNavigation|TestHTTPRequestRecorder' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test -race ./cmd/evener-hub -run 'TestNavigation.*(HTTP|Auth)' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestNavigationService' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestHTTPRecorder|TestNavigation' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod gofmt -d [Task 6 Go paths]
git diff --check
```

The complete `go test ./cmd/evener-hub -count=1` gate was attempted but could not complete in this sandbox. An unrelated existing RPC test panicked while `httptest.NewUnstartedServer` attempted to bind `tcp6 [::1]:0`: `bind: operation not permitted` (`TestHubRPCThreadForkAsideCreatesSideThread`). Focused Task 6 tests and the race gate passed.

## Fix round 1 (R31–R32)

- The authenticated raw-path guard and recorder now recognize any path whose raw or decoded clean candidate reaches `/api/navigation`; dirty dot and doubled-separator paths are handled before ServeMux can redirect them.
- 304 responses now retain cache validators/variation metadata and selected encoding, but deliberately omit `Content-Type` and `Content-Length` to match real net/http behavior.
- Deadline waits are typed as navigation availability (503), both cached encodings are required before either 200 or 304, and the service documentation explicitly requires semantic unversioned keys directly to `Representation`.
- Accept-Encoding and If-None-Match parse every header field line, ignore empty list elements, accept RFC `q=1.`/`q=0.`, and fall back to identity on malformed negotiation.
- Added real Handler/recorder dirty-path tests, an actual TCP4 net/http 304 test, repeated-header, missing-encoding, deadline, bounds, absent-resource, two-server auth, atomic key, and metric-status regressions.

Fix-round focused, recorder, service, race, formatting, and diff-check gates passed. The complete hub suite was retried and remains blocked by the same sandbox IPv6 listener denial in unrelated `TestHubRPCThreadForkAsideCreatesSideThread`.

## Fix round 2

- Restored raw and decoded `/api/navigation` prefix detection in addition to clean-candidate detection, so prefix-adjacent malformed paths are intercepted/redacted before ServeMux or auth can expose their request data.
- Added authenticated and unauthenticated Handler/recorder coverage for `/api/navigation-secret/...` and `/api/navigationevil/...`, including path/query/Authorization/Cookie/body privacy assertions over marshaled records. A `/api/navigatio/...` near miss remains ordinary recorder behavior.
- Added Handler-level query validation/default/max-offset tests; 50-row section, 100-row catalog, project-page remaining, and source-cap integration coverage; expanded exact identity/gzip 200 and 304 headers, malformed negotiation, repeated validators, wildcard, and sequence-absence assertions.
- Added cookie-based two-server isolation, concurrent cached-byte/metric-event assertions that marshal complete events, and source-failure/recovery behavior.

Round-2 commands: focused navigation/recorder, service, and race gates passed after one rerun. The first combined focused run exposed an existing timing-sensitive `TestNavigationCacheConcurrentMissBuildsOnce` expectation (`Coalesced: 0`); its isolated rerun and the complete focused rerun passed. `gofmt -d` and `git diff --check` passed. The full hub suite was attempted and is still blocked by sandbox `tcp6 [::1]:0` listener denial in unrelated `TestHubRPCThreadForkAsideCreatesSideThread`.

## Fix round 3

- Added Handler-level known-location/no-query success and invalid location-query 400 checks with no redirect or identity disclosure.
- Added bounded fixture default-versus-explicit pagination equivalence checks for live, catalog, and project pages; retained exact 50/100/remaining and MaxUint32 empty-page assertions.
- Strengthened the real TCP identity/gzip 200/304 matrix against the cached `NavigationService.Representation`, including exact bytes, all protocol headers, bodyless standard 304 metadata, and sequence absence. Added direct writer cached-byte checks.
- Strengthened cookie auth isolation with distinct sources/services and service/cache counters, and strengthened last-good failure/recovery checks for retained cache bytes, no error validators, no eviction, and recovered cache hits.

Round-3 focused navigation/recorder, service, race, formatting, and diff-check commands passed. The full hub suite was attempted and remains incomplete because sandbox policy denies the unrelated RPC test's `tcp6 [::1]:0` listener.
