package appsource

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"primeradiant.com/evener/appwire"
)

// remoteImageShaPattern is the shape of a stamped sha-addressed image route's
// sha segment: the lowercase hex sha256 the remote hub computed. A route that
// does not match it is not one of the stamped forms.
var remoteImageShaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// rewriteRemoteImageURL rewrites one image URL a remote hub stamped into the
// host-qualified controller route the browser must fetch it from.
//
// A remote hub stamps its own origin-relative routes into the thread snapshots
// it returns: /s/<session>/images/<sha> for replayed input images and
// /doc/image?session=<session>&path=<rel> for file-backed output images. Both
// are meaningful only against the hub that minted them, so reaching the
// controller's browser unchanged would read this hub's own state for a session
// it does not have (404, or another session's bytes on an id collision). The
// visitor maps each stamped form onto the controller route that proxies to the
// owning host:
//
//   - /s/<session>/images/<sha> -> /s/<host>:<session>/images/<sha>;
//   - /doc/image?session=<session>&path=<rel> ->
//     /doc/image?session=<host>:<session>&path=<rel>.
//
// An image route whose session id already names THIS source
// (`<host>:<session>`) is the controller route this visitor itself writes, so it
// is left untouched and applying the visitor twice is a no-op. Every other
// root-relative URL is blanked: it is either a route of the remote origin or one
// naming another source — "local" above all — and a request to this hub for it
// would resolve against this hub's own state. Scheme URLs and network-path
// references (https:, data:, //host/...) name their own origin and are left
// untouched.
//
// Encoding is the stamping functions' own: url.PathEscape for the route id in
// its path segment and url.QueryEscape for the session and path query values,
// exactly as sessionImageURL and resolveOutputImageFile write the local forms
// (output_images.go).
func rewriteRemoteImageURL(hostID, raw string) string {
	if !isRootRelativeURL(raw) {
		return raw
	}
	route, ok := parseRemoteImageRoute(raw)
	if !ok || hostID == "" {
		return ""
	}
	if ref, err := appwire.ParseRef(route.session); err == nil {
		// The session already names a source. A route this source wrote is the
		// host-qualified controller route this hub serves (leave it, so the
		// visitor is idempotent); any other source's route — "local" above all,
		// whose routes resolve against this hub's own filesystem — is blanked
		// rather than prefixed and never left for the browser.
		if ref.SourceID == hostID {
			return raw
		}
		return ""
	}
	switch {
	case route.rel != "":
		return "/doc/image?session=" + url.QueryEscape(hostID+":"+route.session) + "&path=" + url.QueryEscape(route.rel)
	case remoteImageShaPattern.MatchString(route.sha):
		return "/s/" + url.PathEscape(hostID+":"+route.session) + "/images/" + route.sha
	default:
		return ""
	}
}

// remoteImageRoute is one hub-minted image route parsed out of a URL: the sha
// form carries the sha segment, the file-backed form the session-relative path.
type remoteImageRoute struct {
	session string
	sha     string
	rel     string
}

// parseRemoteImageRoute parses one of the two image routes a hub mints for
// itself, reporting false for every other URL (including a network-path
// reference and any scheme URL).
func parseRemoteImageRoute(raw string) (remoteImageRoute, bool) {
	if !isRootRelativeURL(raw) {
		return remoteImageRoute{}, false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return remoteImageRoute{}, false
	}
	switch {
	case parsed.Path == "/doc/image":
		query := parsed.Query()
		session, rel := query.Get("session"), query.Get("path")
		if session == "" || rel == "" {
			return remoteImageRoute{}, false
		}
		return remoteImageRoute{session: session, rel: rel}, true
	case strings.HasPrefix(parsed.Path, "/s/"):
		session, rest, ok := strings.Cut(strings.TrimPrefix(parsed.Path, "/s/"), "/")
		if !ok || session == "" {
			return remoteImageRoute{}, false
		}
		sha, ok := strings.CutPrefix(rest, "images/")
		if !ok {
			return remoteImageRoute{}, false
		}
		return remoteImageRoute{session: session, sha: sha}, true
	default:
		return remoteImageRoute{}, false
	}
}

// HostQualifiedControllerImageRoute reports whether raw is one of the two
// hub-minted image routes whose session id already names another source — the
// host-qualified controller route this hub serves by proxying to that host, and
// the exact form rewriteRemoteImageURL writes. A neutralization pass that runs
// after the source's own translation uses it to tell such a route from one a
// remote hub minted for itself. A "local:"-sourced route is deliberately NOT
// one: it resolves against this hub's own filesystem, so a remote payload
// naming it must never be left for the browser.
func HostQualifiedControllerImageRoute(raw string) bool {
	route, ok := parseRemoteImageRoute(raw)
	if !ok {
		return false
	}
	ref, err := appwire.ParseRef(route.session)
	return err == nil && ref.SourceID != "local" && ref.ThreadID != ""
}

