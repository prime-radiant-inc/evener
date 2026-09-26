package hub

import (
	"context"
	"net/http"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

// remoteSessionImageBudget bounds one proxied image read. The host reads a
// bounded amount from its own disk, so anything longer is a stalled channel,
// not a slow read; the request's own context still cancels earlier.
const remoteSessionImageBudget = 30 * time.Second

// remoteSessionImageFetcher is the source capability the two image routes use to
// serve a session that lives on another host. A source that cannot fetch (this
// hub's own local source, an unknown host, a source with no channel) is refused
// typed rather than read locally.
type remoteSessionImageFetcher interface {
	FetchSessionImage(ctx context.Context, params appwire.SessionImageParams) (appwire.SessionImageResponse, error)
}

// sessionImageFetcher resolves the source a host-qualified route id names to the
// capability the image routes call. A missing registry, an unregistered source,
// and a source with no fetch capability are one refusal: these routes can only
// be served from the owning host, never from a local read.
func sessionImageFetcher(sources *appsource.Registry, ref appwire.Ref) (remoteSessionImageFetcher, bool) {
	source, err := sourceForThread(sources, ref.String(), "")
	if err != nil {
		return nil, false
	}
	fetcher, ok := source.(remoteSessionImageFetcher)
	return fetcher, ok
}

// proxyableSessionImage reports whether a host's answer is one of the shapes the
// local image routes serve: non-empty bytes within the image bound whose media
// type is the one those bytes themselves carry. A host is contract-bound to both
// (see sessionImageFromHub), so a violation is refused rather than streamed —
// the proxy must not become a way to serve a size or a content type the local
// routes never do.
func proxyableSessionImage(resp appwire.SessionImageResponse) bool {
	if len(resp.Data) == 0 || int64(len(resp.Data)) > outputImageMaxBytes {
		return false
	}
	mediaType, ok := supportedOutputImageMedia(resp.Data, "")
	return ok && mediaType == resp.MediaType
}

// hostQualifiedImageRef reports whether a route id is the host-qualified form
// `s.id + ":" + <remote session id>` the outbound image translation writes for a
// remote session. A bare (legacy local) id and a "local:" id are not
// host-qualified and resolve against this hub's own state exactly as before.
func hostQualifiedImageRef(id string) (appwire.Ref, bool) {
	ref, err := appwire.ParseRef(id)
	if err != nil || ref.SourceID == "local" {
		return appwire.Ref{}, false
	}
	return ref, true
}

// sessionImageProxyStatus maps one host AppWire failure onto the browser status
// the image proxy's contract pins: a malformed or refused request is a 400, an
// image that does not resolve is a 404, and an unattached/unknown host or a
// transport failure is a 503. Nothing falls back to a local read.
func sessionImageProxyStatus(err error) int {
	wire, ok := wireErrorFromError(err)
	if !ok {
		return http.StatusServiceUnavailable
	}
	if appwire.ErrorInfo(evenerErrorInfoFromData(wire.Data)) == appwire.ErrorResourceNotFound {
		return http.StatusNotFound
	}
	return statusForWireError(wire, http.StatusServiceUnavailable)
}

// serveRemoteSessionImage proxies one image request for a session that lives on
// another host: it resolves the owning source from the route id's source part,
// fetches the bytes through that host's attached-only client, and answers with
// the same shapes the local routes serve. A remote-stamped URL must never reach
// this hub's own filesystem resolution, so every failure here is a refusal —
// never a fall back to a local read.
func (s *WebServer) serveRemoteSessionImage(w http.ResponseWriter, r *http.Request, ref appwire.Ref, params appwire.SessionImageParams) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	fetcher, ok := sessionImageFetcher(s.sources, ref)
	if !ok {
		http.Error(w, "remote host unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteSessionImageBudget)
	defer cancel()
	resp, err := fetcher.FetchSessionImage(ctx, params)
	if err != nil {
		http.Error(w, "remote image unavailable", sessionImageProxyStatus(err))
		return
	}
	if !proxyableSessionImage(resp) {
		http.Error(w, "remote image unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", resp.MediaType)
	if params.SHA != "" {
		// Sha-addressed content: safe to cache aggressively, exactly as the
		// local route answers.
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Header().Set("ETag", `"`+params.SHA+`"`)
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
		w.Header().Set("ETag", `"`+resp.SHA+`"`)
	}
	_, _ = w.Write(resp.Data)
}
