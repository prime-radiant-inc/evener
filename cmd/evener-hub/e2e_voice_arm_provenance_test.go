package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only the persisted legacy input and external model are fixtures. Resume,
// provenance reconstruction, HTTP admission and typed execution are real.
func TestE2E_VoiceArmRejectsRestoredLegacyProvenance(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: real hub and restored manager daemon")
	}
	home, work := t.TempDir(), t.TempDir()
	project, err := identifier.ResolveProject(work)
	if err != nil {
		t.Fatal(err)
	}
	id := identifier.MustNewSessionID()
	state := filepath.Join(home, "state", "evener", "projects", project.ID)
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{
		ID: id, ProfileID: "fake", Model: fakellm.ModelID, Manager: true,
		Config:  schema.ConfigSnapshot{Manager: true},
		EnvInfo: schema.EnvironmentInfo{WorkingDir: work}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", id+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: id, CreatedAt: now, ProfileID: "fake", Model: fakellm.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	const legacy = "LEGACY-VOICE-INPUT-173"
	if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User(legacy))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := newFakeLLMProvider(t)
	bin := liveStackBinaries(t, repoRootForTestMain())
	stack := startHubStackWithOptions(t, fmt.Sprintf(`
default = "fake"
[providers.fake]
base = "openai-compatible"
base_url = %q
api_key = "fakellm-not-a-secret"
`, provider.BaseURL()), "fake/"+fakellm.ModelID, filepath.Join(bin, "evener"), hubStackOptions{ChildEnv: append(os.Environ(), "HOME="+home)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	ref := "local:" + id
	if _, err := clientRequest[appwire.ThreadResumeResponse](ctx, client, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	e := read.Thread.Evener
	if !e.Manager || !e.MutationStateAuthoritative || !e.Capabilities.VoicePresentationContextV1 || e.InstanceID == "" || e.ConversationProvenance == nil || e.ConversationProvenance.Authoritative || e.ConversationProvenance.InstanceID != e.InstanceID {
		t.Fatalf("legacy restore did not reach the usable-manager/non-authoritative-provenance boundary: %+v", e)
	}
	t.Logf("real restored manager=%s mutationAuthority=true voiceCapability=true provenanceAuthority=false", e.InstanceID)
	journal, err := hubcore.NewVoiceJournal(filepath.Join(home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	// A grant issued by the older admission path must not be retired by a
	// refused replacement. This seed does not supply session/read authority.
	prior := hubcore.VoiceBinding{ID: "legacy-prior-binding", Epoch: "legacy-prior-epoch", Ref: ref, InstanceID: e.InstanceID, DeviceID: "prior-device", GrantHash: "prior-hash", Active: true, ExpiresAt: now.Add(time.Hour), Provider: "fake", Model: "fixture", Voice: "marin"}
	if err := journal.CreateBinding(ctx, prior, false); err != nil {
		t.Fatal(err)
	}
	status, raw := voiceTestPost(ctx, t, stack, "/api/voice/arm", stack.token, map[string]any{
		"ref": ref, "expectedInstanceId": e.InstanceID, "deviceId": "replacement-device", "provider": "fake", "takeover": true,
	})
	var grant voiceArmResponse
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	preserved, err := journal.Binding(ctx, prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict || grant.BindingID != "" || grant.Grant != "" || !preserved.Active {
		t.Fatalf("non-authoritative Arm issued unusable grant or changed ownership: HTTP=%d bindingIssued=%t grantIssued=%t priorActive=%t", status, grant.BindingID != "", grant.Grant != "", preserved.Active)
	}
	const typed = "LEGACY-TYPED-AFTER-REFUSAL-179"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: ref, ExpectedInstanceID: e.InstanceID, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: typed}}}); err != nil {
		t.Fatal(err)
	}
	request, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	if !request.Contains(legacy) || !request.Contains(typed) {
		t.Fatal("refused voice Arm lost legacy context or blocked typed use")
	}
	request.RespondToolCall("communicate", communicateArgs("LEGACY-TYPED-REPLY-181"))
	awaitThread(ctx, t, client, ref, "typed legacy manager settled", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	after, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(after, original) {
		t.Fatalf("legacy transcript prefix was rewritten: %v", err)
	}
	read, err = client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil || read.Thread.Evener.ConversationProvenance == nil || read.Thread.Evener.ConversationProvenance.Authoritative {
		t.Fatalf("typed use synthesized legacy provenance: %v", err)
	}
}

func TestVoiceArmProvenanceAdmission(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    *appwire.ConversationProvenanceState
		instance string
		want     bool
	}{
		{name: "missing", instance: "manager"},
		{name: "unknown-version", state: &appwire.ConversationProvenanceState{Version: 2, InstanceID: "manager", Authoritative: true}, instance: "manager"},
		{name: "legacy", state: &appwire.ConversationProvenanceState{Version: 1, InstanceID: "manager"}, instance: "manager"},
		{name: "wrong-instance", state: &appwire.ConversationProvenanceState{Version: 1, InstanceID: "other", Authoritative: true}, instance: "manager"},
		{name: "empty-instance", state: &appwire.ConversationProvenanceState{Version: 1, Authoritative: true}},
		{name: "authoritative-without-history", state: &appwire.ConversationProvenanceState{Version: 1, InstanceID: "manager", Authoritative: true}, instance: "manager", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validVoiceProvenance(test.state, test.instance); got != test.want {
				t.Fatalf("provenance admission=%t, want %t", got, test.want)
			}
		})
	}
}

func TestE2E_VoiceArmRejectedTakeoverPreservesHealthyBinding(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	status, raw := voiceTestPost(f.ctx, t, f.stack, "/api/voice/arm", f.stack.token, map[string]any{
		"ref": f.ref, "expectedInstanceId": "wrong-instance", "deviceId": "rejected-device", "provider": "voice-fixture", "takeover": true,
	})
	var replacement voiceArmResponse
	if err := json.Unmarshal(raw, &replacement); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict || replacement.Grant != "" || replacement.BindingID != "" {
		t.Fatal("stale takeover issued a binding or grant")
	}
	binding, err := f.journal.Binding(f.ctx, f.armed.BindingID)
	if err != nil || !binding.Active || binding.InstanceID != f.instance || binding.Epoch != f.armed.Epoch {
		t.Fatal("rejected takeover changed the healthy binding")
	}
	f.device.send(`{"type":"renew"}`)
	voiceJourneyEvent(t, f.device, "renewed")
}
