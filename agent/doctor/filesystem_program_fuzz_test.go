//go:build serffuzz

package doctor

import (
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzDoctorFilesystemProgram drives the doctor package through its real
// on-disk data plane. Each case creates a temp XDG-state home with transcripts,
// metadata, and jobs.jsonl records written through the production writers; it
// then resolves, loads, summarizes, and renders every public doctor report.
//
// SAFETY: the fixture is entirely below t.TempDir. It does not create a client,
// invoke a provider, shell, Git, network, or ambient-state command.
func FuzzDoctorFilesystemProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 48 {
			raw = raw[:48]
		}
		fixture := newDoctorFilesystemProgramFixture(t, raw)
		first := runDoctorFilesystemProgram(t, fixture, raw)
		second := runDoctorFilesystemProgram(t, fixture, raw)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("doctor filesystem program is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type doctorFilesystemProgramFixture struct {
	base         string
	rootSID      string
	childSID     string
	grandSID     string
	observerSID  string
	noJobsSID    string
	duplicateID  string
	rootBucket   string
	childBucket  string
	overrideBase string
	overrideSID  string
	directBase   string
	directHash   string
}

type doctorFilesystemProgramTrace struct {
	Paths              []Paths
	Counts             []CountResult
	CountRenders       []string
	Transcripts        []TranscriptResult
	TranscriptRenders  []string
	APILogs            []APILogResult
	APILogRenders      []string
	Watches            []WatchReport
	WatchRenders       []string
	Trees              []TreeNode
	TreeRenders        []string
	ResolvedStateBases []string
}

func newDoctorFilesystemProgramFixture(t *testing.T, raw []byte) doctorFilesystemProgramFixture {
	t.Helper()
	base := t.TempDir()
	rootBucket := stateHomeBucket(base, hash1)
	childBucket := stateHomeBucket(base, hash2)
	token := doctorFilesystemProgramToken(raw)
	fixture := doctorFilesystemProgramFixture{
		base:        base,
		rootSID:     "doctor_root",
		childSID:    "doctor_child",
		grandSID:    "doctor_grand",
		observerSID: "doctor_observer",
		noJobsSID:   "doctor_no_jobs",
		duplicateID: "doctor_duplicate",
		rootBucket:  rootBucket,
		childBucket: childBucket,
	}

	resultTool := "submit_answer"
	longText := "assistant mentions read_file and delegate_send " + token + " " + strings.Repeat("x", textPreviewMax+24)
	longArgs := `{"token":"` + token + `","payload":"` + strings.Repeat("a", argPreviewMax+24) + `"}`
	rootTurns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("read_file please " + token)}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			assistantText(longText),
			toolCall("read_file", `{"path":"notes.txt"}`),
			toolCall(resultTool, longArgs),
			{Kind: llm.ContentToolCall},
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("read_file", map[string]any{"token": token, "ok": true}, false),
			toolResult("shell", "failed\n"+token, true),
			{Kind: llm.ContentToolResult},
		}}),
	}
	cacheRead := 9000
	spikeCacheRead := 1000
	apiCalls := []transcript.APICall{
		{
			Round:        0,
			LatencyMs:    100,
			SystemPrompt: "tools read_file delegate_send read_file",
			Request: llm.APILogRequest{
				Model: "fixture-model", Provider: "fixture-provider",
				EndpointFamily: "openai_public", HistoryMode: llm.HistoryModeResponsesDelta,
			},
			Response: &llm.APILogResponse{FinishReason: "tool_calls", TextLength: 20, ToolCallCount: 2,
				Usage: llm.Usage{InputTokens: 10000, OutputTokens: 200, CacheReadTokens: &cacheRead}},
		},
		{
			Round:     1,
			LatencyMs: 50,
			Request: llm.APILogRequest{
				Model: "fixture-model", Provider: "fixture-provider",
				EndpointFamily: "openai_public", HistoryMode: llm.HistoryModeFullHistory,
			},
			Response: &llm.APILogResponse{FinishReason: "stop", Usage: llm.Usage{InputTokens: 1000}},
		},
		{
			Round:     2,
			LatencyMs: 25,
			Request: llm.APILogRequest{
				Model: "fixture-model", Provider: "fixture-provider",
				EndpointFamily: "openai_public", HistoryMode: llm.HistoryModeFullHistoryFallback,
			},
			Error: "fixture error",
		},
		{
			Round:     3,
			LatencyMs: 75,
			Request:   llm.APILogRequest{Model: "fixture-model", Provider: "fixture-provider"},
			Response: &llm.APILogResponse{FinishReason: "stop", TextLength: 1,
				Usage: llm.Usage{InputTokens: 60000, OutputTokens: 500, CacheReadTokens: &spikeCacheRead}},
		},
	}
	writeRichSession(t, rootBucket, fixture.rootSID, rootTurns, apiCalls, schema.SessionMeta{
		Config:     schema.ConfigSnapshot{ResultToolName: resultTool},
		ObservedBy: []string{fixture.observerSID, "doctor_missing_observer"},
	})
	writeRichSession(t, childBucket, fixture.childSID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{assistantText("child " + token)}}),
	}, nil, schema.SessionMeta{})
	writeRichSession(t, childBucket, fixture.grandSID, nil, nil, schema.SessionMeta{})
	writeRichSession(t, rootBucket, fixture.observerSID, nil, nil, schema.SessionMeta{})
	writeRichSession(t, rootBucket, fixture.noJobsSID, nil, nil, schema.SessionMeta{})
	writeRichSession(t, rootBucket, fixture.duplicateID, nil, nil, schema.SessionMeta{})
	writeRichSession(t, childBucket, fixture.duplicateID, nil, nil, schema.SessionMeta{})

	rootJobs := filepath.Join(rootBucket, "sessions", fixture.rootSID, "jobs.jsonl")
	childJobs := filepath.Join(childBucket, "sessions", fixture.childSID, "jobs.jsonl")
	grandJobs := filepath.Join(childBucket, "sessions", fixture.grandSID, "jobs.jsonl")
	for _, path := range []string{rootJobs, childJobs, grandJobs} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create jobs fixture directory: %v", err)
		}
	}
	watchKey := jobstore.WatchSendKey{WatchID: "watch_a", VisibleSessionID: fixture.rootSID}
	watchDropKey := jobstore.WatchSendKey{WatchID: "watch_a", VisibleSessionID: fixture.rootSID, WatchTarget: "job:drop"}
	writeJobsEvents(t, rootJobs, []jobstore.Event{
		{Kind: jobstore.EventDelegateCreated, DelegateID: "child", Delegate: &jobstore.DelegateEvent{
			ChildSessionID: fixture.childSID, TranscriptRef: "proj:" + hash2 + ":" + fixture.childSID, AgentType: "researcher", Generation: "g1",
		}},
		{Kind: jobstore.EventJobStarted, JobID: "job-child", DelegateID: "child"},
		{Kind: jobstore.EventDelegateCreated, DelegateID: "missing", Delegate: &jobstore.DelegateEvent{ChildSessionID: "doctor_missing", AgentType: "missing"}},
		{Kind: jobstore.EventDelegateCreated, DelegateID: "empty", Delegate: &jobstore.DelegateEvent{}},
		{Kind: jobstore.EventWatchRegistered, WatchID: "watch_a", Watch: &jobstore.WatchEvent{
			Generation: "wg1", OwnerSessionID: fixture.observerSID, VisibleSessionID: fixture.rootSID,
			Target: "job:root", SendTo: fixture.observerSID, Condition: "contains", ConfigHash: "fixture-config",
		}},
		{Kind: jobstore.EventWatchSendPending, WatchID: "watch_a", WatchSend: &jobstore.WatchSendState{Key: watchKey, DeliveryID: "delivery_a", UpdateSeq: 1}},
		{Kind: jobstore.EventWatchSendPending, WatchID: "watch_a", WatchSend: &jobstore.WatchSendState{Key: watchKey, DeliveryID: "delivery_a", UpdateSeq: 2}},
		{Kind: jobstore.EventWatchSendDelivered, WatchID: "watch_a", WatchSend: &jobstore.WatchSendState{
			Key: watchKey, DeliveryID: "delivery_a", UpdateSeq: 2, CoalescedCount: 2, TriggerIdentity: fixture.rootSID, TriggerReason: "contains",
		}},
		{Kind: jobstore.EventWatchSendDropped, WatchID: "watch_a", WatchSend: &jobstore.WatchSendState{
			Key: watchDropKey, DeliveryID: "delivery_drop", UpdateSeq: 1, DiagnosticReason: "runaway", SelfInfluenceDepth: 4,
		}},
		{Kind: jobstore.EventWatchSendEvicted, WatchID: "watch_orphan", WatchSend: &jobstore.WatchSendState{
			Key: jobstore.WatchSendKey{WatchID: "watch_orphan"}, DeliveryID: "delivery_evicted", UpdateSeq: 1,
		}},
	})
	writeJobsEvents(t, childJobs, []jobstore.Event{
		{Kind: jobstore.EventDelegateCreated, DelegateID: "cycle", Delegate: &jobstore.DelegateEvent{
			ChildSessionID: fixture.rootSID, TranscriptRef: "proj:" + hash1 + ":" + fixture.rootSID, AgentType: "cycle",
		}},
		{Kind: jobstore.EventDelegateCreated, DelegateID: "grand", Delegate: &jobstore.DelegateEvent{ChildSessionID: fixture.grandSID, AgentType: "leaf"}},
	})
	writeJobsEvents(t, grandJobs, nil)
	if err := os.MkdirAll(filepath.Join(rootBucket, "sessions", fixture.noJobsSID, "jobs.jsonl"), 0o755); err != nil {
		t.Fatalf("create unreadable jobs fixture: %v", err)
	}
	fixture.overrideBase = t.TempDir()
	fixture.overrideSID = "doctor_override"
	writeSession(t, fixture.overrideBase, fixture.overrideSID)
	fixture.directBase = t.TempDir()
	fixture.directHash = "direct_hash"
	writeSession(t, filepath.Join(fixture.directBase, "serf", "projects", fixture.directHash), fixture.rootSID)
	return fixture
}

