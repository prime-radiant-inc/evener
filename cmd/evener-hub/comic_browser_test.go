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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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
	authorRoot := skillGuardStartHelper(t, fixture, 1)
	literal := "COMIC_READ_SOURCE_3f " + strings.Repeat("Literal *asterisks*, `code`, and a source-offset reading point. ", 24)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	roster := hubcore.NewRoster(fixture.runDir, &hubcore.StatusProber{Timeout: 500 * time.Millisecond})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchHubRoster(watchCtx, roster)
	}()
	defer func() { stopWatch(); <-watchDone }()
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
	config := hubcore.WebConfig{AuthToken: token, HubStateRoot: hubStateRoot, RunDir: fixture.runDir, Past: past, Roster: roster, StateDir: fixture.stateRoot, PastIndexPath: filepath.Join(hubStateRoot, "index.db")}
	origin := httptest.NewServer(newWebServer(config, nil).Handler())
	defer origin.Close()
	transport, err := appwire.DialWebSocketWithHeaders(ctx, "ws"+strings.TrimPrefix(origin.URL, "http")+"/rpc", http.DefaultClient, http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatal(err)
	}
	remote := appwire.NewClient(transport)
	defer remote.Close()
	remote.Start(ctx)
	if _, err := remote.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "comic-real-remote", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	// The network connection is already established, as with an attached host.
	// Remote qualification and every response below it use the real hub/daemon.
	config.RemoteHosts = []hostreg.Host{{Name: "comic-remote"}}
	config.RemoteHostClient = func(context.Context, string) (*appwire.Client, error) { return remote, nil }
	config.RemoteHostClientIfAttached = func(host string) (*appwire.Client, bool) { return remote, host == "comic-remote" }
	web := newWebServer(config, nil)
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
		AuthorshipRef       string   `json:"authorshipRef"`
		AuthorshipControl   string   `json:"authorshipControl"`
		AuthorshipLog       string   `json:"authorshipLog"`
	}{BaseURL: hubedge.AuthURLFor(hub.URL, token), EvidenceDir: evidence, ExpectedSourceText: literal,
		RequirePrivateFonts: os.Getenv("COMICGUARD_PRIVATE_FONTS") == "1", ControlPath: fixture.control[0], RequestLog: fixture.requestLog[0],
		AuthorshipRef: skillGuardSessionRef(authorRoot), AuthorshipControl: fixture.control[1], AuthorshipLog: fixture.requestLog[1]}
	for i := range 75 {
		text := fmt.Sprintf("COMIC_HISTORY_%02d literal history", i)
		if i == 27 {
			text += strings.Repeat(" Older source *literal* reading offset. ", 30)
		}
		input.HistoryTexts = append(input.HistoryTexts, text)
	}
	input.Refs.Live = ref
	input.Refs.Remote = "comic-remote:" + entry.SessionID
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
	skillGuardStopHelper(t, 1)
	count, err := skillGuardCountTranscriptInputs(t, fixture.stateDir[0], literal)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable seeded source inputs = %d, want 1", count)
	}
	comicGuardCheckProviderFailure(t, fixture.stateDir[0], evidence)
	comicGuardCheckChildren(t, fixture.stateDir[0], evidence)
	comicGuardCheckRoster(t, fixture.stateDir[0], evidence)
	comicGuardCheckAuthorship(t, fixture.stateDir[1], evidence)
	t.Logf("Comic Chat real reader evidence: %s", evidence)
}

func comicGuardCheckAuthorship(t *testing.T, stateDir, evidence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evidence, "authorship.json"))
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Root        string
		Child, Leaf struct{ Ref, Key string }
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	var childKey []string
	if err := json.Unmarshal([]byte(observed.Child.Key), &childKey); err != nil || len(childKey) != 2 {
		t.Fatalf("invalid child identity %q", observed.Child.Key)
	}
	leafID := strings.TrimPrefix(observed.Leaf.Ref, "local:")
	if leafID == observed.Leaf.Ref || filepath.Base(leafID) != leafID {
		t.Fatalf("invalid authored leaf %q", observed.Leaf.Ref)
	}
	rootAuthor := schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: strings.TrimPrefix(observed.Root, "local:")}
	childAuthor := schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: strings.TrimPrefix(observed.Child.Ref, "local:"), OwnerSessionID: rootAuthor.SessionID, DelegateID: childKey[1], DelegateName: "comic-auth-child"}
	expected := map[string]schema.TranscriptAuthor{
		"COMIC_AUTH_HUMAN literal inherited human":          {Kind: schema.TranscriptAuthorHuman},
		"COMIC_AUTH_ANSWER_ROOT literal original answer":    rootAuthor,
		"COMIC_AUTH_THOUGHT_ROOT literal original thought":  rootAuthor,
		"COMIC_AUTH_ANSWER_CHILD literal original answer":   childAuthor,
		"COMIC_AUTH_THOUGHT_CHILD literal original thought": childAuthor,
		"COMIC_AUTH_UPDATE_CHILD literal original update":   childAuthor,
	}
	data, err = os.ReadFile(filepath.Join(stateDir, "sessions", leafID+".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	communicateCalls := make(map[string]bool)
	communicateResults := make(map[string]int)
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var record struct{ Turn schema.Turn }
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if message := record.Turn.Communicate; message != nil {
			if author, ok := expected[message.Message]; ok {
				if record.Turn.Authorship == nil || !record.Turn.Authorship.Inherited || record.Turn.Authorship.Author != author {
					t.Fatalf("durable communicate author = %+v, want inherited %+v", record.Turn.Authorship, author)
				}
				counts[message.Message]++
			}
		}
		for _, part := range record.Turn.Message.Content {
			text := part.Text
			if part.Thinking != nil {
				text = part.Thinking.Text
			}
			// Transcript-only COMMUNICATE records do not enter fork context.
			// Its completed tool exchange does, and history projects the message
			// using the original call's author, not the tool result's author.
			if call := part.ToolCall; call != nil && call.Name == "communicate" {
				var args struct{ Message string }
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				if _, ok := expected[args.Message]; ok {
					text = args.Message
					communicateCalls[call.ID] = true
				}
			}
			if result := part.ToolResult; result != nil && communicateCalls[result.ToolCallID] && !result.IsError {
				communicateResults[result.ToolCallID]++
			}
			if author, ok := expected[text]; ok {
				if record.Turn.Authorship == nil || !record.Turn.Authorship.Inherited || record.Turn.Authorship.Author != author {
					t.Fatalf("durable original author of %q = %+v, want inherited %+v", text, record.Turn.Authorship, author)
				}
				counts[text]++
			}
		}
	}
	for text := range expected {
		if counts[text] != 1 {
			t.Fatalf("durable inherited %q count=%d, want 1", text, counts[text])
		}
	}
	if len(communicateCalls) != 1 {
		t.Fatalf("inherited communicate calls=%d, want 1", len(communicateCalls))
	}
	for id := range communicateCalls {
		if communicateResults[id] != 1 {
			t.Fatalf("inherited communicate %s successful results=%d, want 1", id, communicateResults[id])
		}
	}
	data, err = json.MarshalIndent(counts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "authorship-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Durable nested fork: human, root/child answer and thought, and child communicate each retain their original author exactly once")
}

