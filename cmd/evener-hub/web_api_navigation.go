package hub

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const navigationPathPrefix = "/api/navigation"

type navigationRouteError struct {
	status int
}

func (e navigationRouteError) Error() string { return "invalid navigation request" }

func navigationBadRequest() error    { return navigationRouteError{status: http.StatusBadRequest} }
func navigationRouteNotFound() error { return navigationRouteError{status: http.StatusNotFound} }

// navigationRawPathGuard runs after authentication but before ServeMux. ServeMux
// otherwise cleans paths and can turn a malformed navigation identity into a
// request for a different resource.
func (s *WebServer) navigationRawPathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isNavigationRequestPath(r) {
			s.handleNavigation(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isNavigationRequestPath(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, navigationPathPrefix) || strings.HasPrefix(r.URL.RawPath, navigationPathPrefix)
}

func (s *WebServer) handleNavigation(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	class := navigationRouteClass(r)
	status := http.StatusInternalServerError
	encoding := "identity"
	conditional := false
	uncompressed, transferred := 0, 0
	defer func() {
		s.recordNavigationMetric(navigationMetricEvent{
			RouteClass: class, Status: status, Encoding: encoding, Conditional: conditional,
			UncompressedBytes: uncompressed, TransferredBytes: transferred,
			DurationNanos: time.Since(started).Nanoseconds(),
		})
	}()

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		status = http.StatusMethodNotAllowed
		return
	}
	key, routeClass, err := parseNavigationRequest(r)
	if routeClass != "" {
		class = routeClass
	}
	if err != nil {
		status = navigationHTTPError(w, err)
		return
	}
	if s.navigation == nil {
		status = navigationHTTPError(w, errors.New("navigation service unavailable"))
		return
	}
	representation, err := s.navigation.Representation(r.Context(), key)
	if err != nil {
		status = navigationHTTPError(w, err)
		return
	}
	encoding = navigationContentEncoding(r.Header.Get("Accept-Encoding"))
	conditional = navigationETagMatches(r.Header.Get("If-None-Match"), representation.ETag)
	uncompressed = len(representation.JSON)
	if encoding == "gzip" {
		transferred = len(representation.Gzip)
	} else {
		transferred = len(representation.JSON)
	}
	status, err = writeNavigationRepresentation(w, r, representation)
	if err != nil {
		// The writer detects invariants before committing headers, so an internal
		// representation failure can still use the normal JSON error response.
		status = navigationHTTPError(w, err)
		transferred = 0
		return
	}
	if status == http.StatusNotModified {
		transferred = 0
	}
}

func navigationHTTPError(w http.ResponseWriter, err error) int {
	var routeErr navigationRouteError
	if errors.As(err, &routeErr) {
		writeAPIError(w, routeErr.status, routeErr.Error())
		return routeErr.status
	}
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		status := statusErr.StatusCode()
		if status == http.StatusNotFound || status == http.StatusServiceUnavailable {
			writeAPIError(w, status, http.StatusText(status))
			return status
		}
	}
	writeAPIError(w, http.StatusInternalServerError, "navigation unavailable")
	return http.StatusInternalServerError
}