// isRootRelativeURL reports whether raw resolves against the serving origin
// rather than naming an origin of its own. A network-path reference (//host/...)
// and scheme URLs keep their own origin.
func isRootRelativeURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//")
}

// rewriteThreadImageURLs applies the shared image visitor to every image URL a
// remote hub stamped on a whole thread: Images[].URL and OutputImages[].URL of
// every item of every turn. The visitor is defined once over the
// Thread/Turn/ThreadItem types and applied at every seam that returns or relays
// one of them, so a new carrier inherits the rule instead of being silently
// missed.
func rewriteThreadImageURLs(hostID string, thread *appwire.Thread) {
	for turnIndex := range thread.Turns {
		rewriteTurnImageURLs(hostID, &thread.Turns[turnIndex])
	}
}

// rewriteTurnImageURLs applies the shared image visitor to one turn's items.
func rewriteTurnImageURLs(hostID string, turn *appwire.Turn) {
	for itemIndex := range turn.Items {
		rewriteItemImageURLs(hostID, &turn.Items[itemIndex])
	}
}

// rewriteItemImageURLs applies the shared image visitor to both image fields a
// thread item carries: Images[].URL (replayed user-input images) and
// OutputImages[].URL (tool-result thumbnails).
func rewriteItemImageURLs(hostID string, item *appwire.ThreadItem) {
	for index := range item.Images {
		item.Images[index].URL = rewriteRemoteImageURL(hostID, item.Images[index].URL)
	}
	for index := range item.OutputImages {
		item.OutputImages[index].URL = rewriteRemoteImageURL(hostID, item.OutputImages[index].URL)
	}
}

// rewriteRawThreadImageURLs applies the shared image visitor to a raw Thread.
// It walks the same declared containers and fields as the typed visitor, at the
// JSON level: the notification translator must preserve fields this hub does
// not understand, so it cannot round-trip a whole payload through appwire
// types. A value it cannot decode, and a payload whose image URLs did not
// change, is returned byte-for-byte, so a notification is never reshaped by a
// pass that found nothing to rewrite.
func (s *RemoteHubSource) rewriteRawThreadImageURLs(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	turns, ok := fields["turns"]
	if !ok {
		return raw
	}
	rewritten := s.rewriteRawArray(turns, s.rewriteRawTurnImageURLs)
	if bytes.Equal(rewritten, turns) {
		return raw
	}
	fields["turns"] = rewritten
	return encodeRawObject(raw, fields)
}

// rewriteRawTurnImageURLs applies the shared image visitor to a raw Turn.
func (s *RemoteHubSource) rewriteRawTurnImageURLs(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	items, ok := fields["items"]
	if !ok {
		return raw
	}
	rewritten := s.rewriteRawArray(items, s.rewriteRawItemImageURLs)
	if bytes.Equal(rewritten, items) {
		return raw
	}
	fields["items"] = rewritten
	return encodeRawObject(raw, fields)
}

// rewriteRawItemImageURLs applies the shared image visitor to a raw ThreadItem:
// both image arrays, whenever the payload carries them.
func (s *RemoteHubSource) rewriteRawItemImageURLs(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	changed := false
	for _, key := range []string{"images", "outputImages"} {
		if images, ok := fields[key]; ok {
			rewritten := s.rewriteRawArray(images, s.rewriteRawImageURL)
			if !bytes.Equal(rewritten, images) {
				fields[key] = rewritten
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	return encodeRawObject(raw, fields)
}

// rewriteRawImageURL applies the shared image visitor to one raw image
// descriptor, rewriting its own "url" field.
func (s *RemoteHubSource) rewriteRawImageURL(raw json.RawMessage) json.RawMessage {
	fields, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	urlField, ok := fields["url"]
	if !ok {
		return raw
	}
	var value string
	if err := json.Unmarshal(urlField, &value); err != nil {
		return raw
	}
	rewritten := rewriteRemoteImageURL(s.id, value)
	if rewritten == value {
		return raw
	}
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		return raw
	}
	fields["url"] = encoded
	return encodeRawObject(raw, fields)
}

// rewriteRawArray maps visit over every element of a raw JSON array, returning
// the array it received when no element changed (each element's visitor returns
// its input byte-for-byte when it rewrites nothing) and the original value when
// it is not an array.
func (s *RemoteHubSource) rewriteRawArray(raw json.RawMessage, visit func(json.RawMessage) json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	changed := false
	for index := range items {
		rewritten := visit(items[index])
		if !bytes.Equal(rewritten, items[index]) {
			items[index] = rewritten
			changed = true
		}
	}
	if !changed {
		return raw
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return encoded
}

// rawJSONObject decodes a raw JSON object, reporting false for every other
// JSON value so a walk never rewrites a payload of the wrong shape.
func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// encodeRawObject re-encodes a walked object, falling back to the value it
// walked when the object cannot be encoded.
func encodeRawObject(fallback json.RawMessage, fields map[string]json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return fallback
	}
	return encoded
}
