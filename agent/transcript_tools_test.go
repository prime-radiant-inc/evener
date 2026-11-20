package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/testfs"
	"primeradiant.com/evener/llm"
)

func TestReadTranscriptReadsShellJobRef(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "printf hello"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("hello\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "read_transcript",
		Arguments: json.RawMessage(fmt.Sprintf(`{"transcript_ref":"job:%s"}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("read_transcript returned error: %s", res.Output)
	}
	var out readMarkdownEnvelope
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal read_transcript: %v (output: %s)", err, res.Output)
	}
	if out.TranscriptRef != "job:"+rec.JobID {
		t.Fatalf("transcript_ref = %q, want job:%s", out.TranscriptRef, rec.JobID)
	}
	if out.ContentType != "text/markdown" {
		t.Fatalf("content_type = %q, want text/markdown", out.ContentType)
	}
	if !strings.Contains(out.Content, "hello") {
		t.Fatalf("content missing shell output: %q", out.Content)
	}
	if !strings.Contains(out.Content, "total_bytes: 6") {
		t.Fatalf("content missing retained byte count: %q", out.Content)
	}
	if strings.Contains(out.Content, "dropped_bytes:") {
		t.Fatalf("complete output should not report dropped bytes: %q", out.Content)
	}
}

// TestReadTranscriptReadsShellJobRefUnderSymlinkedTemp covers the host layout
// where the process temp dir is reached through a symlink: macOS's /var →
// /private/var is the real-world case. The job store lives under the temp dir
// only when the session has no state dir, and the transcript read path must
// bound its symlink refusal to that store's own root rather than walking up
// through the host's symlinked temp prefix and refusing the read.
func TestReadTranscriptReadsShellJobRefUnderSymlinkedTemp(t *testing.T) {
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported on this host: %v", err)
	}
	t.Setenv("TMPDIR", linkRoot)

	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "printf hello"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	// The job store must live under the raw (symlinked) temp dir: the session
	// has no state dir, so this read exercises localJobTrustedRoot's
	// no-state-dir branch rather than the state-dir path. Assert it, so the
	// test cannot silently start covering a different path.
	if got := s.StateDir(); got != "" {
		t.Fatalf("session state dir = %q, want empty so the job store uses the temp fallback", got)
	}
	owner, err := identifier.JobOwnerSessionID(rec.JobID)
	if err != nil {
		t.Fatalf("JobOwnerSessionID: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(os.TempDir(), "evener-jobs", owner, "jobs.jsonl")); statErr != nil {
		t.Fatalf("job journal is not under the symlinked temp dir %q: %v", os.TempDir(), statErr)
	}

	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("hello\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "read_transcript",
		Arguments: json.RawMessage(fmt.Sprintf(`{"transcript_ref":"job:%s"}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("read_transcript under symlinked TMPDIR returned error: %s", res.Output)
	}
	var out readMarkdownEnvelope
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal read_transcript: %v (output: %s)", err, res.Output)
	}
	if !strings.Contains(out.Content, "hello") {
		t.Fatalf("content missing shell output: %q", out.Content)
	}
}

func TestReadTranscriptPublicDefinitionContinuesSessionExpansion(t *testing.T) {
	dir := newBucket(t)
	sessionID := identifier.MustNewSessionID()
	path := transcriptPath(dir, sessionID)
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("expanded", 4000)))); err != nil {
		_ = w.Close()
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	saveFindMeta(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()})

	deps := &toolDeps{stateDir: dir, sessionID: sessionID}
	registered := readTranscriptTool(deps)
	properties := registered.Definition.Parameters["properties"].(map[string]any)
	for _, name := range []string{"offset_bytes"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("read_transcript public definition is missing %q", name)
		}
	}

	first, err := registered.Exec(context.Background(), nil, map[string]any{
		"transcript_ref": sessionID,
		"expand_turn":    float64(0),
	})
	if err != nil {
		t.Fatalf("first read_transcript expansion: %v", err)
	}
	decode := func(value any) readMarkdownEnvelope {
		t.Helper()
		complete := value.(tool.StateResult)
		if !complete.RequireCompleteOutput {
			t.Fatal("markdown handoff permits partial delivery")
		}
		var envelope readMarkdownEnvelope
		if err := json.Unmarshal([]byte(complete.Output), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	firstEnvelope := decode(first)
	if firstEnvelope.Continuation == nil || firstEnvelope.Continuation.OffsetBytes != defaultExpansionBytes {
		t.Fatalf("first continuation = %+v, want offset %d", firstEnvelope.Continuation, defaultExpansionBytes)
	}

	second, err := registered.Exec(context.Background(), nil, map[string]any{
		"transcript_ref": sessionID,
		"expand_turn":    float64(0),
		"offset_bytes":   float64(firstEnvelope.Continuation.OffsetBytes),
	})
	if err != nil {
		t.Fatalf("continued read_transcript expansion: %v", err)
	}
	secondEnvelope := decode(second)
	if secondEnvelope.Expansion == nil || secondEnvelope.Expansion.OffsetBytes != defaultExpansionBytes {
		t.Fatalf("second expansion = %+v, want offset %d", secondEnvelope.Expansion, defaultExpansionBytes)
	}
}

func TestReadTranscriptPublicSurfaceOmitsLegacyReaderAndPrivateOptions(t *testing.T) {
	t.Parallel()
	registered := readTranscriptTool(&toolDeps{stateDir: t.TempDir(), sessionID: "current"})
	properties := registered.Definition.Parameters["properties"].(map[string]any)
	for _, name := range []string{"source", "attempt_id", "body", "max_bytes"} {
		if _, ok := properties[name]; ok {
			t.Fatalf("read_transcript still exposes removed option %q", name)
		}
	}

	for _, args := range []map[string]any{
		{"transcript_ref": "current", "source": "api_log"},
		{"transcript_ref": "current", "max_bytes": float64(1024)},
	} {
		if _, err := registered.Exec(context.Background(), nil, args); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("read_transcript args %#v error = %v, want unsupported-option error", args, err)
		}
	}

	names := map[string]bool{}
	for _, candidate := range transcriptTools(&toolDeps{stateDir: t.TempDir(), sessionID: "current"}) {
		names[candidate.Definition.Name] = true
	}
	if names["read_session_transcript"] {
		t.Fatal("legacy read_session_transcript remains in the callable transcript tool surface")
	}
}

// TestReadTranscriptRejectsSessionPagingArgumentsForJobRefs spells its three
// parameter names out rather than ranging over jobRefRejectedParams. Reading
// the list from the implementation would make this test agree with whatever
// the implementation happens to say — dropping an entry there would silently
// drop a subtest instead of failing one. The description test in
// read_transcript_description_test.go is the half that keys to the var, so an
// ADDED rejection still cannot land unannounced.
func TestReadTranscriptRejectsSessionPagingArgumentsForJobRefs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"range", "expand_turn", "offset_bytes"} {
		t.Run(name, func(t *testing.T) {
			value := any(float64(1))
			if name == "range" {
				value = "last:1"
			}
			_, err := execReadTranscript(context.Background(), nil, map[string]any{
				"transcript_ref": "job:job_test",
				name:             value,
			})
			if err == nil || !strings.Contains(err.Error(), "applies only to session transcript refs") {
				t.Fatalf("job %s error = %v, want session-only argument rejection", name, err)
			}
		})
	}
}

// findMetaSpec describes a synthetic SessionMeta to write for find tests.
type findMetaSpec struct {
	id              string
	name            string
	model           string
	profileID       string
	originalPrompt  string
	workingDir      string
	gitOrigin       string
	parentSessionID string
	divergenceTurn  int
	isSubagent      bool
	turnCount       int
	updated         time.Time
}

// saveFindMeta writes a SessionMeta built from spec into the bucket's sessions
// dir, without touching the transcript file.
func saveFindMeta(t *testing.T, bucketDir string, spec findMetaSpec) {
	t.Helper()
	if spec.updated.IsZero() {
		spec.updated = time.Now().UTC()
	}
	meta := schema.SessionMeta{
		ID:              spec.id,
		Name:            spec.name,
		Model:           spec.model,
		ProfileID:       spec.profileID,
		OriginalPrompt:  spec.originalPrompt,
		ParentSessionID: spec.parentSessionID,
		DivergenceTurn:  spec.divergenceTurn,
		IsSubagent:      spec.isSubagent,
		TurnCount:       spec.turnCount,
		CreatedAt:       spec.updated,
		UpdatedAt:       spec.updated,
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir:   spec.workingDir,
			GitOriginURL: spec.gitOrigin,
		},
	}
	if err := schema.SaveSessionMeta(bucketDir, meta); err != nil {
		t.Fatalf("save find meta %s: %v", spec.id, err)
	}
}

// writeFindSession writes both a one-turn transcript (carrying turnText as
// assistant text so the content scan can match it) and a SessionMeta for spec.
func writeFindSession(t *testing.T, bucketDir string, spec findMetaSpec, turnText string) {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeFindSession mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		ProfileID: spec.profileID,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write find transcript %s: %v", spec.id, err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(turnText))); err != nil {
		t.Fatalf("append find turn %s: %v", spec.id, err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close find transcript %s: %v", spec.id, err)
	}
	saveFindMeta(t, bucketDir, spec)
}

// marshalFind calls execFindSessionTranscripts and returns the JSON-marshaled
// wire bytes (the exact shape the model sees).
func marshalFind(t *testing.T, deps *toolDeps, args map[string]any) []byte {
	t.Helper()
	v, err := execFindSessionTranscripts(context.Background(), deps, args)
	if err != nil {
		t.Fatalf("execFindSessionTranscripts: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal find result: %v", err)
	}
	return b
}

// decodeEnvelope decodes the marshaled JSON into a map.
func decodeEnvelope(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return m
}

// matchesFromEnvelope extracts the "matches" array from a decoded envelope.
func matchesFromEnvelope(t *testing.T, env map[string]any) []map[string]any {
	t.Helper()
	raw, ok := env["matches"]
	if !ok {
		t.Fatal("envelope missing 'matches' key")
	}
	ifaces, ok := raw.([]any)
	if !ok {
		t.Fatalf("'matches' is not an array: %T", raw)
	}
	out := make([]map[string]any, len(ifaces))
	for i, v := range ifaces {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("match[%d] is not a map: %T", i, v)
		}
		out[i] = m
	}
	return out
}

// --- TestFind_CatalogTrimmedAndOrdered ---

// TestFind_CatalogTrimmedAndOrdered verifies that:
//   - sessions are returned newest-first
//   - the current session sorts last regardless of UpdatedAt
//   - the 5 always-present fields are present in each record
//   - fields that were removed from the spec (session_id, model, profile_id,
//     created_at, default_read, has_transcript) are absent from the wire JSON
func TestFormatSessionFindings(t *testing.T) {
	t.Parallel()
	scanned := 42
	env := findSessionsEnvelope{
		ScopeApplied: "current_project",
		Scanned:      &scanned,
		Matches: []sessionRecord{
			{
				TranscriptRef: "local:01ABC",
				Kind:          "delegate",
				Title:         "Investigate parser test",
				UpdatedAt:     time.Date(2026, 6, 14, 15, 4, 0, 0, time.UTC),
				ApproxTurns:   12,
				Project:       "evener",
				Snippets:      []snippet{{Seq: 9, Role: "assistant", Snippet: "the lexer drops trailing newlines"}},
			},
		},
	}

	out := formatSessionFindings(env)
	if strings.Contains(out, "{") || strings.Contains(out, `"transcript_ref"`) || strings.Contains(out, "matches") {
		t.Fatalf("findings must be plain text, not JSON:\n%s", out)
	}
	for _, want := range []string{"local:01ABC", "Investigate parser test", "delegate", "12", "the lexer drops trailing newlines", "current_project", "42"} {
		if !strings.Contains(out, want) {
			t.Fatalf("findings missing %q:\n%s", want, out)
		}
	}

	if got := formatSessionFindings(findSessionsEnvelope{ScopeApplied: "current_project"}); !strings.Contains(got, "No matching sessions") {
		t.Fatalf("empty findings should say so: %q", got)
	}
}

func TestFind_CurrentSessionUsesLiveTurnCount(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)

	// The current session's on-disk meta is stale mid-run (turnCount 0 here); its
	// live count is only in memory. find must report the live count for the current
	// session, not the stale disk value.
	writeFindSession(t, dir, findMetaSpec{id: "02wMz5TxvEMoJEDTDGOTil", name: "current session", updated: now, turnCount: 0}, "hello world")

	deps := &toolDeps{
		stateDir:  dir,
		sessionID: "02wMz5TxvEMoJEDTDGOTil",
		currentMeta: func() schema.SessionMeta {
			return schema.SessionMeta{ID: "02wMz5TxvEMoJEDTDGOTil", Name: "current session", TurnCount: 7, UpdatedAt: now}
		},
	}

	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps, map[string]any{})))
	var cur map[string]any
	for _, m := range matches {
		if m["is_current"] == true {
			cur = m
		}
	}
	if cur == nil {
		t.Fatalf("current session not in matches: %v", matches)
	}
	if got := cur["approx_turns"]; got != float64(7) {
		t.Fatalf("current session approx_turns = %v, want 7 (live count, not stale disk 0)", got)
	}
}

func TestFind_ProjectName_LinkedGitWorktree(t *testing.T) {
	t.Parallel()
	main, wt := gittest.NewLinkedWorktree(t)
	dir := newBucket(t)
	sid := identifier.MustNewSessionID()
	now := time.Now().UTC().Truncate(time.Second)

	writeFindSession(t, dir, findMetaSpec{
		id:         sid,
		name:       "worktree session",
		workingDir: wt,
		updated:    now,
		turnCount:  3,
	}, "worktree test")

	deps := &toolDeps{stateDir: dir, sessionID: sid}
	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps, map[string]any{})))
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	wantProject := filepath.Base(main)
	if got := matches[0]["project"]; got != wantProject {
		t.Fatalf("match project = %v, want main repo root basename %q (not worktree leaf %q)", got, wantProject, filepath.Base(wt))
	}
}

func TestFind_ProjectName_QueryModeUsesMainRepoRoot(t *testing.T) {
	t.Parallel()
	main, wt := gittest.NewLinkedWorktree(t)
	dir := newBucket(t)
	sid := identifier.MustNewSessionID()
	now := time.Now().UTC().Truncate(time.Second)

	writeFindSession(t, dir, findMetaSpec{
		id:         sid,
		name:       "worktree query session",
		workingDir: wt,
		updated:    now,
		turnCount:  3,
	}, "content search needle here")

	deps := &toolDeps{stateDir: dir, sessionID: sid}
	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps, map[string]any{"query": "content search needle"})))
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	wantProject := filepath.Base(main)
	if got := matches[0]["project"]; got != wantProject {
		t.Fatalf("query-mode project = %v, want main repo root basename %q (not worktree leaf %q)", got, wantProject, filepath.Base(wt))
	}
}

