package hub

// The cross-projection parity oracle: a daemon wired exactly the way
// cmd/evener/serve.go wires it answers thread/read with its own capability
// set — the one answer every projection of that session is supposed to
// agree with (#1840: the same session must not read differently from
// ListThreads and from ThreadRead). This test reads that answer and requires
// the hub's projections to match it field by field, through the one rule
// documented on assertCapabilityParity below.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// daemonCapabilitiesAtState wires a server.Server the production way
// (hubtest.WireCapabilitySeams) and returns the capability set it answers
// thread/read with at the given state.
func daemonCapabilitiesAtState(t *testing.T, state string) appwire.ThreadCapabilities {
	t.Helper()
	srv := daemonserver.NewServer(daemonserver.ServerConfig{})
	srv.SetAppIdentity("local", "th_parity_oracle")
	srv.SetState(state)
	hubtest.WireCapabilitySeams(srv)

	// The in-process connection is the same appserver request path a
	// websocket client — the roster's prober included — speaks to.
	conn := srv.AppServer().NewConnection("parity-oracle")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize: %+v", init.Error)
	}
	read := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{}))
	if read.Kind() != appwire.MessageResponse {
		t.Fatalf("thread/read: %+v", read.Error)
	}
	result, ok := read.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result type = %T, want ThreadReadResponse", read.Response.Result)
	}
	return result.Thread.Evener.Capabilities
}

// assertCapabilityParity is the oracle's one rule, applied to every
// projection. A field is either recorded in the `differ` ledger — with a
// reason, and with the projections genuinely differing, or the entry is
// stale and must go — or recorded in the `agreeOnFalse` ledger — both sides
// false, with the reason the daemon legitimately answers false — or the
// projection must equal the daemon's answer. Both ledgers are validated
// against the field set, so an entry that matches no bit (a typo, a rename,
// a removal) fails as orphaned cruft, and the daemon-false audit in the test
// body forces every false oracle bit to be accounted for somewhere.
func assertCapabilityParity(t *testing.T, projection string, daemon, got appwire.ThreadCapabilities, differ, agreeOnFalse map[string]string) {
	t.Helper()
	daemonV, gotV := reflect.ValueOf(daemon), reflect.ValueOf(got)
	typ := daemonV.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		fieldValue := daemonV.Field(i)
		if fieldValue.Kind() != reflect.Bool {
			t.Fatalf("%s: ThreadCapabilities.%s is not a bool (%v) — the parity oracle walks bool bits only; teach it the field's real shape", projection, name, fieldValue.Kind())
		}
		d, g := fieldValue.Bool(), gotV.Field(i).Bool()
		if reason, listed := differ[name]; listed {
			if d == g {
				t.Errorf("%s: %s is in the differ ledger (%s) but the projections agree (%v) — remove the stale entry", projection, name, reason, g)
			}
			continue
		}
		if reason, listed := agreeOnFalse[name]; listed {
			if d || g {
				t.Errorf("%s: %s is in the agree-on-false ledger (%s) but daemon=%v projection=%v — reclassify the entry", projection, name, reason, d, g)
			}
			continue
		}
		if d != g {
			t.Errorf("%s: %s = %v, but the daemon answers %v — update the projection, or record the exception with its reason", projection, name, g, d)
		}
	}
	for name, reason := range differ {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("%s: differ-ledger key %q (%s) matches no ThreadCapabilities field — remove the orphaned entry", projection, name, reason)
		} else if _, also := agreeOnFalse[name]; also {
			t.Errorf("%s: %s is in both ledgers; pick one", projection, name)
		}
	}
	for name, reason := range agreeOnFalse {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("%s: agree-on-false key %q (%s) matches no ThreadCapabilities field — remove the orphaned entry", projection, name, reason)
		}
	}
}

