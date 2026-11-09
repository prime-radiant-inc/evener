package hubcore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/rendezvous"
)

// TestProberFencesProtocolCurrentStaleSchema is the #4368 regression: the
// daemon passes the protocol handshake, so the old fence never fires, yet it
// advertises a transcript-index schema this hub cannot share. Without the
// schema check the probe proceeds to read a sidecar the daemon keeps
// re-staling, and the two binaries fight indefinitely instead of the session
// being shown as restart-required.
func TestProberFencesProtocolCurrentStaleSchema(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var request struct {
			ID any `json:"id"`
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		if err := wsjson.Write(r.Context(), conn, map[string]any{
			"id": request.ID,
			"result": map[string]any{
				"protocolVersion":         appwire.ProtocolVersion,
				"sourceId":                "local",
				"transcriptIndexSchemaId": strings.Repeat("0", 64),
			},
		}); err != nil {
			t.Errorf("write initialize: %v", err)
		}
	}))
	defer peer.Close()
	prober := &StatusProber{Timeout: 300 * time.Millisecond, client: peer.Client()}
	got := prober.Probe(rendezvous.Entry{
		SessionID: "session-schema", ThreadID: "session-schema",
		Protocol: appwire.ProtocolVersion,
		Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http") + "/rpc",
	})
	if !got.OK || got.Status != appwire.ThreadStatusRestartRequired || !got.SchemaMismatch || got.ProtocolMismatch {
		t.Fatalf("probe = %+v, want a restartRequired fence for the stale schema", got)
	}
}

// TestTranscriptSchemaMismatchTreatsAnAbsentAdvertisementAsUnknown keeps older
// daemons unfenced: a binary that predates the advertisement cannot be proven
// stale, and guessing would fence every session on a mixed-version host.
func TestTranscriptSchemaMismatchTreatsAnAbsentAdvertisementAsUnknown(t *testing.T) {
	if transcriptSchemaMismatch("") {
		t.Fatal("an absent schema advertisement was treated as a mismatch")
	}
	if transcriptSchemaMismatch(transcriptindex.SchemaID()) {
		t.Fatal("this hub's own schema was treated as a mismatch")
	}
	if !transcriptSchemaMismatch(strings.Repeat("0", 64)) {
		t.Fatal("a stale schema advertisement was not treated as a mismatch")
	}
}

// TestLiveEntryFromProbeCarriesTheRestartCause pins the diagnosis the fence's
// message reads: a schema mismatch must not be reported as a protocol
// mismatch.
func TestLiveEntryFromProbeCarriesTheRestartCause(t *testing.T) {
	if got := liveEntryFromProbe(rendezvous.Entry{}, ProbeResult{ProtocolMismatch: true}).RestartRequiredCause; got != "protocol" {
		t.Fatalf("protocol-mismatch cause = %q, want protocol", got)
	}
	if got := liveEntryFromProbe(rendezvous.Entry{}, ProbeResult{SchemaMismatch: true}).RestartRequiredCause; got != "schema" {
		t.Fatalf("schema-mismatch cause = %q, want schema", got)
	}
	if got := liveEntryFromProbe(rendezvous.Entry{}, ProbeResult{OK: true}).RestartRequiredCause; got != "" {
		t.Fatalf("compatible probe cause = %q, want empty", got)
	}
}
