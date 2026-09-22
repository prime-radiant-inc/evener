package agent

// Tests pinning the pasted-image persistence contract: when the session
// accepts user input carrying image attachments and has a state directory,
// each image's bytes are written under the session's attachments directory
// at a unique, content-addressed path, and the user message gains a
// <system-notification> part naming that path so the model can re-read the
// file later (read_file routes image bytes back into context). Sessions
// without a state directory, or whose attachments directory cannot be
// written, keep today's behavior: the image rides the message inline and
// no path is announced.
//
// Assertions are structural: the exact on-disk path (a machine contract:
// content-addressed, stable across retries), the file's bytes, file modes,
// and the presence of the system-notification tag around the path. The
// natural-language sentence inside the tag is prompt prose and is never
// pinned.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// newImagePersistenceSession stands up a session whose model replies are
// scripted by the given steps, with StateDir set to stateDir ("" disables
// state persistence, mirroring the library/test shape).
func newImagePersistenceSession(t *testing.T, stateDir string, steps ...func(req llm.Request) llm.Response) *Session {
	t.Helper()
	return newImagePersistenceSessionWithEnv(t, stateDir, execenv.NewLocalExecutionEnvironment(t.TempDir()), steps...)
}

// newImagePersistenceSessionWithEnv is newImagePersistenceSession with the
// session's execution environment supplied by the caller, for tests that pin
// behavior under a sandboxed env.
func newImagePersistenceSessionWithEnv(t *testing.T, stateDir string, env execenv.ExecutionEnvironment, steps ...func(req llm.Request) llm.Response) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: steps})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	// With a state directory the background session namer shares the
	// session's LLM client and would race the scripted turn replies for
	// steps. Give it its own client that always declines to name — the
	// established fixture pattern (session_fold_publication_test.go).
	namerClient := llm.NewClient()
	namerClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"name":""}`)}
	}})
	updateSessionTestConfig(sess, func(cfg *testConfig) { cfg.namerClient = namerClient })
	return sess
}

// newSandboxedImagePersistenceSession builds the session with a sandboxed
// environment: the policy resolves against a fresh worktree exactly the way
// the daemon resolves a session policy at startup, so the file-tool scope the
// tests pin is the real resolved policy, not a hand-built approximation.
func newSandboxedImagePersistenceSession(t *testing.T, stateDir string, policy sandbox.SandboxPolicy, steps ...func(req llm.Request) llm.Response) *Session {
	t.Helper()
	worktree := t.TempDir()
	net := true
	policy.Network = &net
	rp, err := sandbox.Resolve(policy, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}, worktree)
	if err != nil {
		t.Fatalf("sandbox.Resolve: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	return newImagePersistenceSessionWithEnv(t, stateDir, env, steps...)
}

// expectedAttachmentPath derives the contract path for an attachment: a
// sha256 prefix of the bytes, then the sanitized original name, under
// <stateDir>/sessions/<sessionID>/attachments/.
func expectedAttachmentPath(t *testing.T, stateDir, sessionID string, img ImageAttachment) string {
	t.Helper()
	// The session records canonicalStateDir's resolution of the state dir, so
	// the note names the canonical spelling; expectations must match it (see
	// TestExpectedAttachmentPathUsesCanonicalStateDir).
	stateDir = resolvedPath(t, stateDir)
	sum := sha256.Sum256(img.Data)
	return filepath.Join(stateDir, "sessions", sessionID, "attachments", hex.EncodeToString(sum[:])[:16]+"-"+sanitizeAttachmentName(img))
}

// TestExpectedAttachmentPathUsesCanonicalStateDir pins the fixture helper
// itself: attachment-path expectations must use the canonical
// (symlink-resolved) state-dir spelling, because the session records
// canonicalStateDir's resolution and the announced note names that path. A
// raw fixture spelling compares two names for one file and fails on hosts
// whose temp roots sit behind symlinks (macOS /var, /tmp) — on Linux the two
// spellings are identical, which is why CI never sees it (the skillFixtureRoot
// trap, in miniature).
func TestExpectedAttachmentPathUsesCanonicalStateDir(t *testing.T) {
	t.Parallel()
	physical := t.TempDir()
	if err := os.Mkdir(filepath.Join(physical, "s"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "alias")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	img := ImageAttachment{MediaType: "image/png", Data: []byte("png-bytes"), Name: "shot.png"}
	want := expectedAttachmentPath(t, filepath.Join(physical, "s"), "sess-1", img)
	got := expectedAttachmentPath(t, filepath.Join(link, "s"), "sess-1", img)
	if got != want {
		t.Fatalf("expectedAttachmentPath through symlinked state dir = %q, want the canonical %q", got, want)
	}
}

// findSystemNotificationPart returns the first text part wrapped in the
// <system-notification> machinery tag, if any.
func findSystemNotificationPart(msg llm.Message) (string, bool) {
	for _, p := range msg.Content {
		if p.Kind == llm.ContentText && strings.HasPrefix(p.Text, "<system-notification>") {
			return p.Text, true
		}
	}
	return "", false
}

// historyTurnsWhere returns every history turn matching pred, copied by
// value under the session lock so callers never hold references into
// history past the unlock.
func historyTurnsWhere(t *testing.T, sess *Session, pred func(schema.Turn) bool) []schema.Turn {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var out []schema.Turn
	for _, turn := range sess.history {
		if pred(turn) {
			out = append(out, turn)
		}
	}
	return out
}

// lastTurnOfKind returns the most recent history turn of the given kind.
func lastTurnOfKind(t *testing.T, sess *Session, kind schema.TurnKind) schema.Turn {
	t.Helper()
	turns := historyTurnsWhere(t, sess, func(turn schema.Turn) bool { return turn.Kind == kind })
	if len(turns) == 0 {
		t.Fatalf("no %s turn in history", kind)
	}
	return turns[len(turns)-1]
}

// hasImagePart reports whether the message carries any ContentImage part.
func hasImagePart(msg llm.Message) bool {
	return slices.ContainsFunc(msg.Content, func(p llm.ContentPart) bool { return p.Kind == llm.ContentImage })
}

func replyStep(message string) func(req llm.Request) llm.Response {
	return func(req llm.Request) llm.Response { return finalResponse(message) }
}

// collectWarnings drains the session's event stream in the background,
// forwarding warning events to the returned channel.
func collectWarnings(sess *Session) <-chan events.SessionEvent {
	warnCh := make(chan events.SessionEvent, 16)
	go func() {
		for event := range sess.Events() {
			if event.Kind == events.EventWarning {
				warnCh <- event
			}
		}
	}()
	return warnCh
}

// awaitWarningNaming blocks until a warning naming want arrives on ch,
// tolerating unrelated warnings emitted while the turn runs. The channel
// receives are the await; the bound is a tripwire for a genuine hang,
// never the mechanism.
func awaitWarningNaming(t *testing.T, ch <-chan events.SessionEvent, want string) {
	t.Helper()
	// TRIPWIRE: the matching warning is emitted synchronously during the
	// turn, so the receives are the await; 30s only fires on a hang.
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-ch:
			data, ok := event.Data.(events.WarningData)
			if !ok || !strings.Contains(data.Message, want) {
				continue // unrelated warning; keep waiting for the match
			}
			return
		case <-deadline.C:
			t.Fatalf("no warning naming %q was emitted", want)
		}
	}
}

// TestProcessInput_PersistsImageAttachmentToDisk pins the core contract:
// an accepted image-bearing user turn writes the exact bytes to the
// content-addressed path, the user message carries a system-notification
// part containing that path, and re-pasting the same bytes reuses the same
// path instead of accumulating duplicates.
func TestProcessInput_PersistsImageAttachmentToDisk(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("first reply"), replyStep("second reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("attachment not persisted at %s: %v", wantPath, err)
	}
	if !bytes.Equal(got, png) {
		t.Fatalf("attachment bytes differ: got %d bytes, want the original %d", len(got), len(png))
	}
	if info, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat attachment: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("attachment mode = %o, want 600", perm)
	}
	if info, err := os.Stat(filepath.Dir(wantPath)); err != nil {
		t.Fatalf("stat attachments dir: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("attachments dir mode = %o, want 700", perm)
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Fatalf("user turn lost its ContentImage part: %+v", turn.Message.Content)
	}
	note, ok := findSystemNotificationPart(turn.Message)
	if !ok {
		t.Fatalf("user turn has no system-notification part: %+v", turn.Message.Content)
	}
	if !strings.Contains(note, wantPath) {
		t.Errorf("system-notification part does not name the stored path %q: %q", wantPath, note)
	}

	// The same bytes pasted again reuse the same content-addressed path.
	if _, err := sess.ProcessInput(context.Background(), "same shot again", []ImageAttachment{img}); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(wantPath))
	if err != nil {
		t.Fatalf("ReadDir attachments: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("attachments dir holds %d files after re-pasting identical bytes, want 1: %+v", len(entries), entries)
	}
	second := lastTurnOfKind(t, sess, schema.TurnUserInput)
	note2, ok := findSystemNotificationPart(second.Message)
	if !ok || !strings.Contains(note2, wantPath) {
		t.Errorf("second turn's system-notification part missing or wrong path: %q", note2)
	}
}

// TestProcessInput_NoStateDir_LeavesImageUnpersisted pins the degradation
// gate: a session without a state directory behaves exactly as before — the
// image rides the message inline and nothing is announced or written.
func TestProcessInput_NoStateDir_LeavesImageUnpersisted(t *testing.T) {
	t.Parallel()
	sess := newImagePersistenceSession(t, "", replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Fatalf("user turn lost its ContentImage part: %+v", turn.Message.Content)
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("session without StateDir announced a stored path: %+v", turn.Message.Content)
	}
}

// TestProcessInput_RestrictedSandbox_UnreadableStateDir_OmitsAttachmentNote
// pins the sandbox half of the annotation contract: a restricted session's
// file tools may only read its worktree, so when the state dir lives outside
// it the stored path is one read_file would deny — the note promising it must
// not be attached. The bytes are still written (durability for unrestricted
// readers — a later session, doctor tooling — is not the model's to lose), and
// the image still rides the turn inline.
func TestProcessInput_RestrictedSandbox_UnreadableStateDir_OmitsAttachmentNote(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newSandboxedImagePersistenceSession(t, stateDir, sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	got, err := os.ReadFile(wantPath)
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("attachment bytes must still be persisted for unrestricted readers: %v", err)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Error("image must still ride the turn inline")
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("restricted sandbox: the state dir %q is outside the file-tool read roots, so no read_file path may be announced: %+v", stateDir, turn.Message.Content)
	}
}

// TestProcessInput_RestrictedSandbox_ExtraReadRootStateDir_AnnouncesAttachmentNote
// pins the complementary case: when the session's policy grants the file
// tools a read of the state dir (ExtraReadRoots), the announcement contract
// holds exactly as it does unconfined.
func TestProcessInput_RestrictedSandbox_ExtraReadRootStateDir_AnnouncesAttachmentNote(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	// The roots must carry the canonical spelling too: the announced path is
	// built from the resolved state dir, and a raw root is a different name
	// for the same directory on symlinked-temp hosts.
	sess := newSandboxedImagePersistenceSession(t, stateDir, sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, ExtraReadRoots: []string{resolvedPath(t, stateDir)}}, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	note, ok := findSystemNotificationPart(turn.Message)
	if !ok || !strings.Contains(note, wantPath) {
		t.Errorf("with the state dir inside ExtraReadRoots the stored path must be announced: note=%q want path %q", note, wantPath)
	}
}

// TestProcessInput_UnwritableAttachmentsDir_DegradesWithoutAnnotation pins
// the failure mode: when the attachments directory cannot be created, the
// turn still succeeds with the image inline, no path is announced, and a
// warning reports the write failure.
func TestProcessInput_UnwritableAttachmentsDir_DegradesWithoutAnnotation(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	// The session records the canonical state dir, so the warning naming the
	// blocked attachments path uses the resolved spelling; on hosts whose
	// temp roots sit behind symlinks the raw and canonical spellings differ
	// and a raw wantDir never matches (the resolvedPath rationale).
	canonical := resolvedPath(t, stateDir)
	// Block the attachments directory path with a regular file so MkdirAll
	// fails at turn-build time.
	blocking := filepath.Join(canonical, "sessions", sess.ID(), "attachments")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	wantDir := blocking
	warnCh := collectWarnings(sess)

	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}
	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput with unwritable attachments dir: %v", err)
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Fatalf("user turn lost its ContentImage part: %+v", turn.Message.Content)
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("turn announced a stored path despite the failed write: %+v", turn.Message.Content)
	}
	// The failed write is visible: a warning naming the blocked attachments
	// directory was emitted while the turn ran.
	awaitWarningNaming(t, warnCh, wantDir)
}

// TestProcessInput_BlockedAttachmentFile_DegradesWithoutAnnotation pins the
// per-file failure branch: when the content-addressed target path cannot be
// written, that one attachment stays unannounced (and unpersisted) while the
// turn still succeeds, and the warning names the attachment.
func TestProcessInput_BlockedAttachmentFile_DegradesWithoutAnnotation(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	// Block the content-addressed target with a directory of the same name,
	// so the batch mkdir succeeds but this file's WriteFile fails.
	if err := os.MkdirAll(expectedAttachmentPath(t, stateDir, sess.ID(), img), 0o700); err != nil {
		t.Fatalf("block attachment path: %v", err)
	}
	warnCh := collectWarnings(sess)

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput with blocked attachment file: %v", err)
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Fatalf("user turn lost its ContentImage part: %+v", turn.Message.Content)
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("turn announced a stored path despite the failed write: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "shot.png")
}

// TestEnqueueWithImages_DrainPersistsAttachment pins the queue path: an
// image enqueued during an active turn persists when the queue drains as
// the next user turn.
func TestEnqueueWithImages_DrainPersistsAttachment(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("first reply"), replyStep("second reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "queued.png"}

	if err := sess.EnqueueWithImages(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; 30s only fires on
	// a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first input", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("queued attachment not persisted at %s: %v", wantPath, err)
	}
	// The drained queue entry is the second user turn in history.
	userTurns := historyTurnsWhere(t, sess, func(turn schema.Turn) bool { return turn.Kind == schema.TurnUserInput })
	if len(userTurns) != 2 {
		t.Fatalf("user turns: got %d, want 2", len(userTurns))
	}
	drained := userTurns[1]
	note, ok := findSystemNotificationPart(drained.Message)
	if !ok || !strings.Contains(note, wantPath) {
		t.Errorf("drained turn's system-notification part missing or wrong path: %q", note)
	}
}

// TestConsumeSteeringMessage_PersistsAttachment pins the steering path: a
// steering message carrying images persists its bytes and announces the
// path on the steering turn, via the exact consumption step the production
// drain loop uses.
func TestConsumeSteeringMessage_PersistsAttachment(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir)
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "steer.png"}

	if got := sess.consumeSteeringMessage(steeringMessage{Text: "mid-turn steer", Images: []ImageAttachment{img}}); got != steeringDelivered {
		t.Fatalf("consumeSteeringMessage = %v, want steeringDelivered", got)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("steering attachment not persisted at %s: %v", wantPath, err)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnSteering)
	if !hasImagePart(turn.Message) {
		t.Fatalf("steering turn lost its ContentImage part: %+v", turn.Message.Content)
	}
	note, ok := findSystemNotificationPart(turn.Message)
	if !ok || !strings.Contains(note, wantPath) {
		t.Errorf("steering turn's system-notification part missing or wrong path: %q", note)
	}
}

// TestFailedClientMutationStart_PersistsAttachmentOnRecordedTurn pins the
// failure-recording hook (recordClientMutationFailure): when a
// client-mutation turn start fails before append, the failed start's user
// turn is rebuilt from the journal's path-less attachments, and that
// recorded turn still names the persisted path. The file itself would
// exist even without this hook — the acceptUserInput hook writes it before
// the failure fires — so the annotation on the recorded turn is what pins
// the hook.
func TestFailedClientMutationStart_PersistsAttachmentOnRecordedTurn(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newQueuePersistTestSession(t, stateDir)
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "failed.png"}
	params := appwire.TurnStartParams{
		ClientMutationID: "start-failure-persist",
		Input: []appwire.InputItem{
			{Type: "text", Text: "describe this"},
			{Type: "image", MediaType: "image/png", Data: png, Name: "failed.png"},
		},
	}
	if _, err := sess.AcceptClientMutationStart(params); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	failure := errors.New("deterministic pre-append failure")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); !errors.Is(err, failure) {
		t.Fatalf("acceptUserInput error = %v, want the injected failure", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("failed start's attachment not persisted at %s: %v", wantPath, err)
	}
	recordedTurns := historyTurnsWhere(t, sess, func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnUserInput && turn.ClientMutationID == params.ClientMutationID
	})
	if len(recordedTurns) == 0 {
		t.Fatal("no recorded user turn for the failed start")
	}
	recorded := recordedTurns[len(recordedTurns)-1]
	note, ok := findSystemNotificationPart(recorded.Message)
	if !ok || !strings.Contains(note, wantPath) {
		t.Errorf("recorded failed-start turn's system-notification part missing or wrong path: %q", note)
	}
}

// TestSanitizeAttachmentName pins the filename half of the path contract:
// the stored name is safe for the filesystem, keeps its original stem when
// one exists, and always ends with the canonical extension for the media
// type.
func TestSanitizeAttachmentName(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 300)
	cases := []struct {
		name  string
		media string
		want  string
	}{
		{name: "shot.png", media: "image/png", want: "shot.png"},
		{name: "", media: "image/png", want: "image.png"},
		{name: "photo.jpeg", media: "image/jpeg", want: "photo.jpg"},
		{name: "noext", media: "image/png", want: "noext.png"},
		{name: "shot.png", media: "image/jpeg", want: "shot.jpg"},
		{name: "../../etc/passwd", media: "image/png", want: "passwd.png"},
		{name: "my shot.png", media: "image/png", want: "my_shot.png"},
		{name: ".png", media: "image/png", want: "image.png"},
		{name: long + ".png", media: "image/png", want: strings.Repeat("a", 64) + ".png"},
		{name: "anim.gif", media: "image/gif", want: "anim.gif"},
		{name: "pic.webp", media: "image/webp", want: "pic.webp"},
		{name: "bitmap.bmp", media: "image/bmp", want: "bitmap.bmp"},
		{name: "scan.tiff", media: "image/tiff", want: "scan.tif"},
		{name: "scan.tiff", media: "image/tif", want: "scan.tif"},
		{name: "mystery.bin", media: "", want: "mystery.png"},
	}
	for _, tc := range cases {
		img := ImageAttachment{Name: tc.name, MediaType: tc.media}
		if got := sanitizeAttachmentName(img); got != tc.want {
			t.Errorf("sanitizeAttachmentName(%q, %q) = %q, want %q", tc.name, tc.media, got, tc.want)
		}
	}
}

// TestProcessInput_SymlinkedAttachmentPath_NotFollowed pins the write half of
// the storage contract: a symlink planted at the content-addressed leaf must
// not be followed — the canary it points at survives untouched, no path is
// announced, and the failure is reported as a warning like every other
// attachment write failure.
func TestProcessInput_SymlinkedAttachmentPath_NotFollowed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	canary := filepath.Join(t.TempDir(), "canary.txt")
	if err := os.WriteFile(canary, []byte("canary"), 0o600); err != nil {
		t.Fatalf("write canary: %v", err)
	}
	if err := os.Symlink(canary, wantPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	warnCh := collectWarnings(sess)

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got, err := os.ReadFile(canary); err != nil || string(got) != "canary" {
		t.Fatalf("canary was followed or disturbed: content=%q err=%v", got, err)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Error("image must still ride the turn inline")
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a refused (symlinked) attachment path must not be announced: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "shot.png")
}

// TestProcessInput_PlantedAttachmentFile_NotReplaced pins the
// existing-entry half: the content-addressed name already holds DIFFERENT
// bytes (a planted file), so the write must refuse rather than silently
// replace them, and no path is announced.
func TestProcessInput_PlantedAttachmentFile_NotReplaced(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(wantPath, []byte("planted"), 0o644); err != nil {
		t.Fatalf("plant file: %v", err)
	}
	warnCh := collectWarnings(sess)

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got, err := os.ReadFile(wantPath); err != nil || string(got) != "planted" {
		t.Fatalf("planted file was replaced: content=%q err=%v", got, err)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a planted file's path must not be announced as the attachment: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "shot.png")
}

// TestProcessInput_TightensPreexistingAttachmentsDirMode pins the mode half
// of the storage contract: MkdirAll's 0700 applies only at creation, so an
// attachments directory that already exists with a wider mode (a buggy
// predecessor, a restore) is tightened when the session writes into it —
// the attachments are private to the session's user.
func TestProcessInput_TightensPreexistingAttachmentsDirMode(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	dir := filepath.Join(stateDir, "sessions", sess.ID(), "attachments")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("MkdirAll pre-existing attachments dir: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}
	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if info, err := os.Stat(dir); err != nil {
		t.Fatalf("stat attachments dir: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("pre-existing attachments dir mode = %o, want 700 after a write into it", perm)
	}
}

// TestLiveActivitySessionLabelExcludesMachineryNote pins the label surface
// of the announcement contract: the live-activity label derives its prompt
// from the first user turn, which for an image paste carries the machinery
// note naming the stored path — the label must carry the user's prose, not
// the note, the same rule reloaded bubbles, fork prefill, and metadata
// already follow.
func TestLiveActivitySessionLabelExcludesMachineryNote(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}
	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	label := liveActivitySessionLabel(sess)
	if strings.Contains(label, llm.SystemNotificationOpenTag) || strings.Contains(label, "read_file") {
		t.Errorf("activity label leaks the machinery note: %q", label)
	}
	if !strings.Contains(label, "look at this") {
		t.Errorf("activity label must carry the user's own prose: %q", label)
	}
}

// TestProcessInput_WithoutReadFileTool_OmitsAttachmentNote pins the tool
// half of the announcement contract: the note promises read_file, so a
// session whose registry does not carry that tool (role toolsets are
// plugin-configurable) must not hear the promise. The bytes are still
// persisted for later readers.
func TestProcessInput_WithoutReadFileTool_OmitsAttachmentNote(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	sess.reg.Remove("read_file")
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("attachment bytes must still be persisted: %v", err)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Error("image must still ride the turn inline")
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a session without the read_file tool must not be promised a read_file path: %+v", turn.Message.Content)
	}
}

// TestExtractOriginalPromptExcludesMachineryNote pins the metadata half of
// the note-handling contract: OriginalPrompt feeds session titles and search,
// so the first user input's extraction must be the user's prose, never the
// raw machinery block recorded alongside the image.
func TestExtractOriginalPromptExcludesMachineryNote(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	if _, err := sess.ProcessInput(context.Background(), "look at this picture", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got, want := sess.extractOriginalPrompt(), "look at this picture"; got != want {
		t.Fatalf("extractOriginalPrompt()=%q, want %q (the machinery note must not reach session metadata)", got, want)
	}
}

// TestProcessInput_SameBytesSymlink_NotAnnounced pins the dedupe half of the
// refusal: a symlink at the content-addressed leaf whose target holds the
// SAME bytes must not satisfy the dedupe compare — os.ReadFile follows the
// link, so without an lstat gate the symlinked path is announced as if the
// attachment were stored there, contradicting the refuse-symlinks guarantee.
func TestProcessInput_SameBytesSymlink_NotAnnounced(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	target := filepath.Join(t.TempDir(), "twin.png")
	if err := os.WriteFile(target, png, 0o600); err != nil {
		t.Fatalf("write twin: %v", err)
	}
	if err := os.Symlink(target, wantPath); err != nil {
		t.Fatalf("plant same-bytes symlink: %v", err)
	}
	warnCh := collectWarnings(sess)

	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a same-bytes symlink must not satisfy the dedupe compare: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "shot.png")
}

// TestWriteAttachmentFileRemovesPartialWriteOnFailure pins the retry
// contract: a failed content write must not leave a partial file at the
// content-addressed name, where it would permanently poison dedupe for those
// bytes (EEXIST with different content refuses forever). writeAttachmentFile
// takes the content-write step as a parameter so the failure is injected
// without a process-global seam racing parallel tests.
func TestWriteAttachmentFileRemovesPartialWriteOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	png := validPNGFixture(t)

	var synced []string
	err := writeAttachmentFile(path, png, func(*os.File, []byte) error {
		return errors.New("injected write failure")
	}, func(d string) error {
		synced = append(synced, d)
		return nil
	})
	if err == nil {
		t.Fatal("writeAttachmentFile must propagate the content-write error")
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("partial file left at %q after a failed write (a retry of the same bytes must find the name free)", path)
	}
	if len(synced) != 0 {
		t.Errorf("parent dir synced after a failed write (%v); nothing was created, so there is nothing to flush", synced)
	}
}

// TestWriteAttachmentFileSyncsParentDirAfterCreate pins the durability tail:
// the directory entry that names the synced file must itself be flushed
// before writeAttachmentFile reports success, or a crash can leave the
// transcript holding a promised path whose name never landed. The dir-sync
// step is a parameter, like the content-write step, so tests observe and
// inject it without process-global seams.
func TestWriteAttachmentFileSyncsParentDirAfterCreate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	png := validPNGFixture(t)

	var synced []string
	if err := writeAttachmentFile(path, png, writeAttachmentContents, func(d string) error {
		synced = append(synced, d)
		return nil
	}); err != nil {
		t.Fatalf("writeAttachmentFile: %v", err)
	}
	// The create flushes the entry naming the file (its directory) and the
	// entry naming that directory (its parent, which held the directory's
	// own creation).
	want := []string{dir, filepath.Dir(dir)}
	if !slices.Equal(synced, want) {
		t.Errorf("dir syncs after create = %v, want %v", synced, want)
	}
}

// TestWriteAttachmentFileSyncsDirectoryChainOnDedupe pins the retry half of
// the flush: the dedupe no-op must flush the same directory chain, because a
// fresh create whose dir sync failed leaves the file in place (the
// jobs-store posture) and the next paste of the same bytes reaches this
// branch — the flush it never completed happens here instead of being
// masked by a dedupe success.
func TestWriteAttachmentFileSyncsDirectoryChainOnDedupe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	png := validPNGFixture(t)

	var synced []string
	sync := func(d string) error {
		synced = append(synced, d)
		return nil
	}
	if err := writeAttachmentFile(path, png, writeAttachmentContents, sync); err != nil {
		t.Fatalf("first writeAttachmentFile: %v", err)
	}
	synced = synced[:0]
	if err := writeAttachmentFile(path, png, writeAttachmentContents, sync); err != nil {
		t.Fatalf("dedupe writeAttachmentFile: %v", err)
	}
	want := []string{dir, filepath.Dir(dir)}
	if !slices.Equal(synced, want) {
		t.Errorf("dir syncs on dedupe = %v, want %v (the dedupe must retry the flush a failed create left incomplete)", synced, want)
	}
}

// TestProcessInput_PlantedAttachmentsDirSymlink_Refused pins the store's
// directory leaf to the same no-follow guarantee the attachment files have:
// MkdirAll's Stat follows a symlink planted at the attachments path, so
// without a gate the batch succeeds, the mode tightening reaches the link's
// target, and the bytes are written through it. The plant requires the
// state-dir owner's own hand, but the store refuses the leaf where the
// write lands, exactly as it refuses a symlink at the file's name.
func TestProcessInput_PlantedAttachmentsDirSymlink_Refused(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	wantDir := filepath.Join(stateDir, "sessions", sess.ID(), "attachments")
	if err := os.MkdirAll(filepath.Dir(wantDir), 0o700); err != nil {
		t.Fatalf("MkdirAll session dir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o777); err != nil {
		t.Fatalf("Chmod outside: %v", err)
	}
	if err := os.Symlink(outside, wantDir); err != nil {
		t.Fatalf("plant attachments-dir symlink: %v", err)
	}
	warnCh := collectWarnings(sess)

	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}
	if _, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir outside: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("attachment bytes were written through the planted symlink: %v", entries)
	}
	if info, err := os.Stat(outside); err != nil {
		t.Fatalf("Stat outside: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o777 {
		t.Errorf("the mode tightening reached the symlink target: mode = %o, want it untouched at 777", perm)
	}
	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if !hasImagePart(turn.Message) {
		t.Error("image must still ride the turn inline")
	}
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a refused attachments dir must not yield an announced path: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "attachments")
}

// TestFsyncAttachmentDirSyncsRealDir exercises the production dir-sync step
// against a real directory: open, sync, and close must succeed on the same
// filesystems the state dir lives on.
func TestFsyncAttachmentDirSyncsRealDir(t *testing.T) {
	t.Parallel()
	if err := fsyncAttachmentDir(t.TempDir()); err != nil {
		t.Fatalf("fsyncAttachmentDir: %v", err)
	}
}

// TestWriteAttachmentFileSyncFailurePropagates pins the failure half of the
// dir sync: a directory that cannot be flushed fails the write (the path
// stays unannounced), while the synced file itself remains on disk for later
// readers — the same posture the jobs output store takes.
func TestWriteAttachmentFileSyncFailurePropagates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	png := validPNGFixture(t)

	err := writeAttachmentFile(path, png, writeAttachmentContents, func(string) error {
		return errors.New("injected dir-sync failure")
	})
	if err == nil {
		t.Fatal("writeAttachmentFile must propagate the dir-sync error")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the synced attachment must remain after a dir-sync failure: %v", statErr)
	}
}

// TestWriteAttachmentFileToleratesUnsupportedDirSync pins the tolerance the
// client-mutation store already applies: on a filesystem that cannot sync a
// directory at all, the synced file is as durable as the platform allows and
// the write still succeeds.
func TestWriteAttachmentFileToleratesUnsupportedDirSync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	png := validPNGFixture(t)

	err := writeAttachmentFile(path, png, writeAttachmentContents, func(string) error {
		return fmt.Errorf("sync attachments dir: %w", syscall.ENOSYS)
	})
	if err != nil {
		t.Fatalf("an unsupported dir sync must be tolerated: %v", err)
	}
}
