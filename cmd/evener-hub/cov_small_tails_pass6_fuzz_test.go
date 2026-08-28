package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

type pass6TailSource struct {
	*scriptedAppSource
	listErr, actionErr error
	compactCalls       int
}

func (s *pass6TailSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}
func (s *pass6TailSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	s.compactCalls++
	if s.compactCalls == 1 && s.actionErr != nil {
		return s.actionErr
	}
	return nil
}
func (s *pass6TailSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

type pass6BadFS struct{ body string }

func (f pass6BadFS) Open(string) (fs.File, error) {
	if f.body == "missing" {
		return nil, fs.ErrNotExist
	}
	return os.Open(f.body)
}

func FuzzSmallTailsPass6(f *testing.F) {
	for i := range uint8(16) {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		thread := appwire.Thread{ID: "01TAIL", SessionID: "01TAIL", Source: "local", Name: "tail", Evener: appwire.EvenerThread{Ref: "local:01TAIL", Capabilities: appwire.ThreadCapabilities{Clear: true, ChangeModel: true, Compact: true}}}
		source := &pass6TailSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: thread}}
		registry := appsource.NewRegistry()
		registry.Add(source)
		roster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "", WorkingDir: root}},
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01TAIL", WorkingDir: root}, SessionID: "01TAIL"},
		)
		web := NewWebServer(hubcore.WebConfig{Roster: roster, Past: hubcore.NewPastIndex("")})
		web.sources = registry
		call := func(fn func(http.ResponseWriter, *http.Request), method, target, body string) {
			fn(httptest.NewRecorder(), httptest.NewRequest(method, target, strings.NewReader(body)))
		}

		// Both unavailable compact retries and the known-ref early exits.
		source.actionErr = appwire.WireError{Code: appwire.CodeUnavailable, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorSessionUnavailable}}
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "local:01TAIL"})
		source.compactCalls = 0
		source.actionErr = errors.New("plain")
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "local:01TAIL"})
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "remote:missing"})

		// Transcript target rejection, list failure, normalization and de-duplication.
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{})
		source.listErr = errors.New("list")
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{Ref: "local:01TAIL"})
		source.listErr = nil
		source.thread = appwire.Thread{ID: "child", Name: "child", Evener: appwire.EvenerThread{Kind: "subagent", ParentRef: "local:01TAIL"}}
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{Ref: "local:01TAIL"})

		// Replay projection tails: malformed records, empty image bytes and nil tool results.
		entry := hubcore.PastEntry{StateDir: root, Meta: schema.SessionMeta{ID: "01PAST", Model: "gpt-5"}}
		_ = os.MkdirAll(filepath.Join(root, "sessions"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "sessions", "01PAST.transcript.jsonl"), []byte("bad\n"), 0o600)
		_, _ = pastEntryTurns(entry)
		_ = appItemsFromReplayTurn("t", 0, schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{}}}}}, map[string]string{})
		_ = appItemsFromReplayTurn("t", 0, schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult}}}}, map[string]string{})

		// Web construction and all manifest failures, including impossible encoding.
		web.manifestFS = pass6BadFS{body: "missing"}
		call(web.handleManifest, http.MethodGet, "/", "")
		bad := filepath.Join(root, "bad.json")
		_ = os.WriteFile(bad, []byte("{"), 0o600)
		web.manifestFS = pass6BadFS{body: bad}
		call(web.handleManifest, http.MethodGet, "/", "")
		good := filepath.Join(root, "good.json")
		_ = os.WriteFile(good, []byte(`{"start_url":"/"}`), 0o600)
		web.manifestFS = pass6BadFS{body: good}
		oldMarshal := manifestMarshal
		manifestMarshal = func(any) ([]byte, error) { return nil, errors.New("encode") }
		call(web.handleManifest, http.MethodGet, "/", "")
		manifestMarshal = oldMarshal
		_ = web.Handler()

		// Directory result cap and detached-HEAD short-SHA fallback failure.
		for i := range 32 {
			_ = os.Mkdir(filepath.Join(root, string(rune('a'+i))), 0o755)
		}
		oldGit := gitCommand
		calls := 0
		gitCommand = func(context.Context, string, ...string) *exec.Cmd {
			calls++
			if calls == 1 {
				return exec.Command("printf", "HEAD")
			}
			return exec.Command("false")
		}
		_, _ = resolveGitHead(context.Background(), root)
		gitCommand = oldGit

		_ = workspaceDataFromAppThread(appwire.Thread{ID: "x", Source: "local", Preview: "preview", Status: appwire.ThreadStatus{Type: ""}})
		_ = json.RawMessage(nil)
		_ = variant
	})
}
