package openai

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/llm"
)

func TestAdapter_E2E_PublicResponsesContinuationDiscovery(t *testing.T) {
	if os.Getenv("SERF_OPENAI_RESPONSES_DISCOVERY_E2E") != "1" {
		t.Skip("set SERF_OPENAI_RESPONSES_DISCOVERY_E2E=1 to run live public OpenAI Responses continuation discovery")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is required for public OpenAI discovery")
	}
	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_RESPONSES_DISCOVERY_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}
	runResponsesContinuationDiscovery(t, &Adapter{APIKey: apiKey}, model, "public_openai", true)
}

func TestAdapter_E2E_CodexResponsesContinuationDiscovery(t *testing.T) {
	if os.Getenv("SERF_OPENAI_CODEX_DISCOVERY_E2E") != "1" {
		t.Skip("set SERF_OPENAI_CODEX_DISCOVERY_E2E=1 to run live Codex Responses continuation discovery")
	}
	if testing.Short() {
		t.Skip("skipping live Codex discovery in short mode")
	}
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if !a.usesCodexBackend() {
		t.Skip("OpenAI env did not resolve to stored OAuth/Codex backend credentials")
	}
	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_CODEX_DISCOVERY_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}
	runResponsesContinuationDiscovery(t, a, model, "codex_backend", false)
}

func runResponsesContinuationDiscovery(t *testing.T, a *Adapter, model, endpointFamily string, requestStore bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	id := ulid.Make().String()
	store := requestStore
	anchor, err := a.Complete(ctx, llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.User("Reply exactly: serf continuation discovery anchor")},
		Store:    &store,
		Metadata: map[string]string{
			"serf_discovery_id": id,
		},
	})
	if err != nil {
		t.Fatalf("%s anchor request failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(anchor.ID) == "" {
		t.Fatalf("%s anchor response id is empty; raw=%#v", endpointFamily, anchor.Raw)
	}

	delta, err := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("Reply exactly: serf continuation discovery delta")},
		PreviousResponseID: anchor.ID,
		Store:              &store,
	})
	if err != nil {
		t.Fatalf("%s valid previous_response_id request failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(delta.Text()) == "" {
		t.Fatalf("%s delta response was empty", endpointFamily)
	}

	branch, err := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("Reply exactly: serf continuation discovery branch")},
		PreviousResponseID: anchor.ID,
		Store:              &store,
	})
	if err != nil {
		t.Fatalf("%s second branch from same previous_response_id failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(branch.Text()) == "" {
		t.Fatalf("%s branch response was empty", endpointFamily)
	}

	_, invalidErr := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("This invalid anchor request should fail clearly.")},
		PreviousResponseID: "resp_serf_invalid_" + id,
		Store:              &store,
	})
	if invalidErr == nil {
		t.Fatalf("%s invalid previous_response_id was accepted; cannot enable continuation without silent-drop design", endpointFamily)
	}

	t.Logf("%s discovery: anchor_id=%q delta_text=%q branch_text=%q invalid_anchor_error=%q",
		endpointFamily,
		anchor.ID,
		strings.TrimSpace(delta.Text()),
		strings.TrimSpace(branch.Text()),
		fmt.Sprint(invalidErr),
	)
}