func TestFind_ProjectName_FallbacksAndOrdinaryDirectories(t *testing.T) {
	// Serial: the configured-bucket fixtures below pin XDG_STATE_HOME, and
	// t.Setenv cannot run under t.Parallel.

	// 1. Hyphenated repo name in an in-repo worktree must not be truncated to the last hyphen
	m1 := schema.SessionMeta{}
	m1.EnvInfo.WorkingDir = "/home/jesse/git/my-cool-repo/.claude/worktrees/fix-foo"
	if got := projectName(m1); got != "my-cool-repo" {
		t.Fatalf("projectName(.claude/worktrees) = %q, want %q", got, "my-cool-repo")
	}

	m2 := schema.SessionMeta{}
	m2.EnvInfo.WorkingDir = "/home/jesse/git/my-cool-repo/.worktrees/fix-foo"
	if got := projectName(m2); got != "my-cool-repo" {
		t.Fatalf("projectName(.worktrees) = %q, want %q", got, "my-cool-repo")
	}

	m3 := schema.SessionMeta{}
	m3.EnvInfo.WorkingDir = "/home/jesse/git/my-cool-repo/.git/worktrees/fix-foo"
	if got := projectName(m3); got != "my-cool-repo" {
		t.Fatalf("projectName(.git/worktrees) = %q, want %q", got, "my-cool-repo")
	}

	// 2. Ordinary non-git directory must not be altered or lossily split
	mNotes := schema.SessionMeta{}
	mNotes.EnvInfo.WorkingDir = "/home/jesse/my-notes"
	if got := projectName(mNotes); got != "my-notes" {
		t.Fatalf("projectName(ordinary dir) = %q, want %q", got, "my-notes")
	}

	// 3. WorktreeRestoreRoot / HomeDir takes precedence when in a worktree
	mRestore := schema.SessionMeta{
		WorktreePath:        "/home/jesse/evener/.worktrees/branch",
		WorktreeRestoreRoot: "/home/jesse/evener",
	}
	mRestore.EnvInfo.WorkingDir = "/home/jesse/evener/.worktrees/branch"
	if got := projectName(mRestore); got != "evener" {
		t.Fatalf("projectName(WorktreeRestoreRoot) = %q, want %q", got, "evener")
	}

	// 4. Deleted delegate worktree in Evener state dir falls back to root session meta in bucketDir
	bucketDir := t.TempDir()
	rootSID := identifier.MustNewSessionID()
	rootMeta := schema.SessionMeta{
		ID:                  rootSID,
		WorktreeRestoreRoot: "/home/jesse/my-cool-project",
	}
	rootMeta.EnvInfo.WorkingDir = "/home/jesse/my-cool-project"
	if err := schema.SaveSessionMeta(bucketDir, rootMeta); err != nil {
		t.Fatal(err)
	}

	childMeta := schema.SessionMeta{
		ID: identifier.MustNewSessionID(),
	}
	childMeta.EnvInfo.WorkingDir = filepath.Join(bucketDir, "sessions", rootSID, "worktrees", "dlg_123")
	if got := projectName(childMeta, bucketDir); got != "my-cool-project" {
		t.Fatalf("projectName(state-dir delegate worktree) = %q, want %q", got, "my-cool-project")
	}

	// 5. Unresolvable Evener state-dir worktree: see
	// TestFind_ProjectName_StateWorktreeWithoutBucketMetadata, which pins
	// XDG_STATE_HOME because the case turns on the configured state root.

	// 6. Two-pass bucket scan: GitOriginURL on sibling session takes precedence over older non-worktree home.
	// The bucket must be a real project bucket: sibling inference is scoped to those (see 10).
	twoPassDir := newConfiguredBucket(t)
	s1 := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	s1.EnvInfo.WorkingDir = "/tmp/some-plain-dir"
	if err := schema.SaveSessionMeta(twoPassDir, s1); err != nil {
		t.Fatal(err)
	}
	s2 := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	s2.EnvInfo.GitOriginURL = "git@github.com:owner/authoritative-repo.git"
	if err := schema.SaveSessionMeta(twoPassDir, s2); err != nil {
		t.Fatal(err)
	}
	mTestTwoPass := schema.SessionMeta{}
	mTestTwoPass.EnvInfo.WorkingDir = filepath.Join(twoPassDir, "worktrees", "some-bucket", "lane")
	if got := projectName(mTestTwoPass, twoPassDir); got != "authoritative-repo" {
		t.Fatalf("projectName(two-pass bucket scan) = %q, want %q", got, "authoritative-repo")
	}

	// 7. An ordinary non-Git directory that merely has a "worktrees" segment must
	// keep its own basename, not be classified as a deleted Evener state worktree.
	mOrdinaryWorktrees := schema.SessionMeta{}
	mOrdinaryWorktrees.EnvInfo.WorkingDir = "/tmp/worktrees/notes"
	if got := projectName(mOrdinaryWorktrees, newBucket(t)); got != "notes" {
		t.Fatalf("projectName(/tmp/worktrees/notes) = %q, want %q", got, "notes")
	}

	// 8. A deleted delegate worktree whose WorktreeRestoreRoot is a subdirectory of
	// the repository resolves the main root, so the project is the repo's name and
	// not the subdirectory's.
	restoreMain, _ := gittest.NewLinkedWorktree(t)
	restoreSub := filepath.Join(restoreMain, "pkg")
	if err := os.MkdirAll(restoreSub, 0o755); err != nil {
		t.Fatal(err)
	}
	restoreSID := identifier.MustNewSessionID()
	restoreMeta := schema.SessionMeta{ID: restoreSID, WorktreeRestoreRoot: restoreSub}
	restoreMeta.EnvInfo.WorkingDir = restoreSub
	restoreBucket := newConfiguredBucket(t)
	if err := schema.SaveSessionMeta(restoreBucket, restoreMeta); err != nil {
		t.Fatal(err)
	}
	restoreWD := schema.SessionMeta{}
	restoreWD.EnvInfo.WorkingDir = filepath.Join(restoreBucket, "sessions", restoreSID, "worktrees", "dlg_restore_sub")
	if got, want := projectName(restoreWD, restoreBucket), filepath.Base(restoreMain); got != want {
		t.Fatalf("projectName(delegate restore root in repo subdirectory) = %q, want repo root %q", got, want)
	}

	// 8b. A restore root that is itself a linked worktree resolves to the shared
	// main checkout from the .git pointer alone. This is the layout a process
	// would have needed Git for; the label must answer without running one.
	linkedMain, linkedWT := gittest.NewLinkedWorktree(t)
	if err := os.MkdirAll(filepath.Join(linkedWT, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkedSID := identifier.MustNewSessionID()
	linkedMeta := schema.SessionMeta{ID: linkedSID, WorktreeRestoreRoot: linkedWT}
	linkedMeta.EnvInfo.WorkingDir = filepath.Join(linkedWT, "pkg")
	linkedBucket := newConfiguredBucket(t)
	if err := schema.SaveSessionMeta(linkedBucket, linkedMeta); err != nil {
		t.Fatal(err)
	}
	linkedWD := schema.SessionMeta{}
	linkedWD.EnvInfo.WorkingDir = filepath.Join(linkedBucket, "sessions", linkedSID, "worktrees", "dlg_linked")
	if got, want := projectName(linkedWD, linkedBucket), filepath.Base(linkedMain); got != want {
		t.Fatalf("projectName(delegate restore root in a linked worktree) = %q, want main checkout %q", got, want)
	}

	// 9. Same resolution for the bucket-wide pass that reads a sibling's
	// WorktreeRestoreRoot when the worktree's own session has no meta.
	restoreSiblingBucket := newConfiguredBucket(t)
	siblingRestore := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreeRestoreRoot: restoreSub}
	if err := schema.SaveSessionMeta(restoreSiblingBucket, siblingRestore); err != nil {
		t.Fatal(err)
	}
	siblingWD := schema.SessionMeta{}
	siblingWD.EnvInfo.WorkingDir = filepath.Join(restoreSiblingBucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_sibling_sub")
	if got, want := projectName(siblingWD, restoreSiblingBucket), filepath.Base(restoreMain); got != want {
		t.Fatalf("projectName(bucket-wide sibling restore root in repo subdirectory) = %q, want repo root %q", got, want)
	}

	// 10. A flat --state-dir holds sessions from several repositories, so an
	// originless delegate worktree must not borrow a sibling's project.
	flatDir := filepath.Join(t.TempDir(), "flat-state-dir")
	if err := os.MkdirAll(flatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	flatSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	flatSibling.EnvInfo.GitOriginURL = "git@github.com:owner/other-repo.git"
	if err := schema.SaveSessionMeta(flatDir, flatSibling); err != nil {
		t.Fatal(err)
	}
	mFlat := schema.SessionMeta{}
	mFlat.EnvInfo.WorkingDir = filepath.Join(flatDir, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_flat")
	if got := projectName(mFlat, flatDir); got != "" {
		t.Fatalf("projectName(flat state-dir delegate worktree) = %q, want empty (no cross-repo sibling inference)", got)
	}

	// 11. A flat --state-dir's own managed worktree lives at
	// <stateDir>/worktrees/<projectID>/<lane>. It is still a state worktree, so it
	// must not fall through to the lane leaf as a phantom project name.
	mFlatManaged := schema.SessionMeta{}
	mFlatManaged.EnvInfo.WorkingDir = filepath.Join(flatDir, "worktrees", "a-project-0123456789", "dlg_lane")
	if got := projectName(mFlatManaged, flatDir); got != "" {
		t.Fatalf("projectName(flat state-dir managed worktree) = %q, want empty (not the lane leaf)", got)
	}

	// 12. The resolver is shared across result rows (find sorts newest first), so
	// one worktree's answer must not stand in for another's. A newer deleted
	// worktree with no recoverable project must not suppress the project of the
	// older worktree whose own session metadata names one, and the older one's
	// project must not be inherited by the next originless worktree.
	sharedDir := filepath.Join(t.TempDir(), "shared-flat-dir")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := newProjectNameResolver()
	ownerSID, bareSID := identifier.MustNewSessionID(), identifier.MustNewSessionID()
	otherBareSID := identifier.MustNewSessionID()
	ownerMeta := schema.SessionMeta{ID: ownerSID}
	ownerMeta.EnvInfo.GitOriginURL = "git@github.com:owner/owner-repo.git"
	if err := schema.SaveSessionMeta(sharedDir, ownerMeta); err != nil {
		t.Fatal(err)
	}
	sharedWD := func(sid, lane string) schema.SessionMeta {
		m := schema.SessionMeta{}
		m.EnvInfo.WorkingDir = filepath.Join(sharedDir, "sessions", sid, "worktrees", lane)
		return m
	}
	if got := r.projectName(sharedWD(bareSID, "dlg_bare"), sharedDir); got != "" {
		t.Fatalf("projectName(newer originless flat worktree) = %q, want empty", got)
	}
	if got := r.projectName(sharedWD(ownerSID, "dlg_owner"), sharedDir); got != "owner-repo" {
		t.Fatalf("projectName(older flat worktree whose owner names a project) = %q, want %q after an originless worktree resolved", got, "owner-repo")
	}
	if got := r.projectName(sharedWD(otherBareSID, "dlg_other_bare"), sharedDir); got != "" {
		t.Fatalf("projectName(originless flat worktree after an owner-resolved one) = %q, want empty (not the previous owner's project)", got)
	}

	// 12b. In a project bucket sibling inference does cache a name, so the same
	// ordering rule must hold there: a worktree whose own owner names a project
	// reports that project, never the cached sibling name.
	orderBucket := newConfiguredBucket(t)
	orderSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	orderSibling.EnvInfo.GitOriginURL = "git@github.com:owner/sibling-repo.git"
	if err := schema.SaveSessionMeta(orderBucket, orderSibling); err != nil {
		t.Fatal(err)
	}
	orderOwnerSID := identifier.MustNewSessionID()
	orderOwner := schema.SessionMeta{ID: orderOwnerSID}
	orderOwner.EnvInfo.GitOriginURL = "git@github.com:owner/its-own-repo.git"
	if err := schema.SaveSessionMeta(orderBucket, orderOwner); err != nil {
		t.Fatal(err)
	}
	orderWD := func(sid, lane string) schema.SessionMeta {
		m := schema.SessionMeta{}
		m.EnvInfo.WorkingDir = filepath.Join(orderBucket, "sessions", sid, "worktrees", lane)
		return m
	}
	if got := r.projectName(orderWD(identifier.MustNewSessionID(), "dlg_no_meta"), orderBucket); got != "sibling-repo" {
		t.Fatalf("projectName(worktree with no owner metadata) = %q, want bucket sibling %q", got, "sibling-repo")
	}
	if got := r.projectName(orderWD(orderOwnerSID, "dlg_its_own"), orderBucket); got != "its-own-repo" {
		t.Fatalf("projectName(worktree whose owner names a project) = %q, want %q, not the cached sibling name", got, "its-own-repo")
	}

	// 13. A flat --state-dir whose basename passes identifier.ValidateProjectID
	// (a ten-character numeric suffix) is still not a project bucket: it holds
	// several repositories, so an originless worktree there must not inherit a
	// sibling's project.
	lookalikeDir := filepath.Join(t.TempDir(), "evener-cache-2026111800")
	if err := identifier.ValidateProjectID(filepath.Base(lookalikeDir)); err != nil {
		t.Fatalf("fixture basename %q must pass ValidateProjectID: %v", filepath.Base(lookalikeDir), err)
	}
	if err := os.MkdirAll(lookalikeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lookalikeSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	lookalikeSibling.EnvInfo.GitOriginURL = "git@github.com:owner/unrelated-repo.git"
	if err := schema.SaveSessionMeta(lookalikeDir, lookalikeSibling); err != nil {
		t.Fatal(err)
	}
	mLookalike := schema.SessionMeta{}
	mLookalike.EnvInfo.WorkingDir = filepath.Join(lookalikeDir, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_lookalike")
	if got := projectName(mLookalike, lookalikeDir); got != "" {
		t.Fatalf("projectName(originless worktree in lookalike flat state dir) = %q, want empty", got)
	}

	// 14. A recognized project bucket keeps the sibling fallback even when its
	// name fails identifier.ValidateProjectID: transcript search still enumerates
	// legacy-named buckets, so their sessions must still get a project label.
	pinStateHome(t)
	legacyRoot, err := userdirs.DefaultStateRoot()
	if err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(legacyRoot, "projects", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(legacyDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := identifier.ValidateProjectID(filepath.Base(legacyDir)); err == nil {
		t.Fatalf("fixture bucket name %q must fail ValidateProjectID", filepath.Base(legacyDir))
	}
	legacySibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	legacySibling.EnvInfo.GitOriginURL = "git@github.com:owner/legacy-repo.git"
	if err := schema.SaveSessionMeta(legacyDir, legacySibling); err != nil {
		t.Fatal(err)
	}
	mLegacy := schema.SessionMeta{}
	mLegacy.EnvInfo.WorkingDir = filepath.Join(legacyDir, "worktrees", "dlg_legacy")
	if got := projectName(mLegacy, legacyDir); got != "legacy-repo" {
		t.Fatalf("projectName(legacy-named project bucket worktree) = %q, want %q", got, "legacy-repo")
	}

	// 15. An ordinary non-Git directory that spells a state-shaped path must keep
	// its own basename: the predicate anchors on a recognized state root, not on
	// the segment names.
	mProjectsDemo := schema.SessionMeta{}
	mProjectsDemo.EnvInfo.WorkingDir = "/home/user/projects/demo/worktrees/notes"
	if got := projectName(mProjectsDemo, newBucket(t)); got != "notes" {
		t.Fatalf("projectName(/home/user/projects/demo/worktrees/notes) = %q, want %q", got, "notes")
	}

	// 16. A managed delegate worktree under a flat --state-dir is written at
	// <stateDir>/worktrees/<projectID>/<lane> and records no owner session, so
	// bucket-wide inference is unavailable. Its recorded parent lives in the same
	// bucket, and the parent's canonical project matches the path's project
	// segment, so the parent names the project.
	managedRepo, _ := gittest.NewLinkedWorktree(t)
	managedProjectID := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(managedRepo)).ID
	managedDir := filepath.Join(t.TempDir(), "managed-flat-dir")
	managedParentID := identifier.MustNewSessionID()
	managedParent := schema.SessionMeta{ID: managedParentID, WorktreeRestoreRoot: managedRepo}
	managedParent.EnvInfo.WorkingDir = managedRepo
	if err := schema.SaveSessionMeta(managedDir, managedParent); err != nil {
		t.Fatal(err)
	}
	mManaged := schema.SessionMeta{ParentSessionID: managedParentID}
	mManaged.EnvInfo.WorkingDir = filepath.Join(managedDir, "worktrees", managedProjectID, "dlg_managed")
	if got, want := projectName(mManaged, managedDir), filepath.Base(managedRepo); got != want {
		t.Fatalf("projectName(managed delegate worktree in flat state dir) = %q, want parent's project %q", got, want)
	}

	// 16b. The parent's project must match the path's project segment: a
	// different repository's parent says nothing about this worktree.
	otherRepo, _ := gittest.NewLinkedWorktree(t)
	otherParentID := identifier.MustNewSessionID()
	otherParent := schema.SessionMeta{ID: otherParentID, WorktreeRestoreRoot: otherRepo}
	otherParent.EnvInfo.WorkingDir = otherRepo
	if err := schema.SaveSessionMeta(managedDir, otherParent); err != nil {
		t.Fatal(err)
	}
	mManagedOther := schema.SessionMeta{ParentSessionID: otherParentID}
	mManagedOther.EnvInfo.WorkingDir = filepath.Join(managedDir, "worktrees", managedProjectID, "dlg_managed_other")
	if got := projectName(mManagedOther, managedDir); got != "" {
		t.Fatalf("projectName(managed worktree whose parent names another project) = %q, want empty (the parent does not own this project)", got)
	}

	// 17. A shared --state-dir override whose path spells the state layout
	// (…/evener/projects/shared) is still not a project bucket: it lies under an
	// archive root, not under the configured state home, so an originless
	// worktree there must not inherit a sibling's label.
	sharedOverride := filepath.Join(t.TempDir(), "srv", "archive", "evener", "projects", "shared")
	if err := os.MkdirAll(filepath.Join(sharedOverride, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	sharedSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sharedSibling.EnvInfo.GitOriginURL = "git@github.com:owner/unrelated-archive-repo.git"
	if err := schema.SaveSessionMeta(sharedOverride, sharedSibling); err != nil {
		t.Fatal(err)
	}
	mSharedOverride := schema.SessionMeta{}
	mSharedOverride.EnvInfo.WorkingDir = filepath.Join(sharedOverride, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_shared")
	if got := projectName(mSharedOverride, sharedOverride); got != "" {
		t.Fatalf("projectName(originless worktree in a shared override spelling the layout) = %q, want empty (not a project bucket under the configured root)", got)
	}
}

// TestFind_ProjectName_StateWorktreeWithoutBucketMetadata covers a deleted state
// worktree whose bucket holds no metadata: the label is empty, never the lane
// leaf, and never read from another bucket the recorded path points into. It is
// not parallel because it pins XDG_STATE_HOME: the case turns on which state root
// the process is configured with, and inheriting the developer's own value would
// let the test read real metadata out of a real some-bucket directory.
func TestFind_ProjectName_StateWorktreeWithoutBucketMetadata(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	stateRoot := filepath.Join(stateHome, "evener")

	// The bucket this row was discovered in: a validated project bucket with no
	// metadata of its own.
	bucket := filepath.Join(stateRoot, "projects", "some-bucket")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	mDeleted := schema.SessionMeta{}
	mDeleted.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", "some-bucket", "revert-rail-slice")
	if got := projectName(mDeleted, bucket); got != "" {
		t.Fatalf("projectName(deleted worktree without bucket metadata) = %q, want empty", got)
	}

	// Bucket A holds a session of its own repository. Sibling inference is
	// bucket-wide, so it must answer only for worktrees recorded inside A: a row
	// in A whose recorded path points into bucket B belongs to B's repository and
	// must not be labelled with A's.
	bucketA := filepath.Join(stateRoot, "projects", "bucket-a")
	bucketB := filepath.Join(stateRoot, "projects", "bucket-b")
	for _, dir := range []string{bucketA, bucketB} {
		if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bucketASibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	bucketASibling.EnvInfo.GitOriginURL = "git@github.com:owner/bucket-a-repo.git"
	if err := schema.SaveSessionMeta(bucketA, bucketASibling); err != nil {
		t.Fatal(err)
	}
	mOtherBucketRow := schema.SessionMeta{}
	mOtherBucketRow.EnvInfo.WorkingDir = filepath.Join(bucketB, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_other_bucket")
	if got := projectName(mOtherBucketRow, bucketA); got != "" {
		t.Fatalf("projectName(row in bucket A recording a worktree in bucket B) = %q, want empty (A's sibling must not name B's worktree)", got)
	}

	// Another bucket under the same state root, reached by a symlink to a
	// workspace that does hold a meta naming a project. The recorded path points
	// into it, but the row's validated bucket is the one above, so neither the
	// owner read nor the sibling scan may follow the link.
	workspace := t.TempDir()
	otherSID := identifier.MustNewSessionID()
	workspaceMeta := schema.SessionMeta{ID: otherSID}
	workspaceMeta.EnvInfo.GitOriginURL = "git@github.com:workspace/linked-repo.git"
	if err := schema.SaveSessionMeta(workspace, workspaceMeta); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(stateRoot, "projects", "other-bucket")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspace, other); err != nil {
		t.Fatalf("symlink bucket: %v", err)
	}
	mCross := schema.SessionMeta{}
	mCross.EnvInfo.WorkingDir = filepath.Join(other, "sessions", otherSID, "worktrees", "dlg_cross")
	if got := projectName(mCross, bucket); got != "" {
		t.Fatalf("projectName(recorded path into a symlinked bucket) = %q, want empty (only the validated bucket may be read)", got)
	}
}

// TestFind_ProjectName_ForeignManagedProjectNeedsMatchingEvidence covers a
// --state-dir aimed at another repository's bucket. A managed worktree records
// its project in its path, so when that ID differs from the validated bucket's
// own ID the worktree belongs to a different repository than every session in
// the bucket. The sibling scan must not answer for it: with no evidence about
// this project, the label is omitted.
func TestFind_ProjectName_ForeignManagedProjectNeedsMatchingEvidence(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Bucket A is a real project bucket under the configured root, and it holds
	// a session of A's repository — the label the sibling scan would otherwise
	// hand to any managed worktree whose owner is gone.
	bucketA := filepath.Join(stateHome, "evener", "projects", "repo-a-0123456789")
	if err := os.MkdirAll(filepath.Join(bucketA, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket A: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo-a.git"
	if err := schema.SaveSessionMeta(bucketA, sibling); err != nil {
		t.Fatal(err)
	}

	// The managed worktree's path carries repo B's project ID, and both lanes of
	// its originless delegate are gone, so neither the owner nor the parent
	// lookup can name it. Only the path's project segment says which repository.
	repoB, _ := gittest.NewLinkedWorktree(t)
	repoBID := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB)).ID
	if repoBID == identifier.ProjectFromCanonicalPath(identifier.ResolveClean(bucketA)).ID {
		t.Fatalf("fixture error: repo B and bucket A share project ID %q", repoBID)
	}
	mForeign := schema.SessionMeta{ParentSessionID: identifier.MustNewSessionID()}
	mForeign.EnvInfo.WorkingDir = filepath.Join(bucketA, "worktrees", repoBID, "dlg_foreign")
	if got := projectName(mForeign, bucketA); got != "" {
		t.Fatalf("projectName(managed worktree of repo B in repo A's bucket) = %q, want empty (no evidence names repo B)", got)
	}

	// A sibling that does carry repo B's canonical path is evidence, and then the
	// label is allowed: the gate requires evidence, it does not forbid the answer.
	repoBSession := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	repoBSession.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucketA, repoBSession); err != nil {
		t.Fatal(err)
	}
	if got, want := projectName(mForeign, bucketA), filepath.Base(repoB); got != want {
		t.Fatalf("projectName(managed worktree once a sibling records its project) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_SymlinkedStateHomeKeepsSiblingInference covers the state
// home itself being a symlink. Startup records buckets under the physical path
// while this process's configuration keeps the link's spelling, so a bucket
// reached through the link must still be recognized as a project bucket and a
// deleted originless worktree must still take its sibling's origin.
func TestFind_ProjectName_SymlinkedStateHomeKeepsSiblingInference(t *testing.T) {
	physical := t.TempDir()
	linkParent := t.TempDir()
	linkedHome := filepath.Join(linkParent, "state-home-link")
	if err := os.Symlink(physical, linkedHome); err != nil {
		t.Fatalf("symlink state home: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", linkedHome)

	// The bucket is created through the phys path, as startup would record it.
	bucket := filepath.Join(physical, "evener", "projects", "linked-home-bucket")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/linked-home-repo.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The stored bucket path is physical while the configured spelling is the
	// link, so the two differ as strings and must still name the same home.
	m := schema.SessionMeta{}
	m.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_linked_home")
	if got := projectName(m, bucket); got != "linked-home-repo" {
		t.Fatalf("projectName(originless worktree in a bucket under a symlinked state home) = %q, want the sibling's %q", got, "linked-home-repo")
	}
}

// TestFind_ProjectName_RelativeWorktreeMarkerIsNoLabel covers a legacy record
// whose working directory spells an in-repo worktree path RELATIVELY. The marker
// branches match the path's spelling, so `old-repo/.worktrees/lane` yielded
// `old-repo` even though no directory of that name is known to this process: a
// relative path names no checkout, so its segments are not evidence about a
// repository.
func TestFind_ProjectName_RelativeWorktreeMarkerIsNoLabel(t *testing.T) {
	bucket := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join("old-repo", ".worktrees", "lane")
	if got := projectName(row, bucket); got != "" {
		t.Fatalf("projectName(row recording the relative worktree path %q) = %q, want no label: a relative record names no checkout", row.EnvInfo.WorkingDir, got)
	}
}

// newNamedCheckout builds an origin-less repository whose directory name is
// distinct, so a label that names the wrong checkout is visible in the result.
func newNamedCheckout(t *testing.T, name string) string {
	t.Helper()
	base := t.TempDir()
	gittest.Run(t, base, "init", "-q", name)
	dir := filepath.Join(base, name)
	gittest.Run(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// TestFind_ProjectName_SameProjectManagedLaneKeepsSiblingOrigin covers a bucket
// written by the runtime layout helper: <stateRoot>/projects/<projectID>. The
// bucket's own project ID is its basename, so a managed lane whose path segment
// carries that same ID belongs to the bucket's repository. Reading the ID from
// a hash of the bucket's path instead answers with a different ID for the same
// repository, which makes the lane read as foreign: its sibling is then skipped
// for want of matching evidence even when that sibling's only surviving record
// is a Git origin, and the bucket cache is bypassed for every such row.
func TestFind_ProjectName_SameProjectManagedLaneKeepsSiblingOrigin(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	checkout := newNamedCheckout(t, "same-project-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	if project.ID == "" || identifier.ValidateProjectID(project.ID) != nil {
		t.Fatalf("fixture project ID %q is not a valid bucket basename", project.ID)
	}
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if want := project.ID; filepath.Base(bucket) != want {
		t.Fatalf("runtime layout helper wrote bucket %q, want basename %q", bucket, want)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling survives only as metadata: its checkout is gone, so the only
	// thing it can still say about this project is the origin it recorded.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/same-project-repo.git"
	sibling.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-checkout")
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The managed lane records the project in its own path and no owner session,
	// so the sibling scan is what can name it.
	//
	// The resolver is shared across result rows (find sorts newest first), which
	// is the only way the bucket cache is observable: the package-level
	// projectName builds a fresh one per call.
	r := newProjectNameResolver()
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_same_project")
	const want = "same-project-repo"
	got := r.projectName(lane, bucket)
	if got != want {
		t.Fatalf("projectName(managed lane in its own project bucket) = %q, want the sibling's %q (the bucket basename is its project ID)", got, want)
	}

	// The next row in the same bucket gets the same answer. The resolver is
	// shared across result rows (find sorts newest first), so this pins that one
	// row's answer does not depend on which rows preceded it. It does NOT pin a
	// cache: the sibling-label cache was removed because a bucket-wide key could
	// serve one row's row-specific answer to another row, which was worse than the
	// reread it saved.
	nextLane := schema.SessionMeta{}
	nextLane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_same_project_next")
	if got := r.projectName(nextLane, bucket); got != want {
		t.Fatalf("projectName(second lane in the same bucket) = %q, want %q from the same sibling evidence", got, want)
	}
}

// TestFind_ProjectName_DeletedSubdirKeepsSurvivingRepoAncestor covers a session
// started in an originless repository's subdirectory and later deleted. The
// bucket still records the repository's project and the repository survives, so
// the display label is the surviving ancestor's basename — not the deleted
// directory's. A bucket that records another project keeps the recorded
// basename, and an existing non-repository directory keeps its own.
func TestFind_ProjectName_DeletedSubdirKeepsSurvivingRepoAncestor(t *testing.T) {
	stateHome := pinStateHome(t)

	checkout := newNamedCheckout(t, "deleted-subdir-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The row recorded a subdirectory of the checkout that does not exist on
	// disk, so only the spelling survives.
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(checkout, "pkg")

	wantRepo := filepath.Base(checkout)
	if got := newProjectNameResolver().projectName(row, bucket); got != wantRepo {
		t.Fatalf("projectName(deleted subdirectory %q of a surviving checkout) = %q, want the surviving checkout's %q", row.EnvInfo.WorkingDir, got, wantRepo)
	}

	// The same row under a bucket that records a DIFFERENT project keeps the
	// recorded basename: the surviving ancestor is not this row's project.
	other := newNamedCheckout(t, "other-unique-name")
	otherProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(other))
	otherBucket, err := RuntimeDirForProjectWithStateHome(otherProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper for the other project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(otherBucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir other bucket: %v", err)
	}
	if got := newProjectNameResolver().projectName(row, otherBucket); got != "pkg" {
		t.Fatalf("projectName(deleted subdirectory under a foreign project's bucket) = %q, want the recorded basename %q", got, "pkg")
	}

	// An existing non-repository directory is not relabelled by this fallback.
	notes := filepath.Join(t.TempDir(), "my-notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	notesRow := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	notesRow.EnvInfo.WorkingDir = notes
	if got := newProjectNameResolver().projectName(notesRow, bucket); got != "my-notes" {
		t.Fatalf("projectName(existing non-repository directory) = %q, want %q", got, "my-notes")
	}
}

// TestFind_ProjectName_DeletedSubdirThroughSymlinkedAncestor covers a session
// that entered through a symlink to a checkout subdirectory and named a
// directory inside it that was later removed. Resolving the recorded path as a
// whole fails at the removed tail, so a walk over the link's lexical parents
// misses the checkout the link points into and the deleted leaf would answer.
func TestFind_ProjectName_DeletedSubdirThroughSymlinkedAncestor(t *testing.T) {
	stateHome := pinStateHome(t)

	checkout := newNamedCheckout(t, "aliased-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	sub := filepath.Join(checkout, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir checkout subdirectory: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(sub, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(alias, "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(checkout); got != want {
		t.Fatalf("projectName(deleted subdirectory through a symlinked ancestor %q) = %q, want the surviving checkout's %q", row.EnvInfo.WorkingDir, got, want)
	}
}

// TestFind_ProjectName_DeletedSubdirInsideNestedCheckoutUnderWorktrees covers a
// session in a subdirectory of an independent checkout that sits under another
// checkout's ".worktrees" directory. The deleted leaf must take the nested
// checkout's name when that checkout is the bucket's project, not the name the
// spelling-based marker would give the enclosing checkout.
func TestFind_ProjectName_DeletedSubdirInsideNestedCheckoutUnderWorktrees(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "outer-repo")
	inner := filepath.Join(outer, ".worktrees", "inner")
	gittest.Run(t, outer, "init", "-q", filepath.Join(".worktrees", "inner"))
	gittest.Run(t, inner, "commit", "-q", "--allow-empty", "-m", "init")

	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(inner))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(inner, "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucket), "inner"; got != want {
		t.Fatalf("projectName(deleted subdirectory of a nested checkout under %q) = %q, want the nested checkout's %q", outer+"/.worktrees", got, want)
	}
}

// TestFind_ProjectName_DeletedForeignProjectInMismatchedBucket covers a session
// whose own record names a different project than the bucket it is stored in,
// which --state-dir pointing at the wrong bucket produces. The surviving
// ancestor is the BUCKET's project, not the row's, so the recorded basename is
// the only evidence about the row.
func TestFind_ProjectName_DeletedForeignProjectInMismatchedBucket(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "bucket-owner-repo")
	inner := filepath.Join(outer, "vendor", "inner")
	gittest.Run(t, outer, "init", "-q", filepath.Join("vendor", "inner"))
	gittest.Run(t, inner, "commit", "-q", "--allow-empty", "-m", "init")

	outerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(outer))
	innerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(inner))
	if innerProject.ID == outerProject.ID {
		t.Fatalf("fixture projects collided at %q", innerProject.ID)
	}
	bucket, err := RuntimeDirForProjectWithStateHome(outerProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if err := os.RemoveAll(inner); err != nil {
		t.Fatalf("remove inner checkout: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = innerProject.ID
	row.EnvInfo.WorkingDir = inner

	if got, want := newProjectNameResolver().projectName(row, bucket), "inner"; got != want {
		t.Fatalf("projectName(row of project %q stored in %q's bucket) = %q, want the recorded basename %q", innerProject.ID, outerProject.ID, got, want)
	}
}

// TestFind_ProjectName_ForeignProjectMarkerKeepsBasename covers the same
// mismatched bucket as above when the recorded path also spells a ".worktrees"
// marker: the marker names the bucket's project, which the row's own saved
// project contradicts, so the recorded basename must answer instead.
func TestFind_ProjectName_ForeignProjectMarkerKeepsBasename(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "marker-owner-repo")
	inner := filepath.Join(outer, ".worktrees", "inner")
	gittest.Run(t, outer, "init", "-q", filepath.Join(".worktrees", "inner"))
	gittest.Run(t, inner, "commit", "-q", "--allow-empty", "-m", "init")

	outerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(outer))
	innerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(inner))
	bucket, err := RuntimeDirForProjectWithStateHome(outerProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if err := os.RemoveAll(inner); err != nil {
		t.Fatalf("remove inner checkout: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = innerProject.ID
	row.EnvInfo.WorkingDir = inner

	if got, want := newProjectNameResolver().projectName(row, bucket), "inner"; got != want {
		t.Fatalf("projectName(deleted marker path of project %q stored in %q's bucket) = %q, want the recorded basename %q", innerProject.ID, outerProject.ID, got, want)
	}
}

// TestFind_ProjectName_DeletedSubdirThroughSymlinkToLiveLane covers a recorded
// path that reaches a live state-managed lane through a symlinked ancestor and
// names a subdirectory of the lane that was removed. The lane's own pointer
// still names the checkout, so the deleted leaf must not answer.
func TestFind_ProjectName_DeletedSubdirThroughSymlinkToLiveLane(t *testing.T) {
	stateHome := pinStateHome(t)

	main := newNamedCheckout(t, "deferred-owner-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(main))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	lane := filepath.Join(bucket, "worktrees", project.ID, "dlg_aliased_lane")
	gittest.Run(t, main, "worktree", "add", "-q", lane, "-b", "lane-aliased")

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(lane, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(alias, "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(main); got != want {
		t.Fatalf("projectName(deleted subdirectory through a symlink to a live lane %q) = %q, want the lane checkout's %q", row.EnvInfo.WorkingDir, got, want)
	}
}

// TestFind_ProjectName_ForeignBucketKeepsRowsOwnMarker covers a row stored in
// another project's bucket whose recorded path spells an in-repository worktree
// of its OWN checkout, removed since. The marker names that checkout, which the
// row's saved project confirms, so the marker's name must answer even though the
// bucket's project differs.
func TestFind_ProjectName_ForeignBucketKeepsRowsOwnMarker(t *testing.T) {
	stateHome := pinStateHome(t)

	repo := newNamedCheckout(t, "worktree-owner-repo")
	feature := filepath.Join(repo, ".worktrees", "feature")
	gittest.Run(t, repo, "worktree", "add", "-q", feature, "-b", "feature")

	repoProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	other := newNamedCheckout(t, "unrelated-bucket-repo")
	otherProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(other))
	if otherProject.ID == repoProject.ID {
		t.Fatalf("fixture projects collided at %q", repoProject.ID)
	}
	bucket, err := RuntimeDirForProjectWithStateHome(otherProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if err := os.RemoveAll(feature); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	// The checkout's own Git metadata goes too. While it is there the surviving
	// checkout lookup answers from the walk before the marker is asked, and this
	// test is about the marker's saved-ID check.
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("remove checkout git dir: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = repoProject.ID
	row.EnvInfo.WorkingDir = feature

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(repo); got != want {
		t.Fatalf("projectName(removed worktree %q of the row's own project, stored in another project's bucket) = %q, want the marker's checkout %q", feature, got, want)
	}
}

// TestFind_ProjectName_LegacyBucketKeepsWorktreeMarker covers the same removed
// worktree as above in a legacy or shared state dir, which records no project,
// and a row that saved none either. Nothing records an ID, so nothing contradicts
// the marker's spelling and its checkout must still answer.
func TestFind_ProjectName_LegacyBucketKeepsWorktreeMarker(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	repo := newNamedCheckout(t, "legacy-bucket-repo")
	feature := filepath.Join(repo, ".worktrees", "feature")
	gittest.Run(t, repo, "worktree", "add", "-q", feature, "-b", "feature")
	if err := os.RemoveAll(feature); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = feature

	if got, want := projectName(row, stateDir), filepath.Base(repo); got != want {
		t.Fatalf("projectName(removed worktree %q in a state dir that records no project) = %q, want the marker's checkout %q", feature, got, want)
	}
}

// TestFind_ProjectName_AliasedLaneKeepsItsDeferredOrder covers a lane reached
// only through a symlinked working directory after its checkout was pruned: the
// alias is the only lane record, and the row's recorded restore root names the
// lane's project. That root is the row's own evidence and must answer before a
// sibling's saved origin, which is bucket-wide inference.
func TestFind_ProjectName_AliasedLaneKeepsItsDeferredOrder(t *testing.T) {
	stateHome := pinStateHome(t)

	main, _ := gittest.NewLinkedWorktree(t)
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(main))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	lane := filepath.Join(bucket, "worktrees", project.ID, "dlg_aliased")
	gittest.Run(t, main, "worktree", "add", "-q", lane, "-b", "lane-aliased")
	// The lane's pointer goes, so the bounded walk cannot name the checkout.
	if err := os.Remove(filepath.Join(lane, ".git")); err != nil {
		t.Fatalf("remove lane pointer: %v", err)
	}
	// A sibling of another repository, saved in the same bucket, is admissible
	// bucket-wide evidence and must not outrank the row's own restore root.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "https://example.com/sibling-origin.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}
	// The owner checkout goes too, so its recorded path is the row's only
	// surviving statement about the project.
	if err := os.RemoveAll(main); err != nil {
		t.Fatalf("remove owner checkout: %v", err)
	}

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(lane, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreeRestoreRoot: main}
	row.EnvInfo.WorkingDir = filepath.Join(alias, "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(main); got != want {
		t.Fatalf("projectName(aliased pruned lane with restore root %q) = %q, want the row's own %q, not the sibling's origin", main, got, want)
	}
}

// TestFind_ProjectName_LegacyBucketSkipsTheOrdinaryWalk covers an originless row
// in a state dir that records no project. No walk can be accepted without a
// project ID to check it against, so none is made: the diagnostic counter stays
// at zero and the recorded basename answers.
func TestFind_ProjectName_LegacyBucketSkipsTheOrdinaryWalk(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	checkout := newNamedCheckout(t, "unwalked-repo")

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(checkout, "gone")

	r := newProjectNameResolver()
	if got, want := r.projectName(row, stateDir), "gone"; got != want {
		t.Fatalf("projectName(deleted subdirectory of %q in a state dir that records no project) = %q, want %q", checkout, got, want)
	}
	if r.mainRootLookups != 0 {
		t.Fatalf("labelling a row that records no project and sits in a state dir that records none took %d structural lookups, want 0: the walk cannot be accepted", r.mainRootLookups)
	}
}

// TestFind_ProjectName_AliasedBucketLaneKeepsItsProjectID covers a recorded path
// that reaches a managed lane's project segment through a symlink to the bucket's
// worktrees directory, after the lane and its project directory are gone. The
// project ID the path spells survives the pruning, so the lane is foreign to a
// bucket that records another project and an unrelated sibling cannot label it.
func TestFind_ProjectName_AliasedBucketLaneKeepsItsProjectID(t *testing.T) {
	stateHome := pinStateHome(t)

	laneProject := identifier.ProjectFromCanonicalPath(filepath.Join(t.TempDir(), "lane-repo"))
	if err := identifier.ValidateProjectID(laneProject.ID); err != nil {
		t.Fatalf("fixture lane project ID %q: %v", laneProject.ID, err)
	}
	holder := newNamedCheckout(t, "holder-repo")
	holderProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(holder))
	bucket, err := RuntimeDirForProjectWithStateHome(holderProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "worktrees"), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "https://example.com/unrelated-sibling.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(bucket, "worktrees"), alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(alias, laneProject.ID, "dlg_aliased")

	if got := newProjectNameResolver().projectName(row, bucket); got != "" {
		t.Fatalf("projectName(lane of project %q reached through an alias into %q's bucket) = %q, want empty: the lane is foreign to the bucket", laneProject.ID, holderProject.ID, got)
	}
}

// TestFind_ProjectName_AliasedWorktreeMarkerKeepsTheRepo covers a recorded path
// that reaches an in-repository worktree through a symlink and names a removed
// subdirectory of it, with the repository's Git metadata gone. The marker in the
// resolved spelling names the row's own project, so it must answer.
func TestFind_ProjectName_AliasedWorktreeMarkerKeepsTheRepo(t *testing.T) {
	repo := newNamedCheckout(t, "aliased-marker-repo")
	feature := filepath.Join(repo, ".worktrees", "feature")
	gittest.Run(t, repo, "worktree", "add", "-q", feature, "-b", "feature")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("remove checkout git dir: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(feature, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = project.ID
	row.EnvInfo.WorkingDir = filepath.Join(alias, "pkg")

	if got, want := newProjectNameResolver().projectName(row, filepath.Join(t.TempDir(), "unrelated-bucket")), filepath.Base(repo); got != want {
		t.Fatalf("projectName(deleted subdirectory of the aliased worktree %q) = %q, want the marker's checkout %q", feature, got, want)
	}
}

// TestFind_ProjectName_SymlinkedWorktreesDirKeepsTheMarker covers a checkout
// whose ".worktrees" directory is a symlink to external storage. Resolving the
// recorded path as a whole follows that link, so the resolved spelling no longer
// spells a marker; the recorded spelling still does, and the checkout it names
// is the row's own project.
func TestFind_ProjectName_SymlinkedWorktreesDirKeepsTheMarker(t *testing.T) {
	repo := newNamedCheckout(t, "symlinked-worktrees-repo")
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(repo, ".worktrees")); err != nil {
		t.Fatalf("symlink .worktrees: %v", err)
	}
	feature := filepath.Join(external, "feature")
	gittest.Run(t, repo, "worktree", "add", "-q", feature, "-b", "feature")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	// The worktree's own pointer goes, so no Git walk can name the checkout.
	if err := os.Remove(filepath.Join(feature, ".git")); err != nil {
		t.Fatalf("remove worktree pointer: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = project.ID
	row.EnvInfo.WorkingDir = filepath.Join(repo, ".worktrees", "feature", "pkg")

	if got, want := newProjectNameResolver().projectName(row, filepath.Join(t.TempDir(), "unrelated-bucket")), filepath.Base(repo); got != want {
		t.Fatalf("projectName(deleted subdirectory %q under a symlinked .worktrees directory) = %q, want the recorded spelling's checkout %q", row.EnvInfo.WorkingDir, got, want)
	}
}

// TestFind_ProjectName_SymlinkInsideOneLaneIntoAnother covers a recorded path
// that starts in one managed lane and continues through a symlink into a second,
// live lane, naming a directory of the second that was removed. The recorded
// spelling names the first lane, whose bounded walk refuses the resolved path;
// the lane the path really sits in is the second, and its pointer names the
// checkout.
func TestFind_ProjectName_SymlinkInsideOneLaneIntoAnother(t *testing.T) {
	stateHome := pinStateHome(t)

	mainA := newNamedCheckout(t, "lane-a-repo")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(mainA))
	bucketA, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucketA, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket A: %v", err)
	}
	laneA := filepath.Join(bucketA, "worktrees", projectA.ID, "dlg_lane_a")
	gittest.Run(t, mainA, "worktree", "add", "-q", laneA, "-b", "lane-a")

	mainB := newNamedCheckout(t, "lane-b-repo")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(mainB))
	bucketB, err := RuntimeDirForProjectWithStateHome(projectB, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucketB, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket B: %v", err)
	}
	laneB := filepath.Join(bucketB, "worktrees", projectB.ID, "dlg_lane_b")
	gittest.Run(t, mainB, "worktree", "add", "-q", laneB, "-b", "lane-b")
	if err := os.Symlink(laneB, filepath.Join(laneA, "link")); err != nil {
		t.Fatalf("symlink into lane B: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(laneA, "link", "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucketA), filepath.Base(mainB); got != want {
		t.Fatalf("projectName(path through a symlink into lane B %q) = %q, want lane B's checkout %q", row.EnvInfo.WorkingDir, got, want)
	}
}

// TestFind_ProjectName_SavedLaneOutranksTheEnclosingCheckout covers a lane for an
// independent checkout B nested inside checkout A, stored in A's bucket, after B
// and its lane are gone. A walk from the dead restore root finds A and A's bucket
// accepts it, but the lane path records B and B's own restore root still names
// the project.
func TestFind_ProjectName_SavedLaneOutranksTheEnclosingCheckout(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "enclosing-repo")
	inner := filepath.Join(outer, "vendor", "nested")
	gittest.Run(t, outer, "init", "-q", filepath.Join("vendor", "nested"))
	gittest.Run(t, inner, "commit", "-q", "--allow-empty", "-m", "init")
	outerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(outer))
	innerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(inner))
	bucket, err := RuntimeDirForProjectWithStateHome(outerProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	lane := filepath.Join(bucket, "worktrees", innerProject.ID, "dlg_nested")
	gittest.Run(t, inner, "worktree", "add", "-q", lane, "-b", "nested-lane")
	if err := os.RemoveAll(inner); err != nil {
		t.Fatalf("remove nested checkout: %v", err)
	}
	if err := os.RemoveAll(lane); err != nil {
		t.Fatalf("remove lane: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreePath: lane, WorktreeRestoreRoot: inner}
	row.EnvInfo.WorkingDir = inner

	if got, want := newProjectNameResolver().projectName(row, bucket), "nested"; got != want {
		t.Fatalf("projectName(lane of %q inside %q's bucket) = %q, want the lane project's restore root %q", innerProject.ID, outerProject.ID, got, want)
	}
}

// TestFind_ProjectName_RecordedSubdirOfSurvivingCheckoutLabelsTheLane covers a
// session that entered a managed lane from a subdirectory of a surviving
// checkout: the subdirectory and the lane are both gone, and the surviving
// checkout's canonical project ID is the one the lane path records. The row's
// own lane evidence cannot hash the dead subdirectory to that ID, so the
// verified surviving checkout is what names the lane.
func TestFind_ProjectName_RecordedSubdirOfSurvivingCheckoutLabelsTheLane(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "surviving-root-repo")
	outerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(outer))
	bucket, err := RuntimeDirForProjectWithStateHome(outerProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	recorded := filepath.Join(outer, "pkg")
	if err := os.MkdirAll(recorded, 0o755); err != nil {
		t.Fatalf("mkdir recorded subdirectory: %v", err)
	}
	lane := filepath.Join(bucket, "worktrees", outerProject.ID, "dlg_sub")
	gittest.Run(t, outer, "worktree", "add", "-q", lane, "-b", "lane-sub")
	if err := os.RemoveAll(recorded); err != nil {
		t.Fatalf("remove recorded subdirectory: %v", err)
	}
	if err := os.RemoveAll(lane); err != nil {
		t.Fatalf("remove lane: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreePath: lane, WorktreeRestoreRoot: recorded}
	row.EnvInfo.WorkingDir = recorded

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(outer); got != want {
		t.Fatalf("projectName(lane %q entered from %q, both gone) = %q, want the surviving checkout %q", lane, recorded, got, want)
	}
}

// TestFind_ProjectName_RecordedCheckoutAnswersBeforeTheEnclosingWalk covers a
// lane in the bucket of an independent nested checkout: the lane and the
// checkout are both gone, and hashing the recorded checkout names the lane's
// project. Reading the enclosing repository's Git metadata must not happen to
// reach that answer.
func TestFind_ProjectName_RecordedCheckoutAnswersBeforeTheEnclosingWalk(t *testing.T) {
	stateHome := pinStateHome(t)

	outer := newNamedCheckout(t, "enclosing-walk-repo")
	inner := filepath.Join(outer, "vendor", "nested")
	gittest.Run(t, outer, "init", "-q", filepath.Join("vendor", "nested"))
	gittest.Run(t, inner, "commit", "-q", "--allow-empty", "-m", "init")
	innerProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(inner))
	bucket, err := RuntimeDirForProjectWithStateHome(innerProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	lane := filepath.Join(bucket, "worktrees", innerProject.ID, "dlg_walk")
	gittest.Run(t, inner, "worktree", "add", "-q", lane, "-b", "lane-walk")
	if err := os.RemoveAll(inner); err != nil {
		t.Fatalf("remove nested checkout: %v", err)
	}
	if err := os.RemoveAll(lane); err != nil {
		t.Fatalf("remove lane: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreePath: lane, WorktreeRestoreRoot: inner}
	row.EnvInfo.WorkingDir = inner

	r := newProjectNameResolver()
	if got, want := r.projectName(row, bucket), filepath.Base(inner); got != want {
		t.Fatalf("projectName(lane of gone checkout %q inside %q's bucket) = %q, want the recorded checkout's project %q", inner, outer, got, want)
	}
	if r.mainRootLookups != 0 {
		t.Fatalf("labelling a lane whose own recorded checkout named the project took %d structural lookups, want 0: the row's own recorded checkout named the project, so the enclosing repository's Git metadata must not be read", r.mainRootLookups)
	}
}

// TestFind_ProjectName_RecordedPathBecameAFile covers a recorded subdirectory that
// a branch switch replaced with a regular file. Probing ".git" beneath the file
// is ENOTDIR, not "no repository here", so the walk starts at the closest
// surviving directory and still names the checkout above it.
func TestFind_ProjectName_RecordedPathBecameAFile(t *testing.T) {
	stateHome := pinStateHome(t)

	checkout := newNamedCheckout(t, "file-for-subdir-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	recorded := filepath.Join(checkout, "pkg")
	if err := os.WriteFile(recorded, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write file over the recorded subdirectory: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = recorded

	if got, want := newProjectNameResolver().projectName(row, bucket), filepath.Base(checkout); got != want {
		t.Fatalf("projectName(recorded subdirectory %q that is now a file) = %q, want the surviving checkout %q", recorded, got, want)
	}
}

// TestFind_ProjectName_SymlinkOutOfLaneToOrdinaryCheckout covers a symlink inside
// managed lane A that points at an ordinary checkout B, with the recorded
// subdirectory of B removed. The path is not lane A's evidence: it resolves into
// B, which is the row's own saved project and must be named.
func TestFind_ProjectName_SymlinkOutOfLaneToOrdinaryCheckout(t *testing.T) {
	stateHome := pinStateHome(t)

	mainA := newNamedCheckout(t, "lane-owner-repo")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(mainA))
	bucketA, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucketA, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	lane := filepath.Join(bucketA, "worktrees", projectA.ID, "dlg_out")
	gittest.Run(t, mainA, "worktree", "add", "-q", lane, "-b", "lane-out")

	target := newNamedCheckout(t, "ordinary-target-repo")
	targetProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(target))
	if err := os.Symlink(target, filepath.Join(lane, "link")); err != nil {
		t.Fatalf("symlink out of the lane: %v", err)
	}

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.Config.MemoryProjectID = targetProject.ID
	row.EnvInfo.WorkingDir = filepath.Join(lane, "link", "pkg")

	if got, want := newProjectNameResolver().projectName(row, bucketA), filepath.Base(target); got != want {
		t.Fatalf("projectName(path through a symlink out of lane A into %q) = %q, want the target checkout %q", target, got, want)
	}
}

// TestFind_ProjectName_StateLaneStopsGitWalkAtLaneRoot covers a shared --state-dir
// that lives inside checkout A while holding a managed delegate lane for
// checkout B. Deleting the originless lane leaves nothing at the lane path, so a
// Git walk that climbs from it finds A's .git and answers with A — before the
// same-bucket parent lookup, which knows the lane belongs to B, is consulted.
// The lane is identified from the state directory first and the walk stops at
// the lane root; a lane that still exists keeps resolving through its pointer.
func TestFind_ProjectName_StateLaneStopsGitWalkAtLaneRoot(t *testing.T) {
	checkoutA := newNamedCheckout(t, "checkout-a")
	checkoutB := newNamedCheckout(t, "checkout-b")

	// The shared override sits inside A and spells no Evener layout at all: it is
	// recognized as this row's bucket, and nothing else.
	stateDir := filepath.Join(checkoutA, "shared-state")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID, WorktreeRestoreRoot: checkoutB}
	parent.EnvInfo.WorkingDir = checkoutB
	if err := schema.SaveSessionMeta(stateDir, parent); err != nil {
		t.Fatal(err)
	}

	// The delegate's own metadata is gone with its lane; only the parent remains.
	delegateID := identifier.MustNewSessionID()
	lanePath := filepath.Join(stateDir, "sessions", delegateID, "worktrees", "dlg_checkout_b")
	deletedLane := schema.SessionMeta{ParentSessionID: parentID}
	deletedLane.EnvInfo.WorkingDir = lanePath
	const wantB = "checkout-b"
	if got := projectName(deletedLane, stateDir); got != wantB {
		t.Fatalf("projectName(deleted lane for checkout B in a state dir inside checkout A) = %q, want the parent's %q, not the enclosing checkout %q", got, wantB, filepath.Base(checkoutA))
	}

	// The confinement must not break a lane that still exists: its own .git
	// pointer names the main checkout, and that is the label.
	gittest.Run(t, checkoutB, "worktree", "add", "-q", lanePath, "-b", "lane-b")
	liveLane := schema.SessionMeta{ParentSessionID: parentID}
	liveLane.EnvInfo.WorkingDir = lanePath
	if got, gotWant := projectName(liveLane, stateDir), wantB; got != gotWant {
		t.Fatalf("projectName(live lane for checkout B) = %q, want %q", got, gotWant)
	}
}

// TestFind_ProjectName_BucketMatchingLaneExcludesConflictingSibling covers a
// lane whose project segment IS its bucket's ID, so it matches by name. That
// match alone is not evidence of ownership: another repository's session can be
// given this bucket with --state-dir. A sibling that positively resolves to a
// different project is about another repository, so it must be neither selected
// nor cached — caching is what would turn one wrong label into every later
// row's label. A sibling with no project evidence still answers.
func TestFind_ProjectName_BucketMatchingLaneExcludesConflictingSibling(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	checkout := newNamedCheckout(t, "owning-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// Another repository's live checkout, saved into this bucket as a newer
	// session — what --state-dir aimed at the wrong bucket produces.
	intruderRepo := newNamedCheckout(t, "intruder-repo")
	intruder := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	intruder.EnvInfo.GitOriginURL = "git@github.com:other/intruder-repo.git"
	intruder.EnvInfo.WorkingDir = intruderRepo
	if err := schema.SaveSessionMeta(bucket, intruder); err != nil {
		t.Fatal(err)
	}

	r := newProjectNameResolver()
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_bucket_matching")

	// With the only sibling naming another project, the lane has no evidence for
	// its own project and must not take the intruder's.
	if got := r.projectName(lane, bucket); got != "" {
		t.Fatalf("projectName(bucket-matching lane with only a conflicting sibling) = %q, want empty (the sibling names another project)", got)
	}

	// The refusal must not have been cached: a later row must still be able to
	// take a genuine sibling's evidence, and this bucket now gains one. Its
	// recorded checkout is the bucket's own repository, so its project evidence
	// matches the lane's segment.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/owning-repo.git"
	sibling.EnvInfo.WorkingDir = checkout
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}
	nextLane := schema.SessionMeta{}
	nextLane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_bucket_matching_next")
	if got, want := r.projectName(nextLane, bucket), "owning-repo"; got != want {
		t.Fatalf("projectName(next bucket-matching lane once a matching sibling exists) = %q, want %q (the empty answer must not have been cached)", got, want)
	}
}

// TestFind_ProjectName_CrossBucketParentNeedsLaneEvidence covers a row in bucket
// A recording a deleted session-owned lane in bucket B. A session-owned path
// carries no managed project ID, so without evidence the parent lookup would
// accept a parent in A — whose metadata describes A's repository — and return A
// before the cross-bucket guard is reached. The parent must be tied to the lane
// it names: same bucket, a matching managed project ID, or being that lane's own
// recorded owner.
func TestFind_ProjectName_CrossBucketParentNeedsLaneEvidence(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	bucketA := filepath.Join(stateHome, "evener", "projects", "repo-a-0123456789")
	bucketB := filepath.Join(stateHome, "evener", "projects", "repo-b-0123456789")
	for _, dir := range []string{bucketA, bucketB} {
		if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// The parent lives in A and names A's repository. It is not the lane's owner:
	// the lane's path encodes a different session as its owner.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID, WorktreeRestoreRoot: newNamedCheckout(t, "repo-a")}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-a.git"
	if err := schema.SaveSessionMeta(bucketA, parent); err != nil {
		t.Fatal(err)
	}

	ownerInB := identifier.MustNewSessionID()
	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(bucketB, "sessions", ownerInB, "worktrees", "dlg_in_b")
	if got := projectName(row, bucketA); got != "" {
		t.Fatalf("projectName(row in A recording a deleted lane in B with a parent in A) = %q, want empty (that parent is not evidence about B's lane)", got)
	}

	// Same-bucket parent recovery is preserved: a parent in the row's own bucket
	// does name the lane's project.
	sameParentID := identifier.MustNewSessionID()
	sameParent := schema.SessionMeta{ID: sameParentID, WorktreeRestoreRoot: newNamedCheckout(t, "repo-a")}
	sameParent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-a.git"
	if err := schema.SaveSessionMeta(bucketA, sameParent); err != nil {
		t.Fatal(err)
	}
	sameBucketRow := schema.SessionMeta{ParentSessionID: sameParentID}
	sameBucketRow.EnvInfo.WorkingDir = filepath.Join(bucketA, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_in_a")
	if got, want := projectName(sameBucketRow, bucketA), "repo-a"; got != want {
		t.Fatalf("projectName(deleted lane in A with a parent in A) = %q, want %q", got, want)
	}

	// The lane's own recorded owner is evidence in any bucket: when the parent is
	// exactly the session the lane's path names, it does describe this lane.
	ownerParent := schema.SessionMeta{ID: ownerInB, WorktreeRestoreRoot: newNamedCheckout(t, "repo-b")}
	ownerParent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	if err := schema.SaveSessionMeta(bucketA, ownerParent); err != nil {
		t.Fatal(err)
	}
	ownerRow := schema.SessionMeta{ParentSessionID: ownerInB}
	ownerRow.EnvInfo.WorkingDir = filepath.Join(bucketB, "sessions", ownerInB, "worktrees", "dlg_in_b_owned")
	if got, want := projectName(ownerRow, bucketA), "repo-b"; got != want {
		t.Fatalf("projectName(deleted lane whose recorded owner is its parent) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_CacheKeyedByEligibility covers the cache key. The
// resolver is shared across result rows, and one bucket can answer differently
// depending on the row: a session-owned lane with no project segment accepts any
// sibling, while a managed lane whose path names a project excludes siblings
// that identify a different one. Caching under the bucket alone lets the first
// row's answer be served to the second, so the same managed lane gets a
// different label depending on which rows came before it — and the wrong one.
func TestFind_ProjectName_CacheKeyedByEligibility(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Bucket A, from the runtime layout helper, holding one sibling from another
	// repository: the only thing the bucket can say about a lane is B's project.
	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	repoB := newNamedCheckout(t, "repo-b")
	other := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	other.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	other.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucket, other); err != nil {
		t.Fatal(err)
	}

	// The managed lane names repo A, so only evidence about A may answer for it,
	// and there is none. Alone it must be labelled empty.
	managedLane := schema.SessionMeta{}
	managedLane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_managed")

	// A session-owned lane in the same bucket has no project segment, so it
	// accepts the sibling and reports repo B.
	sessionLane := schema.SessionMeta{}
	sessionLane.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_session")

	if got, want := newProjectNameResolver().projectName(managedLane, bucket), ""; got != want {
		t.Fatalf("projectName(managed lane for repo A, alone) = %q, want empty", got)
	}

	// Now the ordering that used to leak: the permissive row answers first and
	// caches repo B under the bucket. The managed lane must not read that.
	r := newProjectNameResolver()
	if got, want := r.projectName(sessionLane, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(session-owned lane, no project segment) = %q, want the sibling's %q", got, want)
	}
	if got := r.projectName(managedLane, bucket); got != "" {
		t.Fatalf("projectName(managed lane for repo A after a session-owned row) = %q, want empty (it must get the same answer it gets alone)", got)
	}

	// The reverse order must also hold: a managed lane's answer is not served to
	// the permissive row.
	r2 := newProjectNameResolver()
	if got := r2.projectName(managedLane, bucket); got != "" {
		t.Fatalf("projectName(managed lane first) = %q, want empty", got)
	}
	if got, want := r2.projectName(sessionLane, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(session-owned lane after a managed row) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_OrdinaryDirectoryIsItsOwnProject covers an ordinary
// non-Git directory used as a sibling. It has no .git, so a check that treats
// "no Git root" as unknown lets its basename stand in for a managed lane's
// project — exactly what a session launched from such a directory with
// --state-dir aimed at another repository's bucket would supply. The directory
// does have a canonical project of its own, and it is not the lane's.
func TestFind_ProjectName_OrdinaryDirectoryIsItsOwnProject(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// A plain directory, no .git, named so its basename is unmistakable.
	plain := filepath.Join(t.TempDir(), "unrelated-scratch-dir")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir plain: %v", err)
	}
	plainSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	plainSibling.EnvInfo.WorkingDir = plain
	if err := schema.SaveSessionMeta(bucket, plainSibling); err != nil {
		t.Fatal(err)
	}

	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_plain_sibling")
	if got := projectName(lane, bucket); got != "" {
		t.Fatalf("projectName(managed lane whose only sibling is an unrelated plain directory) = %q, want empty (that directory is its own project)", got)
	}

	// Two ordinary directories that share a basename are still two projects: a
	// project ID names the whole canonical path, not its last segment. Pin that,
	// because the exclusion above rests on it. A lane whose own project segment
	// is one of these directories must not exclude the other, even though both
	// are "notes".
	one := filepath.Join(t.TempDir(), "notes")
	two := filepath.Join(t.TempDir(), "notes")
	for _, dir := range []string{one, two} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	oneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(one))
	twoProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(two))
	if oneProject.ID == twoProject.ID {
		t.Fatalf("two directories named %q share project ID %q, so a basename could stand in for a path", filepath.Base(one), oneProject.ID)
	}
	oneBucket, err := RuntimeDirForProjectWithStateHome(oneProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(oneBucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	sameNameSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sameNameSibling.EnvInfo.WorkingDir = two
	if err := schema.SaveSessionMeta(oneBucket, sameNameSibling); err != nil {
		t.Fatal(err)
	}
	sameNameLane := schema.SessionMeta{}
	sameNameLane.EnvInfo.WorkingDir = filepath.Join(oneBucket, "worktrees", oneProject.ID, "dlg_same_basename")
	// The sibling is a different project despite the shared basename, so it may
	// not name this lane: empty. A comparison on basenames would call them equal
	// and answer "notes" from a directory the lane does not belong to.
	if got := projectName(sameNameLane, oneBucket); got != "" {
		t.Fatalf("projectName(lane for %s with a sibling directory %s) = %q, want empty (same basename, different project)", one, two, got)
	}
}

// TestFind_ProjectName_SiblingOriginSurvivesDeletedLaneInsideCheckout covers the
// state home living inside checkout A while a sibling belongs to repo B. When
// the sibling's own lane is deleted, an unbounded walk from its recorded path
// finds A's .git and rejects the sibling as conflicting evidence, so the label
// is omitted — even though its saved origin is the only surviving evidence, and
// it agrees with the row being labelled. The lane boundary applies to sibling
// metadata too.
func TestFind_ProjectName_SiblingOriginSurvivesDeletedLaneInsideCheckout(t *testing.T) {
	checkoutA := newNamedCheckout(t, "checkout-a")
	stateHome := filepath.Join(checkoutA, "nested-state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	// A project bucket under the configured root, which is now inside checkout A.
	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucket := filepath.Join(stateHome, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling's lane is a state-managed one whose checkout is gone: only its
	// metadata and its saved origin remain.
	siblingLane := filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_gone")
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	sibling.EnvInfo.WorkingDir = siblingLane
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The row is a managed lane for the same repository B, so the sibling's
	// origin is evidence that names the row's own project.
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_own")
	if got, want := projectName(row, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(managed lane with a deleted-lane sibling inside checkout A) = %q, want the sibling's saved origin %q", got, want)
	}
}

// TestFind_ProjectName_DeletedLaneRecordsItsProject covers a sibling whose
// managed lane has been deleted. Deleting a worktree deletes the checkout, not
// the project the lane belonged to: <stateDir>/worktrees/<projectID>/<lane>
// still records it. A check that reads only the surviving checkout drops that
// record, so a lane from repository B, sitting in repository A's bucket through
// --state-dir, lends B's saved origin to an A worktree that has no usable
// parent — and the bucket cache carries it to the rows that follow.
func TestFind_ProjectName_DeletedLaneRecordsItsProject(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// A sibling of repository B whose lane has been deleted: only its metadata,
	// the lane path it recorded, and its saved origin remain.
	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	sibling.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_gone")
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// An originless worktree of repository A with no parent. The only thing in
	// this bucket that could answer is B's recorded lane, and B is not A.
	rowA := schema.SessionMeta{}
	rowA.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_a")
	if got := projectName(rowA, bucket); got != "" {
		t.Fatalf("projectName(worktree for repo A, with a sibling whose deleted lane records repo B) = %q, want empty", got)
	}

	// B's own worktree in the same bucket still reads B's origin: the deleted
	// lane's recorded project is evidence for B, and the empty answer above is
	// not cached over it.
	rowB := schema.SessionMeta{}
	rowB.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_b")
	if got, want := projectName(rowB, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(worktree for repo B, same bucket) = %q, want the sibling's saved origin %q", got, want)
	}

	// The same two rows through one shared resolver, in the order that leaked:
	// the conflicting sibling must be neither selected nor cached.
	r := newProjectNameResolver()
	if got := r.projectName(rowA, bucket); got != "" {
		t.Fatalf("projectName(worktree for repo A first) = %q, want empty", got)
	}
	if got, want := r.projectName(rowB, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(worktree for repo B after repo A) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_OwnerRestoreLaneDoesNotInheritEnclosingRepo covers the
// owner fallback when the state directory sits inside checkout A. A deleted
// session-owned lane's originless owner records a restore root that is itself a
// deleted lane of repository B. An unbounded walk from that restore path finds
// A's .git and labels the transcript as A, returning before the parent lookup
// can recover B. The lane root must bound the owner's restore path too, and a
// lane that no longer resolves must leave the parent's evidence to answer.
func TestFind_ProjectName_OwnerRestoreLaneDoesNotInheritEnclosingRepo(t *testing.T) {
	checkoutA := newNamedCheckout(t, "checkout-a")
	stateHome := filepath.Join(checkoutA, "nested-state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucket := filepath.Join(stateHome, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The row is a session-owned lane that has been deleted. Its owner's metadata
	// survived, carries no origin, and records a restore root that is a deleted
	// lane of repository B.
	ownerID := identifier.MustNewSessionID()
	restoreLane := filepath.Join(bucket, "worktrees", projectB.ID, "dlg_restore")
	owner := schema.SessionMeta{ID: ownerID}
	owner.WorktreePath = restoreLane
	owner.WorktreeRestoreRoot = restoreLane
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatal(err)
	}

	// The row's parent is repository B's own checkout, so its metadata names the
	// project those deleted lanes belonged to.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "dlg_row")
	if got, want := projectName(row, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(deleted session lane whose owner recorded a deleted restore lane inside checkout A) = %q, want the parent's %q", got, want)
	}
}

// TestFind_ProjectName_SharedOverrideNamedSessions covers a shared override
// whose own path spells a session-owned layout: --state-dir /srv/sessions/shared
// makes shared/worktrees/<projectID>/<lane> look like a delegate lane of "shared"
// under /srv. The managed reading is the real one, and the project its path
// records is evidence even though the checkout is gone.
func TestFind_ProjectName_SharedOverrideNamedSessions(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "sessions", "shared")
	if err := os.MkdirAll(filepath.Join(shared, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir shared override: %v", err)
	}

	repo := newNamedCheckout(t, "shared-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/shared-repo.git"
	parent.EnvInfo.WorkingDir = repo
	if err := schema.SaveSessionMeta(shared, parent); err != nil {
		t.Fatal(err)
	}

	// The deleted managed lane sits directly under the validated bucket.
	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(shared, "worktrees", project.ID, "dlg_shared")
	if got, want := projectName(row, shared), "shared-repo"; got != want {
		t.Fatalf("projectName(deleted managed lane under a bucket named sessions/shared) = %q, want %q (the managed layout is the real one)", got, want)
	}
}

// TestFind_ProjectName_SharedBucketDoesNotNameAnOwnerlessLane covers a state
// directory this process is configured with whose basename spells a shared
// override: <stateHome>/evener/projects/shared. It sits under the configured
// root, so it is part of the state tree, but its name is not a project ID and
// nothing about it establishes that its sessions served one repository.
//
// A pruned session-owned lane there has no project of its own, no owner and no
// parent, so the only evidence left is any sibling that saved an origin. Taking
// that origin would label the lane with whichever repository last used the
// shared directory. A bucket with no verified per-project scope may not name
// such a lane from an unverified sibling.
func TestFind_ProjectName_SharedBucketDoesNotNameAnOwnerlessLane(t *testing.T) {
	stateHome := pinStateHome(t)

	// Two repositories used the shared directory, which is what makes it shared
	// rather than one project's bucket.
	shared := filepath.Join(stateHome, "evener", "projects", "shared")
	if err := os.MkdirAll(filepath.Join(shared, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir shared bucket: %v", err)
	}
	for _, origin := range []string{
		"git@github.com:owner/first-shared-repo.git",
		"git@github.com:owner/second-shared-repo.git",
	} {
		sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
		sibling.EnvInfo.GitOriginURL = origin
		sibling.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-checkout")
		if err := schema.SaveSessionMeta(shared, sibling); err != nil {
			t.Fatal(err)
		}
	}

	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(shared, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_shared_lane")
	if got := projectName(lane, shared); got != "" {
		t.Fatalf("projectName(pruned lane in a shared bucket whose sessions served several repositories) = %q, want empty: no verified scope ties that origin to this lane", got)
	}

	// Row-specific evidence still answers: a parent in the same shared directory
	// is tied to the lane by the bucket, and its live checkout names the project.
	// The refusal above is about bucket-wide inference, not about the directory
	// becoming unusable.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.WorkingDir = newNamedCheckout(t, "shared-parent-repo")
	if err := schema.SaveSessionMeta(shared, parent); err != nil {
		t.Fatal(err)
	}
	owned := schema.SessionMeta{ParentSessionID: parent.ID}
	owned.EnvInfo.WorkingDir = filepath.Join(shared, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_parent_row")
	if got, want := projectName(owned, shared), "shared-parent-repo"; got != want {
		t.Fatalf("projectName(pruned lane in a shared bucket whose parent checkout resolves) = %q, want the parent's %q", got, want)
	}
}

// TestFind_ProjectName_VerifiedParentOriginSurvivesItsCheckout covers a deleted
// session-owned lane in another bucket whose parent is verified by matching the
// owner in the lane path, while that parent's own checkout is also gone. The
// parent's saved Git origin names the repository without needing anything on
// disk, so it must still answer.
func TestFind_ProjectName_VerifiedParentOriginSurvivesItsCheckout(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucketA, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucketA, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	// A second bucket under the same root, so the lane's state dir differs from
	// the bucket the row was discovered in.
	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucketB, err := RuntimeDirForProjectWithStateHome(projectB, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucketB, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket B: %v", err)
	}

	// The parent is verified by being the owner encoded in the lane path, and its
	// own checkout is gone: only its saved origin survives. Its own lane is
	// session-owned, so its path records no managed project ID — the case where
	// reading only the checkout leaves no evidence at all.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = filepath.Join(bucketB, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_parent_lane")
	if err := schema.SaveSessionMeta(bucketA, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(bucketB, "sessions", parentID, "worktrees", "dlg_child")
	if got, want := projectName(row, bucketA), "repo-b"; got != want {
		t.Fatalf("projectName(deleted session lane in bucket B whose verified parent's checkout is gone) = %q, want its saved origin %q", got, want)
	}
}

// TestFind_ProjectName_LiveSiblingPointerNamesTheRepository covers a deleted
// managed lane with no usable parent, where the only record of the matching
// repository is a live sibling worktree carrying no saved origin or restore
// root. Its Git pointer still names the main checkout, and that is evidence.
func TestFind_ProjectName_LiveSiblingPointerNamesTheRepository(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "pointer-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling's own worktree still exists, with a real Git pointer, and its
	// metadata records no origin and no restore root.
	siblingLane := filepath.Join(bucket, "worktrees", project.ID, "dlg_live_sibling")
	gittest.Run(t, repo, "worktree", "add", "-q", siblingLane, "-b", "live-sibling")
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.WorkingDir = siblingLane
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_deleted")
	if got, want := projectName(row, bucket), "pointer-repo"; got != want {
		t.Fatalf("projectName(deleted lane whose only evidence is a live sibling pointer) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_DanglingGitSymlinkLeavesOriginEligible covers a sibling
// that recorded /repo/pkg, where /repo/.git is now a dangling symlink. The walk
// stops at the refused entry, which must not be read as "pkg is a non-Git
// project of its own": that would name a different project than the sibling's
// saved origin and reject the origin as conflicting evidence.
func TestFind_ProjectName_DanglingGitSymlinkLeavesOriginEligible(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// A subdirectory of the recorded checkout, with the checkout's .git replaced
	// by a symlink that resolves to nothing.
	pkg := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "gone-git"), filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("symlink .git: %v", err)
	}

	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo.git"
	sibling.EnvInfo.WorkingDir = pkg
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_dangling")
	if got, want := projectName(row, bucket), "repo"; got != want {
		t.Fatalf("projectName(deleted lane whose sibling records a checkout with a dangling .git) = %q, want its saved origin %q", got, want)
	}
}

// TestFind_ProjectName_BucketDecodedOnceAcrossRows covers the cost the review
// measured: several originless deleted lanes from one bucket. Every row needs
// the bucket's session metadata to look for a sibling label, and rereading it
// per row decodes the same directory once per result. The snapshot discovery
// already took is what the resolver must use.
func TestFind_ProjectName_BucketDecodedOnceAcrossRows(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "once-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	// A sibling that records only an origin: it labels every row, so each row is
	// resolved through the sibling scan rather than short-circuiting early.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/once-repo.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	rows := []findCandidate{}
	for i := range 5 {
		row := schema.SessionMeta{}
		row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, fmt.Sprintf("dlg_row_%d", i))
		rows = append(rows, findCandidate{meta: row, bucketDir: bucket})
	}

	// Discovery's snapshot, taken once, is what the resolver is given.
	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})

	// Take the bucket's sessions dir away. Any resolver that rereads the bucket
	// now finds nothing and answers empty; one that uses discovery's snapshot
	// still labels every row. This is the read count, made observable.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatalf("remove sessions: %v", err)
	}
	for _, c := range rows {
		if got, want := resolver.projectName(c.meta, bucket), "once-repo"; got != want {
			t.Fatalf("projectName(row from a supplied snapshot) = %q, want %q (the bucket must be read once, not once per row)", got, want)
		}
	}
}

// TestFind_ProjectName_ReusesOneStructuralLookupPerDirectory covers the cost a
// search pays for several results from one checkout: every row resolves its
// recorded working directory through the same Git-layout walk, and the walk is
// identical. The resolver keeps one complete answer per directory for the life
// of one search, so the second row reuses the first row's lookup. The label
// cannot tell one walk from two; the diagnostic counter can.
func TestFind_ProjectName_ReusesOneStructuralLookupPerDirectory(t *testing.T) {
	t.Run("live checkout", func(t *testing.T) {
		checkout := newNamedCheckout(t, "same-checkout")
		// Records point at a subdirectory, so the label cannot pass by echoing the
		// recorded directory's own basename: only the checkout's root names the
		// project, and the walk has to climb to it.
		recorded := filepath.Join(checkout, "sub")
		if err := os.MkdirAll(recorded, 0o755); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		bucket := t.TempDir()
		rowA := schema.SessionMeta{ID: identifier.MustNewSessionID()}
		rowA.EnvInfo.WorkingDir = recorded
		rowB := schema.SessionMeta{ID: identifier.MustNewSessionID()}
		rowB.EnvInfo.WorkingDir = recorded

		// One resolver per search: find labels every result row through it.
		r := newProjectNameResolver()
		if got := r.projectName(rowA, bucket); got != "same-checkout" {
			t.Fatalf("projectName(first row recording %q) = %q, want same-checkout", recorded, got)
		}
		if got := r.projectName(rowB, bucket); got != "same-checkout" {
			t.Fatalf("projectName(second row recording %q) = %q, want same-checkout", recorded, got)
		}
		if r.mainRootLookups != 1 {
			t.Fatalf("two rows recording one directory took %d structural lookups, want 1: the second row repeated the first row's walk", r.mainRootLookups)
		}
	})

	t.Run("refused entry is preserved", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "refused-checkout")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// A .git entry that exists and cannot be used: a symlink to nothing. That
		// is a refusal, not the absence of a repository.
		if err := os.Symlink(filepath.Join(dir, "gone-git"), filepath.Join(dir, ".git")); err != nil {
			t.Fatalf("symlink .git: %v", err)
		}

		r := newProjectNameResolver()
		for i := range 2 {
			root, refused := r.mainRootOutcome(dir)
			if root != "" || !refused {
				t.Fatalf("mainRootOutcome(refused checkout), call %d = (%q, %v), want (\"\", true): the memo must keep refusal as its own answer, not collapse it to absence", i+1, root, refused)
			}
		}
		if r.mainRootLookups != 1 {
			t.Fatalf("two lookups of one refused directory took %d structural lookups, want 1", r.mainRootLookups)
		}
	})
}

// TestFind_ProjectName_ResolvesFallbackSpellingBeforeTheWalk covers a recorded
// path that cannot be resolved as a whole because a component in it was removed
// after the record was written. identifier.ResolveClean then falls back to a
// lexical Clean, and that spelling can still name a symlink: "alias/build/.."
// cleans to "alias". A walk started from the alias climbs its lexical parents
// and misses the checkout the link points into, so the fallback spelling must be
// resolved once more before it becomes the memo key and the walk's start.
func TestFind_ProjectName_ResolvesFallbackSpellingBeforeTheWalk(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "real-checkout")
	if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(checkout, "sub"), alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	// "build" never existed, so the whole path cannot resolve; Clean cancels
	// "build/.." and leaves the alias's own spelling.
	recorded := alias + string(filepath.Separator) + "build" + string(filepath.Separator) + ".."

	got, _, ok, _, _ := newProjectNameResolver().projectAt(recorded, t.TempDir())
	if !ok || got != identifier.ResolveClean(checkout) {
		t.Fatalf("projectAt(recorded %q) = %q, ok=%v, want %q: the fallback spelling names a symlink and must be resolved before the walk", recorded, got, ok, identifier.ResolveClean(checkout))
	}
}

func TestFind_CatalogTrimmedAndOrdered(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)

	now := time.Now().UTC().Truncate(time.Second)

	// OLDER is the oldest but will sort first because it's the current session.
	// But the current session should sort LAST per spec.
	//
	// We write:
	//   02wMz5Txv1C3Hut0M8GCeB: updated now-2h (not current)
	//   02wMz5Txv2enqVTitaig6F: updated now-1h (not current; this is newest non-current)
	//   02wMz5TxvEMoJEDTDGOTil: updated now (this is current; must sort last)
	//
	// Expected order: 02wMz5Txv2enqVTitaig6F (newest non-current), 02wMz5Txv1C3Hut0M8GCeB, 02wMz5TxvEMoJEDTDGOTil (current last).
	specOlder := findMetaSpec{
		id:      "02wMz5Txv1C3Hut0M8GCeB",
		name:    "older session",
		updated: now.Add(-2 * time.Hour),
	}
	specNewer := findMetaSpec{
		id:      "02wMz5Txv2enqVTitaig6F",
		name:    "newer session",
		updated: now.Add(-1 * time.Hour),
	}
	specCurrent := findMetaSpec{
		id:      "02wMz5TxvEMoJEDTDGOTil",
		name:    "current session",
		updated: now,
	}

	writeFindSession(t, dir, specOlder, "older text")
	writeFindSession(t, dir, specNewer, "newer text")
	writeFindSession(t, dir, specCurrent, "current text")

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	b := marshalFind(t, deps, map[string]any{})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) < 3 {
		t.Fatalf("expected at least 3 matches, got %d", len(matches))
	}

	// Verify ordering: newest non-current first, current last.
	if matches[0]["transcript_ref"] != "local:02wMz5Txv2enqVTitaig6F" {
		t.Errorf("match[0] should be 02wMz5Txv2enqVTitaig6F (newest non-current), got %v", matches[0]["transcript_ref"])
	}
	if matches[1]["transcript_ref"] != "local:02wMz5Txv1C3Hut0M8GCeB" {
		t.Errorf("match[1] should be 02wMz5Txv1C3Hut0M8GCeB, got %v", matches[1]["transcript_ref"])
	}
	if matches[2]["transcript_ref"] != "local:02wMz5TxvEMoJEDTDGOTil" {
		t.Errorf("match[2] should be 02wMz5TxvEMoJEDTDGOTil (current, sorts last), got %v", matches[2]["transcript_ref"])
	}

	// Verify is_current is set only on the live session.
	if matches[2]["is_current"] != true {
		t.Errorf("match[2] (current) should have is_current=true, got %v", matches[2]["is_current"])
	}
	if matches[0]["is_current"] == true {
		t.Errorf("match[0] (not current) should not have is_current=true")
	}

	// Verify the 5 always-present fields are present.
	for i, m := range matches {
		for _, field := range []string{"transcript_ref", "kind", "title", "updated_at", "approx_turns"} {
			if _, ok := m[field]; !ok {
				t.Errorf("match[%d] missing required field %q", i, field)
			}
		}
	}

	// Verify removed fields are absent from the wire JSON.
	raw := string(b)
	for _, banned := range []string{"session_id", `"model"`, "profile_id", "created_at", "default_read", "has_transcript"} {
		if strings.Contains(raw, banned) {
			t.Errorf("wire JSON contains banned field %q", banned)
		}
	}
}

// --- TestFind_QuerySearch ---

// TestFind_QuerySearch verifies that:
//   - a query matching session metadata returns that session (metadata hits now
//     carry a bounded prompt/title snippet)
//   - a query matching only transcript content returns that session with snippets
//   - scanned is set when a content scan ran
func TestFind_QuerySearch(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)

	// 02wMz5Txv47YP64RR3B9YJ: matches via name (metadata match — should not require content scan).
	writeFindSession(t, dir, findMetaSpec{
		id:      "02wMz5Txv47YP64RR3B9YJ",
		name:    "uniqueMetaToken session",
		updated: now.Add(-2 * time.Hour),
	}, "unrelated content")

	// 02wMz5Txv5aIxgf9yVdd0N: matches only in transcript content (title/prompt has no match).
	writeFindSession(t, dir, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "plain session",
		updated: now.Add(-1 * time.Hour),
	}, "the uniqueContentToken lives here")

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	b := marshalFind(t, deps, map[string]any{"query": "uniqueContentToken"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %v", len(matches), matches)
	}
	if matches[0]["transcript_ref"] != "local:02wMz5Txv5aIxgf9yVdd0N" {
		t.Errorf("expected 02wMz5Txv5aIxgf9yVdd0N match, got %v", matches[0]["transcript_ref"])
	}

	// Content match should have snippets.
	snips, ok := matches[0]["snippets"]
	if !ok || snips == nil {
		t.Error("content match should have snippets")
	}

	// scanned should be set (we opened at least one transcript).
	if _, ok := env["scanned"]; !ok {
		t.Error("envelope should have 'scanned' when a content scan ran")
	}

	// Now test a metadata-matching query.
	b2 := marshalFind(t, deps, map[string]any{"query": "uniqueMetaToken"})
	env2 := decodeEnvelope(t, b2)
	matches2 := matchesFromEnvelope(t, env2)
	if len(matches2) != 1 {
		t.Fatalf("expected 1 meta match, got %d", len(matches2))
	}
	if matches2[0]["transcript_ref"] != "local:02wMz5Txv47YP64RR3B9YJ" {
		t.Errorf("expected 02wMz5Txv47YP64RR3B9YJ meta match, got %v", matches2[0]["transcript_ref"])
	}
}

// --- TestFind_ChildrenOf ---

// TestFind_ChildrenOf verifies that:
//   - find({children_of:"local:02wMz5Txv733WHFsVy66SR"}) returns exactly the two children
//   - children is metadata-only (no transcript files required for the parent or children)
//   - unrelated sessions are excluded
func TestFind_ChildrenOf(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Parent: meta only, no transcript — proves the parent's transcript is never
	// opened to resolve its children.
	saveFindMeta(t, dir, findMetaSpec{
		id:      "02wMz5Txv733WHFsVy66SR",
		name:    "the parent",
		updated: now.Add(-4 * time.Hour),
	})

	// Two real children, each with a transcript so each is a read-able ref.
	writeFindSession(t, dir, findMetaSpec{
		id:              "02wMz5Txv8Vo4rqb3QYZuV",
		name:            "first child",
		parentSessionID: "02wMz5Txv733WHFsVy66SR",
		updated:         now.Add(-3 * time.Hour),
	}, "first child work")
	writeFindSession(t, dir, findMetaSpec{
		id:              "02wMz5Txv9yYdSRJat13MZ",
		name:            "second child",
		parentSessionID: "02wMz5Txv733WHFsVy66SR",
		updated:         now.Add(-2 * time.Hour),
	}, "second child work")

	// A child by metadata whose transcript was never flushed: not read-able, so
	// children_of must exclude it (the readable-only invariant applies to every mode).
	saveFindMeta(t, dir, findMetaSpec{
		id:              "02wMz5TxvBRJC3228LTWod",
		name:            "unflushed child",
		parentSessionID: "02wMz5Txv733WHFsVy66SR",
		updated:         now.Add(-1 * time.Hour),
	})

	// Unrelated session (no parent).
	writeFindSession(t, dir, findMetaSpec{
		id:      "02wMz5TxvCu3kdckfnw0Gh",
		name:    "unrelated",
		updated: now,
	}, "unrelated work")

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	b := marshalFind(t, deps, map[string]any{"children_of": "local:02wMz5Txv733WHFsVy66SR"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	ids := map[string]bool{}
	for _, m := range matches {
		ref, _ := m["transcript_ref"].(string)
		ids[ref] = true
	}
	if !ids["local:02wMz5Txv8Vo4rqb3QYZuV"] || !ids["local:02wMz5Txv9yYdSRJat13MZ"] {
		t.Errorf("expected both readable children, got %v", ids)
	}
	if ids["local:02wMz5TxvBRJC3228LTWod"] {
		t.Errorf("02wMz5TxvBRJC3228LTWod has no transcript and must be excluded (readable-only): %v", ids)
	}
	if ids["local:02wMz5TxvCu3kdckfnw0Gh"] {
		t.Errorf("unrelated session must not appear: %v", ids)
	}
	if len(matches) != 2 {
		t.Fatalf("expected exactly 2 children, got %d: %v", len(matches), ids)
	}
}

// --- TestRead helpers ---

// writeMultiTurnSession writes a session transcript with nTurns alternating
// user/assistant turns, plus a SessionMeta. It is a helper for read tests.
func writeMultiTurnSession(t *testing.T, bucketDir string, spec findMetaSpec, nTurns int) {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeMultiTurnSession mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		ProfileID: spec.profileID,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write multi-turn transcript %s: %v", spec.id, err)
	}
	for i := range nTurns {
		if i%2 == 0 {
			if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("user turn"))); err != nil {
				t.Fatalf("append user turn %d: %v", i, err)
			}
		} else {
			if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant turn"))); err != nil {
				t.Fatalf("append assistant turn %d: %v", i, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close multi-turn transcript %s: %v", spec.id, err)
	}
	saveFindMeta(t, bucketDir, spec)
}

// writeSessionWithToolTurn writes a session with a named tool call and a
// large repeated tool result (enough lines to trigger head+tail truncation).
// It returns the seq of the assistant turn (0-based).
func writeSessionWithToolTurn(t *testing.T, bucketDir string, spec findMetaSpec) int {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeSessionWithToolTurn mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write tool-turn transcript: %v", err)
	}
	callID := "call-expand-001"
	toolCallArgs := json.RawMessage(`{"command":"ls -la"}`)
	// Turn 0: user
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("run a command"))); err != nil {
		t.Fatalf("append user turn: %v", err)
	}
	// Turn 1: assistant with tool call
	assistantMsg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        callID,
				Name:      "shell",
				Arguments: toolCallArgs,
			}},
		},
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, assistantMsg)); err != nil {
		t.Fatalf("append assistant tool-call turn: %v", err)
	}
	// Build a large result body: 60 UNIQUE non-empty lines so head+tail truncation
	// elides the middle. With head=20 / tail=10, line 30 lands in the elided middle —
	// the only honest probe for whether expand_turn actually un-truncates.
	var resultLines strings.Builder
	for i := range 60 {
		fmt.Fprintf(&resultLines, "result-line-%02d-marker\n", i)
	}
	// Turn 2: tool result
	resultMsg := llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: callID,
				Name:       "shell",
				Content:    resultLines.String(),
			}},
		},
	}
	if err := tw.Append(schema.NewTurn(schema.TurnToolResults, resultMsg)); err != nil {
		t.Fatalf("append tool-result turn: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tool-turn transcript: %v", err)
	}
	saveFindMeta(t, bucketDir, spec)
	return 1 // assistant turn is seq 1
}

// marshalRead calls execReadSessionTranscript and JSON-marshals the result.
func marshalRead(t *testing.T, deps *toolDeps, args map[string]any) []byte {
	t.Helper()
	v, err := execReadSessionTranscript(context.Background(), deps, args)
	if err != nil {
		t.Fatalf("execReadSessionTranscript: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal read result: %v", err)
	}
	return b
}

// decodeReadEnvelope decodes a marshaled read result into a map.
func decodeReadEnvelope(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal read envelope: %v", err)
	}
	return m
}

// readMeta extracts the "meta" sub-map from a decoded read envelope.
func readMetaMap(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	raw, ok := env["meta"]
	if !ok {
		t.Fatal("read envelope missing 'meta' key")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("'meta' is not a map: %T", raw)
	}
	return m
}

// --- TestRead_DefaultMarkdownWindow ---

// TestRead_DefaultMarkdownWindow verifies that a default read of a session with
// >40 turns returns markdown whose meta has turns_total > turns_rendered and
// whose content contains a self-announcing window line.
func TestRead_DefaultMarkdownWindow(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	const totalTurns = 50 // more than the 40-turn default window

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "window session",
		updated: now,
	}, totalTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// format and content_type must be set correctly.
	if env["format"] != "markdown" {
		t.Errorf("format = %v, want markdown", env["format"])
	}
	if env["content_type"] != "text/markdown" {
		t.Errorf("content_type = %v, want text/markdown", env["content_type"])
	}

	// Meta must carry turns_total.
	turnsTotal, ok := meta["turns_total"].(float64)
	if !ok || turnsTotal == 0 {
		t.Fatalf("meta.turns_total missing or zero: %v", meta["turns_total"])
	}
	turnsRendered, ok := meta["turns_rendered"].(float64)
	if !ok {
		t.Fatalf("meta.turns_rendered missing: %v", meta["turns_rendered"])
	}

	// With 50 turns and a 40-turn default window, turns_rendered < turns_total.
	if turnsRendered >= turnsTotal {
		t.Errorf("turns_rendered=%v should be < turns_total=%v for a windowed read", turnsRendered, turnsTotal)
	}

	// Content must contain a self-announcing window line.
	content, ok := env["content"].(string)
	if !ok {
		t.Fatal("content is not a string")
	}
	// The window line must name "of <total>".
	wantSnip := fmt.Sprintf("of %d", totalTurns)
	if !strings.Contains(content, wantSnip) {
		t.Errorf("content does not contain window announcement %q; got:\n%s", wantSnip, content[:min(500, len(content))])
	}
}

// TestRead_WindowHonest verifies the self-announcing window names the turns ACTUALLY
// rendered, never the literal request — including an out-of-range N-M, the bug class
// where "50-100" on a 10-turn session announced a fabricated, reversed "turns 50–9".
func TestRead_WindowHonest(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv2enqVTitaig6F"
	const totalTurns = 10

	writeMultiTurnSession(t, dir, findMetaSpec{id: sessionID, name: "honest window", updated: now}, totalTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"range":          "50-100", // entirely out of bounds; clamps to the last turn
	})
	env := decodeReadEnvelope(t, b)
	content, _ := env["content"].(string)

	// The window line must name the real, in-bounds span (turn 9 of 10) — never the
	// requested 50/100.
	if !strings.Contains(content, "Showing turns 9–9 of 10") {
		t.Errorf("window line must name the real rendered span; got:\n%s", content[:min(600, len(content))])
	}
	if strings.Contains(content, "turns 50") || strings.Contains(content, "–100") {
		t.Errorf("window line must not echo out-of-range request numbers; got:\n%s", content[:min(600, len(content))])
	}
}

// --- TestRead_ExplicitRange ---

// TestRead_ExplicitRange verifies that range:"2-4" renders exactly those turns.
func TestRead_ExplicitRange(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv47YP64RR3B9YJ"
	const totalTurns = 10

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "range session",
		updated: now,
	}, totalTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"range":          "2-4",
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// turns_rendered should be exactly 3 (turns 2, 3, 4).
	turnsRendered, ok := meta["turns_rendered"].(float64)
	if !ok {
		t.Fatalf("meta.turns_rendered missing: %v", meta["turns_rendered"])
	}
	if turnsRendered != 3 {
		t.Errorf("turns_rendered = %v, want 3 (range 2-4)", turnsRendered)
	}

	// The range in meta should reflect what was requested.
	rangeVal, _ := meta["range"].(string)
	if rangeVal != "2-4" {
		t.Errorf("meta.range = %q, want \"2-4\"", rangeVal)
	}

	// Content should contain headings for turns 2, 3, and 4.
	content, _ := env["content"].(string)
	for _, seq := range []int{2, 3, 4} {
		heading := fmt.Sprintf("## Turn %d", seq)
		if !strings.Contains(content, heading) {
			t.Errorf("content missing turn heading %q", heading)
		}
	}
	// And must NOT contain turn 0 or 1 headings.
	for _, seq := range []int{0, 1} {
		heading := fmt.Sprintf("## Turn %d", seq)
		if strings.Contains(content, heading) {
			t.Errorf("content unexpectedly contains heading %q for out-of-range turn", heading)
		}
	}
}

// --- TestRead_MalformedRangeWarns ---

// TestRead_MalformedRangeWarns verifies that a bad range sets meta.range_warning
// and surfaces a warning in content, while falling back to the default.
func TestRead_MalformedRangeWarns(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "warn session",
		updated: now,
	}, 5)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"range":          "not-a-valid-range!!",
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// range_warning must be set.
	warning, _ := meta["range_warning"].(string)
	if warning == "" {
		t.Error("meta.range_warning should be set for a malformed range")
	}

	// Content must also surface the warning.
	content, _ := env["content"].(string)
	if !strings.Contains(content, "range warning") {
		t.Errorf("content should contain a visible range warning; got:\n%s", content[:min(500, len(content))])
	}
}

// --- TestRead_ExpandTurn ---

// TestRead_ExpandTurn verifies that expand_turn:<N> renders the tool result in
// full (lines that would be elided by head+tail truncation are present).
func TestRead_ExpandTurn(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv733WHFsVy66SR"

	assistantSeq := writeSessionWithToolTurn(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "expand session",
		updated: now,
	})

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}

	// Without expand_turn, the middle lines of the 60-line result should be elided.
	bNoExpand := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})
	envNoExpand := decodeReadEnvelope(t, bNoExpand)
	contentNoExpand, _ := envNoExpand["content"].(string)

	// With expand_turn, ALL lines should appear.
	bExpand := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"expand_turn":    float64(assistantSeq), // JSON numbers unmarshal as float64
	})
	envExpand := decodeReadEnvelope(t, bExpand)
	contentExpand, _ := envExpand["content"].(string)

	// Line 30 is in the elided middle (head=20 / tail=10): it MUST be absent without
	// expand_turn and present with it. This is the assertion that actually proves
	// expand un-truncates — a probe in the head/tail would pass even if expand did
	// nothing.
	middleLine := "result-line-30-marker"
	if strings.Contains(contentNoExpand, middleLine) {
		t.Errorf("default read should elide the middle; %q must NOT appear without expand_turn", middleLine)
	}
	if !strings.Contains(contentExpand, middleLine) {
		t.Errorf("expand_turn must un-truncate the result; %q missing from the expanded read", middleLine)
	}
	// A head line and a tail line appear in both (sanity that the result rendered).
	if !strings.Contains(contentNoExpand, "result-line-00-marker") || !strings.Contains(contentNoExpand, "result-line-59-marker") {
		t.Errorf("default read should still show head + tail lines")
	}
	// No elision marker when fully expanded.
	if strings.Contains(contentExpand, "lines elided") {
		t.Error("expanded content should not have elision markers")
	}
}

// TestRead_ExpandTurnWithoutRangeRendersOnlyThatTurn pins expand_turn's
// window: without an explicit range the markdown shows Turn N alone, not the
// default last-40 window around it, and the meta reports that one-turn window.
func TestRead_ExpandTurnWithoutRangeRendersOnlyThatTurn(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv733WHFsVy66ST"
	writeMultiTurnSession(t, dir, findMetaSpec{id: sessionID, name: "expand alone", updated: now}, 5)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	env := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"expand_turn":    float64(2),
	}))
	content, _ := env["content"].(string)
	if !strings.Contains(content, "## Turn 2 ") {
		t.Fatalf("expanded read is missing Turn 2:\n%s", content)
	}
	for _, other := range []int{0, 1, 3, 4} {
		if strings.Contains(content, fmt.Sprintf("## Turn %d ", other)) {
			t.Errorf("expand_turn=2 without range also rendered Turn %d:\n%s", other, content)
		}
	}
	meta := readMetaMap(t, env)
	if meta["range"] != "2-2" || meta["turns_rendered"] != float64(1) || meta["turns_total"] != float64(5) {
		t.Errorf("meta = %v, want range 2-2 with 1 of 5 turns rendered", meta)
	}
}

