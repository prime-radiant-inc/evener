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
	"sync"
	"testing"
	"time"

	sandboxpkg "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// TestComicChatBrowser reads a durable real-daemon turn through the embedded production browser.
func TestComicChatBrowser(t *testing.T) {
	comicGuardBrowser(t, "")
}

// TestComicRemoteNavigationBrowser is a focused diagnostic, not the full Comic gate.
func TestComicRemoteNavigationBrowser(t *testing.T) {
	comicGuardBrowser(t, "remote-navigation")
}

// TestComicRecoveryBrowser qualifies recovery only, not the full Comic gate.
func TestComicRecoveryBrowser(t *testing.T) {
	comicGuardBrowser(t, "recovery")
}

func comicGuardContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		// Leave room to reap Chrome's driver and stop the two seeded daemons
		// plus the two real resume launches after the journey ends.
		deadline = deadline.Add(-backgroundDriverJoinReserve - 4*backgroundHelperStopWait)
		return context.WithDeadline(t.Context(), deadline)
	}
	return context.WithCancel(t.Context())
}

func comicGuardBrowser(t *testing.T, scenario string) {
	t.Helper()
	if testing.Short() {
		t.Fatal("browserguard requires Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxpkg.ResolveNamed("workspace-write", nil, (sandboxpkg.RealProber{}).Probe(), t.TempDir(), nil); err != nil {
		t.Fatalf("Comic browser guard requires an enforcing workspace-write sandbox (Linux: bubblewrap with usable user namespaces): %v", err)
	}
	requireFreshDist(t)
	fixture := skillGuardSetup(t)
	launchBinary := filepath.Join(fixture.root, "evener-launch")
	if scenario == "" || scenario == "recovery" {
		build := exec.CommandContext(t.Context(), "go", "build", "-o", launchBinary, "./cmd/evener")
		build.Dir = skillGuardRepoRoot(t)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build real launch-contract binary: %v\n%s", err, out)
		}
	}
	outside := filepath.Join(fixture.root, "comic-outside-workspace")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	interactionConfig, err := json.Marshal(map[string]string{"OutsideDir": outside})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workDir[0], "comic-interactions.json"), interactionConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := skillGuardStartHelper(t, fixture, 0)
	ref := skillGuardSessionRef(entry)
	authorRef := ""
	if scenario == "" {
		authorRef = skillGuardSessionRef(skillGuardStartHelper(t, fixture, 1))
	}
	var launchMu sync.Mutex
	var launched []*skillGuardHelper
	oldCommand := daemonProcessCommand
	daemonProcessCommand = func(_ string, args, env []string) *exec.Cmd {
		launchMu.Lock()
		defer launchMu.Unlock()
		cmd, helper := skillGuardHelperCommand(t, fixture, 0, fmt.Sprintf("comic-resume-%d", len(launched)+1), args, env)
		launched = append(launched, helper)
		return cmd
	}
	defer func() { daemonProcessCommand = oldCommand }()
	literal := "COMIC_READ_SOURCE_3f " + strings.Repeat("Literal *asterisks*, `code`, and a source-offset reading point. ", 24)
	code := "  COMIC_CODE_FIRST\n\tCOMIC_CODE_TAB  \n\n" + strings.Repeat("    COMIC_CODE_BODY aligned\n", 12) + "  COMIC_CODE_WIDE_" + strings.Repeat("x", 160) + "\n  COMIC_CODE_LAST  "
	cells := []string{"COMIC_TABLE_KEY", "COMIC_TABLE_VALUE", "COMIC_CELL_A", "COMIC_CELL_WORDS " + strings.TrimSpace(strings.Repeat("readable cell words ", 12)), "COMIC_CELL_B", "COMIC_CELL_WIDE_" + strings.Repeat("y", 140)}
	formatted := "## COMIC_FORMATTED_HEADING\n\nCOMIC_FORMATTED_BEFORE 😀 **bold** *emphasis* and `inline`. A   collapsed gap. [safe](https://example.com/comic) [unsafe](javascript:alert%281%29)\n\n> COMIC_FORMATTED_QUOTE\n\n7. COMIC_FORMATTED_LIST\n\n```text\n" + code + "\n```\n\n| " + cells[0] + " | " + cells[1] + " |\n| --- | --- |\n| " + cells[2] + " | " + strings.TrimSpace(cells[3]) + " |\n| " + cells[4] + " | " + cells[5] + " |\n\nCOMIC_FORMATTED_AFTER **settled**."
	ctx, cancel := comicGuardContext(t)
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
	inputs := &hubcore.InputsVersion{}
	config := hubcore.WebConfig{AuthToken: token, HubStateRoot: hubStateRoot, RunDir: fixture.runDir, Past: past, Roster: roster, Inputs: inputs, StateDir: fixture.stateRoot, PastIndexPath: filepath.Join(hubStateRoot, "index.db")}
	config.Spawner = &HubSpawner{EvenerBinary: launchBinary, RunDir: fixture.runDir, NoUserLayer: true}
	originWeb := newWebServer(config, nil)
	origin := httptest.NewServer(originWeb.Handler())
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
	// Match main.go's roster publication wiring. These two hubs share the
	// roster, so both retained navigation projections must observe its changes.
	roster.SetOnChange(func() {
		inputs.Bump()
		originWeb.navigation.Invalidate(navigationChangeHint{})
		web.navigation.Invalidate(navigationChangeHint{})
	})
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchHubRoster(watchCtx, roster)
	}()
	defer func() { stopWatch(); <-watchDone }()
	checkpointTransport, err := appwire.DialWebSocketWithHeaders(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/rpc", http.DefaultClient, http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatal(err)
	}
	checkpointClient := appwire.NewClient(checkpointTransport)
	defer checkpointClient.Close()
	checkpointClient.Start(ctx)
	if _, err := checkpointClient.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "comic-checkpoint", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	checkpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/clear-and-crash-root" {
			cleared, err := checkpointClient.ThreadClear(r.Context(), appwire.ThreadClearParams{Ref: ref, ExpectedInstanceID: entry.SessionID, ClientMutationID: "comic-clear-for-resume"})
			var identity *appwire.DaemonIdentity
			if err == nil {
				launchMu.Lock()
				helper := launched[len(launched)-1]
				launchMu.Unlock()
				var residents appwire.DaemonListResponse
				residents, err = checkpointClient.DaemonList(r.Context(), appwire.DaemonListParams{})
				for _, resident := range residents.Daemons {
					if resident.Identity.PID == helper.cmd.Process.Pid {
						captured := resident.Identity
						identity = &captured
					}
				}
				if err == nil && identity == nil {
					err = fmt.Errorf("owned resumed process missing from actual daemon list")
				}
				if err == nil {
					err = helper.cmd.Process.Kill()
				}
				if err == nil {
					select {
					case <-helper.wait():
					case <-r.Context().Done():
						err = r.Context().Err()
					}
				}
			}
			if err == nil {
				var stopped json.RawMessage
				err = checkpointClient.Request(r.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref, ExpectedDaemon: identity}, &stopped)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Observe the real server's authority and saved projection without
			// changing either the browser connection or its hydration state.
			oldState := web.cfg.ResumeLocks.RecoveryState(entry.SessionID)
			newState := web.cfg.ResumeLocks.RecoveryState(cleared.Thread.ID)
			t.Logf("recovery owner: same registry=%v origin old=%+v", web.cfg.ResumeLocks == originWeb.cfg.ResumeLocks, originWeb.cfg.ResumeLocks.RecoveryState(entry.SessionID))
			saved, found, savedErr := pastThreadItemReadResponse(r.Context(), web.cfg, appwire.ThreadReadParams{Ref: ref})
			t.Logf("after exited acknowledgement: old=%+v new=%+v saved found=%v err=%v resumeRequired=%v foldable=%v", oldState, newState, found, savedErr, saved.Thread.Evener.ResumeRequired, saved.Thread.Evener.ResumeOnlyFoldable)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"cleared": cleared, "exitedDaemon": identity, "checkpointAt": time.Now().UnixMilli(), "oldRecovery": oldState, "newRecovery": newState, "savedFound": found, "savedResumeRequired": saved.Thread.Evener.ResumeRequired, "savedResumeOnlyFoldable": saved.Thread.Evener.ResumeOnlyFoldable})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/stop-root" {
			http.NotFound(w, r)
			return
		}
		skillGuardStopHelper(t, 0)
		if err := roster.RefreshAndWait(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := past.Rebuild(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		web.navigation.Invalidate(navigationChangeHint{})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer checkpoint.Close()
	evidence := filepath.Join(fixture.artifact, "comic")
	if requested := os.Getenv("COMICGUARD_EVIDENCE_DIR"); requested != "" {
		evidence = requested
	}
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	input := struct {
		Scenario string `json:"scenario,omitempty"`
		BaseURL  string `json:"baseURL"`
		Refs     struct {
			Live   string `json:"live"`
			Saved  string `json:"saved,omitempty"`
			Remote string `json:"remote,omitempty"`
		} `json:"refs"`
		EvidenceDir        string   `json:"evidenceDir"`
		RequireFonts       bool     `json:"requireFonts"`
		ExpectedSourceText string   `json:"expectedSourceText"`
		FormattedSource    string   `json:"formattedSource"`
		FormattedCode      string   `json:"formattedCode"`
		FormattedCells     []string `json:"formattedCells"`
		HistoryTexts       []string `json:"historyTexts"`
		ControlPath        string   `json:"controlPath"`
		RequestLog         string   `json:"requestLog"`
		AuthorshipRef      string   `json:"authorshipRef"`
		AuthorshipControl  string   `json:"authorshipControl"`
		AuthorshipLog      string   `json:"authorshipLog"`
		CheckpointURL      string   `json:"checkpointURL"`
	}{BaseURL: hubedge.AuthURLFor(hub.URL, token), EvidenceDir: evidence, ExpectedSourceText: literal,
		RequireFonts: os.Getenv("EVENER_COMIC_BLOCK_FONTS") != "1", ControlPath: fixture.control[0], RequestLog: fixture.requestLog[0],
		Scenario: scenario, AuthorshipRef: authorRef, AuthorshipControl: fixture.control[1], AuthorshipLog: fixture.requestLog[1], CheckpointURL: checkpoint.URL,
		FormattedSource: formatted, FormattedCode: code, FormattedCells: cells}
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
	t.Cleanup(func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove authentication-bearing Comic fixture: %v", err)
		}
	})
	command := exec.CommandContext(ctx, "node", "scripts/comicguard/run.mjs", "--fixture", inputPath)
	configureBackgroundDriverCommand(command)
	command.Dir = filepath.Join(skillGuardRepoRoot(t), "cmd", "evener-hub", "frontend")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	t.Logf("browser: %s", strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("Comic Chat browser journey: %v; evidence: %s", err, evidence)
	}
	skillGuardStopHelper(t, 0)
	if scenario == "remote-navigation" {
		comicGuardCheckRoster(t, fixture.stateDir[0], evidence)
		t.Logf("Focused Comic remote navigation only, not the full gate: %s", evidence)
		return
	}
	if scenario == "" {
		skillGuardStopHelper(t, 1)
	}
	launchMu.Lock()
	if len(launched) != 2 {
		t.Errorf("real HubSpawner launches = %d, want 2", len(launched))
	}
	for _, helper := range launched {
		if err := helper.stop(0); err != nil {
			t.Error(err)
		}
	}
	launchMu.Unlock()
	comicGuardCheckRecovery(t, fixture.stateDir[0], literal)
	if scenario == "recovery" {
		t.Logf("Focused Comic recovery: two real HubSpawner launches, owned helpers stopped; %s", evidence)
		return
	}
	comicGuardCheckProviderFailure(t, fixture.stateDir[0], evidence)
	comicGuardCheckFormatted(t, fixture.stateDir[0], entry.SessionID, formatted, evidence)
	comicGuardCheckChildren(t, fixture.stateDir[0], evidence)
	comicGuardCheckRoster(t, fixture.stateDir[0], evidence)
	comicGuardCheckAuthorship(t, fixture.stateDir[1], evidence)
	comicGuardCheckComposer(t, fixture.stateDir[0], entry.SessionID, evidence)
	comicGuardCheckInteractions(t, fixture.stateDir[0], entry.SessionID, outside, evidence)
	t.Logf("Comic Chat real reader evidence: %s", evidence)
}

