package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// The runtime-started memory librarian is the one delegate that keeps the
// memory save tools, and its descriptor records the role that grants them.
func TestMemoryLibrarianDelegateKeepsSaveTools(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	root := t.TempDir()
	var childTools []llm.ToolDefinition
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, MemoryProjectID: project.ID, Project: project, MemoryLibrarian: "turn", testOnly: testConfig{sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(func(req llm.Request) llm.Response {
		childTools = req.Tools
		return finalResponse("no changes")
	}))
	res := s.createDelegate(context.Background(), delegateArgs{Task: "librarian pass", DelegationAllowance: new(0), runtimeRole: memoryLibrarianRole})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	child := memoryWaitChild(t, s, res.ChildSessionID)
	for _, name := range nativeMemoryToolNames {
		if child.sess.reg.Get(name) == nil {
			t.Errorf("librarian lacks %s", name)
		}
	}
	offered := map[string]bool{}
	for _, def := range childTools {
		offered[def.Name] = true
	}
	for _, name := range memorySaveToolNames {
		if !offered[name] {
			t.Errorf("librarian's model request does not offer %s", name)
		}
	}
	data, _ := child.sess.buildPromptData(child.sess.currentEnv())
	if !data.MemoryLibrarian || !data.MemorySaves || data.MemoryByLibrarian {
		t.Errorf("librarian prompt data: MemoryLibrarian=%t MemorySaves=%t MemoryByLibrarian=%t", data.MemoryLibrarian, data.MemorySaves, data.MemoryByLibrarian)
	}
	if !strings.Contains(data.Role, "memory librarian") {
		t.Errorf("librarian role prompt not frozen in: %.120q", data.Role)
	}
	if got := memoryExec(t, child.sess, "memory_write", map[string]any{"scope": "project", "file_path": "cents.md", "content": "---\ndescription: prices are integer cents\n---\nPrices are integer cents.\n"}); got.IsError {
		t.Fatalf("librarian write refused: %s", got.Output)
	}
	s.delegateController.mu.Lock()
	role := s.delegateController.durable[res.DelegateID].Descriptor.RuntimeRole
	s.delegateController.mu.Unlock()
	if role != memoryLibrarianRole {
		t.Fatalf("descriptor runtime role = %q", role)
	}
}

// No model can start the librarian: the role is missing from the catalog a
// delegate call resolves against, and the delegate tool's decoder never sets
// a runtime role.
func TestMemoryLibrarianRoleIsNotModelSelectable(t *testing.T) {
	t.Parallel()
	s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir()}))
	if _, listed := s.pluginAgents[memoryLibrarianRole]; listed {
		t.Fatal("memory-librarian is in the model-visible agent catalog")
	}
	decoded, err := decodeDelegateArgs(map[string]any{"prompt": "save things", "agent_type": memoryLibrarianRole})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.runtimeRole != "" {
		t.Fatalf("decoder set runtimeRole %q", decoded.runtimeRole)
	}
	res := s.createDelegate(context.Background(), decoded)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "unknown plugin agent type") {
		t.Fatalf("model-requested librarian: err = %v", res.Err)
	}
}

// Under either librarian arm the root reads memory but loses the save tools,
// and its prompt says the librarian saves. Off leaves the root as it was.
func TestLibrarianModeRootReadsButDoesNotSave(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "turn", "sidecar"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir(), MemoryLibrarian: mode}))
			librarian := mode != "off"
			for _, name := range memorySaveToolNames {
				if (s.reg.Get(name) != nil) == librarian {
					t.Errorf("%s: has %s = %t", mode, name, !librarian)
				}
			}
			for _, name := range memoryReadToolNames {
				if s.reg.Get(name) == nil {
					t.Errorf("%s: root lost %s", mode, name)
				}
			}
			prompt, warning := s.renderSystemPrompt(s.currentEnv())
			if warning != "" {
				t.Fatal(warning)
			}
			if got := strings.Contains(prompt, "A memory librarian reads this conversation"); got != librarian {
				t.Errorf("%s: librarian line present = %t", mode, got)
			}
			if got := strings.Contains(prompt, "Write each page with memory_write"); got == librarian {
				t.Errorf("%s: save guidance present = %t", mode, got)
			}
		})
	}
}

// An unknown arm name is off; so is any delegate, whatever the switch says.
func TestLibrarianModeParsing(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]memoryLibrarianMode{"": librarianOff, "bogus": librarianOff, "TURN": librarianTurn, "Sidecar": librarianSidecar} {
		s := &Session{cfg: SessionConfig{MemoryStateRoot: "/m", MemoryLibrarian: value}}
		if value == "" {
			s.cfg.MemoryLibrarian = "off"
		}
		if got := s.librarianMode(); got != want {
			t.Errorf("mode %q = %q, want %q", value, got, want)
		}
	}
	child := &Session{cfg: SessionConfig{MemoryStateRoot: "/m", MemoryLibrarian: "turn"}}
	child.cfg.spawn.parentSessionID = "root"
	if child.librarianMode() != librarianOff {
		t.Error("a delegate runs a librarian arm")
	}
}

