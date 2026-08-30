package agent

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// writeTestFile writes content creating parent dirs.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// doctorToolForTest builds a session with a real state dir (one bucket, one
// session fixture written by the caller) and returns its doctor_evener
// registered tool plus the state root to pass.
func doctorToolForTest(t *testing.T, stateHome string) tool.RegisteredTool {
	t.Helper()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         stateHome,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	drainSessionEvents(sess)

	tools := doctorTools(newToolDeps(sess))
	if len(tools) != 1 || tools[0].Definition.Name != "doctor_evener" {
		t.Fatalf("doctorTools = %d tools, want exactly one named doctor_evener", len(tools))
	}
	return tools[0]
}

// TestDoctorEvener_LocateResolvesSelector proves the tool executes a locate
// against the session's own state root with no state_dir argument: the
// invocation class that failed with `evener: command not found` when the
// skill shelled out.
func TestDoctorEvener_LocateResolvesSelector(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command":  "locate",
		"selector": sid,
	})
	if err != nil {
		t.Fatalf("doctor_evener locate: %v", err)
	}
	paths, ok := out.(doctor.Paths)
	if !ok {
		t.Fatalf("locate result = %T, want doctor.Paths", out)
	}
	if paths.SessionID != sid {
		t.Errorf("session_id = %q, want fixture sid %q", paths.SessionID, sid)
	}
	if !strings.HasSuffix(paths.TranscriptPath, sid+".transcript.jsonl") {
		t.Errorf("transcript_path = %q, want flat SID-prefixed path", paths.TranscriptPath)
	}
}

// TestDoctorEvener_StateDirOverride proves an explicit state_dir beats the
// session default.
func TestDoctorEvener_StateDirOverride(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	other := newStateHome(t)
	rt := doctorToolForTest(t, other)

	// against the session's own (empty) root: not found
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "locate", "selector": sid,
	})
	if err == nil {
		t.Fatal("locate against empty root should fail")
	}

	// with explicit state_dir: resolves
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "locate", "selector": sid, "state_dir": stateHome,
	})
	if err != nil {
		t.Fatalf("locate with state_dir: %v", err)
	}
	paths, ok := out.(doctor.Paths)
	if !ok {
		t.Fatalf("locate result = %T, want doctor.Paths", out)
	}
	if paths.SessionID != sid {
		t.Errorf("session_id = %q, want fixture sid %q", paths.SessionID, sid)
	}
}

// TestDoctorEvener_RejectsUnknownCommand proves bad commands are surfaced as
// errors, not silent defaults.
func TestDoctorEvener_RejectsUnknownCommand(t *testing.T) {
	stateHome := newStateHome(t)
	rt := doctorToolForTest(t, stateHome)
	_, err := rt.Exec(context.Background(), nil, map[string]any{"command": "bogus"})
	if err == nil {
		t.Fatal("unknown command should error")
	}
	if !strings.Contains(err.Error(), "unknown doctor command") {
		t.Errorf("err = %v, want unknown doctor command", err)
	}
}

// TestDoctorEvener_TranscriptCount proves a per-command option flows through:
// transcript --count delegate_send is the skill's structural-invocation
// oracle.
func TestDoctorEvener_TranscriptCount(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "transcript", "selector": sid,
		"count": "delegate_send",
	})
	if err != nil {
		t.Fatalf("doctor_evener transcript count: %v", err)
	}
	count, ok := out.(doctor.CountResult)
	if !ok {
		t.Fatalf("count result = %T, want doctor.CountResult", out)
	}
	if count.Calls != 0 {
		t.Errorf("calls = %d, want 0 (fixture has no delegate_send calls)", count.Calls)
	}
}

// TestDoctorEvener_SessionsAndAudit prove the batch commands work through the
// tool with the option mapping (since, runbook).
func TestDoctorEvener_SessionsAndAudit(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "sessions", "since": "24h",
	})
	if err != nil {
		t.Fatalf("doctor_evener sessions: %v", err)
	}
	sres, ok := out.(doctorSessionsEnvelope)
	if !ok {
		t.Fatalf("sessions result = %T, want doctorSessionsEnvelope", out)
	}
	if len(sres.Sessions) != 1 {
		t.Errorf("sessions = %d, want 1 (fixture session is recent)", len(sres.Sessions))
	}

	// audit with an explicit session list
	out, err = rt.Exec(context.Background(), nil, map[string]any{
		"command": "audit", "runbook": "error-loop", "sessions": sid,
	})
	if err != nil {
		t.Fatalf("doctor_evener audit: %v", err)
	}
	ares, ok := out.(doctor.AuditResult)
	if !ok {
		t.Fatalf("audit result = %T, want doctor.AuditResult", out)
	}
	if ares.Runbook != "error-loop" {
		t.Errorf("runbook = %q, want error-loop", ares.Runbook)
	}
	if ares.SessionsChecked != 1 {
		t.Errorf("sessions_checked = %d, want 1", ares.SessionsChecked)
	}
}