func TestCapabilityProjectionsMatchTheDaemonOracle(t *testing.T) {
	daemon := daemonCapabilitiesAtState(t, appwire.ThreadStatusIdle)

	// The cold set a session with no daemon reads through: send and queue resume
	// it, and the hub's mutation gates re-verify every action against the daemon
	// a resume spawns. Steer and Interrupt are withheld because they need a turn
	// already running and a cold session has none; fork stays advertised because
	// it is the hub's own operation. Voice requires the original daemon's explicit
	// advertisement, which neither this hub nor a future resume can supply.
	pastDiffer := map[string]string{
		"Steer":                      "no daemon is running to carry out a steer",
		"Interrupt":                  "no daemon is running to interrupt",
		"ForkFromTurn":               "the hub's own operation; applyHubForkCapability owns the bit on every path that serves it",
		"StopSubagent":               "a session with no daemon running runs no subagents to stop",
		"PageBefore":                 "the hub's own answer: it pages local and saved transcripts from a before position itself (hubPagesBefore)",
		"VoicePresentationContextV1": "no original daemon has advertised voice support; cold/resume action guarantees are not evidence of that support",
	}
	cold := pastThreadCapabilities()
	if cold.VoicePresentationContextV1 {
		t.Fatal("cold capabilities invent voice support without an original-daemon advertisement")
	}
	assertCapabilityParity(t, "pastThreadCapabilities", daemon, cold, pastDiffer, nil)

	// A probed row mirrors the probe's captured set verbatim. The fallback for
	// an unprobed row approximates action capabilities at the row's status, but
	// cannot infer voice support from the current-daemon oracle. Fork differs
	// from the daemon in no row and in no way: the daemon hardwires it false,
	// rows mirror that, and applyHubForkCapability is the single owner that turns
	// it on. These assertions pin the pre-#1375 Queue-folding regression too.
	unprobedDiffer := map[string]string{
		"VoicePresentationContextV1": "no probe has captured the original daemon's voice support; a current-daemon oracle cannot vouch for an unprobed daemon",
	}
	for _, state := range []string{appwire.ThreadStatusIdle, appwire.ThreadStatusActive, appwire.ThreadStatusClosed} {
		daemonAt := daemonCapabilitiesAtState(t, state)
		// Guard the fixture, not the projection: the mirror assertions below
		// compare against whatever the daemon answered, so a state that did
		// not take effect would leave them comparing idle against idle.
		if !daemonAt.VoicePresentationContextV1 {
			t.Fatalf("%s daemon fixture does not advertise voice support", state)
		}
		if state == appwire.ThreadStatusActive && (daemonAt.Send || !daemonAt.Queue) {
			t.Fatalf("active daemon fixture = %+v, want Send folded out by activity with Queue still advertised", daemonAt)
		}
		if state == appwire.ThreadStatusClosed && (daemonAt.Send || !daemonAt.SkillInput) {
			t.Fatalf("closed daemon fixture = %+v, want Send withheld by closed while skillInput stays advertised", daemonAt)
		}
		rows := listRowsFromLocalDaemonSource(t, func() []appsource.LocalDaemonEntry {
			return []appsource.LocalDaemonEntry{
				{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/probed", ThreadID: "th_probed_" + state, SessionID: "sess_probed_" + state},
					Status: state, Capabilities: daemonAt, CapabilitiesKnown: true},
				{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/unprobed", ThreadID: "th_unprobed_" + state, SessionID: "sess_unprobed_" + state},
					Status: state},
			}
		})
		if len(rows) != 2 {
			t.Fatalf("list rows = %+v, want the probed and unprobed %s fixture rows", rows, state)
		}
		capsBySession := map[string]appwire.ThreadCapabilities{}
		for _, thread := range rows {
			capsBySession[thread.SessionID] = thread.Evener.Capabilities
		}
		for _, projection := range []struct {
			name, session string
			probed        bool
		}{
			{name: "probed list row", session: "sess_probed_" + state, probed: true},
			{name: "unprobed list row", session: "sess_unprobed_" + state},
		} {
			caps, ok := capsBySession[projection.session]
			if !ok {
				t.Fatalf("list rows = %+v, want the %s fixture row for %s", rows, projection.name, state)
			}
			if caps.VoicePresentationContextV1 != projection.probed {
				t.Errorf("%s at %s: voice support = %v, want %v from original-daemon evidence", projection.name, state, caps.VoicePresentationContextV1, projection.probed)
			}
			var differ map[string]string
			if !projection.probed {
				differ = unprobedDiffer
			}
			assertCapabilityParity(t, projection.name+" at "+state, daemonAt, caps, differ, nil)
		}
	}

	// The oracle audit, hoisted so an unwired seam fails once instead of once
	// per projection: every bit the fixture daemon answers false must be
	// recorded in some ledger above, or the fixture has drifted from the
	// production wiring and every parity comparison below is against a lie.
	daemonV := reflect.ValueOf(daemon)
	typ := daemonV.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if daemonV.Field(i).Bool() {
			continue
		}
		if _, listed := pastDiffer[name]; listed {
			continue
		}
		t.Errorf("daemon oracle answers %s = false with no ledger entry anywhere — wire its seam in hubtest.WireCapabilitySeams, or record why every projection legitimately answers false", name)
	}
}

