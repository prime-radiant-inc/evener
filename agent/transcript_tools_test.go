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
	t.Parallel()

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

	// That answer is reusable for the bucket, and it must actually have been
	// cached: the resolver is shared across result rows, and a foreign-project
	// read deliberately does not cache. Removing the only record of the origin
	// leaves the cache as the sole source for the next row.
	if err := os.Remove(schema.SessionMetaPath(bucket, sibling.ID)); err != nil {
		t.Fatalf("remove sibling meta: %v", err)
	}
	nextLane := schema.SessionMeta{}
	nextLane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_same_project_next")
	if got := r.projectName(nextLane, bucket); got != want {
		t.Fatalf("projectName(second lane in the same bucket) = %q, want the cached %q", got, want)
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

// --- roborev fix round 3: RED test for finding 4 ---

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
