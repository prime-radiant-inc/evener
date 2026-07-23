package main

import (
	"net/http"
)

func (s *WebServer) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if newWebEnabled() {
		serveSPAIndex(w, r, distFS())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": "/_partials/settings/credentials"}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
