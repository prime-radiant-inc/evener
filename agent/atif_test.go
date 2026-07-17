package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/atif"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestSession_ExcludesConfiguredCredentialFromResponseEndpointArtifacts(t *testing.T) {
	const credential = "endpoint-path-credential-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+credential+"/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-endpoint-material",
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{
				"id":"communicate-endpoint-material",
				"type":"function",
				"function":{"name":"communicate","arguments":"{\"message\":\"done\",\"end_turn\":true,\"output\":{\"message\":\"\",\"data\":{},\"artifacts\":[]}}"}
			}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(server.Close)

	adapter := openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:    "credential-path-provider",
		BaseURL: server.URL + "/" + credential,
		APIKey:  credential,
	})
	adapter.Client = server.Client()
	client := llm.NewClient()
	client.Register(completeOnlyEndpointAdapter{ProviderAdapter: adapter})

	stateDir := t.TempDir()
	sess, err := NewSession(
		client,
		testOpenAICompatProfile("credential-path-provider", "test-model", 100_000),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	muteNoteElicitation(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if output, err := sess.ProcessInput(ctx, "reply once", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	} else if output != "done" {
		t.Fatalf("ProcessInput output = %q, want done", output)
	}
	sess.Close()

	transcriptPath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("ReadFile transcript: %v", err)
	}
	assertEndpointSentinelsAbsent(t, "transcript", transcriptBytes, credential)
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	for i, entry := range entries {
		if entry.Turn.ResponseEndpoint != "" {
			t.Fatalf("transcript entry %d response_endpoint = %q, want omitted", i, entry.Turn.ResponseEndpoint)
		}
	}

	outputPath := filepath.Join(stateDir, "trajectory.json")
	if err := exportATIF(transcriptPath, outputPath, ""); err != nil {
		t.Fatalf("exportATIF: %v", err)
	}
	atifBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile ATIF: %v", err)
	}
	assertEndpointSentinelsAbsent(t, "ATIF", atifBytes, credential)
	for i, step := range readExportedATIF(t, outputPath).Steps {
		if endpoint, ok := step.Extra["response_endpoint"]; ok {
			t.Fatalf("ATIF step %d response_endpoint = %#v, want omitted", i, endpoint)
		}
	}
}

type completeOnlyEndpointAdapter struct {
	llm.ProviderAdapter
}