// librarianScript answers the root with a plain final message and plays the
// librarian: it reads its range and saves a page on its first request, then
// reports. It records each librarian brief's range.
type librarianScript struct {
	mu     sync.Mutex
	ranges []string
	writes atomic.Int32
}

var librarianRangeRE = regexp.MustCompile(`Read turns (\d+-\d+)`)

func (ls *librarianScript) step(req llm.Request) llm.Response {
	librarian := false
	for _, def := range req.Tools {
		if def.Name == "memory_write" {
			librarian = true
		}
	}
	if !librarian {
		return finalResponse("done")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleTool {
		return finalResponse("saved project/cents.md")
	}
	var brief string
	for _, msg := range req.Messages {
		if m := librarianRangeRE.FindStringSubmatch(msg.Text()); m != nil {
			brief = m[1]
		}
	}
	ls.mu.Lock()
	ls.ranges = append(ls.ranges, brief)
	n := len(ls.ranges)
	ls.mu.Unlock()
	ls.writes.Add(1)
	args, _ := json.Marshal(map[string]any{"scope": "personal", "file_path": fmt.Sprintf("fact-%d.md", n), "content": "---\ndescription: a fact\n---\nA fact.\n"})
	return toolCallResponse(llm.ToolCallData{ID: fmt.Sprintf("lib-write-%d", n), Name: "memory_write", Arguments: args})
}

func (ls *librarianScript) steps() []func(llm.Request) llm.Response {
	out := make([]func(llm.Request) llm.Response, 200)
	for i := range out {
		out[i] = ls.step
	}
	return out
}

func waitLibrarianIdle(t *testing.T, s *Session, wantThrough int) librarianCursor {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s.librarian.mu.Lock()
		busy := s.librarian.inFlight != nil || s.librarian.launching
		s.librarian.mu.Unlock()
		cursor := s.loadLibrarianCursor()
		if !busy && cursor.ProcessedThrough >= wantThrough {
			return cursor
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("librarian did not settle through turn %d: cursor %+v", wantThrough, s.loadLibrarianCursor())
	return librarianCursor{}
}

// Under the turn arm, each human turn's settle starts a fresh librarian over
// the turns no finished pass covered: the second pass starts where the first
// ended, the librarian's writes land, and its report never reaches the root's
// conversation.
func TestLibrarianTurnPassCoversEachTurnOnce(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	memRoot := t.TempDir()
	script := &librarianScript{}
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: memRoot, MemoryProjectID: project.ID, Project: project, MemoryLibrarian: "turn", MaxSubagentDepth: 1, TurnEndsProcess: true, testOnly: testConfig{sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(script.steps()...))

	if _, err := s.ProcessInput(context.Background(), "remember: prices are integer cents", nil); err != nil {
		t.Fatal(err)
	}
	// A one-shot run's drain returns only once the pass has reported, even
	// though the root's terminal message already ended the run.
	if _, err := s.DrainJobTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.loadLibrarianCursor().ProcessedThrough; got == 0 {
		t.Fatal("the drain returned before the librarian pass reported")
	}
	first := waitLibrarianIdle(t, s, 1)
	if _, err := s.ProcessInput(context.Background(), "now add a total", nil); err != nil {
		t.Fatal(err)
	}
	second := waitLibrarianIdle(t, s, first.ProcessedThrough+1)

	script.mu.Lock()
	ranges := append([]string(nil), script.ranges...)
	script.mu.Unlock()
	want := []string{fmt.Sprintf("1-%d", first.ProcessedThrough), fmt.Sprintf("%d-%d", first.ProcessedThrough+1, second.ProcessedThrough)}
	if !slices.Equal(ranges, want) {
		t.Fatalf("librarian ranges = %v, want %v", ranges, want)
	}
	for _, name := range []string{"fact-1.md", "fact-2.md"} {
		if _, err := os.Stat(filepath.Join(memRoot, "memory", name)); err != nil {
			t.Errorf("librarian page %s: %v", name, err)
		}
	}
	if len(second.Reports) != 2 || !strings.Contains(second.Reports[0], "saved project/cents.md") {
		t.Errorf("cursor reports = %q", second.Reports)
	}
	path := transcriptPath(s.stateDir, s.id)
	data, err := readTranscriptFull(path, filepath.Dir(filepath.Dir(path)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range data.Entries {
		if raw, _ := json.Marshal(entry); strings.Contains(string(raw), "saved project/cents.md") {
			t.Fatalf("librarian report reached the root transcript: %s", raw)
		}
	}
}

// A pass that reports a failure leaves the cursor in place, so the next pass
// covers the same turns; a report from a delegate that is not the pass in
// flight changes nothing.
func TestLibrarianFailedPassLeavesRangeForNextPass(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Session{id: "root", stateDir: stateDir, cfg: SessionConfig{MemoryStateRoot: "/m", MemoryLibrarian: "turn"}}
	s.saveLibrarianCursor(librarianCursor{ProcessedThrough: 4})
	failed := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"provider error"`)}
	reported := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"saved a.md"`)}

	s.librarian.inFlight = &librarianPass{delegateID: "dlg_a", from: 5, to: 9}
	s.librarianReported("dlg_a", failed)
	if got := s.loadLibrarianCursor().ProcessedThrough; got != 4 {
		t.Fatalf("failed pass moved the cursor to %d", got)
	}
	if s.librarian.inFlight != nil {
		t.Fatal("failed pass still in flight")
	}

	s.librarian.inFlight = &librarianPass{delegateID: "dlg_b", from: 5, to: 11}
	s.librarianReported("dlg_stranger", reported)
	if got := s.loadLibrarianCursor().ProcessedThrough; got != 4 {
		t.Fatalf("a stranger's report moved the cursor to %d", got)
	}
	s.librarianReported("dlg_b", reported)
	if got := s.loadLibrarianCursor().ProcessedThrough; got != 11 {
		t.Fatalf("cursor after the retried pass = %d, want 11", got)
	}
	if got := librarianTurnBrief("root", 12, 15, s.loadLibrarianCursor().Reports); !strings.Contains(got, "range=\"12-15\"") || !strings.Contains(got, "saved a.md") {
		t.Fatalf("next brief: %s", got)
	}
}

// sidecarScript plays the sidecar librarian: its first generation creates the
// parent watch and reports; every later generation reports at once. It
// records whether a later generation saw a turn.end frame.
type sidecarScript struct {
	mu          sync.Mutex
	generations int
	sawTurnEnd  bool
}

func (ss *sidecarScript) step(req llm.Request) llm.Response {
	librarian := false
	for _, def := range req.Tools {
		if def.Name == "memory_write" {
			librarian = true
		}
	}
	if !librarian {
		return finalResponse("done")
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), "kind: turn.end") {
			ss.sawTurnEnd = true
		}
	}
	last := req.Messages[len(req.Messages)-1]
	if ss.generations == 0 && last.Role != llm.RoleTool {
		args, _ := json.Marshal(map[string]any{"operation": "create", "source": "parent", "events": []string{"user.message", "turn.end"}})
		return toolCallResponse(llm.ToolCallData{ID: "sidecar-watch", Name: "job_watch", Arguments: args})
	}
	ss.generations++
	return finalResponse("no changes")
}

