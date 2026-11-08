package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestE2E_VoiceSessionStopRecoversRelayGap(t *testing.T) {
	for _, phase := range []string{"playing", "session", "response"} {
		t.Run(phase, func(t *testing.T) { voiceStopRelayGap(t, phase) })
	}
}

func TestE2E_VoiceSessionStopRecoversRelayGapWithSavedHistory(t *testing.T) {
	voiceStopRelayGapOn(t, "playing", true)
}

// A renewal read that meets a retryable outage must not be answered from saved
// history: that daemonless response fails authority validation and closes the
// bridge, interrupting unfinished speech. The hook forces the exact read
// failure the relay produces, with saved history present.
func TestE2E_VoiceSessionStopRenewSurvivesRelayGapWithSavedHistory(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	var armed, failed atomic.Bool
	gap, _ := voiceStopRecoveryHub(t, f, nil, true, func(context.Context) error {
		if armed.Load() {
			failed.Store(true)
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		}
		return nil
	})
	_ = gap
	const reply = "STOP-RENEW-LIVE-285"
	f.admit(t, "STOP-RENEW-LIVE-INPUT-285", false)
	voiceStopContinue(f.next(t), reply)
	_ = f.next(t) // The held execution stays active.
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, reply)
	voiceCorrectionAudio(peer, old, "renew-saved-response", "renew-saved-item", base64.StdEncoding.EncodeToString(make([]byte, 960)))
	voiceJourneyEvent(t, f.device, "audio")

	armed.Store(true)
	f.device.send(`{"type":"renew"}`)
	deadline := time.Now().Add(5 * time.Second)
	for !failed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !failed.Load() {
		t.Fatal("renewal read did not reach the outage hook")
	}
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if f.delivery(t, old.ID).Interrupted {
			t.Fatal("a renewal read during an outage was served saved history and interrupted speech")
		}
		time.Sleep(10 * time.Millisecond)
	}
	armed.Store(false)
	f.device.send(`{"type":"renew"}`)
	if event := f.device.read(); event["type"] != "renewed" {
		t.Fatalf("next renewal did not complete after the outage: %v", event)
	}
}