// TestRead_ExpandTurnWithMalformedRangeRendersTheDefault pins the fallback a
// malformed range gets alongside expand_turn: the default window the warning
// names, not the expansion-only window an omitted range gets.
func TestRead_ExpandTurnWithMalformedRangeRendersTheDefault(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv733WHFsVy66SU"
	writeMultiTurnSession(t, dir, findMetaSpec{id: sessionID, name: "expand malformed", updated: now}, 5)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	env := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"expand_turn":    float64(2),
		"range":          "not-a-range",
	}))
	meta := readMetaMap(t, env)
	if meta["range"] != "last:40" || meta["turns_rendered"] != float64(5) || meta["range_warning"] == nil {
		t.Errorf("meta = %v, want the warned default window last:40 over all 5 turns", meta)
	}
}

// --- TestRead_MetaTrimmed ---

// TestRead_MetaTrimmed verifies the marshaled markdown meta has the required
// fields and does NOT contain the banned fields (redaction, raw_formats,
// session_id).
func TestRead_MetaTrimmed(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5TxvHIJQPOuIBJQct"

	writeFindSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "meta trim session",
		updated: now,
	}, "some text")

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})

	// Verify required meta fields.
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	for _, field := range []string{"turns_total", "range", "turns_rendered", "truncated", "elided_turns"} {
		if _, ok := meta[field]; !ok {
			t.Errorf("meta missing required field %q", field)
		}
	}

	// Verify banned fields are absent from the wire JSON.
	raw := string(b)
	for _, banned := range []string{"redaction", "raw_formats", `"session_id"`} {
		if strings.Contains(raw, banned) {
			t.Errorf("wire JSON contains banned field %q", banned)
		}
	}
}