// TestDoctorEvener_ReadOnlyEnforced proves the tool is registered read-only:
// the registry must reject a write-class mutation attempt structurally.
func TestDoctorEvener_ReadOnlyEnforced(t *testing.T) {
	stateHome := newStateHome(t)
	rt := doctorToolForTest(t, stateHome)
	if !rt.ReadOnly {
		t.Fatal("doctor_evener must be registered ReadOnly:true")
	}
}

// TestDoctorEvener_RunbookSeamSubstitutable proves the doctorBundledSkills
// test seam works: a substituted FS changes what the tool resolves, and the
// traversal-rejection path of the shared ParseRunbookFromFS rejects path
// separators through the tool layer.
func TestDoctorEvener_RunbookSeamSubstitutable(t *testing.T) {
	stateHome := newStateHome(t)
	rt := doctorToolForTest(t, stateHome)

	old := doctorBundledSkills
	doctorBundledSkills = func() fs.FS {
		return fstest.MapFS{"doctoring-evener/runbooks/fake.md": &fstest.MapFile{Data: []byte("")}}
	}
	t.Cleanup(func() { doctorBundledSkills = old })

	// substitution takes effect: a runbook only the fake FS has
	if _, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "audit", "runbook": "fake", "sessions": "034ANYSESSION0000000000",
	}); err == nil {
		t.Error("substituted-FS runbook resolved via real bundled skills; seam not honored")
	}

	// traversal rejection through the tool layer
	for _, name := range []string{"../secret", "a/b", `a\b`} {
		_, err := doctorLoadRunbook(name)
		if err == nil || !strings.Contains(err.Error(), "invalid runbook name") {
			t.Errorf("runbook %q: err = %v, want invalid runbook name", name, err)
		}
	}
}

// doctorTestSID mints a validator-passing session id for the tool fixtures.
func doctorTestSID(t *testing.T) string {
	t.Helper()
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// writeDoctorFixtureSession writes a minimal valid session (transcript with
// header + one user turn, meta, empty jobs.jsonl) into bucket/sessions/.
// Returns the minted session id.
func writeDoctorFixtureSession(t *testing.T, bucket string) string {
	t.Helper()
	sid := doctorTestSID(t)
	sess := bucket + "/sessions"
	writeTestFile(t, sess+"/"+sid+".transcript.jsonl",
		"{\"kind\":\"header\",\"format_version\":2,\"session_id\":\""+sid+"\",\"created_at\":\"2026-08-29T19:00:00Z\",\"model\":\"test-model\"}\n"+
			"{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"USER_INPUT\",\"message\":{\"role\":\"user\",\"content\":[{\"kind\":\"text\",\"text\":\"hi\"}]},\"timestamp\":\"2026-08-29T19:00:01Z\"}}\n")
	writeTestFile(t, sess+"/"+sid+".meta.json",
		"{\"id\":\""+sid+"\",\"model\":\"test-model\",\"updated_at\":\"2026-08-29T19:00:02Z\",\"turn_count\":1}")
	writeTestFile(t, sess+"/"+sid+"/jobs.jsonl", "")
	return sid
}

// TestDoctorEvener_RejectsStraySelectorOnSelectorlessCommands proves a
// selector passed to a selector-less command is rejected, not silently
// ignored — the CLI's own usage error. An agent passing a selector to a
// sweep command believing it scopes to one session would otherwise get a
// state-root-wide result with no signal.
func TestDoctorEvener_RejectsStraySelectorOnSelectorlessCommands(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	for _, command := range []string{"turnids", "sessions", "audit", "plugins"} {
		args := map[string]any{"command": command, "selector": "034DOESNOTMATTER0000000"}
		if command == "audit" {
			args["runbook"] = "error-loop"
		}
		_, err := rt.Exec(context.Background(), nil, args)
		if err == nil {
			t.Errorf("%s: stray selector silently ignored; want rejection", command)
		} else if !strings.Contains(err.Error(), "takes no selector") {
			t.Errorf("%s: err = %v, want takes no selector", command, err)
		}
	}
}

// TestDoctorEvener_HandlerDoesNotMutateState drives every selector-taking
// command through the registered tool Exec and asserts byte-identical session
// state before and after — the read-only regression test the proposal's risk
// table promised, extending stable_delegate_readonly_test.go's pattern from
// the library layer to the tool handler layer. (plugins is excluded: its
// store-writability probe creates and removes a temp file by design, and it
// reads the plugin store, not session state. audit is included via a
// since-based sweep over the fixture session.)
func TestDoctorEvener_HandlerDoesNotMutateState(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	// Snapshot every file under the bucket's sessions dir.
	snap := func() map[string]string {
		out := map[string]string{}
		root := filepath.Join(bucket, "sessions")
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, _ := os.ReadFile(p)
			out[p] = string(data)
			return nil
		})
		return out
	}
	before := snap()

	rt := doctorToolForTest(t, stateHome)
	commands := []map[string]any{
		{"command": "locate", "selector": sid},
		{"command": "transcript", "selector": sid},
		{"command": "transcript", "selector": sid, "health": true},
		{"command": "apilog", "selector": sid},
		{"command": "apilog", "selector": sid, "health": true},
		{"command": "jobs", "selector": sid},
		{"command": "mutations", "selector": sid},
		{"command": "watches", "selector": sid},
		{"command": "tree", "selector": sid},
		{"command": "turnids"},
		{"command": "sessions"},
		{"command": "audit", "runbook": "error-loop", "sessions": sid},
	}
	for _, args := range commands {
		if _, err := rt.Exec(context.Background(), nil, args); err != nil {
			// A command failing on this fixture is fine for the mutation
			// check as long as it fails read-only; record and continue.
			t.Logf("%v: %v (acceptable for mutation check)", args["command"], err)
		}
	}

	after := snap()
	if len(before) != len(after) {
		t.Fatalf("file count changed: before=%d after=%d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("session state mutated by doctor_evener: %s", path)
		}
	}
}

