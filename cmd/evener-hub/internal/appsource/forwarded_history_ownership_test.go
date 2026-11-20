package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A FORWARDED history read's ownership evidence belongs to the source that
// produced it, and this layer is the only one that knows the error crossed a
// hub boundary. A legacy remote hub stamps `daemonless` on a failed
// saved-history read whether or not a daemon still runs the thread - the
// `unknown-owner`/live-identity split is newer than the peer - and
// forwarding that token unchanged makes the browser read a failed read as a
// confirmed absence and clear a live hold the daemon will only publish again
// when it changes.
//
// The error travels as JSON, so its Data is the decoder's generic map rather
// than the typed struct. Asserting only to the typed shape missed the real
// payload entirely, which is why this test drives the whole remote call rather
// than the normalizer in isolation.
func TestRemoteHubReadForwardsLegacyHistoryAbsenceAsUnknownOwner(t *testing.T) {
	t.Parallel()
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadRead)
		}
		legacy := appwire.WireError{
			Code:    appwire.CodeInternalError,
			Message: "thread history failed",
			Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorTranscriptHistoryFailed},
		}
		// What an older peer actually puts on the wire: the daemonless stamp,
		// which it cannot qualify.
		stamped := appwire.WithHistoryReadIdentity(legacy, appwire.DaemonlessBootGeneration, 4)
		return scriptedReply{wireErr: &stamped}
	})
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"})
	if err == nil {
		t.Fatal("read of a failed history returned no error")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %T, want WireError", err)
	}
	// The identity the error carries is the assertion: a forwarded legacy
	// absence stamp is not a claim that nobody serves the thread.
	fields, ok := wire.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %T, want the decoder's map", wire.Data)
	}
	if fields["bootGeneration"] != appwire.UnknownOwnerBootGeneration {
		t.Fatalf("forwarded boot generation = %v, want %q", fields["bootGeneration"], appwire.UnknownOwnerBootGeneration)
	}
	// The diagnostic and its ordering metadata survive the rewrite.
	if fields["evenerErrorInfo"] != string(appwire.ErrorTranscriptHistoryFailed) {
		t.Fatalf("evenerErrorInfo = %v, want it preserved", fields["evenerErrorInfo"])
	}
	if epoch, ok := fields["epoch"].(float64); !ok || epoch != 4 {
		t.Fatalf("epoch = %v, want 4 preserved", fields["epoch"])
	}
}

// The other direction, over a hop: the forwarding layer normalizes at the
// remote boundary, so a peer that established the absence must say so or its
// real finding is demoted along with a legacy peer's blanket stamp. Without
// this the browser would keep a hold on a daemon that has exited, for as long
// as the saved history stays unreadable - and an unchanged hold emits no
// update of its own to correct it. The hub's own local stamp is pinned where
// that stamp is chosen (cmd/evener-hub's
// TestPastFallbackWithoutServingSourceStampsDaemonless).
func TestRemoteHubKeepsAttestedAbsence(t *testing.T) {
	t.Parallel()
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadRead)
		}
		failed := appwire.WireError{
			Code:    appwire.CodeInternalError,
			Message: "thread history failed",
			Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorTranscriptHistoryFailed},
		}
		// A CURRENT peer that established the absence: daemonless AND attested.
		attested := appwire.WithAttestedAbsence(failed, 6)
		return scriptedReply{wireErr: &attested}
	})
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"})
	if err == nil {
		t.Fatal("read of a failed history returned no error")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %T, want WireError", err)
	}
	fields, ok := wire.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %T, want the decoder's map", wire.Data)
	}
	// The peer's own finding survives the forwarding hop.
	if fields["bootGeneration"] != appwire.DaemonlessBootGeneration {
		t.Fatalf("forwarded attested boot generation = %v, want %q", fields["bootGeneration"], appwire.DaemonlessBootGeneration)
	}
	if epoch, ok := fields["epoch"].(float64); !ok || epoch != 6 {
		t.Fatalf("epoch = %v, want 6 preserved", fields["epoch"])
	}
}