// A renewal read that loses its relay and then blocks through the hub's retry
// budget must keep the retryable outage; the budget's DeadlineExceeded would
// close the bridge and interrupt unfinished speech.
func TestE2E_VoiceSessionStopRenewSurvivesRetryBudgetExpiry(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	var armed atomic.Bool
	entered := make(chan struct{}, 1)
	gap, _ := voiceStopRecoveryHub(t, f, nil, true, func(ctx context.Context) error {
		if !armed.Load() {
			return nil
		}
		if _, budgeted := ctx.Deadline(); !budgeted {
			// The first attempt carries the bridge's unbudgeted context. It must
			// report the outage instead of blocking here: any armed read may
			// reach this hook, and blocking the run loop's own context would
			// hang the test instead of exercising the retry budget.
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	_ = gap
	const reply = "STOP-RENEW-BUDGET-286"
	f.admit(t, "STOP-RENEW-BUDGET-INPUT-286", false)
	voiceStopContinue(f.next(t), reply)
	_ = f.next(t) // The held execution stays active.
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, reply)
	voiceCorrectionAudio(peer, old, "renew-budget-response", "renew-budget-item", base64.StdEncoding.EncodeToString(make([]byte, 960)))
	voiceJourneyEvent(t, f.device, "audio")

	armed.Store(true)
	f.device.send(`{"type":"renew"}`)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("renewal retry did not reach the held reconnection")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.delivery(t, old.ID).Interrupted {
			t.Fatal("retry-budget expiry replaced the retryable outage and interrupted speech")
		}
		time.Sleep(10 * time.Millisecond)
	}
	armed.Store(false)
	f.device.send(`{"type":"renew"}`)
	if event := f.device.read(); event["type"] != "renewed" {
		t.Fatalf("next renewal did not complete after the budget expiry: %v", event)
	}
}

// A renewal read held by a reconnecting relay must not block the run loop:
// playback receipts sent every 200ms fill the eight-entry control queue, and
// readControls cancels the bridge, interrupting speech before the outage
// handler can preserve it.
func TestE2E_VoiceSessionStopRenewSurvivesReceiptBacklog(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	var armed atomic.Bool
	var renewCalls, inFlight, maxInFlight atomic.Int32
	gap, _ := voiceStopRecoveryHub(t, f, nil, true, func(context.Context) error {
		if !armed.Load() {
			return nil
		}
		active := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			peak := maxInFlight.Load()
			if active <= peak || maxInFlight.CompareAndSwap(peak, active) {
				break
			}
		}
		// Hold the first renewal like a reconnecting relay while receipts
		// arrive; a renewal coalesced behind it runs at once.
		if renewCalls.Add(1) == 1 {
			time.Sleep(3 * time.Second)
		}
		return nil
	})
	_ = gap
	const reply = "STOP-RENEW-BACKLOG-287"
	f.admit(t, "STOP-RENEW-BACKLOG-INPUT-287", false)
	voiceStopContinue(f.next(t), reply)
	_ = f.next(t) // The held execution stays active.
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, reply)
	const response, item = "renew-backlog-response", "renew-backlog-item"
	voiceCorrectionAudio(peer, old, response, item, base64.StdEncoding.EncodeToString(make([]byte, 960)))
	voiceJourneyEvent(t, f.device, "audio")

	armed.Store(true)
	f.device.send(`{"type":"renew"}`)
	const receipts = 9 // the ninth overflows the eight-entry control queue
	for i := int64(1); i <= receipts; i++ {
		if i == 5 {
			// The device resends renew on its own cadence; it must run after
			// the held read, never beside it.
			f.device.send(`{"type":"renew"}`)
		}
		f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":%d}`, old.ID, old.PresentationGeneration, item, 200+i))
		time.Sleep(200 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.delivery(t, old.ID).Interrupted {
			t.Fatal("a playback-receipt backlog cancelled the bridge during a held renewal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	armed.Store(false)
	for i := 0; i < 2; i++ {
		if event := f.device.read(); event["type"] != "renewed" {
			t.Fatalf("renewal %d did not complete after the receipt backlog: %v", i+1, event)
		}
	}
	if got := renewCalls.Load(); got != 2 {
		t.Fatalf("renewal reads = %d, want the held read and its coalesced successor", got)
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("renewal reads in flight = %d, want 1", got)
	}
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool {
		for _, received := range d.Items {
			if received.ItemID == item {
				return received.PlayedSamples == 200+receipts
			}
		}
		return false
	})
	if f.delivery(t, old.ID).Interrupted {
		t.Fatal("speech was interrupted after a held renewal")
	}
}

func voiceStopRelayGap(t *testing.T, phase string) {
	t.Helper()
	voiceStopRelayGapOn(t, phase, false)
}

func voiceStopRelayGapOn(t *testing.T, phase string, savedHistory bool) {
	t.Helper()
	f := newVoiceCorrectionStack(t)
	gap, observer := voiceStopRecoveryHub(t, f, nil, savedHistory, nil)
	const reply = "STOP-RELAY-OUTPUT-281"
	f.admit(t, "STOP-RELAY-INPUT-281", false)
	voiceStopContinue(f.next(t), reply)
	held := f.next(t)
	start := voiceJourneyEvent(t, f.device, "speech_start")
	var peer realtimePeer
	if phase == "session" {
		select {
		case peer = <-f.peers:
		case <-f.ctx.Done():
			t.Fatal("provider did not connect")
		}
		if peer.read()["type"] != "session.update" {
			t.Fatal("provider setup not reached")
		}
	} else {
		peer = f.openSpeaker(t)
		f.speechRequest(t, peer, start, reply)
	}
	old := f.delivery(t, start["deliveryId"].(string))
	const response, item = "stop-relay-response", "stop-relay-item"
	if phase == "playing" {
		voiceCorrectionAudio(peer, old, response, item, base64.StdEncoding.EncodeToString(make([]byte, 960)))
		voiceJourneyEvent(t, f.device, "audio")
		f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":240}`, old.ID, old.PresentationGeneration, item))
		f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return len(d.Items) == 1 && d.Items[0].PlayedSamples == 240 })
	}
	running := f.read(t)
	source := voiceCorrectionItem(running, reply)
	if source.TurnID == "" || source.TurnID != running.Thread.Evener.ActiveTurnID {
		t.Fatal("playing speech is not owned by held execution")
	}
	gap.drop(t)
	select {
	case <-gap.redial:
	case <-f.ctx.Done():
		t.Fatal("real relay did not try to recover its closed daemon connection")
	}
	voiceStopExecution(t, f, held, source.TurnID)
	if f.delivery(t, old.ID).Interrupted {
		t.Fatal("Stop reached voice before relay recovery, gap scenario not established")
	}
	gap.resume()
	voiceStopWaitResync(t, f, observer, running.Thread.ID)
	t.Logf("real daemon connection dropped and redial held through accepted Stop of %s, relay resync observed, provider phase=%s with no completion or acknowledgement release", source.TurnID, phase)
	if phase == "playing" {
		voiceStopFence(t, f, old)
		voiceStopProvider(t, peer, old, response, item, false)
		return
	}
	// This combined retirement/close budget is below the provider timeout.
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	limited := *f
	limited.ctx = ctx
	voiceStopFence(t, &limited, old)
	_, _, err := peer.conn.Read(ctx)
	if err == nil || ctx.Err() != nil {
		t.Fatal("recovery still waits for provider acknowledgement")
	}
}