// TestDoctorEvener_SessionsRowCapDisclosed proves the structural row cap:
// a large enumeration comes back capped with truncated=true and the true
// total — valid JSON under the char limit, never a silent cut.
func TestDoctorEvener_SessionsRowCapDisclosed(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	// more sessions than the cap
	for range doctorRowCap + 25 {
		writeDoctorFixtureSession(t, bucket)
	}

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{"command": "sessions"})
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	res, ok := out.(doctorSessionsEnvelope)
	if !ok {
		t.Fatalf("result = %T, want doctorSessionsEnvelope", out)
	}
	if len(res.Sessions) != doctorRowCap {
		t.Errorf("rows = %d, want capped at %d", len(res.Sessions), doctorRowCap)
	}
	if !res.Truncated {
		t.Error("truncated = false, want true (the cut must be disclosed)")
	}
	if res.TotalRows != doctorRowCap+25 {
		t.Errorf("total_rows = %d, want %d", res.TotalRows, doctorRowCap+25)
	}
}

// TestDoctorEvener_EnumSinglesource pins the definition's command enum to
// the single-source list both the schema and the dispatcher derive from —
// the definition must carry exactly that list in its schema.
func TestDoctorEvener_EnumSinglesource(t *testing.T) {
	def := tool.DefDoctorEvener()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("definition has no properties")
	}
	cmd, ok := props["command"].(map[string]any)
	if !ok {
		t.Fatal("definition has no command property")
	}
	want := tool.DoctorEvenerCommands()
	raw, ok := cmd["enum"].([]any)
	if !ok {
		if enum, ok2 := cmd["enum"].([]string); ok2 {
			raw = make([]any, len(enum))
			for i, v := range enum {
				raw[i] = v
			}
		} else {
			t.Fatalf("command enum = %T, want slice", cmd["enum"])
		}
	}
	if len(raw) != len(want) {
		t.Fatalf("enum has %d commands, want %d", len(raw), len(want))
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Errorf("enum[%d] = %v, want %q", i, raw[i], want[i])
		}
	}
}

// TestDoctorEvener_EveryCommandDispatches pins the other half of the
// single-source claim: every command in tool.DoctorEvenerCommands() must have
// a dispatch case. Without this, adding a command to the enum compiles clean,
// surfaces in the schema, and then always fails at runtime with "unknown
// doctor command" — silent drift the schema-enum test above cannot see.
func TestDoctorEvener_EveryCommandDispatches(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)
	rt := doctorToolForTest(t, stateHome)
	for _, command := range tool.DoctorEvenerCommands() {
		args := map[string]any{"command": command}
		// Sweep commands take no selector; every other command does. The
		// fixtures won't trip every code path — this only needs each command
		// to reach its handler, not to return data.
		switch command {
		case "turnids", "sessions", "audit", "plugins":
		default:
			args["selector"] = sid
		}
		_, err := rt.Exec(context.Background(), nil, args)
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), "unknown doctor command") {
			t.Errorf("command %q has no dispatch case", command)
		}
	}
}
