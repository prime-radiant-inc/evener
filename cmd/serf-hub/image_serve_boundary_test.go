package main

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

const imageBoundarySessionID = "02wMz5TxvHIJQPOuIBJQct"

type imageBoundaryFixture struct {
	web            *WebServer
	transcriptPath string
	apiLogPath     string
	image          []byte
}

func newImageBoundaryFixture(t *testing.T) imageBoundaryFixture {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects", "image-boundary-0123456789")
	sessionsDir := filepath.Join(project, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID: imageBoundarySessionID, UpdatedAt: time.Now(), OriginalPrompt: "image boundary",
	}); err != nil {
		t.Fatal(err)
	}

	image := []byte("semantic-transcript-image")
	transcriptPath := filepath.Join(sessionsDir, imageBoundarySessionID+".transcript.jsonl")
	w, err := transcript.NewWriter(transcriptPath, transcript.Header{
		SessionID: imageBoundarySessionID, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{
		Kind:  llm.ContentImage,
		Image: &llm.ImageData{Data: image, MediaType: "image/png"},
	}}}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, message)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    idx,
	})
	return imageBoundaryFixture{
		web:            web,
		transcriptPath: transcriptPath,
		apiLogPath:     filepath.Join(sessionsDir, imageBoundarySessionID+".api.jsonl"),
		image:          image,
	}
}

func (f imageBoundaryFixture) getImage(sha string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/s/"+imageBoundarySessionID+"/images/"+sha, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	f.web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestWeb_SessionImage_IgnoresInvalidUnreadableAndSentinelSiblingAPILog(t *testing.T) {
	apiImage := []byte("api-log-only-image-sentinel")
	apiSentinel := []byte(`{"kind":"api_attempt","response":{"body":{"encoding":"base64","data":"` + base64.StdEncoding.EncodeToString(apiImage) + `"}}}` + "\n")
	tests := []struct {
		name     string
		contents []byte
		mode     os.FileMode
	}{
		{name: "invalid", contents: []byte("not-json\n"), mode: 0o600},
		{name: "unreadable", contents: apiSentinel, mode: 0o000},
		{name: "sentinel", contents: apiSentinel, mode: 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newImageBoundaryFixture(t)
			if err := os.WriteFile(fixture.apiLogPath, tt.contents, tt.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(fixture.apiLogPath, 0o600) })

			rec := fixture.getImage(imageSha(fixture.image))
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fixture.image) {
				t.Fatalf("semantic image response = status %d body %q, want 200 and transcript bytes", rec.Code, rec.Body.Bytes())
			}
		})
	}
}

func TestWeb_SessionImage_DoesNotServeImagePresentOnlyInSiblingAPILog(t *testing.T) {
	fixture := newImageBoundaryFixture(t)
	apiImage := []byte("api-log-only-image-sentinel")
	line := `{"kind":"api_attempt","response":{"body":{"encoding":"base64","data":"` + base64.StdEncoding.EncodeToString(apiImage) + `"}}}` + "\n"
	if err := os.WriteFile(fixture.apiLogPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := fixture.getImage(imageSha(apiImage))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("API-only image response = status %d body %q, want 404", rec.Code, rec.Body.String())
	}
}

func TestWeb_SessionImage_RejectsV1AndMixedTranscriptWithoutPartialImage(t *testing.T) {
	tests := []struct {
		name    string
		rewrite func([]byte) []byte
	}{
		{
			name: "v1",
			rewrite: func(data []byte) []byte {
				return bytes.Replace(data, []byte(`"format_version":2`), []byte(`"format_version":1`), 1)
			},
		},
		{
			name: "mixed after matching image",
			rewrite: func(data []byte) []byte {
				return append(data, []byte(`{"kind":"api_call","seq":1}`+"\n")...)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newImageBoundaryFixture(t)
			data, err := os.ReadFile(fixture.transcriptPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.transcriptPath, tt.rewrite(data), 0o600); err != nil {
				t.Fatal(err)
			}

			rec := fixture.getImage(imageSha(fixture.image))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("unsupported transcript response = status %d body %q, want 500", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), transcript.ErrUnsupportedFormat.Error()) {
				t.Fatalf("unsupported transcript response body %q does not expose format error", rec.Body.String())
			}
		})
	}
}

func TestWeb_SessionImage_RejectsUnknownTranscriptFieldsWithoutServingPartialImage(t *testing.T) {
	fixture := newImageBoundaryFixture(t)
	data, err := os.ReadFile(fixture.transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"seq":0`), []byte(`"seq":0,"unknown":true`), 1)
	if err := os.WriteFile(fixture.transcriptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := fixture.getImage(imageSha(fixture.image))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unknown-field transcript response = status %d body %q, want 500", rec.Code, rec.Body.String())
	}
}