// TestRead_JSONL verifies the jsonl debug hatch: raw NDJSON content, the NDJSON
// content type, and a meta hint that steers back to markdown for comprehension.
func TestRead_JSONL(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv8Vo4rqb3QYZuV"
	writeMultiTurnSession(t, dir, findMetaSpec{id: sessionID, name: "jsonl session", updated: now}, 5)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"format":         "jsonl",
	})
	env := decodeReadEnvelope(t, b)

	if env["format"] != "jsonl" {
		t.Errorf("format = %v, want jsonl", env["format"])
	}
	if env["content_type"] != "application/x-ndjson" {
		t.Errorf("content_type = %v, want application/x-ndjson", env["content_type"])
	}
	content, _ := env["content"].(string)
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		t.Errorf("jsonl content should be raw NDJSON starting with a JSON object, got: %q", firstLineOf(content))
	}
	meta := readMetaMap(t, env)
	if hint, _ := meta["hint"].(string); !strings.Contains(hint, "markdown") {
		t.Errorf("jsonl meta.hint should steer to markdown, got %q", hint)
	}
	if lr, _ := meta["lines_returned"].(float64); lr < 1 {
		t.Errorf("lines_returned should be >= 1, got %v", lr)
	}
}

func firstLineOf(s string) string {
	if line, _, ok := strings.Cut(s, "\n"); ok {
		return line
	}
	return s
}

