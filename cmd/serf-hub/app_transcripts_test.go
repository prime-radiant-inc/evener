package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestHubRPCTranscriptTargetsUseSerfParentRefs(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "repo")
	parentID := buildRPCParentSession(t, stateDir)
	subID := "01SUBAGENT00000000000001"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:              subID,
		ProfileID:       "openai",
		Model:           "gpt-5",
		EnvInfo:         agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		TurnCount:       1,
		OriginalPrompt:  "inspect parent",
		ParentSessionID: parentID,
		IsSubagent:      true,
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadTranscriptList(context.Background(), appwire.ThreadTranscriptListParams{
		Ref: appwire.Ref{SourceID: "local", ThreadID: parentID}.String(),
	})
	if err != nil {
		t.Fatalf("ThreadTranscriptList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("targets=%+v, want main plus subagent", resp.Data)
	}
	if resp.Data[0].Kind != "main" || resp.Data[0].Ref != "local:"+parentID {
		t.Fatalf("main target=%+v", resp.Data[0])
	}
	if resp.Data[1].Kind != "subagent" || resp.Data[1].Ref != "local:"+subID || resp.Data[1].TurnsUsed != 1 {
		t.Fatalf("subagent target=%+v", resp.Data[1])
	}
}
