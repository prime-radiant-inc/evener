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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// newImagePersistenceSession stands up a session whose model replies are
// scripted by the given steps, with StateDir set to stateDir ("" disables
// state persistence, mirroring the library/test shape).
func newImagePersistenceSession(t *testing.T, stateDir string, steps ...func(req llm.Request) llm.Response) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: steps})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: stateDir})
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

// expectedAttachmentPath derives the contract path for an attachment: a
// sha256 prefix of the bytes, then the sanitized original name, under
// <stateDir>/sessions/<sessionID>/attachments/.
func expectedAttachmentPath(t *testing.T, stateDir, sessionID string, img ImageAttachment) string {
	t.Helper()
	sum := sha256.Sum256(img.Data)
	return filepath.Join(stateDir, "sessions", sessionID, "attachments", hex.EncodeToString(sum[:])[:16]+"-"+sanitizeAttachmentName(img))
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

// lastTurnOfKind returns the most recent history turn of the given kind.
func lastTurnOfKind(t *testing.T, sess *Session, kind schema.TurnKind) schema.Turn {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i := len(sess.history) - 1; i >= 0; i-- {
		if sess.history[i].Kind == kind {
			return sess.history[i]
		}
	}
	t.Fatalf("no %s turn in history (len=%d)", kind, len(sess.history))
	return schema.Turn{}
}

// hasImagePart reports whether the message carries any ContentImage part.
func hasImagePart(msg llm.Message) bool {
	for _, p := range msg.Content {
		if p.Kind == llm.ContentImage {
			return true
		}
	}
	return false
}

func replyStep(message string) func(req llm.Request) llm.Response {
	return func(req llm.Request) llm.Response { return finalResponse(message) }
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

// TestProcessInput_UnwritableAttachmentsDir_DegradesWithoutAnnotation pins
// the failure mode: when the attachments directory cannot be created, the
// turn still succeeds with the image inline, no path is announced, and a
// diagnostic warning reports the write failure.
func TestProcessInput_UnwritableAttachmentsDir_DegradesWithoutAnnotation(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	// Block the attachments directory path with a regular file so MkdirAll
	// fails at turn-build time.
	blocking := filepath.Join(stateDir, "sessions", sess.ID(), "attachments")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	// Collect warning events continuously while the turn runs; the receive
	// below is the awaitable completion, so no polling is needed.
	warnCh := make(chan events.SessionEvent, 16)
	go func() {
		for event := range sess.Events() {
			if event.Kind == events.EventWarning {
				warnCh <- event
			}
		}
	}()

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
	// The failed write is visible: a warning naming the attachment was
	// emitted while the turn ran. The 30s bound is a tripwire for a genuine
	// hang, not the mechanism — the channel receive is the await.
	select {
	case event := <-warnCh:
		data, ok := event.Data.(events.WarningData)
		if !ok {
			t.Fatalf("EventWarning data is %T, want events.WarningData", event.Data)
		}
		wantDir := filepath.Join(stateDir, "sessions", sess.ID(), "attachments")
		if !strings.Contains(data.Message, wantDir) {
			t.Errorf("warning does not name the blocked attachments dir %q: %q", wantDir, data.Message)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no diagnostic warning was emitted for the failed attachment write")
	}
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

	warnCh := make(chan events.SessionEvent, 16)
	go func() {
		for event := range sess.Events() {
			if event.Kind == events.EventWarning {
				warnCh <- event
			}
		}
	}()

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
	select {
	case event := <-warnCh:
		data, ok := event.Data.(events.WarningData)
		if !ok {
			t.Fatalf("EventWarning data is %T, want events.WarningData", event.Data)
		}
		if !strings.Contains(data.Message, "shot.png") {
			t.Errorf("warning does not name the failed attachment: %q", data.Message)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no diagnostic warning was emitted for the failed attachment write")
	}
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
	sess.mu.Lock()
	var userTurns []schema.Turn
	for _, tr := range sess.history {
		if tr.Kind == schema.TurnUserInput {
			userTurns = append(userTurns, tr)
		}
	}
	sess.mu.Unlock()
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
	sess.mu.Lock()
	var recorded *schema.Turn
	for i := range sess.history {
		turn := sess.history[i]
		if turn.Kind == schema.TurnUserInput && turn.ClientMutationID == params.ClientMutationID {
			recorded = &sess.history[i]
		}
	}
	sess.mu.Unlock()
	if recorded == nil {
		t.Fatal("no recorded user turn for the failed start")
	}
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
		{name: "mystery.bin", media: "", want: "mystery.png"},
	}
	for _, tc := range cases {
		img := ImageAttachment{Name: tc.name, MediaType: tc.media}
		if got := sanitizeAttachmentName(img); got != tc.want {
			t.Errorf("sanitizeAttachmentName(%q, %q) = %q, want %q", tc.name, tc.media, got, tc.want)
		}
	}
}