// --- TestRead_Outline* ---

// TestRead_OutlineBasic verifies that format:"outline" returns:
//   - format: "outline"
//   - turns_total matching the session's entry count
//   - content with one line per non-TOOL_RESULTS turn
//   - each line's leading number matches its absolute turn index
//   - hint is present and non-empty
func TestRead_OutlineBasic(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5Txv9yYdSRJat13MZ"
	const nTurns = 6

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "outline basic",
		updated: now,
	}, nTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"format":         "outline",
	})
	env := decodeReadEnvelope(t, b)

	// Top-level format field must be "outline".
	if env["format"] != "outline" {
		t.Errorf("format = %v, want outline", env["format"])
	}

	// turns_total must be present at the top level (no nested meta for outline).
	turnsTotal, ok := env["turns_total"].(float64)
	if !ok || turnsTotal == 0 {
		t.Fatalf("turns_total missing or zero: %v", env["turns_total"])
	}
	if int(turnsTotal) != nTurns {
		t.Errorf("turns_total = %v, want %d", turnsTotal, nTurns)
	}

	// hint must be present and non-empty.
	hint, ok := env["hint"].(string)
	if !ok || hint == "" {
		t.Errorf("hint missing or empty: %v", env["hint"])
	}

	// content must be a non-empty string.
	content, ok := env["content"].(string)
	if !ok || content == "" {
		t.Fatal("content missing or empty")
	}

	// Each line must start with its absolute turn number. writeMultiTurnSession
	// alternates user/assistant, so all 6 turns get outline lines (no TOOL_RESULTS).
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != nTurns {
		t.Fatalf("expected %d outline lines, got %d:\n%s", nTurns, len(lines), content)
	}
	for i, line := range lines {
		wantPrefix := fmt.Sprintf("%d ", i)
		if !strings.HasPrefix(line, wantPrefix) {
			t.Errorf("line[%d] does not start with %q: %q", i, wantPrefix, line)
		}
	}
}

