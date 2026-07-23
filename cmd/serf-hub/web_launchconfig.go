package main

import (
	"net/http"
)

// handleCredentials serves the SPA shell for /credentials; the SPA renders the
// credentials pane over its AppWire/REST data.
func (s *WebServer) handleCredentials(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r, distFS())
}