// parseNavigationRequest reads EscapedPath rather than URL.Path so every
// dynamic segment is decoded exactly once below.
func parseNavigationRequest(r *http.Request) (navigationResourceKey, string, error) {
	raw, err := navigationEscapedPath(r)
	if err != nil {
		return navigationResourceKey{}, "unknown", err
	}
	if raw == navigationPathPrefix || raw == navigationPathPrefix+"/" {
		if err := navigationNoQuery(r); err != nil {
			return navigationResourceKey{}, "manifest", err
		}
		return navigationResourceKey{Kind: navigationResourceManifest}, "manifest", nil
	}
	if !strings.HasPrefix(raw, navigationPathPrefix+"/") {
		return navigationResourceKey{}, "unknown", navigationRouteNotFound()
	}
	remainder := strings.TrimPrefix(raw, navigationPathPrefix+"/")
	if remainder == "" {
		return navigationResourceKey{}, "manifest", navigationRouteNotFound()
	}
	parts := strings.Split(remainder, "/")
	for _, part := range parts {
		if part == "" {
			return navigationResourceKey{}, "unknown", navigationRouteNotFound()
		}
	}

	switch parts[0] {
	case "sections":
		if len(parts) != 2 {
			return navigationResourceKey{}, "section", navigationRouteNotFound()
		}
		var kind navigationResourceKind
		switch parts[1] {
		case "live":
			kind = navigationResourceLive
		case "needs-you":
			kind = navigationResourceNeedsYou
		default:
			return navigationResourceKey{}, "section", navigationRouteNotFound()
		}
		offset, limit, err := navigationPageQuery(r, maxNavigationSectionRows)
		return navigationResourceKey{Kind: kind, Offset: offset, Limit: limit}, "section", err
	case "pin-sections":
		if len(parts) == 1 {
			offset, limit, err := navigationPageQuery(r, maxNavigationCatalogRows)
			return navigationResourceKey{Kind: navigationResourcePinCatalog, Offset: offset, Limit: limit}, "pin_catalog", err
		}
		if len(parts) != 2 {
			return navigationResourceKey{}, "pin_section", navigationRouteNotFound()
		}
		id, err := navigationIdentity(parts[1], "pin section ID")
		if err != nil {
			return navigationResourceKey{}, "pin_section", err
		}
		offset, limit, err := navigationPageQuery(r, maxNavigationSectionRows)
		return navigationResourceKey{Kind: navigationResourcePinSection, SectionID: id, Offset: offset, Limit: limit}, "pin_section", err
	case "catalogs":
		if len(parts) != 2 {
			return navigationResourceKey{}, "catalog", navigationRouteNotFound()
		}
		kind := navigationResourceKind("")
		switch parts[1] {
		case "projects":
			kind = navigationResourceProjects
		case "archived-projects":
			kind = navigationResourceArchivedProjects
		case "test-runs":
			kind = navigationResourceTestRuns
		default:
			return navigationResourceKey{}, "catalog", navigationRouteNotFound()
		}
		offset, limit, err := navigationPageQuery(r, maxNavigationCatalogRows)
		return navigationResourceKey{Kind: kind, Offset: offset, Limit: limit}, "catalog", err
	case "projects":
		if len(parts) != 2 {
			return navigationResourceKey{}, "project", navigationRouteNotFound()
		}
		key, err := navigationIdentity(parts[1], "project key")
		if err != nil {
			return navigationResourceKey{}, "project", err
		}
		return navigationProjectRequest(r, key)
	case "sessions":
		if len(parts) != 2 {
			return navigationResourceKey{}, "location", navigationRouteNotFound()
		}
		ref, err := navigationIdentity(parts[1], "session ref")
		if err != nil {
			return navigationResourceKey{}, "location", err
		}
		parsed, err := navigationRef(ref)
		if err != nil {
			return navigationResourceKey{}, "location", navigationBadRequest()
		}
		if err := navigationNoQuery(r); err != nil {
			return navigationResourceKey{}, "location", err
		}
		return navigationResourceKey{Kind: navigationResourceLocation, ID: parsed.String()}, "location", nil
	default:
		return navigationResourceKey{}, "unknown", navigationRouteNotFound()
	}
}

func navigationProjectRequest(r *http.Request, projectKey string) (navigationResourceKey, string, error) {
	values, err := navigationQuery(r, map[string]bool{"tier": true, "offset": true, "limit": true})
	if err != nil {
		return navigationResourceKey{}, "project", err
	}
	tier, hasTier := values["tier"]
	if !hasTier {
		if _, ok := values["offset"]; ok {
			return navigationResourceKey{}, "project", navigationBadRequest()
		}
		if _, ok := values["limit"]; ok {
			return navigationResourceKey{}, "project", navigationBadRequest()
		}
		return navigationResourceKey{Kind: navigationResourceProject, ProjectKey: projectKey}, "project", nil
	}
	if tier != "current" && tier != "recent" && tier != "archived" {
		return navigationResourceKey{}, "project_page", navigationBadRequest()
	}
	offset, limit, err := navigationPageValues(values, maxNavigationSectionRows)
	if err != nil {
		return navigationResourceKey{}, "project_page", err
	}
	return navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: projectKey, Tier: tier, Offset: offset, Limit: limit}, "project_page", nil
}

func navigationEscapedPath(r *http.Request) (string, error) {
	if r.URL.RawPath != "" {
		// EscapedPath returns RawPath only when it is a valid escape of Path.
		// Comparing it here rejects a malformed or stale RawPath without another
		// identity decode; navigationIdentity below is the sole PathUnescape.
		if r.URL.EscapedPath() != r.URL.RawPath {
			return "", navigationBadRequest()
		}
		return r.URL.RawPath, nil
	}
	return r.URL.EscapedPath(), nil
}

func navigationIdentity(raw, kind string) (string, error) {
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) || decoded == "" || len(decoded) > maxNavigationIdentityBytes {
		return "", navigationBadRequest()
	}
	if url.PathEscape(decoded) != raw {
		return "", navigationBadRequest()
	}
	if err := validateNavigationIdentity(kind, decoded, false); err != nil {
		return "", navigationBadRequest()
	}
	return decoded, nil
}

func navigationNoQuery(r *http.Request) error {
	_, err := navigationQuery(r, map[string]bool{})
	return err
}

func navigationPageQuery(r *http.Request, maximum uint32) (uint32, uint32, error) {
	values, err := navigationQuery(r, map[string]bool{"offset": true, "limit": true})
	if err != nil {
		return 0, 0, err
	}
	return navigationPageValues(values, maximum)
}

func navigationQuery(r *http.Request, allowed map[string]bool) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, navigationBadRequest()
	}
	out := make(map[string]string, len(values))
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return nil, navigationBadRequest()
		}
		out[key] = entries[0]
	}
	return out, nil
}

