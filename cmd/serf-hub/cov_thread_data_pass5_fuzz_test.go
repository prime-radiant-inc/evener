package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

type pass5ThreadSource struct {
	*scriptedAppSource
	listErr error
	readErr error
	threads []appwire.Thread
}

func (s *pass5ThreadSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: s.threads}, nil
}

func (s *pass5ThreadSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, params)
}

func pass5Past(t *testing.T) (*hubcore.PastIndex, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-pass5-0123456789")
	id := "02wMz5Txv1C3Hut0M8GCeB"
	meta := schema.SessionMeta{
		ID: id, Name: "Past title", ProfileID: "openai", Model: "gpt-5",
		CreatedAt: time.Unix(1_700_000_000, 0), UpdatedAt: time.Unix(1_700_000_100, 0),
		EnvInfo: schema.EnvironmentInfo{WorkingDir: filepath.Join(root, "work")}, TurnCount: 3,
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return past, stateDir, id
}

func FuzzThreadDataPass5(f *testing.F) {
	for i := uint8(0); i < 6; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		ctx := context.Background()
		past, stateDir, id := pass5Past(t)
		cfg := hubcore.WebConfig{Past: past}

		switch variant % 6 {
		case 0:
			live := appwire.Thread{ID: id, Preview: id, Path: ".", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}
			got := mergePastMetadataForList(cfg, "local", live)
			if got.Name != "Past title" || got.CWD == "" || got.Serf.Ref != "local:"+id {
				t.Fatalf("merged metadata = %+v", got)
			}
			full := appwire.Thread{ID: id, SessionID: "keep", Preview: "keep", Name: "keep", ModelProvider: "keep", Path: "keep", CWD: "keep", Source: "local", Serf: appwire.SerfThread{Ref: "local:" + id, Profile: "keep"}}
			if got := mergePastMetadataForList(cfg, "local", full); got.Name != "keep" {
				t.Fatalf("live metadata overwritten: %+v", got)
			}
			bySession := mergePastMetadataForList(cfg, "local", appwire.Thread{SessionID: id})
			if bySession.ID != id {
				t.Fatalf("session-id merge = %+v", bySession)
			}
			_ = mergePastMetadataForList(hubcore.WebConfig{}, "local", live)
			_ = mergePastMetadataForList(cfg, "remote", live)
			_ = mergePastMetadataForList(cfg, "local", appwire.Thread{ID: "missing"})
			for _, status := range []string{"active", "notloaded", "systemerror", " Custom "} {
				_ = normalizeThreadListStatusFilter(status)
			}

		case 1:
			sessions := filepath.Join(stateDir, "sessions")
			if err := os.WriteFile(filepath.Join(sessions, id+".transcript.jsonl"), []byte("{\"kind\":\"api_call\",\"error\":\"stream failed\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			active := appwire.Thread{ID: id, Source: "local", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}}
			if got := sanitizeStaleProcessingStatus(cfg, active); got.Status.Type != appwire.ThreadStatusSystemError {
				t.Fatalf("status = %q", got.Status.Type)
			}
			for _, thread := range []appwire.Thread{
				{}, {ID: id, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
				{ID: "missing", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
				{ID: id, Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
				{ID: id, Serf: appwire.SerfThread{Ref: "remote:" + id}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
				{ID: id, Serf: appwire.SerfThread{Ref: "not a ref"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
			} {
				_ = sanitizeStaleProcessingStatus(cfg, thread)
			}
			_ = sanitizeStaleProcessingStatus(hubcore.WebConfig{}, active)

		case 2:
			parts := []hubcore.ReplayPart{
				{Kind: string(llm.ContentText), Text: "answer"},
				{Kind: string(llm.ContentThinking), Thinking: &hubcore.ReplayThinking{Text: "think"}},
				{Kind: string(llm.ContentRedThinking)},
				{Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{Data: []byte("image"), MediaType: "image/png", Name: "shot.png"}},
				{Kind: string(llm.ContentAudio), Audio: &hubcore.ReplayMedia{URL: "audio"}},
				{Kind: string(llm.ContentDocument), Document: &hubcore.ReplayMedia{URL: "doc", FileName: "a.txt"}},
				{Kind: string(llm.ContentWebSearch), WebSearch: &hubcore.ReplayWebSearch{Query: "query"}},
				{Kind: string(llm.ContentToolCall), ToolCall: &hubcore.ReplayToolCall{ID: "call", Name: "view", Arguments: json.RawMessage(`{}`)}},
				{Kind: string(llm.ContentToolResult), ToolResult: &hubcore.ReplayToolResult{ToolCallID: "call", Name: "view", Content: "ok", ImageData: []byte("png")}},
			}
			turn := hubcore.ReplayTurn{Kind: string(schema.TurnAssistant), Message: hubcore.ReplayMessage{Role: string(llm.RoleAssistant), Content: parts}}
			items := appItemsFromReplayTurn("id/escaped", "turn", 2, turn, map[string]string{})
			if len(items) == 0 {
				t.Fatal("replay projected no items")
			}
			_ = appItemsFromReplayTurn(id, "empty", 0, hubcore.ReplayTurn{}, nil)
			_ = windowedReadResponse(appwire.Thread{Turns: []appwire.Turn{{ID: "one"}, {ID: "two"}}}, 1)
			for _, params := range []appwire.ThreadReadParams{{}, {Ref: "bad"}, {Ref: "remote:x"}, {ThreadID: id}} {
				_, _ = pastThreadForRead(cfg, params)
			}
			for _, thread := range []appwire.Thread{{}, {Source: "local"}, {Source: "remote"}, {Serf: appwire.SerfThread{Ref: "local:x"}}, {Serf: appwire.SerfThread{Ref: "bad"}}} {
				_ = liveThreadCanMergeLocalPast(thread)
			}

		case 3:
			root := appwire.Thread{ID: id, Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}
			child := appwire.Thread{ID: "child", Source: "remote", Name: "worker", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}, Serf: appwire.SerfThread{Kind: "subagent", ParentRef: "remote:" + id}}
			source := &pass5ThreadSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: root}, threads: []appwire.Thread{root, child, child}}
			registry := appsource.NewRegistry()
			registry.Add(source)
			resp, err := hubThreadTranscriptList(ctx, cfg, registry, appwire.ThreadTranscriptListParams{Ref: "remote:" + id})
			if err != nil || len(resp.Data) != 2 {
				t.Fatalf("transcripts = %+v, %v", resp, err)
			}
			source.listErr = errors.New("list unavailable")
			_, _ = hubThreadTranscriptList(ctx, cfg, registry, appwire.ThreadTranscriptListParams{Ref: "remote:" + id})

		case 4:
			bad := &pass5ThreadSource{scriptedAppSource: &scriptedAppSource{id: "remote"}, readErr: errors.New("read failed")}
			registry := appsource.NewRegistry()
			registry.Add(bad)
			if _, err := hubTranscriptRoot(ctx, cfg, registry, "remote:missing"); err == nil {
				t.Fatal("expected root error")
			}
			if got, err := hubTranscriptRoot(ctx, cfg, registry, "local:"+id); err != nil || got.ID != id {
				t.Fatalf("past root = %+v, %v", got, err)
			}
			_, _ = hubTranscriptRoot(ctx, cfg, registry, "malformed")
			_ = threadRef(appwire.Thread{})
			_ = threadRef(appwire.Thread{ID: id, Source: "local"})
			_ = transcriptTargetSource("bad ref", "fallback")

		case 5:
			live := appwire.Thread{ID: id, Preview: id, Source: "local", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}
			registry := appsource.NewRegistry()
			registry.Add(&pass5ThreadSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: live}, threads: []appwire.Thread{live}})
			resp, err := hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{SearchTerm: "past", Statuses: []string{"idle"}, SourceIDs: []string{"local"}, Limit: 1})
			if err != nil || len(resp.Data) != 1 {
				t.Fatalf("thread list = %+v, %v", resp, err)
			}
			_, _ = hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{SourceIDs: []string{"other"}})
		}
	})
}