// TestRead_OutlineRange verifies that range:"last:5" on a 12-turn session:
//   - returns only 5 outline lines
//   - the leading numbers are the ABSOLUTE turns 7..11 (not 0..4)
//   - turns_total is still 12 (the full session)
func TestRead_OutlineRange(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "02wMz5TxvBRJC3228LTWod"
	const nTurns = 12

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "outline range",
		updated: now,
	}, nTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "02wMz5TxvFpYrooBkiqxAp"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"format":         "outline",
		"range":          "last:5",
	})
	env := decodeReadEnvelope(t, b)

	turnsTotal, ok := env["turns_total"].(float64)
	if !ok {
		t.Fatalf("turns_total missing: %v", env["turns_total"])
	}
	if int(turnsTotal) != nTurns {
		t.Errorf("turns_total = %v, want %d (full count regardless of range)", turnsTotal, nTurns)
	}

	content, ok := env["content"].(string)
	if !ok || content == "" {
		t.Fatal("content missing or empty")
	}

	// Only 5 lines (last 5 of 12 = turns 7..11).
	lines := nonEmptyLines(content)
	if len(lines) != 5 {
		t.Fatalf("expected 5 outline lines for last:5, got %d:\n%s", len(lines), content)
	}

	// Each line's leading number must be the absolute turn index (7, 8, 9, 10, 11).
	for i, line := range lines {
		wantTurn := 7 + i
		wantPrefix := fmt.Sprintf("%d ", wantTurn)
		if !strings.HasPrefix(line, wantPrefix) {
			t.Errorf("line[%d] must start with absolute turn %d, got: %q", i, wantTurn, line)
		}
	}
}

