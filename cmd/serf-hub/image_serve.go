package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// imageShaRegexp limits paths to lowercase hex sha256 (64 chars). The browser
// only ever sees shas the hub computed, so anything else is a bad request.
var imageShaRegexp = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handleSessionImage streams the image whose sha256 matches the URL fragment
// for a given session. Used by the renderer when replaying a past USER_INPUT
// turn — the replay path strips inline image bytes and references each image
// by sha so SSE replay payloads stay small. The bytes still live in the
// session transcript, so we re-scan to find them.
//
// This is loopback-only and a session-scoped read, so we don't index or
// cache. Each fetch re-scans; the browser caches by URL via standard
// HTTP caching headers.
func (s *WebServer) handleSessionImage(w http.ResponseWriter, r *http.Request, sessionID, sha string) {
	if !imageShaRegexp.MatchString(sha) {
		http.Error(w, "bad sha", http.StatusBadRequest)
		return
	}
	if s.cfg.Past == nil {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.cfg.Past.Find(sessionID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	data, mediaType, ok := findImageInTranscript(transcriptPath, sha)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	// Sha-addressed content: safe to cache aggressively.
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Write(data) //nolint:errcheck
}

// findImageInTranscript scans the transcript for any USER_INPUT turn
// containing an image part with the given sha256. Returns the raw bytes and
// media type. Image bytes can be large; we only buffer the matching one.
func findImageInTranscript(path, wantSha string) ([]byte, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			continue
		}
		if head.Kind != "entry" {
			continue
		}
		var rec replayEntry
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Turn.Kind != "USER_INPUT" {
			continue
		}
		for _, p := range rec.Turn.Message.Content {
			if p.Kind != "image" || p.Image == nil || len(p.Image.Data) == 0 {
				continue
			}
			h := sha256.Sum256(p.Image.Data)
			if hex.EncodeToString(h[:]) == wantSha {
				return p.Image.Data, p.Image.MediaType, true
			}
		}
	}
	return nil, "", false
}

// imageSha returns the lowercase hex sha256 of raw image bytes.
func imageSha(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