func runDoctorFilesystemProgram(t *testing.T, fixture doctorFilesystemProgramFixture, raw []byte) doctorFilesystemProgramTrace {
	t.Helper()
	trace := doctorFilesystemProgramTrace{}

	for _, selector := range []string{
		fixture.rootSID,
		"local:" + fixture.rootSID,
		"proj:" + hash1 + ":" + fixture.rootSID,
		"proj:" + hash2 + ":" + fixture.childSID,
	} {
		paths, err := Locate(fixture.base, selector)
		if err != nil || paths.SessionID == "" || paths.TranscriptPath == "" || paths.JobsPath == "" {
			t.Fatalf("Locate(%q) = %#v, %v", selector, paths, err)
		}
		trace.Paths = append(trace.Paths, paths)
	}
	if _, err := Locate(fixture.base, fixture.duplicateID); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous Locate error = %v", err)
	}
	if _, err := Locate(fixture.base, "local:doctor_absent"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing Locate error = %v", err)
	}
	if _, err := Locate(fixture.base, "proj:doctor_absent:"+fixture.rootSID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing bucket Locate error = %v", err)
	}
	for _, selector := range []string{"", "current", "../escape", "local:../escape", "proj:bad", "proj::sid"} {
		if _, err := Locate(fixture.base, selector); err == nil {
			t.Fatalf("invalid selector %q unexpectedly resolved", selector)
		}
	}

	overridePaths, err := Locate(fixture.overrideBase, "local:"+fixture.overrideSID)
	if err != nil || overridePaths.TranscriptRef != "local:"+fixture.overrideSID {
		t.Fatalf("override Locate = %#v, %v", overridePaths, err)
	}
	trace.Paths = append(trace.Paths, overridePaths)
	direct, err := locateInBucket(fixture.directBase, nil, selector{projectID: fixture.directHash, sid: fixture.rootSID})
	if err != nil || direct.ProjectID != fixture.directHash {
		t.Fatalf("direct locateInBucket fallback = %#v, %v", direct, err)
	}
	trace.Paths = append(trace.Paths, direct)

	t.Setenv("SERF_STATE_DIR", fixture.base)
	t.Setenv("XDG_STATE_HOME", filepath.Join(fixture.base, "xdg"))
	trace.ResolvedStateBases = append(trace.ResolvedStateBases, ResolveStateBase("flag-state"), ResolveStateBase(""))
	t.Setenv("SERF_STATE_DIR", "")
	trace.ResolvedStateBases = append(trace.ResolvedStateBases, ResolveStateBase(""))
	if trace.ResolvedStateBases[0] != "flag-state" || trace.ResolvedStateBases[1] != fixture.base || trace.ResolvedStateBases[2] != filepath.Join(fixture.base, "xdg") {
		t.Fatalf("ResolveStateBase precedence = %#v", trace.ResolvedStateBases)
	}
	t.Setenv("XDG_STATE_HOME", "")
	_ = ResolveStateBase("")
	originalGlob := globProjectBuckets
	globProjectBuckets = func(string) ([]string, error) { return nil, errors.New("scripted glob failure") }
	t.Cleanup(func() { globProjectBuckets = originalGlob })
	if _, err := Locate(fixture.base, fixture.rootSID); err == nil {
		t.Fatal("malformed bucket glob unexpectedly succeeded")
	}
	globProjectBuckets = originalGlob
	originalHome := doctorUserHomeDir
	doctorUserHomeDir = func() (string, error) { return "", errors.New("scripted home failure") }
	_ = ResolveStateBase("")
	doctorUserHomeDir = originalHome
	missing := filepath.Join(fixture.base, "missing.transcript.jsonl")
	if _, err := loadTranscript(missing); err == nil {
		t.Fatal("missing transcript unexpectedly loaded")
	}
	badInterior := filepath.Join(fixture.base, "bad-interior.jsonl")
	if err := os.WriteFile(badInterior, []byte("{bad}\n{\"kind\":\"header\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTranscript(badInterior); err == nil {
		t.Fatal("malformed interior transcript unexpectedly loaded")
	}
	badEntry := filepath.Join(fixture.base, "bad-entry.jsonl")
	if err := os.WriteFile(badEntry, []byte("{\"kind\":\"entry\",\"turn\":\"bad\"}\n{\"kind\":\"header\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTranscript(badEntry); err == nil {
		t.Fatal("malformed transcript entry unexpectedly loaded")
	}
	if _, err := Count(fixture.base, "missing", "tool"); err == nil {
		t.Fatal("Count missing selector unexpectedly succeeded")
	}
	if _, err := Transcript(fixture.base, "missing", TranscriptOpts{}); err == nil {
		t.Fatal("Transcript missing selector unexpectedly succeeded")
	}
	_ = toolResultContentText(make(chan int))
	_ = toolResultContentText(nil)
	if _, err := Locate(fixture.base, "proj:"+hash1+":missing"); err == nil {
		t.Fatal("missing session in existing bucket unexpectedly resolved")
	}
	originalRead := readTranscriptFile
	readTranscriptFile = func(string) ([]byte, error) { return nil, errors.New("scripted transcript read failure") }
	if _, err := Count(fixture.base, fixture.rootSID, "tool"); err == nil {
		t.Fatal("Count unreadable transcript unexpectedly succeeded")
	}
	if _, err := Transcript(fixture.base, fixture.rootSID, TranscriptOpts{}); err == nil {
		t.Fatal("Transcript unreadable transcript unexpectedly succeeded")
	}
	readTranscriptFile = originalRead
	blankLine := filepath.Join(fixture.base, "blank-line.jsonl")
	if err := os.WriteFile(blankLine, []byte("\n{\"kind\":\"header\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTranscript(blankLine); err != nil {
		t.Fatal(err)
	}
	if _, err := Watches(fixture.base, "missing", WatchOpts{}); err == nil {
		t.Fatal("Watches missing selector unexpectedly succeeded")
	}
	_ = RenderWatches(WatchReport{SessionID: "edge", Watches: []WatchView{{MaxSelfInfluenceDepth: 1}}})
	unreadableJobs := "\x00"
	if got := depthLimitNote(Paths{JobsPath: unreadableJobs}); got != "" {
		t.Fatalf("missing depth-limit jobs note = %q", got)
	}
	emptyJobs := filepath.Join(fixture.base, "empty-jobs.jsonl")
	writeJobsEvents(t, emptyJobs, nil)
	if got := depthLimitNote(Paths{JobsPath: emptyJobs}); got != "" {
		t.Fatalf("empty depth-limit jobs note = %q", got)
	}
	depthJobs := filepath.Join(fixture.base, "depth-jobs.jsonl")
	writeJobsEvents(t, depthJobs, []jobstore.Event{{Kind: jobstore.EventDelegateCreated, DelegateID: "d", Delegate: &jobstore.DelegateEvent{ChildSessionID: "child"}}})
	if got := depthLimitNote(Paths{JobsPath: depthJobs}); got == "" {
		t.Fatal("depth-limit child note missing")
	}

	for _, tool := range []string{"read_file", "submit_answer", "delegate_send"} {
		result, err := Count(fixture.base, fixture.rootSID, tool)
		if err != nil || result.SessionID != fixture.rootSID || result.Tool != tool {
			t.Fatalf("Count(%q) = %#v, %v", tool, result, err)
		}
		trace.Counts = append(trace.Counts, result)
		rendered := RenderCount(result)
		if !strings.Contains(rendered, tool) {
			t.Fatalf("RenderCount(%q) = %q", tool, rendered)
		}
		trace.CountRenders = append(trace.CountRenders, rendered)
	}
	if trace.Counts[0].Calls != 1 || trace.Counts[1].Calls != 1 || trace.Counts[2].Calls != 0 || trace.Counts[2].MentionsAssistantText == 0 || trace.Counts[2].MentionsAPICalls == 0 {
		t.Fatalf("Count results = %#v", trace.Counts)
	}
	if _, err := Count(fixture.base, "local:doctor_absent", "read_file"); err == nil {
		t.Fatal("Count missing selector unexpectedly succeeded")
	}

	ranges := []string{"", "last:1", "start:2", "2-3", "3-1", "last:not-a-number"}
	for _, rangeArg := range ranges {
		result, err := Transcript(fixture.base, fixture.rootSID, TranscriptOpts{Range: rangeArg})
		if err != nil || result.TurnsRendered+result.Elided != result.TurnsTotal || result.ResultTool != "submit_answer" {
			t.Fatalf("Transcript(%q) = %#v, %v", rangeArg, result, err)
		}
		trace.Transcripts = append(trace.Transcripts, result)
		outline := RenderTranscript(result, "outline")
		markdown := RenderTranscript(result, "markdown")
		if !strings.Contains(outline, "turns_total=") || !strings.Contains(markdown, "result_tool=submit_answer") {
			t.Fatalf("RenderTranscript(%q) = outline:%q markdown:%q", rangeArg, outline, markdown)
		}
		trace.TranscriptRenders = append(trace.TranscriptRenders, outline, markdown)
	}
	if len(trace.Transcripts[0].Turns) != 3 || !trace.Transcripts[0].Turns[1].ToolCalls[1].IsResult || len(trace.Transcripts[0].Turns[2].ToolResults) != 2 {
		t.Fatalf("Transcript summaries = %#v", trace.Transcripts[0])
	}
	if _, err := Transcript(fixture.base, "local:doctor_absent", TranscriptOpts{}); err == nil {
		t.Fatal("Transcript missing selector unexpectedly succeeded")
	}
	if got := toolResultContentText(math.Inf(1)); got != "+Inf" {
		t.Fatalf("toolResultContentText marshal fallback = %q", got)
	}
	if got := resolveResultTool(Paths{BucketDir: t.TempDir(), SessionID: "missing"}); got != "communicate" {
		t.Fatalf("resolveResultTool missing meta = %q", got)
	}
	if _, err := loadTranscript(filepath.Join(t.TempDir(), "missing.transcript.jsonl")); err == nil {
		t.Fatal("loadTranscript missing path unexpectedly succeeded")
	}

	apiOpts := []APILogOpts{
		{},
		{EmptyOnly: true},
		{ErrorsOnly: true},
		{CacheSpikes: true},
		{CacheSpikes: true, SpikeThreshold: 500},
		{SummaryOnly: true},
	}
	for _, opts := range apiOpts {
		result, err := APILog(fixture.base, fixture.rootSID, opts)
		if err != nil || result.SessionID != fixture.rootSID || result.Totals.TotalTokens != result.Totals.InputTokens+result.Totals.OutputTokens {
			t.Fatalf("APILog(%#v) = %#v, %v", opts, result, err)
		}
		trace.APILogs = append(trace.APILogs, result)
		rendered := RenderAPILog(result, opts)
		if !strings.Contains(rendered, "session "+fixture.rootSID) {
			t.Fatalf("RenderAPILog(%#v) = %q", opts, rendered)
		}
		trace.APILogRenders = append(trace.APILogRenders, rendered)
	}
	if trace.APILogs[0].Totals.Calls != 4 || trace.APILogs[1].Totals.Empties != 1 || len(trace.APILogs[2].Calls) != 1 || len(trace.APILogs[3].Calls) != 1 {
		t.Fatalf("APILog reports = %#v", trace.APILogs)
	}
	if _, err := APILog(fixture.base, "local:doctor_absent", APILogOpts{}); err == nil {
		t.Fatal("APILog missing selector unexpectedly succeeded")
	}

	watchOpts := []WatchOpts{{}, {SelfLoopsOnly: true}, {WatchID: "watch_a"}, {WatchID: "doctor_absent", SelfLoopsOnly: true}}
	for _, opts := range watchOpts {
		result, err := Watches(fixture.base, fixture.rootSID, opts)
		if err != nil || result.SessionID != fixture.rootSID {
			t.Fatalf("Watches(%#v) = %#v, %v", opts, result, err)
		}
		trace.Watches = append(trace.Watches, result)
		rendered := RenderWatches(result)
		if !strings.Contains(rendered, "session "+fixture.rootSID) {
			t.Fatalf("RenderWatches(%#v) = %q", opts, rendered)
		}
		trace.WatchRenders = append(trace.WatchRenders, rendered)
	}
	if len(trace.Watches[0].Watches) < 2 || trace.Watches[0].Watches[0].Generation != "wg1" || len(trace.Watches[1].Watches) != 1 || trace.Watches[1].Watches[0].RunawayDrops == 0 || len(trace.Watches[3].Watches) != 0 {
		t.Fatalf("Watch reports = %#v", trace.Watches)
	}
	if _, err := Watches(fixture.base, fixture.noJobsSID, WatchOpts{}); err == nil {
		t.Fatal("Watches unreadable jobs unexpectedly succeeded")
	}
	if got := terminalKind(jobstore.EventKind("unknown")); got != "unknown" {
		t.Fatalf("terminalKind default = %q", got)
	}

	for _, opts := range []TreeOpts{{Depth: 1, Observers: true}, {Depth: 3, Observers: true}, {Depth: maxTreeDepth + 1}} {
		result, err := Tree(fixture.base, fixture.rootSID, opts)
		if err != nil || result.SessionID != fixture.rootSID {
			t.Fatalf("Tree(%#v) = %#v, %v", opts, result, err)
		}
		trace.Trees = append(trace.Trees, result)
		rendered := RenderTree(result)
		if !strings.Contains(rendered, fixture.rootSID) {
			t.Fatalf("RenderTree(%#v) = %q", opts, rendered)
		}
		trace.TreeRenders = append(trace.TreeRenders, rendered)
	}
	if !doctorFilesystemProgramTreeHasNote(trace.Trees[0], "depth limit") || !doctorFilesystemProgramTreeHasNote(trace.Trees[1], "already shown") || !doctorFilesystemProgramTreeHasNote(trace.Trees[1], "transcript not found") {
		t.Fatalf("Tree notes = %#v", trace.Trees)
	}
	noJobsTree, err := Tree(fixture.base, fixture.noJobsSID, TreeOpts{})
	if err != nil || !strings.Contains(noJobsTree.Note, "jobs unreadable") {
		t.Fatalf("Tree unreadable jobs = %#v, %v", noJobsTree, err)
	}
	if _, err := Tree(fixture.base, "local:doctor_absent", TreeOpts{}); err == nil {
		t.Fatal("Tree missing selector unexpectedly succeeded")
	}
	return trace
}

func doctorFilesystemProgramToken(raw []byte) string {
	if len(raw) == 0 {
		return "empty"
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func doctorFilesystemProgramTreeHasNote(node TreeNode, want string) bool {
	if strings.Contains(node.Note, want) {
		return true
	}
	for _, child := range node.Children {
		if doctorFilesystemProgramTreeHasNote(child, want) {
			return true
		}
	}
	return false
}