func (completeOnlyEndpointAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestExportATIF_ExcludesCredentialBearingResponseEndpoint(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "sessions", "test-sess.transcript.jsonl")
	const (
		endpointUser     = "endpoint-user-sentinel"
		endpointPassword = "endpoint-password-sentinel"
		endpointQuery    = "endpoint-query-sentinel"
		endpointFragment = "endpoint-fragment-sentinel"
	)
	credentialEndpoint := "https://" + endpointUser + ":" + endpointPassword +
		"@provider.test/v1/responses?credential=" + endpointQuery + "#" + endpointFragment
	wantEndpoint := "https://provider.test/v1/responses"

	resp := llm.Response{Message: llm.Assistant("done")}
	llm.StampEndpointURL(&resp, credentialEndpoint, llm.APILogCredentialMaterial{})
	if got, _ := resp.Raw["endpoint_url"].(string); got != wantEndpoint {
		t.Fatalf("response endpoint metadata = %q, want %q", got, wantEndpoint)
	}
	meta := completeAttemptMetadata(ModelAttemptMetadata{
		EndpointFamily: "openai_public",
	}, resp)

	tw, err := transcript.NewWriter(transcriptPath, transcript.Header{
		SessionID: "test-sess",
		Model:     "gpt-5.3-codex",
		CreatedAt: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.Turn{
		Kind:                   schema.TurnAssistant,
		Message:                resp.Message,
		ResponseEndpointFamily: meta.EndpointFamily,
		ResponseEndpoint:       meta.EndpointURL,
		Timestamp:              time.Date(2026, 7, 16, 0, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("ReadFile transcript: %v", err)
	}
	assertEndpointSentinelsAbsent(t, "transcript", transcriptBytes, endpointUser, endpointPassword, endpointQuery, endpointFragment)
	if !bytes.Contains(transcriptBytes, []byte(`"response_endpoint":"`+wantEndpoint+`"`)) {
		t.Fatalf("transcript omitted sanitized response endpoint: %s", transcriptBytes)
	}

	outputPath := filepath.Join(dir, "output", "trajectory.json")
	if err := exportATIF(transcriptPath, outputPath, ""); err != nil {
		t.Fatalf("exportATIF: %v", err)
	}
	atifBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile ATIF: %v", err)
	}
	assertEndpointSentinelsAbsent(t, "ATIF", atifBytes, endpointUser, endpointPassword, endpointQuery, endpointFragment)
	trajectory := readExportedATIF(t, outputPath)
	if got := trajectory.Steps[0].Extra["response_endpoint"]; got != wantEndpoint {
		t.Fatalf("ATIF response_endpoint = %#v, want %q", got, wantEndpoint)
	}
	if got := trajectory.Steps[0].Extra["response_endpoint_family"]; got != "openai_public" {
		t.Fatalf("ATIF response_endpoint_family = %#v, want openai_public", got)
	}
}

func TestExportATIF_SanitizesTranscriptResponseEndpoints(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "sessions", "test-sess.transcript.jsonl")
	const (
		endpointUser     = "endpoint-user-sentinel"
		endpointPassword = "endpoint-password-sentinel"
		endpointQuery    = "endpoint-query-sentinel"
		endpointFragment = "endpoint-fragment-sentinel"
	)
	credentialEndpoint := "https://" + endpointUser + ":" + endpointPassword +
		"@provider.test/v1/responses?credential=" + endpointQuery + "#" + endpointFragment

	tw, err := transcript.NewWriter(transcriptPath, transcript.Header{
		SessionID: "test-sess",
		Model:     "custom-model",
		CreatedAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for _, endpoint := range []string{credentialEndpoint, "://not-a-valid-endpoint?credential=" + endpointQuery} {
		if err := tw.Append(schema.Turn{
			Kind:             schema.TurnAssistant,
			Message:          llm.Assistant("done"),
			ResponseEndpoint: endpoint,
			Timestamp:        time.Date(2026, 7, 17, 0, 0, 1, 0, time.UTC),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	outputPath := filepath.Join(dir, "output", "trajectory.json")
	if err := exportATIF(transcriptPath, outputPath, ""); err != nil {
		t.Fatalf("exportATIF: %v", err)
	}
	atifBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile ATIF: %v", err)
	}
	assertEndpointSentinelsAbsent(t, "ATIF", atifBytes, endpointUser, endpointPassword, endpointQuery, endpointFragment)

	trajectory := readExportedATIF(t, outputPath)
	if got := trajectory.Steps[0].Extra["response_endpoint"]; got != "https://provider.test/v1/responses" {
		t.Fatalf("ATIF response_endpoint = %#v, want sanitized endpoint", got)
	}
	if _, ok := trajectory.Steps[1].Extra["response_endpoint"]; ok {
		t.Fatalf("ATIF persisted invalid response_endpoint: %#v", trajectory.Steps[1].Extra["response_endpoint"])
	}
}

func assertEndpointSentinelsAbsent(t *testing.T, artifact string, data []byte, sentinels ...string) {
	t.Helper()
	for _, sentinel := range sentinels {
		if bytes.Contains(data, []byte(sentinel)) {
			t.Fatalf("%s contains endpoint credential sentinel %q", artifact, sentinel)
		}
	}
}

func TestExportATIF_WritesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "sessions", "test-sess.transcript.jsonl")

	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	header := transcript.Header{
		SessionID:    "test-sess",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0",
		ProfileID:    "openai",
		CreatedAt:    ts,
	}

	tw, err := transcript.NewWriter(transcriptPath, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	err = tw.Append(schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.User("Hello!"),
		Timestamp: ts,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	outputPath := filepath.Join(dir, "output", "trajectory.json")
	if err := exportATIF(transcriptPath, outputPath, ""); err != nil {
		t.Fatalf("exportATIF: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var traj atif.Trajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if traj.SessionID != "test-sess" {
		t.Errorf("SessionID = %q, want %q", traj.SessionID, "test-sess")
	}
	if traj.SchemaVersion != "ATIF-v1.7" {
		t.Errorf("SchemaVersion = %q, want %q", traj.SchemaVersion, "ATIF-v1.7")
	}
	if len(traj.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	if traj.Steps[0].Source != "user" {
		t.Errorf("Steps[0].Source = %q, want %q", traj.Steps[0].Source, "user")
	}
	if traj.Steps[0].Message != "Hello!" {
		t.Errorf("Steps[0].Message = %q, want %q", traj.Steps[0].Message, "Hello!")
	}
}

func TestExportATIF_ProviderHandleModes(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "sessions", "test-sess.transcript.jsonl")

	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	header := transcript.Header{
		SessionID:    "test-sess",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0",
		ProfileID:    "openai",
		CreatedAt:    ts,
	}

	tw, err := transcript.NewWriter(transcriptPath, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.Turn{
		Kind:           schema.TurnAssistant,
		Message:        llm.Assistant("done"),
		ResponseID:     "resp_raw_phase11",
		ResponseIDHash: "cont-handle-v1:response_id:phase11",
		Timestamp:      ts,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	redactedPath := filepath.Join(dir, "output", "redacted.json")
	if err := exportATIF(transcriptPath, redactedPath, ""); err != nil {
		t.Fatalf("exportATIF redacted: %v", err)
	}
	redacted := readExportedATIF(t, redactedPath)
	if _, ok := redacted.Steps[0].Extra["response_id"]; ok {
		t.Fatalf("redacted export leaked response_id: %#v", redacted.Steps[0].Extra["response_id"])
	}
	if got := redacted.Steps[0].Extra["response_id_hash"]; got != "cont-handle-v1:response_id:phase11" {
		t.Fatalf("redacted response_id_hash = %#v, want hash", got)
	}

	rawPath := filepath.Join(dir, "output", "raw.json")
	if err := exportATIF(transcriptPath, rawPath, "raw-local"); err != nil {
		t.Fatalf("exportATIF raw-local: %v", err)
	}
	raw := readExportedATIF(t, rawPath)
	if got := raw.Steps[0].Extra["response_id"]; got != "resp_raw_phase11" {
		t.Fatalf("raw-local response_id = %#v, want raw response id", got)
	}
	if got := raw.Steps[0].Extra["response_id_hash"]; got != "cont-handle-v1:response_id:phase11" {
		t.Fatalf("raw-local response_id_hash = %#v, want hash", got)
	}
}

func readExportedATIF(t *testing.T, path string) atif.Trajectory {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var traj atif.Trajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return traj
}