func comicGuardCheckRecovery(t *testing.T, stateDir, literal string) {
	t.Helper()
	count, err := skillGuardCountTranscriptInputs(t, stateDir, literal)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable seeded source inputs = %d, want 1", count)
	}
	for _, marker := range []string{"COMIC_RECOVERY_before", "COMIC_RECOVERY_after", "COMIC_RESUME_SAME", "COMIC_RESUME_CHANGED"} {
		count, err := skillGuardCountTranscriptInputs(t, stateDir, marker)
		if err != nil || count != 1 {
			t.Fatalf("durable recovered input %s count = %d, want 1: %v", marker, count, err)
		}
	}
	t.Log("Durable recovery: exactly one literal seed and one input each for COMIC_RECOVERY_before, COMIC_RECOVERY_after, COMIC_RESUME_SAME, COMIC_RESUME_CHANGED")
}

func comicGuardCheckFormatted(t *testing.T, stateDir, sessionID, source, evidence string) {
	t.Helper()
	count, err := skillGuardCountTranscriptInputs(t, stateDir, source)
	if err != nil || count != 1 {
		t.Fatalf("durable formatted source count = %d, want 1: %v", count, err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := "COMIC_STREAM_BEFORE **bold**\n\n```text\n  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  \n```\n\n| key | value |\n| --- | --- |\n| COMIC_STREAM_CELL | **settled** |\n\nCOMIC_STREAM_AFTER *done*"
	answers := 0
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var record struct{ Turn schema.Turn }
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		for _, part := range record.Turn.Message.Content {
			if record.Turn.Kind == schema.TurnAssistant && part.Text == want {
				answers++
			}
		}
	}
	if answers != 1 {
		t.Fatalf("durable formatted stream answers = %d, want 1", answers)
	}
	data, err = json.MarshalIndent(map[string]any{"source": source, "stream": want, "inputs": count, "answers": answers}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "formatted-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Durable formatted Markdown: one untouched original source and one complete streamed answer")
}

func comicGuardCheckInteractions(t *testing.T, stateDir, sessionID, outside, evidence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	calls, results := map[string]int{}, map[string]int{}
	decisions := map[string][]schema.ApprovalDecisionNotice{}
	answers := 0
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var record struct{ Turn schema.Turn }
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if notice := record.Turn.Notice; notice != nil && notice.ApprovalDecision != nil {
			decision := *notice.ApprovalDecision
			decisions[decision.DeniedPath] = append(decisions[decision.DeniedPath], decision)
		}
		for _, part := range record.Turn.Message.Content {
			if record.Turn.Kind == schema.TurnUserInput && strings.Contains(part.Text, "COMIC_SINGLE_B") && strings.Contains(part.Text, "COMIC_MULTI_A") && strings.Contains(part.Text, "COMIC_MULTI_B") {
				answers++
			}
			if call := part.ToolCall; call != nil && strings.HasPrefix(call.ID, "comic_interaction_") {
				calls[call.ID]++
			}
			if result := part.ToolResult; result != nil && strings.HasPrefix(result.ToolCallID, "comic_interaction_") {
				results[result.ToolCallID]++
			}
		}
	}
	if answers != 1 {
		t.Fatalf("durable human answer count = %d, want 1", answers)
	}
	for _, marker := range []string{"ASK", "APPROVE", "DENY", "LATE"} {
		id := "comic_interaction_COMIC_INTERACTION_" + marker
		if calls[id] != 1 || results[id] != 1 {
			t.Fatalf("durable interaction %s calls/results=%d/%d, want 1/1", marker, calls[id], results[id])
		}
		if marker == "ASK" {
			continue
		}
		path := filepath.Join(outside, "COMIC_INTERACTION_"+marker)
		if got := decisions[path]; len(got) != 1 || got[0].Approved != (marker != "DENY") || got[0].Tool != "write_file" || got[0].EscalationID == "" {
			t.Fatalf("durable decision %s = %+v", marker, got)
		}
		body, err := os.ReadFile(path)
		if marker == "DENY" {
			if !os.IsNotExist(err) {
				t.Fatalf("declined write executed: %q, %v", body, err)
			}
		} else if err != nil || string(body) != "COMIC_INTERACTION_"+marker {
			t.Fatalf("approved write %s: %q, %v", marker, body, err)
		}
	}
	data, err = json.MarshalIndent(map[string]any{"answers": answers, "calls": calls, "results": results, "decisions": decisions, "approvedFiles": []string{"APPROVE", "LATE"}, "deniedFileAbsent": true}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "interactions-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Durable root interactions: one ask and three sandbox tool exchanges, two approved writes, declined write absent")
}

