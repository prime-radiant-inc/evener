package hub

import (
	"net/http"
	"strings"
)

// handleSession routes the public /s/<id>[/<sub>] paths. The bare page route
// serves the SPA shell (client routing owns the path); /s/<id>/images/<sha> is
// the sha-addressed image fetch the SPA consumes directly. Every legacy sub-
// route (the /_partials fragments and the /s/<id>/<action> form-POSTs) is gone.
func (s *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/s/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	id = canonicalRouteID(id)
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	switch {
	case sub == "":
		serveSPAIndex(w, r, distFS())
	case strings.HasPrefix(sub, "images/"):
		sha := strings.TrimPrefix(sub, "images/")
		s.handleSessionImage(w, r, id, sha)
	default:
		http.NotFound(w, r)
	}
}

func (s *WebServer) handleThreadDocument(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r, distFS())
}