// A successful probe does not prove that an older daemon supports voice.
// Exercise the real websocket, roster, and source projections with all three
// wire representations; even an initialize feature claim cannot substitute for
// the original daemon's per-thread capability claim.
func TestCapabilityProjectionsPreserveDaemonVoiceAdvertisement(t *testing.T) {
	for _, state := range []string{appwire.ThreadStatusIdle, appwire.ThreadStatusActive, appwire.ThreadStatusClosed} {
		for _, tc := range []struct {
			name      string
			voiceJSON string
			wantVoice bool
		}{
			{name: "omitted"},
			{name: "explicit_false", voiceJSON: `,"voicePresentationContextV1":false`},
			{name: "explicit_true", voiceJSON: `,"voicePresentationContextV1":true`, wantVoice: true},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				const sessionID = "th_voice_evidence"
				// Raw JSON preserves explicit false, which a Go capability struct's
				// omitempty tag would collapse into the omitted case before transport.
				threadJSON := fmt.Sprintf(`{"id":%q,"sessionId":%q,"status":{"type":%q},"evener":{"capabilities":{"send":true,"queue":true%s}}}`, sessionID, sessionID, state, tc.voiceJSON)
				rpc := appserver.NewServer(appserver.ServerConfig{
					ServerName: "voice-capability-test", SourceID: "local",
					Features: appwire.FeatureSet{VoicePresentationContextV1: true},
				})
				appserver.HandleTyped(rpc.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (json.RawMessage, error) {
					return json.RawMessage(`{"data":[` + threadJSON + `]}`), nil
				})
				appserver.HandleTyped(rpc.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (json.RawMessage, error) {
					return json.RawMessage(`{"thread":` + threadJSON + `}`), nil
				})
				httpSrv := httptest.NewServer(http.HandlerFunc(rpc.ServeWebSocket))
				t.Cleanup(httpSrv.Close)
				t.Cleanup(func() { _ = rpc.Shutdown(context.Background()) })
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: 1, Protocol: appwire.ProtocolVersion,
					Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http"),
					SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				})
				roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{}).SetProcessAlive(func(int) bool { return true })
				if err := roster.RefreshAndWait(t.Context()); err != nil {
					t.Fatal(err)
				}
				live, ok := roster.Find(sessionID)
				if !ok || !live.CapabilitiesKnown || live.Status != state {
					t.Fatalf("roster entry = %+v, found=%v, want a successfully probed %s row", live, ok, state)
				}
				source, ok := newHubSourceRegistry(hubcore.WebConfig{Roster: roster}).Source("local")
				if !ok {
					t.Fatal("local source missing")
				}
				listed, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
				if err != nil {
					t.Fatal(err)
				}
				if len(listed.Data) != 1 || listed.Data[0].SessionID != sessionID || listed.Data[0].Status.Type != state {
					t.Fatalf("list rows = %+v, want the %s fixture row", listed.Data, state)
				}
				read, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
				if err != nil {
					t.Fatal(err)
				}
				if read.Thread.SessionID != sessionID || read.Thread.Status.Type != state {
					t.Fatalf("read thread = %+v, want the %s fixture row", read.Thread, state)
				}
				// Compare the entire sparse set, not just voice: falling back to
				// current-daemon defaults must not invent other capabilities either.
				want := appwire.ThreadCapabilities{VoicePresentationContextV1: tc.wantVoice, Send: true, Queue: true}
				for projection, got := range map[string]appwire.ThreadCapabilities{
					"roster": live.Capabilities,
					"list":   listed.Data[0].Evener.Capabilities,
					"read":   read.Thread.Evener.Capabilities,
				} {
					if got != want {
						t.Errorf("%s capabilities = %+v, want the original daemon's advertised set %+v", projection, got, want)
					}
				}
			})
		}
	}
}

func listRowsFromLocalDaemonSource(t *testing.T, entries func() []appsource.LocalDaemonEntry) []appwire.Thread {
	t.Helper()
	source := appsource.NewLocalDaemonSourceWithEntries("local", entries, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	return resp.Data
}
