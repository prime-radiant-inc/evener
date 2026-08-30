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
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

type modelAvailabilityAdapter struct {
	fakeAdapter
	models  []registry.Model
	calls   atomic.Int32
	observe func(context.Context)
	listErr error
}

func (a *modelAvailabilityAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	a.calls.Add(1)
	if a.observe != nil {
		a.observe(ctx)
	}
	if a.listErr != nil {
		return nil, a.listErr
	}
	return append([]registry.Model(nil), a.models...), nil
}

func TestNewSessionReusesValidatedModelsAndBoundsDelegateSchema(t *testing.T) {
	models := make([]registry.Model, modelavailability.DefaultInlineMaxCount)
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
		t.Fatalf("selected provider listing calls = %d, want one validation fetch reused by startup snapshot", got)
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

	selected := &modelAvailabilityAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	var inheritedOwner atomic.Bool
	other := &modelAvailabilityAdapter{
		models: []registry.Model{{ID: "claude-opus-4-6"}},
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

	selected := &modelAvailabilityAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	var inheritedOwner atomic.Bool
	other := &modelAvailabilityAdapter{
		models: []registry.Model{{ID: "claude-opus-4-6"}},
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
		t.Fatalf("selected provider listing calls = %d, want one metadata fetch reused by restored snapshot", got)
	}
	if got := other.calls.Load(); got != 1 {
		t.Fatalf("other provider listing calls = %d, want one restored snapshot fetch", got)
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

func TestSessionsWithoutDelegateCapabilitySkipModelAvailabilityCapture(t *testing.T) {
	newClient := func() (*llm.Client, *modelAvailabilityAdapter, *modelAvailabilityAdapter) {
		selected := &modelAvailabilityAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
		selected.name = "openai"
		other := &modelAvailabilityAdapter{models: []registry.Model{{ID: "claude-opus-4-6"}}}
		other.name = "anthropic"
		client := llm.NewClient()
		client.Register(selected)
		client.Register(other)
		return client, selected, other
	}
	assertLeaf := func(t *testing.T, sess *Session, selected, other *modelAvailabilityAdapter) {
		t.Helper()
		if got := selected.calls.Load(); got != 1 {
			t.Fatalf("selected provider listing calls = %d, want one live metadata fetch", got)
		}
		if got := other.calls.Load(); got != 0 {
			t.Fatalf("other provider listing calls = %d, want none for a leaf session", got)
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

	t.Run("fresh restricted by allowed tools", func(t *testing.T) {
		client, selected, other := newClient()
		cfg := SessionConfig{NoProjectPrompts: true}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 1
		cfg.spawn.allowedToolNames = []string{"read_file"}
		cfg.testOnly = testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		assertLeaf(t, sess, selected, other)
	})

	t.Run("restored under tool ceiling", func(t *testing.T) {
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
		restoreCfg.spawn.delegationAllowance = 1
		restoreCfg.spawn.toolNameCeiling = []string{"communicate"}
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

// TestNewSessionSnapshotOmitsInvisibleRows pins the delegate snapshot to the
// client's visibility rule: a row an instance's own listing marks hidden or
// tool-less is never offered as a delegate choice (spec §5). The rule lives in
// Client.Models, so the session must not need a second filter of its own.
func TestNewSessionSnapshotOmitsInvisibleRows(t *testing.T) {
	selected := &modelAvailabilityAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	other := &modelAvailabilityAdapter{models: []registry.Model{
		{ID: "tool-model"},
		{ID: "tool-less-model", Caps: registry.Caps{Tools: new(false)}},
		{ID: "hidden-model", Hidden: true},
	}}
	other.name = "router"
	client := llm.NewClient()
	client.Register(selected)
	client.Register(other)

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

func TestNewSessionBoundedSnapshotRetainsSelectedProvider(t *testing.T) {
	const selectedName = "zz-selected-provider"
	selected := &modelAvailabilityAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	selected.name = selectedName
	client := llm.NewClient()
	client.Register(selected)
	nameToTag := map[string]string{selectedName: "openai"}
	for i := range 16 {
		name := fmt.Sprintf("provider-%02d", i)
		adapter := &modelAvailabilityAdapter{models: []registry.Model{{ID: "model"}}}
		adapter.name = name
		client.Register(adapter)
		nameToTag[name] = "openai"
	}
	client.SetNameToTag(nameToTag)
	profile := provider.WithProviderID(NewOpenAIProfile("gpt-5.5"), selectedName)

	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	want := selectedName + "/gpt-5.5"
	if sess.modelSnapshot == nil || !slices.Contains(sess.modelSnapshot.Choices, want) {
		t.Fatalf("bounded snapshot omitted selected provider choice %q: %#v", want, sess.modelSnapshot)
	}
	if sess.modelSnapshot.Complete {
		t.Fatal("provider-bounded snapshot claimed to be complete")
	}
}

func TestModelListToolReturnsEveryChoiceExactlyOnceWithinPageBound(t *testing.T) {
	models := make([]registry.Model, modelavailability.DefaultInlineMaxCount)
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

	if sess.reg.Get("model_list") == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	var got []string
	cursor := ""
	pages := 0
	for {
		page := executeModelListPage(t, sess, cursor, modelavailability.DefaultPageMaxCount, maxBytes)
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

func TestModelListToolPreservesJSONUnderConfiguredOutputLimit(t *testing.T) {
	models := make([]registry.Model, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%02d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{models: models}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(
		client,
		NewOpenAIProfile(models[0].ID),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"model_list": {MaxChars: 64, MaxLines: 1, Strategy: schema.TruncTail},
		}},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	page := executeModelListPage(t, sess, "", modelavailability.DefaultPageMaxCount, 512)
	if len(page.Choices) == 0 {
		t.Fatal("model_list returned no choices")
	}
	repeated := executeModelListPage(t, sess, "", modelavailability.DefaultPageMaxCount, 512)
	if !slices.Equal(repeated.Choices, page.Choices) || repeated.Next != page.Next || repeated.Terminal != page.Terminal {
		t.Fatalf("repeated model_list page = %#v, want %#v", repeated, page)
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

	if sess.reg.Get("model_list") == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	page := executeModelListPage(t, sess, "", modelavailability.DefaultPageMaxCount, maxBytes)
	if !page.Terminal || len(page.Choices) != 0 || page.Next != "" {
		t.Fatalf("empty page = %#v", page)
	}
}

func executeModelListPage(t *testing.T, sess *Session, cursor string, maxCount, maxBytes int) modelavailability.Page {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"cursor": cursor, "max_count": maxCount, "max_bytes": maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "model-list-page", Name: "model_list", Arguments: args,
	})
	if result.IsError {
		t.Fatalf("model_list: %s", result.Output)
	}
	if result.Truncated {
		t.Fatal("model_list output was generically truncated")
	}
	if len([]byte(result.Output)) > maxBytes {
		t.Fatalf("public model_list output = %d bytes, exceeds %d", len([]byte(result.Output)), maxBytes)
	}
	var page modelavailability.Page
	if err := json.Unmarshal([]byte(result.Output), &page); err != nil {
		t.Fatalf("decode model_list output: %v", err)
	}
	return page
}