// TestFind_ChildrenOf_ProjBucket verifies children_of for a parent in a sibling
// project (proj: ref): the parent's bucket is resolved from the ref with no stat, and
// the child comes back with a proj: ref into that bucket.
func TestFind_ChildrenOf_ProjBucket(t *testing.T) {
	t.Parallel()
	stateHome := newStateHome(t)
	currentBucket := newBucketUnder(t, stateHome)
	siblingBucket := newBucketUnder(t, stateHome)
	hash := filepath.Base(siblingBucket)
	now := time.Now().UTC().Truncate(time.Second)

	// Parent (meta only) + one readable child, both in the SIBLING bucket.
	saveFindMeta(t, siblingBucket, findMetaSpec{id: "02wMz5TxvIl3yzzcpdlu4x", name: "sibling parent", updated: now.Add(-time.Hour)})
	writeFindSession(t, siblingBucket, findMetaSpec{
		id:              "02wMz5TxvKDoXaaLN6ENX1",
		name:            "sibling child",
		parentSessionID: "02wMz5TxvIl3yzzcpdlu4x",
		updated:         now,
	}, "sibling child work")

	deps := &toolDeps{stateDir: currentBucket, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	b := marshalFind(t, deps, map[string]any{"children_of": "proj:" + hash + ":02wMz5TxvIl3yzzcpdlu4x"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) != 1 {
		t.Fatalf("expected 1 sibling-project child, got %d", len(matches))
	}
	wantRef := "proj:" + hash + ":02wMz5TxvKDoXaaLN6ENX1"
	if ref, _ := matches[0]["transcript_ref"].(string); ref != wantRef {
		t.Errorf("child ref = %q, want %q", ref, wantRef)
	}
}

// TestFind_ChildrenOf_BareIDResolvesCrossBucket asserts that children_of with a
// bare session ID resolves the parent cross-bucket (mirroring resolveTranscript's
// bare-ID semantics), not just the current bucket. A parent in a legacy-named
// sibling bucket and its child in the same legacy bucket must be found when
// children_of uses the parent's bare ID. Today parentBucketAndID returns
// currentStateDir for bare IDs, so the search is scoped to the current bucket
// and silently returns 0 matches — the wrong project.
func TestFind_ChildrenOf_BareIDResolvesCrossBucket(t *testing.T) {
	t.Parallel()
	stateHome := newStateHome(t)
	currentBucket := newBucketUnder(t, stateHome)
	legacyBucket := filepath.Join(stateHome, "evener", "projects", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(legacyBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	parentID := "02wMz5TxvIl3yzzcpdlu4x"
	childID := "02wMz5TxvKDoXaaLN6ENX1"

	// Parent + one readable child, both in the LEGACY bucket. The parent needs
	// a transcript file so parentBucketAndID's cross-bucket resolution (which
	// stats the parent transcript) can locate it in the legacy bucket.
	writeFindSession(t, legacyBucket, findMetaSpec{id: parentID, name: "legacy parent", updated: now.Add(-time.Hour)}, "legacy parent work")
	writeFindSession(t, legacyBucket, findMetaSpec{
		id:              childID,
		name:            "legacy child",
		parentSessionID: parentID,
		updated:         now,
	}, "legacy child work")

	deps := &toolDeps{stateDir: currentBucket, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	b := marshalFind(t, deps, map[string]any{"children_of": parentID})
	env := decodeEnvelope(t, b)

	// Handle nil matches (the RED state: 0 matches found in the current bucket).
	var matches []map[string]any
	if raw, ok := env["matches"]; ok {
		if ifaces, ok := raw.([]any); ok {
			for _, v := range ifaces {
				if m, ok := v.(map[string]any); ok {
					matches = append(matches, m)
				}
			}
		}
	}

	if len(matches) != 1 {
		t.Fatalf("expected 1 child in legacy bucket via bare-ID children_of, got %d", len(matches))
	}
	if title, _ := matches[0]["title"].(string); title != "legacy child" {
		t.Errorf("child title = %q, want %q", title, "legacy child")
	}
}

// TestFind_ProjectName_ManagedLaneNeedsParentNamingItsProject pins the
// admissibility rule for row-specific evidence in the direction that must NOT
// tighten: a parent row that positively identifies a managed lane's project
// still names it.
//
// The rule itself — a parent that records no project is not a match for a lane
// that records one — is refuted as a defect for the reviewer's construction: a
// row in bucket A recording a managed lane of project B already fails
// admissible() there, because B differs from A's own project ID and a foreign
// project requires positive evidence naming it. The strict form is kept anyway,
// because it states the rule where the parent is read rather than relying on a
// different guard to cover it.
func TestFind_ProjectName_ManagedLaneNeedsParentNamingItsProject(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if want := projectA.ID; filepath.Base(bucket) != want {
		t.Fatalf("fixture bucket %q does not encode project %q", filepath.Base(bucket), want)
	}

	// The parent's own live checkout canonicalises to the bucket's project, so it
	// identifies the lane and names it.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-a.git"
	parent.EnvInfo.WorkingDir = repoA
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}
	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_named")
	if got, want := projectName(row, bucket), "repo-a"; got != want {
		t.Fatalf("projectName(managed lane of A with a parent that names A) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_SiblingRestoreRootBasenameFallback covers an eligible
// sibling whose only surviving record is a WorktreeRestoreRoot naming an
// ordinary directory that has since moved. The recording is the evidence, so the
// recorded basename answers: dropping it leaves an otherwise unlabeled lane with
// nothing, which is the fallback the resolver promises to preserve. A state
// lane's leaf is excluded — it is a lane name, never a repository name.
func TestFind_ProjectName_SiblingRestoreRootBasenameFallback(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling records nothing but a restore root in an ordinary directory
	// under the same project, and that directory is removed before the search.
	ordinary := filepath.Join(t.TempDir(), "moved-ordinary-repo")
	if err := os.MkdirAll(ordinary, 0o755); err != nil {
		t.Fatalf("mkdir ordinary: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreeRestoreRoot: ordinary}
	sibling.EnvInfo.WorkingDir = ordinary
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ordinary); err != nil {
		t.Fatalf("remove ordinary: %v", err)
	}

	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_restore_only")
	if got, want := projectName(lane, bucket), "moved-ordinary-repo"; got != want {
		t.Fatalf("projectName(lane whose only sibling records a moved ordinary restore root) = %q, want the recorded basename %q", got, want)
	}

	// A sibling whose restore root is a deleted STATE lane must stay silent: its
	// leaf is a lane name. A second project gives this half its own bucket, so
	// the ordinary sibling above is not an alternative answer.
	repoC := newNamedCheckout(t, "repo-c")
	projectC := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoC))
	bucket2, err := RuntimeDirForProjectWithStateHome(projectC, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if bucket2 == bucket {
		t.Fatalf("fixture buckets collided at %q", bucket)
	}
	if err := os.MkdirAll(filepath.Join(bucket2, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	laneSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	laneSibling.WorktreeRestoreRoot = filepath.Join(bucket2, "worktrees", projectC.ID, "dlg_deleted_restore")
	laneSibling.EnvInfo.WorkingDir = laneSibling.WorktreeRestoreRoot
	if err := schema.SaveSessionMeta(bucket2, laneSibling); err != nil {
		t.Fatal(err)
	}
	lane2 := schema.SessionMeta{}
	lane2.EnvInfo.WorkingDir = filepath.Join(bucket2, "worktrees", projectC.ID, "dlg_only_lane_sibling")
	if got := projectName(lane2, bucket2); got != "" {
		t.Fatalf("projectName(lane whose only sibling records a deleted state lane) = %q, want empty (a lane leaf is not a project)", got)
	}
}

// TestFind_ProjectName_RowAnswersIndependentOfOrder covers the property the
// removed sibling cache used to break. The resolver is shared across result
// rows, so a bucket-wide cached label could be served to a later row that had a
// different answer of its own. With no cache, every row's label is decided only
// by its own evidence and the bucket's, never by which rows preceded it.
func TestFind_ProjectName_RowAnswersIndependentOfOrder(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// One sibling from another repository: it labels a session-owned row, and it
	// must not label a managed lane of this bucket's project.
	other := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	other.EnvInfo.GitOriginURL = "git@github.com:owner/other-repo.git"
	other.EnvInfo.WorkingDir = newNamedCheckout(t, "other-repo")
	if err := schema.SaveSessionMeta(bucket, other); err != nil {
		t.Fatal(err)
	}

	managed := schema.SessionMeta{}
	managed.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_managed")
	session := schema.SessionMeta{}
	session.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_session")

	// Alone first, then after the other row, then the reverse. Every row must
	// return exactly what it returns alone.
	const wantSession = "other-repo"
	forward := newProjectNameResolver()
	if got, want := forward.projectName(session, bucket), wantSession; got != want {
		t.Fatalf("projectName(session-owned row, first) = %q, want %q", got, want)
	}
	if got := forward.projectName(managed, bucket); got != "" {
		t.Fatalf("projectName(managed lane after a session-owned row) = %q, want empty (the sibling names another project)", got)
	}
	backward := newProjectNameResolver()
	if got := backward.projectName(managed, bucket); got != "" {
		t.Fatalf("projectName(managed lane, first) = %q, want empty", got)
	}
	if got, want := backward.projectName(session, bucket), wantSession; got != want {
		t.Fatalf("projectName(session-owned row after a managed row) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_SavedOriginOutranksCheckoutName covers the sibling
// precedence the two-pass scan established: a saved Git origin and an explicit
// restore root record the repository, while a checkout name is a directory that
// merely happens to resolve to one. When both answer for the same bucket they can
// disagree — a second checkout of the repository under another directory name —
// and the record must win regardless of which row the scan reaches first.
// Ranking them equally lets scan order decide the label.
//
// The row is session-owned rather than a managed lane, which is what makes the
// two candidates competitors at all: a managed lane's recorded project ID filters
// out any checkout that resolves to a different project, so only an unfiltered
// row can be answered by a checkout name that disagrees with an origin.
func TestFind_ProjectName_SavedOriginOutranksCheckoutName(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "recorded-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// A real checkout under a different directory name. It has no restore root of
	// its own, so the only thing it can supply is the main root its .git layout
	// resolves to — which is itself, under that other name.
	copyCheckout := newNamedCheckout(t, "checkout-copy")
	checkoutSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	checkoutSibling.EnvInfo.WorkingDir = copyCheckout

	originSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	originSibling.EnvInfo.GitOriginURL = "git@github.com:owner/recorded-repo.git"
	originSibling.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-checkout")

	// The checkout sibling is presented FIRST, which is the order that makes an
	// equal rank answer with the directory name.
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{
		bucket: {checkoutSibling, originSibling},
	})

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_precedence")
	if got, want := resolver.projectName(row, bucket), "recorded-repo"; got != want {
		t.Fatalf("projectName(session-owned lane with a resolving checkout sibling and an origin sibling) = %q, want the recorded origin's %q", got, want)
	}
}

// TestFind_ProjectName_LegacyBucketKeepsOriginOnlySiblingFallback covers the
// conditions of the legacy-bucket rule at once, because they are one
// classification and a test for each would let some of them rot.
//
// A bucket under the configured state root whose basename is not a project ID
// records no project. That is UNKNOWN, not conflicting: treating the unknown ID
// as a conflict marked every lane stored there foreign and threw away the
// bucket-wide fallback a legacy bucket genuinely supports.
//
// An unverified label — a sibling's saved origin, which states no project the row
// can check against — is licensed by verified per-project scope, and a legacy
// bucket has none. So such a label answers a row that asks only for a name, where
// there is no project to contradict, and does not answer a managed lane that
// records its own project. Without that distinction a pruned lane inherited a
// different repository's origin from a bucket that had merely been pointed at.
//
// The remaining conditions stay in force and are asserted here rather than
// assumed: a sibling that positively names a DIFFERENT project is still rejected,
// and a flat override outside the configured root is still ineligible even when
// its basename happens to look like a project ID.
func TestFind_ProjectName_LegacyBucketKeepsOriginOnlySiblingFallback(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// A legacy bucket: under the configured root, basename is not a project ID.
	legacy := filepath.Join(stateHome, "evener", "projects", "legacy-shared-bucket")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir legacy bucket: %v", err)
	}
	if err := identifier.ValidateProjectID(filepath.Base(legacy)); err == nil {
		t.Fatalf("fixture basename %q is a valid project ID, so it cannot exercise the legacy case", filepath.Base(legacy))
	}

	// An origin-only sibling: the only thing the bucket can say about its project.
	originOnly := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	originOnly.EnvInfo.GitOriginURL = "git@github.com:owner/legacy-repo.git"
	originOnly.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-checkout")
	if err := schema.SaveSessionMeta(legacy, originOnly); err != nil {
		t.Fatal(err)
	}

	// A managed lane records a project in its path, so it compares IDs. The
	// sibling states none and the bucket has no verified scope, so nothing ties
	// this origin to that project and the lane gets no label.
	laneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(newNamedCheckout(t, "legacy-repo")))
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(legacy, "worktrees", laneProject.ID, "dlg_legacy")
	if got := projectName(lane, legacy); got != "" {
		t.Fatalf("projectName(managed lane in a legacy bucket with an unverified origin sibling) = %q, want empty: an unverified label needs verified per-project scope", got)
	}

	// A session-owned lane records no project, so it asks only for a name and
	// there is nothing to contradict. The legacy bucket's origin-only sibling is
	// exactly the evidence such a row has, and it still answers.
	sessionOwned := schema.SessionMeta{}
	sessionOwned.EnvInfo.WorkingDir = filepath.Join(legacy, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_legacy_name_only")
	if got, want := projectName(sessionOwned, legacy), "legacy-repo"; got != want {
		t.Fatalf("projectName(session-owned lane in a legacy bucket with an origin-only sibling) = %q, want %q (an unknown bucket project is not a conflict for a name-only row)", got, want)
	}

	// Condition two: a sibling that POSITIVELY names a different project is still
	// rejected, legacy bucket or not. A fresh legacy bucket keeps the origin-only
	// sibling from answering.
	legacy2 := filepath.Join(stateHome, "evener", "projects", "legacy-shared-bucket-two")
	if err := os.MkdirAll(filepath.Join(legacy2, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir second legacy bucket: %v", err)
	}
	conflicting := schema.SessionMeta{ID: identifier.MustNewSessionID(), WorktreeRestoreRoot: newNamedCheckout(t, "intruder-repo")}
	conflicting.EnvInfo.WorkingDir = conflicting.WorktreeRestoreRoot
	if err := schema.SaveSessionMeta(legacy2, conflicting); err != nil {
		t.Fatal(err)
	}
	lane2 := schema.SessionMeta{}
	lane2.EnvInfo.WorkingDir = filepath.Join(legacy2, "worktrees", laneProject.ID, "dlg_legacy_conflict")
	if got := projectName(lane2, legacy2); got != "" {
		t.Fatalf("projectName(legacy bucket lane whose only sibling names another project) = %q, want empty", got)
	}

	// Condition three: a flat override outside the configured root is ineligible
	// even when its basename is a valid project ID, which is what a date-stamped
	// --state-dir looks like.
	flat := filepath.Join(t.TempDir(), laneProject.ID)
	if err := os.MkdirAll(filepath.Join(flat, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir flat: %v", err)
	}
	flatSibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	flatSibling.EnvInfo.GitOriginURL = "git@github.com:owner/legacy-repo.git"
	flatSibling.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-checkout-flat")
	if err := schema.SaveSessionMeta(flat, flatSibling); err != nil {
		t.Fatal(err)
	}
	flatLane := schema.SessionMeta{}
	flatLane.EnvInfo.WorkingDir = filepath.Join(flat, "worktrees", laneProject.ID, "dlg_flat")
	if got := projectName(flatLane, flat); got != "" {
		t.Fatalf("projectName(lane in a flat override whose basename looks like a project ID) = %q, want empty (inference is ineligible outside the configured root)", got)
	}
}

// TestFind_ProjectName_LegacyBucketOriginOnlyNeedsMatchingProjectID covers the
// remaining hole in the flat-state guard. A bucket under the configured root
// whose name fails ValidateProjectID is classified legacy, and legacy evidence
// covers an UNVERIFIED origin: one whose sibling records no project the row can
// check against. A pruned lane that records its own project would then take a
// different repository's saved origin, because nothing tied that origin to this
// project.
//
// Independently verified per-project scope is what has to license an unverified
// label. A legacy bucket has none, so its origin-only sibling stays evidence only
// for rows that do not compare project IDs. The bucket remains searchable: a bare
// session ID in it is still found and still gets this label.
func TestFind_ProjectName_LegacyBucketOriginOnlyNeedsMatchingProjectID(t *testing.T) {
	stateHome := pinStateHome(t)

	// A legacy bucket: under the configured root, basename is not a project ID.
	legacy := filepath.Join(stateHome, "evener", "projects", "legacy-shared-scope")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir legacy bucket: %v", err)
	}
	if err := identifier.ValidateProjectID(filepath.Base(legacy)); err == nil {
		t.Fatalf("fixture basename %q is a valid project ID, so it cannot exercise the legacy case", filepath.Base(legacy))
	}

	// The only sibling is another repository's saved origin, and its checkout is
	// gone so nothing resolves to supply a project ID.
	foreign := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	foreign.EnvInfo.GitOriginURL = "git@github.com:owner/other-repository.git"
	foreign.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-other-checkout")
	if err := schema.SaveSessionMeta(legacy, foreign); err != nil {
		t.Fatal(err)
	}

	// The lane records its own project in its path and no owner, so its record
	// can be checked against a candidate's project ID — and this candidate states
	// none.
	laneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(newNamedCheckout(t, "lane-own-repository")))
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(legacy, "worktrees", laneProject.ID, "dlg_scoped")
	if got := projectName(lane, legacy); got != "" {
		t.Fatalf("projectName(pruned lane in a legacy bucket whose only sibling records another repository's origin) = %q, want empty: an unverified label needs verified per-project scope", got)
	}
}

// TestFind_ProjectName_EmptyOriginBasenameDoesNotBlockTheScan covers a sibling
// whose saved origin ends in "/.git", which this resolver reads as a local path
// and whose last segment after stripping ".git" is empty. Such an origin states
// no repository name at all, so it is no evidence — and no evidence must not
// stand in front of the sibling after it or of a restore root that does name
// something.
//
// A name-only row also takes the origin-first pass, so the empty name is tried
// there first: returning it would answer the row with "" and stop the scan before
// any usable sibling was read.
func TestFind_ProjectName_EmptyOriginBasenameDoesNotBlockTheScan(t *testing.T) {
	stateHome := pinStateHome(t)

	bucket, err := RuntimeDirForProjectWithStateHome(
		identifier.ProjectFromCanonicalPath(identifier.ResolveClean(newNamedCheckout(t, "useful-repo"))), stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The unusable origin is written FIRST, so a scan that stops at it never
	// reaches the sibling that can answer.
	empty := schema.SessionMeta{ID: identifier.MustNewSessionID(), UpdatedAt: time.Now().Add(time.Hour).UTC()}
	empty.EnvInfo.GitOriginURL = "/srv/mirror/checkout/.git"
	empty.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-empty-origin-checkout")
	if err := schema.SaveSessionMeta(bucket, empty); err != nil {
		t.Fatal(err)
	}

	// The sibling that can answer, with its checkout gone so only its recorded
	// restore root names a repository.
	usable := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	usable.WorktreeRestoreRoot = filepath.Join(testfs.CanonicalTempDir(t), "usable-restore-repo")
	usable.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-usable-checkout")
	if err := schema.SaveSessionMeta(bucket, usable); err != nil {
		t.Fatal(err)
	}

	// A name-only row: a pruned ownerless lane with no project of its own.
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", "dlg_empty_origin")
	if got, want := projectName(row, bucket), "usable-restore-repo"; got != want {
		t.Fatalf("projectName(pruned lane beside a sibling whose origin names nothing and one whose restore root does) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_ClassifiesWhereThePathReallyIs covers the two ways the
// resolver used to decide what kind of path it had from the path's spelling
// rather than from where it points, in one fixture because they are one
// classification.
//
// M3: a sibling records a working directory that is a symlink to a repository
// subdirectory. Walking the symlink's lexical parents misses the repository's
// .git, so the later canonicalization hands the subdirectory a project of its
// own and a deleted managed lane rejects the sibling's valid saved origin as
// conflicting evidence.
//
// M4: a configured state directory sits inside repository A's own .worktrees
// checkout. Every lane in it therefore has an ancestral ".worktrees" segment, and
// the in-repo marker fallback names A — before the lane's verified parent can
// name the project the lane actually belongs to. A path already classified as a
// state lane must not fall through to an ancestry-based marker.
func TestFind_ProjectName_ClassifiesWhereThePathReallyIs(t *testing.T) {
	// The state directory lives inside checkout A, which is what gives every lane
	// below it an ancestral ".worktrees" segment.
	checkoutA := newNamedCheckout(t, "repo-a")
	stateHome := filepath.Join(checkoutA, ".worktrees", "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucket := filepath.Join(stateHome, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// M3's sibling: repo B has a subdirectory "pkg", and the sibling records a
	// SYMLINK to it rather than the path itself.
	repoSub := filepath.Join(repoB, "pkg")
	if err := os.MkdirAll(repoSub, 0o755); err != nil {
		t.Fatalf("mkdir repo sub: %v", err)
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "aliased-subdir")
	if err := os.Symlink(repoSub, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	sibling.EnvInfo.WorkingDir = alias
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The row is a deleted managed lane for B with no owner metadata and no parent,
	// so the sibling's saved origin is the only thing that can name it.
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_aliased")
	if got, want := projectName(lane, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(deleted lane whose only sibling records a symlinked subdirectory) = %q, want the sibling's saved origin %q", got, want)
	}

	// M4's fixture: the same arrangement, but with an originless lane whose
	// verified parent names B. The enclosing checkout A must not answer for it,
	// even though A's ".worktrees" is an ancestor of the lane path.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}
	orphan := schema.SessionMeta{ParentSessionID: parent.ID}
	orphan.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_orphan")
	if got, want := projectName(orphan, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(deleted lane inside a state dir under another checkout's .worktrees) = %q, want its parent's %q (not the enclosing checkout)", got, want)
	}
}

// TestFind_ProjectName_SiblingScanSkippedWhenRowEvidenceWins covers the cost the
// review measured: several originless deleted lanes from one bucket, each already
// answered by its own owner, still walked every sibling's recorded checkout. The
// scan is bucket-wide, so that is one checkout resolution per sibling per row.
//
// The assertion is on the work, not on the answer: an answer cannot distinguish
// the two implementations, because row-specific evidence outranks the bucket
// whatever the scan says. siblingEvidence is only built when the scan actually
// runs, so an empty map after an owner-resolved row proves the scan was skipped.
// The last case proves the short-circuit is not a blanket suppression.
func TestFind_ProjectName_SiblingScanSkippedWhenRowEvidenceWins(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "owner-named-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.EnvInfo.GitOriginURL = "git@github.com:owner/owner-named-repo.git"
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatal(err)
	}

	// Discovery's snapshot, which is the case that reuses sibling evidence. It is
	// also what makes the reuse observable: without a snapshot nothing is
	// remembered, deliberately, so a later write cannot be masked by stale work.
	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "dlg_owned")
	if got, want := resolver.projectName(row, bucket), "owner-named-repo"; got != want {
		t.Fatalf("projectName(owner-resolved row) = %q, want the owner's %q", got, want)
	}
	for _, key := range []siblingEvidenceKey{{bucketDir: bucket}, {bucketDir: bucket, needProjectID: true}} {
		if _, scanned := resolver.siblingEvidence[key]; scanned {
			t.Fatalf("owner-resolved row built the bucket's sibling evidence (%#v): the scan ran for a row that needed nothing from it", key)
		}
	}

	// The short-circuit must not suppress a row that genuinely needs the scan.
	unowned := schema.SessionMeta{}
	unowned.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_unowned")
	if got, want := resolver.projectName(unowned, bucket), "owner-named-repo"; got != want {
		t.Fatalf("projectName(unowned lane needing the sibling scan) = %q, want %q", got, want)
	}
	// This lane carries a project ID in its path, so the snapshot it needs is the
	// one that resolves each sibling's ID for comparison.
	if _, scanned := resolver.siblingEvidence[siblingEvidenceKey{bucketDir: bucket, needProjectID: true}]; !scanned {
		t.Fatalf("lane with no row-specific evidence did not build the bucket's sibling evidence: the scan was suppressed for a row that needed it")
	}
}

// TestFind_ProjectName_SymlinkedSessionsDoesNotPanicOnFirstSiblingLookup drives
// the interleaving the review named: candidate discovery sees a real sessions/
// directory, the directory is replaced with a symlink, and the first sibling
// lookup then rejects it. The rejection must skip sibling inference, not write
// through an unallocated map — a guard that panics is worse than the layout it
// guards.
//
// The map is populated at construction precisely so this ordering cannot matter,
// so the test asserts both halves: the lookup returns no evidence and does not
// panic, and a bucket that was never refused still gets its evidence.
func TestFind_ProjectName_SymlinkedSessionsDoesNotPanicOnFirstSiblingLookup(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "replaced-sessions-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	sessionsDir := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/replaced-sessions-repo.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// Discovery's snapshot is taken while sessions/ is a real directory. This is
	// the snapshot a search would hold when the directory is swapped underneath it.
	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}

	// Swap the real directory for a symlink pointing at another directory that
	// does hold metadata, which is the layout the guard exists to reject.
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir elsewhere: %v", err)
	}
	if err := os.RemoveAll(sessionsDir); err != nil {
		t.Fatalf("remove sessions: %v", err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "sessions"), sessionsDir); err != nil {
		t.Fatalf("symlink sessions: %v", err)
	}

	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})
	// A lane with no row-specific evidence reaches the sibling lookup, which is
	// the first thing to notice the swap. It must return empty, not panic.
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_swapped")
	if got := resolver.projectName(lane, bucket); got != "" {
		t.Fatalf("projectName(lane whose bucket sessions/ became a symlink) = %q, want empty (inference skipped, not served from the linked directory)", got)
	}

	// The refusal is memoized per bucket and must not leak to a healthy bucket.
	healthyRepo := newNamedCheckout(t, "healthy-repo")
	healthyProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(healthyRepo))
	healthy, err := RuntimeDirForProjectWithStateHome(healthyProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if healthy == bucket {
		t.Fatalf("fixture buckets collided at %q", bucket)
	}
	if err := os.MkdirAll(filepath.Join(healthy, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir healthy sessions: %v", err)
	}
	healthySibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	healthySibling.EnvInfo.GitOriginURL = "git@github.com:owner/healthy-repo.git"
	if err := schema.SaveSessionMeta(healthy, healthySibling); err != nil {
		t.Fatal(err)
	}
	healthyLane := schema.SessionMeta{}
	healthyLane.EnvInfo.WorkingDir = filepath.Join(healthy, "worktrees", healthyProject.ID, "dlg_healthy")
	if got, want := resolver.projectName(healthyLane, healthy), "healthy-repo"; got != want {
		t.Fatalf("projectName(lane in an untouched bucket after another bucket was refused) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_SymlinkedLaneKeepsItsBoundedWalk covers a recorded
// working directory that is a symlink to a state lane whose own directory
// survives but whose .git entry is gone. The lane check is what chooses the walk
// boundary, so classifying the alias rather than what it points to means the path
// is not seen as a lane at all: the walk is unbounded, climbs out of the lane,
// and names the repository enclosing the state directory before the verified
// parent is consulted.
//
// The state directory lives inside another checkout's .worktrees precisely so the
// unbounded walk has a wrong answer available; with the walk correctly bounded
// the parent's project is what answers.
func TestFind_ProjectName_SymlinkedLaneKeepsItsBoundedWalk(t *testing.T) {
	checkoutA := newNamedCheckout(t, "repo-a")
	stateHome := filepath.Join(checkoutA, ".worktrees", "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucket := filepath.Join(stateHome, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The parent names project B, so it is the evidence that must answer.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	// The lane's own directory survives, with no .git entry, so its own pointer
	// cannot resolve it. The row records the lane through a symlink.
	laneDir := filepath.Join(bucket, "worktrees", projectB.ID, "dlg_symlinked")
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "lane-alias")
	if err := os.Symlink(laneDir, alias); err != nil {
		t.Fatalf("symlink lane alias: %v", err)
	}

	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = alias
	if got, want := projectName(row, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(row recording a symlink to a deleted-checkout lane) = %q, want its parent's %q (not the repository enclosing the state dir)", got, want)
	}
}

// TestFind_ProjectName_MissingSubdirOfSymlinkedLaneKeepsItsBounds covers a
// recorded working directory that names a DELETED subdirectory of a surviving
// lane, with the state home reached through a symlink.
//
// Full symlink resolution leaves the missing path in its original spelling while
// resolving the existing lane root to its physical spelling, so the two never
// compare equal: the walk does not stop at the lane root. If the lane has also
// lost its .git entry there is nothing to stop it, and it labels the transcript
// with the checkout that encloses the state directory — a repository the session
// never ran in.
//
// Resolving the existing prefix of both paths, the way sameBucket already does,
// keeps them comparable, so containment holds and the answer comes from the
// lane's verified parent instead.
func TestFind_ProjectName_MissingSubdirOfSymlinkedLaneKeepsItsBounds(t *testing.T) {
	// The enclosing checkout is what an escaped walk would answer with. Both the
	// physical state home AND the link live inside it, so a walk that climbs the
	// link's lexical parents reaches the checkout's own .git. A link placed
	// outside any repository would let the same escape return nothing and prove
	// nothing.
	checkoutA := newNamedCheckout(t, "enclosing-repo")
	physical := filepath.Join(checkoutA, "state")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatalf("mkdir physical state home: %v", err)
	}
	linkedHome := filepath.Join(checkoutA, "state-home-link")
	if err := os.Symlink(physical, linkedHome); err != nil {
		t.Fatalf("symlink state home: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", linkedHome)

	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	bucket := filepath.Join(physical, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The parent names project B, so it is the evidence that must answer.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = repoB
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	// The lane survives with no .git entry. The recorded working directory is a
	// subdirectory of it that was deleted, reached through the symlinked home so
	// the recorded spelling is the link's.
	//
	// The lane is written where a managed lane actually goes,
	// <bucket>/worktrees/<projectID>/<lane>, so the recorded path's lane root is a
	// directory that exists. A lane directory built without the project segment
	// would make the lane root nonexistent, and a walk from a path with no
	// existing ancestor inside the state tree proves nothing about containment.
	laneDir := filepath.Join(bucket, "worktrees", projectB.ID, "dlg_escaped")
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	recorded := filepath.Join(linkedHome, "evener", "projects", projectB.ID, "worktrees", projectB.ID, "dlg_escaped", "pkg")
	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = recorded
	if got, want := projectName(row, bucket), "repo-b"; got != want {
		t.Fatalf("projectName(row recording a deleted subdirectory of a surviving lane through a symlinked state home) = %q, want its parent's %q (not the checkout enclosing the state dir)", got, want)
	}
}

// TestFind_ProjectName_MissingSubdirOfSymlinkedLiveLaneResolvesPointer is the
// other half of the containment rule, and it is what keeps refusal from being the
// only answer. The lane root is LIVE — it still holds a linked-worktree .git
// pointer naming its main checkout — and the recorded directory is a subdirectory
// of it that has been deleted, spelled through the symlinked state home.
//
// Refusing the walk whenever the two spellings disagree would lose this: the
// lane's own pointer is what identifies the project, and it is reachable. The
// prefix of each path has to be resolved consistently so containment holds and
// the walk still reaches the pointer. Answering the parent's label instead would
// pass a containment-only check by luck of the fixture, so the two labels are
// made to differ.
func TestFind_ProjectName_MissingSubdirOfSymlinkedLiveLaneResolvesPointer(t *testing.T) {
	mainCheckout, liveLane := gittest.NewLinkedWorktree(t)

	physical := t.TempDir()
	linkedHome := filepath.Join(t.TempDir(), "state-home-link")
	if err := os.Symlink(physical, linkedHome); err != nil {
		t.Fatalf("symlink state home: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", linkedHome)

	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(mainCheckout))
	bucket := filepath.Join(physical, "evener", "projects", projectB.ID)
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "worktrees", projectB.ID), 0o755); err != nil {
		t.Fatalf("mkdir lane parent: %v", err)
	}

	// The lane root is the live linked worktree, so its .git pointer is what the
	// bounded walk has to reach.
	laneDir := filepath.Join(bucket, "worktrees", projectB.ID, "dlg_live")
	if err := os.Symlink(liveLane, laneDir); err != nil {
		t.Fatalf("symlink live lane into the bucket: %v", err)
	}

	// A parent names a DIFFERENT repository, so a walk that gives up and asks the
	// parent is visible in the result.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/parent-decoy-repo.git"
	parent.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-parent-checkout")
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	recorded := filepath.Join(linkedHome, "evener", "projects", projectB.ID, "worktrees", projectB.ID, "dlg_live", "pkg")
	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = recorded
	if got, want := projectName(row, bucket), filepath.Base(mainCheckout); got != want {
		t.Fatalf("projectName(deleted subdir of a live lane through a symlinked state home) = %q, want the lane's own main checkout %q (not the parent's %q)", got, want, "parent-decoy-repo")
	}
}

// TestFind_ProjectName_MissingSiblingCheckoutProvesNothing covers a sibling whose
// checkout was nested inside a different repository and has since been removed.
// Walking from the missing path finds the enclosing repository's .git, so the
// deleted directory inherits an identity it never had — and a deleted managed lane
// then rejects that sibling's saved origin as conflicting evidence, losing the
// only label it had.
//
// Absence of a path proves nothing about its project. The saved origin must stay
// eligible, and the project ID recorded in the lane's own path must not be
// weakened.
func TestFind_ProjectName_MissingSiblingCheckoutProvesNothing(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// The enclosing repository, whose .git is what an unbounded walk from the
	// missing path would find.
	enclosing := newNamedCheckout(t, "enclosing-repo")

	// The lane's repository, and a bucket for it.
	laneRepo := newNamedCheckout(t, "lane-repo")
	laneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(laneRepo))
	bucket, err := RuntimeDirForProjectWithStateHome(laneProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling's checkout lived inside the enclosing repository and is now
	// gone. Its saved origin is the only surviving record of the lane's project.
	goneCheckout := filepath.Join(enclosing, "vendor", "lane-repo-copy")
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/lane-repo.git"
	sibling.EnvInfo.WorkingDir = goneCheckout
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The row is a deleted managed lane for the lane's project, with no owner and
	// no parent, so the sibling's saved origin is the only thing that can name it.
	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", laneProject.ID, "dlg_missing_sibling")
	if got, want := projectName(lane, bucket), "lane-repo"; got != want {
		t.Fatalf("projectName(lane whose only sibling's checkout was removed from inside another repo) = %q, want the sibling's saved origin %q", got, want)
	}

	// The project ID recorded in a DELETED managed lane is still its own answer,
	// which is the property the existence check must not touch.
	deletedLane := schema.SessionMeta{}
	deletedLane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", laneProject.ID, "dlg_records_its_project")
	if got, want := projectName(deletedLane, bucket), "lane-repo"; got != want {
		t.Fatalf("projectName(deleted managed lane recording its project) = %q, want %q", got, want)
	}
}

// symlinkedBucket builds a bucket whose physical directory is reached through a
// symlink the test makes itself, and returns both spellings: the canonical one
// session construction records, and the lexical one a launcher may have named.
//
// The link is built here instead of inherited from the host's temp prefix. A
// developer machine whose temp directory sits behind a symlink (macOS's /var)
// exercises the difference by accident, and a Linux runner never does, so a
// fixture relying on the host asserts nothing on CI. Requiring the two spellings
// to differ keeps the trigger live on whatever filesystem runs this.
func symlinkedBucket(t *testing.T) (canonical, lexical string) {
	t.Helper()
	// The expected physical spelling is canonical for the same reason the bucket
	// is: t.TempDir() returns the lexical path, and on a host whose temp prefix is
	// a symlink the two differ, so comparing against the raw spelling would fail at
	// setup for a reason unrelated to what these tests exercise.
	physical := testfs.CanonicalTempDir(t)
	lexical = filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(physical, lexical); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	canonical = canonicalStateDir(lexical)
	if canonical != physical {
		t.Fatalf("canonicalStateDir(%q) = %q, want the physical target %q", lexical, canonical, physical)
	}
	if canonical == lexical {
		t.Fatalf("fixture has no symlinked prefix: %q is its own canonical form", lexical)
	}
	return canonical, lexical
}

// TestFind_ProjectName_OwnerRestoreSymlinkAliasIsStillALane covers an owner whose
// restore root is a symlink to a surviving state lane. The structural lookup
// resolves the alias, recognizes the lane, and answers "no project" — its leaf is
// a lane name. Classifying the alias instead of the resolved path sends the alias
// down the ordinary-directory branch, where its basename is offered as a project
// name ahead of the parent that can still name the repository.
func TestFind_ProjectName_OwnerRestoreSymlinkAliasIsStillALane(t *testing.T) {
	// The bucket is the canonical spelling, which is what session construction
	// records. The lane below is built under the same spelling and the alias
	// resolves to it physically, so a fixture that kept the lexical spelling here
	// would compare a physical lane path against a lexical bucket and label the
	// row from the alias — on any host, because the link is made here.
	stateDir, _ := symlinkedBucket(t)
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// A surviving lane directory with no usable Git metadata: nothing in it, or
	// above it below the lane root, identifies a checkout.
	liveLane := filepath.Join(stateDir, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_restore")
	if err := os.MkdirAll(liveLane, 0o755); err != nil {
		t.Fatalf("mkdir live lane: %v", err)
	}
	// The alias the owner recorded: a symlink to the lane, not a directory of its
	// own, and its own name says nothing about a repository.
	alias := filepath.Join(t.TempDir(), "not-a-repo-name")
	if err := os.Symlink(liveLane, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.WorktreePath = alias
	owner.WorktreeRestoreRoot = alias
	if err := schema.SaveSessionMeta(stateDir, owner); err != nil {
		t.Fatal(err)
	}

	// The parent is the checkout the lane was created from, and its saved origin
	// is the only record that names the project.
	repo := newNamedCheckout(t, "repo-b")
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = repo
	if err := schema.SaveSessionMeta(stateDir, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "sessions", ownerID, "worktrees", "dlg_row")
	if got, want := projectName(row, stateDir), "repo-b"; got != want {
		t.Fatalf("projectName(lane whose owner recorded a symlink to a state lane) = %q, want the parent's %q", got, want)
	}
}

// TestFind_ProjectName_OwnerHomeSymlinkAliasIsStillALane covers the owner's
// HOME fallback, which is the same question as the restore-root branch asked
// about a different field. An owner that never entered a worktree records no
// restore root, so HomeDir answers with its working directory — here an alias to
// a state lane. Classified by the alias it is an ordinary directory and its leaf
// outranks the parent; classified from the structural lookup it is a lane and
// contributes nothing.
func TestFind_ProjectName_OwnerHomeSymlinkAliasIsStillALane(t *testing.T) {
	stateDir, _ := symlinkedBucket(t)
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	liveLane := filepath.Join(stateDir, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_home")
	if err := os.MkdirAll(liveLane, 0o755); err != nil {
		t.Fatalf("mkdir live lane: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "not-a-repo-name")
	if err := os.Symlink(liveLane, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	// The owner records no restore root: HomeDir answers with WorkingDir.
	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.EnvInfo.WorkingDir = alias
	if err := schema.SaveSessionMeta(stateDir, owner); err != nil {
		t.Fatal(err)
	}

	repo := newNamedCheckout(t, "repo-b")
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	parent.EnvInfo.WorkingDir = repo
	if err := schema.SaveSessionMeta(stateDir, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "sessions", ownerID, "worktrees", "dlg_home_row")
	if got, want := projectName(row, stateDir), "repo-b"; got != want {
		t.Fatalf("projectName(lane whose owner's home directory is a symlink to a state lane) = %q, want the parent's %q", got, want)
	}

	// An ordinary home directory keeps its basename: only a lane is excluded.
	// Without this, "no evidence" and "no fallback" would be the same code and the
	// exclusion above would be indistinguishable from having deleted the branch.
	plain := filepath.Join(t.TempDir(), "plain-workspace")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir plain home: %v", err)
	}
	plainOwner := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	plainOwner.EnvInfo.WorkingDir = plain
	if err := schema.SaveSessionMeta(stateDir, plainOwner); err != nil {
		t.Fatal(err)
	}
	plainRow := schema.SessionMeta{}
	plainRow.EnvInfo.WorkingDir = filepath.Join(stateDir, "sessions", plainOwner.ID, "worktrees", "dlg_plain_row")
	if got, want := projectName(plainRow, stateDir), "plain-workspace"; got != want {
		t.Fatalf("projectName(lane whose owner's home is an ordinary directory) = %q, want the recorded basename %q", got, want)
	}
}

// TestFind_ProjectName_SiblingRestoreSymlinkAliasIsStillALane covers the same
// alias classification on the sibling scan. A sibling's restore root is a symlink
// to a surviving state lane, so the resolved path is a lane that names no
// project; the alias, read as an ordinary directory, would offer its own leaf as
// a candidate instead. The ordinary-directory half of this fallback is pinned by
// TestFind_ProjectName_SiblingRestoreRootBasenameFallback.
func TestFind_ProjectName_SiblingRestoreSymlinkAliasIsStillALane(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	bucket, err := RuntimeDirForProjectWithStateHome(projectA, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	liveLane := filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_restore")
	if err := os.MkdirAll(liveLane, 0o755); err != nil {
		t.Fatalf("mkdir live lane: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "not-a-repo-name")
	if err := os.Symlink(liveLane, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	// The sibling's only surviving record is that alias. It names no project, so
	// it must not answer for the lane.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.WorktreeRestoreRoot = alias
	sibling.EnvInfo.WorkingDir = alias
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_alias_only")
	if got := projectName(lane, bucket); got != "" {
		t.Fatalf("projectName(lane whose only sibling recorded a symlink to a state lane) = %q, want empty (a lane leaf is not a project)", got)
	}
}

// TestFind_ProjectName_RestoreRootDoesNotMaskTheWorkingDir covers a parent that
// entered a managed worktree before the working directory was recorded absolute.
// Its restore root is a relative ".", which names no checkout; its working
// directory is still the absolute checkout the lane was created from. A record
// consulted through one preferred field answers with nothing and takes the
// still-valid field down with it, so the deleted lane loses its only label.
//
// Not parallel: the relative restore root is what this case is about.
func TestFind_ProjectName_RestoreRootDoesNotMaskTheWorkingDir(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	checkout := newNamedCheckout(t, "masked-repo")
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.WorktreeRestoreRoot = "."
	parent.EnvInfo.WorkingDir = checkout
	if err := schema.SaveSessionMeta(stateDir, parent); err != nil {
		t.Fatal(err)
	}

	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "worktrees", project.ID, "dlg_masked")
	if got, want := projectName(row, stateDir), filepath.Base(checkout); got != want {
		t.Fatalf("projectName(deleted lane whose parent recorded a relative restore root and an absolute working dir) = %q, want the working directory's %q", got, want)
	}
}

// TestFind_ProjectName_SiblingRestoreRootDoesNotMaskItsLane covers the same
// masking in the direction that mislabels rather than omits. A sibling in a
// legacy-named bucket — the one shape where sibling inference is still allowed
// without a project to check against — records a restore root that has
// disappeared AND the managed lane path it occupied, which records its project.
// Reading only the restore root makes the sibling look like an unrelated
// directory and lends its saved origin to the row; reading the lane path first
// makes it evidence for its own project and nothing for a different one.
func TestFind_ProjectName_SiblingRestoreRootDoesNotMaskItsLane(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	// A legacy bucket: the layout ancestor exists but the basename is not a
	// project ID, so the bucket records no project of its own.
	bucket := filepath.Join(stateHome, "evener", "projects", "legacy-bucket")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	repoB := newNamedCheckout(t, "repo-b")
	projectB := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoB))
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.WorktreeRestoreRoot = filepath.Join(t.TempDir(), "gone-restore")
	sibling.WorktreePath = filepath.Join(bucket, "worktrees", projectB.ID, "dlg_sibling_lane")
	sibling.EnvInfo.WorkingDir = sibling.WorktreePath
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/repo-b.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The row is a managed lane of a DIFFERENT project. B's origin must not label
	// it, and the sibling's lane path is what makes that provable.
	repoA := newNamedCheckout(t, "repo-a")
	projectA := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repoA))
	if projectA.ID == projectB.ID {
		t.Fatalf("fixture projects collided at %q", projectA.ID)
	}
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", projectA.ID, "dlg_other")
	if got := projectName(row, bucket); got != "" {
		t.Fatalf("projectName(lane of repo A beside a sibling whose restore root is gone and whose lane records repo B) = %q, want empty", got)
	}
}

// TestFind_ProjectName_DanglingWorkdirAliasUsesSavedLanePath covers the row
// itself having been launched through a symlink to its managed lane: the symlink
// is recorded as the working directory and the lane itself as the worktree path.
// Once the lane is removed the alias dangles, so a walk that starts from the
// alias climbs to whatever encloses it — here a checkout that is not the lane's
// project at all, and the exit is the alias basename. The lane path the record
// still carries is the identity, and it is what the row must be labelled from.
func TestFind_ProjectName_DanglingWorkdirAliasUsesSavedLanePath(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// The alias sits inside an unrelated checkout, so an unbounded walk from it
	// names that checkout rather than nothing.
	unrelated := newNamedCheckout(t, "unrelated-repo")
	alias := filepath.Join(unrelated, "lane-alias")
	if err := os.Symlink(filepath.Join(stateDir, "worktrees", "gone-project-0123456789", "dlg_alias"), alias); err != nil {
		t.Fatalf("symlink dangling alias: %v", err)
	}

	// The row records both: the alias as its working directory and the lane
	// itself as its saved worktree path. Its project is the one the lane path
	// records, whose checkout is gone.
	row := schema.SessionMeta{}
	row.WorktreePath = filepath.Join(stateDir, "worktrees", "gone-project-0123456789", "dlg_alias")
	row.EnvInfo.WorkingDir = alias
	if got := projectName(row, stateDir); got != "" {
		t.Fatalf("projectName(row whose dangling workdir alias sits inside %q and whose saved lane path records another project) = %q, want empty", "unrelated-repo", got)
	}
}

// TestFind_ProjectName_DeletedLaneUsesSurvivingRestoreRoot covers the originless
// root session of a flat state dir after its managed worktree is pruned while
// the original checkout is still there. The record keeps both the lane it
// occupied and the root it entered from; the lane field is tried first, and
// answering "" from it would discard a restore root that still names the
// project. A missing lane is no evidence, so the surviving root answers.
func TestFind_ProjectName_DeletedLaneUsesSurvivingRestoreRoot(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	checkout := newNamedCheckout(t, "surviving-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))

	// The lane is gone; the checkout it was cut from is not.
	row := schema.SessionMeta{}
	row.WorktreePath = filepath.Join(stateDir, "worktrees", project.ID, "dlg_pruned")
	row.WorktreeRestoreRoot = checkout
	row.EnvInfo.WorkingDir = row.WorktreePath
	if got, want := projectName(row, stateDir), "surviving-repo"; got != want {
		t.Fatalf("projectName(originless row whose lane was pruned and whose restore root still exists) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_PrunedLaneFallsBackToMovedRestoreRoot covers the case the
// surviving-restore-root rule misses: the checkout MOVED as well, so nothing on
// disk resolves. The record still holds an absolute restore root, and that path
// hashes to the same project ID the pruned lane's own path carries, so the
// recording identifies the lane's project without anything being walked.
//
// Landing on the empty lane fallback throws that away. The lane's own leaf is
// still kept out of the result: a lane name is not a repository name, and only
// the restore root supplies one.
func TestFind_ProjectName_PrunedLaneFallsBackToMovedRestoreRoot(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// The checkout is gone, and its recorded path is what the lane was cut under.
	// The path is canonical from the start, because the ID the lane's own path
	// records was computed from the canonical form: on a host whose temp prefix is
	// a symlink the lexical spelling hashes to a different ID.
	moved := filepath.Join(testfs.CanonicalTempDir(t), "moved-away-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(moved))

	row := schema.SessionMeta{}
	row.WorktreePath = filepath.Join(stateDir, "worktrees", project.ID, "dlg_pruned_moved")
	row.WorktreeRestoreRoot = moved
	row.EnvInfo.WorkingDir = row.WorktreePath
	if got, want := projectName(row, stateDir), "moved-away-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose saved restore root moved but still hashes to the lane's project) = %q, want the restore root's basename %q, not the lane leaf", got, want)
	}
}

// TestFind_ProjectName_PrunedLaneKeepsNoLeafWhenProjectIsUnrecorded is the
// negative half of the rule above: the restore-root basename is licensed by the
// restore root identifying the LANE'S project. An ownerless lane at
// <stateDir>/worktrees/<lane> records no project of its own, so no restore root
// can be shown to identify anything, and the label stays empty. Answering the
// lane's leaf here is the phantom project name the guard exists to prevent.
func TestFind_ProjectName_PrunedLaneKeepsNoLeafWhenProjectIsUnrecorded(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	moved := filepath.Join(t.TempDir(), "unverified-moved-repo")
	row := schema.SessionMeta{}
	// An ownerless lane: <stateDir>/worktrees/<lane>, with no project segment.
	row.WorktreePath = filepath.Join(stateDir, "worktrees", "dlg_no_project_segment")
	row.WorktreeRestoreRoot = moved
	row.EnvInfo.WorkingDir = row.WorktreePath
	if got := projectName(row, stateDir); got != "" {
		t.Fatalf("projectName(pruned ownerless lane whose restore root cannot be tied to a recorded project) = %q, want empty, never the lane leaf", got)
	}
}

// TestFind_ProjectName_MatchingRestoreRootDoesNotResolveSiblings covers a pruned
// managed lane whose own record already identifies its project: the recorded
// restore root hashes to the project ID the lane's path carries. The bucket also
// holds a sibling whose checkout is live, so resolving that sibling is work that
// succeeds and leaves a trace.
//
// The label is the same whether or not the sibling is resolved, because the
// row's own record wins. What differs is the work, and on an unavailable mount
// that work blocks the whole read-only search: the bucket-wide scan must not run
// for a row whose own record has already answered. pathResolutions is the
// evidence, since the answer cannot tell the two orders apart.
func TestFind_ProjectName_MatchingRestoreRootDoesNotResolveSiblings(t *testing.T) {
	stateHome := pinStateHome(t)

	// The lane's own restore root, gone but recorded. It is canonical from the
	// start, because the lane's path records the canonical hash.
	own := filepath.Join(testfs.CanonicalTempDir(t), "own-record-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(own))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling's checkout is LIVE and names a DIFFERENT project, so it can
	// never be this lane's label: only the work of asking it is under test.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.WorkingDir = newNamedCheckout(t, "sibling-live-checkout")
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	resolver := newProjectNameResolver()
	row := schema.SessionMeta{}
	row.WorktreePath = filepath.Join(bucket, "worktrees", project.ID, "dlg_pruned")
	row.WorktreeRestoreRoot = own
	row.EnvInfo.WorkingDir = row.WorktreePath
	if got, want := resolver.projectName(row, bucket), "own-record-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose recorded restore root matches its project) = %q, want its own record's %q", got, want)
	}
	if resolver.pathResolutions != 0 {
		t.Fatalf("the lookup resolved %d recorded path(s) although the row's own record already named its project; a checkout on an unavailable mount would hold the whole search", resolver.pathResolutions)
	}
}

// TestFind_ProjectName_OwnerLabelDoesNotWalkTheParentCheckout covers a pruned
// session-owned lane whose owner's restore root still resolves. The owner is the
// row's own record and outranks a parent, so the label is settled before the
// parent is read — and reading the parent resolves its recorded paths, a
// filesystem call that an unavailable mount stalls.
//
// Both records point at live checkouts, so every resolution succeeds and leaves
// a trace. Only the owner's one resolution is needed: a lookup that also read
// the parent's record cannot stop at one.
func TestFind_ProjectName_OwnerLabelDoesNotWalkTheParentCheckout(t *testing.T) {
	bucket := newConfiguredBucket(t)

	// The owner's restore root is live, so reading the owner's record resolves
	// exactly one path and produces the label.
	ownerRestore := filepath.Join(t.TempDir(), "owner-restore-repo")
	if err := os.MkdirAll(ownerRestore, 0o755); err != nil {
		t.Fatalf("mkdir owner restore: %v", err)
	}
	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID, WorktreeRestoreRoot: ownerRestore}
	owner.EnvInfo.WorkingDir = ownerRestore
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatal(err)
	}

	// The parent's checkout is live as well, so a lookup that consults it is
	// visible in the resolution count rather than merely slower.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.WorkingDir = newNamedCheckout(t, "parent-live-checkout")
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	resolver := newProjectNameResolver()
	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "dlg_pruned")
	if got, want := resolver.projectName(row, bucket), "owner-restore-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose owner restore root resolves) = %q, want the owner's %q", got, want)
	}
	if resolver.pathResolutions != 1 {
		t.Fatalf("the lookup resolved %d recorded paths; the owner's restore root needs exactly one, and the parent's live checkout is a second walk an unavailable mount stalls", resolver.pathResolutions)
	}
}

// TestFind_ProjectName_SurvivingWorktreeDirectoryKeepsTheRepoName covers an
// in-repository worktree whose Git metadata is gone twice over: the worktree's
// own .git pointer is removed, and so is the enclosing repository's .git.
//
// The directory itself survives. Nothing above it is a checkout any more, so a
// Git walk finds no root and the surviving directory becomes an ordinary project
// — and its basename is then a LANE name, not the repository the lane was cut
// from. The path still spells the repository's own worktree layout, which is the
// only evidence left, and it names the repository.
//
// The whole shape is constructed here — real directories, real .git entries,
// removed at known points — rather than borrowed from the host, so it proves the
// same thing wherever it runs.
func TestFind_ProjectName_SurvivingWorktreeDirectoryKeepsTheRepoName(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// A repository with a real worktree under its own .worktrees layout, so both
	// the linked-worktree pointer and the enclosing .git directory are real Git
	// metadata and removing them is a real loss rather than a fixture assumption.
	base := t.TempDir()
	repo := filepath.Join(base, "surviving-repo")
	gittest.Run(t, base, "init", "-q", "surviving-repo")
	gittest.Run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	lane := filepath.Join(repo, ".worktrees", "lane")
	gittest.Run(t, repo, "worktree", "add", "-q", lane, "-b", "lane-branch")

	if err := os.Remove(filepath.Join(lane, ".git")); err != nil {
		t.Fatalf("remove the worktree's Git pointer: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("remove the enclosing repository's Git metadata: %v", err)
	}

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = lane
	if got, want := projectName(row, stateDir), "surviving-repo"; got != want {
		t.Fatalf("projectName(worktree directory that outlived both its own and its repository's Git metadata) = %q, want the repository name %q, not the lane's own basename %q", got, want, filepath.Base(lane))
	}
}

// TestFind_ProjectName_SiblingRestoreRootLabelsTheProjectItsLaneRecords covers a
// sibling that keeps BOTH records: the pruned managed-lane path it ran in and the
// absolute root that lane was cut from. Both have moved.
//
// The lane path is where the project ID comes from — a managed lane's path
// records it — and the restore root is a separate field that can still display a
// name. When the root's canonical path hashes to that same ID, the two records
// agree about which project the sibling belonged to, and their agreement is
// exactly the claim this candidate makes.
//
// Discarding the root because the lane path was read first drops that claim, and
// an originless lane in the same bucket whose only evidence is this sibling then
// loses its label. The lane's own leaf stays suppressed: it is a lane name.
func TestFind_ProjectName_SiblingRestoreRootLabelsTheProjectItsLaneRecords(t *testing.T) {
	stateHome := pinStateHome(t)

	// The sibling's restore root is gone, and its canonical path is the project the
	// sibling's lane path records. The path is canonical from the start for the
	// reason above.
	root := filepath.Join(testfs.CanonicalTempDir(t), "sibling-restore-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(root))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling records no origin. Its pruned lane path carries the project ID,
	// and its separate restore root is the repository root it was cut from.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.WorktreePath = filepath.Join(bucket, "worktrees", project.ID, "dlg_sibling_lane")
	sibling.WorktreeRestoreRoot = root
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// Another originless lane: its own path records no project, it is gone, and it
	// has no owner or parent. The sibling is the only evidence in the bucket.
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", "dlg_no_project")
	if got, want := projectName(row, bucket), "sibling-restore-repo"; got != want {
		t.Fatalf("projectName(originless lane whose only evidence is a sibling's matching restore root) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_MatchingRestoreRootBeatsAnUnverifiedSibling covers a
// pruned managed lane whose OWN record identifies its project while a bucket
// sibling offers only an unverified origin.
//
// The lane's restore root is gone but recorded, and that path hashes to the
// project ID the lane's own path carries: the row has positively identified its
// project. The sibling saved a different repository's origin and its record
// establishes no project at all, so the sibling's label is bucket-wide inference
// with no project ID — admissible in a project bucket, because there the bucket's
// verified scope is what ties its sessions to one repository, but never a claim
// about THIS lane.
//
// A claim about the lane outranks an inference about its bucket. Answering the
// origin replaces a label the row can prove with one it merely inherits, and the
// result then depends on whether some other session ever used that bucket. The
// origin stays the answer when the lane's own record names nothing, which is what
// keeps an origin-only bucket labelling its pruned lanes.
func TestFind_ProjectName_MatchingRestoreRootBeatsAnUnverifiedSibling(t *testing.T) {
	stateHome := pinStateHome(t)

	// The lane's own restore root, gone but recorded. The bucket is the project
	// bucket for that same ID, so the lane's path and its restore root agree, and
	// the bucket's verified scope makes its sessions one repository.
	moved := filepath.Join(testfs.CanonicalTempDir(t), "own-moved-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(moved))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// An unrelated session that saved another repository's origin and nothing that
	// identifies a project: its recorded directory is gone, so reading its record
	// yields no ID and the candidate is bucket-wide inference only.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:other/other-repo.git"
	sibling.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "sibling-gone-checkout")
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// A managed lane: its path records its project, and it is gone from disk.
	row := schema.SessionMeta{}
	row.WorktreePath = filepath.Join(bucket, "worktrees", project.ID, "dlg_own_record")
	row.WorktreeRestoreRoot = moved
	row.EnvInfo.WorkingDir = row.WorktreePath
	if got, want := projectName(row, bucket), "own-moved-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose restore root matches its recorded project, beside a sibling's unverified origin) = %q, want its own record's %q", got, want)
	}
}

// TestFind_ProjectName_DeletedNestedCheckoutIsNotItsEnclosingRepo covers an
// originless ordinary checkout nested inside another repository, deleted, so
// only its recorded path survives. A Git walk from the missing directory climbs
// to the enclosing repository's .git and labels the row with it — an
// affiliation the deleted checkout never had. The directory does not exist, so
// its ancestry is not evidence; the recorded basename is.
func TestFind_ProjectName_DeletedNestedCheckoutIsNotItsEnclosingRepo(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	enclosing := newNamedCheckout(t, "enclosing-repo")
	gone := filepath.Join(enclosing, "pkg", "nested-checkout")

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = gone
	if got, want := projectName(row, stateDir), "nested-checkout"; got != want {
		t.Fatalf("projectName(originless row whose nested checkout was deleted from inside %q) = %q, want the recorded basename %q", "enclosing-repo", got, want)
	}
}

// TestFind_ProjectName_LaneThroughSymlinkedStateHomeKeepsBucketEvidence covers a
// historical lane recorded through a symlinked XDG_STATE_HOME. Discovery works
// from the physical bucket path; the record keeps the link's spelling. The lane
// is deleted, so resolving the full lane path fails and the alias survives, and
// comparing the two spellings as strings makes the lane read as foreign to its
// own bucket — owner and sibling evidence are discarded even though both name
// the same directory. The existing prefixes are what has to be compared.
func TestFind_ProjectName_LaneThroughSymlinkedStateHomeKeepsBucketEvidence(t *testing.T) {
	physical := t.TempDir()
	linkParent := t.TempDir()
	linkedHome := filepath.Join(linkParent, "state-home-link")
	if err := os.Symlink(physical, linkedHome); err != nil {
		t.Fatalf("symlink state home: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", linkedHome)

	bucket := filepath.Join(physical, "evener", "projects", "linked-lane-bucket")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/linked-lane-repo.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	// The row records the lane through the link; discovery opens the physical
	// bucket. The lane directory does not exist, so only the spelling differs.
	aliasBucket := filepath.Join(linkedHome, "evener", "projects", "linked-lane-bucket")
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(aliasBucket, "worktrees", "dlg_linked_lane")
	if got, want := projectName(row, bucket), "linked-lane-repo"; got != want {
		t.Fatalf("projectName(deleted lane recorded through a symlinked state home) = %q, want the sibling's %q", got, want)
	}
}

// TestFind_ProjectName_RecordedProjectOutranksForeignOrigin covers a managed
// lane whose path records its project while one sibling names a different
// repository by origin and another positively names the lane's project. The
// origin sibling is admissible — it is unknown-project evidence inside the
// bucket — but it is an inference where the other is a verification, and
// letting the tie fall to whichever row came first labels the lane with
// another repository. A verified identifier outranks an inference.
func TestFind_ProjectName_RecordedProjectOutranksForeignOrigin(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	laneRepo := newNamedCheckout(t, "verified-repo")
	laneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(laneRepo))
	bucket, err := RuntimeDirForProjectWithStateHome(laneProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The intruder is presented FIRST, which is the order that lets a tie answer
	// with the wrong repository.
	intruder := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	intruder.EnvInfo.GitOriginURL = "git@github.com:owner/intruder-repo.git"
	intruder.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-intruder")

	// This sibling positively identifies the lane's project: it sits in the
	// checkout the lane was cut from, so its resolved working directory carries
	// that project's ID as well as the origin that names it.
	verified := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	verified.EnvInfo.GitOriginURL = "git@github.com:owner/verified-repo.git"
	verified.EnvInfo.WorkingDir = laneRepo

	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{
		bucket: {intruder, verified},
	})
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", laneProject.ID, "dlg_ranked")
	if got, want := resolver.projectName(row, bucket), "verified-repo"; got != want {
		t.Fatalf("projectName(managed lane whose recorded project is named by one sibling while another sibling's origin names a different repo) = %q, want the verified %q", got, want)
	}

	// The negative direction: with no sibling naming the lane's project, the
	// origin-only inference must still answer.
	originOnly := newProjectNameResolver(map[string][]schema.SessionMeta{
		bucket: {intruder},
	})
	if got, want := originOnly.projectName(row, bucket), "intruder-repo"; got != want {
		t.Fatalf("projectName(managed lane whose only sibling records an origin) = %q, want the origin-only inference %q", got, want)
	}
}

// TestFind_ProjectName_MatchingSiblingOutranksForeignOrigin covers a managed
// lane whose bucket was reached by another repository through --state-dir. A
// sibling from that other repository records an origin but no project, so it is
// unknown-project evidence; another sibling resolves to the lane's own live
// checkout. Ranking by source alone lets the unverified origin win, because its
// rank is stronger than a resolved checkout — and the lane takes another
// repository's name even though its own checkout is still on disk.
func TestFind_ProjectName_MatchingSiblingOutranksForeignOrigin(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	laneRepo := newNamedCheckout(t, "own-checkout-repo")
	laneProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(laneRepo))
	bucket, err := RuntimeDirForProjectWithStateHome(laneProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The intruder's origin is recorded and its checkout is gone, which is the
	// shape that makes its rank the strongest a sibling can reach.
	intruder := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	intruder.EnvInfo.GitOriginURL = "git@github.com:owner/intruder-repo.git"
	intruder.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "deleted-intruder")

	// The lane's own checkout, still live. It names the lane's project by
	// resolving to it, which is a positive identification rather than an origin
	// recorded in a bucket that stopped belonging to this repository.
	own := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	own.EnvInfo.WorkingDir = laneRepo

	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{
		bucket: {intruder, own},
	})
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", laneProject.ID, "dlg_matching_sibling")
	if got, want := resolver.projectName(row, bucket), "own-checkout-repo"; got != want {
		t.Fatalf("projectName(lane whose own checkout sibling resolves while another sibling's origin names a different repo) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_MovedCheckoutKeepsItsRecordedProject covers an originless
// repository moved away from its recorded path, and its delegate lane pruned, so
// the only surviving record of the repository is an absolute path that no longer
// exists. Hashing that path yields the project ID the lane's own path stores, so
// the recording still proves which project the lane served. Discarding it for
// want of a directory loses a label the basename fallback used to supply.
func TestFind_ProjectName_MovedCheckoutKeepsItsRecordedProject(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// The recorded path is what the lane was created under; it no longer exists.
	// It is canonical from the start, because the lane's recorded project ID was
	// computed from the canonical form.
	moved := filepath.Join(testfs.CanonicalTempDir(t), "moved-repo")
	movedProject := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(moved))
	bucket, err := RuntimeDirForProjectWithStateHome(movedProject, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The parent is the only row left, and all it keeps is the moved checkout as
	// its working directory.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	parent.EnvInfo.WorkingDir = moved
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", movedProject.ID, "dlg_moved")
	if got, want := projectName(row, bucket), "moved-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose only surviving record is a moved checkout path matching the lane's project) = %q, want %q", got, want)
	}
}

// TestFind_ProjectName_RelativeRecordNeverUsesTheSearchersCheckout covers a
// legacy row that saved --dir . with no origin. The record names no checkout:
// resolving it would answer with whichever repository THIS process runs in. A
// search started from a linked worktree would then label the row with that
// worktree's main checkout, which the session never ran in.
//
// The test runs from a checkout of its own, built here, so the wrong answer is
// distinguishable from the right one: a walk of "." resolves to that checkout and
// returns its directory name, which is a name no fixture in this test carries.
func TestFind_ProjectName_RelativeRecordNeverUsesTheSearchersCheckout(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// The searcher's directory is a real checkout named for this test, so a walk of
	// "." has an answer to give and that answer is visibly not the row's.
	searcher := newNamedCheckout(t, "searchers-own-repo")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(searcher); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore chdir: %v", err)
		}
	})

	row := schema.SessionMeta{EnvInfo: schema.EnvironmentInfo{WorkingDir: "."}}
	if got := projectName(row, stateDir); got != "" {
		t.Fatalf("projectName(legacy row whose only record is a relative --dir, searched from %q) = %q, want empty", "searchers-own-repo", got)
	}
}

// TestFind_ProjectName_MovedParentCheckoutStillNamesItsLane covers a pruned
// managed lane in a flat --state-dir whose only surviving evidence is its
// parent's record. The parent is originless and its checkout has moved, so
// nothing on disk resolves, but the recorded absolute path hashes to the same
// project ID the lane's own path stores. That recording is evidence even after
// the directory it names is gone: the row's own admissibility decides whether
// the parent names the lane, so a parent from another project is still refused.
//
// Bucket-wide inference is disabled here, which is what makes the parent the
// only source that can answer: a flat override is not a per-project bucket.
func TestFind_ProjectName_MovedParentCheckoutStillNamesItsLane(t *testing.T) {
	moved := newNamedCheckout(t, "moved-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(moved))
	flat := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(flat, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir flat state dir: %v", err)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatalf("move the parent checkout away: %v", err)
	}

	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.WorkingDir = moved
	if err := schema.SaveSessionMeta(flat, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(flat, "worktrees", project.ID, "dlg_moved_parent")
	if got, want := projectName(row, flat), "moved-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose originless parent checkout moved) = %q, want the parent's recorded project %q", got, want)
	}
}

// TestFind_ProjectName_MovedParentCheckoutThroughSymlinkStillNamesItsLane is the
// same case with the symlink made explicitly, so it covers the same behavior on
// Linux and Darwin.
//
// On macOS /var is a symlink to /private/var, so a temp directory is already
// reached through one and this bug shows up there on its own. A test that only
// fails because the host happens to hide a link is not a proof: it passes on
// Linux whether the code is right or wrong. Building the link here makes the
// case identical on both.
//
// The parent's checkout is removed, so its project ID can only come from
// hashing the recorded path. That recorded path names the checkout through the
// link; the lane was created under the canonical form. Hashing the lexical path
// does not reproduce it, and the label is lost.
func TestFind_ProjectName_MovedParentCheckoutThroughSymlinkStillNamesItsLane(t *testing.T) {
	// A physical directory reached through a link, which is the shape /var has.
	physicalParent := t.TempDir()
	linkedParent := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(physicalParent, linkedParent); err != nil {
		t.Fatalf("symlink root: %v", err)
	}

	// The checkout is created at its PHYSICAL path, so the ID recorded when the
	// lane was made is the canonical one.
	movedPhysical := filepath.Join(physicalParent, "linked-moved-repo")
	if err := os.MkdirAll(movedPhysical, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(movedPhysical))

	flat := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(flat, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir flat state dir: %v", err)
	}
	if err := os.RemoveAll(movedPhysical); err != nil {
		t.Fatalf("move the parent checkout away: %v", err)
	}

	// The record keeps the spelling the session used: through the link.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.WorkingDir = filepath.Join(linkedParent, "linked-moved-repo")
	if err := schema.SaveSessionMeta(flat, parent); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(flat, "worktrees", project.ID, "dlg_linked_parent")
	if got, want := projectName(row, flat), "linked-moved-repo"; got != want {
		t.Fatalf("projectName(pruned lane whose parent checkout moved and was recorded through a symlink) = %q, want %q from the canonical hash", got, want)
	}
}

// TestFind_ProjectName_RelativeFallbacksNeverLabelADot covers the two basename
// fallbacks a legacy record can reach: a sibling's restore root and an owner's
// home directory. Both answer from a recorded path's last segment, which is only
// meaningful for a path that named a directory. A relative record names none —
// it would be read against whatever directory this process happens to run in —
// and "." is a label every such record shares, so neither fallback may produce
// it.
func TestFind_ProjectName_RelativeFallbacksNeverLabelADot(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(newNamedCheckout(t, "dot-repo")))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// A sibling that records only a relative restore root and no origin.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.WorktreeRestoreRoot = "."
	sibling.WorktreePath = filepath.Join(bucket, "worktrees", project.ID, "dlg_dot_sibling")

	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{
		bucket: {sibling},
	})
	ownerless := schema.SessionMeta{}
	ownerless.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_dot_lane")
	if got, want := resolver.projectName(ownerless, bucket), ""; got != want {
		t.Fatalf("projectName(ownerless lane whose only sibling records a relative restore root) = %q, want %q", got, want)
	}

	// An owner that records an origin-less relative home directory.
	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.EnvInfo.WorkingDir = "."
	if err := schema.SaveSessionMeta(bucket, owner); err != nil {
		t.Fatal(err)
	}
	owned := schema.SessionMeta{}
	owned.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", ownerID, "worktrees", "dlg_dot_owner")
	if got, want := resolver.projectName(owned, bucket), ""; got != want {
		t.Fatalf("projectName(lane whose owner records a relative home directory) = %q, want %q", got, want)
	}
}
