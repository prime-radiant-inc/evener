package main

import (
	"net/http"
	"strings"
)

func setDaemonAuthorization(header http.Header, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		header.Del("Authorization")
		return
	}
	header.Set("Authorization", "Bearer "+token)
}