func comicGuardCheckRoster(t *testing.T, stateDir, evidence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evidence, "activity-roster.json"))
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Pages []appwire.SessionDelegatesResponse `json:"pages"`
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	refs := make(map[string]bool)
	named := 0
	for _, page := range observed.Pages {
		for _, row := range page.Delegates {
			if row.Task != "COMIC_ROSTER_LEAF" || refs[row.ChildRef] {
				continue
			}
			refs[row.ChildRef] = true
			if row.Name == "comic-same-name" {
				named++
			}
		}
	}
	if len(refs) != 101 || named != 2 {
		t.Fatalf("real roster leaves=%d same-named=%d, want 101/2", len(refs), named)
	}
	for ref := range refs {
		id := strings.TrimPrefix(ref, "local:")
		if id == ref || id == "" || filepath.Base(id) != id {
			t.Fatalf("invalid canonical fixture child ref %q", ref)
		}
		data, err := os.ReadFile(filepath.Join(stateDir, "sessions", id+".transcript.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		inputs, answers := 0, 0
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var record struct{ Turn schema.Turn }
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			for _, part := range record.Turn.Message.Content {
				if record.Turn.Kind == schema.TurnUserInput && part.Text == "COMIC_ROSTER_LEAF" {
					inputs++
				}
				if record.Turn.Kind == schema.TurnAssistant && part.Text == "COMIC_ROSTER_LEAF literal answer" {
					answers++
				}
			}
		}
		if inputs != 1 || answers != 1 {
			t.Fatalf("%s durable roster inputs/answers=%d/%d, want 1/1", ref, inputs, answers)
		}
	}
	t.Log("Durable roster: 101 actual leaf transcripts with one exact input and answer each, two same-named delegates")
}

func comicGuardCheckChildren(t *testing.T, stateDir, evidence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evidence, "children.json"))
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Header     struct{ Ref string } `json:"header"`
		Grandchild struct{ Ref string } `json:"grandchild"`
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]map[string]int)
	for level, ref := range map[string]string{"CHILD": observed.Header.Ref, "GRANDCHILD": observed.Grandchild.Ref} {
		id := strings.TrimPrefix(ref, "local:")
		if id == ref || id == "" || filepath.Base(id) != id {
			t.Fatalf("expected actual local fixture child, got %q", ref)
		}
		data, err := os.ReadFile(filepath.Join(stateDir, "sessions", id+".transcript.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]int)
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
			if record.Turn.Kind != schema.TurnAssistant {
				continue
			}
			for _, part := range record.Turn.Message.Content {
				if part.Thinking != nil && part.Thinking.Text == "COMIC_THOUGHT_"+level+" available literal reasoning" {
					found["thought"]++
				}
				if part.Text == "COMIC_ANSWER_"+level+" literal attributed answer" {
					found["answer"]++
				}
				for index := range 45 {
					text := fmt.Sprintf("COMIC_CHILD_SOURCE_%02d literal child history with *source* characters.", index)
					if index == 5 {
						text += strings.Repeat(" Later-page child text preserves its literal reading offset. ", 30)
					}
					if part.Text == text {
						found[fmt.Sprintf("source_%02d", index)]++
					}
				}
			}
		}
		if found["thought"] != 1 || found["answer"] != 1 {
			t.Fatalf("%s durable thought/answer counts = %v, want exactly one each", ref, found)
		}
		if level == "CHILD" {
			for index := range 45 {
				if found[fmt.Sprintf("source_%02d", index)] != 1 {
					t.Fatalf("%s durable source %02d count = %d, want one", ref, index, found[fmt.Sprintf("source_%02d", index)])
				}
			}
		}
		counts[ref] = found
	}
	data, err = json.MarshalIndent(counts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "children-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Durable child and grandchild: one thought and answer each, 45 exact child source messages")
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