func comicGuardCheckComposer(t *testing.T, stateDir, sessionID, evidence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{}
	counts := map[string]int{}
	completions := map[string]schema.TurnCompletionStatus{}
	answerTurn := ""
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var record struct{ Turn schema.Turn }
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		turn := record.Turn
		if turn.Completion != nil {
			completions[turn.TurnID] = turn.Completion.Status
		}
		for _, part := range turn.Message.Content {
			if turn.Kind == schema.TurnUserInput && (part.Text == "/settings COMIC_COMPOSER_SEND literal input" || part.Text == "COMIC_COMPOSER_QUEUE literal queued input") {
				inputs[part.Text] = turn.TurnID
				counts[part.Text]++
			}
			if turn.Kind == schema.TurnAssistant && part.Text == "COMIC_COMPOSER_REPLY streamed first and second" {
				counts[part.Text]++
				answerTurn = turn.TurnID
			}
		}
	}
	sent, queued := inputs["/settings COMIC_COMPOSER_SEND literal input"], inputs["COMIC_COMPOSER_QUEUE literal queued input"]
	if sent == "" || queued == "" || sent == queued || answerTurn != sent || completions[sent] != schema.TurnCompleted || completions[queued] != schema.TurnInterrupted {
		t.Fatalf("composer durable turns: sent=%q queued=%q answer=%q statuses=%v", sent, queued, answerTurn, completions)
	}
	for _, text := range []string{"/settings COMIC_COMPOSER_SEND literal input", "COMIC_COMPOSER_QUEUE literal queued input", "COMIC_COMPOSER_REPLY streamed first and second"} {
		if counts[text] != 1 {
			t.Fatalf("durable composer %q count=%d, want 1", text, counts[text])
		}
	}
	data, err = json.MarshalIndent(map[string]any{"counts": counts, "sent": sent, "queued": queued, "sentStatus": completions[sent], "queuedStatus": completions[queued]}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "composer-durable.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Durable composer: one literal Send and complete streamed reply, one queued input in a distinct interrupted turn")
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