// Under the sidecar arm the root starts one long-lived librarian with
// watch_parent on its first human message; the sidecar's parent watch wakes it
// at the root's turn end, and a later human message starts no second one.
func TestLibrarianSidecarStartsOnceAndWakesOnTurnEnd(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	script := &sidecarScript{}
	steps := make([]func(llm.Request) llm.Response, 200)
	for i := range steps {
		steps[i] = script.step
	}
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir(), MemoryProjectID: project.ID, Project: project, MemoryLibrarian: "sidecar", MaxSubagentDepth: 1, TurnEndsProcess: true, testOnly: testConfig{sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(steps...))

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := s.DrainJobTree(context.Background()); err != nil {
				t.Fatal(err)
			}
			script.mu.Lock()
			done := cond()
			script.mu.Unlock()
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", what, script)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, err := s.ProcessInput(context.Background(), "remember: prices are integer cents", nil); err != nil {
		t.Fatal(err)
	}
	// The first turn can end before the sidecar has armed its watch; its
	// first pass reads that turn from the transcript.
	waitFor("sidecar never finished its first pass", func() bool { return script.generations >= 1 })
	sidecar := s.loadLibrarianCursor().SidecarDelegateID
	if sidecar == "" {
		t.Fatal("no sidecar recorded")
	}
	if _, err := s.ProcessInput(context.Background(), "now add a total", nil); err != nil {
		t.Fatal(err)
	}
	// One one-shot drain delivers the turn.end frame and waits for the pass
	// it wakes, though the root's terminal message already ended the run.
	if _, err := s.DrainJobTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	script.mu.Lock()
	woke := script.sawTurnEnd && script.generations >= 2
	script.mu.Unlock()
	if !woke {
		t.Fatalf("the drain returned before the sidecar's turn.end pass: %+v", script)
	}
	if got := s.loadLibrarianCursor().SidecarDelegateID; got != sidecar {
		t.Fatalf("sidecar changed from %s to %s", sidecar, got)
	}
	s.delegateController.mu.Lock()
	librarians := 0
	for _, aggregate := range s.delegateController.durable {
		if aggregate.Descriptor.RuntimeRole == memoryLibrarianRole {
			librarians++
			if !aggregate.Descriptor.ParentWatchGranted {
				t.Error("sidecar lacks watch_parent")
			}
		}
	}
	s.delegateController.mu.Unlock()
	if librarians != 1 {
		t.Fatalf("librarian delegates = %d, want 1", librarians)
	}
}
