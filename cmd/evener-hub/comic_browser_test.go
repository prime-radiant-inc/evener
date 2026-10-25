//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// TestComicChatBrowser reads a durable real-daemon turn through the embedded production browser.
func TestComicChatBrowser(t *testing.T) {
	if testing.Short() {
		t.Fatal("browserguard requires Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	requireFreshDist(t)
	fixture := skillGuardSetup(t)
	entry := skillGuardStartHelper(t, fixture, 0)
	ref := skillGuardSessionRef(entry)
	literal := "COMIC_READ_SOURCE_3f " + strings.Repeat("Literal *asterisks*, `code`, and a source-offset reading point. ", 24)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	roster := hubcore.NewRoster(fixture.runDir, &hubcore.StatusProber{Timeout: 500 * time.Millisecond})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(fixture.stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(tokenBytes)
	hubStateRoot := filepath.Join(fixture.root, "hub-state")
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	web := newWebServer(hubcore.WebConfig{AuthToken: token, HubStateRoot: hubStateRoot, RunDir: fixture.runDir, Past: past, Roster: roster, StateDir: fixture.stateRoot, PastIndexPath: filepath.Join(hubStateRoot, "index.db")}, nil)
	hub := httptest.NewServer(web.Handler())
	defer hub.Close()
	evidence := filepath.Join(fixture.artifact, "comic")
	if requested := os.Getenv("COMICGUARD_EVIDENCE_DIR"); requested != "" {
		evidence = requested
	}
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	input := struct {
		BaseURL string `json:"baseURL"`
		Refs    struct {
			Live   string `json:"live"`
			Saved  string `json:"saved,omitempty"`
			Remote string `json:"remote,omitempty"`
		} `json:"refs"`
		EvidenceDir         string   `json:"evidenceDir"`
		RequirePrivateFonts bool     `json:"requirePrivateFonts"`
		ExpectedSourceText  string   `json:"expectedSourceText"`
		HistoryTexts        []string `json:"historyTexts"`
		ControlPath         string   `json:"controlPath"`
		RequestLog          string   `json:"requestLog"`
	}{BaseURL: hubedge.AuthURLFor(hub.URL, token), EvidenceDir: evidence, ExpectedSourceText: literal,
		RequirePrivateFonts: os.Getenv("COMICGUARD_PRIVATE_FONTS") == "1", ControlPath: fixture.control[0], RequestLog: fixture.requestLog[0]}
	for i := range 75 {
		text := fmt.Sprintf("COMIC_HISTORY_%02d literal history", i)
		if i == 27 {
			text += strings.Repeat(" Older source *literal* reading offset. ", 30)
		}
		input.HistoryTexts = append(input.HistoryTexts, text)
	}
	input.Refs.Live = ref
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(fixture.artifact, "comic-fixture.json")
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "node", "scripts/comicguard/run.mjs", "--fixture", inputPath)
	command.Dir = filepath.Join(skillGuardRepoRoot(t), "cmd", "evener-hub", "frontend")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	t.Logf("browser: %s", strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("Comic Chat browser journey: %v; evidence: %s", err, evidence)
	}
	skillGuardStopHelper(t, 0)
	count, err := skillGuardCountTranscriptInputs(t, fixture.stateDir[0], literal)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable seeded source inputs = %d, want 1", count)
	}
	comicGuardCheckProviderFailure(t, fixture.stateDir[0], evidence)
	t.Logf("Comic Chat real reader evidence: %s", evidence)
}

func comicGuardCheckProviderFailure(t *testing.T, stateDir, evidence string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var turns []schema.Turn
	var failedID string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var record struct {
				Turn schema.Turn `json:"turn"`
			}
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			turns = append(turns, record.Turn)
			if record.Turn.Kind == schema.TurnUserInput {
				for _, part := range record.Turn.Message.Content {
					if part.Text == "COMIC_PROVIDER_FAILURE" {
						if failedID != "" {
							t.Fatal("provider failure input recorded more than once")
						}
						failedID = record.Turn.TurnID
					}
				}
			}
		}
	}
	if failedID == "" {
		t.Fatal("missing durable provider failure input")
	}
	failures, completions, assistants := 0, 0, 0
	for _, turn := range turns {
		if turn.TurnID != failedID {
			continue
		}
		switch turn.Kind {
		case schema.TurnAssistant, schema.TurnToolResults, schema.TurnCommunicate:
			assistants++
		case schema.TurnFailure:
			failures++
			if turn.Error == nil || turn.Error.Cause == nil || turn.Error.Cause.Kind != "provider" || turn.Error.Cause.Provider != "openai" {
				t.Fatalf("durable failure has no typed provider cause: %+v", turn.Error)
			}
		case schema.TurnCompletion:
			completions++
			if turn.Completion == nil || turn.Completion.Status != schema.TurnFailed {
				t.Fatalf("durable completion = %+v, want failed", turn.Completion)
			}
		}
	}
	if failures != 1 || completions != 1 || assistants != 0 {
		t.Fatalf("durable failure records=%d completions=%d assistant-content records=%d, want 1/1/0", failures, completions, assistants)
	}
	data, err := json.MarshalIndent(map[string]any{"turnId": failedID, "status": schema.TurnFailed, "causeKind": "provider", "provider": "openai", "failureRecords": failures, "completionRecords": completions, "assistantContentRecords": assistants}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "failed-turn-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Durable %s: typed provider failure, one failed completion, no assistant content (not itemless)", failedID)
}
