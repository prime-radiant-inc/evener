package hub

// The cross-projection parity oracle: a daemon wired exactly the way
// cmd/evener/serve.go wires it answers thread/read with its own capability
// set — the one answer every projection of that session is supposed to
// agree with (#1840: the same session must not read differently from
// ListThreads and from ThreadRead). This test reads that answer and requires
// the hub's hand-written projections to match it field by field.
//
// Every deliberate difference is enumerated in one ledger, with its reason,
// and every field NOT in the ledger must be true on the daemon side and equal
// on the projection side. A capability bit added to
// appwire.ThreadCapabilities therefore fails this test until the projection
// answers it and the ledger records the decision — the drift that shipped
// SkillInput and ChangeVisionModel understated (issue: skill selections
// aren't supported on this session yet) cannot recur silently.

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// daemonIdleCapabilities wires a server.Server with every seam
// appCapabilitiesLocked consults — the production shape of
// cmd/evener/serve.go — and returns the capability set it answers thread/read
// with at idle.
func daemonIdleCapabilities(t *testing.T) appwire.ThreadCapabilities {
	t.Helper()
	srv := daemonserver.NewServer(daemonserver.ServerConfig{})
	srv.SetAppIdentity("local", "th_parity_oracle")
	srv.SetState("idle")
	srv.SetRetrySafeTurnFunctions(daemonserver.RetrySafeTurnFunctions{
		Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, nil
		},
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
		Drain: func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			return appwire.TurnDrainAsSteerResponse{}, nil
		},
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return appwire.TurnCancelQueuedResponse{}, nil
		},
		Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return appwire.TurnInterruptResponse{}, nil
		},
	})
	srv.SetCompactFunc(func(context.Context) error { return nil })
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return nil })
	srv.SetShutdownFunc(func() {})
	srv.SetModelFunc(func(string) error { return nil })
	srv.SetVisionModelFunc(func(string) error { return nil })
	srv.SetNameFunc(func(string) error { return nil })
	srv.SetGoalFunc(func(string) (bool, error) { return false, nil })
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		return appwire.NotesHumanSetResponse{}, nil
	})
	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) { return false, nil })

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
// projection: a field either appears in the exception ledger — with a reason,
// and with the projections genuinely differing, or the ledger entry is stale
// and must go — or the projection must equal the daemon's answer, and the
// daemon's answer must be true (a false oracle-side bit would let a future
// un-wired capability pass here silently, since false == false).
func assertCapabilityParity(t *testing.T, projection string, daemon, got appwire.ThreadCapabilities, exceptions map[string]string) {
	t.Helper()
	daemonV, gotV := reflect.ValueOf(daemon), reflect.ValueOf(got)
	typ := daemonV.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		d, g := daemonV.Field(i).Bool(), gotV.Field(i).Bool()
		if reason, listed := exceptions[name]; listed {
			if d == g {
				t.Errorf("%s: %s is in the exception ledger (%s) but the projections agree (%v) — remove the stale entry", projection, name, reason, g)
			}
			continue
		}
		if d != g {
			t.Errorf("%s: %s = %v, but the daemon answers %v — update the projection, or record the exception with its reason", projection, name, g, d)
		}
		if !d {
			t.Errorf("%s: the daemon oracle answers %s = false; wire the oracle server's seam for it, or this test cannot force parity accounting for that bit", projection, name)
		}
	}
}

func TestCapabilityProjectionsMatchTheDaemonOracle(t *testing.T) {
	daemon := daemonIdleCapabilities(t)

	// The cold set a session with no daemon reads through: send resumes it,
	// and the hub's mutation gates re-verify every action against the daemon
	// a resume spawns. The turn actions are withheld because the hub cannot
	// carry them out with nothing there to take them.
	assertCapabilityParity(t, "pastThreadCapabilities", daemon, pastThreadCapabilities(), map[string]string{
		"Steer":        "no daemon is running to carry out a steer",
		"Interrupt":    "no daemon is running to interrupt",
		"Queue":        "no daemon is running to queue behind",
		"ForkFromTurn": "the hub's own operation; the daemon hardwires false and applyHubForkCapability owns the bit",
	})

	// The unprobed list-row fallback approximates the daemon's expected idle
	// answer for rows no probe has confirmed yet (probed rows mirror the
	// daemon directly, the test in appsource pins that path).
	fallbackRows := listRowsFromLocalDaemonSource(t, func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:  rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/unprobed", ThreadID: "th_unprobed", SessionID: "sess_unprobed"},
			Status: "idle",
		}}
	})
	if len(fallbackRows) != 1 {
		t.Fatalf("list rows = %+v, want the one unprobed fixture row", fallbackRows)
	}
	assertCapabilityParity(t, "unprobed list row", daemon, fallbackRows[0].Evener.Capabilities, map[string]string{
		"ForkFromTurn": "the hub's own operation; every hub list path re-fences the bit through applyHubForkCapability",
	})

	// The probe-mirrored row is the same daemon answer flowing back through
	// the roster plumbing, so it must render as the oracle's own set — the
	// fork overlay aside.
	probedRows := listRowsFromLocalDaemonSource(t, func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/probed", ThreadID: "th_probed", SessionID: "sess_probed"},
			Status:            "idle",
			Capabilities:      daemon,
			CapabilitiesKnown: true,
		}}
	})
	if len(probedRows) != 1 {
		t.Fatalf("list rows = %+v, want the one probed fixture row", probedRows)
	}
	assertCapabilityParity(t, "probed list row", daemon, probedRows[0].Evener.Capabilities, map[string]string{
		"ForkFromTurn": "the hub's own operation; every hub list path re-fences the bit through applyHubForkCapability",
	})
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