func navigationPageValues(values map[string]string, maximum uint32) (uint32, uint32, error) {
	offset := uint32(0)
	if value, ok := values["offset"]; ok {
		parsed, err := navigationUint32(value)
		if err != nil {
			return 0, 0, err
		}
		offset = parsed
	}
	limit := maximum
	if value, ok := values["limit"]; ok {
		parsed, err := navigationUint32(value)
		if err != nil || parsed == 0 || parsed > maximum {
			return 0, 0, navigationBadRequest()
		}
		limit = parsed
	}
	return offset, limit, nil
}

func navigationUint32(value string) (uint32, error) {
	if value == "" {
		return 0, navigationBadRequest()
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, navigationBadRequest()
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed > math.MaxUint32 {
		return 0, navigationBadRequest()
	}
	return uint32(parsed), nil
}

// writeNavigationRepresentation writes cached bytes directly. It performs all
// invariant checks before headers so callers can map a broken representation to
// a 5xx without leaking validators.
func writeNavigationRepresentation(w http.ResponseWriter, r *http.Request, representation navigationRepresentation) (int, error) {
	encoding := navigationContentEncoding(r.Header.Get("Accept-Encoding"))
	body := representation.JSON
	if encoding == "gzip" {
		body = representation.Gzip
	}
	if len(body) == 0 || representation.ETag == "" || representation.Generation == "" {
		return 0, fmt.Errorf("invalid navigation representation")
	}
	status := http.StatusOK
	if navigationETagMatches(r.Header.Get("If-None-Match"), representation.ETag) {
		status = http.StatusNotModified
	}
	headers := w.Header()
	headers.Set("Content-Type", "application/json")
	headers.Set("Cache-Control", "private, no-cache")
	headers.Set("Vary", "Accept-Encoding")
	headers.Set("ETag", representation.ETag)
	headers.Set("X-Evener-Navigation-Generation", representation.Generation)
	headers.Set("X-Evener-Navigation-Revision", strconv.FormatUint(representation.Revision, 10))
	if encoding == "gzip" {
		headers.Set("Content-Encoding", "gzip")
	} else {
		headers.Del("Content-Encoding")
	}
	if status == http.StatusNotModified {
		headers.Set("Content-Length", "0")
		w.WriteHeader(status)
		return status, nil
	}
	headers.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return status, nil
}

func navigationContentEncoding(header string) string {
	if header == "" {
		return "identity"
	}
	gzipQ, wildcardQ := -1.0, -1.0
	gzipSeen := false
	for _, coding := range strings.Split(header, ",") {
		parts := strings.Split(coding, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name == "" {
			return "identity"
		}
		quality := 1.0
		seenQ := false
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" || !strings.EqualFold(strings.TrimSpace(key), "q") || seenQ {
				return "identity"
			}
			parsed, ok := navigationQuality(strings.TrimSpace(value))
			if !ok {
				return "identity"
			}
			quality, seenQ = parsed, true
		}
		switch name {
		case "gzip":
			gzipSeen = true
			if quality > gzipQ {
				gzipQ = quality
			}
		case "*":
			if quality > wildcardQ {
				wildcardQ = quality
			}
		}
	}
	if gzipSeen {
		if gzipQ > 0 {
			return "gzip"
		}
		return "identity"
	}
	if wildcardQ > 0 {
		return "gzip"
	}
	return "identity"
}

func navigationQuality(value string) (float64, bool) {
	if value == "0" || value == "1" {
		parsed, err := strconv.ParseFloat(value, 64)
		return parsed, err == nil
	}
	if len(value) < 3 || value[1] != '.' || (value[0] != '0' && value[0] != '1') || len(value) > 5 {
		return 0, false
	}
	for _, r := range value[2:] {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	if value[0] == '1' && strings.Trim(value[2:], "0") != "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed, err == nil
}

func navigationETagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.HasPrefix(candidate, "W/") {
			candidate = strings.TrimPrefix(candidate, "W/")
		}
		comparison := etag
		if strings.HasPrefix(comparison, "W/") {
			comparison = strings.TrimPrefix(comparison, "W/")
		}
		if candidate != "" && candidate == comparison {
			return true
		}
	}
	return false
}

func navigationRouteClass(r *http.Request) string {
	raw := r.URL.RawPath
	if raw == "" {
		raw = r.URL.EscapedPath()
	}
	if raw == navigationPathPrefix || raw == navigationPathPrefix+"/" {
		return "manifest"
	}
	if !strings.HasPrefix(raw, navigationPathPrefix+"/") {
		return "unknown"
	}
	parts := strings.Split(strings.TrimPrefix(raw, navigationPathPrefix+"/"), "/")
	if len(parts) == 0 {
		return "unknown"
	}
	switch parts[0] {
	case "sections":
		return "section"
	case "pin-sections":
		if len(parts) == 1 {
			return "pin_catalog"
		}
		return "pin_section"
	case "catalogs":
		return "catalog"
	case "projects":
		return "project"
	case "sessions":
		return "location"
	default:
		return "unknown"
	}
}
