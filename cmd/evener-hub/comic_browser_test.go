//go:build browserguard

package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	const literal = "COMIC_READ_SOURCE_3f user-recorded literal *asterisks*"
	if err := appendFileLine(fixture.control[0], map[string]string{"command": "background-start-turn", "phase": literal}); err != nil {
		t.Fatal(err)
	}
	if err := newSkillGuardChoreography(fixture).waitForTranscriptText(0, literal, 30*time.Second); err != nil {
		t.Fatal(err)
	}
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
		EvidenceDir         string `json:"evidenceDir"`
		RequirePrivateFonts bool   `json:"requirePrivateFonts"`
		ExpectedSourceText  string `json:"expectedSourceText"`
	}{BaseURL: hubedge.AuthURLFor(hub.URL, token), EvidenceDir: evidence, ExpectedSourceText: literal}
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
	t.Logf("Comic Chat real reader evidence: %s", evidence)
}
