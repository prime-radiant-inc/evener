package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestE2E_VoiceArmBindsExistingManager(t *testing.T) {
	stack, provider := startE2EHubStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	ref := startManagerThread(ctx, t, client, stack, "VOICE-ARM-OPENING")
	request, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	request.RespondToolCall("communicate", communicateArgs("VOICE-ARM-READY"))
	awaitThread(ctx, t, client, ref, "manager settled", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})
	initial, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	thread := initial.Thread
	body := map[string]any{
		"ref": ref, "expectedInstanceId": thread.Evener.InstanceID,
		"deviceId": "installation-one", "provider": "fake",
	}
	status, _ := voiceTestPost(t, ctx, stack, "/api/voice/arm", "", body)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Arm status = %d", status)
	}
	body["expectedInstanceId"] = "replaced-instance"
	status, _ = voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, body)
	if status != http.StatusConflict {
		t.Fatalf("Arm against replaced instance status = %d, want conflict", status)
	}
	body["expectedInstanceId"] = thread.Evener.InstanceID
	status, raw := voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, body)
	if status != http.StatusOK {
		t.Fatalf("Arm status = %d", status)
	}
	var armed struct {
		BindingID  string    `json:"bindingId"`
		Epoch      string    `json:"epoch"`
		Grant      string    `json:"grant"`
		SocketPath string    `json:"socketPath"`
		ExpiresAt  time.Time `json:"expiresAt"`
		SampleRate int       `json:"sampleRate"`
	}
	if err := json.Unmarshal(raw, &armed); err != nil {
		t.Fatal(err)
	}
	if armed.BindingID == "" || armed.Epoch == "" || len(armed.Grant) < 32 || armed.Grant == stack.token || !armed.ExpiresAt.After(time.Now()) || armed.SocketPath != "/api/voice/socket" || armed.SampleRate != 24000 {
		t.Fatal("Arm did not return a complete, separate voice grant")
	}
	journal, err := hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	binding, err := journal.Binding(ctx, armed.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(armed.Grant))
	if binding.Ref != ref || binding.InstanceID != thread.Evener.InstanceID || binding.DeviceID != "installation-one" || binding.Epoch != armed.Epoch || binding.GrantHash != hex.EncodeToString(digest[:]) || !binding.Active {
		t.Fatal("durable binding disagrees with selected session, owner or grant hash")
	}
	status, _ = voiceTestPost(t, ctx, stack, "/api/voice/arm", armed.Grant, body)
	if status != http.StatusUnauthorized {
		t.Fatalf("scoped grant could create another binding: %d", status)
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+armed.Grant)
	headers.Set("X-Evener-Voice-Binding", armed.BindingID)
	headers.Set("X-Evener-Voice-Epoch", armed.Epoch)
	headers.Set("X-Evener-Voice-Device", "installation-one")
	device, response, err := websocket.Dial(ctx, "ws://"+stack.addr+armed.SocketPath, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		code := 0
		if response != nil {
			code = response.StatusCode
			_ = response.Body.Close()
		}
		t.Fatalf("scoped socket rejected durable Arm: HTTP %d", code)
	}
	defer func() { _ = device.CloseNow() }()
	_, packet, err := device.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Type, BindingID, Epoch string
		Generation             uint64
	}
	if err := json.Unmarshal(packet, &ready); err != nil || ready.Type != "ready" || ready.BindingID != armed.BindingID || ready.Epoch != armed.Epoch || ready.Generation == 0 {
		t.Fatal("socket did not establish its current binding generation")
	}
	body["deviceId"] = "installation-two"
	status, _ = voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, body)
	if status != http.StatusConflict {
		t.Fatalf("second device silently took over: %d", status)
	}
	body["takeover"] = true
	status, raw = voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, body)
	if status != http.StatusOK {
		t.Fatalf("explicit takeover status = %d", status)
	}
	var replacement voiceArmResponse
	if err := json.Unmarshal(raw, &replacement); err != nil {
		t.Fatal(err)
	}
	closedCtx, closedCancel := context.WithTimeout(ctx, 2*time.Second)
	defer closedCancel()
	_, _, err = device.Read(closedCtx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("takeover left the previous device connected")
	}
	binding, err = journal.Binding(ctx, armed.BindingID)
	if err != nil || binding.Active {
		t.Fatalf("takeover did not retire old binding: active=%t err=%v", binding.Active, err)
	}
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil || read.Thread.Evener.InstanceID != thread.Evener.InstanceID || !read.Thread.Evener.Manager {
		t.Fatalf("Arm replaced the manager: %v", err)
	}
	status, _ = voiceTestPost(t, ctx, stack, "/api/voice/disarm", stack.token, map[string]string{"bindingId": replacement.BindingID, "epoch": replacement.Epoch})
	if status != http.StatusOK {
		t.Fatalf("Disarm status = %d", status)
	}
	binding, err = journal.Binding(ctx, replacement.BindingID)
	if err != nil || binding.Active {
		t.Fatal("Disarm did not persist binding retirement")
	}
}

func voiceTestPost(t *testing.T, ctx context.Context, stack hubStack, path, token string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+stack.addr+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}
