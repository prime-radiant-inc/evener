package hub

import (
	"context"
	"net/http"
	"time"

	"primeradiant.com/evener/appwire"
)

// remoteSessionDocumentFetcher is the source capability /doc/file uses to read a
// file in a session that lives on another host (S7).
type remoteSessionDocumentFetcher interface {
	FetchSessionDocument(ctx context.Context, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error)
}

// serveRemoteSessionDocument reads one file of a session that lives on another
// host: the owning host resolves the path inside its own session folder
// (sessionDocumentFromHub), and this hub answers with the local route's shapes
// (writeDocFileRaw). Every failure is a refusal, never a local read.
func (s *WebServer) serveRemoteSessionDocument(w http.ResponseWriter, r *http.Request, ref appwire.Ref, rel string) {
	fetcher, ok := owningSourceAs[remoteSessionDocumentFetcher](s.sources, ref)
	if !ok {
		http.Error(w, "remote host unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteSessionFileBudget)
	defer cancel()
	resp, err := fetcher.FetchSessionDocument(ctx, appwire.SessionDocumentParams{SessionID: ref.ThreadID, Path: rel})
	if err != nil {
		http.Error(w, "remote document unavailable", sessionDocumentProxyStatus(err))
		return
	}
	if !proxyableSessionDocument(resp) {
		http.Error(w, "remote document malformed", http.StatusBadGateway)
		return
	}
	doc := docFileRead{Data: resp.Data, TotalSize: resp.TotalSize, Revision: resp.Revision}
	if resp.ModifiedAt > 0 {
		doc.ModifiedAt = time.UnixMilli(resp.ModifiedAt)
	}
	writeDocFileRaw(w, r, doc)
}

// sessionDocumentProxyStatus maps one host AppWire failure onto the status the
// local route answers for the same case: a path outside the session is 403, a
// missing session or file is 404, and a malformed request is 400. A host built
// before S7 answers MethodNotFound, which is 501 so a client can tell "this
// host can't serve documents yet" from "this file is gone". Every other
// failure (an unattached or unknown host, a transport failure, the host's own
// internal error or conflict) is 503: the document is unavailable right now.
func sessionDocumentProxyStatus(err error) int {
	wire, ok := wireErrorFromError(err)
	if !ok {
		return http.StatusServiceUnavailable
	}
	switch appwire.ErrorInfo(evenerErrorInfoFromData(wire.Data)) {
	case appwire.ErrorPathOutsideSession:
		return http.StatusForbidden
	case appwire.ErrorResourceNotFound:
		return http.StatusNotFound
	}
	switch wire.Code {
	case appwire.CodeMethodNotFound:
		return http.StatusNotImplemented
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest:
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

// proxyableSessionDocument reports whether a host's answer is one the local
// route could have produced: at most docFileMaxBytes, a total size that covers
// them, the whole head when the file was truncated, a revision exactly when
// the file is small enough to hash, and, when the bytes are the whole file, the
// revision those bytes carry. The controller cannot check a truncated file's revision (it has
// only the head), so it checks the form.
func proxyableSessionDocument(resp appwire.SessionDocumentResponse) bool {
	n := int64(len(resp.Data))
	if n > docFileMaxBytes || resp.TotalSize < n {
		return false
	}
	truncated := resp.TotalSize > n
	if truncated && n != docFileMaxBytes {
		return false
	}
	// The host hashes every file up to docRevisionMaxBytes and no larger one.
	if resp.TotalSize > docRevisionMaxBytes {
		return resp.Revision == ""
	}
	if !imageShaRegexp.MatchString(resp.Revision) {
		return false
	}
	return truncated || resp.Revision == imageSha(resp.Data)
}
