package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"primeradiant.com/evener/appwire"
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

	client       *appwire.Client
	provider     *VoiceRealtime
	turn         *hubcore.VoiceTurn
	nextFrame    int64
	inputSamples int64
	inputReady   bool
	inputQueued  bool
	work         chan voiceWorkResult
	workCancel   context.CancelFunc
	workKind     string
	workers      sync.WaitGroup

	closeProviderAfterWork bool

	reconcile      bool
	reconciling    bool
	reconciliation chan voiceWorkResult

	generation       uint64
	awaitingFence    bool
	history          appwire.SnapshotIdentity
	historyThreadID  string
	historyBoot      string
	historyEpoch     uint64
	interruptedTurns map[string]bool
	speechCursor     *appwire.ThreadItemPosition
	refresh          bool
	delivery         *hubcore.SpeechDelivery
	deliveryTurnID   string
	transcripts      map[string]string
	provenance       *appwire.ConversationProvenanceState
	retiring         *hubcore.SpeechDelivery
}

type voiceControl struct {
	Type          string           `json:"type"`
	Sequence      int64            `json:"sequence,omitempty"`
	Frame         int64            `json:"frame,omitempty"`
	PCM           string           `json:"pcm,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	DeliveryID    string           `json:"deliveryId,omitempty"`
	Generation    uint64           `json:"generation,omitempty"`
	ItemID        string           `json:"itemId,omitempty"`
	Samples       int64            `json:"samples,omitempty"`
	Complete      bool             `json:"complete,omitempty"`
	PlayedSamples map[string]int64 `json:"playedSamples,omitempty"`
}

type voiceWorkResult struct {
	provider *VoiceRealtime
	intent   hubcore.VoiceIntent
	read     appwire.ThreadReadResponse
	err      error
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
	previous := v.sockets[binding.Ref]
	if previous != nil {
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
	if previous != nil {
		// Join even if this bridge was also replaced. The chain must retire every
		// older writer before a successor reads the durable generation floor.
		<-previous.done
	}
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
	client, err := b.service.web.voiceClient(b.ctx)
	if err != nil {
		_ = b.reportError("session_unavailable")
		return
	}
	b.client = client
	b.work = make(chan voiceWorkResult)
	b.reconciliation = make(chan voiceWorkResult)
	defer b.close()
	read, err := b.client.ThreadRead(b.ctx, appwire.ThreadReadParams{Ref: b.binding.Ref, IncludeTurns: true, Subscribe: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil || !b.validSpeechRead(read) {
		_ = b.reportError("session_unavailable")
		return
	}
	b.captureSpeechBaseline(read)
	b.provenance = read.Thread.Evener.ConversationProvenance
	generation, err := b.journal.MaxSpeechGeneration(b.ctx, b.binding.ID, b.binding.Epoch)
	if err != nil || generation >= voiceMaxGeneration {
		_ = b.reportError("speech_failed")
		return
	}
	b.generation = max(1, generation)
	b.awaitingFence = true
	controls := make(chan voiceControl, 8)
	b.workers.Add(1)
	go b.readControls(controls)
	if b.send(map[string]any{"type": "ready", "bindingId": b.binding.ID, "epoch": b.binding.Epoch, "generation": b.generation, "expiresAt": b.binding.ExpiresAt, "policy": currentVoicePolicy()}) != nil {
		return
	}
	expiry := time.NewTimer(time.Until(b.binding.ExpiresAt))
	defer expiry.Stop()
	b.beginWork("recovery", func(ctx context.Context) voiceWorkResult {
		return voiceWorkResult{err: b.recoverIntents(ctx)}
	})
	for {
		if b.reconcile && !b.reconciling {
			b.beginSpeechReconciliation()
		}
		if b.retiring != nil && b.workCancel == nil && b.provider == nil {
			// A closed connection has no provider response left to interrupt.
			b.retiring = nil
		}
		if b.retiring != nil && b.workCancel == nil {
			provider, delivery := b.provider, *b.retiring
			b.beginWork("retired", func(ctx context.Context) voiceWorkResult {
				played := make(map[string]int64, len(delivery.Items))
				for _, item := range delivery.Items {
					played[item.ItemID] = item.PlayedSamples
				}
				return voiceWorkResult{err: provider.Interrupt(ctx, delivery.PresentationGeneration, played)}
			})
		} else if b.inputQueued && !b.awaitingFence && b.workCancel == nil {
			b.inputQueued = false
			provider := b.provider
			b.beginWork("input_started", func(ctx context.Context) voiceWorkResult {
				result := voiceWorkResult{}
				if provider == nil {
					provider, result.err = b.openProvider(ctx)
					result.provider = provider
				}
				if result.err == nil {
					result.err = provider.BeginInput(ctx)
				}
				return result
			})
		} else if b.refresh && !b.awaitingFence && b.workCancel == nil && b.turn == nil {
			b.refresh = false
			history, cursor := b.history, appwire.ClonePointer(b.speechCursor)
			b.beginWork("history", func(ctx context.Context) voiceWorkResult {
				read, err := b.readSpeechHistory(ctx, history, cursor)
				return voiceWorkResult{read: read, err: err}
			})
		}
		var events <-chan VoiceRealtimeEvent
		if b.provider != nil && (b.retiring == nil || b.workKind != "speech") {
			events = b.provider.Events()
		}
		select {
		case <-b.ctx.Done():
			return
		case <-expiry.C:
			_ = b.reportError("grant_expired")
			return
		case control := <-controls:
			if err := b.control(control); err != nil {
				_ = b.reportError("input_failed")
				return
			}
			if control.Type == "renew" {
				expiry.Reset(time.Until(b.binding.ExpiresAt))
			}
		case notification, ok := <-b.client.Notifications():
			if !ok {
				return
			}
			if notification.Method == appwire.NotifyEvenerThreadResync {
				var changed appwire.ThreadResyncParams
				if json.Unmarshal(notification.Params, &changed) != nil {
					return
				}
				b.applySpeechResync(changed)
			}
			if notification.Method == appwire.NotifyHistoryUpdated {
				var changed appwire.HistoryUpdatedParams
				if json.Unmarshal(notification.Params, &changed) != nil {
					return
				}
				if b.applySpeechHistoryUpdate(changed) != nil {
					_ = b.reportError("speech_failed")
					return
				}
			}
			if notification.Method == appwire.NotifyConversationProvenanceChanged {
				var changed appwire.ConversationProvenanceChangedParams
				if json.Unmarshal(notification.Params, &changed) != nil {
					return
				}
				if changed.Ref == b.binding.Ref && b.applyProvenance(changed.Provenance) != nil {
					_ = b.reportError("speech_failed")
					return
				}
			}
			if notification.Method == appwire.NotifyHistoryUpdated || notification.Method == appwire.NotifyConversationProvenanceChanged {
				b.refresh = true
			}
		case event, ok := <-events:
			if !ok || b.providerEvent(event) != nil {
				_ = b.reportError("speech_failed")
				return
			}
		case result := <-b.reconciliation:
			b.reconciling = false
			if voiceHistoryCursorStale(result.err) {
				b.reconcile = true
				continue
			}
			if result.err != nil || b.applySpeechAuthority(result.read) != nil {
				_ = b.reportError("speech_failed")
				return
			}
			// Only authority is applied here. Selecting new speech still uses the
			// ordinary work slot, so it cannot race input or provider setup.
			b.refresh = true
		case result := <-b.work:
			kind := b.workKind
			b.workCancel()
			b.workCancel, b.workKind = nil, ""
			if result.provider != nil {
				b.provider = result.provider
			}
			if kind == "input_aborted" || b.closeProviderAfterWork {
				// An aborted input's connection may still belong to setup or
				// retirement. Close it only after that owner has settled.
				if b.closeAbortedProvider() != nil {
					_ = b.reportError("input_failed")
					return
				}
				b.closeProviderAfterWork = false
			}
			if kind == "input_aborted" {
				continue
			}
			if kind == "speech" && b.retiring != nil {
				// Setup was interrupted before acknowledgement. Its connection
				// cannot safely carry the new utterance, even if setup just won.
				if b.provider != nil {
					_ = b.provider.Close()
					b.provider = nil
				}
				b.retiring = nil
				continue
			}
			if result.err != nil {
				if kind == "history" && voiceHistoryCursorStale(result.err) {
					b.refresh = true
					continue
				}
				_ = b.reportError("input_failed")
				return
			}
			switch kind {
			case "recovery":
				b.refresh = true
			case "input_started":
				b.inputReady = true
				if b.send(map[string]any{"type": kind, "sequence": b.turn.Sequence, "turnId": b.turn.ID}) != nil {
					return
				}
			case "intent":
				b.turn = nil
				b.refresh = true
				if b.send(map[string]any{"type": "intent", "turnId": result.intent.TurnID, "text": result.intent.Text, "custody": result.intent.State}) != nil {
					return
				}
			case "history":
				if b.selectSpeech(result.read) != nil {
					_ = b.reportError("speech_failed")
					return
				}
			case "retired":
				b.retiring = nil
			}
		}
	}
}

func (b *voiceBridge) reportError(code string) error {
	return b.send(map[string]any{"type": "error", "code": code, "recoverable": true})
}

func (b *voiceBridge) readControls(controls chan<- voiceControl) {
	defer b.workers.Done()
	defer b.cancel()
	for {
		kind, raw, err := b.conn.Read(b.ctx)
		if err != nil || kind != websocket.MessageText {
			return
		}
		var control voiceControl
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&control) != nil || decoder.Decode(new(any)) != io.EOF {
			return
		}
		select {
		case controls <- control:
		case <-b.ctx.Done():
			return
		default:
			// A full input queue invalidates the whole partial utterance.
			return
		}
	}
}

func (b *voiceBridge) close() {
	b.cancel()
	b.workers.Wait()
	if b.provider != nil {
		_ = b.provider.Close()
	}
	_ = b.client.Close()
	if b.turn != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), time.Second)
		defer cancel()
		_ = b.journal.AbortTurn(ctx, b.turn.ID)
	}
	if b.delivery != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), time.Second)
		defer cancel()
		// Preserve pending chunks on disconnect. A revoked binding may refuse
		// them, but retirement of its already-durable evidence must still run.
		_ = b.updateDelivery(ctx, hubcore.SpeechDeliveryUpdate{})
		_, _ = b.journal.UpdateSpeechDelivery(ctx, b.delivery.ID, hubcore.SpeechDeliveryUpdate{Epoch: b.binding.Epoch, PresentationGeneration: b.delivery.PresentationGeneration, Interrupted: !b.playbackFinished()})
	}
}

func (b *voiceBridge) beginWork(kind string, work func(context.Context) voiceWorkResult) {
	ctx, cancel := context.WithCancel(b.ctx)
	b.workCancel, b.workKind = cancel, kind
	b.workers.Go(func() {
		result := work(ctx)
		select {
		case b.work <- result:
		case <-b.ctx.Done():
			if result.provider != nil {
				_ = result.provider.Close()
			}
		}
	})
}

// A recovery read cannot occupy the provider work slot: the read may prove
// that the execution owning an unacknowledged setup was interrupted. Workers
// only read, and the run loop alone applies their results and retires speech.
func (b *voiceBridge) beginSpeechReconciliation() {
	b.reconcile, b.reconciling = false, true
	history, cursor := b.history, appwire.ClonePointer(b.speechCursor)
	b.workers.Go(func() {
		read, err := b.readSpeechHistory(b.ctx, history, cursor)
		select {
		case b.reconciliation <- voiceWorkResult{read: read, err: err}:
		case <-b.ctx.Done():
		}
	})
}

func voiceHistoryCursorStale(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	return ok && data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
}

func (b *voiceBridge) control(control voiceControl) error {
	switch control.Type {
	case "input_start":
		if b.turn != nil || control.Sequence <= 0 {
			return b.reportError("input_busy")
		}
		turn, err := b.journal.CreateTurn(b.ctx, b.binding.ID, b.binding.Epoch, control.Sequence)
		if err != nil {
			return err
		}
		if turn.State != hubcore.VoiceTurnOpen {
			return b.reportError("turn_closed")
		}
		b.turn, b.nextFrame, b.inputSamples, b.inputReady = &turn, 0, 0, false
		b.inputQueued = true
	case "audio":
		if b.turn == nil || !b.inputReady || b.turn.Sequence != control.Sequence || b.nextFrame != control.Frame {
			return hubcore.ErrVoiceConflict
		}
		pcm, err := base64.StdEncoding.DecodeString(control.PCM)
		if err != nil || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > voicePCMChunkLimit {
			return hubcore.ErrVoiceInvalid
		}
		b.inputSamples += int64(len(pcm) / 2)
		if b.inputSamples > int64(currentVoicePolicy().MaxUtteranceMS)*24 {
			return hubcore.ErrVoiceInvalid
		}
		if err := b.provider.AppendPCM(b.ctx, pcm); err != nil {
			return err
		}
		b.nextFrame++
	case "input_end":
		if b.turn == nil || !b.inputReady || b.turn.Sequence != control.Sequence || b.workCancel != nil {
			return hubcore.ErrVoiceConflict
		}
		b.inputReady = false
		turn, provider := *b.turn, b.provider
		b.beginWork("intent", func(ctx context.Context) voiceWorkResult {
			intent, err := b.finishInput(ctx, turn, provider)
			return voiceWorkResult{intent: intent, err: err}
		})
	case "input_abort":
		if b.turn != nil && b.turn.Sequence == control.Sequence {
			// input_end transfers ownership to durable intent preparation.
			// A delayed capture abort must not cancel that work.
			if b.workKind == "intent" {
				return nil
			}
			if err := b.journal.AbortTurn(b.ctx, b.turn.ID); err != nil {
				return err
			}
			if b.workKind == "input_started" {
				b.workCancel()
				b.workKind = "input_aborted"
			}
			if b.provider != nil && (b.inputReady || b.workKind == "input_aborted") {
				if b.workCancel == nil || b.workKind == "input_aborted" {
					if err := b.closeAbortedProvider(); err != nil {
						return err
					}
				} else {
					b.closeProviderAfterWork = true
				}
			}
			b.turn, b.inputReady, b.inputQueued = nil, false, false
			b.nextFrame, b.inputSamples = 0, 0
			b.refresh = true
		}
	case "played":
		return b.recordPlayed(control)
	case "interrupt":
		return b.interruptSpeech(control)
	case "renew":
		read, err := b.client.ThreadRead(b.ctx, appwire.ThreadReadParams{Ref: b.binding.Ref})
		if err != nil {
			return err
		}
		if read.Thread.Evener.InstanceID != b.binding.InstanceID || !read.Thread.Evener.MutationStateAuthoritative || !read.Thread.Evener.Manager {
			return hubcore.ErrVoiceStale
		}
		expires := time.Now().UTC().Add(voiceGrantLifetime)
		if err := b.journal.RenewBinding(b.ctx, b.binding.ID, b.binding.Epoch, expires); err != nil {
			return err
		}
		b.binding.ExpiresAt = expires
		return b.send(map[string]any{"type": "renewed", "expiresAt": expires})
	case "disarm":
		if err := b.journal.RetireBinding(b.ctx, b.binding.ID, b.binding.Epoch); err != nil {
			return err
		}
		b.cancel()
	default:
		return b.reportError("unsupported_control")
	}
	return nil
}

// Closing an aborted input also ends speech owned by that provider. Unlike
// retireSpeech, it must not queue an Interrupt against the closed connection.
func (b *voiceBridge) closeAbortedProvider() error {
	if b.delivery != nil && !b.playbackFinished() {
		if err := b.updateDelivery(b.ctx, hubcore.SpeechDeliveryUpdate{Interrupted: true}); err != nil {
			return err
		}
		if err := b.advanceGeneration(); err != nil {
			return err
		}
		if err := b.send(map[string]any{"type": "stop", "generation": b.generation}); err != nil {
			return err
		}
	}
	b.delivery, b.retiring = nil, nil
	b.deliveryTurnID, b.transcripts = "", nil
	if b.provider != nil {
		_ = b.provider.Close()
		b.provider = nil
	}
	return nil
}

func (b *voiceBridge) openProvider(ctx context.Context) (*VoiceRealtime, error) {
	endpoint, headers, err := b.service.web.voiceProvider(ctx, b.binding.Provider)
	if err != nil {
		return nil, err
	}
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	return OpenVoiceRealtime(ctx, b.ctx, VoiceRealtimeConfig{Endpoint: endpoint, Header: headers, Model: b.binding.Model, Voice: b.binding.Voice})
}

func (b *voiceBridge) finishInput(ctx context.Context, turn hubcore.VoiceTurn, provider *VoiceRealtime) (hubcore.VoiceIntent, error) {
	item, err := provider.CommitInput(ctx)
	if err != nil {
		return hubcore.VoiceIntent{}, err
	}
	if _, err := b.journal.CommitTurnInput(ctx, turn.ID, item); err != nil {
		return hubcore.VoiceIntent{}, err
	}
	interpretation, err := provider.Interpret(ctx, turn.ID, item)
	if err != nil {
		return hubcore.VoiceIntent{}, err
	}
	read, err := b.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref})
	if err != nil {
		return hubcore.VoiceIntent{}, err
	}
	if read.Thread.Evener.InstanceID != b.binding.InstanceID || !read.Thread.Evener.MutationStateAuthoritative || !read.Thread.Evener.Manager || !read.Thread.Evener.Capabilities.VoicePresentationContextV1 {
		return hubcore.VoiceIntent{}, hubcore.ErrVoiceStale
	}
	method := appwire.MethodTurnStart
	if read.Thread.Evener.ActiveTurnID != "" {
		method = appwire.MethodTurnSteer
	}
	intent, err := b.journal.PrepareIntent(ctx, turn.ID, method, interpretation.Text, uuid.NewString())
	if err != nil {
		return intent, err
	}
	return b.submitIntent(ctx, intent, false)
}

func (b *voiceBridge) submitIntent(ctx context.Context, intent hubcore.VoiceIntent, reconciled bool) (hubcore.VoiceIntent, error) {
	intent, err := b.journal.RecordAttempt(ctx, intent.TurnID, b.binding.InstanceID, reconciled)
	if err != nil {
		return intent, err
	}
	var response struct {
		Receipt appwire.MutationReceipt `json:"receipt"`
	}
	if err := b.client.Request(ctx, intent.Method, intent.Params, &response); err != nil {
		if wire, ok := errors.AsType[appwire.WireError](err); ok {
			if data, ok := appwire.DecodedErrorData(wire.Data); ok && data.Cause == appwire.JournaledRejectionCause {
				rejectionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				defer cancel()
				return b.journal.RecordRejection(rejectionCtx, intent.TurnID, wire)
			}
		}
		return intent, err
	}
	// A receipt remains durable evidence even if the phone disconnected in flight.
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return b.journal.RecordReceipt(receiptCtx, intent.TurnID, response.Receipt)
}