func TestE2E_VoiceSessionStopResyncAlonePreservesSpeech(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	var hold atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	releaseRead := func() { once.Do(func() { close(release) }) }
	defer releaseRead()
	gap, observer := voiceStopRecoveryHub(t, f, func() {
		if hold.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
	}, false, nil)
	const reply = "STOP-RESYNC-ONLY-OUTPUT-283"
	f.admit(t, "STOP-RESYNC-ONLY-INPUT-283", false)
	voiceStopContinue(f.next(t), reply)
	_ = f.next(t) // The actual execution stays active throughout recovery.
	start := voiceJourneyEvent(t, f.device, "speech_start")
	var peer realtimePeer
	select {
	case peer = <-f.peers:
	case <-f.ctx.Done():
		t.Fatal("provider did not connect")
	}
	if peer.read()["type"] != "session.update" {
		t.Fatal("provider setup not reached")
	}
	running := f.read(t)
	hold.Store(true)
	gap.drop(t)
	select {
	case <-gap.redial:
	case <-f.ctx.Done():
		t.Fatal("relay never redialed")
	}
	gap.resume()
	voiceStopWaitResync(t, f, observer, running.Thread.ID)
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("resync did not start an authoritative read while provider setup was pending")
	}
	peer.ctx = ctx
	peer.send(`{"type":"session.updated","session":{"type":"realtime","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	old := f.speechRequest(t, peer, start, reply)
	voiceCorrectionAudio(peer, old, "resync-only-response", "resync-only-item", "AQACAA==")
	device := f.device
	device.ctx = ctx
	audio := voiceJourneyEvent(t, device, "audio")
	if audio["deliveryId"] != old.ID || audio["generation"] != float64(old.PresentationGeneration) || f.delivery(t, old.ID).Interrupted {
		t.Fatal("resync alone silenced speech or blocked it behind the held history read")
	}
	t.Log("provider setup and device audio progressed while the recovery read was held, without any Stop")
	releaseRead()
	voiceCorrectionDone(peer, old, "resync-only-response", "resync-only-item")
	voiceJourneyEvent(t, device, "speech_end")
	voiceJourneyEvent(t, device, "delivery_end")
}

func TestVoiceSpeechRecoveryHintsRequireAuthoritativeRead(t *testing.T) {
	for _, kind := range []string{"ref-only", "matching", "other-ref", "other-thread", "epoch", "incarnation", "boot"} {
		t.Run(kind, func(t *testing.T) {
			delivery := &hubcore.SpeechDelivery{ID: "speaking", PresentationGeneration: 7}
			b := &voiceBridge{binding: hubcore.VoiceBinding{Ref: "local:manager"}, historyThreadID: "manager", historyBoot: "boot", historyEpoch: 3, history: appwire.SnapshotIdentity{Incarnation: "history"}, delivery: delivery, deliveryTurnID: "execution", generation: 7}
			wantRead := true
			switch kind {
			case "ref-only", "matching", "other-ref", "other-thread":
				params := appwire.ThreadResyncParams{Ref: b.binding.Ref, ThreadID: b.historyThreadID}
				switch kind {
				case "ref-only":
					params.ThreadID = ""
				case "other-ref":
					params.Ref += "-other"
					wantRead = false
				case "other-thread":
					params.ThreadID += "-other"
					wantRead = false
				}
				b.applySpeechResync(params)
			default:
				params := appwire.HistoryUpdatedParams{Ref: b.binding.Ref, ThreadID: b.historyThreadID, BootGeneration: b.historyBoot, Epoch: b.historyEpoch, Snapshot: b.history, Turns: []appwire.Turn{{ID: "execution", Status: appwire.TurnStatusInterrupted}}}
				switch kind {
				case "epoch":
					params.Epoch++
				case "incarnation":
					params.Snapshot.Incarnation += "-new"
				case "boot":
					params.BootGeneration += "-new"
				}
				if err := b.applySpeechHistoryUpdate(params); err != nil {
					t.Fatal(err)
				}
			}
			if b.reconcile != wantRead || b.delivery != delivery || b.generation != 7 || b.retiring != nil || len(b.interruptedTurns) != 0 {
				t.Fatal("recovery hint changed speech authority or requested the wrong refresh")
			}
		})
	}
}

func voiceStopWaitResync(t *testing.T, f *voiceCorrectionStack, observer *appwire.Client, threadID string) {
	t.Helper()
	for {
		select {
		case notification := <-observer.Notifications():
			if notification.Method != appwire.NotifyEvenerThreadResync {
				continue
			}
			var params appwire.ThreadResyncParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Ref != f.ref || (params.ThreadID != "" && params.ThreadID != threadID) {
				t.Fatalf("recovery resync did not name bound manager: got=%+v ref=%q thread=%q", params, f.ref, threadID)
			}
			return
		case <-f.ctx.Done():
			t.Fatal("real relay did not broadcast resync")
		}
	}
}

// Only the TCP transport is faulted. Both hubs use the original live manager,
// its mutation journal and transcript, and the production local source/relay.
func voiceStopRecoveryHub(t *testing.T, f *voiceCorrectionStack, beforeRead func(), savedHistory bool, readFailure func(context.Context) error) (*voiceStopTransportGap, *appwire.Client) {
	t.Helper()
	_ = f.device.conn.CloseNow()
	root := filepath.Join(f.stack.home, "state", "evener")
	entries, err := rendezvous.List(filepath.Join(root, "run"))
	if err != nil || len(entries) != 1 || entries[0].InstanceID != f.instance {
		t.Fatalf("live manager rendezvous not found: count=%d err=%v", len(entries), err)
	}
	gap := &voiceStopTransportGap{redial: make(chan struct{}, 1)}
	transport := &http.Transport{DialContext: gap.dial}
	t.Cleanup(transport.CloseIdleConnections)
	source := appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return entries }, &http.Client{Transport: transport})
	config := filepath.Join(f.stack.home, "config", "evener")
	auth := newTestAuthController(t, t.TempDir(), t.TempDir(), filepath.Join(config, "providers.toml"))
	srv := httptest.NewUnstartedServer(nil)
	past := hubcore.NewPastIndex("")
	if savedHistory {
		past = voiceStopSavedHistory(t, f)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), AuthToken: f.stack.token, HubStateRoot: root, Registry: auth.reg, Past: past, RelayHooks: hubcore.RelayLifecycleHooks{BeforeReadThread: beforeRead, ReadThreadFailure: readFailure}})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(func() { gap.resume(); _ = web.appRPC.Shutdown(context.Background()) })
	second := f.stack
	second.addr = srv.Listener.Addr().String()
	observer := second.dialRPC(f.ctx, t)
	if _, err := observer.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	conn := voiceJourneyConnect(f.ctx, t, "ws://"+second.addr+f.armed.SocketPath, f.headers)
	t.Cleanup(func() { _ = conn.CloseNow() })
	f.device = realtimePeer{conn: conn, ctx: f.ctx, t: t}
	voiceJourneyAcknowledgeReady(t, f.device, voiceJourneyEvent(t, f.device, "ready"), 0)
	voiceJourneyEvent(t, f.device, "stop")
	return gap, observer
}

// voiceStopSavedHistory gives the second hub a daemonless snapshot of the
// manager thread, so a live read defeated by the relay gap has saved history
// to fall back to unless the voice reads require a live response.
func voiceStopSavedHistory(t *testing.T, f *voiceCorrectionStack) *hubcore.PastIndex {
	t.Helper()
	ref, err := appwire.ParseRef(f.ref)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	state := filepath.Join(root, "projects", "project-voice-0000000000")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: ref.ThreadID}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", ref.ThreadID+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: ref.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, state, ref.ThreadID)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := past.Find(ref.ThreadID); !ok {
		t.Fatal("saved-history fixture did not index the manager thread")
	}
	return past
}

type voiceStopTransportGap struct {
	mu          sync.Mutex
	connections []net.Conn
	gate        chan struct{}
	redial      chan struct{}
}

func (g *voiceStopTransportGap) dial(ctx context.Context, network, address string) (net.Conn, error) {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		select {
		case g.redial <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err == nil {
		g.mu.Lock()
		g.connections = append(g.connections, conn)
		g.mu.Unlock()
	}
	return conn, err
}

func (g *voiceStopTransportGap) drop(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.connections) == 0 {
		t.Fatal("no real daemon transport to drop")
	}
	g.gate = make(chan struct{})
	for _, conn := range g.connections {
		_ = conn.Close()
	}
	g.connections = nil
}

func (g *voiceStopTransportGap) resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
}
