package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

type modelAvailabilityAdapter struct {
	fakeAdapter
	models  []llm.ModelInfo
	calls   atomic.Int32
	observe func(context.Context)
	listErr error
}

func (a *modelAvailabilityAdapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	a.calls.Add(1)
	if a.observe != nil {
		a.observe(ctx)
	}
	if a.listErr != nil {
		return nil, a.listErr
	}
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func TestNewSessionReusesValidatedModelsAndBoundsDelegateSchema(t *testing.T) {
	models := make([]llm.ModelInfo, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%03d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{models: models}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile(models[0].ID), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := adapter.calls.Load(); got != 1 {
		t.Fatalf("selected provider ListModels calls = %d, want one validation fetch reused by startup snapshot", got)
	}
	props := sess.delegateToolDefinition().Parameters["properties"].(map[string]any)
	modelDescription := props["model"].(map[string]any)["description"].(string)
	if got := len([]byte(modelDescription)); got > modelavailability.DefaultInlineMaxBytes {
		t.Fatalf("delegate model description = %d bytes, exceeds %d-byte contract", got, modelavailability.DefaultInlineMaxBytes)
	}
	if !sess.reg.RegisteredNames()["model_list"] {
		t.Fatal("model_list is not registered when the verified choices do not fit in the bounded schema description")
	}
}

func TestNewSessionEnumeratesOtherProvidersUnderLifetimeContext(t *testing.T) {
	type ownerContextKey struct{}
	const ownerMarker = "one-shot-run"

	selected := &modelAvailabilityAdapter{models: []llm.ModelInfo{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	var inheritedOwner atomic.Bool
	other := &modelAvailabilityAdapter{
		models: []llm.ModelInfo{{ID: "claude-opus-4-6"}},
		observe: func(ctx context.Context) {
			inheritedOwner.Store(ctx.Value(ownerContextKey{}) == ownerMarker)
		},
	}
	other.name = "anthropic"
	client := llm.NewClient()
	client.Register(selected)
	client.Register(other)
	owner := context.WithValue(context.Background(), ownerContextKey{}, ownerMarker)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{LifetimeContext: owner})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if !inheritedOwner.Load() {
		t.Fatal("other-provider model enumeration did not inherit the session lifetime context")
	}
}

func TestRestoreSessionReusesSelectedModelsAndAdvertisesSnapshot(t *testing.T) {
	type ownerContextKey struct{}
	const ownerMarker = "restored-run"

	selected := &modelAvailabilityAdapter{models: []llm.ModelInfo{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	var inheritedOwner atomic.Bool
	other := &modelAvailabilityAdapter{
		models: []llm.ModelInfo{{ID: "claude-opus-4-6"}},
		observe: func(ctx context.Context) {
			inheritedOwner.Store(ctx.Value(ownerContextKey{}) == ownerMarker)
		},
	}
	other.name = "anthropic"
	client := llm.NewClient()
	client.Register(selected)
	client.Register(other)
	owner := context.WithValue(context.Background(), ownerContextKey{}, ownerMarker)
	stateDir := t.TempDir()
	meta := schema.SessionMeta{
		ID:        ulid.Make().String(),
		ProfileID: "openai",
		Model:     "gpt-5.5",
		Config:    (SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true}).toSnapshot(),
	}

	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.5"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			LifetimeContext: owner,
			StateDir:        stateDir,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	if got := selected.calls.Load(); got != 1 {
		t.Fatalf("selected provider ListModels calls = %d, want one metadata fetch reused by restored snapshot", got)
	}
	if got := other.calls.Load(); got != 1 {
		t.Fatalf("other provider ListModels calls = %d, want one restored snapshot fetch", got)
	}
	if !inheritedOwner.Load() {
		t.Fatal("other-provider model enumeration did not inherit the restored session lifetime context")
	}
	wantChoices := []string{"anthropic/claude-opus-4-6", "openai/gpt-5.5"}
	if sess.modelSnapshot == nil || !slices.Equal(sess.modelSnapshot.Choices, wantChoices) {
		t.Fatalf("restored model snapshot choices = %#v, want %q", sess.modelSnapshot, wantChoices)
	}
	properties := sess.delegateToolDefinition().Parameters["properties"].(map[string]any)
	modelDescription := properties["model"].(map[string]any)["description"].(string)
	if sess.delegateModelDescription == "" || !strings.HasSuffix(modelDescription, sess.delegateModelDescription) {
		t.Fatal("restored delegate schema does not advertise its captured model snapshot")
	}
}

func TestLeafSessionsSkipModelAvailabilityCapture(t *testing.T) {
	newClient := func() (*llm.Client, *modelAvailabilityAdapter, *modelAvailabilityAdapter) {
		selected := &modelAvailabilityAdapter{models: []llm.ModelInfo{{ID: "gpt-5.5"}}}
		selected.name = "openai"
		other := &modelAvailabilityAdapter{models: []llm.ModelInfo{{ID: "claude-opus-4-6"}}}
		other.name = "anthropic"
		client := llm.NewClient()
		client.Register(selected)
		client.Register(other)
		return client, selected, other
	}
	assertLeaf := func(t *testing.T, sess *Session, selected, other *modelAvailabilityAdapter) {
		t.Helper()
		if got := selected.calls.Load(); got != 1 {
			t.Fatalf("selected provider ListModels calls = %d, want one live metadata fetch", got)
		}
		if got := other.calls.Load(); got != 0 {
			t.Fatalf("other provider ListModels calls = %d, want none for a leaf session", got)
		}
		if sess.modelSnapshot != nil {
			t.Fatalf("leaf model snapshot = %#v, want nil", sess.modelSnapshot)
		}
		if sess.reg.Get("model_list") != nil {
			t.Fatal("leaf registry contains model_list")
		}
		if sess.reg.Get("delegate") != nil {
			t.Fatal("leaf registry contains delegate")
		}
	}

	t.Run("fresh", func(t *testing.T) {
		client, selected, other := newClient()
		cfg := SessionConfig{NoProjectPrompts: true}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 0
		cfg.testOnly = testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		assertLeaf(t, sess, selected, other)
	})

	t.Run("restored", func(t *testing.T) {
		client, selected, other := newClient()
		meta := schema.SessionMeta{
			ID:         ulid.Make().String(),
			ProfileID:  "openai",
			Model:      "gpt-5.5",
			IsSubagent: true,
			Config:     (SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true}).toSnapshot(),
		}
		restoreCfg := RestoreSessionConfig{
			StateDir: t.TempDir(),
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}
		restoreCfg.spawn.depth = 1
		restoreCfg.spawn.parentSessionID = "parent-session"
		restoreCfg.spawn.delegationAllowance = 0
		sess, err := RestoreSessionFromMetaWithConfig(
			client,
			NewOpenAIProfile("gpt-5.5"),
			execenv.NewLocalExecutionEnvironment(t.TempDir()),
			meta,
			restoreCfg,
		)
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		defer sess.Close()
		assertLeaf(t, sess, selected, other)
	})
}

func TestNewSessionSnapshotUsesSharedModelVisibility(t *testing.T) {
	selected := &modelAvailabilityAdapter{models: []llm.ModelInfo{
		{ID: "gpt-5.5"},
		{ID: "text-embedding-3-small"},
	}}
	selected.name = "openai"
	other := &modelAvailabilityAdapter{models: []llm.ModelInfo{
		{ID: "tool-model", CapabilitiesAdvertised: true, SupportsTools: true},
		{ID: "tool-less-model", CapabilitiesAdvertised: true, SupportsTools: false},
	}}
	other.name = "router"
	client := llm.NewClient()
	client.Register(selected)
	client.Register(other)
	client.SetNameToTag(map[string]string{"router": "openrouter"})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	want := []string{"openai/gpt-5.5", "router/tool-model"}
	if sess.modelSnapshot == nil || !slices.Equal(sess.modelSnapshot.Choices, want) {
		t.Fatalf("visible startup choices = %#v, want %q", sess.modelSnapshot, want)
	}
}

func TestModelListToolReturnsEveryChoiceExactlyOnceWithinPageBound(t *testing.T) {
	models := make([]llm.ModelInfo, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%03d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{models: models}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile(models[0].ID), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	registered := sess.reg.Get("model_list")
	if registered == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	var got []string
	cursor := ""
	pages := 0
	for {
		raw, err := registered.Exec(context.Background(), sess.env, map[string]any{
			"cursor": cursor, "max_count": float64(modelavailability.DefaultPageMaxCount), "max_bytes": float64(maxBytes),
		})
		if err != nil {
			t.Fatalf("model_list page %d: %v", pages, err)
		}
		page := raw.(modelavailability.Page)
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatalf("marshal page %d: %v", pages, err)
		}
		if len(encoded) > maxBytes {
			t.Fatalf("page %d = %d bytes, exceeds %d", pages, len(encoded), maxBytes)
		}
		if len(page.Oversized) != 0 {
			t.Fatalf("page %d marked ordinary choices oversized: %v", pages, page.Oversized)
		}
		got = append(got, page.Choices...)
		pages++
		if page.Terminal {
			if page.Next != "" {
				t.Fatalf("terminal page has continuation %q", page.Next)
			}
			break
		}
		if page.Next == "" {
			t.Fatalf("non-terminal page %d has no continuation", pages-1)
		}
		cursor = page.Next
		if pages > len(models) {
			t.Fatal("model_list did not terminate")
		}
	}
	if pages < 3 {
		t.Fatalf("model_list returned %d pages, want first, middle, and terminal pages", pages)
	}
	if !slices.Equal(got, sess.modelSnapshot.Choices) {
		t.Fatalf("paged choices differ from snapshot:\n got %q\nwant %q", got, sess.modelSnapshot.Choices)
	}
}

func TestModelListToolReturnsBoundedEmptyTerminalPage(t *testing.T) {
	adapter := &modelAvailabilityAdapter{listErr: errors.New("enumeration unavailable")}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	registered := sess.reg.Get("model_list")
	if registered == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	raw, err := registered.Exec(context.Background(), sess.env, map[string]any{"max_bytes": float64(maxBytes)})
	if err != nil {
		t.Fatalf("model_list empty page: %v", err)
	}
	page := raw.(modelavailability.Page)
	if !page.Terminal || len(page.Choices) != 0 || page.Next != "" {
		t.Fatalf("empty page = %#v", page)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxBytes {
		t.Fatalf("empty terminal page = %d bytes, exceeds %d", len(encoded), maxBytes)
	}
}
