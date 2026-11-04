package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A bridge is owned by one authenticated native connection. Its grant cannot
// dispatch general AppWire requests or choose another session.
type voiceBridge struct {
	service *voiceService
	journal *hubcore.VoiceJournal
	binding hubcore.VoiceBinding
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

func (v *voiceService) socket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		voiceHTTPError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	journal, err := v.store()
	if err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "voice_storage_unavailable")
		return
	}
	binding, err := journal.Binding(r.Context(), r.Header.Get("X-Evener-Voice-Binding"))
	grant := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	digest := sha256.Sum256([]byte(grant))
	if err != nil || !binding.Active || !binding.ExpiresAt.After(time.Now()) || binding.Epoch != r.Header.Get("X-Evener-Voice-Epoch") || binding.DeviceID != r.Header.Get("X-Evener-Voice-Device") || subtle.ConstantTimeCompare([]byte(binding.GrantHash), []byte(hex.EncodeToString(digest[:]))) != 1 {
		voiceHTTPError(w, http.StatusUnauthorized, "invalid_voice_grant")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(v.web.appRPC.Lifetime())
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	bridge := &voiceBridge{service: v, journal: journal, binding: binding, conn: conn, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	defer close(bridge.done)
	defer cancel()
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(128 << 10)
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	if v.sockets == nil {
		v.sockets = make(map[string]*voiceBridge)
	}
	if previous := v.sockets[binding.Ref]; previous != nil {
		previous.cancel()
	}
	v.sockets[binding.Ref] = bridge
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		if v.sockets[binding.Ref] == bridge {
			delete(v.sockets, binding.Ref)
		}
		v.mu.Unlock()
	}()
	bridge.run()
}

func (b *voiceBridge) send(message any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(b.ctx, time.Second)
	defer cancel()
	return b.conn.Write(ctx, websocket.MessageText, raw)
}

func (b *voiceBridge) run() {
	if b.send(map[string]any{"type": "ready", "bindingId": b.binding.ID, "epoch": b.binding.Epoch, "generation": 1, "expiresAt": b.binding.ExpiresAt, "policy": currentVoicePolicy()}) != nil {
		return
	}
	// The quiet control connection carries no microphone data to a provider.
	for {
		if _, _, err := b.conn.Read(b.ctx); err != nil {
			return
		}
		if b.send(map[string]any{"type": "error", "code": "unsupported_control", "recoverable": true}) != nil {
			return
		}
	}
}
