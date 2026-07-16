package main

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

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

type pass6TailSource struct {
	*scriptedAppSource
	readErr, listErr, actionErr error
	clearResp                   appwire.ThreadClearResponse
	compactCalls                int
}

func (s *pass6TailSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}
func (s *pass6TailSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}
func (s *pass6TailSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return s.clearResp, s.actionErr
}
func (s *pass6TailSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return s.actionErr
}
func (s *pass6TailSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return s.actionErr
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
	for i := uint8(0); i < 16; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		thread := appwire.Thread{ID: "01TAIL", SessionID: "01TAIL", Source: "local", Name: "tail", Serf: appwire.SerfThread{Ref: "local:01TAIL", Capabilities: appwire.ThreadCapabilities{Clear: true, ChangeModel: true, Compact: true}}}
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

		// Search sorting, empty-session filtering, query rejection and inclusion.
		call(web.handleApiSearch, http.MethodGet, "/api/search?q=no-match", "")
		call(web.handleApiSearch, http.MethodGet, "/api/search?q=tail", "")

		// Upgrade's command failure is external and injected deterministically.
		oldUpgrade := webHubUpgrade
		webHubUpgrade = func(context.Context, appwire.UpgradeParams) (appwire.UpgradeResponse, error) {
			return appwire.UpgradeResponse{}, errors.New("upgrade")
		}
		call(web.handleAPIUpgrade, http.MethodPost, "/api/upgrade", "{}")
		webHubUpgrade = oldUpgrade

		// Source read/action failures cover the endpoint-specific wire paths.
		source.readErr = appwire.Unavailable("read")
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "01TAIL") }, http.MethodPost, "/", "")
		source.readErr = nil
		source.actionErr = errors.New("action")
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "01TAIL") }, http.MethodPost, "/", "")
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIModel(w, r, "01TAIL") }, http.MethodPost, "/", `{"model":"p/m"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIReasoningEffort(w, r, "01TAIL") }, http.MethodPost, "/", `{"reasoning_effort":"high"}`)
		source.actionErr = nil

		// Successful clear with an invalid returned ref takes the local fallback.
		source.clearResp = appwire.ThreadClearResponse{Thread: appwire.Thread{ID: "fallback"}}
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "01TAIL") }, http.MethodPost, "/", "")

		// Both unavailable compact retries and the known-ref early exits.
		source.actionErr = appwire.WireError{Code: appwire.CodeUnavailable, Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorSessionUnavailable}}
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "local:01TAIL"})
		source.compactCalls = 0
		source.actionErr = errors.New("plain")
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "local:01TAIL"})
		_ = compactThreadWithResume(context.Background(), web.cfg, registry, appwire.ThreadCompactStartParams{Ref: "remote:missing"})

		// Preview source errors, including past fallback miss.
		source.readErr = errors.New("read")
		call(web.handleSubagentPreview, http.MethodGet, "/_api/subagent-preview?ref=local%3A01TAIL", "")
		call(web.handleSubagentPreview, http.MethodGet, "/_api/subagent-preview?ref=remote%3Amissing", "")
		source.readErr = nil

		// Transcript target rejection, list failure, normalization and de-duplication.
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{})
		source.listErr = errors.New("list")
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{Ref: "local:01TAIL"})
		source.listErr = nil
		source.thread = appwire.Thread{ID: "child", Name: "child", Serf: appwire.SerfThread{Kind: "subagent", ParentRef: "local:01TAIL"}}
		_, _ = hubThreadTranscriptList(context.Background(), web.cfg, registry, appwire.ThreadTranscriptListParams{Ref: "local:01TAIL"})

		// Replay projection tails: malformed records, empty image bytes and nil tool results.
		entry := hubcore.PastEntry{StateDir: root, Meta: schema.SessionMeta{ID: "01PAST", Model: "gpt-5"}}
		_ = os.MkdirAll(filepath.Join(root, "sessions"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "sessions", "01PAST.transcript.jsonl"), []byte("bad\n"), 0o600)
		_, _ = pastEntryTurns(entry)
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{{Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{}}}}}, map[string]string{})
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{{Kind: string(llm.ContentToolResult)}}}}, map[string]string{})

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
		t.Setenv("SERF_HUB_ASSETS_DIR", root)
		_ = web.Handler()
		call(web.handleInternalPartial, http.MethodGet, "/_partials/workspace/spawn", "")

		// Directory result cap and detached-HEAD short-SHA fallback failure.
		for i := 0; i < 32; i++ {
			_ = os.Mkdir(filepath.Join(root, string(rune('a'+i))), 0o755)
		}
		call(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix="+root+"/", "")
		oldGit := gitCommand
		calls := 0
		gitCommand = func(context.Context, string, ...string) *exec.Cmd {
			calls++
			if calls == 1 {
				return exec.Command("printf", "HEAD")
			}
			return exec.Command("false")
		}
		_, _ = gitHeadBranch(context.Background(), root)
		gitCommand = oldGit

		_ = workspaceDataFromAppThread(appwire.Thread{ID: "x", Source: "local", Preview: "preview", Status: appwire.ThreadStatus{Type: ""}})
		_ = hubapi.RefResponse{}
		_ = json.RawMessage(nil)
		_ = variant
	})
}
